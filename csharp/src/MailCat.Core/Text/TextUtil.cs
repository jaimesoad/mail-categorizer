using System.Globalization;
using System.Text;

namespace MailCat.Text;

/// <summary>Small text helpers shared by feature extraction and similarity.</summary>
public static class TextUtil
{
    static readonly Dictionary<string, HashSet<string>> LangStop = new()
    {
        ["es"] = Words("el la los las un una unos unas de del al y o u en por para con sin que se su sus es son fue ser esta este estos estas esto como mas pero muy ya no si le les lo me mi tu te nos nuestro hemos has han hay ha fue desde entre sobre tambien cuando donde hasta todo todos usted ustedes gracias hola saludos"),
        ["en"] = Words("the a an and or of to in on for with without that this these those is are was were be been it its as at by from your you we our they their he she his her not have has had will would can could do does did but if so than then there here all any please thanks hello regards"),
        ["pt"] = Words("o os as um uma uns umas de do da dos das em no na nos nas por para com sem que se seu sua seus suas e ou mas muito ja nao sim voce voces nosso obrigado ola"),
        ["fr"] = Words("le la les un une des du de et ou en dans pour avec sans que qui est sont ete etre vous nous votre vos notre nos ce cette ces mais pas plus tres merci bonjour"),
    };

    static readonly HashSet<string> Stop = LangStop.Values.SelectMany(s => s).ToHashSet(StringComparer.Ordinal);

    static HashSet<string> Words(string s) =>
        s.Split(' ', StringSplitOptions.RemoveEmptyEntries).ToHashSet(StringComparer.Ordinal);

    /// <summary>Stop-word sets by language code, used for language detection.</summary>
    public static IReadOnlyDictionary<string, HashSet<string>> LanguageStopwords => LangStop;

    /// <summary>
    /// Lower-cases s and strips diacritics ("Transacción" → "transaccion"). Table based, so it
    /// does not depend on ICU or the globalization mode.
    /// </summary>
    public static string Fold(string s)
    {
        var lower = s.ToLowerInvariant();
        var sb = new StringBuilder(lower.Length);
        foreach (var c in lower)
        {
            if (c < 128) sb.Append(c);
            else if (Diacritics.TryFold(c, out var f)) sb.Append(f);
            else if (CharUnicodeInfo.GetUnicodeCategory(c) != UnicodeCategory.NonSpacingMark) sb.Append(c);
        }
        return sb.ToString();
    }

    /// <summary>
    /// Splits folded text into alphanumeric tokens of at least 3 characters, with
    /// stop-words removed. Pure-number tokens are dropped.
    /// </summary>
    public static List<string> Tokens(string s)
    {
        var folded = Fold(s);
        var output = new List<string>();
        int start = -1;
        for (int i = 0; i <= folded.Length; i++)
        {
            bool isWord = i < folded.Length && char.IsLetterOrDigit(folded[i]);
            if (isWord)
            {
                if (start < 0) start = i;
                continue;
            }
            if (start >= 0)
            {
                var t = folded[start..i];
                start = -1;
                if (t.Length < 3 || IsNumber(t) || IsStopword(t)) continue;
                output.Add(t);
            }
        }
        return output;
    }

    static bool IsNumber(string t) => t.All(char.IsDigit);

    public static bool IsStopword(string folded) => Stop.Contains(folded);
}
