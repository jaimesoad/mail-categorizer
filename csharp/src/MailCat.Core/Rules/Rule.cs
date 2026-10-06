using MailCat.Classification;
using MailCat.Emails;
using MailCat.Features;

namespace MailCat.Rules;

/// <summary>
/// A user-defined deterministic rule: when its condition holds it adds Weight (negative =
/// suppress) to Category.
/// </summary>
public sealed class Rule
{
    public string Id { get; set; } = "";
    public string Category { get; set; } = "";
    public double Weight { get; set; }
    public string Reason { get; set; } = "";
    public Condition When { get; set; } = new();
    public bool Disabled { get; set; }
}

/// <summary>Evaluates a fixed rule list. It implements <see cref="ISignal"/>.</summary>
public sealed class RuleEngine : ISignal
{
    readonly List<(Rule Rule, IMatcher Matcher)> rules = [];

    /// <summary>Compiles rules, rejecting duplicates, invalid weights and bad conditions.</summary>
    public RuleEngine(IEnumerable<Rule> rs)
    {
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (var r in rs)
        {
            if (r.Id == "") throw new ConfigException("rule with empty id");
            if (!seen.Add(r.Id)) throw new ConfigException($"duplicate rule id \"{r.Id}\"");
            if (r.Weight == 0 || r.Weight < -1 || r.Weight > 1)
                throw new ConfigException($"rule \"{r.Id}\": weight must be in [-1,1] and non-zero");
            if (r.Category == "") throw new ConfigException($"rule \"{r.Id}\": category required");
            IMatcher m;
            try { m = ConditionCompiler.Compile(r.When); }
            catch (ConfigException ex) { throw new ConfigException($"rule \"{r.Id}\": {ex.Message}"); }
            if (!r.Disabled) rules.Add((r, m));
        }
    }

    public int Count => rules.Count;

    public List<Evidence> Evidence(Email _, FeatureSet f)
    {
        var o = new List<Evidence>();
        foreach (var (r, m) in rules)
        {
            if (!m.Match(f)) continue;
            var reason = r.Reason != "" ? r.Reason : "Regla " + r.Id;
            o.Add(new Evidence(r.Category, r.Weight, Method.Rule, reason, r.Id));
        }
        return o;
    }
}
