using MailCat.Categories;
using MailCat.Classification;
using MailCat.Emails;
using MailCat.Explain;
using MailCat.Features;
using MailCat.Questions;
using MailCat.Storage;

namespace MailCat.Engine;

public sealed partial class MailCatEngine
{
    /// <summary>
    /// Runs the full pipeline for one email (§26): normalize → features → signals →
    /// classification → decide (classify automatically, or open a session with a question).
    /// Processing the same message twice is idempotent.
    /// </summary>
    public Outcome Process(Email raw)
    {
        if (raw.From.Email == "" && raw.Subject == "" && raw.BodyText == "")
            throw new InvalidRequestException("email has no sender, subject or body");
        var em = EmailNormalizer.Normalize(raw, cfg.Privacy.MaxBodyChars);

        if (db.EmailIdByHash(em.RawHash) is { } known) return AlreadyProcessed(known);

        // Extract before inserting so history features describe the past only.
        var set = new FeatureSet(extractor.Extract(em));
        var stored = cfg.Privacy.StoreBody ? em : em with { BodyText = "" };
        var (id, existed) = db.InsertEmail(stored, set.All(), now());
        if (existed) return AlreadyProcessed(id);
        em = em with { Id = id };

        var res = cls.Classify(em, set);
        var st = new SessionState { Evidence = res.Evidence, Base = res.Candidates, Candidates = res.Candidates };
        Advance(st, set);
        st.Initial = st.Decision;
        Persist(id, st);
        return OutcomeOf(id, st);
    }

    Outcome AlreadyProcessed(string id)
    {
        var o = Get(id);
        o.AlreadyProcessed = true;
        return o;
    }

    /// <summary>
    /// Decides what happens next for a session: finish with a category or pick the next
    /// question. It is the single place where thresholds, question limits and the
    /// unknown/ancestor fallbacks (§15, §31, §41) are applied.
    /// </summary>
    void Advance(SessionState st, FeatureSet set)
    {
        var res = cls.Build(st.Candidates, st.Evidence);
        st.Pending = null;
        if (res.Level == Level.High)
        {
            Finalize(st, Decisions.Know, res.Best.Category, res.Best.Score, st.Answered() > 0 ? Resolved.Answers : Resolved.Auto);
            return;
        }
        if (st.Asked.Count < cfg.MaxQuestionsPerEmail)
        {
            var q = NextQuestion(st, set);
            if (q != null)
            {
                st.Pending = q;
                st.Decision = Decisions.Ask;
                st.Final = res.Best.Category;
                st.FinalScore = res.Best.Score;
                st.ResolvedBy = "";
                st.Method = res.Method;
                return;
            }
        }
        // No (more) questions: be explicit about how sure we are.
        if (res.Level == Level.Medium)
        {
            Finalize(st, Decisions.Predict, res.Best.Category, res.Best.Score, Resolved.Predicted);
            return;
        }
        if (AncestorFallback(res) is var (cat, mass))
        {
            Finalize(st, mass >= cfg.Thresholds.Auto ? Decisions.Know : Decisions.Predict, cat, mass, Resolved.Ancestor);
            return;
        }
        // score of "unknown" = how little we believe in the best alternative
        Finalize(st, Decisions.Unknown, CategoryTree.UnknownId, 1 - res.Best.Score, Resolved.Unknown);
    }

    /// <summary>
    /// (M6) When the best leaf is undecidable, the deepest node on its path (itself included,
    /// with its descendants) whose subtree mass is solid is still useful ("it is Finance, not sure which").
    /// </summary>
    (string Category, double Mass)? AncestorFallback(Result res)
    {
        var best = res.Best.Category;
        if (best == CategoryTree.UnknownId || !tree.Has(best)) return null;
        foreach (var node in new[] { best }.Concat(tree.Ancestors(best))) // deepest first
        {
            double m = Aggregator.SubtreeMass(tree, res.Candidates, node);
            if (m >= cfg.Thresholds.Ask) return (node, m);
        }
        return null;
    }

    void Finalize(SessionState st, string decision, string cat, double score, string by)
    {
        st.Decision = decision;
        st.Final = cat;
        st.FinalScore = score;
        st.ResolvedBy = by;
        st.Pending = null;
        st.Method = Classifier.DominantMethod(tree, cat, st.Evidence);
        if (st.Answered() > 0 && by == Resolved.Answers) st.Method = Method.Combined;
    }

    /// <summary>The most useful unasked question.</summary>
    PendingRec? NextQuestion(SessionState st, FeatureSet set)
    {
        var sc = sel.Select(new SelectorSession { Candidates = st.Candidates, Features = set, Asked = st.Asked });
        return sc == null ? null : new PendingRec { QuestionId = sc.Question.Id, ExpectedGain = sc.Gain, Utility = sc.Utility };
    }

