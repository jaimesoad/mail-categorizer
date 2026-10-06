// Package similarity finds previously classified emails that resemble a new one
// (TF-IDF cosine k-NN, §22) and turns the neighbourhood into classifier evidence.
// The Index interface lets an embedding backend replace TF-IDF later.
package similarity

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/textutil"
)

// Doc is an already classified email. Trust is 1.0 for user-confirmed labels.
type Doc struct {
	ID       string
	Category string
	Text     string
	Trust    float64
}

// Neighbor is a similar document.
type Neighbor struct {
	ID       string
	Category string
	Sim      float64
	Trust    float64
}

// Index answers nearest-neighbour queries.
type Index interface {
	// Neighbors returns up to k documents by descending similarity, excluding
	// the document with id excludeID.
	Neighbors(text string, k int, excludeID string) []Neighbor
	Len() int
}

// DocText builds the text used for similarity from subject and body. The
// subject is repeated so that it weighs more than a single body sentence.
func DocText(e email.Email) string { return e.Subject + " " + e.Subject + " " + e.BodyText }

type posting struct {
	doc int
	w   float64
}

// TFIDF is an in-memory TF-IDF index with an inverted list.
type TFIDF struct {
	docs     []Doc
	idf      map[string]float64
	postings map[string][]posting
}

// Build creates an index. It is deterministic: docs are processed in ID order.
func Build(docs []Doc) *TFIDF {
	docs = append([]Doc(nil), docs...)
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	t := &TFIDF{docs: docs, idf: map[string]float64{}, postings: map[string][]posting{}}
	df := map[string]int{}
	tfs := make([]map[string]float64, len(docs))
	for i, d := range docs {
		counts := map[string]int{}
		for _, tok := range textutil.Tokens(d.Text) {
			counts[tok]++
		}
		tf := make(map[string]float64, len(counts))
		for tok, c := range counts {
			tf[tok] = 1 + math.Log(float64(c))
			df[tok]++
		}
		tfs[i] = tf
	}
	n := float64(len(docs))
	for tok, c := range df {
		t.idf[tok] = math.Log((1+n)/(1+float64(c))) + 1
	}
	for i, tf := range tfs {
		norm := 0.0
		terms := sortedKeys(tf)
		for _, tok := range terms {
			w := tf[tok] * t.idf[tok]
			norm += w * w
		}
		norm = math.Sqrt(norm)
		if norm == 0 {
			continue
		}
		for _, tok := range terms {
			t.postings[tok] = append(t.postings[tok], posting{i, tf[tok] * t.idf[tok] / norm})
		}
	}
	return t
}

// Len implements Index.
func (t *TFIDF) Len() int { return len(t.docs) }

// Neighbors implements Index.
func (t *TFIDF) Neighbors(text string, k int, excludeID string) []Neighbor {
	if t.Len() == 0 || k <= 0 {
		return nil
	}
	counts := map[string]int{}
	for _, tok := range textutil.Tokens(text) {
		if _, ok := t.idf[tok]; ok {
			counts[tok]++
		}
	}
	if len(counts) == 0 {
		return nil
	}
	q := map[string]float64{}
	norm := 0.0
	terms := sortedKeys2(counts)
	for _, tok := range terms {
		w := (1 + math.Log(float64(counts[tok]))) * t.idf[tok]
		q[tok] = w
		norm += w * w
	}
	norm = math.Sqrt(norm)
	sims := make([]float64, len(t.docs))
	for _, tok := range terms { // sorted: deterministic float accumulation
		for _, p := range t.postings[tok] {
			sims[p.doc] += q[tok] / norm * p.w
		}
	}
	var out []Neighbor
	for i, s := range sims {
		if s <= 0 || t.docs[i].ID == excludeID {
			continue
		}
		out = append(out, Neighbor{t.docs[i].ID, t.docs[i].Category, s, t.docs[i].Trust})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sim != out[j].Sim {
			return out[i].Sim > out[j].Sim
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys2(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Signal adapts an Index to classifier.Signal. Its evidence is capped at
// MaxWeight (< the auto-classify threshold): similarity supports the other
// signals, it never decides alone (§22).
type Signal struct {
	Load      func(ctx context.Context) (Index, error)
	K         int     // neighbours considered (default 10)
	MinSim    float64 // ignore weaker neighbours (default 0.25)
	MaxWeight float64 // cap on the emitted weight (default 0.75)
	Name      func(category string) string
}

// Evidence implements classifier.Signal.
func (s Signal) Evidence(ctx context.Context, e email.Email, _ *features.Set) ([]classifier.Evidence, error) {
	if s.Load == nil {
		return nil, nil
	}
	idx, err := s.Load(ctx)
	if err != nil || idx == nil || idx.Len() == 0 {
		return nil, err
	}
	k, minSim, maxW := s.K, s.MinSim, s.MaxWeight
	if k == 0 {
		k = 10
	}
	if minSim == 0 {
		minSim = 0.25
	}
	if maxW == 0 {
		maxW = 0.75
	}
	var ns []Neighbor
	for _, n := range idx.Neighbors(DocText(e), k, e.ID) {
		if n.Sim >= minSim {
			ns = append(ns, n)
		}
	}
	if len(ns) == 0 {
		return nil, nil
	}
	votes := map[string]float64{}
	sims := map[string]float64{}
	counts := map[string]int{}
	total := 0.0
	for _, n := range ns {
		v := n.Sim * n.Trust
		votes[n.Category] += v
		sims[n.Category] += n.Sim
		counts[n.Category]++
		total += v
	}
	cats := make([]string, 0, len(votes))
	for c := range votes {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	name := s.Name
	if name == nil {
		name = func(c string) string { return c }
	}
	var out []classifier.Evidence
	for _, c := range cats {
		share := votes[c] / total
		avg := sims[c] / float64(counts[c])
		coverage := math.Min(1, float64(counts[c])/3)
		w := maxW * share * avg * coverage
		if w < 0.02 {
			continue
		}
		out = append(out, classifier.Evidence{
			Category: c, Weight: w, Method: classifier.MethodSimilarity,
			Reason: fmt.Sprintf("%d de %d correos similares fueron clasificados como %s", counts[c], len(ns), name(c)),
		})
	}
	return out, nil
}
