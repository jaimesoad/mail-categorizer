package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/rules"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

func newServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	cfg, _ := config.LoadOrDefault("")
	cfg.Rules = append(cfg.Rules,
		rules.Rule{ID: "my-bank", Category: "finance.bank", Weight: 0.7, Reason: "Dominio bancario conocido.",
			When: rules.Condition{Feature: "sender.domain", Op: "in", Value: []any{"mybank.example"}}},
		rules.Rule{ID: "my-bank-tx", Category: "finance.bank", Weight: 0.6, Reason: "Transacción de un banco conocido.",
			When: rules.Condition{All: []rules.Condition{
				{Feature: "sender.domain", Value: "mybank.example"}, {Feature: "content.contains_transaction", Value: "true"}}}},
	)
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	db, _ := storage.Open(":memory:")
	t.Cleanup(func() { db.Close() })
	eng, err := engine.New(context.Background(), db, built)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(eng, token, nil).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSpecClassifyExampleAndFullAskAnswerFeedbackFlow(t *testing.T) {
	srv := newServer(t, "")

	// §27 example payload
	code, out := call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{
		"id": "email-123", "sender": "alerts@mybank.example", "subject": "Transaction completed",
		"body": "A transaction of $85.40 was completed on your account.",
	})
	if code != 201 {
		t.Fatalf("status %d: %v", code, out)
	}
	cl := out["classification"].(map[string]any)
	if cl["category"] != "finance.bank" || out["needs_question"] != false || len(out["explanation"].([]any)) == 0 {
		t.Fatalf("unexpected: %v", out)
	}

	// idempotent re-post => 200 and already_processed
	code, out = call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{
		"id": "email-123", "sender": "alerts@mybank.example", "subject": "Transaction completed",
		"body": "A transaction of $85.40 was completed on your account.",
	})
	if code != 200 || out["already_processed"] != true {
		t.Fatalf("repost: %d %v", code, out)
	}

	// ambiguous email -> question -> answer -> feedback
	code, out = call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{
		"id": "email-9", "sender": "store@store.example", "subject": "Payment received", "body": "Payment received for your order #884211.",
	})
	if code != 201 || out["needs_question"] != true {
		t.Fatalf("expected a question: %d %v", code, out)
	}
	code, q := call(t, srv, "POST", "/v1/emails/email-9/question", "", nil)
	qv := q["question"].(map[string]any)
	if code != 200 || qv["id"] == nil || len(qv["answers"].([]any)) != 2 {
		t.Fatalf("question: %d %v", code, q)
	}
	code, ans := call(t, srv, "POST", "/v1/emails/email-9/answer", "", map[string]any{"question_id": qv["id"], "answer": "yes"})
	if code != 200 || ans["learned"] == nil || ans["classification"] == nil {
		t.Fatalf("answer: %d %v", code, ans)
	}
	code, fb := call(t, srv, "POST", "/v1/emails/email-9/feedback", "", map[string]any{"correct": false, "category": "finance.purchase"})
	if code != 200 || fb["classification"].(map[string]any)["category"] != "finance.purchase" || fb["learned"] != true {
		t.Fatalf("feedback: %d %v", code, fb)
	}
	code, ex := call(t, srv, "GET", "/v1/emails/email-9/explain", "", nil)
	if code != 200 || len(ex["text"].([]any)) == 0 {
		t.Fatalf("explain: %d %v", code, ex)
	}
	code, m := call(t, srv, "GET", "/v1/metrics", "", nil)
	if code != 200 || m["emails"].(float64) != 2 {
		t.Fatalf("metrics: %d %v", code, m)
	}
	code, cats := call(t, srv, "GET", "/v1/categories", "", nil)
	if code != 200 || len(cats["categories"].([]any)) == 0 {
		t.Fatalf("categories: %d %v", code, cats)
	}
}

func TestErrorMapping(t *testing.T) {
	srv := newServer(t, "")
	cases := []struct {
		name         string
		method, path string
		body         any
		want         int
	}{
		{"unknown email", "GET", "/v1/emails/nope", nil, 404},
		{"answer unknown email", "POST", "/v1/emails/nope/answer", map[string]any{"question_id": "q", "answer": "yes"}, 404},
		{"empty email", "POST", "/v1/emails/classify", map[string]any{}, 400},
		{"missing answer", "POST", "/v1/emails/x/answer", map[string]any{"question_id": "q"}, 400},
		{"feedback without correct", "POST", "/v1/emails/x/feedback", map[string]any{"category": "work"}, 400},
		{"delete unknown", "DELETE", "/v1/emails/nope", nil, 404},
		{"bad raw eml", "POST", "/v1/emails/classify", map[string]any{"raw_eml": ""}, 400},
	}
	for _, c := range cases {
		if code, out := call(t, srv, c.method, c.path, "", c.body); code != c.want {
			t.Errorf("%s: got %d want %d (%v)", c.name, code, c.want, out)
		}
	}
	// malformed JSON
	resp, _ := http.Post(srv.URL+"/v1/emails/classify", "application/json", strings.NewReader("{oops"))
	if resp.StatusCode != 400 {
		t.Errorf("malformed JSON: %d", resp.StatusCode)
	}
	// conflicting id
	call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{"id": "dup", "sender": "a@b.example", "subject": "one", "body": "first"})
	if code, _ := call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{"id": "dup", "sender": "a@b.example", "subject": "two", "body": "second"}); code != 409 {
		t.Errorf("id conflict: %d", code)
	}
}

