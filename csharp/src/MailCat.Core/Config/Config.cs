using System.Globalization;
using System.Reflection;
using MailCat.Categories;
using MailCat.Classification;
using MailCat.Questions;
using MailCat.Rules;
using YamlDotNet.Core;
using YamlDotNet.Serialization;
using YamlDotNet.Serialization.NamingConventions;

namespace MailCat.Config;

/// <summary>Decision thresholds, mirrors §15.</summary>
public sealed class ThresholdsConfig
{
    /// <summary>≥ auto: classify automatically.</summary>
    public double Auto { get; set; } = 0.85;
    /// <summary>≥ ask: good prediction; below: low confidence.</summary>
    public double Ask { get; set; } = 0.60;
    /// <summary>Required gap to the runner-up for automatic classification.</summary>
    public double MinMargin { get; set; } = 0.20;
}

/// <summary>The TF-IDF k-NN signal (§22).</summary>
public sealed class SimilarityConfig
{
    public bool Enabled { get; set; } = true;
    public int K { get; set; } = 10;
    public double MinSimilarity { get; set; } = 0.25;
    public double MaxWeight { get; set; } = 0.75;
}

public sealed class LearningConfig
{
    public bool Enabled { get; set; } = true;
}

/// <summary>Sampled confirmation requests (§16).</summary>
public sealed class ConfirmationConfig
{
    /// <summary>Probability of asking to confirm an automatic classification.</summary>
    public double BaseRate { get; set; } = 0.10;
    /// <summary>Keeps sampling deterministic yet configurable.</summary>
    public string Seed { get; set; } = "mailcat";
}

/// <summary>The selector (§13).</summary>
public sealed class QuestionSelectionConfig
{
    public double MinGain { get; set; } = 0.05;
}

/// <summary>What is stored (§24).</summary>
public sealed class PrivacyConfig
{
    /// <summary>False: bodies are never persisted (disables similarity).</summary>
    public bool StoreBody { get; set; } = true;
    /// <summary>Normalization limit.</summary>
    public int MaxBodyChars { get; set; } = 20000;
}

/// <summary>A category as written in YAML.</summary>
public sealed class CategoryConfig
{
    public string Id { get; set; } = "";
    public string Parent { get; set; } = "";
    public string Name { get; set; } = "";
    public string Description { get; set; } = "";
    public bool Disabled { get; set; }
}

public sealed class AnswerConfig
{
    public string Id { get; set; } = "";
    public string Label { get; set; } = "";
    public string Value { get; set; } = "";
}

/// <summary>A question as written in YAML.</summary>
public sealed class QuestionConfig
{
    public string Id { get; set; } = "";
    public string Text { get; set; } = "";
    public string Feature { get; set; } = "";
    public string Type { get; set; } = "";
    public List<AnswerConfig> Answers { get; set; } = [];
    public double Reliability { get; set; }
    public double Confidence { get; set; }
    public double Complexity { get; set; }
    public bool Disabled { get; set; }

    /// <summary>
    /// category → either a bare number (P(yes) of a boolean question) or an answer→probability
    /// map (choice questions).
    /// </summary>
    public Dictionary<string, object> Affinity { get; set; } = [];
}

/// <summary>The whole configuration (§28, §29): categories, questions, rules and tunables.</summary>
public sealed class MailCatConfig
{
    public string Database { get; set; } = "mailcat.db";
    public ThresholdsConfig Thresholds { get; set; } = new();
    public int MaxQuestionsPerEmail { get; set; } = 5;
    public SimilarityConfig Similarity { get; set; } = new();
    public LearningConfig Learning { get; set; } = new();
    public ConfirmationConfig Confirmation { get; set; } = new();
    public QuestionSelectionConfig QuestionSelection { get; set; } = new();
    public PrivacyConfig Privacy { get; set; } = new();

    public List<CategoryConfig> Categories { get; set; } = [];
    public List<QuestionConfig> Questions { get; set; } = [];
    public List<Rule> Rules { get; set; } = [];

    static readonly IDeserializer Deserializer = new DeserializerBuilder()
        .WithNamingConvention(UnderscoredNamingConvention.Instance)
        .Build();

    /// <summary>The embedded example configuration (used by <c>mailcat init</c>).</summary>
    public static string DefaultYaml()
    {
        using var s = Assembly.GetExecutingAssembly().GetManifestResourceStream("default.yaml")
                      ?? throw new InvalidOperationException("embedded default.yaml missing");
        using var r = new StreamReader(s);
        return r.ReadToEnd();
    }

