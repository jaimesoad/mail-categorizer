package source

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

func write(t *testing.T, path, subject string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	raw := "From: a@b.example\r\nSubject: " + subject + "\r\nDate: Mon, 05 Oct 2026 10:00:00 +0000\r\n\r\nbody " + subject + "\r\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEMLDirectoryIsRecursiveSortedAndFiltered(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "b.eml"), "second")
	write(t, filepath.Join(dir, "sub", "a.eml"), "third")
	write(t, filepath.Join(dir, "a.EML"), "first")
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore"), 0o644)
	src, err := NewEML(dir)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	err = Each(context.Background(), src, func(e email.Email) error { subjects = append(subjects, e.Subject); return nil })
	if err != nil || len(subjects) != 3 || subjects[0] != "first" || subjects[1] != "second" || subjects[2] != "third" {
		t.Fatalf("%v %v", subjects, err)
	}
	if _, err := src.Next(context.Background()); err != io.EOF {
		t.Fatal("expected EOF")
	}
}

func TestSingleFileAndErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.eml")
	write(t, p, "only")
	src, err := NewEML(p)
	if err != nil || src.Len() != 1 {
		t.Fatalf("%v %v", src, err)
	}
	if _, err := NewEML(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing path")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.Next(ctx); err == nil {
		t.Error("a cancelled context must stop reading")
	}
}
