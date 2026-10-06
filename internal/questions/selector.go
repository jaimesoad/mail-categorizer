package questions

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// StatsProvider supplies learned per-question statistics (storage implements it).
type StatsProvider interface {
	QuestionStats(ctx context.Context) (map[string]Stats, error)
}

// Params tune the selector (§13).
type Params struct {
	MinGain             float64 // bits; questions below this are never asked
	KnownConfidence     float64 // skip questions whose feature is already known at least this well
	MaxCandidates       int     // candidates considered for the entropy computation
	RepetitionPenalty   float64 // per already-asked question of the same feature family
	WordPenaltyFree     int     // words allowed before the complexity penalty
	WordPenalty         float64 // per extra word
	ResponsePriorAnswer float64 // pseudo-counts for the response-rate estimate (answered)
	ResponsePriorShown  float64 // pseudo-counts for the response-rate estimate (shown)
}

// DefaultParams returns sensible defaults.
func DefaultParams() Params {
	return Params{
		MinGain: 0.05, KnownConfidence: 0.9, MaxCandidates: 8, RepetitionPenalty: 0.02,
		WordPenaltyFree: 12, WordPenalty: 0.002, ResponsePriorAnswer: 4, ResponsePriorShown: 5,
	}
}

// Scored is a question together with how it scored.
type Scored struct {
	Question Question `json:"question"`
	Gain     float64  `json:"information_gain"`
	Utility  float64  `json:"utility"`
}

// Selector picks the next question: the one that best separates the current
// candidates, discounted by practicalities (§12, §13).
type Selector struct {
	Tree      *categories.Tree
	Questions []Question
	Stats     StatsProvider // optional
	Params    Params
}

// Session is the part of a classification session the selector needs.
type Session struct {
	Candidates []classifier.Candidate
	Features   *features.Set
	Asked      []string // question ids already asked (and answered or skipped)
}

// Select returns the best question or nil when none is worth asking.
func (s *Selector) Select(ctx context.Context, ss Session) (*Scored, error) {
	ranked, err := s.Rank(ctx, ss)
	if err != nil || len(ranked) == 0 {
		return nil, err
	}
	best := ranked[0]
	return &best, nil
}

// Rank scores every eligible question, best first (deterministic order).
func (s *Selector) Rank(ctx context.Context, ss Session) ([]Scored, error) {
	var stats map[string]Stats
	if s.Stats != nil {
		var err error
		if stats, err = s.Stats.QuestionStats(ctx); err != nil {
			return nil, err
		}
	}
	cands := topCandidates(ss.Candidates, s.Params.MaxCandidates)
	if len(cands) < 2 {
		return nil, nil
	}
	asked := map[string]bool{}
	families := map[string]int{}
	for _, id := range ss.Asked {
		asked[id] = true
		for _, q := range s.Questions {
			if q.ID == id {
				families[family(q.Feature)]++
			}
		}
	}

	var out []Scored
	for _, q := range s.Questions {
		if q.Disabled || !q.Validated || !q.Selectable() || asked[q.ID] {
			continue
		}
		// "ask only what you don't know": skip if the feature is already known well.
		if ss.Features != nil && ss.Features.Confidence(q.Feature) >= s.Params.KnownConfidence {
			continue
		}
		st := stats[q.ID]
		m := Model{Q: q, Tree: s.Tree, Stats: st}
		gain := InformationGain(cands, m)
		if gain < s.Params.MinGain {
			continue
		}
		response := (float64(st.Answered) + s.Params.ResponsePriorAnswer) / (float64(st.Shown) + s.Params.ResponsePriorShown)
		confQ := q.Confidence * math.Min(1, response)
		rel := q.Reliability
		if rel == 0 {
			rel = 0.95
		}
		rep := s.Params.RepetitionPenalty * float64(families[family(q.Feature)])
		words := len(strings.Fields(q.Text))
		cplx := q.Complexity
		if extra := words - s.Params.WordPenaltyFree; extra > 0 {
			cplx += float64(extra) * s.Params.WordPenalty
		}
		util := gain*confQ*rel - rep - cplx
		if util <= 0 {
			continue
		}
		out = append(out, Scored{Question: q, Gain: gain, Utility: util})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Utility != out[j].Utility {
			return out[i].Utility > out[j].Utility
		}
		return out[i].Question.ID < out[j].Question.ID
	})
	return out, nil
}

func family(feature string) string {
	if i := strings.LastIndexByte(feature, '.'); i > 0 {
		return feature[:i]
	}
	return feature
}

func topCandidates(in []classifier.Candidate, n int) []classifier.Candidate {
	out := make([]classifier.Candidate, 0, n)
	for _, c := range in {
		if c.Score < 0.01 {
			break // sorted desc
		}
		out = append(out, c)
		if len(out) == n {
			break
		}
	}
	return classifier.Normalize(out)
}

// ProposeInput is what a question generator may see: the candidates and the
// known features — and the email itself only if the generator is allowed to.
type ProposeInput struct {
	Email      email.Email
	Candidates []classifier.Candidate
	Features   *features.Set
	Asked      []string
}
