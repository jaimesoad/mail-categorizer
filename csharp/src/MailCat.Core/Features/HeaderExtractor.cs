using System.Text.RegularExpressions;
using MailCat.Emails;

namespace MailCat.Features;

/// <summary>Derives features from RFC 5322 headers (layer 7.1).</summary>
public sealed partial class HeaderExtractor : IFeatureExtractor
{
    /// <summary>Matches one leading "Re:"/"Fwd:" style marker (several languages).</summary>
    [GeneratedRegex(@"^\s*(re|res|aw|sv|fwd?|rv|reenv|wg)\s*(?:\[\d+\])?\s*:", RegexOptions.IgnoreCase | RegexOptions.CultureInvariant)]
    private static partial Regex SubjectPrefix();

    /// <summary>Walks the chain of leading prefixes ("Re: Fwd: x") and reports reply/forward markers.</summary>
    internal static (bool Reply, bool Forward) SubjectMarkers(string subject)
    {
        bool reply = false, forward = false;
        while (true)
        {
            var m = SubjectPrefix().Match(subject);
            if (!m.Success) return (reply, forward);
            switch (m.Groups[1].Value.ToLowerInvariant())
            {
                case "fwd" or "fw" or "rv" or "reenv" or "wg": forward = true; break;
                default: reply = true; break;
            }
            subject = subject[m.Length..];
        }
    }

    public IEnumerable<Feature> Extract(Email e)
    {
        var o = new List<Feature>();
        void Add(string n, string v, double c) => o.Add(new Feature(n, v, c, FeatureSource.Header));
        void Bool(string n, bool v) => Add(n, v ? "true" : "false", 1.0);

        Bool(FeatureNames.HeadersHasUnsubscribe, e.HasHeader("List-Unsubscribe"));
        var lid = e.Header("List-Id");
        if (lid != "") Add(FeatureNames.HeadersListId, lid.Trim('<', '>', ' ').ToLowerInvariant(), 1.0);

        var prec = e.Header("Precedence").ToLowerInvariant();
        bool bulk = prec is "bulk" or "list" or "junk" || e.HasHeader("List-Id") || e.HasHeader("List-Unsubscribe");
        Bool(FeatureNames.HeadersIsBulk, bulk);

        var auto = e.Header("Auto-Submitted").ToLowerInvariant();
        Bool(FeatureNames.HeadersIsAutomated, (auto != "" && auto != "no") || e.HasHeader("X-Auto-Response-Suppress"));

        var (subjReply, subjFwd) = SubjectMarkers(e.Subject);
        Bool(FeatureNames.HeadersIsReply, e.InReplyTo != "" || e.References.Count > 0 || subjReply);
        Bool(FeatureNames.HeadersIsForward, subjFwd);
        return o;
    }
}
