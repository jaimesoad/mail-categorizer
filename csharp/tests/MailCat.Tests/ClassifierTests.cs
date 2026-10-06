using MailCat.Categories;
using MailCat.Classification;

namespace MailCat.Tests;

public class ClassifierTests
{
    static CategoryTree Tree() => new(
    [
        new("work", "Work"), new("finance", "Finance"), new("finance.bank", "Bank", "finance"), new("finance.purchase", "Purchase", "finance"),
    ]);

    static double Score(List<Candidate> c, string id) => c.FirstOrDefault(x => x.Category == id)?.Score ?? 0;
    static void Near(double want, double got) => Assert.InRange(got, want - 1e-6, want + 1e-6);
    static Evidence Ev(string cat, double w) => new(cat, w, Method.Rule, "");

    [Fact]
    public void AggregateSumsToOne()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.bank", 0.7), Ev("work", 0.3)]);
        Near(1, c.Sum(x => x.Score));
    }

    [Fact]
    public void SingleWeakEvidenceLeavesUnknownMass()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("work", 0.3)]);
        Near(0.3, Score(c, "work"));
        Near(0.7, Score(c, CategoryTree.UnknownId));
        Assert.Equal(CategoryTree.UnknownId, c[0].Category);
    }

    [Fact]
    public void NoEvidenceIsUnknown()
    {
        var c = Aggregator.Aggregate(Tree(), []);
        var only = Assert.Single(c);
        Assert.Equal(CategoryTree.UnknownId, only.Category);
        Near(1, only.Score);
    }

    [Fact]
    public void NoisyOrCombination()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.bank", 0.7), Ev("finance.bank", 0.2), Ev("finance.bank", 0.1)]);
        Near(1 - 0.3 * 0.8 * 0.9, Score(c, "finance.bank"));
    }

    [Fact]
    public void HierarchyLeafEvidenceKeepsItsStrength()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.bank", 0.7)]);
        Near(0.7, Score(c, "finance.bank"));
        Assert.True(Score(c, "finance") < 1e-6, "no unrefined mass expected");
    }

    [Fact]
    public void ParentOnlyEvidenceStopsAtParent()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance", 0.9)]);
        Assert.Equal("finance", c[0].Category);
        Near(0.9, c[0].Score);
    }

    [Fact]
    public void ParentPlusChildRefines()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance", 0.9), Ev("finance.bank", 0.7)]);
        Assert.Equal("finance.bank", c[0].Category);
        Assert.True(Score(c, "finance") > 0, "remainder should stay at parent");
    }

    [Fact]
    public void SiblingsConflictIsAmbiguous()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.bank", 0.7), Ev("finance.purchase", 0.7)]);
        Near(Score(c, "finance.bank"), Score(c, "finance.purchase"));
        Assert.True(Score(c, "finance.bank") <= 0.5, "ambiguous evidence must not exceed 0.5");
    }

    [Fact]
    public void NegativeEvidenceSuppresses()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.bank", 0.8), Ev("finance.bank", -0.9), Ev("finance.purchase", 0.6)]);
        Assert.Equal("finance.purchase", c[0].Category);
    }

    [Fact]
    public void NegativeOnParentSuppressesSubtreeShare()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.bank", 0.8), Ev("finance", -0.5)]);
        Assert.True(Score(c, "finance.bank") < 0.8);
    }

    [Fact]
    public void DisabledCategoryIgnored()
    {
        var tr = new CategoryTree([new("a", "", "A", "", false), new("b", "B")]);
        Assert.Equal(CategoryTree.UnknownId, Aggregator.Aggregate(tr, [Ev("a", 0.9)])[0].Category);
    }

    [Fact]
    public void DeterministicTieBreak()
    {
        var ev = new[] { Ev("work", 0.6), Ev("finance", 0.6) };
        var a = Aggregator.Aggregate(Tree(), ev);
        var b = Aggregator.Aggregate(Tree(), ev);
        Assert.Equal(a, b);
        Assert.Equal("finance", a[0].Category); // id order breaks ties
    }

    [Fact]
    public void UpdateAndEntropy()
    {
        var c = new List<Candidate> { new("a", 0.5), new("b", 0.5) };
        Near(1, Aggregator.Entropy(c));
        var u = Aggregator.Update(c, id => id == "a" ? 0.9 : 0.1);
        Near(0.9, u[0].Score);
        Assert.Equal("a", u[0].Category);
    }

    [Fact]
    public void ThresholdLevels()
    {
        var th = Thresholds.Default;
        Result Mk(double a, double b) => new() { Best = new Candidate("work", a), Margin = a - b };
        Assert.Equal(Level.High, th.LevelOf(Mk(0.9, 0.05)));
        Assert.Equal(Level.Medium, th.LevelOf(Mk(0.7, 0.2)));
        Assert.Equal(Level.Low, th.LevelOf(Mk(0.4, 0.3)));
        Assert.Equal(Level.Low, th.LevelOf(new Result { Best = new Candidate(CategoryTree.UnknownId, 0.95), Margin = 0.9 }));
        // margin guard: high score but tiny margin must not auto-classify
        Assert.NotEqual(Level.High, new Thresholds(0.4, 0.3, 0.2).LevelOf(Mk(0.45, 0.40)));
    }

    [Fact]
    public void StrongEvidenceBeatsWeakCompetitor()
    {
        var c = Aggregator.Aggregate(Tree(), [Ev("finance.purchase", 0.9), Ev("finance.purchase", 0.7), Ev("finance.bank", 0.43)]);
        Assert.Equal("finance.purchase", c[0].Category);
        Assert.True(c[0].Score >= 0.9);
    }

    [Theory]
    [InlineData(0.2)]
    [InlineData(0.5)]
    [InlineData(0.7)]
    [InlineData(0.91)]
    public void LoneCategoryKeepsItsEvidenceAsScore(double w) =>
        Near(w, Score(Aggregator.Aggregate(Tree(), [Ev("work", w)]), "work"));
}
