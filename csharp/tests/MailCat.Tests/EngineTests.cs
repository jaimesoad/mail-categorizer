using MailCat.Emails;
using MailCat.Engine;
using MailCat.Explain;
using MailCat.Samples;
using static MailCat.Tests.TestHelpers;

namespace MailCat.Tests;

public class EngineTests
{
    [Fact]
    public void KnownBankSenderIsClassifiedAutomatically()
    {
        using var f = TestEngine();
        var o = f.Engine.Process(Mail("e1", "alerts@mybank.example", "Transaction completed", "A transaction of $85.40 was completed on your account."));
        Assert.Equal((Decisions.Know, "finance.bank", false), (o.Decision, o.Category, o.NeedsQuestion));
        Assert.True(o.Score >= 0.85);
        Assert.NotEmpty(o.Explanation.Reasons); // an automatic classification must be explainable
    }

    [Fact]
    public void AmbiguousEmailAsksThenLearnsAndStopsAsking()
    {
        using var f = TestEngine();
        var e = f.Engine;
        // First email from store.example: genuinely ambiguous.
        var o = e.Process(Mail("s1", "store@store.example", "Payment received", "Payment received for your order #884211. Thank you for your purchase."));
        Assert.True(o.NeedsQuestion && o.Question != null, "expected a question");

        // Answer like a user who bought something.
        int asked = 0;
        while (o.NeedsQuestion && asked < 5)
        {
            o = e.Answer("s1", o.Question!.Id, o.Question.Id == "is_purchase" ? "yes" : "no");
            asked++;
        }
        Assert.Equal(("finance.purchase", Decisions.Know), (o.Category, o.Decision));
        Assert.True(o.Learned, "a session resolved through answers must be learned");

        // The user confirms (strongest signal) a few more of the same sender.
        for (int i = 2; i <= 6; i++)
        {
            var id = $"s{i}";
            var x = e.Process(Mail(id, "store@store.example", "Payment received", $"Payment received for your order #{884211 + i}. Thank you for your purchase."));
            if (x.NeedsQuestion) x = e.Answer(id, x.Question!.Id, x.Question.Id == "is_purchase" ? "yes" : "no");
            if (x.Decision != Decisions.User) e.Feedback(id, x.Category == "finance.purchase", "finance.purchase");
        }

        // Now the system should know this sender without asking.
        var later = e.Process(Mail("s99", "store@store.example", "Payment received", "Payment received for your order #999999. Thank you for your purchase."));
        Assert.False(later.NeedsQuestion, "still asking after repeated confirmations");
        Assert.Equal("finance.purchase", later.Category);
    }

    [Fact]
    public void CorrectionLearnsNegativeKnowledge()
    {
        using var f = TestEngine();
        var e = f.Engine;
        // Teach (wrongly) that shop.example is a bank, until the system commits to it.
        for (int i = 0; i < 10; i++)
        {
            var id = $"w{i}";
            e.Process(Mail(id, "x@shop.example", "Hello " + id, "Something unrelated number " + id));
            e.Move(id, "finance.bank");
        }
        var pre = e.Process(Mail("wx", "x@shop.example", "Payment status", "weekly update 9"));
        Assert.True(pre.Category == "finance.bank" && !pre.NeedsQuestion, "setup: the system should have committed to bank");

        var o = e.Feedback("wx", false, "finance.purchase");
        Assert.Equal((Decisions.User, "finance.purchase", 1.0, "user"), (o.Decision, o.Category, o.Score, o.Method));
        Assert.Contains(f.Db.ListRules("finance.bank", 50), r => r.Negative && r.Key == "sender.domain=shop.example");
    }

    [Fact]
    public void NoEvidenceIsUnknownNotAGuess()
    {
        using var f = TestEngine(c => c.Questions = []);
        var o = f.Engine.Process(Mail("u1", "zed@gmail.example", "zzz", "qqq www eee"));
        Assert.Equal((Decisions.Unknown, "unknown", true, false), (o.Decision, o.Category, o.NeedsManual, o.NeedsQuestion));
    }

    [Fact]
    public void QuestionLimitIsRespected()
    {
        using var f = TestEngine(c => c.MaxQuestionsPerEmail = 1);
        var o = f.Engine.Process(Mail("l1", "store@store.example", "Payment received", "Payment received."));
        Assert.True(o.NeedsQuestion);
        o = f.Engine.Answer("l1", o.Question!.Id, "no"); // does not settle the matter
        Assert.False(o.NeedsQuestion, "max_questions_per_email exceeded");
        Assert.Equal(1, o.QuestionsAsked);
    }

