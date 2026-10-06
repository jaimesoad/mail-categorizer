package email

import (
	"crypto/sha256"
	"encoding/hex"
	"net/mail"
	"strings"
	"unicode/utf8"
)

// DefaultMaxBody is the maximum number of runes of body text that are kept.
const DefaultMaxBody = 20000

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Normalize canonicalizes an email: trimmed and lower-cased addresses, unified
// line endings, collapsed blank lines, bounded body size, a stable raw hash and
// an ID when none was supplied. It never mutates its argument.
func Normalize(e Email, maxBody int) Email {
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	out := e
	out.From = normAddr(e.From)
	out.To = normAddrs(e.To)
	out.Cc = normAddrs(e.Cc)
	out.ReplyTo = normAddrs(e.ReplyTo)
	out.Subject = strings.Join(strings.Fields(e.Subject), " ")
	out.BodyText = normBody(e.BodyText, maxBody)
	out.MessageID = strings.Trim(strings.TrimSpace(e.MessageID), "<>")
	out.InReplyTo = strings.Trim(strings.TrimSpace(e.InReplyTo), "<>")
	out.References = nil
	for _, r := range e.References {
		if r = strings.Trim(strings.TrimSpace(r), "<>"); r != "" {
			out.References = append(out.References, r)
		}
	}
	if e.Headers != nil {
		out.Headers = make(map[string][]string, len(e.Headers))
		for k, v := range e.Headers {
			out.Headers[lower(k)] = append([]string(nil), v...)
		}
	}
	if out.RawHash == "" {
		out.RawHash = ContentHash(out)
	}
	if out.ID == "" {
		out.ID = "em_" + out.RawHash[:16]
	}
	return out
}

// ContentHash is a stable hash over the normalized content, used to detect
// duplicates when no raw bytes are available.
func ContentHash(e Email) string {
	h := sha256.New()
	for _, s := range []string{e.MessageID, e.From.Email, e.Subject, e.BodyText, e.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z")} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normAddr(a Address) Address {
	return Address{Name: strings.TrimSpace(a.Name), Email: lower(a.Email)}
}

func normAddrs(in []Address) []Address {
	var out []Address
	for _, a := range in {
		if n := normAddr(a); n.Email != "" {
			out = append(out, n)
		}
	}
	return out
}

func normBody(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ToValidUTF8(s, "")
	lines := strings.Split(s, "\n")
	var b strings.Builder
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	s = strings.TrimSpace(b.String())
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// ParseAddress parses "Name <a@b>" or "a@b". Invalid input yields a zero Address.
func ParseAddress(s string) Address {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		if strings.Contains(s, "@") {
			return Address{Email: lower(s)}
		}
		return Address{}
	}
	return Address{Name: a.Name, Email: lower(a.Address)}
}
