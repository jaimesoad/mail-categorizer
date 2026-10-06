package learning

import (
	"context"
	"fmt"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// minUsefulWeight hides rules too weak to matter.
const minUsefulWeight = 0.02

// dependentDiscount: learned rules of one email overlap (sender ⊂ domain ⊂
// domain+intent), so they are not independent evidence. Per category and
// polarity only the strongest counts fully; the others count at this fraction.
const dependentDiscount = 0.5

// Signal exposes learned rules as classifier evidence.
type Signal struct {
	Repo Repo
	Name func(category string) string // human readable category path
}

// Evidence implements classifier.Signal.
func (s Signal) Evidence(ctx context.Context, _ email.Email, f *features.Set) ([]classifier.Evidence, error) {
	keys := Keys(f)
	if len(keys) == 0 {
		return nil, nil
	}
	rules, err := s.Repo.RulesByKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	// A condition that was confirmed for several categories (one domain that
	// sends both "work" and "meetings" mail) is not discriminative: each rule
	// only keeps its share of the confirmations seen for that condition.
	totals := map[string]int{}
	for _, r := range rules {
		if !r.Negative {
			totals[r.Key] += r.Confirmations
		}
	}
	type group struct {
		cat string
		neg bool
	}
	groups := map[group][]Rule{}
	for _, r := range rules {
		if !r.Negative && totals[r.Key] > 0 {
			r.Weight *= float64(r.Confirmations) / float64(totals[r.Key])
		}
		if r.Weight < minUsefulWeight {
			continue
		}
		g := group{r.CategoryID, r.Negative}
		groups[g] = append(groups[g], r)
	}
	gkeys := make([]group, 0, len(groups))
	for g := range groups {
		gkeys = append(gkeys, g)
	}
	sort.Slice(gkeys, func(i, j int) bool {
		if gkeys[i].cat != gkeys[j].cat {
			return gkeys[i].cat < gkeys[j].cat
		}
		return !gkeys[i].neg && gkeys[j].neg
	})
	name := s.Name
	if name == nil {
		name = func(c string) string { return c }
	}
	var out []classifier.Evidence
	for _, g := range gkeys {
		rs := groups[g]
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].Weight != rs[j].Weight {
				return rs[i].Weight > rs[j].Weight
			}
			return rs[i].Key < rs[j].Key
		})
		for i, r := range rs {
			w := r.Weight
			if i > 0 {
				w *= dependentDiscount
			}
			if r.Negative {
				w = -w
			}
			out = append(out, classifier.Evidence{
				Category: r.CategoryID, Weight: w, Method: classifier.MethodLearned,
				Reason: describe(r, name(r.CategoryID)), Ref: r.Key,
			})
		}
	}
	return out, nil
}

func describe(r Rule, cat string) string {
	what := map[string]string{
		KindSenderEmail:  "el remitente",
		KindList:         "la lista de correo",
		KindSenderDomain: "el dominio",
		KindDomainIntent: "el dominio con ese tipo de mensaje",
		KindIntent:       "ese tipo de mensaje",
	}[r.Kind]
	val := ""
	for i, c := range r.Conditions {
		if i > 0 {
			val += " + "
		}
		val += c.Value
	}
	if r.Negative {
		return fmt.Sprintf("Aprendido: %s (%s) NO suele ir en %s (corregido %d %s)", what, val, cat, r.Confirmations, veces(r.Confirmations))
	}
	return fmt.Sprintf("Aprendido: %s (%s) se ha clasificado %d %s como %s", what, val, r.Confirmations, veces(r.Confirmations), cat)
}

func veces(n int) string {
	if n == 1 {
		return "vez"
	}
	return "veces"
}
