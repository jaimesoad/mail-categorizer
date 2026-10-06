namespace MailCat.Features;

/// <summary>
/// The collection of features of one email. Iteration is always sorted so results
/// are deterministic.
/// </summary>
public sealed class FeatureSet
{
    readonly Dictionary<string, List<Feature>> byName = new(StringComparer.Ordinal);

    public FeatureSet() { }

    public FeatureSet(IEnumerable<Feature> fs) => Add(fs);

    /// <summary>
    /// Inserts features; for an existing (name,value) the higher confidence wins, except
    /// that a user-sourced feature always wins.
    /// </summary>
    public void Add(IEnumerable<Feature> fs)
    {
        foreach (var f in fs)
        {
            if (f.Name == "") continue;
            if (!byName.TryGetValue(f.Name, out var list)) byName[f.Name] = list = [];
            bool found = false;
            for (int i = 0; i < list.Count; i++)
            {
                if (list[i].Value != f.Value) continue;
                found = true;
                if (f.Source == FeatureSource.User || (list[i].Source != FeatureSource.User && f.Confidence > list[i].Confidence))
                    list[i] = f;
            }
            if (!found) list.Add(f);
        }
    }

    public void Add(params Feature[] fs) => Add((IEnumerable<Feature>)fs);

    /// <summary>All features with the given name, sorted by value.</summary>
    public List<Feature> Values(string name) =>
        byName.TryGetValue(name, out var l)
            ? l.OrderBy(f => f.Value, StringComparer.Ordinal).ToList()
            : [];

    public bool Has(string name, string value) =>
        byName.TryGetValue(name, out var l) && l.Any(f => f.Value == value);

    public bool Exists(string name) => byName.TryGetValue(name, out var l) && l.Count > 0;

    /// <summary>The highest-confidence feature of name (ties broken by value).</summary>
    public Feature? First(string name)
    {
        if (!byName.TryGetValue(name, out var list) || list.Count == 0) return null;
        var best = list[0];
        foreach (var f in list.Skip(1))
        {
            if (f.Confidence > best.Confidence ||
                (f.Confidence == best.Confidence && string.CompareOrdinal(f.Value, best.Value) < 0))
                best = f;
        }
        return best;
    }

    /// <summary>The best value of name or "".</summary>
    public string Str(string name) => First(name)?.Value ?? "";

    /// <summary>
    /// A feature is "known true" only when a "true" value is present; "known false" when
    /// only "false" values are present.
    /// </summary>
    public (bool Value, bool Known) Bool(string name)
    {
        if (!byName.TryGetValue(name, out var list) || list.Count == 0) return (false, false);
        return (list.Any(f => string.Equals(f.Value, "true", StringComparison.OrdinalIgnoreCase)), true);
    }

    /// <summary>The highest confidence among the values of name, or 0.</summary>
    public double Confidence(string name) =>
        byName.TryGetValue(name, out var l) && l.Count > 0 ? l.Max(f => f.Confidence) : 0;

    /// <summary>Every feature sorted by (name, value).</summary>
    public List<Feature> All() =>
        byName.Values.SelectMany(l => l)
            .OrderBy(f => f.Name, StringComparer.Ordinal).ThenBy(f => f.Value, StringComparer.Ordinal).ToList();

    public List<string> Names() => byName.Keys.OrderBy(k => k, StringComparer.Ordinal).ToList();

    public FeatureSet Clone() => new(All());
}
