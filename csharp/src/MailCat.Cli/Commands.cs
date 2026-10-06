using System.Globalization;
using System.Text.Json;
using MailCat.Categories;
using MailCat.Engine;
using MailCat.Eval;
using MailCat.Metrics;
using MailCat.Samples;

namespace MailCat.Cli;

static class Commands
{
    public static void Init(CliEnv env, string[] args)
    {
        var f = new Flags(args, ["force"], ["dir"]);
        var path = Path.Combine(f.Str("dir", "."), "mailcat.yaml");
        if (File.Exists(path) && !f.Bool("force")) throw new InvalidOperationException($"{path} ya existe (usa -force para sobrescribir)");
        File.WriteAllText(path, Config.MailCatConfig.DefaultYaml());
        env.Out.WriteLine($"Configuración escrita en {path}. Edita categorías, preguntas y reglas; luego prueba:");
        env.Out.WriteLine("  mailcat sample-data -out sample && mailcat classify -i sample/personal");
    }

    public static void SampleData(CliEnv env, string[] args)
    {
        var f = new Flags(args, [], ["out", "per", "seed"]);
        var outDir = f.Str("out");
        if (outDir == "") throw new UsageException("-out es obligatorio");
        int n = 0;
        foreach (var s in SampleGenerator.Generate(f.Int("seed", 42), f.Int("per", 40)))
        {
            var dir = Path.Combine(outDir, s.Category);
            Directory.CreateDirectory(dir);
            File.WriteAllBytes(Path.Combine(dir, s.Id + ".eml"), s.Eml);
            n++;
        }
        env.Out.WriteLine($"{n} correos escritos en {outDir}/<categoría>/*.eml");
    }

    public static void Eval(CliEnv env, string[] args)
    {
        var f = new Flags(args, ["json"], ["data", "train", "val", "window"]);
        var data = f.Str("data");
        if (data == "") throw new UsageException("-data es obligatorio");
        var built = CliApp.LoadConfig(env).Build();
        var items = Evaluator.LoadDir(data);
        foreach (var it in items)
        {
            if (!built.Tree.Has(it.Expected))
                throw new InvalidOperationException($"el directorio «{it.Expected}» no es una categoría de la configuración");
        }
        var res = Evaluator.Run(built, items, new EvalOptions
        {
            TrainPct = f.Int("train", 60), ValPct = f.Int("val", 20), Window = f.Int("window", 20),
        });
        env.Out.WriteLine(f.Bool("json") ? JsonSerializer.Serialize(res, Json.Options) : res.Text().TrimEnd('\n'));
    }

    public static void List(CliEnv env, MailCatEngine eng, string[] args)
    {
        var f = new Flags(args, [], ["category", "n"]);
        var ids = eng.Db.ListEmailIds(f.Str("category"), f.Int("n", 20));
        if (ids.Count == 0) env.Out.WriteLine("Aún no hay correos procesados.");
        foreach (var id in ids)
        {
            var o = eng.Get(id);
            var subject = eng.Db.GetEmail(id).Subject;
            env.Out.WriteLine($"{StateLabel(o.Decision),-11} {o.Path,-28} {o.Score * 100,5:F0}%  {id}  «{Trunc(subject, 50)}»");
        }
    }

    public static void Feedback(CliEnv env, MailCatEngine eng, string[] args)
    {
        var f = new Flags(args, ["correct"], ["category"]);
        if (f.Positional.Count != 1 || (!f.Bool("correct") && f.Str("category") == ""))
            throw new UsageException("uso: feedback <id> (-correct | -category X)");
        var id = f.Positional[0];
        var o = eng.Feedback(id, f.Bool("correct"), f.Str("category"));
        env.Out.WriteLine($"{id} → {o.Path} (aprendido: {Lower(o.Learned)})");
    }

    public static void Move(CliEnv env, MailCatEngine eng, string[] args)
    {
        if (args.Length != 2) throw new UsageException("uso: move <id> <categoría>");
        var o = eng.Move(args[0], args[1]);
        env.Out.WriteLine($"{args[0]} → {o.Path} (aprendido: {Lower(o.Learned)})");
    }

