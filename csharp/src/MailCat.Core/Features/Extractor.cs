using MailCat.Emails;

namespace MailCat.Features;

/// <summary>
/// Produces features from an email. Implementations must be deterministic for a given
/// input (and, for history-based ones, knowledge base).
/// </summary>
public interface IFeatureExtractor
{
    IEnumerable<Feature> Extract(Email e);
}

/// <summary>Runs several extractors in order and merges their output.</summary>
public sealed class LayeredExtractor(IEnumerable<IFeatureExtractor> layers) : IFeatureExtractor
{
    readonly List<IFeatureExtractor> layers = layers.ToList();

    public IEnumerable<Feature> Extract(Email e)
    {
        var set = new FeatureSet();
        foreach (var layer in layers) set.Add(layer.Extract(e));
        return set.All();
    }
}

/// <summary>Summarizes what is known about a sender.</summary>
public sealed class SenderHistory
{
    public int EmailCount { get; set; }
    public int DomainCount { get; set; }
    /// <summary>Most frequent first.</summary>
    public List<string> EmailCategories { get; set; } = [];
    public List<string> DomainCategories { get; set; } = [];
}

/// <summary>The parent message of a reply.</summary>
public sealed record ThreadInfo(bool Found, string Category, string Sender)
{
    public static readonly ThreadInfo None = new(false, "", "");
}

/// <summary>
/// Gives extractors access to previously learned knowledge. A null history disables
/// history-derived features, which keeps extraction testable.
/// </summary>
public interface IHistory
{
    SenderHistory SenderHistory(string senderEmail, string senderDomain);

    /// <summary>
    /// Looks up the first known message among ids (In-Reply-To, References) and returns
    /// its final category and sender.
    /// </summary>
    ThreadInfo ThreadInfo(IReadOnlyList<string> messageIds);
}

public static class Extractors
{
    /// <summary>The standard extractor stack (§7): headers, sender, content, intent, attachments and optional history/thread.</summary>
    public static IFeatureExtractor Default(IHistory? history)
    {
        var layers = new List<IFeatureExtractor>
        {
            new HeaderExtractor(),
            new SenderExtractor(),
            new AttachmentExtractor(),
            new ContentExtractor(),
            new IntentExtractor(),
        };
        if (history != null) layers.Add(new HistoryExtractor(history));
        return new LayeredExtractor(layers);
    }
}
