using MailCat.Classification;
using MailCat.Questions;
using MailCat.Storage;

namespace MailCat.Engine;

/// <summary>Resolution sources stored in sessions.resolved_by.</summary>
public static class Resolved
{
    public const string Auto = "auto";
    public const string Answers = "answers";
    public const string Predicted = "predicted";
    public const string Ancestor = "ancestor";
    public const string Unknown = "unknown";
    public const string User = "user";
}

sealed class AnswerRec
{
    public string QuestionId { get; set; } = "";
    /// <summary>Canonical answer id(s), comma separated; "" when skipped.</summary>
    public string Answer { get; set; } = "";
    public string FeatureValue { get; set; } = "";
    public bool Skipped { get; set; }
    public double Gain { get; set; }
}

sealed class PendingRec
{
    public string QuestionId { get; set; } = "";
    public double ExpectedGain { get; set; }
    public double Utility { get; set; }
}

/// <summary>The persisted JSON of a session (§30): frozen evidence, candidates, asked, answers.</summary>
sealed class SessionState
{
    public List<Evidence> Evidence { get; set; } = [];
    public List<Candidate> Base { get; set; } = [];
    public List<Candidate> Candidates { get; set; } = [];
    public List<string> Asked { get; set; } = [];
    public List<AnswerRec> Answers { get; set; } = [];
    public PendingRec? Pending { get; set; }

    public string Decision { get; set; } = "";
    public string Final { get; set; } = "";
    public double FinalScore { get; set; }
    public string Method { get; set; } = "";
    public string ResolvedBy { get; set; } = "";
    public string Initial { get; set; } = "";

    public int Answered() => Answers.Count(a => !a.Skipped);

    public string Marshal() => Json.Serialize(this);
}
