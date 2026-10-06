using MailCat.Categories;
using MailCat.Classification;
using MailCat.Emails;
using MailCat.Explain;
using MailCat.Similarity;

namespace MailCat.Tests;

public class SimilarityTests
{
    static List<Doc> Corpus() =>
    [
        new("a1", "bank", "Transaction completed. A transaction of $85.40 was completed on your account", 1),
        new("a2", "bank", "Transaction alert: your card transaction was approved at your bank account", 1),
        new("a3", "bank", "Estado de cuenta: tu transaccion fue completada en tu cuenta bancaria", 1),
        new("b1", "shop", "Your order has shipped. Thank you for your purchase, track the package", 1),
        new("b2", "shop", "Order confirmation: thank you for your purchase of the headphones", 1),
        new("c1", "work", "Meeting tomorrow: sprint planning agenda and project roadmap review", 1),
    ];

    [Fact]
    public void NeighborsRankBySimilarity()
    {
        var ns = new TfIdfIndex(Corpus()).Neighbors("A transaction of $20 was completed on your bank account", 3, "");
        Assert.NotEmpty(ns);
        Assert.Equal("bank", ns[0].Category);
        for (int i = 1; i < ns.Count; i++) Assert.True(ns[i].Sim <= ns[i - 1].Sim, "not sorted");
    }

    [Fact]
    public void NeighborsExcludeSelfAndDeterministic()
    {
        var idx = new TfIdfIndex(Corpus());
        Assert.DoesNotContain(idx.Neighbors(Corpus()[0].Text, 5, "a1"), n => n.Id == "a1");
        Assert.Equal(idx.Neighbors("thank you for your order", 5, ""), new TfIdfIndex(Corpus()).Neighbors("thank you for your order", 5, ""));
    }

    [Fact]
    public void EmptyAndUnrelated()
    {
        Assert.Empty(new TfIdfIndex([]).Neighbors("hello", 3, ""));
        Assert.Empty(new TfIdfIndex(Corpus()).Neighbors("zzzz qqqq", 3, ""));
    }

    [Fact]
    public void SignalCappedBelowAutoThreshold()
    {
        var idx = new TfIdfIndex(Corpus());
        var s = new SimilaritySignal { Load = () => idx };
        var ev = s.Evidence(new Email { Subject = "Transaction completed", BodyText = "transaction of $10 completed on your bank account" }, new MailCat.Features.FeatureSet());
        Assert.NotEmpty(ev);
        Assert.All(ev, e => Assert.True(e.Weight <= 0.75, $"exceeds cap: {e.Weight}"));
        Assert.Equal("bank", ev.MaxBy(e => e.Weight)!.Category);
    }
}

public class ExplainTests
{
    static CategoryTree Tree() => new(
        [new("finance", "Finance"), new("finance.bank", "Bank", "finance"), new("finance.purchase", "Purchase", "finance")]);

    static Result Res(CategoryTree tr)
    {
        List<Evidence> ev =
        [
            new("finance.bank", 0.7, Method.Rule, "Dominio bancario conocido."),
            new("finance", 0.5, Method.Rule, "Describe una transacción."),
            new("finance.bank", 0.4, Method.Similarity, "8 de 10 correos similares fueron Banco."),
            new("finance.purchase", -0.6, Method.Learned, "Aprendido: ese dominio NO suele ir en Compras."),
            new("finance.purchase", 0.3, Method.Rule, "Menciona un pedido."),
        ];
        return new Classifier(tr, [], Thresholds.Default).FromEvidence(ev);
    }

    [Fact]
    public void ExplanationAnswersWhy()
    {
        var tr = Tree();
        var x = Explanation.Build(new ExplainInput { Tree = tr, Result = Res(tr), Decision = Decisions.Know });
        Assert.Equal("finance.bank", x.Category);
        Assert.Equal("Finance / Bank", x.Path);
        var joined = string.Join('|', x.Reasons);
        foreach (var want in new[] { "Dominio bancario", "transacción", "correos similares" }) Assert.Contains(want, joined);
        Assert.DoesNotContain("pedido", joined);
        Assert.NotEmpty(x.Against);
        Assert.Contains("NO suele", x.Against[0]);
        Assert.Equal(Method.Combined, x.Method);
        Assert.StartsWith("Estoy seguro", x.Headline);
        var lines = string.Join('\n', x.Lines());
        Assert.Contains("Razones:", lines);
        Assert.Contains("Finance / Bank", lines);
    }

    [Fact]
    public void FourStatesAreDistinguishable()
    {
        var tr = Tree();
        var res = Res(tr);
        var heads = new[] { Decisions.Know, Decisions.Predict, Decisions.Ask, Decisions.Unknown, Decisions.User }
            .Select(d => Explanation.Build(new ExplainInput { Tree = tr, Result = res, Decision = d }).Headline).ToHashSet();
        Assert.Equal(5, heads.Count);
    }

    [Fact]
    public void UserDecisionAndAnswers()
    {
        var tr = Tree();
        var x = Explanation.Build(new ExplainInput
        {
            Tree = tr, Result = Res(tr), Decision = Decisions.User, Category = "finance.purchase",
            Answers = [new QA("¿Es una compra?", "Sí")],
        });
        Assert.Equal("user", x.Method);
        Assert.Equal(1, x.Score);
        Assert.Contains("usuario", x.Reasons[0]);
        Assert.Contains("Respondiste «Sí»", string.Join('|', x.Reasons));
    }

    [Fact]
    public void UnknownHasAnHonestExplanation()
    {
        var tr = Tree();
        var res = new Classifier(tr, [], Thresholds.Default).FromEvidence([]);
        var x = Explanation.Build(new ExplainInput { Tree = tr, Result = res, Decision = Decisions.Unknown });
        Assert.Equal(CategoryTree.UnknownId, x.Category);
        Assert.Contains("No hay evidencia", x.Reasons[0]);
    }
}
