using MailCat.Classification;
using MailCat.Emails;
using MailCat.Features;
using MailCat.Text;

namespace MailCat.Similarity;

/// <summary>An already classified email. Trust is 1.0 for user-confirmed labels.</summary>
public sealed record Doc(string Id, string Category, string Text, double Trust);

/// <summary>A similar document.</summary>
public sealed record Neighbor(string Id, string Category, double Sim, double Trust);

/// <summary>Answers nearest-neighbour queries. An embedding backend could replace TF-IDF later.</summary>
public interface IIndex
{
    /// <summary>Up to k documents by descending similarity, excluding the document with id excludeId.</summary>
    List<Neighbor> Neighbors(string text, int k, string excludeId);
    int Count { get; }
}

/// <summary>An in-memory TF-IDF index with an inverted list (cosine k-NN, §22).</summary>
public sealed class TfIdfIndex : IIndex
{
    readonly record struct Posting(int Doc, double W);

    readonly List<Doc> docs;
    readonly Dictionary<string, double> idf = new(StringComparer.Ordinal);
    readonly Dictionary<string, List<Posting>> postings = new(StringComparer.Ordinal);

    /// <summary>Builds an index. It is deterministic: docs are processed in ID order.</summary>
    public TfIdfIndex(IEnumerable<Doc> input)
    {
        docs = input.OrderBy(d => d.Id, StringComparer.Ordinal).ToList();
        var df = new Dictionary<string, int>(StringComparer.Ordinal);
        var tfs = new List<Dictionary<string, double>>(docs.Count);
        foreach (var d in docs)
        {
            var counts = new Dictionary<string, int>(StringComparer.Ordinal);
            foreach (var tok in TextUtil.Tokens(d.Text)) counts[tok] = counts.GetValueOrDefault(tok) + 1;
            var tf = new Dictionary<string, double>(counts.Count, StringComparer.Ordinal);
            foreach (var (tok, c) in counts)
            {
                tf[tok] = 1 + Math.Log(c);
                df[tok] = df.GetValueOrDefault(tok) + 1;
            }
            tfs.Add(tf);
        }
        double n = docs.Count;
        foreach (var (tok, c) in df) idf[tok] = Math.Log((1 + n) / (1 + c)) + 1;

        for (int i = 0; i < tfs.Count; i++)
        {
            var tf = tfs[i];
            double norm = 0;
            var terms = tf.Keys.OrderBy(k => k, StringComparer.Ordinal).ToList();
            foreach (var tok in terms)
            {
                double w = tf[tok] * idf[tok];
                norm += w * w;
            }
            norm = Math.Sqrt(norm);
            if (norm == 0) continue;
            foreach (var tok in terms)
            {
                if (!postings.TryGetValue(tok, out var l)) postings[tok] = l = [];
                l.Add(new Posting(i, tf[tok] * idf[tok] / norm));
            }
        }
    }

    public int Count => docs.Count;

    public List<Neighbor> Neighbors(string text, int k, string excludeId)
    {
        if (Count == 0 || k <= 0) return [];
        var counts = new Dictionary<string, int>(StringComparer.Ordinal);
        foreach (var tok in TextUtil.Tokens(text))
        {
            if (idf.ContainsKey(tok)) counts[tok] = counts.GetValueOrDefault(tok) + 1;
        }
        if (counts.Count == 0) return [];

        var q = new Dictionary<string, double>(StringComparer.Ordinal);
        double norm = 0;
        var terms = counts.Keys.OrderBy(t => t, StringComparer.Ordinal).ToList();
        foreach (var tok in terms)
        {
            double w = (1 + Math.Log(counts[tok])) * idf[tok];
            q[tok] = w;
            norm += w * w;
        }
        norm = Math.Sqrt(norm);

        var sims = new double[docs.Count];
        foreach (var tok in terms) // sorted: deterministic float accumulation
        {
            if (!postings.TryGetValue(tok, out var l)) continue;
            foreach (var p in l) sims[p.Doc] += q[tok] / norm * p.W;
        }

        var o = new List<Neighbor>();
        for (int i = 0; i < sims.Length; i++)
        {
            if (sims[i] <= 0 || docs[i].Id == excludeId) continue;
            o.Add(new Neighbor(docs[i].Id, docs[i].Category, sims[i], docs[i].Trust));
        }
        return o.OrderByDescending(n => n.Sim).ThenBy(n => n.Id, StringComparer.Ordinal).Take(k).ToList();
    }
}

/// <summary>
/// Adapts an index to <see cref="ISignal"/>. Its evidence is capped at MaxWeight (&lt; the
/// auto-classify threshold): similarity supports the other signals, it never decides alone (§22).
/// </summary>
public sealed class SimilaritySignal : ISignal
{
    public Func<IIndex?>? Load { get; init; }
    /// <summary>Neighbours considered (default 10).</summary>
    public int K { get; init; }
    /// <summary>Ignore weaker neighbours (default 0.25).</summary>
    public double MinSim { get; init; }
    /// <summary>Cap on the emitted weight (default 0.75).</summary>
    public double MaxWeight { get; init; }
    public Func<string, string>? Name { get; init; }

    /// <summary>Text used for similarity: the subject is repeated so it weighs more than a single body sentence.</summary>
    public static string DocText(Email e) => e.Subject + " " + e.Subject + " " + e.BodyText;

    public List<Evidence> Evidence(Email e, FeatureSet _)
    {
        var idx = Load?.Invoke();
        if (idx == null || idx.Count == 0) return [];
        int k = K == 0 ? 10 : K;
        double minSim = MinSim == 0 ? 0.25 : MinSim;
        double maxW = MaxWeight == 0 ? 0.75 : MaxWeight;

        var ns = idx.Neighbors(DocText(e), k, e.Id).Where(n => n.Sim >= minSim).ToList();
        if (ns.Count == 0) return [];

        var votes = new Dictionary<string, double>(StringComparer.Ordinal);
        var sims = new Dictionary<string, double>(StringComparer.Ordinal);
        var counts = new Dictionary<string, int>(StringComparer.Ordinal);
        double total = 0;
        foreach (var n in ns)
        {
            double v = n.Sim * n.Trust;
            votes[n.Category] = votes.GetValueOrDefault(n.Category) + v;
            sims[n.Category] = sims.GetValueOrDefault(n.Category) + n.Sim;
            counts[n.Category] = counts.GetValueOrDefault(n.Category) + 1;
            total += v;
        }
        var name = Name ?? (c => c);
        var o = new List<Evidence>();
        foreach (var c in votes.Keys.OrderBy(c => c, StringComparer.Ordinal))
        {
            double share = votes[c] / total;
            double avg = sims[c] / counts[c];
            double coverage = Math.Min(1, counts[c] / 3.0);
            double w = maxW * share * avg * coverage;
            if (w < 0.02) continue;
            o.Add(new Evidence(c, w, Method.Similarity,
                $"{counts[c]} de {ns.Count} correos similares fueron clasificados como {name(c)}"));
        }
        return o;
    }
}
