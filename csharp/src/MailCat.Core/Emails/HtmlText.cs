using System.Net;
using System.Text.RegularExpressions;

namespace MailCat.Emails;

/// <summary>Minimal HTML → plain text conversion for messages that carry no text/plain part.</summary>
static partial class HtmlText
{
    [GeneratedRegex(@"<(script|style|head)\b[^>]*>.*?</\1\s*>", RegexOptions.IgnoreCase | RegexOptions.Singleline)]
    private static partial Regex Hidden();

    [GeneratedRegex(@"<\s*(br|/p|/div|/li|/tr|/h[1-6]|/table)\b[^>]*>", RegexOptions.IgnoreCase)]
    private static partial Regex BlockEnd();

    [GeneratedRegex(@"<[^>]*>", RegexOptions.Singleline)]
    private static partial Regex Tag();

    [GeneratedRegex(@"[ \t ]+")]
    private static partial Regex Spaces();

    public static string Convert(string html)
    {
        var s = Hidden().Replace(html, " ");
        s = BlockEnd().Replace(s, "\n");
        s = Tag().Replace(s, " ");
        s = WebUtility.HtmlDecode(s);
        s = Spaces().Replace(s, " ");
        return string.Join('\n', s.Split('\n').Select(l => l.Trim())).Trim();
    }
}