func TestAuth(t *testing.T) {
	srv := newServer(t, "s3cret")
	if code, _ := call(t, srv, "GET", "/v1/categories", "", nil); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/categories", "wrong", nil); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/categories", "s3cret", nil); code != 200 {
		t.Errorf("good token: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/healthz", "", nil); code != 200 {
		t.Errorf("healthz must stay open: %d", code)
	}
}

func TestRawEMLAndPurge(t *testing.T) {
	srv := newServer(t, "")
	raw := "From: News <news@letters.example>\r\nTo: me@x.example\r\nSubject: Weekly digest\r\nMessage-ID: <abc@letters.example>\r\nList-Unsubscribe: <mailto:u@letters.example>\r\n\r\nNewsletter: top stories. View in browser.\r\n"
	code, out := call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{"id": "raw1", "raw_eml": raw})
	if code != 201 || out["email_id"] != "raw1" {
		t.Fatalf("raw: %d %v", code, out)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/emails/raw1?content_only=true", "", nil); code != 204 {
		t.Errorf("purge content: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/emails/raw1", "", nil); code != 200 {
		t.Errorf("classification must survive a content purge: %d", code)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/emails/raw1", "", nil); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/v1/emails/raw1", "", nil); code != 404 {
		t.Errorf("deleted email still there: %d", code)
	}
}

func TestQuietImportSyncQueueAndAck(t *testing.T) {
	srv := newServer(t, "")
	// a normal email that resolves automatically => appears in the sync queue
	call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{
		"id": "gmail-A1", "sender": "alerts@mybank.example", "subject": "Transaction completed", "body": "A transaction of $85.40 was completed on your account."})
	// a historical email imported quietly: never synced, never prompts
	call(t, srv, "POST", "/v1/emails/classify?quiet=true", "", map[string]any{
		"id": "gmail-OLD", "sender": "alerts@mybank.example", "subject": "Transaction completed long ago", "body": "A transaction of $10.00 was completed on your account."})

	code, out := call(t, srv, "GET", "/v1/sync/pending", "", nil)
	items, _ := out["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("only the non-quiet resolved email must be pending: %d %v", code, out)
	}
	it := items[0].(map[string]any)
	if it["email_id"] != "gmail-A1" || it["label"] != "mailcat/Finanzas/Banco" || it["confident"] != true {
		t.Fatalf("%v", it)
	}
	if _, has := it["previous_label"]; has {
		t.Error("nothing was pushed yet")
	}
	if code, _ := call(t, srv, "POST", "/v1/sync/ack", "", map[string]any{"email_id": "gmail-A1", "category": "finance.bank"}); code != 200 {
		t.Fatalf("ack: %d", code)
	}
	if _, out = call(t, srv, "GET", "/v1/sync/pending", "", nil); len(out["items"].([]any)) != 0 {
		t.Fatal("an acked email must leave the queue")
	}
	// the user corrects it => it comes back with the previous label to remove
	call(t, srv, "POST", "/v1/emails/gmail-A1/feedback", "", map[string]any{"correct": false, "category": "finance.purchase"})
	_, out = call(t, srv, "GET", "/v1/sync/pending", "", nil)
	items = out["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("a correction must be re-synced: %v", out)
	}
	it = items[0].(map[string]any)
	if it["label"] != "mailcat/Finanzas/Compras" || it["previous_label"] != "mailcat/Finanzas/Banco" {
		t.Fatalf("%v", it)
	}
	if code, _ := call(t, srv, "POST", "/v1/sync/ack", "", map[string]any{"email_id": "nope", "category": "x"}); code != 404 {
		t.Errorf("ack unknown: %d", code)
	}
	if code, _ := call(t, srv, "POST", "/v1/sync/ack", "", map[string]any{"email_id": "gmail-A1"}); code != 400 {
		t.Errorf("ack missing category: %d", code)
	}
}

// The exact payload the n8n "Preparar para mailcat" node produces must be understood.
func TestN8nPayloadWithGmailLabels(t *testing.T) {
	srv := newServer(t, "")
	code, out := call(t, srv, "POST", "/v1/emails/classify", "", map[string]any{
		"id": "gmail-18f0abc", "sender": "Shop <deals@shop.example>", "subject": "Hello", "body": "see you",
		"message_id": "m1@shop.example", "in_reply_to": "", "received_at": "2026-10-05T10:00:00.000Z",
		"headers": map[string]string{"list-unsubscribe": "<mailto:x@y>", "x-gmail-labels": "inbox,category promotions,unread"},
	})
	if code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	reasons := strings.Join(toStrings(out["explanation"]), "|")
	if !strings.Contains(reasons, "Gmail lo marcó como Promociones") {
		t.Errorf("Gmail's own category label should be used as evidence: %v", out["explanation"])
	}
	// the category tree exposes the labels n8n must create
	_, cats := call(t, srv, "GET", "/v1/categories", "", nil)
	found := false
	for _, l := range cats["labels"].([]any) {
		if l.(map[string]any)["label"] == "mailcat/Finanzas/Banco" {
			found = true
		}
	}
	if !found {
		t.Errorf("labels missing: %v", cats["labels"])
	}
}

func toStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}
