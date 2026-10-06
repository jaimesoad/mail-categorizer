package engine

import (
	"sort"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/samples"
)

// §38: classifying one email without an LLM must be interactive: p95 < 100 ms,
// measured on a database that already holds a few hundred labelled emails.
func TestClassificationLatencyP95(t *testing.T) {
	if testing.Short() {
		t.Skip("latency check skipped in -short mode")
	}
	e := testEngine(t)
	all := samples.Generate(7, 30)
	parse := func(s samples.Sample) email.Email {
		m, err := email.ParseEML(s.EML)
		if err != nil {
			t.Fatal(err)
		}
		m.ID = ""
		return m
	}
	// train on 2/3, measure on the rest
	var fresh []samples.Sample
	for i, s := range all {
		if i%3 == 0 {
			fresh = append(fresh, s)
			continue
		}
		m := parse(s)
		m.ID = "train-" + s.ID
		if _, err := e.Process(bg, m); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Move(bg, m.ID, s.Category); err != nil {
			t.Fatal(err)
		}
	}
	var durs []time.Duration
	for _, s := range fresh {
		m := parse(s)
		m.ID = "fresh-" + s.ID
		start := time.Now()
		if _, err := e.Process(bg, m); err != nil {
			t.Fatal(err)
		}
		durs = append(durs, time.Since(start))
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	p95 := durs[len(durs)*95/100]
	limit := 100 * time.Millisecond
	if raceEnabled {
		limit *= 8 // the race detector slows execution by an order of magnitude
	}
	t.Logf("n=%d p50=%v p95=%v max=%v", len(durs), durs[len(durs)/2], p95, durs[len(durs)-1])
	if p95 > limit {
		t.Fatalf("p95 %v exceeds %v", p95, limit)
	}
}
