using System.Text.Json.Serialization;
using MailCat.Classification;
using MailCat.Explain;
using MailCat.Questions;

namespace MailCat.Engine;

/// <summary>A question presented to the user.</summary>
public sealed class PendingQuestion
{
    public string Id { get; set; } = "";
    public string Text { get; set; } = "";
    public string Type { get; set; } = "";
    public string Feature { get; set; } = "";
    public List<Answer> Answers { get; set; } = [];
    public double InformationGain { get; set; }
    public double Utility { get; set; }
}

/// <summary>The externally visible state of one email's classification.</summary>
public sealed class Outcome
{
    public string EmailId { get; set; } = "";
    public string SessionId { get; set; } = "";

    /// <summary>know | predict | ask | unknown | user</summary>
    public string Decision { get; set; } = "";
    /// <summary>Current best / final category id.</summary>
    public string Category { get; set; } = "";
    /// <summary>Human readable "Finance / Bank".</summary>
    public string Path { get; set; } = "";
    public double Score { get; set; }
    public string Method { get; set; } = "";
    public bool Resolved { get; set; }
    public List<Candidate> Candidates { get; set; } = [];

    public bool NeedsQuestion { get; set; }
    public PendingQuestion? Question { get; set; }
    /// <summary>"I don't know": please file it manually.</summary>
    public bool NeedsManual { get; set; }
    public bool RequestConfirmation { get; set; }
    public bool Learned { get; set; }
    [JsonIgnore(Condition = JsonIgnoreCondition.WhenWritingDefault)]
    public bool AlreadyProcessed { get; set; }
    public int QuestionsAsked { get; set; }

    public Explanation Explanation { get; set; } = new();
}