    /// <summary>Finds an active, validated catalogue question.</summary>
    Question? QuestionById(string id) =>
        sel.Questions.FirstOrDefault(q => q.Id == id && !q.Disabled && q.Validated);

    /// <summary>Writes classification rows and the session.</summary>
    void Persist(string emailId, SessionState st)
    {
        var at = now();
        string status = st.Decision == Decisions.Ask ? "open" : "resolved";
        var expl = Json.Serialize(Explain(st));
        var rows = new List<StoredClassification>();
        var final = st.Final;
        bool hasFinal = false;
        for (int i = 0; i < st.Candidates.Count; i++)
        {
            var c = st.Candidates[i];
            if (i >= 5 || (c.Score < 0.01 && c.Category != final)) break;
            var r = new StoredClassification
            {
                EmailId = emailId, CategoryId = c.Category, Score = c.Score,
                Method = Classifier.DominantMethod(tree, c.Category, st.Evidence),
            };
            if (status == "resolved" && c.Category == final)
            {
                r.Final = true;
                r.Method = st.Method;
                r.Explanation = expl;
                r.Score = st.FinalScore;
                hasFinal = true;
            }
            rows.Add(r);
        }
        if (status == "resolved" && !hasFinal)
        {
            rows.Add(new StoredClassification
            {
                EmailId = emailId, CategoryId = final, Score = st.FinalScore, Method = st.Method, Explanation = expl, Final = true,
            });
        }
        if (status == "open" && rows.Count > 0) rows[0].Explanation = expl;
        db.SaveClassification(emailId, rows, at);

        var row = new SessionRow
        {
            EmailId = emailId, Status = status, InitialDecision = st.Initial, QuestionsAsked = st.Asked.Count,
            FinalCategory = final, ResolvedBy = st.ResolvedBy, State = st.Marshal(),
        };
        try { row.Id = db.GetSession(emailId).Id; } catch (NotFoundException) { }
        if (status == "open")
        {
            row.FinalCategory = "";
            row.ResolvedBy = "";
        }
        db.SaveSession(row, at);
    }

    Explanation Explain(SessionState st)
    {
        var res = cls.Build(st.Candidates, st.Evidence);
        var qa = new List<QA>();
        foreach (var a in st.Answers)
        {
            if (a.Skipped) continue;
            if (QuestionById(a.QuestionId) is { } q) qa.Add(new QA(q.Text, AnswerLabel(q, a.Answer)));
        }
        var x = Explanation.Build(new ExplainInput
        {
            Tree = tree, Result = res, Decision = st.Decision, Category = st.Final, Answers = qa,
        });
        if (st.Decision != Decisions.User && st.FinalScore > 0) x.Score = (int)(st.FinalScore * 100 + 0.5) / 100.0;
        return x;
    }

    static string AnswerLabel(Question q, string ans) =>
        string.Join(", ", ans.Split(',').Select(id => q.Answers.FirstOrDefault(a => a.Id == id)?.Label ?? id));

    /// <summary>The public view of a session.</summary>
    Outcome OutcomeOf(string emailId, SessionState st)
    {
        var row = db.GetSession(emailId);
        var o = new Outcome
        {
            EmailId = emailId, SessionId = row.Id, Decision = st.Decision, Category = st.Final, Path = tree.Path(st.Final),
            Score = st.FinalScore, Method = st.Method, Resolved = st.Decision != Decisions.Ask, Candidates = st.Candidates,
            QuestionsAsked = st.Asked.Count, Explanation = Explain(st),
        };
        switch (st.Decision)
        {
            case Decisions.Ask:
                o.NeedsQuestion = true;
                if (st.Pending != null && QuestionById(st.Pending.QuestionId) is { } q)
                {
                    o.Question = new PendingQuestion
                    {
                        Id = q.Id, Text = q.Text, Type = q.Type, Feature = q.Feature, Answers = q.Answers,
                        InformationGain = Round3(st.Pending.ExpectedGain), Utility = Round3(st.Pending.Utility),
                    };
                }
                break;
            case Decisions.Unknown:
                o.NeedsManual = true;
                break;
            case Decisions.Predict:
                o.RequestConfirmation = true;
                break;
            case Decisions.Know:
                o.RequestConfirmation = st.Answered() == 0 && ShouldConfirm(emailId, st.FinalScore);
                break;
        }
        return o;
    }

    static double Round3(double f) => (int)(f * 1000 + 0.5) / 1000.0;

    SessionState LoadSession(string emailId) => Json.Deserialize<SessionState>(db.GetSession(emailId).State);

    /// <summary>The current outcome of an already processed email.</summary>
    public Outcome Get(string emailId) => OutcomeOf(emailId, LoadSession(emailId));

    /// <summary>The structured explanation of the current classification.</summary>
    public Explanation ExplainEmail(string emailId) => Explain(LoadSession(emailId));
}
