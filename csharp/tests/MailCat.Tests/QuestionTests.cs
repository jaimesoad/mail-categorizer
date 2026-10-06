using MailCat.Categories;
using MailCat.Classification;
using MailCat.Features;
using MailCat.Questions;

namespace MailCat.Tests;

public class QuestionTests
{
    static CategoryTree Tree() => new(
    [
        new("finance", "Finance"), new("finance.bank", "Bank", "finance"), new("finance.purchase", "Purchase", "finance"), new("work", "Work"),
    ]);

    static Question BoolQ(string id, string feature, Dictionary<string, double>? aff = null)
    {
        var q = new Question { Id = id, Text = $"¿{id}?", Feature = feature, Type = QuestionType.Boolean, Validated = true };
        foreach (var (c, p) in aff ?? []) q.Affinity[c] = new Dictionary<string, double> { [Question.Yes] = p };
        q.Normalize();
        return q;
    }

    static List<Candidate> Cands(params (string, double)[] cs) => cs.Select(c => new Candidate(c.Item1, c.Item2)).ToList();

    sealed class FakeStats(Dictionary<string, QuestionStats> s) : IQuestionStatsProvider
    {
        public Dictionary<string, QuestionStats> QuestionStats() => s;
    }

    static readonly SelectorSession Empty = new();

    static SelectorSession Sess(List<Candidate> c, FeatureSet? f = null, params string[] asked) =>
        new() { Candidates = c, Features = f ?? new FeatureSet(), Asked = [.. asked] };

    [Fact]
    public void InformationGainPerfectSplitIsOneBit()
    {
        var q = BoolQ("is_purchase", "context.is_purchase", new() { ["finance.purchase"] = 1, ["finance.bank"] = 0 });
        var g = new QuestionModel(q, Tree()).InformationGain(Cands(("finance.bank", 0.5), ("finance.purchase", 0.5)));
        Assert.InRange(g, 0.5, 1.0001); // prior smoothing keeps it slightly below 1
    }

    [Fact]
    public void UninformativeQuestionHasZeroGain()
    {
        var g = new QuestionModel(BoolQ("meh", "context.meh"), Tree()).InformationGain(Cands(("finance.bank", 0.5), ("finance.purchase", 0.5)));
        Assert.InRange(g, -1e-9, 1e-9);
    }

    [Fact]
    public void SelectorPrefersMostInformative()
    {
        var good = BoolQ("is_purchase", "context.is_purchase", new() { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 });
        var weak = BoolQ("is_important", "context.is_important", new() { ["finance.purchase"] = 0.6, ["finance.bank"] = 0.5 });
        var sel = new QuestionSelector(Tree(), null) { Questions = [weak, good] };
        var got = sel.Select(Sess(Cands(("finance.bank", 0.45), ("finance.purchase", 0.45), ("unknown", 0.1))));
        Assert.Equal("is_purchase", got?.Question.Id);
    }

    [Fact]
    public void SelectorRespectsExclusions()
    {
        var q = BoolQ("is_purchase", "content.contains_receipt", new() { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 });
        var cands = Cands(("finance.bank", 0.5), ("finance.purchase", 0.5));
        var sel = new QuestionSelector(Tree(), null) { Questions = [q] };
        Assert.NotNull(sel.Select(Sess(cands)));
        Assert.Null(sel.Select(Sess(cands, null, "is_purchase"))); // already asked in this session
        var known = new FeatureSet([new Feature("content.contains_receipt", "true", 0.97, "")]);
        Assert.Null(sel.Select(Sess(cands, known))); // the system already knows the feature

        var d = BoolQ("is_purchase", "content.contains_receipt", new() { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 });
        d.Disabled = true;
        var u = BoolQ("is_purchase", "content.contains_receipt", new() { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 });
        u.Validated = false;
        sel.Questions = [d, u];
        Assert.Null(sel.Select(Sess(cands))); // disabled / unvalidated

        sel.Questions = [q];
        Assert.Null(sel.Select(Sess(Cands(("work", 1))))); // a single confident candidate: nothing to separate
    }

