using MailCat.Categories;
using MailCat.Emails;
using MailCat.Features;
using MailCat.Learning;
using MailCat.Questions;
using MailCat.Storage;
using static MailCat.Tests.TestHelpers;

namespace MailCat.Tests;

public class StorageTests : IDisposable
{
    readonly Database db = Database.Open(":memory:");

    public StorageTests() => db.SyncCategories(
        [new("finance", "Finance"), new("finance.bank", "Bank", "finance"), new("unknown", "Unknown")]);

    public void Dispose() => db.Dispose();

    static Email Mail(string id, string hash) => new()
    {
        Id = id, MessageId = id + "@x", RawHash = hash, From = new Address("A", "a@bank.example"), Subject = "s " + id, BodyText = "body " + id,
    };

    [Fact]
    public void MigrationsAreIdempotentAndFileBacked()
    {
        var path = Path.Combine(Path.GetTempPath(), $"mailcat-{Guid.NewGuid():N}.db");
        try
        {
            using (var a = Database.Open(path)) a.SyncCategories([new("a", "A")]);
            using var b = Database.Open(path); // reopen: migrations must not re-run destructively
            var cats = b.StoredCategories();
            Assert.Equal("a", cats.First(c => c.Id == "a").Id);
            Assert.Equal(Migrations.All.Length.ToString(), b.Scalar("SELECT value FROM schema_meta WHERE key='version'")?.ToString());
        }
        finally
        {
            foreach (var f in Directory.GetFiles(Path.GetTempPath(), Path.GetFileName(path) + "*")) File.Delete(f);
        }
    }

    [Fact]
    public void InsertEmailDedupeConflictAndFeatures()
    {
        var fs = new[] { new Feature("sender.domain", "bank.example", 1, FeatureSource.Header) };
        var (id, existed) = db.InsertEmail(Mail("e1", "h1"), fs, Now);
        Assert.Equal(("e1", false), (id, existed));
        Assert.Equal(("e1", true), db.InsertEmail(Mail("other-id", "h1"), [], Now)); // same hash
        Assert.Throws<ConflictException>(() => db.InsertEmail(Mail("e1", "h2"), [], Now));
        Assert.True(db.Features("e1").Has("sender.domain", "bank.example"));
        var got = db.GetEmail("e1");
        Assert.Equal("s e1", got.Subject);
        Assert.Equal("A", got.From.Name);
        Assert.Throws<NotFoundException>(() => db.GetEmail("zzz"));
    }