    [Fact]
    public void ProcessIsIdempotentAndIdConflictDetected()
    {
        using var f = TestEngine();
        var m = Mail("i1", "alerts@mybank.example", "Transaction completed", "A transaction of $10 was completed.");
        var a = f.Engine.Process(m);
        var b = f.Engine.Process(m);
        Assert.True(b.AlreadyProcessed);
        Assert.Equal(a.SessionId, b.SessionId);
        Assert.Throws<ConflictException>(() => f.Engine.Process(Mail("i1", "other@x.example", "Different", "different content")));
    }

    [Fact]
    public void SessionSurvivesRestart()
    {
        using var f = TestEngine();
        var o = f.Engine.Process(Mail("r1", "store@store.example", "Payment received", "Payment received."));
        Assert.True(o.NeedsQuestion);
        var e2 = new MailCatEngine(f.Db, f.Built); // a fresh engine over the same DB
        var again = e2.Get("r1");
        Assert.Equal(o.Question!.Id, again.Question?.Id);
    }

    [Fact]
    public void AnswerValidation()
    {
        using var f = TestEngine();
        var e = f.Engine;
        var o = e.Process(Mail("v1", "store@store.example", "Payment received", "Payment received."));
        Assert.Throws<InvalidRequestException>(() => e.Answer("v1", o.Question!.Id, "maybe"));
        Assert.Throws<InvalidRequestException>(() => e.Answer("v1", "nope", "yes"));
        Assert.Throws<NotFoundException>(() => e.Answer("ghost", o.Question!.Id, "yes"));
        Assert.Throws<InvalidRequestException>(() => e.Feedback("v1", false, "no-such-category"));
        Assert.Throws<InvalidRequestException>(() => e.Feedback("v1", true));
        e.Answer("v1", o.Question!.Id, "yes");
        Assert.Throws<ConflictException>(() => e.Answer("v1", o.Question!.Id, "yes")); // double answer
    }

    [Fact]
    public void SkipNeverRepeatsQuestion()
    {
        using var f = TestEngine();
        var o = f.Engine.Process(Mail("k1", "store@store.example", "Payment received", "Payment received."));
        var first = o.Question!.Id;
        o = f.Engine.Skip("k1", first);
        Assert.False(o.NeedsQuestion && o.Question!.Id == first, "skipped question was asked again");
    }

    [Fact]
    public void Determinism()
    {
        static string Run()
        {
            using var f = TestEngine();
            var o = f.Engine.Process(Mail("d1", "store@store.example", "Payment received", "Payment received for your order #1234."));
            o.SessionId = "";
            return Json.Serialize(o);
        }
        Assert.Equal(Run(), Run());
    }

    [Fact]
    public void PrivacyStoreBodyOff()
    {
        using var f = TestEngine(c => c.Privacy.StoreBody = false);
        f.Engine.Process(Mail("p1", "a@b.example", "secret subject", "very secret body text"));
        Assert.Equal("", f.Db.GetEmail("p1").BodyText);
    }

    [Fact]
    public void PurgeKeepsKnowledge()
    {
        using var f = TestEngine();
        for (int i = 0; i < 3; i++)
        {
            var id = $"g{i}";
            f.Engine.Process(Mail(id, "news@letters.example", "Weekly digest", "newsletter content number " + id));
            f.Engine.Move(id, "newsletters");
        }
        var before = f.Db.ListRules("newsletters", 10);
        f.Db.PurgeAllContent(DateTime.UtcNow);
        var em = f.Db.GetEmail("g0");
        Assert.Equal(("", ""), (em.Subject, em.BodyText));
        var after = f.Db.ListRules("newsletters", 10);
        Assert.True(after.Count > 0 && after.Count == before.Count, "purging content must keep learned knowledge");
        f.Engine.Get("g0"); // the classification survives a purge
    }

    /// <summary>§38: classifying one email without an LLM must be interactive: p95 &lt; 100 ms.</summary>
    [Fact]
    public void ClassificationLatencyP95()
    {
        using var f = TestEngine();
        var all = SampleGenerator.Generate(7, 30);
        var fresh = new List<Sample>();
        for (int i = 0; i < all.Count; i++)
        {
            var s = all[i];
            if (i % 3 == 0) { fresh.Add(s); continue; }
            var m = EmlParser.Parse(s.Eml) with { Id = "train-" + s.Id };
            f.Engine.Process(m);
            f.Engine.Move(m.Id, s.Category);
        }
        var durs = new List<double>();
        foreach (var s in fresh)
        {
            var m = EmlParser.Parse(s.Eml) with { Id = "fresh-" + s.Id };
            var sw = System.Diagnostics.Stopwatch.StartNew();
            f.Engine.Process(m);
            durs.Add(sw.Elapsed.TotalMilliseconds);
        }
        durs.Sort();
        double p95 = durs[durs.Count * 95 / 100];
        Assert.True(p95 < 100, $"p95 {p95:F1} ms exceeds 100 ms (n={durs.Count}, p50={durs[durs.Count / 2]:F1} ms, max={durs[^1]:F1} ms)");
    }
}
