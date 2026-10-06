using MailCat.Categories;
using MailCat.Classification;
using MailCat.Emails;
using MailCat.Explain;
using MailCat.Features;
using MailCat.Learning;
using MailCat.Questions;
using MailCat.Storage;

namespace MailCat.Engine;

public sealed partial class MailCatEngine
{
    /// <summary>
    /// Records the user's answer to a question and advances the session (§12–§13, §17): the
    /// answer becomes a user-sourced feature, updates the candidates by Bayes' rule and, if
    /// it settles the matter, is learned.
    /// </summary>
    public Outcome Answer(string emailId, string questionId, string raw)
    {
        var st = LoadSession(emailId);
        if (st.Decision is Decisions.Know or Decisions.User)
            throw new ConflictException("session already resolved");
        var q = QuestionById(questionId) ?? throw new InvalidRequestException($"unknown or disabled question \"{questionId}\"");
        if (st.Answers.Any(a => a.QuestionId == questionId))
            throw new ConflictException($"question \"{questionId}\" was already answered or skipped");

        var ids = q.ResolveAnswer(raw);

        // "category" questions are manual labelling.
        if (q.Type == QuestionType.Category)
        {
            CheckLabelable(ids[0]);
            return Label(emailId, st, ids[0]);
        }

        var em = db.GetEmail(emailId);
        var fvals = ids.Select(q.FeatureValueOf).ToList();
        var rec = new AnswerRec { QuestionId = questionId, Answer = string.Join(',', ids), FeatureValue = string.Join(',', fvals) };

        double before = Aggregator.Entropy(st.Candidates);
        string prevBest = st.Candidates[0].Category;
        double expected = st.Pending != null && st.Pending.QuestionId == questionId ? st.Pending.ExpectedGain : 0;

        // the answer is knowledge about the email
        db.AddFeatures(emailId, [.. fvals.Select(v => new Feature(q.Feature, v, 1, FeatureSource.User))]);
        var set = db.Features(emailId);

        // rules that reference the answered feature may fire now: freeze new evidence only
        st.Evidence = MergeRuleEvidence(st.Evidence, em, set);
        st.Asked.Add(questionId);
        st.Answers.Add(rec);
        Replay(st);
        rec.Gain = before - Aggregator.Entropy(st.Candidates);
        Advance(st, set);
        Persist(emailId, st);

        db.AddInteraction(new Interaction
        {
            EmailId = emailId, SessionId = SessionIdOf(emailId), Kind = InteractionKind.Answer, QuestionId = questionId,
            Answer = rec.Answer, PreviousPrediction = prevBest, ResultingCategory = st.Candidates[0].Category,
            ExpectedGain = expected, RealizedGain = rec.Gain,
        }, now());

        bool learned = false;
        if (st.Decision == Decisions.Know && st.ResolvedBy == Resolved.Answers)
            learned = Learn(emailId, set, st.Final, "", FeedbackKind.Answer);

        var o = OutcomeOf(emailId, st);
        o.Learned = learned;
        return o;
    }

    /// <summary>
    /// Records that the user declined to answer a question (it will not be asked again for
    /// this email, and lowers its response rate, §2.2).
    /// </summary>
    public Outcome Skip(string emailId, string questionId)
    {
        var st = LoadSession(emailId);
        if (st.Decision != Decisions.Ask) throw new ConflictException("no open question to skip");
        if (QuestionById(questionId) == null) throw new InvalidRequestException($"unknown question \"{questionId}\"");
        if (st.Answers.Any(a => a.QuestionId == questionId))
            throw new ConflictException($"question \"{questionId}\" was already answered or skipped");

        var set = db.Features(emailId);
        st.Asked.Add(questionId);
        st.Answers.Add(new AnswerRec { QuestionId = questionId, Skipped = true });
        Advance(st, set);
        Persist(emailId, st);
        db.AddInteraction(new Interaction
        {
            EmailId = emailId, SessionId = SessionIdOf(emailId), Kind = InteractionKind.Skip, QuestionId = questionId,
        }, now());
        return OutcomeOf(emailId, st);
    }

    /// <summary>
    /// Rebuilds the candidates from the frozen evidence and re-applies every answer in
    /// order. Using one code path for it keeps sessions reproducible.
    /// </summary>
    void Replay(SessionState st)
    {
        var stats = db.QuestionStats();
        var cands = Aggregator.Aggregate(tree, st.Evidence);
        st.Base = cands;
        foreach (var a in st.Answers)
        {
            if (a.Skipped) continue;
            var q = QuestionById(a.QuestionId);
            if (q == null || !q.Selectable) continue;
            var m = new QuestionModel(q, tree, stats.GetValueOrDefault(q.Id));
            cands = m.Posterior(cands, a.Answer);
        }
        st.Candidates = cands;
    }

    /// <summary>Appends evidence of rules that fire only now (because an answer introduced a feature they reference) without touching the frozen rest.</summary>
    List<Evidence> MergeRuleEvidence(List<Evidence> ev, Email em, FeatureSet set)
    {
        var have = ev.Where(x => x.Method == Method.Rule).Select(x => x.Ref).ToHashSet(StringComparer.Ordinal);
        var merged = new List<Evidence>(ev);
        foreach (var x in built.Rules.Evidence(em, set))
        {
            if (!have.Contains(x.Ref)) merged.Add(x);
        }
        return merged;
    }

    string SessionIdOf(string emailId)
    {
        try { return db.GetSession(emailId).Id; }
        catch (NotFoundException) { return ""; }
    }

    bool Learn(string emailId, FeatureSet set, string cat, string prev, FeedbackKind kind)
    {
        if (learner == null || cat == "" || cat == CategoryTree.UnknownId) return false;
        learner.Learn(new Feedback(emailId, set, cat, prev, kind));
        return true;
    }
}
