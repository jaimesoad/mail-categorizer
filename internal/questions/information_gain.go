package questions

import "github.com/jaimesoad/mail-categorizer/internal/classifier"

// InformationGain is H(before) − E_answer[H(after)] in bits (§12), computed
// with the raw P(answer | category) of the model.
func InformationGain(cands []classifier.Candidate, m Model) float64 {
	before := classifier.Entropy(cands)
	expectedAfter := 0.0
	for _, a := range m.Q.Answers {
		pa := 0.0
		for _, c := range cands {
			pa += c.Score * m.Dist(c.Category)[a.ID]
		}
		if pa <= 0 {
			continue
		}
		post := classifier.Update(cands, func(cat string) float64 { return m.Dist(cat)[a.ID] })
		expectedAfter += pa * classifier.Entropy(post)
	}
	g := before - expectedAfter
	if g < 0 {
		return 0
	}
	return g
}

// Posterior applies the user's answer to the candidates (Bayes), using the
// reliability-damped likelihood.
func Posterior(cands []classifier.Candidate, m Model, answerID string) []classifier.Candidate {
	return classifier.Update(cands, func(cat string) float64 { return m.Likelihood(cat, answerID) })
}
