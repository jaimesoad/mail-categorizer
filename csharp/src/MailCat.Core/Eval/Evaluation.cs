using System.Globalization;
using System.Text;
using MailCat.Categories;
using MailCat.Config;
using MailCat.Emails;
using MailCat.Engine;
using MailCat.Explain;
using MailCat.Questions;
using MailCat.Storage;

namespace MailCat.Eval;

/// <summary>One labelled email.</summary>
public sealed record Item(string Id, Email Email, string Expected);

/// <summary>Simulates the user answering questions about an email whose true category is known.</summary>
public interface IOracle
{
    /// <summary>The answer id, or null when the user would skip the question.</summary>
    string? Answer(Question q, string truth, CategoryTree tree);
}

/// <summary>
/// Answers with the most likely answer under the question's configured affinity for the true
/// category (inherited from its ancestors). Questions without information about the category are skipped.
/// </summary>
public sealed class AffinityOracle : IOracle
{
    public string? Answer(Question q, string truth, CategoryTree tree)
    {
        for (var cur = truth; cur != "";)
        {
            if (q.Affinity.TryGetValue(cur, out var row))
            {
                if (q.Type == QuestionType.Boolean)
                    return row.GetValueOrDefault(Question.Yes) >= 0.5 ? Question.Yes : Question.No;
                string? best = null;
                double bp = -1;
                foreach (var a in q.Answers)
                {
                    if (row.TryGetValue(a.Id, out var p) && p > bp)
                    {
                        best = a.Id;
                        bp = p;
                    }
                }
                return best;
            }
            var c = tree.Get(cur);
            if (c == null) break;
            cur = c.ParentId;
        }
        return null;
    }
}

public sealed class EvalOptions
{
    public int TrainPct { get; set; }
    public int ValPct { get; set; }
    public string Seed { get; set; } = "";
    public IOracle? Oracle { get; set; }
    /// <summary>Size of the learning-curve windows (default 20).</summary>
    public int Window { get; set; }
}

/// <summary>The results of one split.</summary>
public sealed class Phase
{
    public int N { get; set; }
    /// <summary>Exact category match.</summary>
    public double Accuracy { get; set; }
    /// <summary>Also counts an ancestor of the truth.</summary>
    public double LenientAccuracy { get; set; }
    /// <summary>Classified without any question.</summary>
    public double AutoRate { get; set; }
    /// <summary>Needed at least one question.</summary>
    public double QuestionRate { get; set; }
    public double AvgQuestions { get; set; }
    public double UnknownRate { get; set; }
    /// <summary>Among "know" decisions: the key safety figure (§41).</summary>
    public double AccuracyWhenSure { get; set; }
    public double MeanSureConfidence { get; set; }
    public int Sure { get; set; }
}

/// <summary>The average effort over a window of training emails.</summary>
public sealed record CurvePoint(int UpTo, double QuestionRate, double AvgQuestions, double CorrectionRate);

/// <summary>A held-out email that was classified wrongly (error analysis).</summary>
public sealed record Miss(string Id, string Expected, string Got, string Decision, double Score, string Subject);

public sealed class EvalResult
{
    public Phase Train { get; set; } = new();
    public Phase Validation { get; set; } = new();
    public Phase Test { get; set; } = new();
    public List<CurvePoint> LearningCurve { get; set; } = [];
    /// <summary>Validation + test errors.</summary>
    public List<Miss> Misses { get; set; } = [];

