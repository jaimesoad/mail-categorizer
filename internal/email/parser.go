package email

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"

	"github.com/jhillyerd/enmime/v2"
)

// keptHeaders are the only headers copied into the model. Everything else is
// dropped on purpose: less data stored, less data leaked.
var keptHeaders = []string{
	"List-Unsubscribe", "List-Id", "Precedence", "Auto-Submitted", "X-Auto-Response-Suppress",
	"X-Mailer", "Reply-To", "In-Reply-To", "References", "Return-Path", "Content-Type",
	"MIME-Version", "X-Priority", "Importance", "X-Gmail-Labels",
}

// ParseEML parses RFC 5322 / MIME bytes into an Email. MIME decoding, charsets
// and HTML-to-text fallbacks are delegated to enmime; no LLM is involved.
func ParseEML(raw []byte) (Email, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return Email{}, fmt.Errorf("parse eml: %w", err)
	}
	sum := sha256.Sum256(raw)
	e := Email{
		MessageID: env.GetHeader("Message-ID"),
		InReplyTo: env.GetHeader("In-Reply-To"),
		Subject:   env.GetHeader("Subject"),
		BodyText:  env.Text,
		RawHash:   hex.EncodeToString(sum[:]),
		Headers:   map[string][]string{},
	}
	if e.BodyText == "" {
		e.BodyText = env.HTML
	}
	e.References = strings.Fields(env.GetHeader("References"))
	if from, err := mail.ParseAddress(env.GetHeader("From")); err == nil {
		e.From = Address{Name: from.Name, Email: from.Address}
	} else {
		e.From = ParseAddress(env.GetHeader("From"))
	}
	e.To = addressList(env, "To")
	e.Cc = addressList(env, "Cc")
	e.ReplyTo = addressList(env, "Reply-To")
	if d, err := env.Date(); err == nil {
		e.ReceivedAt = d.UTC()
	}
	for _, h := range keptHeaders {
		if v := env.GetHeaderValues(h); len(v) > 0 {
			e.Headers[strings.ToLower(h)] = v
		}
	}
	for _, a := range append(append([]*enmime.Part{}, env.Attachments...), env.Inlines...) {
		e.Attach = append(e.Attach, Attachment{Filename: a.FileName, ContentType: a.ContentType, Size: len(a.Content)})
	}
	return Normalize(e, 0), nil
}

func addressList(env *enmime.Envelope, h string) []Address {
	list, err := env.AddressList(h)
	if err != nil {
		return nil
	}
	out := make([]Address, 0, len(list))
	for _, a := range list {
		out = append(out, Address{Name: a.Name, Email: a.Address})
	}
	return out
}

// ParseEMLFile reads and parses a .eml file.
func ParseEMLFile(path string) (Email, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Email{}, err
	}
	return ParseEML(b)
}

// ParseEMLReader parses from a reader.
func ParseEMLReader(r io.Reader) (Email, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return Email{}, err
	}
	return ParseEML(b)
}
