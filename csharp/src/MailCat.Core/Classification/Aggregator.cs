using MailCat.Categories;

namespace MailCat.Classification;

/// <summary>
/// A possible outcome with its probability-like mass. Scores of a distribution sum to 1
/// and the residual is assigned to <see cref="CategoryTree.UnknownId"/>, which makes
/// "I don't know" a first-class outcome (§41). A Candidate whose Category is an inner
/// node means "belongs there, not refined further".
/// </summary>
public sealed record Candidate(string Category, double Score);

public static class Aggregator
{
    const double MaxWeight = 0.999;
    const double MaxEvidence = 0.9999;
    const double Epsilon = 1e-9;

    /// <summary>
    /// Turns evidence into a candidate distribution.
    /// <list type="number">
    /// <item>Per category, positive weights combine with noisy-OR (independent supporting
    /// signals); negative weights multiply by (1-|w|) (suppression).</item>
    /// <item>Evidence accumulates up the tree: E(n) = supp(n) · noisyOR(pos(n), E(children)).</item>
    /// <item>Roots compete as hypotheses against a null "unknown" hypothesis of odds 1:
    /// P(root) = o/(1+Σo) with o = E/(1-E). A lone category keeps P = E; strong evidence beats
    /// weak evidence instead of merely sharing the mass with it; conflicting equal evidence
    /// leaves a large unknown share.</item>
    /// <item>Going down, the children of n receive the part of P(n) that child evidence
    /// explains (coverage = noisyOR(E(children)) / E(n), at most 1), split by their odds.
    /// What is not explained stays at n ("n, not refined"). Evidence aimed at a parent
    /// therefore never fights its own children.</item>
    /// </list>
    /// </summary>
    public static List<Candidate> Aggregate(CategoryTree tree, IEnumerable<Evidence> ev)
    {
        var pos = new Dictionary<string, double>(StringComparer.Ordinal);  // product of (1-w) over positive weights
        var supp = new Dictionary<string, double>(StringComparer.Ordinal); // product of (1-|w|) over negative weights
        foreach (var e in ev)
        {
            if (e.Category == CategoryTree.UnknownId || !tree.Has(e.Category) || !tree.Enabled(e.Category)) continue;
            double w = Math.Max(-MaxWeight, Math.Min(MaxWeight, e.Weight));
            if (w > 0) pos[e.Category] = pos.GetValueOrDefault(e.Category, 1) * (1 - w);
            else if (w < 0) supp[e.Category] = supp.GetValueOrDefault(e.Category, 1) * (1 - -w);
        }

        var E = new Dictionary<string, double>(StringComparer.Ordinal);  // subtree evidence
        var EC = new Dictionary<string, double>(StringComparer.Ordinal); // evidence explained by the children alone

        double Subtree(string id)
        {
            double kids = 1.0;
            foreach (var c in tree.Children(id)) kids *= 1 - Subtree(c);
            double own = pos.TryGetValue(id, out var p) ? p : 1.0;
            double v = 1 - own * kids;
            if (supp.TryGetValue(id, out var s)) v *= s;
            E[id] = Math.Min(v, MaxEvidence);
            EC[id] = 1 - kids;
            return E[id];
        }

        static double Odds(double e) => e / (1 - e);

        var roots = tree.Roots();
        double z = 1.0;
        foreach (var r in roots) z += Odds(Subtree(r));

        var output = new List<Candidate>();

        void Expand(string id, double p)
        {
            if (p < Epsilon) return;
            var kids = tree.Children(id);
            if (kids.Count == 0 || EC[id] < Epsilon || E[id] < Epsilon)
            {
                output.Add(new Candidate(id, p));
                return;
            }
            double cover = Math.Min(1, EC[id] / E[id]);
            double sumOdds = 0;
            foreach (var c in kids) sumOdds += Odds(E[c]);
            double rest = p;
            foreach (var c in kids)
            {
                double share = p * cover * Odds(E[c]) / sumOdds;
                rest -= share;
                Expand(c, share);
            }
            if (rest > Epsilon) output.Add(new Candidate(id, rest));
        }

        double total = 0;
        foreach (var r in roots)
        {
            double p = Odds(E[r]) / z;
            total += p;
            Expand(r, p);
        }
        double u = 1 - total;
        if (u > Epsilon) output.Add(new Candidate(CategoryTree.UnknownId, u));
        return Normalize(output);
    }

    /// <summary>Sorts candidates by score (desc, id asc), drops dust and rescales so the scores sum to exactly 1.</summary>
    public static List<Candidate> Normalize(IEnumerable<Candidate> input)
    {
        double sum = 0;
        var o = new List<Candidate>();
        foreach (var c in input)
        {
            if (c.Score > Epsilon)
            {
                o.Add(c);
                sum += c.Score;
            }
        }
        if (sum == 0) return [new Candidate(CategoryTree.UnknownId, 1)];
        return o.Select(c => c with { Score = c.Score / sum })
            .OrderByDescending(c => c.Score).ThenBy(c => c.Category, StringComparer.Ordinal).ToList();
    }

    /// <summary>Bayesian update: p'(c) ∝ p(c)·likelihood(c). Used after the user answers a question.</summary>
    public static List<Candidate> Update(IEnumerable<Candidate> cands, Func<string, double> likelihood) =>
        Normalize(cands.Select(c => new Candidate(c.Category, c.Score * likelihood(c.Category))));

    /// <summary>Shannon entropy in bits of a distribution.</summary>
    public static double Entropy(IEnumerable<Candidate> cands)
    {
        double h = 0;
        foreach (var c in cands)
        {
            if (c.Score > 0) h -= c.Score * Math.Log2(c.Score);
        }
        return h;
    }

    /// <summary>The total score of category id and all its descendants.</summary>
    public static double SubtreeMass(CategoryTree tree, IEnumerable<Candidate> cands, string id) =>
        cands.Where(c => c.Category == id || tree.IsAncestor(id, c.Category)).Sum(c => c.Score);
}
