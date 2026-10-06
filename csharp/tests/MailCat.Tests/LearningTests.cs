using MailCat.Categories;
using MailCat.Emails;
using MailCat.Features;
using MailCat.Learning;

namespace MailCat.Tests;

public class LearningTests
{
    sealed class MemRepo : ILearnedRuleRepo
    {
        readonly Dictionary<string, LearnedRule> rules = [];
        static string K(string c, string k, bool n) => $"{c}#{k}{(n ? "#neg" : "")}";

        public List<LearnedRule> RulesByKeys(IReadOnlyList<string> keys) =>
            rules.Values.Where(r => keys.Contains(r.Key)).Select(r => r.Clone()).ToList();

        public LearnedRule? GetRule(string c, string k, bool n) => rules.TryGetValue(K(c, k, n), out var r) ? r.Clone() : null;

        public void SaveRule(LearnedRule r) => rules[K(r.CategoryId, r.Key, r.Negative)] = r.Clone();
    }

    static CategoryTree Tree() => new(
        [new("finance", "Finance"), new("finance.bank", "Bank", "finance"), new("finance.purchase", "Purchase", "finance")]);

    static FeatureSet Fs(string domain, bool free) => new(
    [
        new Feature(FeatureNames.SenderEmail, "a@" + domain, 1, FeatureSource.Header),
        new Feature(FeatureNames.SenderDomain, domain, 1, FeatureSource.Header),
        new Feature(FeatureNames.SenderIsFreemail, free ? "true" : "false", 1, FeatureSource.Parser),
        new Feature(FeatureNames.ContentIntent, "transaction", 0.9, FeatureSource.Pattern),
        new Feature("context.is_work", "true", 1, FeatureSource.User),
    ]);

    static Feedback Fb(FeatureSet set, string cat, FeedbackKind kind, string prev = "") => new("", set, cat, prev, kind);

    [Fact]
    public void WeightCurveMatchesSpec()
    {
        foreach (var (conf, want) in new[] { (1, 0.20), (3, 0.45), (10, 0.80) })
            Assert.InRange(Generalizations.Weight(0.9, conf, 0), want - 0.04, want + 0.04);
        Assert.Equal(0, Generalizations.Weight(0.9, 0, 5));
        double a = Generalizations.Weight(0.9, 10, 0), b = Generalizations.Weight(0.9, 10, 1);
        Assert.True(b >= 0.85 * a && b < a, $"a single rejection should attenuate mildly: {a} -> {b}");
    }

    [Fact]
    public void GeneralizationsSkipFreemailDomainsAndUserFeatures()
    {
        Dictionary<string, int> Kinds(FeatureSet set)
        {
            var m = new Dictionary<string, int>();
            foreach (var g in Generalizations.Of(set))
            {
                m[g.Kind] = m.GetValueOrDefault(g.Kind) + 1;
                Assert.DoesNotContain(g.Conds, c => c.Feature == "context.is_work");
            }
            return m;
        }
        var corp = Kinds(Fs("bank.example", false));
        Assert.Equal(1, corp[RuleKind.SenderEmail]);
        Assert.Equal(1, corp[RuleKind.SenderDomain]);
        Assert.Equal(1, corp[RuleKind.DomainIntent]);
        Assert.Equal(1, corp[RuleKind.Intent]);
        var free = Kinds(Fs("gmail.com", true));
        Assert.False(free.ContainsKey(RuleKind.SenderDomain));
        Assert.False(free.ContainsKey(RuleKind.DomainIntent));
        Assert.Equal(1, free[RuleKind.SenderEmail]);
    }

    [Fact]
    public void LearnPositiveGrowsAndSignalReadsIt()
    {
        var repo = new MemRepo();
        var l = new Learner(repo, Tree());
        var set = Fs("store.example", false);
        for (int i = 0; i < 5; i++) l.Learn(Fb(set, "finance.purchase", FeedbackKind.Manual));
        var r = repo.GetRule("finance.purchase", "sender.domain=store.example", false);
        Assert.NotNull(r);
        Assert.Equal(5, r.Confirmations);
        Assert.True(r.Weight >= 0.5);

        var ev = new LearnedSignal(repo).Evidence(new Email(), set);
        Assert.NotEmpty(ev);
        Assert.All(ev, e =>
        {
            Assert.Equal("finance.purchase", e.Category);
            Assert.True(e.Weight > 0);
            Assert.Equal("learned", e.Method);
        });
        Assert.True(ev[0].Weight > ev[1].Weight, "expected discounted dependents");
    }

