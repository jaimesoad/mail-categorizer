// Package email defines the provider-independent email model, a .eml parser
// and the normalization step that precedes feature extraction.
package email

import "time"

// Address is a parsed mailbox.
type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// Domain returns the lower-cased host part of the address.
func (a Address) Domain() string {
	for i := len(a.Email) - 1; i >= 0; i-- {
		if a.Email[i] == '@' {
			return a.Email[i+1:]
		}
	}
	return ""
}

// Attachment describes an attachment without keeping its payload.
type Attachment struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int    `json:"size,omitempty"`
}

// Email is the normalized representation consumed by the rest of the system.
// Mail providers (IMAP, Gmail, .eml files...) only have to produce this type.
type Email struct {
	ID         string              `json:"id"`
	MessageID  string              `json:"message_id,omitempty"`
	ThreadID   string              `json:"thread_id,omitempty"`
	InReplyTo  string              `json:"in_reply_to,omitempty"`
	References []string            `json:"references,omitempty"`
	From       Address             `json:"from"`
	To         []Address           `json:"to,omitempty"`
	Cc         []Address           `json:"cc,omitempty"`
	ReplyTo    []Address           `json:"reply_to,omitempty"`
	Subject    string              `json:"subject"`
	BodyText   string              `json:"body"`
	ReceivedAt time.Time           `json:"received_at,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"` // lower-cased names
	Attach     []Attachment        `json:"attachments,omitempty"`
	RawHash    string              `json:"raw_hash,omitempty"`
}

// Header returns the first value of a header (case-insensitive) or "".
func (e Email) Header(name string) string {
	if v := e.Headers[lower(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// HasHeader reports whether the header is present.
func (e Email) HasHeader(name string) bool {
	_, ok := e.Headers[lower(name)]
	return ok
}
