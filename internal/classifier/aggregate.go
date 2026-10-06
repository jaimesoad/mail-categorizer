package classifier

import (
	"math"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
)

// Candidate is a possible outcome with its probability-like mass. Scores of a
// distribution sum to 1 and the residual is assigned to categories.UnknownID,
// which makes "I don't know" a first-class outcome (§41). A Candidate whose
// Category is an inner node means "belongs there, not refined further".
type Candidate struct {
	Category string  `json:"category"`
	Score    float64 `json:"score"`
}

const (
	maxWeight   = 0.999
	maxEvidence = 0.9999
	epsilon     = 1e-9
)

// Aggregate turns evidence into a candidate distribution.
//
//  1. Per category, positive weights combine with noisy-OR (independent
//     supporting signals); negative weights multiply by (1-|w|) (suppression).
//  2. Evidence accumulates up the tree: E(n) = supp(n) · noisyOR(pos(n), E(children)).
//  3. Roots compete as hypotheses against a null "unknown" hypothesis of odds 1:
//     P(root) = o/(1+Σo) with o = E/(1-E). A lone category keeps P = E; strong
//     evidence beats weak evidence instead of merely sharing the mass with it;
//     conflicting equal evidence leaves a large unknown share.
//  4. Going down, the children of n receive the part of P(n) that child
//     evidence explains (coverage = noisyOR(E(children)) / E(n), at most 1),
//     split by their odds. What is not explained stays at n ("n, not refined").
//     Evidence aimed at a parent therefore never fights its own children.
func Aggregate(tree *categories.Tree, ev []Evidence) []Candidate {
	pos := map[string]float64{}  // product of (1-w) over positive weights
	supp := map[string]float64{} // product of (1-|w|) over negative weights
	for _, e := range ev {
		if e.Category == categories.UnknownID || !tree.Has(e.Category) || !tree.Enabled(e.Category) {
			continue
		}
		w := math.Max(-maxWeight, math.Min(maxWeight, e.Weight))
		switch {
		case w > 0:
			if _, ok := pos[e.Category]; !ok {
				pos[e.Category] = 1
			}
			pos[e.Category] *= 1 - w
		case w < 0:
			if _, ok := supp[e.Category]; !ok {
				supp[e.Category] = 1
			}
			supp[e.Category] *= 1 - (-w)
		}
	}

	E := map[string]float64{}  // subtree evidence
	EC := map[string]float64{} // evidence explained by the children alone
	var subtree func(id string) float64
	subtree = func(id string) float64 {
		kids := 1.0
		for _, c := range tree.Children(id) {
			kids *= 1 - subtree(c)
		}
		own := 1.0
		if p, ok := pos[id]; ok {
			own = p
		}
		v := 1 - own*kids
		if s, ok := supp[id]; ok {
			v *= s
		}
		E[id] = math.Min(v, maxEvidence)
		EC[id] = 1 - kids
		return E[id]
	}
	odds := func(e float64) float64 { return e / (1 - e) }
	roots := tree.Roots()
	z := 1.0
	for _, r := range roots {
		z += odds(subtree(r))
	}

	var out []Candidate
	var expand func(id string, p float64)
	expand = func(id string, p float64) {
		if p < epsilon {
			return
		}
		kids := tree.Children(id)
		if len(kids) == 0 || EC[id] < epsilon || E[id] < epsilon {
			out = append(out, Candidate{id, p})
			return
		}
		cover := math.Min(1, EC[id]/E[id])
		sumOdds := 0.0
		for _, c := range kids {
			sumOdds += odds(E[c])
		}
		rest := p
		for _, c := range kids {
			share := p * cover * odds(E[c]) / sumOdds
			rest -= share
			expand(c, share)
		}
		if rest > epsilon {
			out = append(out, Candidate{id, rest})
		}
	}
	total := 0.0
	for _, r := range roots {
		p := odds(E[r]) / z
		total += p
		expand(r, p)
	}
	if u := 1 - total; u > epsilon {
		out = append(out, Candidate{categories.UnknownID, u})
	}
	return Normalize(out)
}

// Normalize sorts candidates by score (desc, id asc), drops dust and rescales
// so the scores sum to exactly 1 when anything is left.
func Normalize(in []Candidate) []Candidate {
	sum := 0.0
	out := make([]Candidate, 0, len(in))
	for _, c := range in {
		if c.Score > epsilon {
			out = append(out, c)
			sum += c.Score
		}
	}
	if sum == 0 {
		return []Candidate{{categories.UnknownID, 1}}
	}
	for i := range out {
		out[i].Score /= sum
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Category < out[j].Category
	})
	return out
}

// Update applies a Bayesian update: p'(c) ∝ p(c)·likelihood(c). Used after the
// user answers a question.
func Update(cands []Candidate, likelihood func(category string) float64) []Candidate {
	out := make([]Candidate, len(cands))
	for i, c := range cands {
		out[i] = Candidate{c.Category, c.Score * likelihood(c.Category)}
	}
	return Normalize(out)
}

// Entropy is the Shannon entropy in bits of a distribution.
func Entropy(cands []Candidate) float64 {
	h := 0.0
	for _, c := range cands {
		if c.Score > 0 {
			h -= c.Score * math.Log2(c.Score)
		}
	}
	return h
}

// SubtreeMass returns the total score of category id and all its descendants:
// "how sure are we that it belongs somewhere under id".
func SubtreeMass(tree *categories.Tree, cands []Candidate, id string) float64 {
	m := 0.0
	for _, c := range cands {
		if c.Category == id || tree.IsAncestor(id, c.Category) {
			m += c.Score
		}
	}
	return m
}
