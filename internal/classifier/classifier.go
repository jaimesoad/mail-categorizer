package classifier

import (
	"context"
	"fmt"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// Level is the confidence band of a result (§15).
type Level string

const (
	HighConfidence   Level = "high"
	MediumConfidence Level = "medium"
	LowConfidence    Level = "low"
)

// Thresholds are the configurable decision limits (§15).
type Thresholds struct {
	High      float64 // >= High (and margin) => automatic
	Medium    float64 // >= Medium => good prediction; below => low
	MinMargin float64 // minimum gap to the runner-up for automatic classification
}

// DefaultThresholds are the values suggested by the spec plus a margin guard.
func DefaultThresholds() Thresholds { return Thresholds{High: 0.85, Medium: 0.60, MinMargin: 0.20} }

// Result is the outcome of one classification pass.
type Result struct {
	Candidates []Candidate `json:"candidates"`
	Best       Candidate   `json:"best"`
	Margin     float64     `json:"margin"`
	Level      Level       `json:"level"`
	Method     Method      `json:"method"`
	Evidence   []Evidence  `json:"evidence"`
}

// Classifier orchestrates signals + aggregation.
type Classifier struct {
	Tree       *categories.Tree
	Signals    []Signal
	Thresholds Thresholds
}

// Classify gathers evidence from every signal and aggregates it.
func (c *Classifier) Classify(ctx context.Context, e email.Email, f *features.Set) (Result, error) {
	var ev []Evidence
	for _, s := range c.Signals {
		es, err := s.Evidence(ctx, e, f)
		if err != nil {
			return Result{}, fmt.Errorf("signal: %w", err)
		}
		ev = append(ev, es...)
	}
	return c.FromEvidence(ev), nil
}

// FromEvidence aggregates already-collected evidence (used to rebuild a
// session from its frozen evidence).
func (c *Classifier) FromEvidence(ev []Evidence) Result {
	return c.Build(Aggregate(c.Tree, ev), ev)
}

// Build packages a (possibly Bayes-updated) candidate list into a Result.
func (c *Classifier) Build(cands []Candidate, ev []Evidence) Result {
	r := Result{Candidates: cands, Evidence: ev}
	if len(cands) > 0 {
		r.Best = cands[0]
	}
	if len(cands) > 1 {
		r.Margin = cands[0].Score - cands[1].Score
	} else {
		r.Margin = r.Best.Score
	}
	r.Level = c.Thresholds.LevelOf(r)
	r.Method = DominantMethod(c.Tree, r.Best.Category, ev)
	return r
}

// LevelOf maps a result to a confidence band. Unknown is never high/medium.
func (t Thresholds) LevelOf(r Result) Level {
	if r.Best.Category == categories.UnknownID {
		return LowConfidence
	}
	switch {
	case r.Best.Score >= t.High && r.Margin >= t.MinMargin:
		return HighConfidence
	case r.Best.Score >= t.Medium:
		return MediumConfidence
	}
	return LowConfidence
}

// Relevant returns the evidence that supports category id: evidence on id, its
// ancestors, or its descendants, strongest first.
func Relevant(tree *categories.Tree, ev []Evidence, id string) []Evidence {
	var out []Evidence
	for _, e := range ev {
		if e.Weight <= 0 {
			continue
		}
		if e.Category == id || tree.IsAncestor(e.Category, id) || tree.IsAncestor(id, e.Category) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

// DominantMethod is the single method behind a category's support, or
// "combined" when several independent kinds of signal contributed.
func DominantMethod(tree *categories.Tree, id string, ev []Evidence) Method {
	seen := map[Method]bool{}
	var last Method
	for _, e := range Relevant(tree, ev, id) {
		seen[e.Method] = true
		last = e.Method
	}
	switch len(seen) {
	case 0:
		return MethodCombined
	case 1:
		return last
	}
	return MethodCombined
}