    [Fact]
    public void ComplexityPenaltyDemotesLongQuestions()
    {
        var aff = new Dictionary<string, double> { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 };
        var shortQ = BoolQ("short", "context.a", aff);
        var longQ = BoolQ("long", "context.b", aff);
        longQ.Text = "¿Este correo electrónico que acabas de recibir en tu bandeja de entrada principal se relaciona con alguna compra que hiciste recientemente en línea o en una tienda física cercana?";
        var sel = new QuestionSelector(Tree(), null) { Questions = [longQ, shortQ] };
        var r = sel.Rank(Sess(Cands(("finance.bank", 0.5), ("finance.purchase", 0.5))));
        Assert.Equal(2, r.Count);
        Assert.Equal("short", r[0].Question.Id);
        Assert.True(r[0].Utility > r[1].Utility);
    }

    [Fact]
    public void LowResponseRateDemotesQuestion()
    {
        var aff = new Dictionary<string, double> { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 };
        var stats = new FakeStats(new()
        {
            ["a"] = new QuestionStats { Shown = 50, Answered = 2 },
            ["b"] = new QuestionStats { Shown = 50, Answered = 48 },
        });
        var sel = new QuestionSelector(Tree(), stats) { Questions = [BoolQ("a", "context.a", aff), BoolQ("b", "context.b", aff)] };
        var r = sel.Rank(Sess(Cands(("finance.bank", 0.5), ("finance.purchase", 0.5))));
        Assert.Equal("b", r[0].Question.Id);
    }

    [Fact]
    public void LearnedCountsOverridePrior()
    {
        var q = BoolQ("q", "context.q", new() { ["work"] = 0.1 });
        double before = new QuestionModel(q, Tree()).Dist("work")[Question.Yes];
        var stats = new QuestionStats { Counts = new() { ["work"] = new() { [Question.Yes] = 40 } } };
        double after = new QuestionModel(q, Tree(), stats).Dist("work")[Question.Yes];
        Assert.True(after > 0.8 && before < 0.2, $"before={before} after={after}");
    }

    [Fact]
    public void AffinityInheritedFromAncestor()
    {
        var m = new QuestionModel(BoolQ("q", "context.q", new() { ["finance"] = 0.9 }), Tree());
        Assert.InRange(m.Dist("finance.bank")[Question.Yes], 0.9 - 1e-9, 0.9 + 1e-9);
        Assert.InRange(m.Dist("work")[Question.Yes], 0.5 - 1e-9, 0.5 + 1e-9); // uninformative category
    }

    [Fact]
    public void PosteriorAfterAnswer()
    {
        var q = BoolQ("is_purchase", "context.is_purchase", new() { ["finance.purchase"] = 0.95, ["finance.bank"] = 0.05 });
        var post = new QuestionModel(q, Tree()).Posterior(
            Cands(("finance.bank", 0.43), ("finance.purchase", 0.41), ("work", 0.08), ("unknown", 0.08)), Question.Yes);
        Assert.Equal("finance.purchase", post[0].Category);
        Assert.True(post[0].Score >= 0.75);
    }

    [Fact]
    public void ResolveAnswer()
    {
        var b = BoolQ("b", "context.b");
        foreach (var (input, want) in new[] { ("yes", "yes"), ("Sí", "yes"), ("NO", "no"), ("true", "yes"), ("0", "no") })
            Assert.Equal(want, b.ResolveAnswer(input)[0]);
        Assert.Throws<InvalidRequestException>(() => b.ResolveAnswer("maybe"));

        var mc = new Question
        {
            Id = "m", Text = "?", Feature = "x.y", Type = QuestionType.MultiChoice,
            Answers = [new Answer { Id = "a" }, new Answer { Id = "b" }, new Answer { Id = "c" }],
        };
        mc.Normalize();
        Assert.Equal(["a", "c"], mc.ResolveAnswer("a, C"));
    }

    [Fact]
    public void Validate()
    {
        BoolQ("ok", "context.ok").Validate();
        var bad = new Dictionary<string, Action<Question>>
        {
            ["no id"] = q => q.Id = "",
            ["no text"] = q => q.Text = " ",
            ["bad feature"] = q => q.Feature = "Bad Feature",
            ["bad type"] = q => q.Type = "weird",
            ["bad affinity"] = q => q.Affinity = new() { ["x"] = new() { [Question.Yes] = 1.5 } },
            ["affinity unknown answer"] = q => q.Affinity = new() { ["x"] = new() { ["maybe"] = 0.5 } },
            ["long text"] = q => q.Text = new string('a', 250),
        };
        foreach (var (name, mutate) in bad)
        {
            var q = BoolQ("ok", "context.ok");
            mutate(q);
            Assert.True(Throws(q), name);
        }
        static bool Throws(Question q)
        {
            try { q.Validate(); return false; }
            catch (ConfigException) { return true; }
        }
    }
}
