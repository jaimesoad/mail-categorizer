package pretrain

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/samples"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

var bg = context.Background()

type sliceSource struct {
	emails []email.Email
	i      int
}

func (s *sliceSource) Next(context.Context) (email.Email, error) {
	if s.i >= len(s.emails) {
		return email.Email{}, errEOF
	}
	s.i++
	return s.emails[s.i-1], nil
}
func (s *sliceSource) Close() error { return nil }

var errEOF = errors.New("EOF")

func init() { errEOF = io.EOF }

func corpus(t *testing.T, seed int64, per int) ([]email.Email, map[string]string) {
	t.Helper()
	var out []email.Email
	truth := map[string]string{}
	for _, s := range samples.Generate(seed, per) {
		e, err := email.ParseEML(s.EML)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
		truth[e.MessageID] = s.Category
	}
	return out, truth
}

// oracleTeacher knows the truth, optionally gets some wrong or is unsure.
type oracleTeacher struct {
	truth    map[string]string
	wrongOn  map[string]string // message-id -> wrong category
	unsure   map[string]bool
	failAll  bool
	failures int
}

func (o *oracleTeacher) Categorize(_ context.Context, e email.Email) (string, float64, error) {
	if o.failAll {
		o.failures++
		return "", 0, errors.New("ollama down")
	}
	if o.unsure[e.MessageID] {
		return "", 0, nil
	}
	if w, ok := o.wrongOn[e.MessageID]; ok {
		return w, 0.9, nil
	}
	return o.truth[e.MessageID], 0.95, nil
}