    /// <summary>Decodes YAML over the defaults, rejecting unknown keys.</summary>
    public static MailCatConfig Parse(string yaml)
    {
        try
        {
            return Deserializer.Deserialize<MailCatConfig?>(yaml) ?? new MailCatConfig();
        }
        catch (YamlException ex)
        {
            var inner = ex.InnerException?.Message;
            throw new ConfigException($"config: {ex.Message}{(inner != null ? " — " + inner : "")}");
        }
    }

    public static MailCatConfig Load(string path) => Parse(File.ReadAllText(path));

    /// <summary>Loads path, or the embedded example configuration when path is "".</summary>
    public static MailCatConfig LoadOrDefault(string path) => path == "" ? Parse(DefaultYaml()) : Load(path);

    public Thresholds ClassifierThresholds() => new(Thresholds.Auto, Thresholds.Ask, Thresholds.MinMargin);

    /// <summary>Validates the configuration and compiles categories, questions and rules (M15).</summary>
    public BuiltConfig Build()
    {
        var t = Thresholds;
        if (!(t.Ask > 0 && t.Ask <= t.Auto && t.Auto <= 1) || t.MinMargin < 0 || t.MinMargin > 1)
            throw new ConfigException(string.Create(CultureInfo.InvariantCulture,
                $"thresholds: need 0 < ask <= auto <= 1 and 0 <= min_margin <= 1 (got ask={t.Ask} auto={t.Auto} margin={t.MinMargin})"));
        if (MaxQuestionsPerEmail < 0 || MaxQuestionsPerEmail > 20)
            throw new ConfigException("max_questions_per_email must be in [0,20]");

        var cats = Categories.Select(c => new Category(c.Id, c.Parent, c.Name, c.Description, !c.Disabled)).ToList();
        CategoryTree tree;
        try { tree = new CategoryTree(cats); }
        catch (ConfigException ex) { throw new ConfigException($"categories: {ex.Message}"); }

        var qs = new List<Question>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (var qc in Questions)
        {
            var q = new Question
            {
                Id = qc.Id, Text = qc.Text, Feature = qc.Feature, Type = qc.Type, Reliability = qc.Reliability,
                Confidence = qc.Confidence, Complexity = qc.Complexity, Disabled = qc.Disabled,
                Answers = qc.Answers.Select(a => new Answer { Id = a.Id, Label = a.Label, FeatureValue = a.Value }).ToList(),
            };
            foreach (var (cat, row) in qc.Affinity)
            {
                if (!tree.Has(cat)) throw new ConfigException($"question \"{q.Id}\": affinity for unknown category \"{cat}\"");
                q.Affinity[cat] = AffinityRow(q.Id, row);
            }
            q.Normalize();
            q.Validated = true;
            if (!seen.Add(q.Id)) throw new ConfigException($"duplicate question id \"{q.Id}\"");
            q.Validate();
            qs.Add(q);
        }

        foreach (var r in Rules)
        {
            if (!tree.Has(r.Category)) throw new ConfigException($"rule \"{r.Id}\": unknown category \"{r.Category}\"");
        }
        return new BuiltConfig(this, tree, tree.All(), qs, new RuleEngine(Rules));
    }

    /// <summary>A bare number is P(yes) of a boolean question; a map is answer → probability.</summary>
    static Dictionary<string, double> AffinityRow(string questionId, object row)
    {
        double Num(object v)
        {
            if (!double.TryParse(Convert.ToString(v, CultureInfo.InvariantCulture), NumberStyles.Float, CultureInfo.InvariantCulture, out var d))
                throw new ConfigException($"question \"{questionId}\": affinity value \"{v}\" is not a number");
            return d;
        }
        if (row is IDictionary<object, object> map)
            return map.ToDictionary(kv => Convert.ToString(kv.Key, CultureInfo.InvariantCulture)!, kv => Num(kv.Value), StringComparer.Ordinal);
        return new Dictionary<string, double> { [Question.Yes] = Num(row) };
    }
}

/// <summary>The validated, compiled artifacts derived from a <see cref="MailCatConfig"/>.</summary>
public sealed record BuiltConfig(
    MailCatConfig Config, CategoryTree Tree, List<Category> Categories, List<Question> Questions, RuleEngine Rules);
