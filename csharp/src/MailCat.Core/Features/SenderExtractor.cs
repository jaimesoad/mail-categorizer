using MailCat.Emails;

namespace MailCat.Features;

/// <summary>Derives sender features (layer 7.2) except history-based ones.</summary>
public sealed class SenderExtractor : IFeatureExtractor
{
    /// <summary>
    /// Free mailbox providers: a domain-level rule for them would generalize across
    /// unrelated people, so they are flagged and never used for domain learning.
    /// </summary>
    static readonly HashSet<string> Freemail = new(
        ("gmail.com googlemail.com outlook.com hotmail.com live.com msn.com " +
         "yahoo.com yahoo.es yahoo.com.mx ymail.com icloud.com me.com mac.com aol.com proton.me protonmail.com " +
         "gmx.com gmx.net mail.com zoho.com yandex.com qq.com 163.com hotmail.es outlook.es")
        .Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries), StringComparer.Ordinal);

    public static bool IsFreemail(string domain) => Freemail.Contains(domain);

    public static string RegistrableDomain(string host) => PublicSuffix.RegistrableDomain(host);

    internal static bool IsNoReply(string local)
    {
        var l = local.Replace("-", "").Replace("_", "");
        return l.StartsWith("noreply", StringComparison.Ordinal) || l.StartsWith("donotreply", StringComparison.Ordinal) || l == "mailerdaemon";
    }

    internal static string Str(bool b) => b ? "true" : "false";

    public IEnumerable<Feature> Extract(Email e)
    {
        if (e.From.Email == "") return [];
        var host = e.From.Domain;
        var domain = RegistrableDomain(host);
        var local = e.From.Email.EndsWith("@" + host, StringComparison.Ordinal) ? e.From.Email[..^(host.Length + 1)] : e.From.Email;
        var o = new List<Feature>
        {
            new(FeatureNames.SenderEmail, e.From.Email, 1, FeatureSource.Header),
            new(FeatureNames.SenderHost, host, 1, FeatureSource.Header),
            new(FeatureNames.SenderDomain, domain, 1, FeatureSource.Header),
        };
        if (e.From.Name != "") o.Add(new(FeatureNames.SenderName, e.From.Name.ToLowerInvariant(), 1, FeatureSource.Header));
        bool free = IsFreemail(domain);
        o.Add(new(FeatureNames.SenderIsFreemail, Str(free), 1, FeatureSource.Parser));
        o.Add(new(FeatureNames.SenderIsCorporate, Str(!free), 0.8, FeatureSource.Parser));
        o.Add(new(FeatureNames.SenderIsNoReply, Str(IsNoReply(local)), 1, FeatureSource.Parser));
        return o;
    }
}

/// <summary>
/// Adds features that depend on the knowledge base: whether the sender is known, which
/// categories it was filed under, and thread context.
/// </summary>
public sealed class HistoryExtractor(IHistory history) : IFeatureExtractor
{
    public IEnumerable<Feature> Extract(Email e)
    {
        var o = new List<Feature>();
        if (e.From.Email != "")
        {
            var domain = PublicSuffix.RegistrableDomain(e.From.Domain);
            var sh = history.SenderHistory(e.From.Email, domain);
            o.Add(new(FeatureNames.SenderIsKnown, SenderExtractor.Str(sh.EmailCount > 0), 1, FeatureSource.History));
            o.Add(new(FeatureNames.SenderDomainIsKnown, SenderExtractor.Str(sh.DomainCount > 0), 1, FeatureSource.History));
            foreach (var c in sh.EmailCategories.Take(3)) o.Add(new(FeatureNames.SenderPrevCategory, c, 0.9, FeatureSource.History));
            foreach (var c in sh.DomainCategories.Take(3)) o.Add(new(FeatureNames.DomainPrevCategory, c, 0.8, FeatureSource.History));
        }
        var ids = new List<string>();
        if (e.InReplyTo != "") ids.Add(e.InReplyTo);
        for (int i = e.References.Count - 1; i >= 0; i--) ids.Add(e.References[i]); // closest ancestors first
        if (ids.Count > 0)
        {
            var ti = history.ThreadInfo(ids);
            if (ti.Found)
            {
                if (ti.Category != "") o.Add(new(FeatureNames.ThreadPrevCategory, ti.Category, 0.9, FeatureSource.History));
                if (ti.Sender != "") o.Add(new(FeatureNames.ThreadPrevSender, ti.Sender, 1, FeatureSource.History));
            }
        }
        return o;
    }
}