    /// <summary>Renders a result for terminals.</summary>
    public string Text()
    {
        var b = new StringBuilder();
        static string P(double f, int w = 5) => (f * 100).ToString("F1", CultureInfo.InvariantCulture).PadLeft(w);
        void Row(string name, Phase p) => b.AppendLine(string.Create(CultureInfo.InvariantCulture,
            $"{name,-11} n={p.N,-4} acierto {P(p.Accuracy)}%  (laxo {P(p.LenientAccuracy)}%)  auto {P(p.AutoRate)}%  con pregunta {P(p.QuestionRate)}%  preg/correo {p.AvgQuestions:F2}  desconocido {P(p.UnknownRate, 4)}%  acierto cuando está seguro {P(p.AccuracyWhenSure)}% (n={p.Sure}, conf. media {p.MeanSureConfidence:F2})"));
        Row("train", Train);
        Row("validation", Validation);
        Row("test", Test);
        if (Misses.Count > 0)
        {
            b.AppendLine().AppendLine("Errores en validación/test:");
            foreach (var m in Misses)
                b.AppendLine(string.Create(CultureInfo.InvariantCulture,
                    $"  {m.Id,-32} esperado {m.Expected,-16} obtuvo {m.Got,-16} ({m.Decision} {m.Score:F2}) «{m.Subject}»"));
        }
        b.AppendLine().AppendLine("Curva de aprendizaje (entrenamiento) — ¿pregunta cada vez menos?");
        foreach (var c in LearningCurve)
        {
            var bar = new string('█', (int)(c.QuestionRate * 40 + 0.5));
            b.AppendLine(string.Create(CultureInfo.InvariantCulture,
                $"  hasta {c.UpTo,4}  preguntas {P(c.QuestionRate)}% {bar,-40} correcciones {P(c.CorrectionRate, 4)}%"));
        }
        return b.ToString();
    }
}

/// <summary>
/// The offline evaluation of §35: a labelled dataset is split deterministically into training /
/// validation / test; the system learns online from the training split (with a simulated user),
/// and is then measured on held-out data without learning. It also reports the learning curve
/// that §40 defines as success: the system should ask less and less.
/// </summary>
public static class Evaluator
{
    /// <summary>Reads dir/&lt;category-id&gt;/*.eml.</summary>
    public static List<Item> LoadDir(string dir)
    {
        var items = new List<Item>();
        foreach (var c in Directory.GetDirectories(dir).OrderBy(d => d, StringComparer.Ordinal))
        {
            var cat = System.IO.Path.GetFileName(c);
            foreach (var f in Directory.GetFiles(c, "*.eml").OrderBy(f => f, StringComparer.Ordinal))
            {
                Email e;
                try { e = EmlParser.ParseFile(f); }
                catch (FormatException ex) { throw new FormatException($"{f}: {ex.Message}"); }
                items.Add(new Item(cat + "/" + System.IO.Path.GetFileNameWithoutExtension(f), e with { Id = "" }, cat));
            }
        }
        if (items.Count == 0) throw new FileNotFoundException($"no .eml files found under {dir}/<category>/");
        return items;
    }

    /// <summary>Divides items deterministically by a hash of their id.</summary>
    public static (List<Item> Train, List<Item> Val, List<Item> Test) Split(IEnumerable<Item> items, int trainPct, int valPct, string seed)
    {
        var hs = items.Select(it => (It: it, H: MailCatEngine.Fnv1aPublic(seed + "|" + it.Id)))
            .OrderBy(x => x.H).ThenBy(x => x.It.Id, StringComparer.Ordinal).ToList();
        List<Item> train = [], val = [], test = [];
        foreach (var x in hs)
        {
            int bucket = (int)(x.H / 100 % 100); // different bits than the ordering
            if (bucket < trainPct) train.Add(x.It);
            else if (bucket < trainPct + valPct) val.Add(x.It);
            else test.Add(x.It);
        }
        return (train, val, test);
    }

    /// <summary>Executes the evaluation on a fresh in-memory database.</summary>
    public static EvalResult Run(BuiltConfig built, IReadOnlyList<Item> items, EvalOptions opts)
    {
        if (opts.TrainPct == 0) { opts.TrainPct = 60; opts.ValPct = 20; }
        opts.Oracle ??= new AffinityOracle();
        if (opts.Window == 0) opts.Window = 20;
        if (opts.Seed == "") opts.Seed = "eval";

        using var db = Database.Open(":memory:");
        var eng = new MailCatEngine(db, built);
        var (train, val, test) = Split(items, opts.TrainPct, opts.ValPct, opts.Seed);
        var res = new EvalResult();
        (res.Train, res.LearningCurve) = TrainPhase(eng, train, opts);
        var frozen = eng.WithoutLearning();
        var (vp, m1) = Measure(frozen, val, opts, "val:");
        var (tp, m2) = Measure(frozen, test, opts, "test:");
        res.Validation = vp;
        res.Test = tp;
        res.Misses = [.. m1, .. m2];
        return res;
    }

    static Email UniqueEmail(Item it, string prefix) => it.Email with { Id = prefix + it.Id };

