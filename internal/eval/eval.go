// Package eval implements the offline evaluation of §35: a labelled dataset is
// split deterministically into training / validation / test; the system learns
// online from the training split (with a simulated user), and is then measured
// on held-out data without learning. It also reports the learning curve that
// §40 defines as success: the system should ask less and less.
package eval

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Item is one labelled email.
type Item struct {
	ID       string
	Email    email.Email
	Expected string
}

// LoadDir reads dir/<category-id>/*.eml.
func LoadDir(dir string) ([]Item, error) {
	cats, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, c := range cats {
		if !c.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(dir, c.Name(), "*.eml"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			e, err := email.ParseEMLFile(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			id := c.Name() + "/" + strings.TrimSuffix(filepath.Base(f), ".eml")
			e.ID = ""
			items = append(items, Item{ID: id, Email: e, Expected: c.Name()})
		}
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no .eml files found under %s/<category>/", dir)
	}
	return items, nil
}

// Split divides items deterministically by a hash of their id.
func Split(items []Item, trainPct, valPct int, seed string) (train, val, test []Item) {
	type hi struct {
		it Item
		h  uint32
	}
	hs := make([]hi, len(items))
	for i, it := range items {
		f := fnv.New32a()
		f.Write([]byte(seed + "|" + it.ID))
		hs[i] = hi{it, f.Sum32()}
	}
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].h != hs[j].h {
			return hs[i].h < hs[j].h
		}
		return hs[i].it.ID < hs[j].it.ID
	})
	for _, x := range hs {
		bucket := int(x.h/100) % 100 // different bits than the ordering
		switch {
		case bucket < trainPct:
			train = append(train, x.it)
		case bucket < trainPct+valPct:
			val = append(val, x.it)
		default:
			test = append(test, x.it)
		}
	}
	return
}

// Oracle simulates the user answering questions about an email whose true
// category is known.
type Oracle interface {
	Answer(q questions.Question, truth string, tree *categories.Tree) (answer string, ok bool)
}

// AffinityOracle answers with the most likely answer under the question's
// configured affinity for the true category (inherited from its ancestors).
// Questions without information about the category are skipped.
type AffinityOracle struct{}

// Answer implements Oracle.
func (AffinityOracle) Answer(q questions.Question, truth string, tree *categories.Tree) (string, bool) {
	for cur := truth; cur != ""; {
		if row, ok := q.Affinity[cur]; ok {
			best, bp := "", -1.0
			for _, a := range q.Answers {
				p, ok := row[a.ID]
				if !ok {
					continue
				}
				if p > bp {
					best, bp = a.ID, p
				}
			}
			if best == "" && len(row) > 0 && q.Type == questions.Boolean {
				// shorthand row only contains "yes": a missing "no" is the complement
				if row[questions.Yes] >= 0.5 {
					return questions.Yes, true
				}
				return questions.No, true
			}
			if q.Type == questions.Boolean {
				if row[questions.Yes] >= 0.5 {
					return questions.Yes, true
				}
				return questions.No, true
			}
			return best, best != ""
		}
		c, ok := tree.Get(cur)
		if !ok {
			break
		}
		cur = c.ParentID
	}
	return "", false
}

// Options configure a run.
type Options struct {
	TrainPct, ValPct int
	Seed             string
	Oracle           Oracle
	Window           int // size of the learning-curve windows (default 20)
}

// Phase aggregates the results of one split.
type Phase struct {
	N                int     `json:"n"`
	Accuracy         float64 `json:"accuracy"`         // exact category match
	LenientAccuracy  float64 `json:"lenient_accuracy"` // also counts an ancestor of the truth
	AutoRate         float64 `json:"auto_rate"`        // classified without any question
	QuestionRate     float64 `json:"question_rate"`    // needed at least one question
	AvgQuestions     float64 `json:"avg_questions"`
	UnknownRate      float64 `json:"unknown_rate"`
	AccuracyWhenSure float64 `json:"accuracy_when_sure"` // among "know" decisions: the key safety figure (§41)
	MeanSureScore    float64 `json:"mean_sure_confidence"`
	Sure             int     `json:"sure"`
}

// CurvePoint is the average effort over a window of training emails.
type CurvePoint struct {
	UpTo         int     `json:"up_to"`
	QuestionRate float64 `json:"question_rate"`
	AvgQuestions float64 `json:"avg_questions"`
	Corrections  float64 `json:"correction_rate"`
}

