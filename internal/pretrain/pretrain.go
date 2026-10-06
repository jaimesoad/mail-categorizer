// Package pretrain seeds the knowledge base from an existing mailbox. A local
// LLM "teacher" files each email; the labels are attributed to the LLM actor
// (half weight, never overriding a person) and are meant to be spot-checked
// with the audit flow. Runs are resumable: processing is idempotent per message.
package pretrain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/source"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Teacher decides the category of an email. "" means "no confident answer".
type Teacher interface {
	Categorize(ctx context.Context, e email.Email) (category string, confidence float64, err error)
}

// Options control a run.
type Options struct {
	Skip   int // ignore the first N messages of the source
	Limit  int // stop after N processed messages (0 = all)
	Stride int // take every k-th message (cheap sampling); default 1
	// MaxConsecutiveErrors aborts the run when the model keeps failing
	// (Ollama down, wrong model name...). Default 10.
	MaxConsecutiveErrors int
	// Progress is called every ProgressEvery processed emails (default 25).
	Progress      func(Stats)
	ProgressEvery int
}

// Stats summarizes a run.
type Stats struct {
	Seen        int            `json:"seen"`
	Duplicates  int            `json:"duplicates"` // already processed in a previous run
	ParseErrors int            `json:"parse_errors"`
	LLMErrors   int            `json:"llm_errors"`
	Labeled     int            `json:"labeled"`
	Unlabeled   int            `json:"unlabeled"` // the teacher was not confident
	Agreed      int            `json:"agreed"`    // the system's own prediction matched the teacher
	ByCategory  map[string]int `json:"by_category"`
}

// AgreementRate is how often the system, before seeing the teacher's label,
// already predicted it: a running indicator of how much it has learned.
func (s Stats) AgreementRate() float64 {
	if s.Labeled == 0 {
		return 0
	}
	return float64(s.Agreed) / float64(s.Labeled)
}

// Run labels the emails of src with the teacher.
func Run(ctx context.Context, eng *engine.Engine, t Teacher, src source.Source, opts Options) (Stats, error) {
	if opts.Stride < 1 {
		opts.Stride = 1
	}
	if opts.MaxConsecutiveErrors == 0 {
		opts.MaxConsecutiveErrors = 10
	}
	if opts.ProgressEvery == 0 {
		opts.ProgressEvery = 25
	}
	st := Stats{ByCategory: map[string]int{}}
	llmCtx := engine.WithActor(ctx, engine.ActorLLM)
	consecutive, position, processed := 0, 0, 0

	for {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if opts.Limit > 0 && processed >= opts.Limit {
			return st, nil
		}
		em, err := src.Next(ctx)
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		var pe *source.ParseError
		if errors.As(err, &pe) {
			st.ParseErrors++
			continue
		}
		if err != nil {
			return st, err
		}
		position++
		if position <= opts.Skip || (position-1-opts.Skip)%opts.Stride != 0 {
			continue
		}
		processed++
		st.Seen++

		out, err := eng.Process(ctx, em)
		if err != nil {
			return st, fmt.Errorf("process message %d: %w", position, err)
		}
		if out.AlreadyProcessed {
			st.Duplicates++
			continue
		}
		cat, _, err := t.Categorize(ctx, em)
		if err != nil {
			st.LLMErrors++
			consecutive++
			if consecutive >= opts.MaxConsecutiveErrors {
				return st, fmt.Errorf("abortado tras %d fallos seguidos del LLM (último: %w)", consecutive, err)
			}
			continue
		}
		consecutive = 0
		if cat == "" {
			st.Unlabeled++
		} else {
			if out.Category == cat {
				st.Agreed++
			}
			if _, err := eng.Move(llmCtx, out.EmailID, cat); err != nil {
				if ctx.Err() != nil {
					return st, ctx.Err() // interrupted (Ctrl-C): a clean stop, resumable
				}
				return st, fmt.Errorf("label message %d: %w", position, err)
			}
			st.Labeled++
			st.ByCategory[cat]++
		}
		if opts.Progress != nil && st.Seen%opts.ProgressEvery == 0 {
			opts.Progress(st)
		}
	}
}

// AuditSample picks up to n teacher-labelled emails that no person has checked
// yet, spread across categories (round robin, rarest categories first) so that
// small categories are verified too. Deterministic.
func AuditSample(labels map[string]storage.Label, n int) []string {
	byCat := map[string][]string{}
	for id, l := range labels {
		if l.Source == "llm" {
			byCat[l.Category] = append(byCat[l.Category], id)
		}
	}
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		sort.Strings(byCat[c])
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool {
		if len(byCat[cats[i]]) != len(byCat[cats[j]]) {
			return len(byCat[cats[i]]) < len(byCat[cats[j]])
		}
		return cats[i] < cats[j]
	})
	var out []string
	for round := 0; len(out) < n; round++ {
		added := false
		for _, c := range cats {
			if round < len(byCat[c]) && len(out) < n {
				// spread picks through the category instead of taking the first ones
				ids := byCat[c]
				out = append(out, ids[(round*7919)%len(ids)])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return dedupe(out)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// RunDB labels the emails already stored (e.g. imported through the API by n8n)
// that no one has labelled yet. Resumable: labelled emails leave the queue.
func RunDB(ctx context.Context, eng *engine.Engine, t Teacher, opts Options) (Stats, error) {
	if opts.MaxConsecutiveErrors == 0 {
		opts.MaxConsecutiveErrors = 10
	}
	if opts.ProgressEvery == 0 {
		opts.ProgressEvery = 25
	}
	st := Stats{ByCategory: map[string]int{}}
	ids, err := eng.DB().UnlabeledEmailIDs(ctx, opts.Limit)
	if err != nil {
		return st, err
	}
	llmCtx := engine.WithActor(ctx, engine.ActorLLM)
	consecutive := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		em, err := eng.DB().GetEmail(ctx, id)
		if err != nil {
			continue
		}
		st.Seen++
		before, _ := eng.Get(ctx, id)
		cat, _, err := t.Categorize(ctx, em)
		if err != nil {
			st.LLMErrors++
			consecutive++
			if consecutive >= opts.MaxConsecutiveErrors {
				return st, fmt.Errorf("abortado tras %d fallos seguidos del LLM (último: %w)", consecutive, err)
			}
			continue
		}
		consecutive = 0
		if cat == "" {
			st.Unlabeled++
			continue
		}
		if before != nil && before.Category == cat {
			st.Agreed++
		}
		if _, err := eng.Move(llmCtx, id, cat); err != nil {
			if ctx.Err() != nil {
				return st, ctx.Err()
			}
			return st, fmt.Errorf("label %s: %w", id, err)
		}
		st.Labeled++
		st.ByCategory[cat]++
		if opts.Progress != nil && st.Seen%opts.ProgressEvery == 0 {
			opts.Progress(st)
		}
	}
	return st, nil
}
