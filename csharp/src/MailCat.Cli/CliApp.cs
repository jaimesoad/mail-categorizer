using MailCat.Config;
using MailCat.Engine;
using MailCat.Storage;

namespace MailCat.Cli;

/// <summary>Everything a command needs: streams and the global options.</summary>
sealed class CliEnv
{
    public required TextReader In { get; init; }
    public required TextWriter Out { get; init; }
    public required TextWriter Err { get; init; }
    public string ConfigPath { get; set; } = "";
    public string DbPath { get; set; } = "";
    /// <summary>--config was given on the command line (a missing file is then an error).</summary>
    public bool Explicit { get; set; }
}

static class CliApp
{
    const string Usage = """
        mailcat — clasificador de correo adaptativo (estilo Akinator)

        Uso: mailcat [--config archivo.yaml] [--db archivo.db] <comando> [argumentos]

        Comandos:
          init [-dir .] [-force]                 crea mailcat.yaml con la configuración de ejemplo
          classify [-i] [-json] <.eml|dir>...    clasifica correos (-i: modo interactivo con preguntas)
          list [-category X] [-n 20]             lista los correos procesados (id, categoría, asunto)
          feedback <id> -correct | -category X   confirma o corrige la clasificación de un correo
          move <id> <categoría>                  archiva manualmente ("Move to...")
          explain <id>                           ¿por qué quedó en esa categoría?
          categories                             muestra el árbol de categorías
          questions                              lista las preguntas disponibles
          rules [-category X] [-n 50]            muestra lo aprendido
          metrics [-json]                        métricas de calidad y esfuerzo
          eval -data <dir> [-json]               evaluación offline (dir/<categoría>/*.eml)
          sample-data -out <dir> [-per 40]       genera un buzón sintético reproducible
          purge (-id X [-delete] | -all)         borra el contenido de correos conservando lo aprendido
          backup <destino.db>                    copia consistente de la base de datos

        Opciones globales (antes del comando):
          --config <archivo>   configuración YAML (por defecto ./mailcat.yaml, $MAILCAT_CONFIG o la embebida)
          --db <archivo>       ruta de la base SQLite (sobrescribe la config)
        """;

    public static int Run(string[] args, TextReader input, TextWriter output, TextWriter error)
    {
        var env = new CliEnv { In = input, Out = output, Err = error, ConfigPath = Environment.GetEnvironmentVariable("MAILCAT_CONFIG") ?? "" };
        try
        {
            return Dispatch(args, env);
        }
        catch (UsageException ex)
        {
            error.WriteLine($"error: {ex.Message}");
            return 2;
        }
        catch (Exception ex) when (ex is ConfigException or InvalidRequestException or ConflictException or NotFoundException
                                       or IOException or FormatException or InvalidOperationException
                                       or Microsoft.Data.Sqlite.SqliteException or UnauthorizedAccessException)
        {
            error.WriteLine($"error: {ex.Message}");
            return 1;
        }
    }

    static int Dispatch(string[] args, CliEnv env)
    {
        int i = 0;
        for (; i < args.Length; i++)
        {
            var a = args[i];
            if (a is "--config" or "-config" or "-c")
            {
                env.ConfigPath = i + 1 < args.Length ? args[++i] : throw new UsageException("--config necesita un valor");
                env.Explicit = true;
            }
            else if (a is "--db" or "-db")
            {
                env.DbPath = i + 1 < args.Length ? args[++i] : throw new UsageException("--db necesita un valor");
            }
            else break;
        }
        if (i >= args.Length)
        {
            env.Err.WriteLine(Usage);
            throw new UsageException("falta el comando");
        }
        var cmd = args[i];
        var rest = args[(i + 1)..];

        switch (cmd)
        {
            case "help" or "-h" or "--help":
                env.Out.WriteLine(Usage);
                return 0;
            case "init": Commands.Init(env, rest); break;
            case "sample-data": Commands.SampleData(env, rest); break;
            case "eval": Commands.Eval(env, rest); break;
            case "classify": WithEngine(env, eng => ClassifyCommand.Run(env, eng, rest)); break;
            case "list": WithEngine(env, eng => Commands.List(env, eng, rest)); break;
            case "feedback": WithEngine(env, eng => Commands.Feedback(env, eng, rest)); break;
            case "move": WithEngine(env, eng => Commands.Move(env, eng, rest)); break;
            case "explain": WithEngine(env, eng => Commands.ExplainCmd(env, eng, rest)); break;
            case "categories": WithEngine(env, eng => Commands.Categories(env, eng)); break;
            case "questions": WithEngine(env, eng => Commands.Questions(env, eng)); break;
            case "rules": WithEngine(env, eng => Commands.Rules(env, eng, rest)); break;
            case "metrics": WithEngine(env, eng => Commands.Metrics(env, eng, rest)); break;
            case "purge": WithEngine(env, eng => Commands.Purge(env, eng, rest)); break;
            case "backup": WithEngine(env, eng => Commands.Backup(env, eng, rest)); break;
            default:
                env.Err.WriteLine(Usage);
                throw new UsageException($"comando desconocido «{cmd}»");
        }
        return 0;
    }

    /// <summary>Resolves the configuration: --config, ./mailcat.yaml, or the embedded default.</summary>
    public static MailCatConfig LoadConfig(CliEnv env)
    {
        var path = env.ConfigPath;
        if (path == "")
        {
            if (File.Exists("mailcat.yaml")) path = "mailcat.yaml";
        }
        else if (!File.Exists(path) && !env.Explicit)
        {
            path = ""; // MAILCAT_CONFIG points to a file that does not exist yet: use the embedded default
        }
        var cfg = MailCatConfig.LoadOrDefault(path);
        if (env.DbPath != "") cfg.Database = env.DbPath;
        return cfg;
    }

    static void WithEngine(CliEnv env, Action<MailCatEngine> fn)
    {
        var cfg = LoadConfig(env);
        var built = cfg.Build();
        using var db = Database.Open(cfg.Database);
        fn(new MailCatEngine(db, built));
    }
}