// Miss is a held-out email that was classified wrongly (error analysis).
type Miss struct {
	ID       string  `json:"id"`
	Expected string  `json:"expected"`
	Got      string  `json:"got"`
	Decision string  `json:"decision"`
	Score    float64 `json:"score"`
	Subject  string  `json:"subject"`
}

// Result of a run.
type Result struct {
	Train, Validation, Test Phase
	Curve                   []CurvePoint `json:"learning_curve"`
	Misses                  []Miss       `json:"misses,omitempty"` // validation + test errors
}

// Run executes the evaluation on a fresh in-memory database.
func Run(ctx context.Context, built *config.Built, items []Item, opts Options) (Result, error) {
	if opts.TrainPct == 0 {
		opts.TrainPct, opts.ValPct = 60, 20
	}
	if opts.Oracle == nil {
		opts.Oracle = AffinityOracle{}
	}
	if opts.Window == 0 {
		opts.Window = 20
	}
	if opts.Seed == "" {
		opts.Seed = "eval"
	}
	db, err := storage.Open(":memory:")
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	eng, err := engine.New(ctx, db, built)
	if err != nil {
		return Result{}, err
	}
	train, val, test := Split(items, opts.TrainPct, opts.ValPct, opts.Seed)

	var res Result
	res.Train, res.Curve, err = trainPhase(ctx, eng, train, opts)
	if err != nil {
		return res, err
	}
	frozen := eng.WithoutLearning()
	var m1, m2 []Miss
	if res.Validation, m1, err = measure(ctx, frozen, val, opts, "val:"); err != nil {
		return res, err
	}
	res.Test, m2, err = measure(ctx, frozen, test, opts, "test:")
	res.Misses = append(m1, m2...)
	return res, err
}

func uniqueEmail(it Item, prefix string) email.Email {
	e := it.Email
	e.ID = prefix + it.ID
	return e
}

// resolve answers questions with the oracle until the session ends.
func resolve(ctx context.Context, eng *engine.Engine, out *engine.Outcome, truth string, oracle Oracle) (*engine.Outcome, int, error) {
	asked := 0
	for guard := 0; out.NeedsQuestion && guard < 30; guard++ {
		q := findQuestion(eng, out.Question.ID)
		ans, ok := oracle.Answer(q, truth, eng.Tree())
		var err error
		if ok {
			out, err = eng.Answer(ctx, out.EmailID, q.ID, ans)
		} else {
			out, err = eng.Skip(ctx, out.EmailID, q.ID)
		}
		if err != nil {
			return out, asked, err
		}
		asked++
	}
	return out, asked, nil
}

func findQuestion(eng *engine.Engine, id string) questions.Question {
	for _, q := range eng.Questions() {
		if q.ID == id {
			return q
		}
	}
	return questions.Question{ID: id}
}

func trainPhase(ctx context.Context, eng *engine.Engine, items []Item, opts Options) (Phase, []CurvePoint, error) {
	var ph acc
	var curve []CurvePoint
	wQ, wAsked, wCorr, wN := 0, 0, 0, 0
	for i, it := range items {
		out, err := eng.Process(ctx, uniqueEmail(it, "train:"))
		if err != nil {
			return Phase{}, nil, fmt.Errorf("%s: %w", it.ID, err)
		}
		first := out.Decision
		out, asked, err := resolve(ctx, eng, out, it.Expected, opts.Oracle)
		if err != nil {
			return Phase{}, nil, fmt.Errorf("%s: %w", it.ID, err)
		}
		ph.add(eng, out, it.Expected, asked, first)

		// A diligent user verifies every result: confirms right ones, fixes wrong ones.
		corrected := 0
		switch {
		case out.Decision == engine.DecisionUnknown:
			_, err = eng.Move(ctx, out.EmailID, it.Expected)
		case out.Category == it.Expected:
			_, err = eng.Feedback(ctx, out.EmailID, true, "")
		default:
			corrected = 1
			_, err = eng.Feedback(ctx, out.EmailID, false, it.Expected)
		}
		if err != nil {
			return Phase{}, nil, fmt.Errorf("%s: feedback: %w", it.ID, err)
		}

		wN++
		if asked > 0 {
			wQ++
		}
		wAsked += asked
		wCorr += corrected
		if wN == opts.Window || i == len(items)-1 {
			curve = append(curve, CurvePoint{UpTo: i + 1, QuestionRate: float64(wQ) / float64(wN),
				AvgQuestions: float64(wAsked) / float64(wN), Corrections: float64(wCorr) / float64(wN)})
			wQ, wAsked, wCorr, wN = 0, 0, 0, 0
		}
	}
	return ph.phase(), curve, nil
}

