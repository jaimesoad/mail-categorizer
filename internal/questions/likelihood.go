package questions

import (
	"github.com/jaimesoad/mail-categorizer/internal/categories"
)

// Stats is the learned evidence about one question, supplied by storage.
type Stats struct {
	Shown    int                           // times it was presented
	Answered int                           // times the user answered (not skipped)
	Counts   map[string]map[string]float64 // category -> answer id -> count (user-confirmed labels only)
}

// priorStrength is the pseudo-count weight of the configured affinity versus
// real observations in the likelihood estimate.
const priorStrength = 3.0

// Model gives P(answer | category) for one question by blending the
// configured affinity (cold start) with counts from user-confirmed history.
type Model struct {
	Q     Question
	Tree  *categories.Tree
	Stats Stats
}

// prior returns the configured P(a|c) inheriting from ancestors; ok=false when
// nothing is configured (uninformative).
func (m Model) prior(cat string) (map[string]float64, bool) {
	for cur := cat; cur != ""; {
		if row, ok := m.Q.Affinity[cur]; ok {
			return normalizeRow(m.Q, row), true
		}
		c, ok := m.Tree.Get(cur)
		if !ok {
			break
		}
		cur = c.ParentID
	}
	return nil, false
}

// normalizeRow turns partial rows (e.g. only "yes") into a full distribution.
func normalizeRow(q Question, row map[string]float64) map[string]float64 {
	out := map[string]float64{}
	sum, missing := 0.0, 0
	for _, a := range q.Answers {
		if p, ok := row[a.ID]; ok {
			out[a.ID] = p
			sum += p
		} else {
			missing++
		}
	}
	if missing > 0 {
		rest := 1 - sum
		if rest < 0 {
			rest = 0
		}
		for _, a := range q.Answers {
			if _, ok := out[a.ID]; !ok {
				out[a.ID] = rest / float64(missing)
			}
		}
		return out
	}
	if sum > 0 && (sum < 0.999 || sum > 1.001) {
		for k := range out {
			out[k] /= sum
		}
	}
	return out
}

// Dist returns the (raw, unmixed) distribution over answers for a category.
//
// "unknown" is the mass of categories we have no evidence for, so its answer
// distribution is the average over all leaf categories (a uniform prior over
// where the email could belong). That way a decisive answer shrinks the unknown
// mass instead of leaving it untouched.
func (m Model) Dist(cat string) map[string]float64 {
	if cat == categories.UnknownID && m.Tree != nil {
		return m.unknownDist()
	}
	return m.dist(cat)
}

func (m Model) unknownDist() map[string]float64 {
	out := make(map[string]float64, len(m.Q.Answers))
	n := 0
	for _, c := range m.Tree.All() {
		if c.ID == categories.UnknownID || !m.Tree.Enabled(c.ID) || len(m.Tree.Children(c.ID)) > 0 {
			continue
		}
		n++
		for a, p := range m.dist(c.ID) {
			out[a] += p
		}
	}
	if n == 0 {
		for _, a := range m.Q.Answers {
			out[a.ID] = 1 / float64(len(m.Q.Answers))
		}
		return out
	}
	for a := range out {
		out[a] /= float64(n)
	}
	return out
}

func (m Model) dist(cat string) map[string]float64 {
	n := float64(len(m.Q.Answers))
	prior, has := m.prior(cat)
	counts := m.Stats.Counts[cat]
	total := 0.0
	for _, c := range counts {
		total += c
	}
	out := make(map[string]float64, len(m.Q.Answers))
	for _, a := range m.Q.Answers {
		p0 := 1 / n
		if has {
			p0 = prior[a.ID]
		}
		out[a.ID] = (counts[a.ID] + priorStrength*p0) / (total + priorStrength)
	}
	return out
}

// Likelihood returns P(answer | category) damped by the question's reliability:
// a user may mis-click, so no answer ever fully rules a category out.
func (m Model) Likelihood(cat, answer string) float64 {
	r := m.Q.Reliability
	if r == 0 {
		r = 0.95
	}
	n := float64(len(m.Q.Answers))
	return r*m.Dist(cat)[answer] + (1-r)/n
}
