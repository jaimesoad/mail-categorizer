using MailCat.Config;
using MailCat.Emails;
using MailCat.Eval;
using MailCat.Samples;

namespace MailCat.Tests;

public class EvalTests
{
    static List<Item> Dataset(int per) =>
        SampleGenerator.Generate(42, per)
            .Select(s => new Item(s.Category + "/" + s.Id, EmlParser.Parse(s.Eml) with { Id = "" }, s.Category)).ToList();

    [Fact]
    public void SplitIsDeterministicAndDisjoint()
    {
        var items = Dataset(20);
        var (a1, b1, c1) = Evaluator.Split(items, 60, 20, "s");
        var (a2, b2, c2) = Evaluator.Split(items, 60, 20, "s");
        Assert.Equal(items.Count, a1.Count + b1.Count + c1.Count);
        Assert.Equal(a1.Select(i => i.Id), a2.Select(i => i.Id));
        Assert.Equal((b1.Count, c1.Count), (b2.Count, c2.Count));
        var all = a1.Concat(b1).Concat(c1).Select(i => i.Id).ToList();
        Assert.Equal(all.Count, all.Distinct().Count());
        Assert.True(a1.Count >= items.Count / 2 && c1.Count > 0, $"unexpected proportions {a1.Count}/{b1.Count}/{c1.Count}");
    }

    /// <summary>The success criterion of §40: ask a lot at first, then less and less, while staying accurate and never confidently wrong.</summary>
    [Fact]
    public void SystemLearnsToAskLess()
    {
        var built = MailCatConfig.LoadOrDefault("").Build();
        var res = Evaluator.Run(built, Dataset(40), new EvalOptions { Window = 40 });
        Assert.True(res.LearningCurve.Count >= 3, "curve too short");
        var (first, last) = (res.LearningCurve[0], res.LearningCurve[^1]);
        Assert.True(last.QuestionRate < first.QuestionRate, $"question rate did not decrease: {first.QuestionRate:F2} -> {last.QuestionRate:F2}");
        Assert.True(last.QuestionRate <= 0.5 * first.QuestionRate + 0.05, $"question rate should at least halve: {first.QuestionRate:F2} -> {last.QuestionRate:F2}");
        Assert.True(res.Test.Accuracy >= 0.85, $"test accuracy {res.Test.Accuracy:F2} < 0.85");
        if (res.Test.Sure > 0) Assert.True(res.Test.AccuracyWhenSure >= 0.95, $"accuracy when sure {res.Test.AccuracyWhenSure:F2}");
        Assert.True(res.Test.AutoRate >= 0.4, $"auto rate on test too low: {res.Test.AutoRate:F2}");
        Assert.Contains("Curva de aprendizaje", res.Text());
    }

    [Fact]
    public void LoadDirReadsCategoryFolders()
    {
        var dir = Path.Combine(Path.GetTempPath(), $"mailcat-eval-{Guid.NewGuid():N}");
        try
        {
            foreach (var s in SampleGenerator.Generate(1, 2))
            {
                Directory.CreateDirectory(Path.Combine(dir, s.Category));
                File.WriteAllBytes(Path.Combine(dir, s.Category, s.Id + ".eml"), s.Eml);
            }
            var items = Evaluator.LoadDir(dir);
            Assert.Equal(SampleGenerator.Categories().Count * 2, items.Count);
            Assert.All(items, i => Assert.StartsWith(i.Expected + "/", i.Id));
        }
        finally { Directory.Delete(dir, true); }
        Assert.Throws<DirectoryNotFoundException>(() => Evaluator.LoadDir(dir));
    }
}
