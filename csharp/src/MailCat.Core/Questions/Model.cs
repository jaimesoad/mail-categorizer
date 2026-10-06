using MailCat.Categories;
using MailCat.Classification;

namespace MailCat.Questions;

/// <summary>The learned evidence about one question, supplied by storage.</summary>
public sealed class QuestionStats
{
    /// <summary>Times it was presented.</summary>
    public int Shown { get; set; }
    /// <summary>Times the user answered (not skipped).</summary>
    public int Answered { get; set; }
    /// <summary>category → answer id → count (user-confirmed labels only).</summary>
    public Dictionary<string, Dictionary<string, double>> Counts { get; set; } = [];
}

/// <summary>
/// Gives P(answer | category) for one question by blending the configured affinity (cold
/// start) with counts from user-confirmed history.
/// </summary>
public sealed class QuestionModel(Question q, CategoryTree tree, QuestionStats? stats = null)
{
    /// <summary>Pseudo-count weight of the configured affinity versus real observations.</summary>
    const double PriorStrength = 3.0;

    public Question Q { get; } = q;
    readonly CategoryTree tree = tree;
    readonly QuestionStats stats = stats ?? new QuestionStats();
    readonly Dictionary<string, Dictionary<string, double>> cache = new(StringComparer.Ordinal);

    /// <summary>The configured P(a|c) inheriting from ancestors; null when nothing is configured (uninformative).</summary>
    Dictionary<string, double>? Prior(string cat)
    {
        for (var cur = cat; cur != "";)
        {
            if (Q.Affinity.TryGetValue(cur, out var row)) return NormalizeRow(row);
            var c = tree.Get(cur);
            if (c == null) break;
            cur = c.ParentId;
        }
        return null;
    }

    /// <summary>Turns partial rows (e.g. only "yes") into a full distribution.</summary>
    Dictionary<string, double> NormalizeRow(Dictionary<string, double> row)
    {
        var o = new Dictionary<string, double>();
        double sum = 0;
        int missing = 0;
        foreach (var a in Q.Answers)
        {
            if (row.TryGetValue(a.Id, out var p)) { o[a.Id] = p; sum += p; }
            else missing++;
        }
        if (missing > 0)
        {
            double rest = Math.Max(0, 1 - sum);
            foreach (var a in Q.Answers)
            {
                if (!o.ContainsKey(a.Id)) o[a.Id] = rest / missing;
            }
            return o;
        }
        if (sum > 0 && (sum < 0.999 || sum > 1.001))
        {
            foreach (var k in o.Keys.ToList()) o[k] /= sum;
        }
        return o;
    }

    /// <summary>
    /// The (raw, unmixed) distribution over answers for a category. "unknown" is the mass of
    /// categories we have no evidence for, so its answer distribution is the average over all
    /// leaf categories. That way a decisive answer shrinks the unknown mass instead of
    /// leaving it untouched.
    /// </summary>
    public Dictionary<string, double> Dist(string cat)
    {
        if (cache.TryGetValue(cat, out var d)) return d;
        d = cat == CategoryTree.UnknownId ? UnknownDist() : DistOf(cat);
        cache[cat] = d;
        return d;
    }

    Dictionary<string, double> UnknownDist()
    {
        var o = new Dictionary<string, double>();
        int n = 0;
        foreach (var c in tree.All())
        {
            if (c.Id == CategoryTree.UnknownId || !tree.Enabled(c.Id) || tree.Children(c.Id).Count > 0) continue;
            n++;
            foreach (var (a, p) in DistOf(c.Id)) o[a] = o.GetValueOrDefault(a) + p;
        }
        if (n == 0)
        {
            foreach (var a in Q.Answers) o[a.Id] = 1.0 / Q.Answers.Count;
            return o;
        }
        foreach (var k in o.Keys.ToList()) o[k] /= n;
        return o;
    }

    Dictionary<string, double> DistOf(string cat)
    {
        double n = Q.Answers.Count;
        var prior = Prior(cat);
        stats.Counts.TryGetValue(cat, out var counts);
        double total = counts?.Values.Sum() ?? 0;
        var o = new Dictionary<string, double>(Q.Answers.Count);
        foreach (var a in Q.Answers)
        {
            double p0 = prior != null ? prior.GetValueOrDefault(a.Id) : 1 / n;
            o[a.Id] = ((counts?.GetValueOrDefault(a.Id) ?? 0) + PriorStrength * p0) / (total + PriorStrength);
        }
        return o;
    }

    /// <summary>P(answer | category) damped by the question's reliability: a user may mis-click, so no answer ever fully rules a category out.</summary>
    public double Likelihood(string cat, string answer)
    {
        double r = Q.Reliability == 0 ? 0.95 : Q.Reliability;
        double n = Q.Answers.Count;
        return r * Dist(cat).GetValueOrDefault(answer) + (1 - r) / n;
    }

    /// <summary>H(before) − E_answer[H(after)] in bits (§12), computed with the raw P(answer | category).</summary>
    public double InformationGain(List<Candidate> cands)
    {
        double before = Aggregator.Entropy(cands);
        double expectedAfter = 0;
        foreach (var a in Q.Answers)
        {
            double pa = 0;
            foreach (var c in cands) pa += c.Score * Dist(c.Category).GetValueOrDefault(a.Id);
            if (pa <= 0) continue;
            var post = Aggregator.Update(cands, cat => Dist(cat).GetValueOrDefault(a.Id));
            expectedAfter += pa * Aggregator.Entropy(post);
        }
        return Math.Max(0, before - expectedAfter);
    }

    /// <summary>Applies the user's answer to the candidates (Bayes), using the reliability-damped likelihood.</summary>
    public List<Candidate> Posterior(List<Candidate> cands, string answerId) =>
        Aggregator.Update(cands, cat => Likelihood(cat, answerId));
}
