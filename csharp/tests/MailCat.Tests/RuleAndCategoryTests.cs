using MailCat.Categories;
using MailCat.Emails;
using MailCat.Features;
using MailCat.Rules;
using static MailCat.Tests.TestHelpers;

namespace MailCat.Tests;

public class RuleTests
{
    static FeatureSet Set() => new(
    [
        new Feature("sender.domain", "MyBank.example", 1, ""),
        new Feature("content.intent", "transaction", 0.9, ""),
        new Feature("content.intent", "notification", 0.5, ""),
        new Feature("attachment.count", "2", 1, ""),
        new Feature("subject.tokens", "payment", 0.8, ""),
    ]);

    static bool Match(Condition c) => ConditionCompiler.Compile(c).Match(Set());

    public static TheoryData<string, Condition, bool> OperatorCases() => new()
    {
        { "equals case-insens", Leaf("sender.domain", "equals", "mybank.example"), true },
        { "equals default op", new Condition { Feature = "sender.domain", Value = "mybank.example" }, true },
        { "equals miss", Leaf("sender.domain", "equals", "other.com"), false },
        { "not_equals", Leaf("sender.domain", "not_equals", "other.com"), true },
        { "not_equals multi-value", Leaf("content.intent", "not_equals", "notification"), false },
        { "not_equals missing feature", Leaf("nope", "not_equals", "x"), false },
        { "contains", Leaf("sender.domain", "contains", "bank"), true },
        { "starts_with", Leaf("sender.domain", "starts_with", "mybank"), true },
        { "ends_with", Leaf("sender.domain", "ends_with", ".example"), true },
        { "regex", Leaf("sender.domain", "regex", @"^my.*\.example$"), true },
        { "exists", new Condition { Feature = "attachment.count", Op = "exists" }, true },
        { "exists missing", new Condition { Feature = "nope", Op = "exists" }, false },
        { "gt", Leaf("attachment.count", "gt", 1), true },
        { "gte", Leaf("attachment.count", "gte", 2), true },
        { "lt", Leaf("attachment.count", "lt", 2), false },
        { "lte", Leaf("attachment.count", "lte", 2), true },
        { "in", Leaf("content.intent", "in", new List<object> { "receipt", "transaction" }), true },
        { "in miss", Leaf("content.intent", "in", new List<object> { "receipt" }), false },
        { "min_confidence filters", new Condition { Feature = "content.intent", Op = "equals", Value = "notification", MinConfidence = 0.8 }, false },
    };

    [Theory]
    [MemberData(nameof(OperatorCases))]
    public void Operators(string name, Condition c, bool want) => Assert.True(Match(c) == want, name);

    [Fact]
    public void BooleanCombinators()
    {
        var yes = Leaf("sender.domain", "equals", "mybank.example");
        var no = Leaf("sender.domain", "equals", "x");
        Assert.True(Match(new Condition { All = [yes, Leaf("content.intent", "equals", "transaction")] }));
        Assert.False(Match(new Condition { All = [yes, no] }));
        Assert.True(Match(new Condition { Any = [no, yes] }));
        Assert.True(Match(new Condition { Not = no }));
        Assert.False(Match(new Condition { Not = yes }));
    }

    [Fact]
    public void CompileErrors()
    {
        Condition[] bad =
        [
            new(),
            Leaf("a", "bogus", "x"),
            Leaf("a", "regex", "("),
            Leaf("a", "gt", "abc"),
            Leaf("a", "in", "notalist"),
            new() { Feature = "a", Value = "x", All = [Eq("b", "y")] },
        ];
        foreach (var c in bad) Assert.Throws<ConfigException>(() => ConditionCompiler.Compile(c));
    }

    [Fact]
    public void EngineEmitsWeightedEvidence()
    {
        var e = new RuleEngine(
        [
            new Rule { Id = "bank", Category = "finance.bank", Weight = 0.7, Reason = "dominio bancario", When = Leaf("sender.domain", "equals", "mybank.example") },
            new Rule { Id = "neg", Category = "work", Weight = -0.5, When = Leaf("content.intent", "equals", "transaction") },
            new Rule { Id = "miss", Category = "work", Weight = 0.5, When = Leaf("sender.domain", "equals", "zzz") },
            new Rule { Id = "off", Category = "work", Weight = 0.5, Disabled = true, When = Leaf("sender.domain", "equals", "mybank.example") },
        ]);
        var ev = e.Evidence(new Email(), Set());
        Assert.Equal(2, ev.Count);
        Assert.Equal("bank", ev[0].Ref);
        Assert.Equal(-0.5, ev[1].Weight);
        Assert.Equal("dominio bancario", ev[0].Reason);
    }

    [Fact]
    public void EngineValidation()
    {
        var ok = Leaf("a", "equals", "b");
        Rule R(string id, string cat, double w) => new() { Id = id, Category = cat, Weight = w, When = ok };
        Assert.Throws<ConfigException>(() => new RuleEngine([R("x", "c", 0.5), R("x", "c", 0.5)]));
        Assert.Throws<ConfigException>(() => new RuleEngine([R("x", "c", 0)]));
        Assert.Throws<ConfigException>(() => new RuleEngine([R("x", "c", 1.5)]));
        Assert.Throws<ConfigException>(() => new RuleEngine([R("x", "", 0.5)]));
        Assert.Throws<ConfigException>(() => new RuleEngine([R("", "c", 0.5)]));
    }
}

public class CategoryTests
{
    static List<Category> Cats() =>
    [
        new("work", "Work"), new("finance", "Finance"), new("finance.bank", "Bank", "finance"),
        new("finance.old", "finance", "Old", "", false),
    ];

    [Fact]
    public void TreeNavigation()
    {
        var tr = new CategoryTree(Cats());
        Assert.True(tr.Has(CategoryTree.UnknownId), "Unknown must always exist");
        Assert.Equal(["finance", "work"], tr.Roots());
        Assert.Equal(["finance.bank"], tr.Children("finance"));
        Assert.Equal("Finance / Bank", tr.Path("finance.bank"));
        Assert.True(tr.IsAncestor("finance", "finance.bank"));
        Assert.False(tr.IsAncestor("finance.bank", "finance"));
        Assert.False(tr.IsAncestor("work", "finance.bank"));
        Assert.False(tr.Enabled("finance.old"));
        Assert.True(tr.Enabled("finance.bank"));
        Assert.False(tr.Enabled("nope"));
        var order = new List<string>();
        tr.Walk((c, _) => order.Add(c.Id));
        Assert.Equal("finance", order[0]);
        Assert.Equal("finance.bank", order[1]);
    }

    [Fact]
    public void DisabledParentDisablesSubtree()
    {
        var c = Cats();
        c[1] = c[1] with { Enabled = false };
        Assert.False(new CategoryTree(c).Enabled("finance.bank"));
    }

    [Fact]
    public void Validation()
    {
        Assert.Throws<ConfigException>(() => new CategoryTree([new("", "")]));
        Assert.Throws<ConfigException>(() => new CategoryTree([new("a", "A"), new("a", "A")]));
        Assert.Throws<ConfigException>(() => new CategoryTree([new("a", "A", "zzz")]));
        Assert.Throws<ConfigException>(() => new CategoryTree([new("a", "A", "b"), new("b", "B", "a")]));
        Assert.Throws<ConfigException>(() => new CategoryTree([new("a", "A", "a")]));
        Assert.Throws<ConfigException>(() => new CategoryTree([new("unknown", "U", "a"), new("a", "A")]));
    }
}
