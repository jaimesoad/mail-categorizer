package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/samples"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// fakeOllama answers /chat/completions like a model that knows each sender's category.
func fakeOllama(t *testing.T, bySender map[string]string) *httptest.Server {
	re := regexp.MustCompile(`From: .*? <([^>]+)>`)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		user := req.Messages[len(req.Messages)-1].Content
		cat := "unknown"
		if m := re.FindStringSubmatch(user); m != nil {
			if c, ok := bySender[m[1]]; ok {
				cat = c
			}
		}
		out, _ := json.Marshal(map[string]any{"category": cat, "confidence": 0.9})
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(out)}}}})
	}))
}

func TestCLIPretrainAuditExport(t *testing.T) {
	dir := t.TempDir()
	sample := filepath.Join(dir, "sample")
	if _, err := runCLI(t, "", "sample-data", "-out", sample, "-per", "8"); err != nil {
		t.Fatal(err)
	}
	// the "model": majority category per sender address
	votes := map[string]map[string]int{}
	for _, s := range samples.Generate(42, 8) {
		m, _ := email.ParseEML(s.EML)
		if votes[m.From.Email] == nil {
			votes[m.From.Email] = map[string]int{}
		}
		votes[m.From.Email][s.Category]++
	}
	bySender := map[string]string{}
	for snd, v := range votes {
		best, n := "", 0
		for c, k := range v {
			if k > n || (k == n && c < best) {
				best, n = c, k
			}
		}
		bySender[snd] = best
	}
	srv := fakeOllama(t, bySender)
	defer srv.Close()

	db := filepath.Join(dir, "p.db")
	out, err := runCLI(t, "", "-db", db, "pretrain", "-eml", sample, "-base-url", srv.URL, "-model", "fake")
	if err != nil || !strings.Contains(out, "Resumen:") || !strings.Contains(out, "etiquetados") {
		t.Fatalf("pretrain: %v\n%s", err, out)
	}
	// resumable: a second run sees everything as already processed
	out, err = runCLI(t, "", "-db", db, "pretrain", "-eml", sample, "-base-url", srv.URL, "-model", "fake")
	if err != nil || !strings.Contains(out, "0 etiquetados") || !strings.Contains(out, "ya vistos") {
		t.Fatalf("resume: %v\n%s", err, out)
	}
	if out, _ := runCLI(t, "", "-db", db, "metrics"); !strings.Contains(out, "Etiquetados por el LLM local") {
		t.Errorf("metrics must report teacher labels:\n%s", out)
	}
	// audit: confirm the first, mark the second as wrong (category 1), quit
	out, err = runCLI(t, "\n1\nq\n", "-db", db, "audit", "-n", "5")
	if err != nil || !strings.Contains(out, "LLM →") || !strings.Contains(out, "El LLM acertó") {
		t.Fatalf("audit: %v\n%s", err, out)
	}
	// export strips the content but keeps the knowledge
	exp := filepath.Join(dir, "knowledge.db")
	if out, err := runCLI(t, "", "-db", db, "export", "-out", exp); err != nil || !strings.Contains(out, "no salen de tu máquina") {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "", "-db", db, "export", "-out", exp); err == nil {
		t.Error("export must not overwrite")
	}
	cp, err := storage.Open(exp)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var bodies int
	cp.QueryRow(`SELECT COUNT(*) FROM emails WHERE body_text!='' OR subject!=''`).Scan(&bodies)
	var rules int
	cp.QueryRow(`SELECT COUNT(*) FROM learned_rules`).Scan(&rules)
	if bodies != 0 || rules == 0 {
		t.Fatalf("export must erase text (%d left) and keep rules (%d)", bodies, rules)
	}
	// the original is untouched
	orig, _ := storage.Open(db)
	defer orig.Close()
	orig.QueryRow(`SELECT COUNT(*) FROM emails WHERE body_text!=''`).Scan(&bodies)
	if bodies == 0 {
		t.Error("export must not modify the source database")
	}
	_ = os.Remove
}

func TestCLIPretrainRequiresOneSourceAndReportsLLMDown(t *testing.T) {
	dir := t.TempDir()
	if _, err := runCLI(t, "", "-db", filepath.Join(dir, "x.db"), "pretrain"); err == nil {
		t.Error("needs -mbox or -eml")
	}
	sample := filepath.Join(dir, "sample")
	runCLI(t, "", "sample-data", "-out", sample, "-per", "3")
	_, err := runCLI(t, "", "-db", filepath.Join(dir, "y.db"), "pretrain", "-eml", sample, "-base-url", "http://127.0.0.1:1/v1")
	if err == nil || !strings.Contains(err.Error(), "LLM") {
		t.Errorf("an unreachable Ollama must abort with a clear error, got %v", err)
	}
	if _, err := runCLI(t, "", "-db", filepath.Join(dir, "z.db"), "pretrain", "-eml", sample, "-base-url", "https://api.example.com/v1"); err == nil {
		t.Error("external endpoints need llm.allow_external")
	}
}