    [Fact]
    public void ClassificationFinalAndLabels()
    {
        db.InsertEmail(Mail("e1", "h1"), [], Now);
        db.SaveClassification("e1",
        [
            new StoredClassification { CategoryId = "finance.bank", Score = 0.9, Method = "rule", Final = true, Explanation = "{}" },
            new StoredClassification { CategoryId = "unknown", Score = 0.1, Method = "rule" },
        ], Now);
        var f = db.FinalClassification("e1");
        Assert.Equal("finance.bank", f.CategoryId);
        Assert.True(f.Final);
        Assert.Empty(db.Labels()); // an automatic classification is not a trusted label

        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Confirm, ResultingCategory = "finance.bank" }, Now);
        Assert.Equal(new Label("finance.bank", "user"), db.Labels()["e1"]);
        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Correct, ResultingCategory = "finance" }, Now.AddSeconds(1));
        Assert.Equal("finance", db.Labels()["e1"].Category); // the latest verdict wins

        db.SaveClassification("e1", [new StoredClassification { CategoryId = "finance", Score = 1, Method = "user", Final = true }], Now);
        Assert.Single(db.Candidates("e1")); // saving again replaces candidates
    }

    [Fact]
    public void SessionsUpsert()
    {
        db.InsertEmail(Mail("e1", "h1"), [], Now);
        Assert.Throws<NotFoundException>(() => db.GetSession("e1"));
        db.SaveSession(new SessionRow { EmailId = "e1", Status = "open", InitialDecision = "ask", State = "{\"a\":1}" }, Now);
        var s1 = db.GetSession("e1");
        db.SaveSession(new SessionRow
        {
            Id = s1.Id, EmailId = "e1", Status = "resolved", InitialDecision = "ask", FinalCategory = "finance",
            ResolvedBy = "answers", QuestionsAsked = 2, State = "{\"a\":2}",
        }, Now);
        var s2 = db.GetSession("e1");
        Assert.Equal(s1.Id, s2.Id);
        Assert.Equal("resolved", s2.Status);
        Assert.Equal(2, s2.QuestionsAsked);
        Assert.Equal("{\"a\":2}", s2.State);
    }

    [Fact]
    public void LearnedRulesRepo()
    {
        var r = new LearnedRule
        {
            CategoryId = "finance.bank", Key = "sender.domain=bank.example", Kind = RuleKind.SenderDomain,
            Conditions = [new Cond("sender.domain", "bank.example")], Weight = 0.2, Observations = 1, Confirmations = 1, CreatedAt = Now, UpdatedAt = Now,
        };
        db.SaveRule(r);
        r.Confirmations = 2;
        r.Weight = 0.35;
        db.SaveRule(r); // upsert by (category,key,negative)
        var neg = r.Clone();
        neg.Id = "";
        neg.Negative = true;
        db.SaveRule(neg);

        var got = db.GetRule("finance.bank", r.Key, false);
        Assert.NotNull(got);
        Assert.Equal(2, got.Confirmations);
        Assert.Equal(0.35, got.Weight);
        Assert.Single(got.Conditions);
        Assert.Equal(2, db.RulesByKeys([r.Key, "nope"]).Count); // positive + negative
        Assert.Null(db.GetRule("finance", "x", false));
        Assert.Equal(2, db.ListRules("", 10).Count);
    }

    static Question BoolQ(string id)
    {
        var q = new Question
        {
            Id = id, Text = "¿Es?", Feature = "ctx.q", Type = QuestionType.Boolean, Validated = true,
            Affinity = new() { ["finance.bank"] = new() { ["yes"] = 0.9 } },
        };
        q.Normalize();
        return q;
    }

    [Fact]
    public void QuestionsRoundTripAndState()
    {
        db.SaveQuestion(BoolQ("q1"), false);
        var gen = BoolQ("gen1");
        gen.Source = "llm";
        gen.Validated = false;
        gen.Disabled = true;
        db.SaveQuestion(gen, true);
        Dictionary<string, Question> ById() => db.Questions().ToDictionary(q => q.Id);

        var m = ById();
        Assert.Equal(2, m.Count);
        Assert.Equal(0.9, m["q1"].Affinity["finance.bank"]["yes"]);
        Assert.Equal(2, m["q1"].Answers.Count);
        Assert.False(m["gen1"].Validated);
        Assert.True(m["gen1"].Disabled);

        db.SetQuestionState("gen1", true, true);
        m = ById();
        Assert.True(m["gen1"].Validated);
        Assert.False(m["gen1"].Disabled);
        Assert.Throws<NotFoundException>(() => db.SetQuestionState("nope", true, true));
    }

    [Fact]
    public void QuestionStatsUseOnlyExplicitLabels()
    {
        db.SaveQuestion(BoolQ("q1"), false);
        foreach (var id in new[] { "e1", "e2", "e3" }) db.InsertEmail(Mail(id, "h" + id), [], Now);
        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Answer, QuestionId = "q1", Answer = "yes" }, Now);
        db.AddInteraction(new Interaction { EmailId = "e2", Kind = InteractionKind.Answer, QuestionId = "q1", Answer = "no" }, Now);
        db.AddInteraction(new Interaction { EmailId = "e3", Kind = InteractionKind.Skip, QuestionId = "q1" }, Now);
        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Confirm, ResultingCategory = "finance.bank" }, Now);
        var s = db.QuestionStats()["q1"];
        Assert.Equal((3, 2), (s.Shown, s.Answered));
        Assert.Equal(1, s.Counts["finance.bank"]["yes"]);
        Assert.Single(s.Counts); // only e1 has an explicit label
    }

    [Fact]
    public void HistoryAndThread()
    {
        db.InsertEmail(Mail("e1", "h1"), [], Now);
        db.SaveClassification("e1", [new StoredClassification { CategoryId = "finance.bank", Score = 1, Method = "user", Final = true }], Now);
        var h = db.SenderHistory("a@bank.example", "bank.example");
        Assert.Equal((1, 1), (h.EmailCount, h.DomainCount));
        Assert.Equal(["finance.bank"], h.EmailCategories);
        Assert.Equal(0, db.SenderHistory("new@x.example", "x.example").EmailCount);

        var ti = db.ThreadInfo(["missing@x", "<e1@x>"]);
        Assert.True(ti.Found);
        Assert.Equal("finance.bank", ti.Category);
        Assert.Equal("a@bank.example", ti.Sender);
        Assert.False(db.ThreadInfo(["zzz"]).Found);
    }

    [Fact]
    public void PurgeDeleteAndBackup()
    {
        db.InsertEmail(Mail("e1", "h1"),
        [
            new Feature("sender.domain", "bank.example", 1, ""), new Feature("content.keyword", "secret", 0.5, ""),
            new Feature("subject.tokens", "hello", 0.8, ""),
        ], Now);
        db.SaveClassification("e1", [new StoredClassification { CategoryId = "finance.bank", Score = 1, Method = "user", Final = true }], Now);
        db.PurgeContent("e1", Now);
        var e = db.GetEmail("e1");
        var set = db.Features("e1");
        Assert.Equal("", e.BodyText);
        Assert.Equal("", e.Subject);
        Assert.False(set.Exists("content.keyword"));
        Assert.False(set.Exists("subject.tokens"));
        Assert.True(set.Exists("sender.domain"));
        db.FinalClassification("e1"); // the classification survives a content purge
        Assert.Throws<NotFoundException>(() => db.PurgeContent("nope", Now));

        var dst = Path.Combine(Path.GetTempPath(), $"mailcat-bk-{Guid.NewGuid():N}.db");
        try
        {
            db.Backup(dst);
            using var bk = Database.Open(dst);
            bk.FinalClassification("e1");
        }
        finally { File.Delete(dst); }

        db.DeleteEmail("e1");
        Assert.Throws<NotFoundException>(() => db.FinalClassification("e1")); // delete cascades
        Assert.Throws<NotFoundException>(() => db.DeleteEmail("e1"));
    }

    [Fact]
    public void SyncCategoriesDisablesRemoved()
    {
        db.SyncCategories([new("finance", "Finance")]);
        var enabled = db.StoredCategories().ToDictionary(c => c.Id, c => c.Enabled);
        Assert.True(enabled["finance"]);
        Assert.False(enabled["finance.bank"]); // disabled, not deleted
    }

    [Fact]
    public void CacheInvalidation()
    {
        db.InsertEmail(Mail("e1", "h1"), [], Now);
        var v = db.LabelVersion;
        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Answer }, Now);
        Assert.Equal(v, db.LabelVersion); // an answer is not a label
        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Manual, ResultingCategory = "finance" }, Now);
        Assert.NotEqual(v, db.LabelVersion);
    }

    [Fact]
    public void TransactionRollsBackOnError()
    {
        Assert.Throws<InvalidOperationException>(() => db.Transaction(() =>
        {
            db.Exec("INSERT INTO categories(id,name,enabled) VALUES ('tmp','Tmp',1)");
            throw new InvalidOperationException("boom");
        }));
        Assert.DoesNotContain(db.StoredCategories(), c => c.Id == "tmp");
    }
}