func newEngine(t *testing.T) *engine.Engine {
	t.Helper()
	cfg, _ := config.LoadOrDefault("")
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e, err := engine.New(bg, db, built, engine.WithClock(time.Now))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPretrainSeedsKnowledgeAndImprovesAgreement(t *testing.T) {
	mails, truth := corpus(t, 11, 30)
	eng := newEngine(t)
	var early, late Stats
	st, err := Run(bg, eng, &oracleTeacher{truth: truth}, &sliceSource{emails: mails}, Options{
		ProgressEvery: 60, Progress: func(s Stats) {
			if early.Seen == 0 {
				early = s
			}
			late = s
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Seen != len(mails) || st.Labeled != len(mails) || st.LLMErrors != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if len(st.ByCategory) != len(samples.Categories()) {
		t.Errorf("every category should be represented: %v", st.ByCategory)
	}
	// the system should agree with the teacher more as it learns
	earlyRate := early.AgreementRate()
	lateRate := float64(st.Agreed-early.Agreed) / float64(st.Labeled-early.Labeled)
	t.Logf("agreement: first window %.0f%% -> rest %.0f%% (overall %.0f%%)", earlyRate*100, lateRate*100, st.AgreementRate()*100)
	if lateRate <= earlyRate {
		t.Errorf("agreement with the teacher should grow as knowledge accumulates: %.2f -> %.2f", earlyRate, lateRate)
	}
	_ = late

	// all labels are teacher labels, none are human
	labels, _ := eng.DB().Labels(bg)
	for id, l := range labels {
		if l.Source != "llm" {
			t.Fatalf("%s: %+v", id, l)
		}
	}

	// the pre-trained system now handles NEW mail of the same kind with few questions
	fresh, ftruth := corpus(t, 999, 12)
	frozen := eng.WithoutLearning()
	correct, asked, sure, sureOK := 0, 0, 0, 0
	for _, m := range fresh {
		out, err := frozen.Process(bg, m)
		if err != nil {
			t.Fatal(err)
		}
		if out.NeedsQuestion {
			asked++
		}
		if out.Category == ftruth[m.MessageID] {
			correct++
		}
		if out.Decision == engine.DecisionKnow {
			sure++
			if out.Category == ftruth[m.MessageID] {
				sureOK++
			}
		}
	}
	acc := float64(correct) / float64(len(fresh))
	askRate := float64(asked) / float64(len(fresh))
	t.Logf("fresh mail after pre-training: accuracy %.0f%%, question rate %.0f%%, sure %d (%d right)", acc*100, askRate*100, sure, sureOK)
	if acc < 0.8 || askRate > 0.3 {
		t.Errorf("pre-training should leave a useful system: accuracy %.2f, question rate %.2f", acc, askRate)
	}
	if sure > 0 && float64(sureOK)/float64(sure) < 0.95 {
		t.Errorf("when sure it must be right: %d/%d", sureOK, sure)
	}
}

func TestResumableAndOptions(t *testing.T) {
	mails, truth := corpus(t, 3, 5)
	eng := newEngine(t)
	if st, err := Run(bg, eng, &oracleTeacher{truth: truth}, &sliceSource{emails: mails}, Options{Limit: 20}); err != nil || st.Seen != 20 || st.Labeled != 20 {
		t.Fatalf("limit: %+v %v", st, err)
	}
	// second run over the same source: the first 20 are duplicates, the rest are new
	st, err := Run(bg, eng, &oracleTeacher{truth: truth}, &sliceSource{emails: mails}, Options{})
	if err != nil || st.Duplicates != 20 || st.Labeled != len(mails)-20 {
		t.Fatalf("resume: %+v %v", st, err)
	}
	// stride + skip
	eng2 := newEngine(t)
	st, _ = Run(bg, eng2, &oracleTeacher{truth: truth}, &sliceSource{emails: mails}, Options{Skip: 10, Stride: 5})
	want := (len(mails) - 10 + 4) / 5
	if st.Seen != want {
		t.Errorf("skip/stride: seen %d want %d", st.Seen, want)
	}
}

func TestUnsureTeacherLeavesEmailsUnlabelled(t *testing.T) {
	mails, truth := corpus(t, 4, 3)
	unsure := map[string]bool{mails[0].MessageID: true, mails[1].MessageID: true}
	eng := newEngine(t)
	st, err := Run(bg, eng, &oracleTeacher{truth: truth, unsure: unsure}, &sliceSource{emails: mails}, Options{})
	if err != nil || st.Unlabeled != 2 || st.Labeled != len(mails)-2 {
		t.Fatalf("%+v %v", st, err)
	}
	labels, _ := eng.DB().Labels(bg)
	if _, ok := labels[mailID(t, eng, mails[0])]; ok {
		t.Error("an email the teacher was unsure about must stay unlabelled")
	}
}

func mailID(t *testing.T, eng *engine.Engine, m email.Email) string {
	t.Helper()
	return email.Normalize(m, 0).ID
}

func TestAbortsWhenTheModelKeepsFailing(t *testing.T) {
	mails, truth := corpus(t, 5, 3)
	teacher := &oracleTeacher{truth: truth, failAll: true}
	st, err := Run(bg, newEngine(t), teacher, &sliceSource{emails: mails}, Options{MaxConsecutiveErrors: 4})
	if err == nil || !strings.Contains(err.Error(), "LLM") || st.LLMErrors != 4 || teacher.failures != 4 {
		t.Fatalf("must abort instead of burning through the mailbox: %+v %v", st, err)
	}
}

func TestCancellationStopsGracefully(t *testing.T) {
	mails, truth := corpus(t, 6, 5)
	ctx, cancel := context.WithCancel(bg)
	n := 0
	teacher := teacherFunc(func(ctx context.Context, e email.Email) (string, float64, error) {
		n++
		if n == 7 {
			cancel()
		}
		return truth[e.MessageID], 0.9, nil
	})
	st, err := Run(ctx, newEngine(t), teacher, &sliceSource{emails: mails}, Options{})
	if !errors.Is(err, context.Canceled) || st.Labeled < 6 || st.Labeled >= len(mails) {
		t.Fatalf("%+v %v", st, err)
	}
}

type teacherFunc func(context.Context, email.Email) (string, float64, error)

func (f teacherFunc) Categorize(ctx context.Context, e email.Email) (string, float64, error) {
	return f(ctx, e)
}

func TestAuditCorrectsTeacherMistakesAndUserWins(t *testing.T) {
	mails, truth := corpus(t, 8, 10)
	wrong := map[string]string{mails[0].MessageID: "finance.bank"}
	if truth[mails[0].MessageID] == "finance.bank" {
		wrong[mails[0].MessageID] = "work"
	}
	eng := newEngine(t)
	if _, err := Run(bg, eng, &oracleTeacher{truth: truth, wrongOn: wrong}, &sliceSource{emails: mails}, Options{}); err != nil {
		t.Fatal(err)
	}
	labels, _ := eng.DB().Labels(bg)
	sample := AuditSample(labels, 1000)
	if len(sample) != len(mails) {
		t.Fatalf("every teacher label should be auditable: %d vs %d", len(sample), len(mails))
	}
	// the person reviews the wrong one
	id := mailID(t, eng, mails[0])
	if _, err := eng.Feedback(bg, id, false, truth[mails[0].MessageID]); err != nil {
		t.Fatal(err)
	}
	labels, _ = eng.DB().Labels(bg)
	if labels[id].Source != "user" || labels[id].Category != truth[mails[0].MessageID] {
		t.Fatalf("human correction must win: %+v", labels[id])
	}
	if after := AuditSample(labels, 1000); len(after) != len(mails)-1 {
		t.Errorf("a verified email must leave the audit pool: %d", len(after))
	}
}

func TestAuditSampleIsStratifiedAndDeterministic(t *testing.T) {
	labels := map[string]storage.Label{}
	for i := 0; i < 50; i++ {
		labels[strings.Repeat("a", 1)+string(rune('A'+i%26))+string(rune('a'+i/26))+"-big"] = storage.Label{Category: "big", Source: "llm"}
	}
	labels["rare1"] = storage.Label{Category: "rare", Source: "llm"}
	labels["rare2"] = storage.Label{Category: "rare", Source: "llm"}
	labels["human"] = storage.Label{Category: "big", Source: "user"}
	a, b := AuditSample(labels, 6), AuditSample(labels, 6)
	if len(a) != 6 || strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("not deterministic: %v vs %v", a, b)
	}
	rare := 0
	for _, id := range a {
		if strings.HasPrefix(id, "rare") {
			rare++
		}
		if id == "human" {
			t.Error("human-verified emails must not be audited again")
		}
	}
	if rare != 2 {
		t.Errorf("rare categories must be covered first: %v", a)
	}
}

func TestRunDBLabelsWhatWasImportedQuietly(t *testing.T) {
	mails, truth := corpus(t, 21, 6)
	eng := newEngine(t)
	quiet := engine.WithQuiet(bg)
	for _, m := range mails { // what n8n's bulk import does through the API
		if _, err := eng.Process(quiet, m); err != nil {
			t.Fatal(err)
		}
	}
	if ids, _ := eng.DB().EmailsAwaitingQuestion(bg, 100); len(ids) != 0 {
		t.Fatalf("quiet imports must never reach the notification queue: %v", ids)
	}
	st, err := RunDB(bg, eng, &oracleTeacher{truth: truth}, Options{})
	if err != nil || st.Labeled != len(mails) {
		t.Fatalf("%+v %v", st, err)
	}
	// resumable and complete: nothing left to label
	st, _ = RunDB(bg, eng, &oracleTeacher{truth: truth}, Options{})
	if st.Seen != 0 {
		t.Fatalf("labelled emails must leave the queue: %+v", st)
	}
	if p, _ := eng.DB().SyncPending(bg, 10); len(p) != 0 {
		t.Error("history must not be pushed to the mailbox")
	}
}
