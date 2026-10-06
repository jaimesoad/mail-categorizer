using MailCat.Features;

namespace MailCat.Learning;

/// <summary>Kinds of generalization, from most specific to most general (§19).</summary>
public static class RuleKind
{
    public const string SenderEmail = "sender_email";
    public const string List = "list";
    public const string SenderDomain = "sender_domain";
    public const string DomainIntent = "domain_intent";
    public const string Intent = "intent";
}

/// <summary>One equality condition of a learned rule. Learned rules are conjunctions (AND) of equalities, stored as JSON.</summary>
public sealed record Cond(string Feature, string Value);

/// <summary>A learned rule. Negative rules mean "NOT this category" (§21).</summary>
public sealed class LearnedRule
{
    public string Id { get; set; } = "";
    public string CategoryId { get; set; } = "";
    /// <summary>Canonical form of Conditions; used for lookup.</summary>
    public string Key { get; set; } = "";
    public string Kind { get; set; } = "";
    public List<Cond> Conditions { get; set; } = [];
    public bool Negative { get; set; }
    public double Weight { get; set; }
    public int Observations { get; set; }
    /// <summary>Confirmed by a person.</summary>
    public int Confirmations { get; set; }
    public int Rejections { get; set; }
    public DateTime CreatedAt { get; set; }
    public DateTime UpdatedAt { get; set; }

    public LearnedRule Clone() => new()
    {
        Id = Id, CategoryId = CategoryId, Key = Key, Kind = Kind, Conditions = [.. Conditions], Negative = Negative,
        Weight = Weight, Observations = Observations, Confirmations = Confirmations, Rejections = Rejections,
        CreatedAt = CreatedAt, UpdatedAt = UpdatedAt,
    };
}

/// <summary>The persistence the learner and the learned signal need.</summary>
public interface ILearnedRuleRepo
{
    /// <summary>Every rule (positive and negative, any category) whose key is in keys.</summary>
    List<LearnedRule> RulesByKeys(IReadOnlyList<string> keys);
    LearnedRule? GetRule(string category, string key, bool negative);
    /// <summary>Inserts or updates by (category, key, negative).</summary>
    void SaveRule(LearnedRule r);
}

/// <summary>A candidate condition set derived from an email's features.</summary>
public sealed record Generalization(string Kind, string Key, List<Cond> Conds, double Cap);

public static class Generalizations
{
    /// <summary>
    /// Limits how strong a rule of each kind can become: a single sender can be trusted
    /// almost fully, a bare intent ("receipt") never dominates.
    /// </summary>
    static readonly Dictionary<string, double> WeightCap = new()
    {
        [RuleKind.SenderEmail] = 0.95, [RuleKind.List] = 0.90, [RuleKind.SenderDomain] = 0.90,
        [RuleKind.DomainIntent] = 0.95, [RuleKind.Intent] = 0.50,
    };

    /// <summary>
    /// Maps counts to a rule weight (§18): confirmations grow it with diminishing returns
    /// (1→0.20, 3→0.45, 10→0.80 for cap 0.9) and the share of rejections attenuates it without
    /// ever zeroing a well-confirmed rule.
    /// </summary>
    public static double Weight(double cap, double confirmations, int rejections)
    {
        if (confirmations <= 0) return 0;
        double growth = cap * (1 - Math.Exp(-0.25 * confirmations));
        double precision = (confirmations + 1) / (confirmations + rejections + 1);
        return Round4(growth * precision);
    }

    /// <summary><see cref="Weight"/> applied to a rule.</summary>
    public static double RuleWeight(LearnedRule r) =>
        Weight(WeightCap.GetValueOrDefault(r.Kind), r.Confirmations, r.Rejections);

    static double Round4(double f) => Math.Round(f * 10000, MidpointRounding.AwayFromZero) / 10000;

    public static string Key(IEnumerable<Cond> conds) =>
        string.Join('|', conds.OrderBy(c => c.Feature, StringComparer.Ordinal).Select(c => c.Feature + "=" + c.Value));

    /// <summary>
    /// Derives, from the extractable features of an email, the condition sets worth learning,
    /// ordered from specific to general. Features that only exist because the user answered a
    /// question are never used: they would not be present on future emails.
    /// </summary>
    public static List<Generalization> Of(FeatureSet f)
    {
        var o = new List<Generalization>();
        void Add(string kind, params Cond[] conds) => o.Add(new Generalization(kind, Key(conds), [.. conds], WeightCap[kind]));

        var email = f.Str(FeatureNames.SenderEmail);
        if (email != "") Add(RuleKind.SenderEmail, new Cond(FeatureNames.SenderEmail, email));
        var list = f.Str(FeatureNames.HeadersListId);
        if (list != "") Add(RuleKind.List, new Cond(FeatureNames.HeadersListId, list));

        var domain = f.Str(FeatureNames.SenderDomain);
        var (free, _) = f.Bool(FeatureNames.SenderIsFreemail);
        if (domain != "" && !free) // "gmail.com → Bank" would be a terrible generalization
        {
            Add(RuleKind.SenderDomain, new Cond(FeatureNames.SenderDomain, domain));
            foreach (var intent in TopIntents(f, 0.6, 2))
                Add(RuleKind.DomainIntent, new Cond(FeatureNames.SenderDomain, domain), new Cond(FeatureNames.ContentIntent, intent));
        }
        var top = TopIntents(f, 0.7, 1);
        if (top.Count > 0) Add(RuleKind.Intent, new Cond(FeatureNames.ContentIntent, top[0]));
        return o;
    }

    static List<string> TopIntents(FeatureSet f, double minConf, int n) =>
        f.Values(FeatureNames.ContentIntent)
            .OrderByDescending(v => v.Confidence) // stable: ties keep the value order
            .Where(v => v.Confidence >= minConf && v.Source != FeatureSource.User)
            .Take(n).Select(v => v.Value).ToList();

    /// <summary>The lookup keys of all generalizations of f.</summary>
    public static List<string> Keys(FeatureSet f) => Of(f).Select(g => g.Key).ToList();
}
