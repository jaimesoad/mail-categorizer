package metrics

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

var ctx = context.Background()
var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SyncCategories(ctx, []categories.Category{
		{ID: "bank", Name: "Bank", Enabled: true}, {ID: "shop", Name: "Shop", Enabled: true}, {ID: "unknown", Name: "U", Enabled: true},
	})
	return db
}

func addEmail(t *testing.T, db *storage.DB, id, status, initial, final, by string, asked int) {
	t.Helper()
	if _, _, err := db.InsertEmail(ctx, email.Email{ID: id, RawHash: id, From: email.Address{Email: "a@b.example"}, Subject: id}, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSession(ctx, storage.SessionRow{EmailID: id, Status: status, InitialDecision: initial, QuestionsAsked: asked, FinalCategory: final, ResolvedBy: by, State: []byte("{}")}, now); err != nil {
		t.Fatal(err)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRatesAndAccuracy(t *testing.T) {
	db := setup(t)
	addEmail(t, db, "a1", "resolved", "know", "bank", "auto", 0)   // auto, verified correct
	addEmail(t, db, "a2", "resolved", "know", "bank", "auto", 0)   // auto, user corrects -> shop
	addEmail(t, db, "q1", "resolved", "ask", "shop", "answers", 2) // needed 2 questions, verified correct
	addEmail(t, db, "u1", "resolved", "unknown", "unknown", "unknown", 1)
	addEmail(t, db, "o1", "open", "ask", "", "", 0) // still pending: excluded from rates

	db.AddInteraction(ctx, storage.Interaction{EmailID: "a1", Kind: storage.KindConfirm, PreviousPrediction: "bank", ResultingCategory: "bank"}, now)
	db.AddInteraction(ctx, storage.Interaction{EmailID: "a2", Kind: storage.KindCorrect, PreviousPrediction: "bank", ResultingCategory: "shop"}, now)
	db.AddInteraction(ctx, storage.Interaction{EmailID: "q1", Kind: storage.KindAnswer, QuestionID: "", ExpectedGain: 0.5, RealizedGain: 0.7}, now)
	db.AddInteraction(ctx, storage.Interaction{EmailID: "q1", Kind: storage.KindConfirm, PreviousPrediction: "shop", ResultingCategory: "shop"}, now)
	db.AddInteraction(ctx, storage.Interaction{EmailID: "u1", Kind: storage.KindManual, ResultingCategory: "bank"}, now)

	r, err := Compute(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if r.Emails != 5 || r.Resolved != 4 || r.OpenSessions != 1 {
		t.Fatalf("counts: %+v", r)
	}
	if !near(r.AutoClassificationRate, 0.5) || !near(r.QuestionRate, 0.5) || !near(r.UnknownRate, 0.25) || !near(r.AvgQuestionsPerEmail, 0.75) {
		t.Errorf("rates: auto=%v q=%v unk=%v avg=%v", r.AutoClassificationRate, r.QuestionRate, r.UnknownRate, r.AvgQuestionsPerEmail)
	}
	// verdicts: a1 ok, a2 wrong, q1 ok, u1 (manual from unknown) wrong => 2/4
	if r.VerifiedByUser != 4 || !near(r.ClassificationAccuracy, 0.5) {
		t.Errorf("accuracy: n=%d acc=%v", r.VerifiedByUser, r.ClassificationAccuracy)
	}
	if r.Corrections != 1 || r.ManualLabels != 1 || r.QuestionsAnswered != 1 {
		t.Errorf("effort counters: %+v", r)
	}
	// 3 judged predictions (a1,a2,q1), 1 correction
	if !near(r.CorrectionRate, 1.0/3) {
		t.Errorf("correction rate: %v", r.CorrectionRate)
	}
	// correct = resolved, non-unknown, not corrected = a1, q1 = 2 ; effort = 1 answer + 1 correction + 1 manual = 3
	if !near(r.EffortRatio, 2.0/3) {
		t.Errorf("effort ratio: %v", r.EffortRatio)
	}
	var bank *CategoryStats
	for i := range r.ByCategory {
		if r.ByCategory[i].Category == "bank" {
			bank = &r.ByCategory[i]
		}
	}
	// bank: predicted twice (a1, a2), one true positive; truth support 2 (a1, u1)
	if bank == nil || !near(bank.Precision, 0.5) || !near(bank.Recall, 0.5) {
		t.Errorf("bank stats: %+v", bank)
	}
	if !strings.Contains(r.Text(), "Auto-clasificación") {
		t.Error("Text() should render")
	}
}

func TestQuestionUsefulness(t *testing.T) {
	db := setup(t)
	addEmail(t, db, "e1", "resolved", "ask", "bank", "answers", 1)
	addEmail(t, db, "e2", "resolved", "ask", "bank", "answers", 1)
	for _, q := range []string{"is_bank"} {
		db.Exec(`INSERT INTO questions(id,text,feature,type,enabled,source,validated,definition) VALUES (?,?,?,?,1,'config',1,'{}')`, q, "?", "a.b", "boolean")
	}
	db.AddInteraction(ctx, storage.Interaction{EmailID: "e1", Kind: storage.KindAnswer, QuestionID: "is_bank", Answer: "yes", ExpectedGain: 0.6, RealizedGain: 0.8}, now)
	db.AddInteraction(ctx, storage.Interaction{EmailID: "e2", Kind: storage.KindSkip, QuestionID: "is_bank"}, now)
	r, _ := Compute(ctx, db)
	if len(r.Questions) != 1 {
		t.Fatalf("%+v", r.Questions)
	}
	q := r.Questions[0]
	if q.Shown != 2 || q.Answered != 1 || !near(q.ResponseRate, 0.5) || !near(q.AvgExpectedGain, 0.6) || !near(q.AvgRealizedGain, 0.8) {
		t.Errorf("%+v", q)
	}
}

func TestEmptyDatabase(t *testing.T) {
	r, err := Compute(ctx, setup(t))
	if err != nil || r.Emails != 0 || r.ClassificationAccuracy != 0 || math.IsNaN(r.EffortRatio) {
		t.Fatalf("%+v %v", r, err)
	}
}