    [Fact]
    public void CorrectionCreatesNegativeRuleAndRejects()
    {
        var repo = new MemRepo();
        var l = new Learner(repo, Tree());
        var set = Fs("shop.example", false);
        for (int i = 0; i < 2; i++) l.Learn(Fb(set, "finance.bank", FeedbackKind.Manual)); // the system learned "shop → bank" (wrongly)
        var before = repo.GetRule("finance.bank", "sender.domain=shop.example", false)!;
        l.Learn(Fb(set, "finance.purchase", FeedbackKind.Correct, "finance.bank"));
        var after = repo.GetRule("finance.bank", "sender.domain=shop.example", false)!;
        Assert.Equal(1, after.Rejections);
        Assert.True(after.Weight < before.Weight && after.Weight != 0, "positive bank rule should be attenuated, not destroyed");
        var neg = repo.GetRule("finance.bank", "sender.domain=shop.example", true);
        Assert.NotNull(neg);
        Assert.True(neg.Negative);
        Assert.Equal(1, neg.Confirmations);
        Assert.Contains(new LearnedSignal(repo).Evidence(new Email(), set), e => e.Category == "finance.bank" && e.Weight < 0);
    }

    [Fact]
    public void RefinementIsNotAnError()
    {
        var repo = new MemRepo();
        var l = new Learner(repo, Tree());
        var set = Fs("mybank.example", false);
        l.Learn(Fb(set, "finance", FeedbackKind.Manual));
        l.Learn(Fb(set, "finance.bank", FeedbackKind.Correct, "finance")); // Finance -> Finance/Bank
        Assert.Null(repo.GetRule("finance", "sender.domain=mybank.example", true));
        Assert.Equal(0, repo.GetRule("finance", "sender.domain=mybank.example", false)!.Rejections);
    }

    [Fact]
    public void UnknownAndNullAreIgnored()
    {
        var l = new Learner(new MemRepo(), Tree());
        l.Learn(Fb(Fs("a.com", false), CategoryTree.UnknownId, FeedbackKind.Manual));
        l.Learn(new Feedback("", null!, "finance", "", FeedbackKind.Manual));
        Assert.Throws<InvalidRequestException>(() => l.Learn(Fb(Fs("a.com", false), "nope", FeedbackKind.Manual)));
    }

    [Fact]
    public void AmbiguousConditionSharesItsConfirmations()
    {
        var repo = new MemRepo();
        var l = new Learner(repo, Tree());
        var set = Fs("acme.example", false);
        for (int i = 0; i < 9; i++) l.Learn(Fb(set, "finance.purchase", FeedbackKind.Manual));
        l.Learn(Fb(set, "finance.bank", FeedbackKind.Manual));
        var ev = new LearnedSignal(repo).Evidence(new Email(), set);
        double purchase = ev.Where(e => e.Category == "finance.purchase").Max(e => e.Weight);
        double bank = ev.Where(e => e.Category == "finance.bank").Max(e => e.Weight);
        Assert.True(purchase >= 4 * bank, $"a domain confirmed 9:1 must favour purchase clearly: purchase={purchase:F3} bank={bank:F3}");

        // a key confirmed for a single category keeps its full weight
        var solo = Fs("solo.example", false);
        for (int i = 0; i < 4; i++) l.Learn(Fb(solo, "finance.bank", FeedbackKind.Manual));
        var sev = new LearnedSignal(repo).Evidence(new Email(), solo);
        var r = repo.GetRule("finance.bank", "sender.email=a@solo.example", false)!;
        Assert.True(sev.Count > 0 && sev[0].Weight >= r.Weight - 1e-9, "single-category rule must not be diluted");
    }
}
