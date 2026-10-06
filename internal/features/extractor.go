package features

import (
	"context"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

// Extractor produces features from an email. Implementations must be
// deterministic for a given input (and, for history-based ones, knowledge base).
type Extractor interface {
	Extract(ctx context.Context, e email.Email) ([]Feature, error)
}

// ExtractorFunc adapts a function to Extractor.
type ExtractorFunc func(ctx context.Context, e email.Email) ([]Feature, error)

func (f ExtractorFunc) Extract(ctx context.Context, e email.Email) ([]Feature, error) {
	return f(ctx, e)
}

// Layered runs several extractors in order and merges their output.
type Layered struct{ Layers []Extractor }

// Extract implements Extractor.
func (l Layered) Extract(ctx context.Context, e email.Email) ([]Feature, error) {
	set := NewSet()
	for _, layer := range l.Layers {
		fs, err := layer.Extract(ctx, e)
		if err != nil {
			return nil, err
		}
		set.Add(fs...)
	}
	return set.All(), nil
}

// History gives extractors access to previously learned knowledge. A nil
// History disables history-derived features, which keeps extraction testable.
type History interface {
	SenderHistory(ctx context.Context, senderEmail, senderDomain string) (SenderHistory, error)
	// ThreadInfo looks up the first known message among ids (In-Reply-To,
	// References) and returns its final category and sender.
	ThreadInfo(ctx context.Context, messageIDs []string) (ThreadInfo, error)
}

// SenderHistory summarizes what is known about a sender.
type SenderHistory struct {
	EmailCount       int
	DomainCount      int
	EmailCategories  []string // most frequent first
	DomainCategories []string
}

// ThreadInfo describes the parent message of a reply.
type ThreadInfo struct {
	Found    bool
	Category string
	Sender   string
}

// Default returns the standard extractor stack (§7): headers, sender, content,
// intent, attachments and optional history/thread layers.
func Default(h History) Extractor {
	layers := []Extractor{
		HeaderExtractor{},
		SenderExtractor{},
		AttachmentExtractor{},
		ContentExtractor{},
		NewIntentExtractor(),
	}
	if h != nil {
		layers = append(layers, HistoryExtractor{H: h})
	}
	return Layered{Layers: layers}
}
