using MailCat.Classification;
using MailCat.Emails;
using MailCat.Features;

namespace MailCat.Learning;

/// <summary>Exposes learned rules as classifier evidence.</summary>
public sealed class LearnedSignal(ILearnedRuleRepo repo, Func<string, string>? name = null) : ISignal
{
    /// <summary>Hides rules too weak to matter.</summary>
    const double MinUsefulWeight = 0.02;

    /// <summary>
    /// Learned rules of one email overlap (sender ⊂ domain ⊂ domain+intent), so they are not
    /// independent evidence. Per category and polarity only the strongest counts fully; the
    /// others count at this fraction.
    /// </summary>
    const double DependentDiscount = 0.5;

    public List<Evidence> Evidence(Email _, FeatureSet f)
    {
        var keys = Generalizations.Keys(f);
        if (keys.Count == 0) return [];
        var rules = repo.RulesByKeys(keys);

        // A condition that was confirmed for several categories (one domain that sends both
        // "work" and "meetings" mail) is not discriminative: each rule only keeps its share
        // of the confirmations seen for that condition.
        var totals = new Dictionary<string, double>(StringComparer.Ordinal);
        foreach (var r in rules.Where(r => !r.Negative))
            totals[r.Key] = totals.GetValueOrDefault(r.Key) + r.Confirmations;

        var groups = new Dictionary<(string Cat, bool Neg), List<LearnedRule>>();
        foreach (var r0 in rules)
        {
            var r = r0.Clone();
            if (!r.Negative && totals.GetValueOrDefault(r.Key) > 0)
                r.Weight *= r.Confirmations / totals[r.Key];
            if (r.Weight < MinUsefulWeight) continue;
            var g = (r.CategoryId, r.Negative);
            if (!groups.TryGetValue(g, out var l)) groups[g] = l = [];
            l.Add(r);
        }

        var nm = name ?? (c => c);
        var o = new List<Evidence>();
        foreach (var g in groups.Keys.OrderBy(g => g.Cat, StringComparer.Ordinal).ThenBy(g => g.Neg)) // positive before negative
        {
            var rs = groups[g].OrderByDescending(r => r.Weight).ThenBy(r => r.Key, StringComparer.Ordinal).ToList();
            for (int i = 0; i < rs.Count; i++)
            {
                var r = rs[i];
                double w = r.Weight;
                if (i > 0) w *= DependentDiscount;
                if (r.Negative) w = -w;
                o.Add(new Evidence(r.CategoryId, w, Method.Learned, Describe(r, nm(r.CategoryId)), r.Key));
            }
        }
        return o;
    }

    static string Describe(LearnedRule r, string cat)
    {
        var what = r.Kind switch
        {
            RuleKind.SenderEmail => "el remitente",
            RuleKind.List => "la lista de correo",
            RuleKind.SenderDomain => "el dominio",
            RuleKind.DomainIntent => "el dominio con ese tipo de mensaje",
            RuleKind.Intent => "ese tipo de mensaje",
            _ => "",
        };
        var val = string.Join(" + ", r.Conditions.Select(c => c.Value));
        int n = r.Confirmations;
        var veces = n == 1 ? "vez" : "veces";
        return r.Negative
            ? $"Aprendido: {what} ({val}) NO suele ir en {cat} (corregido {n} {veces})"
            : $"Aprendido: {what} ({val}) se ha clasificado {n} {veces} como {cat}";
    }
}
