package source

import (
	"context"
	"errors"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mboxData = "From 1234567890@xxx Mon Oct 05 10:00:00 +0000 2026\n" +
	"From: Ana <ana@x.example>\nSubject: first\nX-Gmail-Labels: Inbox,Category Promotions\nDate: Mon, 05 Oct 2026 10:00:00 +0000\n\nhello\n>From the old days\n\n" +
	"From 1234567891@xxx Mon Oct 05 11:00:00 +0000 2026\n" +
	"From: Bob <bob@y.example>\nSubject: second\n\nbody two\n" +
	"From 1234567892@xxx Mon Oct 05 12:00:00 +0000 2026\n" +
	"Subject: third, no from\n\nbody three\n"

func writeMbox(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "x.mbox")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMBoxSplitsMessagesAndUnquotesFrom(t *testing.T) {
	m, err := NewMBox(writeMbox(t, mboxData))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	e1, err := m.Next(ctx)
	if err != nil || e1.Subject != "first" || e1.From.Email != "ana@x.example" {
		t.Fatalf("%+v %v", e1, err)
	}
	if !strings.Contains(e1.BodyText, "From the old days") || strings.Contains(e1.BodyText, ">From") {
		t.Errorf("mboxrd unquoting failed: %q", e1.BodyText)
	}
	if e1.Header("X-Gmail-Labels") != "Inbox,Category Promotions" {
		t.Errorf("gmail labels must be kept: %v", e1.Headers)
	}
	e2, err := m.Next(ctx)
	if err != nil || e2.Subject != "second" || strings.Contains(e2.BodyText, "third") {
		t.Fatalf("%+v %v", e2, err)
	}
	e3, err := m.Next(ctx)
	if err != nil || e3.Subject != "third, no from" {
		t.Fatalf("%+v %v", e3, err)
	}
	if _, err := m.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestMBoxCountAndCancellation(t *testing.T) {
	m, _ := NewMBox(writeMbox(t, mboxData))
	defer m.Close()
	n := 0
	if err := Each(context.Background(), m, func(_ email.Email) error { n++; return nil }); err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	m2, _ := NewMBox(writeMbox(t, mboxData))
	defer m2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m2.Next(ctx); err == nil {
		t.Error("cancelled context must stop reading")
	}
}

func TestMBoxEmptyAndMissing(t *testing.T) {
	m, _ := NewMBox(writeMbox(t, ""))
	defer m.Close()
	if _, err := m.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Errorf("empty mbox: %v", err)
	}
	if _, err := NewMBox("/nonexistent/file.mbox"); err == nil {
		t.Error("missing file")
	}
}
