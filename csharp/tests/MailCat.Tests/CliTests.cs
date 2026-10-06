using System.Text.Json;
using MailCat.Cli;

namespace MailCat.Tests;

public class CliTests : IDisposable
{
    readonly string dir = Path.Combine(Path.GetTempPath(), $"mailcat-cli-{Guid.NewGuid():N}");

    public CliTests() => Directory.CreateDirectory(dir);

    public void Dispose() => Directory.Delete(dir, true);

    static (int Code, string Output) Run(string stdin, params string[] args)
    {
        var o = new StringWriter();
        var e = new StringWriter();
        int code = CliApp.Run(args, new StringReader(stdin), o, e);
        return (code, o.ToString() + e);
    }

    [Fact]
    public void EndToEnd()
    {
        var db = Path.Combine(dir, "t.db");
        var sample = Path.Combine(dir, "sample");
        var (code, output) = Run("", "sample-data", "-out", sample, "-per", "6");
        Assert.True(code == 0 && output.Contains("correos escritos"), output);

        (code, output) = Run("", "--db", db, "classify", Path.Combine(sample, "finance.bank"));
        Assert.True(code == 0 && output.Count(c => c == '\n') >= 6, output);

        // JSON mode: one object per line
        (code, output) = Run("", "--db", db, "classify", "-json", Path.Combine(sample, "newsletters", "newsletters-000.eml"));
        Assert.True(code == 0 && output.TrimStart().StartsWith('{') && output.Contains("\"decision\""), output);
        var id = JsonDocument.Parse(output.Split('\n')[0]).RootElement.GetProperty("email_id").GetString()!;
        Assert.NotEmpty(id);

        Assert.NotEqual(0, Run("", "--db", db, "classify", Path.Combine(sample, "missing")).Code);

        // interactive session: answer yes, confirm
        (code, output) = Run("s\ns\n", "--db", Path.Combine(dir, "i.db"), "classify", "-i", Path.Combine(sample, "finance.purchase", "finance_purchase-000.eml"));
        Assert.True(code == 0 && output.Contains('?') && output.Contains("Razones"), output);

        foreach (var args in new[] { new[] { "categories" }, ["questions"], ["rules"], ["metrics"], ["metrics", "-json"], ["list"] })
        {
            (code, output) = Run("", ["--db", db, .. args]);
            Assert.True(code == 0 && output != "", $"{string.Join(' ', args)}: {output}");
        }

        // move + explain + purge + backup
        (code, output) = Run("", "--db", db, "move", id, "finance.bank");
        Assert.True(code == 0 && output.Contains("Banco"), output);
        (code, output) = Run("", "--db", db, "explain", id);
        Assert.True(code == 0 && output.Contains("Razones"), output);
        (code, output) = Run("", "--db", db, "rules");
        Assert.True(code == 0 && !output.Contains("Aún no"), output);
        Assert.NotEqual(0, Run("", "--db", db, "move", id, "nonexistent").Code);
        (code, output) = Run("", "--db", db, "purge", "-all");
        Assert.True(code == 0 && output.Contains("purgado"), output);

        var bk = Path.Combine(dir, "bk.db");
        Assert.Equal(0, Run("", "--db", db, "backup", bk).Code);
        Assert.True(File.Exists(bk));
        Assert.NotEqual(0, Run("", "--db", db, "backup", bk).Code); // must not overwrite
    }

    [Fact]
    public void FeedbackCommandAcceptsFlagsInAnyPosition()
    {
        var db = Path.Combine(dir, "f.db");
        var sample = Path.Combine(dir, "sample");
        Run("", "sample-data", "-out", sample, "-per", "2");
        var (_, output) = Run("", "--db", db, "classify", "-json", Path.Combine(sample, "social", "social-000.eml"));
        var id = JsonDocument.Parse(output.Split('\n')[0]).RootElement.GetProperty("email_id").GetString()!;
        var (code, o1) = Run("", "--db", db, "feedback", id, "-category", "promotions");
        Assert.True(code == 0 && o1.Contains("Promociones"), o1);
        (code, o1) = Run("", "--db", db, "feedback", "-correct", id);
        Assert.True(code == 0, o1);
        Assert.NotEqual(0, Run("", "--db", db, "feedback", id).Code); // needs -correct or -category
    }

    [Fact]
    public void InitAndUsage()
    {
        var (code, output) = Run("", "init", "-dir", dir);
        Assert.True(code == 0 && output.Contains("mailcat.yaml"), output);
        Assert.NotEqual(0, Run("", "init", "-dir", dir).Code); // must not overwrite without -force
        Assert.Equal(0, Run("", "init", "-dir", dir, "-force").Code);

        // the generated config is usable
        var cfg = Path.Combine(dir, "mailcat.yaml");
        (code, output) = Run("", "--config", cfg, "--db", Path.Combine(dir, "x.db"), "categories");
        Assert.True(code == 0 && output.Contains("Banco"), output);

        Assert.NotEqual(0, Run("").Code); // no command
        Assert.NotEqual(0, Run("", "bogus").Code);
        var bad = Path.Combine(dir, "bad.yaml");
        File.WriteAllText(bad, "thresholds: {auto: 0.2, ask: 0.9}\n");
        Assert.NotEqual(0, Run("", "--config", bad, "categories").Code); // invalid config must fail at startup
    }

    [Fact]
    public void EvalCommand()
    {
        var sample = Path.Combine(dir, "sample");
        Run("", "sample-data", "-out", sample, "-per", "10");
        var (code, output) = Run("", "eval", "-data", sample);
        Assert.True(code == 0 && output.Contains("validation") && output.Contains("Curva de aprendizaje"), output);
    }
}
