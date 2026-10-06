package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// maxLLMConfidence caps the confidence of LLM-sourced features: they are
// useful hints but never as trustworthy as a user answer or a header.
const maxLLMConfidence = 0.8

var intentName = regexp.MustCompile(`^[a-z][a-z_]{1,31}$`)

// IntentEnricher asks the model for the intent of an email when the
// deterministic detector is not already sure (§23: no LLM for what regexes can
// do). It implements engine.Enricher.
type IntentEnricher struct {
	P        Provider
	SendBody bool // false: only subject and sender domain, redacted
	// Skip the call when an intent is already known with at least this confidence.
	SkipAbove float64
}

// Enrich implements engine.Enricher.
func (x IntentEnricher) Enrich(ctx context.Context, e email.Email, set *features.Set) ([]features.Feature, error) {
	skip := x.SkipAbove
	if skip == 0 {
		skip = 0.85
	}
	if set.Confidence(features.ContentIntent) >= skip {
		return nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sender domain: %s\nSubject: %s\n", set.Str(features.SenderDomain), Redact(e.Subject))
	if x.SendBody {
		body := Redact(e.BodyText)
		if len([]rune(body)) > 1500 {
			body = string([]rune(body)[:1500])
		}
		fmt.Fprintf(&b, "Body:\n%s\n", body)
	}
	ans, err := x.P.Complete(ctx, Request{
		System: `You classify emails by intent. Answer ONLY with JSON: {"intents":[{"intent":"<snake_case>","confidence":<0..1>}]}. ` +
			`Prefer these intents: transaction, receipt, invoice, statement, promotion, newsletter, notification, security_alert, meeting, personal_message, shipping, password_reset, account_update, social_notification. Return at most 3.`,
		User: b.String(),
	})
	if err != nil {
		return nil, err
	}
	return parseIntents(ans)
}

func parseIntents(ans string) ([]features.Feature, error) {
	js, err := extractJSON(ans)
	if err != nil {
		return nil, err
	}
	var r struct {
		Intents []struct {
			Intent     string  `json:"intent"`
			Confidence float64 `json:"confidence"`
		} `json:"intents"`
	}
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		return nil, fmt.Errorf("llm: bad intents JSON: %w", err)
	}
	var out []features.Feature
	for i, it := range r.Intents {
		name := strings.ToLower(strings.TrimSpace(it.Intent))
		if i >= 3 || !intentName.MatchString(name) || it.Confidence <= 0 || it.Confidence > 1 {
			continue // validation: ignore malformed entries instead of trusting them
		}
		c := it.Confidence
		if c > maxLLMConfidence {
			c = maxLLMConfidence
		}
		out = append(out, features.Feature{Name: features.ContentIntent, Value: name, Confidence: c, Source: features.SourceLLM})
	}
	return out, nil
}
