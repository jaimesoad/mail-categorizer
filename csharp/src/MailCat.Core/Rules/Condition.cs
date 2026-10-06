using System.Globalization;
using System.Text.RegularExpressions;
using MailCat.Features;

namespace MailCat.Rules;

/// <summary>
/// A node of the condition tree (YAML representation). Exactly one of All, Any, Not or
/// Feature must be set. <see cref="ConditionCompiler.Compile"/> turns it into a matcher.
/// </summary>
public sealed class Condition
{
    public List<Condition>? All { get; set; }
    public List<Condition>? Any { get; set; }
    public Condition? Not { get; set; }

    public string? Feature { get; set; }
    public string? Op { get; set; }
    /// <summary>A scalar, or a list of scalars for the "in" operator.</summary>
    public object? Value { get; set; }
    /// <summary>Ignores feature values whose confidence is lower.</summary>
    public double MinConfidence { get; set; }
}

/// <summary>Operators supported by a leaf condition.</summary>
public static class Ops
{
    public const string Equal = "equals";
    public const string NotEqual = "not_equals";
    public const string Contains = "contains";
    public const string StartsWith = "starts_with";
    public const string EndsWith = "ends_with";
    public const string Regex = "regex";
    public const string Exists = "exists";
    public const string Gt = "gt";
    public const string Gte = "gte";
    public const string Lt = "lt";
    public const string Lte = "lte";
    public const string In = "in";
}

/// <summary>Evaluates a compiled condition against a feature set.</summary>
public interface IMatcher
{
    bool Match(FeatureSet s);
}

public static class ConditionCompiler
{
    /// <summary>Validates and compiles a condition tree.</summary>
    public static IMatcher Compile(Condition c)
    {
        int n = 0;
        if (c.All is { Count: > 0 }) n++;
        if (c.Any is { Count: > 0 }) n++;
        if (c.Not != null) n++;
        if (!string.IsNullOrEmpty(c.Feature)) n++;
        if (n != 1) throw new ConfigException($"condition must set exactly one of all/any/not/feature (got {n})");

        if (c.All is { Count: > 0 }) return new AndMatcher(CompileAll(c.All));
        if (c.Any is { Count: > 0 }) return new OrMatcher(CompileAll(c.Any));
        if (c.Not != null) return new NotMatcher(Compile(c.Not));
        return CompileLeaf(c);
    }

    static List<IMatcher> CompileAll(List<Condition> cs)
    {
        var o = new List<IMatcher>();
        for (int i = 0; i < cs.Count; i++)
        {
            try { o.Add(Compile(cs[i])); }
            catch (ConfigException ex) { throw new ConfigException($"[{i}]: {ex.Message}"); }
        }
        return o;
    }

    sealed class AndMatcher(List<IMatcher> ms) : IMatcher
    {
        public bool Match(FeatureSet s) => ms.All(m => m.Match(s));
    }

    sealed class OrMatcher(List<IMatcher> ms) : IMatcher
    {
        public bool Match(FeatureSet s) => ms.Any(m => m.Match(s));
    }

    sealed class NotMatcher(IMatcher m) : IMatcher
    {
        public bool Match(FeatureSet s) => !m.Match(s);
    }

    static IMatcher CompileLeaf(Condition c)
    {
        var l = new Leaf { Feature = c.Feature!, Op = string.IsNullOrEmpty(c.Op) ? Ops.Equal : c.Op, MinConf = c.MinConfidence };
        switch (l.Op)
        {
            case Ops.Exists:
                break;
            case Ops.Equal or Ops.NotEqual or Ops.Contains or Ops.StartsWith or Ops.EndsWith:
                if (c.Value == null) throw new ConfigException($"{c.Feature} {l.Op}: value required");
                l.Str = Scalar(c.Value).ToLowerInvariant();
                break;
            case Ops.Regex:
                try { l.Re = new Regex(Scalar(c.Value), RegexOptions.IgnoreCase | RegexOptions.CultureInvariant); }
                catch (ArgumentException ex) { throw new ConfigException($"{c.Feature} regex: {ex.Message}"); }
                break;
            case Ops.Gt or Ops.Gte or Ops.Lt or Ops.Lte:
                if (!double.TryParse(Scalar(c.Value), NumberStyles.Float, CultureInfo.InvariantCulture, out var f))
                    throw new ConfigException($"{c.Feature} {l.Op}: numeric value required");
                l.Num = f;
                break;
            case Ops.In:
                if (c.Value is not System.Collections.IEnumerable list || c.Value is string)
                    throw new ConfigException($"{c.Feature} in: non-empty list required");
                l.Set = [];
                foreach (var v in list) l.Set.Add(Scalar(v).ToLowerInvariant());
                if (l.Set.Count == 0) throw new ConfigException($"{c.Feature} in: non-empty list required");
                break;
            default:
                throw new ConfigException($"unknown operator \"{c.Op}\"");
        }
        return l;
    }

    internal static string Scalar(object? v) => v switch
    {
        null => "",
        string s => s,
        bool b => b ? "true" : "false",
        double d => d.ToString("R", CultureInfo.InvariantCulture),
        IFormattable f => f.ToString(null, CultureInfo.InvariantCulture),
        _ => v.ToString() ?? "",
    };

    sealed class Leaf : IMatcher
    {
        public string Feature = "";
        public string Op = "";
        public string Str = "";
        public double Num;
        public HashSet<string>? Set;
        public Regex? Re;
        public double MinConf;

        /// <summary>
        /// A feature may have several values; a leaf matches when any value satisfies it,
        /// except not_equals which requires that none equals the operand.
        /// </summary>
        public bool Match(FeatureSet s)
        {
            var vals = s.Values(Feature).Where(f => f.Confidence >= MinConf).ToList();
            switch (Op)
            {
                case Ops.Exists:
                    return vals.Count > 0;
                case Ops.NotEqual:
                    return vals.Count > 0 && vals.All(f => f.Value.ToLowerInvariant() != Str);
            }
            foreach (var f in vals)
            {
                var v = f.Value.ToLowerInvariant();
                bool ok = false;
                switch (Op)
                {
                    case Ops.Equal: ok = v == Str; break;
                    case Ops.Contains: ok = v.Contains(Str, StringComparison.Ordinal); break;
                    case Ops.StartsWith: ok = v.StartsWith(Str, StringComparison.Ordinal); break;
                    case Ops.EndsWith: ok = v.EndsWith(Str, StringComparison.Ordinal); break;
                    case Ops.Regex: ok = Re!.IsMatch(f.Value); break;
                    case Ops.In: ok = Set!.Contains(v); break;
                    case Ops.Gt or Ops.Gte or Ops.Lt or Ops.Lte:
                        if (!double.TryParse(f.Value, NumberStyles.Float, CultureInfo.InvariantCulture, out var n)) continue;
                        ok = Op switch { Ops.Gt => n > Num, Ops.Gte => n >= Num, Ops.Lt => n < Num, _ => n <= Num };
                        break;
                }
                if (ok) return true;
            }
            return false;
        }
    }
}
