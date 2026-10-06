using MailCat.Emails;
using MailCat.Features;

namespace MailCat.Classification;

/// <summary>Which kind of signal produced a classification (§5.4).</summary>
public static class Method
{
    public const string Rule = "rule";
    public const string Learned = "learned";
    public const string Similarity = "similarity";
    public const string User = "user";
    public const string Combined = "combined";
}

/// <summary>
/// One piece of support (Weight &gt; 0) or contradiction (Weight &lt; 0) for a category.
/// |Weight| is in (0,1].
/// </summary>
public sealed record Evidence(string Category, double Weight, string Method, string Reason, string Ref = "");

/// <summary>
/// Produces evidence for an email. Implementations must be deterministic and must return
/// evidence in a stable order.
/// </summary>
public interface ISignal
{
    List<Evidence> Evidence(Email e, FeatureSet f);
}
