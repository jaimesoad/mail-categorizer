using MailCat.Categories;
using MailCat.Classification;
using MailCat.Features;

namespace MailCat.Questions;

/// <summary>Supplies learned per-question statistics (storage implements it).</summary>
public interface IQuestionStatsProvider
{
    Dictionary<string, QuestionStats> QuestionStats();
}

/// <summary>Tunables of the selector (§13).</summary>
public sealed class SelectorParams
{
    /// <summary>Bits; questions below this are never asked.</summary>
    public double MinGain { get; set; } = 0.05;
    /// <summary>Skip questions whose feature is already known at least this well.</summary>
    public double KnownConfidence { get; set; } = 0.9;
    /// <summary>Candidates considered for the entropy computation.</summary>
    public int MaxCandidates { get; set; } = 8;
    /// <summary>Per already-asked question of the same feature family.</summary>
    public double RepetitionPenalty { get; set; } = 0.02;
    /// <summary>Words allowed before the complexity penalty.</summary>
    public int WordPenaltyFree { get; set; } = 12;
    /// <summary>Per extra word.</summary>
    public double WordPenalty { get; set; } = 0.002;
    /// <summary>Pseudo-counts for the response-rate estimate (answered).</summary>
    public double ResponsePriorAnswer { get; set; } = 4;
    /// <summary>Pseudo-counts for the response-rate estimate (shown).</summary>
    public double ResponsePriorShown { get; set; } = 5;
}

/// <summary>A question together with how it scored.</summary>
public sealed record Scored(Question Question, double Gain, double Utility);

/// <summary>The part of a classification session the selector needs.</summary>
public sealed class SelectorSession
{
    public List<Candidate> Candidates { get; init; } = [];
    public FeatureSet? Features { get; init; }
    /// <summary>Question ids already asked (and answered or skipped).</summary>
    public List<string> Asked { get; init; } = [];
}

/// <summary>Picks the next question: the one that best separates the current candidates, discounted by practicalities (§12, §13).</summary>
public sealed class QuestionSelector(CategoryTree tree, IQuestionStatsProvider? stats, SelectorParams? @params = null)
{
    public CategoryTree Tree { get; } = tree;
    public List<Question> Questions { get; set; } = [];
    public SelectorParams Params { get; } = @params ?? new SelectorParams();

    /// <summary>The best question or null when none is worth asking.</summary>
    public Scored? Select(SelectorSession ss) => Rank(ss).FirstOrDefault();

    /// <summary>Scores every eligible question, best first (deterministic order).</summary>
    public List<Scored> Rank(SelectorSession ss)
    {
        var stat = stats?.QuestionStats() ?? [];
        var cands = TopCandidates(ss.Candidates, Params.MaxCandidates);
        if (cands.Count < 2) return [];

        var asked = ss.Asked.ToHashSet();
        var families = new Dictionary<string, int>();
        foreach (var id in ss.Asked)
        {
            foreach (var q in Questions.Where(q => q.Id == id))
                families[Family(q.Feature)] = families.GetValueOrDefault(Family(q.Feature)) + 1;
        }

        var o = new List<Scored>();
        foreach (var q in Questions)
        {
            if (q.Disabled || !q.Validated || !q.Selectable || asked.Contains(q.Id)) continue;
            // "ask only what you don't know": skip if the feature is already known well.
            if (ss.Features != null && ss.Features.Confidence(q.Feature) >= Params.KnownConfidence) continue;

            var st = stat.GetValueOrDefault(q.Id) ?? new QuestionStats();
            var m = new QuestionModel(q, Tree, st);
            double gain = m.InformationGain(cands);
            if (gain < Params.MinGain) continue;

            double response = (st.Answered + Params.ResponsePriorAnswer) / (st.Shown + Params.ResponsePriorShown);
            double confQ = q.Confidence * Math.Min(1, response);
            double rel = q.Reliability == 0 ? 0.95 : q.Reliability;
            double rep = Params.RepetitionPenalty * families.GetValueOrDefault(Family(q.Feature));
            int words = q.Text.Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries).Length;
            double cplx = q.Complexity;
            int extra = words - Params.WordPenaltyFree;
            if (extra > 0) cplx += extra * Params.WordPenalty;
            double util = gain * confQ * rel - rep - cplx;
            if (util <= 0) continue;
            o.Add(new Scored(q, gain, util));
        }
        return o.OrderByDescending(s => s.Utility).ThenBy(s => s.Question.Id, StringComparer.Ordinal).ToList();
    }

    static string Family(string feature)
    {
        int i = feature.LastIndexOf('.');
        return i > 0 ? feature[..i] : feature;
    }

    static List<Candidate> TopCandidates(List<Candidate> input, int n)
    {
        var o = new List<Candidate>(n);
        foreach (var c in input)
        {
            if (c.Score < 0.01) break; // sorted desc
            o.Add(c);
            if (o.Count == n) break;
        }
        return Aggregator.Normalize(o);
    }
}
