using System.Reflection;

namespace MailCat.Features;

/// <summary>
/// Registrable-domain (eTLD+1) computation over the embedded Public Suffix List, so
/// "alerts.mybank.co.uk" and "mybank.co.uk" are the same entity.
/// </summary>
public static class PublicSuffix
{
    static readonly Lazy<(HashSet<string> Rules, HashSet<string> Wildcards, HashSet<string> Exceptions)> Data = new(Load);

    static (HashSet<string>, HashSet<string>, HashSet<string>) Load()
    {
        var rules = new HashSet<string>(StringComparer.Ordinal);
        var wild = new HashSet<string>(StringComparer.Ordinal);
        var exc = new HashSet<string>(StringComparer.Ordinal);
        using var s = Assembly.GetExecutingAssembly().GetManifestResourceStream("public_suffix_list.dat")
                      ?? throw new InvalidOperationException("embedded public suffix list missing");
        using var r = new StreamReader(s);
        string? line;
        while ((line = r.ReadLine()) != null)
        {
            line = line.Trim();
            if (line.Length == 0 || line.StartsWith("//")) continue;
            int sp = line.IndexOfAny([' ', '\t']);
            if (sp > 0) line = line[..sp];
            if (!line.All(c => c < 128)) continue; // IDN rules: hosts in emails arrive as punycode
            if (line.StartsWith("!")) exc.Add(line[1..]);
            else if (line.StartsWith("*.")) wild.Add(line[2..]);
            else rules.Add(line);
        }
        return (rules, wild, exc);
    }

    /// <summary>
    /// The eTLD+1 of host ("alerts.mybank.co.uk" → "mybank.co.uk"), falling back to the host itself.
    /// </summary>
    public static string RegistrableDomain(string host)
    {
        host = (host ?? "").Trim().TrimEnd('.').ToLowerInvariant();
        if (host == "") return "";
        var labels = host.Split('.');
        if (labels.Any(l => l.Length == 0)) return host;
        int n = labels.Length;
        var (rules, wild, exc) = Data.Value;

        string Join(int from) => string.Join('.', labels, from, n - from);

        int suffixLen = 1; // the implicit "*" rule
        bool exception = false;
        for (int i = 0; i < n; i++)
        {
            if (exc.Contains(Join(i)))
            {
                suffixLen = n - i - 1; // the exception rule minus its leftmost label
                exception = true;
                break;
            }
        }
        if (!exception)
        {
            for (int i = 0; i < n; i++) // smaller i = longer suffix = more specific rule
            {
                if (rules.Contains(Join(i)) || (i + 1 < n && wild.Contains(Join(i + 1))))
                {
                    suffixLen = n - i;
                    break;
                }
            }
        }
        return n > suffixLen ? Join(n - suffixLen - 1) : host;
    }
}