    public static void ExplainCmd(CliEnv env, MailCatEngine eng, string[] args)
    {
        if (args.Length != 1) throw new UsageException("uso: explain <id>");
        foreach (var l in eng.ExplainEmail(args[0]).Lines()) env.Out.WriteLine(l);
    }

    public static void Categories(CliEnv env, MailCatEngine eng) =>
        eng.Tree.Walk((c, depth) =>
            env.Out.WriteLine($"{new string(' ', depth * 2)}{c.Name}  ({c.Id}){(c.Enabled ? "" : "  [desactivada]")}"));

    public static void Questions(CliEnv env, MailCatEngine eng)
    {
        foreach (var q in eng.Db.Questions())
        {
            var state = !q.Validated ? "PENDIENTE de aprobación" : q.Disabled ? "desactivada" : "activa";
            env.Out.WriteLine($"{q.Id,-22} [{q.Source}, {state}] {q.Text}");
        }
    }

    public static void Rules(CliEnv env, MailCatEngine eng, string[] args)
    {
        var f = new Flags(args, [], ["category", "n"]);
        var rs = eng.Db.ListRules(f.Str("category"), f.Int("n", 50));
        if (rs.Count == 0) env.Out.WriteLine("Aún no se ha aprendido ninguna regla.");
        foreach (var r in rs)
        {
            env.Out.WriteLine(string.Create(CultureInfo.InvariantCulture,
                $"{r.Key,-46} {(r.Negative ? "≠" : "→")} {eng.Tree.Path(r.CategoryId),-20} peso {r.Weight:F2}  (confirmaciones {r.Confirmations}, rechazos {r.Rejections})"));
        }
    }

    public static void Metrics(CliEnv env, MailCatEngine eng, string[] args)
    {
        var f = new Flags(args, ["json"], []);
        var rep = Report.Compute(eng.Db);
        env.Out.WriteLine(f.Bool("json") ? JsonSerializer.Serialize(rep, Json.Indented) : rep.Text().TrimEnd('\n'));
    }

    public static void Purge(CliEnv env, MailCatEngine eng, string[] args)
    {
        var f = new Flags(args, ["all", "delete"], ["id"]);
        var now = DateTime.UtcNow;
        if (f.Bool("all"))
        {
            int n = eng.Db.PurgeAllContent(now);
            env.Out.WriteLine($"contenido purgado en {n} correos; las reglas aprendidas se conservan");
        }
        else if (f.Str("id") != "" && f.Bool("delete")) eng.Db.DeleteEmail(f.Str("id"));
        else if (f.Str("id") != "") eng.Db.PurgeContent(f.Str("id"), now);
        else throw new UsageException("uso: purge (-id X [-delete] | -all)");
    }

    public static void Backup(CliEnv env, MailCatEngine eng, string[] args)
    {
        if (args.Length != 1) throw new UsageException("uso: backup <destino.db>");
        if (File.Exists(args[0])) throw new InvalidOperationException($"{args[0]} ya existe");
        eng.Db.Backup(args[0]);
        env.Out.WriteLine($"copia escrita en {args[0]}");
    }

    // ── shared formatting ─────────────────────────────────────────────────────────────

    public static string StateLabel(string decision) => decision switch
    {
        MailCat.Explain.Decisions.Know => "SEGURO",
        MailCat.Explain.Decisions.Predict => "PREDICCIÓN",
        MailCat.Explain.Decisions.Ask => "PREGUNTA",
        MailCat.Explain.Decisions.Unknown => "DESCONOCIDO",
        MailCat.Explain.Decisions.User => "USUARIO",
        _ => decision,
    };

    public static string Trunc(string s, int n)
    {
        var runes = s.EnumerateRunes().ToList();
        return runes.Count <= n ? s : string.Concat(runes.Take(n - 1).Select(r => r.ToString())) + "…";
    }

    static string Lower(bool b) => b ? "true" : "false";
}
