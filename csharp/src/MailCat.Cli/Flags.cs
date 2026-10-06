using System.Globalization;

namespace MailCat.Cli;

/// <summary>
/// Minimal flag parser: accepts "-x", "--x", "-x value" and "-x=value", in any position
/// (so both "feedback ID -correct" and "feedback -correct ID" work). Everything else is positional.
/// </summary>
sealed class Flags
{
    readonly HashSet<string> boolFlags;
    readonly HashSet<string> valueFlags;
    readonly Dictionary<string, string> values = new(StringComparer.Ordinal);
    readonly HashSet<string> set = new(StringComparer.Ordinal);

    public List<string> Positional { get; } = [];

    public Flags(string[] args, string[] boolFlags, string[] valueFlags)
    {
        this.boolFlags = [.. boolFlags];
        this.valueFlags = [.. valueFlags];
        for (int i = 0; i < args.Length; i++)
        {
            var a = args[i];
            if (a == "--")
            {
                Positional.AddRange(args[(i + 1)..]);
                break;
            }
            if (a.Length < 2 || a[0] != '-' || (a.Length > 1 && char.IsDigit(a[1])))
            {
                Positional.Add(a);
                continue;
            }
            var name = a.TrimStart('-');
            string? inline = null;
            int eq = name.IndexOf('=');
            if (eq >= 0)
            {
                inline = name[(eq + 1)..];
                name = name[..eq];
            }
            if (this.boolFlags.Contains(name))
            {
                set.Add(name);
                values[name] = inline ?? "true";
            }
            else if (this.valueFlags.Contains(name))
            {
                if (inline == null)
                {
                    if (i + 1 >= args.Length) throw new UsageException($"la opción -{name} necesita un valor");
                    inline = args[++i];
                }
                set.Add(name);
                values[name] = inline;
            }
            else throw new UsageException($"opción desconocida: {a}");
        }
    }

    public bool Has(string name) => set.Contains(name);

    public bool Bool(string name) => values.TryGetValue(name, out var v) && v != "false";

    public string Str(string name, string def = "") => values.TryGetValue(name, out var v) ? v : def;

    public int Int(string name, int def)
    {
        if (!values.TryGetValue(name, out var v)) return def;
        return int.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out var n)
            ? n
            : throw new UsageException($"-{name}: «{v}» no es un número");
    }
}

/// <summary>Bad command line usage (printed without a stack trace).</summary>
sealed class UsageException(string message) : Exception(message);
