using System.Security.Cryptography;
using System.Text;
using MimeKit;

namespace MailCat.Emails;

/// <summary>Canonicalization that precedes feature extraction.</summary>
public static class EmailNormalizer
{
    /// <summary>Maximum number of characters of body text that are kept.</summary>
    public const int DefaultMaxBody = 20000;

    internal static string Lower(string s) => (s ?? "").Trim().ToLowerInvariant();

    /// <summary>
    /// Canonicalizes an email: trimmed and lower-cased addresses, unified line endings,
    /// collapsed blank lines, bounded body size, a stable raw hash and an ID when none
    /// was supplied. It never mutates its argument.
    /// </summary>
    public static Email Normalize(Email e, int maxBody = 0)
    {
        if (maxBody <= 0) maxBody = DefaultMaxBody;
        var refs = new List<string>();
        foreach (var r in e.References)
        {
            var t = r.Trim().Trim('<', '>');
            if (t.Length > 0) refs.Add(t);
        }
        var headers = new Dictionary<string, IReadOnlyList<string>>();
        foreach (var (k, v) in e.Headers) headers[Lower(k)] = v.ToList();

        var o = e with
        {
            From = NormAddr(e.From),
            To = NormAddrs(e.To),
            Cc = NormAddrs(e.Cc),
            ReplyTo = NormAddrs(e.ReplyTo),
            Subject = string.Join(' ', (e.Subject ?? "").Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries)),
            BodyText = NormBody(e.BodyText ?? "", maxBody),
            MessageId = (e.MessageId ?? "").Trim().Trim('<', '>'),
            InReplyTo = (e.InReplyTo ?? "").Trim().Trim('<', '>'),
            References = refs,
            Headers = headers,
        };
        if (o.RawHash == "") o = o with { RawHash = ContentHash(o) };
        if (o.Id == "") o = o with { Id = "em_" + o.RawHash[..16] };
        return o;
    }

    /// <summary>Stable hash over the normalized content (used when no raw bytes are available).</summary>
    public static string ContentHash(Email e)
    {
        var received = (e.ReceivedAt ?? DateTime.MinValue).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ");
        using var h = SHA256.Create();
        var sb = new StringBuilder();
        foreach (var s in new[] { e.MessageId, e.From.Email, e.Subject, e.BodyText, received })
        {
            sb.Append(s).Append('\0');
        }
        return Convert.ToHexString(h.ComputeHash(Encoding.UTF8.GetBytes(sb.ToString()))).ToLowerInvariant();
    }

    static Address NormAddr(Address a) => new((a.Name ?? "").Trim(), Lower(a.Email));

    static List<Address> NormAddrs(IReadOnlyList<Address> input)
    {
        var o = new List<Address>();
        foreach (var a in input)
        {
            var n = NormAddr(a);
            if (n.Email != "") o.Add(n);
        }
        return o;
    }

    static string NormBody(string s, int max)
    {
        s = s.Replace("\r\n", "\n").Replace('\r', '\n').Replace("�", "");
        var b = new StringBuilder();
        int blank = 0;
        foreach (var line in s.Split('\n'))
        {
            var l = line.TrimEnd(' ', '\t');
            if (l.Length == 0)
            {
                blank++;
                if (blank > 1) continue;
            }
            else blank = 0;
            b.Append(l).Append('\n');
        }
        return TruncateRunes(b.ToString().Trim(), max);
    }

    /// <summary>Truncates to at most <paramref name="max"/> Unicode code points.</summary>
    public static string TruncateRunes(string s, int max)
    {
        int count = 0, i = 0;
        while (i < s.Length)
        {
            if (count == max) return s[..i];
            i += char.IsHighSurrogate(s[i]) && i + 1 < s.Length ? 2 : 1;
            count++;
        }
        return s;
    }

    /// <summary>Parses "Name &lt;a@b&gt;" or "a@b". Invalid input yields a default Address.</summary>
    public static Address ParseAddress(string s)
    {
        s = (s ?? "").Trim();
        if (MailboxAddress.TryParse(s, out var mb) && (mb.Address ?? "").Contains('@'))
            return new Address(mb.Name ?? "", Lower(mb.Address!));
        return s.Contains('@') ? new Address("", Lower(s)) : default;
    }
}
