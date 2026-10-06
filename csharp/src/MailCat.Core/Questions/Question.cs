using System.Text.RegularExpressions;

namespace MailCat.Questions;

/// <summary>Type of a question (§5.5).</summary>
public static class QuestionType
{
    public const string Boolean = "boolean";
    public const string SingleChoice = "single_choice";
    public const string MultiChoice = "multi_choice";
    public const string Text = "text";
    public const string Category = "category";
}

public static class QuestionSource
{
    public const string Config = "config";
}

/// <summary>One selectable answer and the feature value it asserts.</summary>
public sealed class Answer
{
    public string Id { get; set; } = "";
    public string Label { get; set; } = "";
    public string FeatureValue { get; set; } = "";
}

/// <summary>A user-facing question that yields information about a feature.</summary>
public sealed partial class Question
{
    public const string Yes = "yes";
    public const string No = "no";

    public string Id { get; set; } = "";
    public string Text { get; set; } = "";
    public string Feature { get; set; } = "";
    public string Type { get; set; } = "";
    public List<Answer> Answers { get; set; } = [];

    /// <summary>
    /// category → answer → P(answer | category). For boolean questions the config shorthand
    /// "category: 0.9" means P(yes | category) = 0.9. Categories without an entry inherit
    /// from their closest ancestor; without any entry the answers are uninformative.
    /// </summary>
    public Dictionary<string, Dictionary<string, double>> Affinity { get; set; } = [];

    /// <summary>P(the user answers correctly); default 0.95.</summary>
    public double Reliability { get; set; }
    /// <summary>Trust in the question itself; default 1.</summary>
    public double Confidence { get; set; }
    /// <summary>Extra penalty (needs context, ambiguous...).</summary>
    public double Complexity { get; set; }

    public bool Disabled { get; set; }
    public bool Validated { get; set; }
    public string Source { get; set; } = "";

    [GeneratedRegex(@"^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$")]
    private static partial Regex FeatureName();

    /// <summary>Fills defaults: boolean answers, reliability, confidence and source.</summary>
    public void Normalize()
    {
        if (Type == "") Type = QuestionType.Boolean;
        if (Type == QuestionType.Boolean && Answers.Count == 0)
        {
            Answers =
            [
                new Answer { Id = Yes, Label = "Sí", FeatureValue = "true" },
                new Answer { Id = No, Label = "No", FeatureValue = "false" },
            ];
        }
        foreach (var a in Answers)
        {
            if (a.Label == "") a.Label = a.Id;
            if (a.FeatureValue == "") a.FeatureValue = a.Id;
        }
        if (Reliability == 0) Reliability = 0.95;
        if (Confidence == 0) Confidence = 1;
        if (Source == "") Source = QuestionSource.Config;
    }

    /// <summary>Checks structural soundness; throws <see cref="ConfigException"/> otherwise.</summary>
    public void Validate()
    {
        if (Id == "") throw new ConfigException("question id required");
        if (string.IsNullOrWhiteSpace(Text)) throw new ConfigException($"question \"{Id}\": text required");
        int n = Text.EnumerateRunes().Count();
        if (n > 200) throw new ConfigException($"question \"{Id}\": text too long ({n} > 200)");
        if (!FeatureName().IsMatch(Feature))
            throw new ConfigException($"question \"{Id}\": feature \"{Feature}\" must look like \"group.name\"");

        switch (Type)
        {
            case QuestionType.Boolean or QuestionType.SingleChoice or QuestionType.MultiChoice:
                if (Answers.Count < 2) throw new ConfigException($"question \"{Id}\": at least 2 answers required");
                var seen = new HashSet<string>();
                foreach (var a in Answers)
                {
                    if (a.Id == "" || !seen.Add(a.Id))
                        throw new ConfigException($"question \"{Id}\": empty or duplicate answer id \"{a.Id}\"");
                }
                if (Type == QuestionType.Boolean && !(seen.Contains(Yes) && seen.Contains(No) && Answers.Count == 2))
                    throw new ConfigException($"question \"{Id}\": boolean questions need exactly the answers yes/no");
                break;
            case QuestionType.Text or QuestionType.Category:
                break;
            default:
                throw new ConfigException($"question \"{Id}\": unknown type \"{Type}\"");
        }

        foreach (var (cat, row) in Affinity)
        {
            double sum = 0;
            foreach (var (a, p) in row)
            {
                if (p < 0 || p > 1) throw new ConfigException($"question \"{Id}\": affinity {cat}/{a} out of [0,1]");
                if (!HasAnswer(a)) throw new ConfigException($"question \"{Id}\": affinity for unknown answer \"{a}\"");
                sum += p;
            }
            if (sum <= 0) throw new ConfigException($"question \"{Id}\": empty affinity row for {cat}");
        }
        if (Reliability < 0 || Reliability > 1 || Confidence < 0 || Confidence > 1)
            throw new ConfigException($"question \"{Id}\": reliability/confidence must be in [0,1]");
    }

    bool HasAnswer(string id) => Answers.Any(a => a.Id == id);

    /// <summary>Whether information gain can be computed for the type.</summary>
    [System.Text.Json.Serialization.JsonIgnore]
    public bool Selectable => Type is QuestionType.Boolean or QuestionType.SingleChoice;

    public List<string> AnswerIds() => Answers.Select(a => a.Id).ToList();

    static readonly Dictionary<string, string> YesWords = new()
    {
        ["yes"] = Yes, ["y"] = Yes, ["true"] = Yes, ["1"] = Yes, ["si"] = Yes, ["sí"] = Yes, ["s"] = Yes,
        ["no"] = No, ["n"] = No, ["false"] = No, ["0"] = No,
    };

    /// <summary>
    /// Maps raw user input to the canonical answer id(s). Boolean accepts yes/no/sí/true/...;
    /// choices accept the answer id or label (case-insensitive); multi_choice accepts a
    /// comma-separated list. Throws <see cref="InvalidRequestException"/> on bad input.
    /// </summary>
    public List<string> ResolveAnswer(string raw)
    {
        raw = (raw ?? "").Trim();
        if (raw == "") throw new InvalidRequestException("empty answer");
        switch (Type)
        {
            case QuestionType.Boolean:
                if (YesWords.TryGetValue(raw.ToLowerInvariant(), out var id)) return [id];
                throw new InvalidRequestException($"answer \"{raw}\" is not yes/no");
            case QuestionType.SingleChoice or QuestionType.MultiChoice:
                var parts = Type == QuestionType.MultiChoice ? raw.Split(',') : [raw];
                var o = new List<string>();
                foreach (var part in parts)
                {
                    var p = part.Trim();
                    var found = Answers.FirstOrDefault(a =>
                        string.Equals(a.Id, p, StringComparison.OrdinalIgnoreCase) ||
                        string.Equals(a.Label, p, StringComparison.OrdinalIgnoreCase));
                    if (found == null)
                        throw new InvalidRequestException($"answer \"{p}\" is not one of [{string.Join(' ', AnswerIds())}]");
                    o.Add(found.Id);
                }
                return o;
            case QuestionType.Text:
                if (raw.EnumerateRunes().Count() > 200) throw new InvalidRequestException("text answer too long");
                return [raw];
            case QuestionType.Category:
                return [raw];
        }
        throw new InvalidRequestException($"unsupported question type \"{Type}\"");
    }

    /// <summary>The feature value asserted by an answer id (text / category answers carry their own value).</summary>
    public string FeatureValueOf(string answerId) => Answers.FirstOrDefault(a => a.Id == answerId)?.FeatureValue ?? answerId;
}
