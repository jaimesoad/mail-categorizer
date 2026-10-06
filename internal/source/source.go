// Package source defines where emails come from. Mail providers are inputs, not
// part of the engine (§1): IMAP, Gmail or Microsoft Graph adapters only have to
// implement Source. This iteration ships the .eml file/directory source.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

// Source yields normalized emails one at a time.
type Source interface {
	// Next returns the next email, or io.EOF when exhausted.
	Next(ctx context.Context) (email.Email, error)
	Close() error
}

// EML reads .eml files from a file path or, recursively, a directory
// (in lexical order, so runs are reproducible).
type EML struct {
	files []string
	i     int
}

// NewEML opens a file or directory.
func NewEML(path string) (*EML, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return &EML{files: []string{path}}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".eml") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return &EML{files: files}, nil
}

// Len is the number of files.
func (s *EML) Len() int { return len(s.files) }

// Next implements Source.
func (s *EML) Next(ctx context.Context) (email.Email, error) {
	if err := ctx.Err(); err != nil {
		return email.Email{}, err
	}
	if s.i >= len(s.files) {
		return email.Email{}, io.EOF
	}
	p := s.files[s.i]
	s.i++
	e, err := email.ParseEMLFile(p)
	if err != nil {
		return email.Email{}, fmt.Errorf("%s: %w", p, err)
	}
	return e, nil
}

// Close implements Source.
func (s *EML) Close() error { return nil }

// Each calls fn for every email of src until EOF or an error.
func Each(ctx context.Context, src Source, fn func(email.Email) error) error {
	for {
		e, err := src.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
}
