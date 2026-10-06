using MailCat.Categories;
using MailCat.Emails;
using MailCat.Features;

namespace MailCat.Classification;

/// <summary>Confidence band of a result (§15).</summary>
public enum Level { High, Medium, Low }

/// <summary>The configurable decision limits (§15).</summary>
public sealed record Thresholds(double High, double Medium, double MinMargin)
{
    /// <summary>The values suggested by the spec plus a margin guard.</summary>
    public static Thresholds Default => new(0.85, 0.60, 0.20);

    /// <summary>Maps a result to a confidence band. Unknown is never high/medium.</summary>
    public Level LevelOf(Result r)
    {
        if (r.Best.Category == CategoryTree.UnknownId) return Level.Low;
        if (r.Best.Score >= High && r.Margin >= MinMargin) return Level.High;
        if (r.Best.Score >= Medium) return Level.Medium;
        return Level.Low;
    }
}

/// <summary>The outcome of one classification pass.</summary>
public sealed class Result
{
    public List<Candidate> Candidates { get; init; } = [];
    public Candidate Best { get; init; } = new(CategoryTree.UnknownId, 0);
    public double Margin { get; init; }
    public Level Level { get; set; }
    public string Method { get; set; } = Classification.Method.Combined;
    public List<Evidence> Evidence { get; init; } = [];
}

/// <summary>Orchestrates signals + aggregation.</summary>
public sealed class Classifier(CategoryTree tree, IEnumerable<ISignal> signals, Thresholds thresholds)
{
    public CategoryTree Tree { get; } = tree;
    public Thresholds Thresholds { get; } = thresholds;
    readonly List<ISignal> signals = signals.ToList();

    /// <summary>Gathers evidence from every signal and aggregates it.</summary>
    public Result Classify(Email e, FeatureSet f)
    {
        var ev = new List<Evidence>();
        foreach (var s in signals) ev.AddRange(s.Evidence(e, f));
        return FromEvidence(ev);
    }

    /// <summary>Aggregates already-collected evidence (used to rebuild a session from its frozen evidence).</summary>
    public Result FromEvidence(List<Evidence> ev) => Build(Aggregator.Aggregate(Tree, ev), ev);

    /// <summary>Packages a (possibly Bayes-updated) candidate list into a Result.</summary>
    public Result Build(List<Candidate> cands, List<Evidence> ev)
    {
        var best = cands.Count > 0 ? cands[0] : new Candidate(CategoryTree.UnknownId, 0);
        double margin = cands.Count > 1 ? cands[0].Score - cands[1].Score : best.Score;
        var r = new Result { Candidates = cands, Evidence = ev, Best = best, Margin = margin };
        r.Level = Thresholds.LevelOf(r);
        r.Method = DominantMethod(Tree, best.Category, ev);
        return r;
    }

    /// <summary>Evidence that supports category id: evidence on id, its ancestors or descendants, strongest first.</summary>
    public static List<Evidence> Relevant(CategoryTree tree, IEnumerable<Evidence> ev, string id) =>
        ev.Where(e => e.Weight > 0 && (e.Category == id || tree.IsAncestor(e.Category, id) || tree.IsAncestor(id, e.Category)))
            .OrderByDescending(e => e.Weight).ToList(); // OrderBy is stable

    /// <summary>The single method behind a category's support, or "combined" when several kinds of signal contributed.</summary>
    public static string DominantMethod(CategoryTree tree, string id, IEnumerable<Evidence> ev)
    {
        var rel = Relevant(tree, ev, id);
        var seen = rel.Select(e => e.Method).ToHashSet();
        return seen.Count == 1 ? rel[^1].Method : Method.Combined;
    }
}
