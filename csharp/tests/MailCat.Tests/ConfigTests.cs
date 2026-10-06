using MailCat.Config;

namespace MailCat.Tests;

public class ConfigTests
{
    [Fact]
    public void DefaultConfigBuilds()
    {
        var b = MailCatConfig.LoadOrDefault("").Build();
        Assert.True(b.Tree.Has("finance.bank"));
        Assert.True(b.Tree.Has("unknown"));
        Assert.True(b.Questions.Count >= 5);
        Assert.True(b.Rules.Count >= 10);
        Assert.All(b.Questions, q =>
        {
            Assert.True(q.Validated);
            Assert.Equal(2, q.Answers.Count);
        });
        Assert.Equal(0.95, b.Questions[0].Affinity["finance.purchase"]["yes"]); // affinity shorthand
    }

    static void Build(string yaml) => MailCatConfig.Parse(yaml).Build();

    [Fact]
    public void ValidationErrors()
    {
        const string cats = "categories:\n  - {id: a, name: A}\n";
        var cases = new Dictionary<string, string>
        {
            ["unknown key"] = "bogus: 1\n",
            ["bad thresholds"] = "thresholds: {auto: 0.5, ask: 0.9, min_margin: 0.1}\n",
            ["missing parent"] = "categories:\n  - {id: a, parent: zzz}\n",
            ["cycle"] = "categories:\n  - {id: a, parent: b}\n  - {id: b, parent: a}\n",
            ["duplicate category"] = "categories:\n  - {id: a}\n  - {id: a}\n",
            ["rule bad category"] = cats + "rules:\n  - {id: r, category: nope, weight: 0.5, when: {feature: x.y, value: z}}\n",
            ["rule bad regex"] = cats + "rules:\n  - {id: r, category: a, weight: 0.5, when: {feature: x.y, op: regex, value: '('}}\n",
            ["affinity bad cat"] = cats + "questions:\n  - {id: q, text: '?', feature: ctx.q, affinity: {zzz: 0.5}}\n",
            ["question dup"] = cats + "questions:\n  - {id: q, text: '?', feature: ctx.q}\n  - {id: q, text: '?', feature: ctx.q}\n",
            ["question bad feat"] = cats + "questions:\n  - {id: q, text: '?', feature: Bad}\n",
            ["removed section"] = "llm: {enabled: true}\n", // not part of the C# version
        };
        foreach (var (name, yaml) in cases)
            Assert.True(Throws(yaml), name);

        Build(cats + "questions:\n  - {id: q, text: '?', feature: ctx.q, affinity: {a: 0.7}}\n");
        Build("");

        static bool Throws(string yaml)
        {
            try { Build(yaml); return false; }
            catch (ConfigException) { return true; }
        }
    }

    [Fact]
    public void ChoiceQuestionAffinityMapsAndInList()
    {
        var b = MailCatConfig.Parse("""
            categories:
              - {id: a, name: A}
              - {id: b, name: B}
            questions:
              - id: kind
                text: "¿Qué tipo?"
                feature: ctx.kind
                type: single_choice
                answers:
                  - {id: x, label: Equis}
                  - {id: y, label: Ye, value: why}
                affinity:
                  a: {x: 0.8, y: 0.2}
            rules:
              - id: r
                category: a
                weight: 0.5
                when: {feature: sender.domain, op: in, value: [one.example, two.example]}
            """).Build();
        var q = Assert.Single(b.Questions);
        Assert.Equal(0.8, q.Affinity["a"]["x"]);
        Assert.Equal("why", q.Answers[1].FeatureValue);
        Assert.Equal(1, b.Rules.Count);
    }
}
