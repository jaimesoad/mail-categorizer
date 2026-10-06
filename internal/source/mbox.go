package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

// ParseError is returned for a message that could not be parsed. The source
// stays usable: callers may log it and keep reading.
type ParseError struct {
	Index int // 1-based position of the message in the mailbox
	Err   error
}

func (e *ParseError) Error() string { return fmt.Sprintf("message %d: %v", e.Index, e.Err) }
func (e *ParseError) Unwrap() error { return e.Err }

// maxMessage bounds the memory used for one message (attachments included).
const maxMessage = 64 << 20

var (
	fromLine   = regexp.MustCompile(`^From \S+.*\d{4}\s*$`)
	quotedFrom = regexp.MustCompile(`^>+From `)
)

// MBox reads an mbox file (e.g. the "All Mail.mbox" of a Google Takeout)
// message by message, streaming: memory use is one message, not the mailbox.
type MBox struct {
	f   *os.File
	r   *bufio.Reader
	idx int
	// pending holds the "From " separator line that ended the previous message.
	started bool
	eof     bool
}

// NewMBox opens an mbox file.
func NewMBox(path string) (*MBox, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &MBox{f: f, r: bufio.NewReaderSize(f, 1<<20)}, nil
}

// Close implements Source.
func (m *MBox) Close() error { return m.f.Close() }

// Next implements Source.
func (m *MBox) Next(ctx context.Context) (email.Email, error) {
	for {
		raw, err := m.nextRaw(ctx)
		if err != nil {
			return email.Email{}, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		m.idx++
		e, perr := email.ParseEML(raw)
		if perr != nil {
			return email.Email{}, &ParseError{Index: m.idx, Err: perr}
		}
		return e, nil
	}
}

// nextRaw returns the raw bytes of the next message (separator excluded).
func (m *MBox) nextRaw(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.eof {
		return nil, io.EOF
	}
	var buf bytes.Buffer
	truncated := false
	for {
		line, err := m.r.ReadBytes('\n')
		if len(line) > 0 {
			isSep := fromLine.Match(bytes.TrimRight(line, "\r\n"))
			switch {
			case isSep && !m.started:
				m.started = true // first separator: nothing to emit yet
				continue
			case isSep:
				if truncated {
					return nil, &ParseError{Index: m.idx + 1, Err: errors.New("message larger than 64 MiB skipped")}
				}
				return buf.Bytes(), nil
			}
			m.started = true
			if !truncated {
				if quotedFrom.Match(line) { // mboxrd: ">From " was escaped on write
					line = line[1:]
				}
				if buf.Len()+len(line) > maxMessage {
					truncated = true
					buf.Reset()
				} else {
					buf.Write(line)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				m.eof = true
				if truncated {
					m.idx++
					return nil, &ParseError{Index: m.idx, Err: errors.New("message larger than 64 MiB skipped")}
				}
				if buf.Len() == 0 {
					return nil, io.EOF
				}
				return buf.Bytes(), nil
			}
			return nil, err
		}
	}
}
