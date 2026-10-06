using System.Text.RegularExpressions;
using MailCat.Emails;
using MailCat.Text;

namespace MailCat.Features;

/// <summary>
/// Derives features from the subject and body (layer 7.3). Raw values (amounts, ids,
/// phone numbers) are intentionally not stored: only their presence is, which is all
/// classification needs and is safer.
/// </summary>
public sealed class ContentExtractor : IFeatureExtractor
{
    const RegexOptions Opts = RegexOptions.IgnoreCase | RegexOptions.CultureInvariant;

    static readonly Regex ReAmount = new(@"(?:[$€£]\s?\d[\d.,]*|\b\d[\d.,]*\s?(?:usd|eur|mxn|gbp|cop|ars|clp|dolares|euros|pesos)\b)", Opts);
    static readonly Regex ReInvNum = new(@"\b(?:invoice|factura|folio|inv)\b[^\n\d]{0,16}[a-z]{0,4}-?\d{3,}", Opts);
    static readonly Regex ReOrdNum = new(@"\b(?:order|pedido|orden)\b[^\n\d]{0,16}[a-z]{0,4}-?\d{4,}", Opts);
    static readonly Regex ReTxId = new(@"\b(?:transaction|transaccion|referencia|reference|confirmation|confirmacion|auth(?:orization)?|autorizacion)\b[^\n\d]{0,16}[a-z0-9-]{0,6}\d{4,}", Opts);
    static readonly Regex RePhone = new(@"\+?\d[\d\s().-]{7,}\d", RegexOptions.CultureInvariant);
    static readonly Regex ReDate = new(@"\b(?:\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\d{4}-\d{2}-\d{2}|\d{1,2}\s+(?:de\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec|ene|abr|ago|dic)[a-z]*\.?(?:\s+(?:de\s+)?\d{4})?)\b", Opts);
    static readonly Regex ReUrl = new(@"https?://[^\s<>""')\]]+", Opts);

    public IEnumerable<Feature> Extract(Email e)
    {
        var text = e.Subject + "\n" + e.BodyText;
        var folded = TextUtil.Fold(text);
        var o = new List<Feature>();
        void Add(string name, string v, double c) => o.Add(new Feature(name, v, c, FeatureSource.Pattern));

        if (ReAmount.IsMatch(text)) Add(FeatureNames.ContentContainsAmount, "true", 0.95);
        if (ReInvNum.IsMatch(folded)) Add(FeatureNames.ContentHasInvoiceNumber, "true", 0.9);
        if (ReOrdNum.IsMatch(folded)) Add(FeatureNames.ContentHasOrderNumber, "true", 0.9);
        if (ReTxId.IsMatch(folded)) Add(FeatureNames.ContentHasTxId, "true", 0.85);
        if (HasPhone(text)) Add(FeatureNames.ContentHasPhone, "true", 0.7);
        if (ReDate.IsMatch(folded)) Add(FeatureNames.ContentHasDate, "true", 0.8);

        // URLs: keep only registrable domains, never full URLs (they often carry tokens).
        var urls = ReUrl.Matches(text);
        if (urls.Count > 0)
        {
            Add(FeatureNames.ContentUrlCount, urls.Count.ToString(), 1);
            var seen = new HashSet<string>(StringComparer.Ordinal);
            var doms = new List<string>();
            foreach (Match m in urls)
            {
                if (!Uri.TryCreate(m.Value, UriKind.Absolute, out var uri) || uri.Host == "") continue;
                var d = PublicSuffix.RegistrableDomain(uri.Host);
                if (seen.Add(d)) doms.Add(d);
            }
            doms.Sort(StringComparer.Ordinal);
            foreach (var d in doms.Take(5)) Add(FeatureNames.ContentUrlDomain, d, 0.9);
        }

        var (lang, conf) = DetectLanguage(folded);
        if (lang != "") Add(FeatureNames.ContentLanguage, lang, conf);

        foreach (var t in UniqueTokens(TextUtil.Tokens(e.Subject), 12))
            o.Add(new Feature(FeatureNames.SubjectTokens, t, 0.8, FeatureSource.Parser));
        foreach (var k in TopKeywords(TextUtil.Tokens(text), 8))
            Add(FeatureNames.ContentKeyword, k, 0.5);
        return o;
    }

    static bool HasPhone(string s)
    {
        foreach (Match m in RePhone.Matches(s).Take(5))
        {
            int digits = m.Value.Count(c => c is >= '0' and <= '9');
            if (digits >= 9 && digits <= 15 && (m.Value.IndexOfAny(['+', '(', ')', '-', ' ']) >= 0 || m.Value.StartsWith('+')))
                return true;
        }
        return false;
    }

    static (string Lang, double Conf) DetectLanguage(string folded)
    {
        var words = SplitAsciiWords(folded);
        if (words.Count < 4) return ("", 0);
        var langs = TextUtil.LanguageStopwords;
        string best = "";
        int bestN = 0, total = 0;
        foreach (var l in langs.Keys.OrderBy(k => k, StringComparer.Ordinal))
        {
            int n = words.Count(w => langs[l].Contains(w));
            total += n;
            if (n > bestN) { best = l; bestN = n; }
        }
        if (bestN < 2 || total == 0) return ("", 0);
        double share = (double)bestN / total;
        if (share < 0.5) return ("", 0);
        double ev = bestN / (bestN + 3.0); // confidence grows with both dominance and amount of evidence
        return (best, Round2(share * ev));
    }

    static List<string> SplitAsciiWords(string s)
    {
        var o = new List<string>();
        int start = -1;
        for (int i = 0; i <= s.Length; i++)
        {
            bool w = i < s.Length && s[i] is >= 'a' and <= 'z';
            if (w) { if (start < 0) start = i; }
            else if (start >= 0) { o.Add(s[start..i]); start = -1; }
        }
        return o;
    }

    internal static double Round2(double f) => (int)(f * 100 + 0.5) / 100.0;

    static List<string> UniqueTokens(List<string> input, int max)
    {
        var seen = new HashSet<string>(StringComparer.Ordinal);
        var o = new List<string>();
        foreach (var t in input)
        {
            if (!seen.Add(t)) continue;
            o.Add(t);
            if (o.Count == max) break;
        }
        return o;
    }

    static List<string> TopKeywords(List<string> tokens, int n)
    {
        var freq = new Dictionary<string, int>(StringComparer.Ordinal);
        foreach (var t in tokens) freq[t] = freq.GetValueOrDefault(t) + 1;
        return freq.Where(kv => kv.Value >= 2 || freq.Count <= n)
            .OrderByDescending(kv => kv.Value).ThenBy(kv => kv.Key, StringComparer.Ordinal)
            .Take(n).Select(kv => kv.Key).ToList();
    }
}