    /// <summary>Answers questions with the oracle until the session ends.</summary>
    static (Outcome Out, int Asked) Resolve(MailCatEngine eng, Outcome o, string truth, IOracle oracle)
    {
        int asked = 0;
        for (int guard = 0; o.NeedsQuestion && o.Question != null && guard < 30; guard++)
        {
            var q = eng.Questions.FirstOrDefault(x => x.Id == o.Question.Id) ?? new Question { Id = o.Question.Id };
            var ans = oracle.Answer(q, truth, eng.Tree);
            o = ans != null ? eng.Answer(o.EmailId, q.Id, ans) : eng.Skip(o.EmailId, q.Id);
            asked++;
        }
        return (o, asked);
    }

    static (Phase, List<CurvePoint>) TrainPhase(MailCatEngine eng, List<Item> items, EvalOptions opts)
    {
        var ph = new Acc();
        var curve = new List<CurvePoint>();
        int wQ = 0, wAsked = 0, wCorr = 0, wN = 0;
        for (int i = 0; i < items.Count; i++)
        {
            var it = items[i];
            Outcome o;
            int asked;
            try
            {
                o = eng.Process(UniqueEmail(it, "train:"));
                var first = o.Decision;
                (o, asked) = Resolve(eng, o, it.Expected, opts.Oracle!);
                ph.Add(eng, o, it.Expected, asked, first);
            }
            catch (Exception ex) { throw new InvalidOperationException($"{it.Id}: {ex.Message}", ex); }

            // A diligent user verifies every result: confirms right ones, fixes wrong ones.
            int corrected = 0;
            if (o.Decision == Decisions.Unknown) eng.Move(o.EmailId, it.Expected);
            else if (o.Category == it.Expected) eng.Feedback(o.EmailId, true);
            else
            {
                corrected = 1;
                eng.Feedback(o.EmailId, false, it.Expected);
            }
            wN++;
            if (asked > 0) wQ++;
            wAsked += asked;
            wCorr += corrected;
            if (wN == opts.Window || i == items.Count - 1)
            {
                curve.Add(new CurvePoint(i + 1, (double)wQ / wN, (double)wAsked / wN, (double)wCorr / wN));
                wQ = wAsked = wCorr = wN = 0;
            }
        }
        return (ph.ToPhase(), curve);
    }

    static (Phase, List<Miss>) Measure(MailCatEngine eng, List<Item> items, EvalOptions opts, string prefix)
    {
        var ph = new Acc();
        var misses = new List<Miss>();
        foreach (var it in items)
        {
            var o = eng.Process(UniqueEmail(it, prefix));
            var first = o.Decision;
            int asked;
            (o, asked) = Resolve(eng, o, it.Expected, opts.Oracle!);
            ph.Add(eng, o, it.Expected, asked, first);
            if (o.Category != it.Expected)
                misses.Add(new Miss(it.Id, it.Expected, o.Category, o.Decision, o.Score, it.Email.Subject));
        }
        return (ph.ToPhase(), misses);
    }

    sealed class Acc
    {
        int n, exact, lenient, auto, withQ, asked, unknown, sure, sureOk;
        double sureScore;

        public void Add(MailCatEngine eng, Outcome o, string truth, int askedNow, string firstDecision)
        {
            n++;
            if (o.Category == truth) { exact++; lenient++; }
            else if (eng.Tree.IsAncestor(o.Category, truth) && o.Category != CategoryTree.UnknownId) lenient++;
            if (askedNow == 0 && firstDecision == Decisions.Know) auto++;
            if (askedNow > 0) withQ++;
            asked += askedNow;
            if (o.Decision == Decisions.Unknown) unknown++;
            if (o.Decision == Decisions.Know)
            {
                sure++;
                sureScore += o.Score;
                if (o.Category == truth) sureOk++;
            }
        }

        public Phase ToPhase()
        {
            if (n == 0) return new Phase();
            double d = n;
            var p = new Phase
            {
                N = n, Accuracy = exact / d, LenientAccuracy = lenient / d, AutoRate = auto / d, QuestionRate = withQ / d,
                AvgQuestions = asked / d, UnknownRate = unknown / d, Sure = sure,
            };
            if (sure > 0)
            {
                p.AccuracyWhenSure = (double)sureOk / sure;
                p.MeanSureConfidence = sureScore / sure;
            }
            return p;
        }
    }
}
