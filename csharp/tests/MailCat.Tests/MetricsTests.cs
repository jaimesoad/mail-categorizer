using MailCat.Categories;
using MailCat.Emails;
using MailCat.Metrics;
using MailCat.Storage;
using static MailCat.Tests.TestHelpers;

namespace MailCat.Tests;

public class MetricsTests : IDisposable
{
    readonly Database db = Database.Open(":memory:");

    public MetricsTests() => db.SyncCategories([new("bank", "Bank"), new("shop", "Shop"), new("unknown", "U")]);

    public void Dispose() => db.Dispose();

    void AddEmail(string id, string status, string initial, string final, string by, int asked)
    {
        db.InsertEmail(new Email { Id = id, RawHash = id, From = new Address("a@b.example"), Subject = id }, [], Now);
        db.SaveSession(new SessionRow
        {
            EmailId = id, Status = status, InitialDecision = initial, QuestionsAsked = asked, FinalCategory = final, ResolvedBy = by, State = "{}",
        }, Now);
    }

    static void Near(double want, double got) => Assert.InRange(got, want - 1e-9, want + 1e-9);

    [Fact]
    public void RatesAndAccuracy()
    {
        AddEmail("a1", "resolved", "know", "bank", "auto", 0); // auto, verified correct
        AddEmail("a2", "resolved", "know", "bank", "auto", 0); // auto, user corrects -> shop
        AddEmail("q1", "resolved", "ask", "shop", "answers", 2); // needed 2 questions, verified correct
        AddEmail("u1", "resolved", "unknown", "unknown", "unknown", 1);
        AddEmail("o1", "open", "ask", "", "", 0); // still pending: excluded from rates
        db.AddInteraction(new Interaction { EmailId = "a1", Kind = InteractionKind.Confirm, PreviousPrediction = "bank", ResultingCategory = "bank" }, Now);
        db.AddInteraction(new Interaction { EmailId = "a2", Kind = InteractionKind.Correct, PreviousPrediction = "bank", ResultingCategory = "shop" }, Now);
        db.AddInteraction(new Interaction { EmailId = "q1", Kind = InteractionKind.Answer, ExpectedGain = 0.5, RealizedGain = 0.7 }, Now);
        db.AddInteraction(new Interaction { EmailId = "q1", Kind = InteractionKind.Confirm, PreviousPrediction = "shop", ResultingCategory = "shop" }, Now);
        db.AddInteraction(new Interaction { EmailId = "u1", Kind = InteractionKind.Manual, ResultingCategory = "bank" }, Now);

        var r = Report.Compute(db);
        Assert.Equal((5, 4, 1), (r.Emails, r.Resolved, r.OpenSessions));
        Near(0.5, r.AutoClassificationRate);
        Near(0.5, r.QuestionRate);
        Near(0.25, r.UnknownRate);
        Near(0.75, r.AvgQuestionsPerEmail);
        // verdicts: a1 ok, a2 wrong, q1 ok, u1 (manual from unknown) wrong => 2/4
        Assert.Equal(4, r.VerifiedByUser);
        Near(0.5, r.ClassificationAccuracy);
        Assert.Equal((1, 1, 1), (r.Corrections, r.ManualLabels, r.QuestionsAnswered));
        Near(1.0 / 3, r.CorrectionRate); // 3 judged predictions (a1,a2,q1), 1 correction
        // correct = resolved, non-unknown, not corrected = a1, q1 = 2 ; effort = 1 answer + 1 correction + 1 manual = 3
        Near(2.0 / 3, r.EffortRatio);
        var bank = r.ByCategory.First(c => c.Category == "bank");
        Near(0.5, bank.Precision); // predicted twice (a1, a2), one true positive
        Near(0.5, bank.Recall); // truth support 2 (a1, u1)
        Assert.Contains("Auto-clasificación", r.Text());
    }

    [Fact]
    public void QuestionUsefulness()
    {
        AddEmail("e1", "resolved", "ask", "bank", "answers", 1);
        AddEmail("e2", "resolved", "ask", "bank", "answers", 1);
        db.Exec("INSERT INTO questions(id,text,feature,type,enabled,source,validated,definition) VALUES (?,?,?,?,1,'config',1,'{}')", "is_bank", "?", "a.b", "boolean");
        db.AddInteraction(new Interaction { EmailId = "e1", Kind = InteractionKind.Answer, QuestionId = "is_bank", Answer = "yes", ExpectedGain = 0.6, RealizedGain = 0.8 }, Now);
        db.AddInteraction(new Interaction { EmailId = "e2", Kind = InteractionKind.Skip, QuestionId = "is_bank" }, Now);
        var q = Assert.Single(Report.Compute(db).Questions);
        Assert.Equal((2, 1), (q.Shown, q.Answered));
        Near(0.5, q.ResponseRate);
        Near(0.6, q.AvgExpectedGain);
        Near(0.8, q.AvgRealizedGain);
    }

    [Fact]
    public void EmptyDatabase()
    {
        var r = Report.Compute(db);
        Assert.Equal(0, r.Emails);
        Assert.Equal(0, r.ClassificationAccuracy);
        Assert.False(double.IsNaN(r.EffortRatio));
    }
}
