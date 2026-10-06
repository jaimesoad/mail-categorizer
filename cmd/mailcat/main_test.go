package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errw bytes.Buffer
	err := run(args, strings.NewReader(stdin), &out, &errw)
	return out.String() + errw.String(), err
}

func TestCLIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "t.db")
	sample := filepath.Join(dir, "sample")

	if out, err := runCLI(t, "", "sample-data", "-out", sample, "-per", "6"); err != nil || !strings.Contains(out, "correos escritos") {
		t.Fatalf("sample-data: %v %s", err, out)
	}
	out, err := runCLI(t, "", "-db", db, "classify", filepath.Join(sample, "finance.bank"))
	if err != nil || strings.Count(out, "\n") < 6 {
		t.Fatalf("classify: %v\n%s", err, out)
	}
	// JSON mode: one object per line
	out, err = runCLI(t, "", "-db", db, "classify", "-json", filepath.Join(sample, "newsletters", "newsletters-000.eml"))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, `"decision"`) {
		t.Fatalf("classify -json: %v\n%s", err, out)
	}
	var first struct {
		EmailID string `json:"email_id"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &first); err != nil || first.EmailID == "" {
		t.Fatalf("json output: %v", err)
	}
	if _, err := runCLI(t, "", "-db", db, "classify", filepath.Join(sample, "missing")); err == nil {
		t.Error("missing path must fail")
	}

	// interactive session: answer yes, confirm
	out, err = runCLI(t, "s\ns\n", "-db", filepath.Join(dir, "i.db"), "classify", "-i", filepath.Join(sample, "finance.purchase", "finance_purchase-000.eml"))
	if err != nil || !strings.Contains(out, "?") || !strings.Contains(out, "Razones") {
		t.Fatalf("interactive: %v\n%s", err, out)
	}

	for _, args := range [][]string{
		{"categories"}, {"questions"}, {"rules"}, {"metrics"}, {"metrics", "-json"},
	} {
		if out, err := runCLI(t, "", append([]string{"-db", db}, args...)...); err != nil || out == "" {
			t.Errorf("%v: %v %q", args, err, out)
		}
	}
	// move + explain + purge + backup
	id := first.EmailID
	if out, err := runCLI(t, "", "-db", db, "move", id, "finance.bank"); err != nil || !strings.Contains(out, "Banco") {
		t.Fatalf("move: %v %s", err, out)
	}
	if out, err := runCLI(t, "", "-db", db, "explain", id); err != nil || !strings.Contains(out, "Razones") {
		t.Fatalf("explain: %v %s", err, out)
	}
	if out, err := runCLI(t, "", "-db", db, "rules"); err != nil || strings.Contains(out, "Aún no") {
		t.Fatalf("rules after move: %v %s", err, out)
	}
	if _, err := runCLI(t, "", "-db", db, "move", id, "nonexistent"); err == nil {
		t.Error("moving to an unknown category must fail")
	}
	if out, err := runCLI(t, "", "-db", db, "purge", "-all"); err != nil || !strings.Contains(out, "purgado") {
		t.Fatalf("purge: %v %s", err, out)
	}
	bk := filepath.Join(dir, "bk.db")
	if _, err := runCLI(t, "", "-db", db, "backup", bk); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(bk); err != nil {
		t.Error("backup file missing")
	}
	if _, err := runCLI(t, "", "-db", db, "backup", bk); err == nil {
		t.Error("backup must not overwrite")
	}
}

func TestCLIInitAndUsage(t *testing.T) {
	dir := t.TempDir()
	if out, err := runCLI(t, "", "init", "-dir", dir); err != nil || !strings.Contains(out, "mailcat.yaml") {
		t.Fatalf("init: %v %s", err, out)
	}
	if _, err := runCLI(t, "", "init", "-dir", dir); err == nil {
		t.Error("init must not overwrite without -force")
	}
	if _, err := runCLI(t, "", "init", "-dir", dir, "-force"); err != nil {
		t.Error("init -force")
	}
	// the generated config is usable
	cfg := filepath.Join(dir, "mailcat.yaml")
	if out, err := runCLI(t, "", "-config", cfg, "-db", filepath.Join(dir, "x.db"), "categories"); err != nil || !strings.Contains(out, "Banco") {
		t.Fatalf("categories with generated config: %v %s", err, out)
	}
	if _, err := runCLI(t, ""); err == nil {
		t.Error("no command must fail")
	}
	if _, err := runCLI(t, "", "bogus"); err == nil {
		t.Error("unknown command must fail")
	}
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("thresholds: {auto: 0.2, ask: 0.9}\n"), 0o644)
	if _, err := runCLI(t, "", "-config", bad, "categories"); err == nil {
		t.Error("invalid config must fail at startup")
	}
}