func measure(ctx context.Context, eng *engine.Engine, items []Item, opts Options, prefix string) (Phase, []Miss, error) {
	var ph acc
	var misses []Miss
	for _, it := range items {
		out, err := eng.Process(ctx, uniqueEmail(it, prefix))
		if err != nil {
			return Phase{}, nil, fmt.Errorf("%s: %w", it.ID, err)
		}
		first := out.Decision
		out, asked, err := resolve(ctx, eng, out, it.Expected, opts.Oracle)
		if err != nil {
			return Phase{}, nil, fmt.Errorf("%s: %w", it.ID, err)
		}
		ph.add(eng, out, it.Expected, asked, first)
		if out.Category != it.Expected {
			misses = append(misses, Miss{ID: it.ID, Expected: it.Expected, Got: out.Category, Decision: out.Decision, Score: out.Score, Subject: it.Email.Subject})
		}
	}
	return ph.phase(), misses, nil
}

type acc struct {
	n, exact, lenient, auto, withQ, asked, unknown, sure, sureOK int
	sureScore                                                    float64
}

func (a *acc) add(eng *engine.Engine, out *engine.Outcome, truth string, asked int, firstDecision string) {
	a.n++
	if out.Category == truth {
		a.exact++
		a.lenient++
	} else if eng.Tree().IsAncestor(out.Category, truth) && out.Category != categories.UnknownID {
		a.lenient++
	}
	if asked == 0 && firstDecision == engine.DecisionKnow {
		a.auto++
	}
	if asked > 0 {
		a.withQ++
	}
	a.asked += asked
	if out.Decision == engine.DecisionUnknown {
		a.unknown++
	}
	if out.Decision == engine.DecisionKnow {
		a.sure++
		a.sureScore += out.Score
		if out.Category == truth {
			a.sureOK++
		}
	}
}

func (a *acc) phase() Phase {
	if a.n == 0 {
		return Phase{}
	}
	n := float64(a.n)
	p := Phase{N: a.n, Accuracy: float64(a.exact) / n, LenientAccuracy: float64(a.lenient) / n,
		AutoRate: float64(a.auto) / n, QuestionRate: float64(a.withQ) / n, AvgQuestions: float64(a.asked) / n,
		UnknownRate: float64(a.unknown) / n, Sure: a.sure}
	if a.sure > 0 {
		p.AccuracyWhenSure = float64(a.sureOK) / float64(a.sure)
		p.MeanSureScore = a.sureScore / float64(a.sure)
	}
	return p
}

// Text renders a result for terminals.
func (r Result) Text() string {
	var b strings.Builder
	row := func(name string, p Phase) {
		fmt.Fprintf(&b, "%-11s n=%-4d acierto %5.1f%%  (laxo %5.1f%%)  auto %5.1f%%  con pregunta %5.1f%%  preg/correo %.2f  desconocido %4.1f%%  acierto cuando está seguro %5.1f%% (n=%d, conf. media %.2f)\n",
			name, p.N, p.Accuracy*100, p.LenientAccuracy*100, p.AutoRate*100, p.QuestionRate*100, p.AvgQuestions, p.UnknownRate*100, p.AccuracyWhenSure*100, p.Sure, p.MeanSureScore)
	}
	row("train", r.Train)
	row("validation", r.Validation)
	row("test", r.Test)
	if len(r.Misses) > 0 {
		b.WriteString("\nErrores en validación/test:\n")
		for _, m := range r.Misses {
			fmt.Fprintf(&b, "  %-32s esperado %-16s obtuvo %-16s (%s %.2f) «%s»\n", m.ID, m.Expected, m.Got, m.Decision, m.Score, m.Subject)
		}
	}
	b.WriteString("\nCurva de aprendizaje (entrenamiento) — ¿pregunta cada vez menos?\n")
	for _, c := range r.Curve {
		bar := strings.Repeat("█", int(c.QuestionRate*40+0.5))
		fmt.Fprintf(&b, "  hasta %4d  preguntas %5.1f%% %-40s correcciones %4.1f%%\n", c.UpTo, c.QuestionRate*100, bar, c.Corrections*100)
	}
	return b.String()
}
