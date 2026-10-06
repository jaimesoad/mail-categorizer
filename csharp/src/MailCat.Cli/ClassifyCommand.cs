using System.Globalization;
using System.Text.Json;
using MailCat.Categories;
using MailCat.Emails;
using MailCat.Engine;
using MailCat.Explain;
using MailCat.Questions;

namespace MailCat.Cli;

/// <summary>The user quit (q) or stdin ended.</summary>
sealed class QuitException : Exception;

static class ClassifyCommand
{
    public static void Run(CliEnv env, MailCatEngine eng, string[] args)
    {
        var f = new Flags(args, ["i", "json"], []);
        if (f.Positional.Count == 0) throw new UsageException("indica al menos un archivo .eml o directorio");
        bool interactive = f.Bool("i");
        try
        {
            foreach (var path in f.Positional)
            {
                foreach (var file in EmlFiles(path))
                {
                    Email m;
                    try { m = EmlParser.ParseFile(file); }
                    catch (FormatException ex) { throw new FormatException($"{file}: {ex.Message}"); }
                    var o = eng.Process(m);
                    if (interactive) Converse(env, eng, m, o);
                    else if (f.Bool("json")) env.Out.WriteLine(JsonSerializer.Serialize(o, Json.Options));
                    else PrintLine(env.Out, m, o);
                }
            }
        }
        catch (QuitException)
        {
            env.Out.WriteLine("\nSesión terminada.");
        }
    }

    /// <summary>A .eml file, or every .eml under a directory (recursively, in lexical order so runs are reproducible).</summary>
    static IEnumerable<string> EmlFiles(string path)
    {
        if (Directory.Exists(path))
        {
            return Directory.EnumerateFiles(path, "*", SearchOption.AllDirectories)
                .Where(p => p.EndsWith(".eml", StringComparison.OrdinalIgnoreCase))
                .OrderBy(p => p, StringComparer.Ordinal);
        }
        if (!File.Exists(path)) throw new FileNotFoundException($"no existe {path}");
        return [path];
    }

    static void PrintLine(TextWriter w, Email m, Outcome o)
    {
        w.Write(string.Create(CultureInfo.InvariantCulture,
            $"{Commands.StateLabel(o.Decision),-11} {o.Path,-28} {o.Score * 100,5:F0}%  {o.Method,-12} {o.EmailId}  «{Commands.Trunc(m.Subject, 50)}»"));
        if (o.AlreadyProcessed) w.Write(" (ya procesado)");
        if (o is { NeedsQuestion: true, Question: not null }) w.Write($"\n            ↳ pregunta pendiente: {o.Question.Text}");
        w.WriteLine();
    }

    static string Prompt(CliEnv env, string msg)
    {
        env.Out.Write(msg);
        env.Out.Flush();
        return env.In.ReadLine()?.Trim() ?? throw new QuitException();
    }

    /// <summary>Runs the Akinator loop for one email on the terminal.</summary>
    static void Converse(CliEnv env, MailCatEngine eng, Email m, Outcome o)
    {
        var w = env.Out;
        w.WriteLine($"\n━━ {o.EmailId}\n   De: {m.From.Email}\n   Asunto: {Commands.Trunc(m.Subject, 80)}");
        if (o.AlreadyProcessed) w.WriteLine("   (ya procesado antes)");

        while (o is { NeedsQuestion: true, Question: not null })
        {
            var q = o.Question;
            w.WriteLine($"\n? {q.Text}");
            var hint = "[s]í / [n]o";
            if (q.Type != QuestionType.Boolean)
                hint = string.Join(" | ", q.Answers.Select(a => $"{a.Id}={a.Label}"));
            var line = Prompt(env, $"  {hint}, [o]mitir, [c]ategoría manual, [q] salir > ");
            try
            {
                switch (line.ToLowerInvariant())
                {
                    case "q" or "quit":
                        throw new QuitException();
                    case "o" or "omitir" or "skip" or "":
                        o = eng.Skip(o.EmailId, q.Id);
                        break;
                    case "c":
                        var cat = ChooseCategory(env, eng);
                        if (cat == "") continue;
                        o = eng.Move(o.EmailId, cat);
                        break;
                    default:
                        var ans = line;
                        if (q.Type == QuestionType.Boolean && line.ToLowerInvariant() is "s" or "si" or "sí") ans = "yes";
                        o = eng.Answer(o.EmailId, q.Id, ans);
                        break;
                }
            }
            catch (InvalidRequestException ex)
            {
                w.WriteLine($"  ✗ {ex.Message}");
            }
        }
        Finish(env, eng, o);
    }

    static void Finish(CliEnv env, MailCatEngine eng, Outcome o)
    {
        var w = env.Out;
        w.WriteLine();
        foreach (var l in o.Explanation.Lines()) w.WriteLine("  " + l);
        if (o.Learned) w.WriteLine("  ✓ Aprendido: la próxima vez preguntaré menos.");

        if (o.Decision == Decisions.Unknown)
        {
            w.WriteLine("\n  No sé dónde va. Elige una categoría (o Enter para dejarlo sin clasificar):");
            var cat = ChooseCategory(env, eng);
            if (cat == "") return;
            var moved = eng.Move(o.EmailId, cat);
            w.WriteLine($"  → {moved.Path} (aprendido)");
        }
        else if (o.RequestConfirmation)
        {
            var line = Prompt(env, $"\n  ¿Es correcto «{o.Path}»? [S/n/categoría] > ");
            try
            {
                switch (line.ToLowerInvariant())
                {
                    case "" or "s" or "si" or "sí" or "y" or "yes":
                        eng.Feedback(o.EmailId, true);
                        break;
                    case "n" or "no":
                        var cat = ChooseCategory(env, eng);
                        if (cat != "") eng.Feedback(o.EmailId, false, cat);
                        break;
                    default:
                        eng.Feedback(o.EmailId, false, line);
                        break;
                }
            }
            catch (InvalidRequestException ex)
            {
                w.WriteLine($"  ✗ {ex.Message}");
            }
        }
    }

    /// <summary>Lists the enabled categories and returns the chosen id ("" = none).</summary>
    static string ChooseCategory(CliEnv env, MailCatEngine eng)
    {
        var ids = new List<string>();
        eng.Tree.Walk((c, depth) =>
        {
            if (c.Id == CategoryTree.UnknownId || !eng.Tree.Enabled(c.Id)) return;
            ids.Add(c.Id);
            env.Out.WriteLine($"   {ids.Count,2}. {new string(' ', depth * 2)}{c.Name}  ({c.Id})");
        });
        var line = Prompt(env, "   número o id > ");
        if (line == "") return "";
        if (int.TryParse(line, NumberStyles.Integer, CultureInfo.InvariantCulture, out var n) && n >= 1 && n <= ids.Count)
            return ids[n - 1];
        foreach (var id in ids)
        {
            if (string.Equals(id, line, StringComparison.OrdinalIgnoreCase)) return id;
        }
        env.Out.WriteLine("   ✗ categoría no válida");
        return "";
    }
}
