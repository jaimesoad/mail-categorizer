// Package learning turns user feedback into reusable knowledge: learned rules
// (positive and negative) whose strength grows with confirmations and is only
// attenuated, never erased, by an isolated contradiction (§17–§21).
package learning

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// Kinds of generalization, from most specific to most general (§19).
const (
	KindSenderEmail  = "sender_email"
	KindList         = "list"
	KindSenderDomain = "sender_domain"
	KindDomainIntent = "domain_intent"
	KindIntent       = "intent"
)

// weightCap limits how strong a rule of each kind can become: a single sender
// can be trusted almost fully, a bare intent ("receipt") never dominates.
var weightCap = map[string]float64{
	KindSenderEmail: 0.95, KindList: 0.90, KindSenderDomain: 0.90, KindDomainIntent: 0.95, KindIntent: 0.50,
}

// Cond is one equality condition of a learned rule. Learned rules are
// conjunctions (AND) of equalities, stored as JSON.
type Cond struct {
	Feature string `json:"feature"`
	Value   string `json:"value"`
}

// Rule is a learned rule. Negative rules mean "NOT this category" (§21).
type Rule struct {
	ID            string  `json:"id"`
	CategoryID    string  `json:"category_id"`
	Key           string  `json:"key"` // canonical form of Conditions; used for lookup
	Kind          string  `json:"kind"`
	Conditions    []Cond  `json:"conditions"`
	Negative      bool    `json:"negative"`
	Weight        float64 `json:"weight"`
	Observations  int     `json:"observations"`
	Confirmations int     `json:"confirmations"` // confirmed by a person
	Rejections    int     `json:"rejections"`
	// TeacherConfirmations counts confirmations that came only from the local
	// LLM "teacher": they count for half and cannot make a rule strong alone.
	TeacherConfirmations int       `json:"teacher_confirmations,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Repo is the persistence the learner and the learned signal need.
type Repo interface {
	// RulesByKeys returns every rule (positive and negative, any category)
	// whose key is in keys.
	RulesByKeys(ctx context.Context, keys []string) ([]Rule, error)
	GetRule(ctx context.Context, category, key string, negative bool) (*Rule, error)
	// SaveRule inserts or updates by (category, key, negative).
	SaveRule(ctx context.Context, r Rule) error
}

// teacherWeight is how much one LLM-teacher confirmation counts relative to a
// human one; teacherCap bounds rules that no person has ever confirmed.
const (
	teacherWeight = 0.5
	teacherCap    = 0.70
)

// EffectiveConfirmations blends human and teacher confirmations.
func (r Rule) EffectiveConfirmations() float64 {
	return float64(r.Confirmations) + teacherWeight*float64(r.TeacherConfirmations)
}

// Weight maps counts to a rule weight (§18): confirmations grow it with
// diminishing returns (1→0.20, 3→0.45, 10→0.80 for cap 0.9) and the share of
// rejections attenuates it without ever zeroing a well-confirmed rule.
func Weight(cap float64, confirmations float64, rejections int) float64 {
	if confirmations <= 0 {
		return 0
	}
	growth := cap * (1 - math.Exp(-0.25*confirmations))
	precision := (confirmations + 1) / (confirmations + float64(rejections) + 1)
	return round4(growth * precision)
}

// RuleWeight is Weight applied to a rule, honouring the teacher cap.
func RuleWeight(r Rule) float64 {
	cap := weightCap[r.Kind]
	if r.Confirmations == 0 && cap > teacherCap {
		cap = teacherCap
	}
	return Weight(cap, r.EffectiveConfirmations(), r.Rejections)
}

func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// Generalization is a candidate condition set derived from an email's features.
type Generalization struct {
	Kind  string
	Key   string
	Conds []Cond
	Cap   float64
}

func key(conds []Cond) string {
	cs := append([]Cond(nil), conds...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Feature < cs[j].Feature })
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.Feature + "=" + c.Value
	}
	return strings.Join(parts, "|")
}

// Generalizations derives, from the extractable features of an email, the
// condition sets worth learning, ordered from specific to general. Features
// that only exist because the user answered a question are never used: they
// would not be present on future emails.
func Generalizations(f *features.Set) []Generalization {
	var out []Generalization
	add := func(kind string, conds ...Cond) {
		out = append(out, Generalization{Kind: kind, Key: key(conds), Conds: conds, Cap: weightCap[kind]})
	}
	if v := f.Str(features.SenderEmail); v != "" {
		add(KindSenderEmail, Cond{features.SenderEmail, v})
	}
	if v := f.Str(features.HeadersListID); v != "" {
		add(KindList, Cond{features.HeadersListID, v})
	}
	domain := f.Str(features.SenderDomain)
	free, _ := f.Bool(features.SenderIsFreemail)
	if domain != "" && !free { // "gmail.com → Bank" would be a terrible generalization
		add(KindSenderDomain, Cond{features.SenderDomain, domain})
		n := 0
		for _, in := range topIntents(f, 0.6, 2) {
			add(KindDomainIntent, Cond{features.SenderDomain, domain}, Cond{features.ContentIntent, in})
			n++
		}
	}
	if ins := topIntents(f, 0.7, 1); len(ins) > 0 {
		add(KindIntent, Cond{features.ContentIntent, ins[0]})
	}
	return out
}

func topIntents(f *features.Set, minConf float64, n int) []string {
	vals := f.Values(features.ContentIntent)
	sort.SliceStable(vals, func(i, j int) bool {
		if vals[i].Confidence != vals[j].Confidence {
			return vals[i].Confidence > vals[j].Confidence
		}
		return vals[i].Value < vals[j].Value
	})
	var out []string
	for _, v := range vals {
		if v.Confidence >= minConf && v.Source != features.SourceUser {
			out = append(out, v.Value)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// Keys returns the lookup keys of all generalizations of f.
func Keys(f *features.Set) []string {
	gs := Generalizations(f)
	keys := make([]string, len(gs))
	for i, g := range gs {
		keys[i] = g.Key
	}
	return keys
}
