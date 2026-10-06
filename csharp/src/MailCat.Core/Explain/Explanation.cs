using System.Globalization;
using MailCat.Categories;
using MailCat.Classification;

namespace MailCat.Explain;

/// <summary>Decision states of §41.</summary>
public static class Decisions
{
    /// <summary>"I know".</summary>
    public const string Know = "know";
    /// <summary>"I have a good prediction".</summary>
    public const string Predict = "predict";
    /// <summary>"I need to ask".</summary>
    public const string Ask = "ask";
    /// <summary>"I don't know".</summary>
    public const string Unknown = "unknown";
    /// <summary>The user decided.</summary>
    public const string User = "user";
}

/// <summary>A question the user answered during the session.</summary>
public sealed record QA(string Question, string Answer);

/// <summary>An alternative the system considered.</summary>
public sealed record Alt(string Category, string Path, double Score);

/// <summary>Everything needed to explain a classification.</summary>
public sealed class ExplainInput
{
    public required CategoryTree Tree { get; init; }
    public required Result Result { get; init; }
    public string Decision { get; init; } = "";
    /// <summary>The final category; defaults to Result.Best.</summary>
    public string Category { get; init; } = "";
    public List<QA> Answers { get; init; } = [];
}

/// <summary>The structured explanation: "why was this email put here?" (§2.4) in plain language.</summary>
public sealed class Explanation
{
    const int MaxReasons = 6;

    public string Category { get; set; } = "";
    public string Path { get; set; } = "";
    public double Score { get; set; }
    public string Method { get; set; } = "";
    public string Decision { get; set; } = "";
    public string Headline { get; set; } = "";
    public List<string> Reasons { get; set; } = [];
    public List<string> Against { get; set; } = [];
    public List<QA> Answers { get; set; } = [];
    public List<Alt> Alternatives { get; set; } = [];

    static double Round2(double f) => (int)(f * 100 + 0.5) / 100.0;

    public static Explanation Build(ExplainInput input)
    {
        var cat = input.Category != "" ? input.Category : input.Result.Best.Category;
        double score = 0;
        foreach (var c in input.Result.Candidates)
        {
            if (c.Category == cat) score = c.Score;
        }
        if (cat == "") cat = CategoryTree.UnknownId;

        var x = new Explanation
        {
            Category = cat, Path = input.Tree.Path(cat), Score = Round2(score), Decision = input.Decision,
            Method = Classifier.DominantMethod(input.Tree, cat, input.Result.Evidence), Answers = input.Answers,
        };
        if (input.Decision == Decisions.User)
        {
            x.Method = Classification.Method.User;
            x.Score = 1;
        }
        x.Headline = MakeHeadline(input.Decision, x.Path, x.Score);

        var seen = new HashSet<string>();
        if (input.Decision == Decisions.User) x.Reasons.Add("La categoría fue establecida por el usuario.");
        foreach (var e in Classifier.Relevant(input.Tree, input.Result.Evidence, cat))
        {
            if (!seen.Contains(e.Reason) && x.Reasons.Count < MaxReasons)
            {
                seen.Add(e.Reason);
                x.Reasons.Add(e.Reason);
            }
        }
        foreach (var qa in input.Answers) x.Reasons.Add($"Respondiste «{qa.Answer}» a «{qa.Question}».");
        if (x.Reasons.Count == 0) x.Reasons = ["No hay evidencia suficiente a favor de ninguna categoría."];

        // negative evidence that argued against a category
        foreach (var e in input.Result.Evidence)
        {
            if (e.Weight >= 0) continue;
            var line = $"{input.Tree.Path(e.Category)}: {e.Reason}";
            if (seen.Add("!" + line)) x.Against.Add(line);
        }
        x.Against.Sort(StringComparer.Ordinal);

        foreach (var c in input.Result.Candidates)
        {
            if (c.Category == cat || c.Score < 0.03 || x.Alternatives.Count >= 3) continue;
            x.Alternatives.Add(new Alt(c.Category, input.Tree.Path(c.Category), Round2(c.Score)));
        }
        return x;
    }

    static string Pct(double score) => (score * 100).ToString("F0", CultureInfo.InvariantCulture);

    static string MakeHeadline(string decision, string path, double score) => decision switch
    {
        Decisions.Know => $"Estoy seguro: {path} ({Pct(score)}%).",
        Decisions.Predict => $"Mi mejor predicción es {path} ({Pct(score)}%), pero no estoy seguro.",
        Decisions.Ask => "Necesito preguntarte algo para decidir.",
        Decisions.User => $"Clasificado por ti como {path}.",
        _ => "No tengo evidencia suficiente para clasificar este correo.",
    };

    /// <summary>Renders the explanation as text lines.</summary>
    public List<string> Lines()
    {
        var lines = new List<string> { Headline };
        if (Category != CategoryTree.UnknownId) lines.Add($"Categoría: {Path}");
        lines.Add("Razones:");
        lines.AddRange(Reasons.Select(r => "- " + r));
        if (Against.Count > 0)
        {
            lines.Add("En contra de otras categorías:");
            lines.AddRange(Against.Select(a => "- " + a));
        }
        if (Alternatives.Count > 0)
        {
            lines.Add("Alternativas:");
            lines.AddRange(Alternatives.Select(a => $"- {a.Path} ({Pct(a.Score)}%)"));
        }
        return lines;
    }
}
