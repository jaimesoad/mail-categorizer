package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
)

type fake struct {
	answer string
	got    Request
}

func (f *fake) Complete(_ context.Context, r Request) (string, error) {
	f.got = r
	return f.answer, nil
}

func TestFromConfigEnforcesPrivacy(t *testing.T) {
	base := config.LLM{Enabled: true, Model: "m", Timeout: time.Second}
	cases := map[string]struct {
		url   string
		allow bool
		ok    bool
	}{
		"ollama local":     {"http://localhost:11434/v1", false, true},
		"loopback ip":      {"http://127.0.0.1:8080/v1", false, true},
		"external blocked": {"https://api.example.com/v1", false, false},
		"external allowed": {"https://api.example.com/v1", true, true},
		"garbage":          {"not a url", false, false},
	}
	for name, c := range cases {
		cfg := base
		cfg.BaseURL, cfg.AllowExternal = c.url, c.allow
		_, err := FromConfig(cfg)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v want ok=%v", name, err, c.ok)
		}
	}
	if _, err := FromConfig(config.LLM{}); err == nil {
		t.Error("disabled llm must not build")
	}
}

func TestOpenAICompatRoundTrip(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"ok":true}`}}}})
	}))
	defer srv.Close()
	p := &OpenAICompat{BaseURL: srv.URL, Model: "m", APIKey: "k", Client: srv.Client()}
	out, err := p.Complete(context.Background(), Request{System: "s", User: "u"})
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if gotAuth != "Bearer k" || gotBody["model"] != "m" || gotBody["temperature"] != float64(0) {
		t.Errorf("request: auth=%q body=%v", gotAuth, gotBody)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) }))
	defer bad.Close()
	if _, err := (&OpenAICompat{BaseURL: bad.URL, Model: "m", Client: bad.Client()}).Complete(context.Background(), Request{}); err == nil {
		t.Error("HTTP 500 must surface as an error")
	}
}

func TestRedact(t *testing.T) {
	got := Redact("Call +52 55 1234 5678 or mail jaime@example.com, see https://x.example/a?token=abc card 4111 1111 1111 1111")
	for _, leak := range []string{"jaime@", "5678", "token=abc", "4111"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in %q", leak, got)
		}
	}
}

func TestIntentEnricherSkipsWhenSureAndValidates(t *testing.T) {
	f := &fake{answer: "```json\n{\"intents\":[{\"intent\":\"Transaction\",\"confidence\":0.99},{\"intent\":\"DROP TABLE\",\"confidence\":0.9},{\"intent\":\"receipt\",\"confidence\":7}]}\n```"}
	x := IntentEnricher{P: f}
	sure := features.NewSet(features.Feature{Name: features.ContentIntent, Value: "receipt", Confidence: 0.95})
	if got, err := x.Enrich(context.Background(), email.Email{}, sure); err != nil || got != nil || f.got.User != "" {
		t.Fatalf("must not call the model when the deterministic detector is sure: %v %v", got, err)
	}
	set := features.NewSet(features.Feature{Name: features.SenderDomain, Value: "bank.example", Confidence: 1})
	got, err := x.Enrich(context.Background(), email.Email{Subject: "Hello jaime@example.com", BodyText: "SECRET BODY"}, set)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "transaction" || got[0].Confidence != maxLLMConfidence || got[0].Source != features.SourceLLM {
		t.Fatalf("validation/capping failed: %+v", got)
	}
	if strings.Contains(f.got.User, "SECRET BODY") || strings.Contains(f.got.User, "jaime@") {
		t.Errorf("body or address leaked to the model: %q", f.got.User)
	}
	x.SendBody = true
	x.Enrich(context.Background(), email.Email{BodyText: "visible body"}, set)
	if !strings.Contains(f.got.User, "visible body") {
		t.Error("send_body=true should include the (redacted) body")
	}
}

func tree(t *testing.T) *categories.Tree {
	tr, err := categories.NewTree([]categories.Category{
		{ID: "finance.bank", Name: "Bank", Enabled: true}, {ID: "finance.purchase", Name: "Purchase", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestQuestionGeneratorBuildsValidatedQuestion(t *testing.T) {
	f := &fake{answer: `{"question":"¿El correo informa sobre dinero que salió de tu cuenta?","answers":["yes","no"],"feature":"transaction.direction",
	 "mapping":{"yes":"outgoing","no":"other"},"favors":{"yes":["finance.bank"],"no":["finance.purchase","hacked"]}}`}
	g := QuestionGenerator{P: f, Tree: tree(t)}
	set := features.NewSet(
		features.Feature{Name: "content.contains_amount", Value: "true", Confidence: 1},
		features.Feature{Name: features.SenderEmail, Value: "secret@person.example", Confidence: 1},
	)
	q, err := g.Propose(context.Background(), questions.ProposeInput{
		Candidates: []classifier.Candidate{{Category: "finance.bank", Score: 0.45}, {Category: "finance.purchase", Score: 0.4}, {Category: "unknown", Score: 0.15}},
		Features:   set,
	})
	if err != nil || q == nil {
		t.Fatalf("q=%v err=%v", q, err)
	}
	if q.Source != questions.SourceLLM || q.Validated || q.Feature != "transaction.direction" || q.FeatureValue("yes") != "outgoing" {
		t.Errorf("bad question: %+v", q)
	}
	if q.Affinity["finance.bank"]["yes"] <= q.Affinity["finance.bank"]["no"] || q.Affinity["finance.purchase"]["no"] <= q.Affinity["finance.purchase"]["yes"] {
		t.Errorf("affinity should follow 'favors': %+v", q.Affinity)
	}
	if _, bogus := q.Affinity["hacked"]; bogus {
		t.Error("categories the model invented must be dropped")
	}
	if strings.Contains(f.got.User, "secret@person.example") {
		t.Error("sender address leaked into the prompt")
	}
	if !strings.Contains(f.got.User, "content.contains_amount") {
		t.Error("safe abstract features should be described")
	}
}

func TestQuestionGeneratorRejectsBadOutput(t *testing.T) {
	for name, ans := range map[string]string{
		"not json":     "sorry I cannot",
		"no favors":    `{"question":"¿Algo?","answers":["yes","no"],"feature":"a.b","favors":{}}`,
		"three way":    `{"question":"¿Algo?","answers":["a","b","c"],"feature":"a.b","favors":{"a":["finance.bank"]}}`,
		"bad feature":  `{"question":"¿Algo?","answers":["yes","no"],"feature":"Bad Name","favors":{"yes":["finance.bank"]}}`,
		"empty q text": `{"question":" ","answers":["yes","no"],"feature":"a.b","favors":{"yes":["finance.bank"]}}`,
	} {
		g := QuestionGenerator{P: &fake{answer: ans}, Tree: tree(t)}
		q, err := g.Propose(context.Background(), questions.ProposeInput{
			Candidates: []classifier.Candidate{{Category: "finance.bank", Score: 0.5}, {Category: "finance.purchase", Score: 0.4}}, Features: features.NewSet(),
		})
		if err == nil && q != nil {
			t.Errorf("%s: accepted %+v", name, q)
		}
	}
}
