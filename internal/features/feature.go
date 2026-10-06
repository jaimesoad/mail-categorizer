// Package features defines the observable characteristics ("features") of an
// email and the layered extraction pipeline that produces them.
package features

import (
	"sort"
	"strings"
)

// Source says where a feature came from (§6).
type Source string

const (
	SourceHeader     Source = "header"
	SourceParser     Source = "parser"
	SourcePattern    Source = "pattern"
	SourceClassifier Source = "classifier"
	SourceLLM        Source = "llm"
	SourceUser       Source = "user"
	SourceHistory    Source = "history"
)

// Feature is one observation: hierarchical name, value, confidence, source.
// Multi-valued features (e.g. subject.tokens) are several Features sharing a name.
type Feature struct {
	Name       string  `json:"name"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Source     Source  `json:"source"`
}

// Names of the features emitted by the built-in extractors.
const (
	SenderEmail        = "sender.email"
	SenderDomain       = "sender.domain"
	SenderHost         = "sender.host"
	SenderName         = "sender.name"
	SenderIsKnown      = "sender.is_known"
	SenderIsFreemail   = "sender.is_freemail"
	SenderIsCorporate  = "sender.domain.is_corporate"
	SenderIsNoReply    = "sender.is_noreply"
	SenderPrevCategory = "sender.previous_category"
	DomainPrevCategory = "sender.domain.previous_category"

	ContentLanguage         = "content.language"
	ContentIntent           = "content.intent"
	ContentKeyword          = "content.keyword"
	ContentContainsAmount   = "content.contains_amount"
	ContentContainsInvoice  = "content.contains_invoice"
	ContentContainsReceipt  = "content.contains_receipt"
	ContentContainsTx       = "content.contains_transaction"
	ContentContainsMeeting  = "content.contains_meeting"
	ContentContainsNewsltr  = "content.contains_newsletter"
	ContentHasInvoiceNumber = "content.has_invoice_number"
	ContentHasOrderNumber   = "content.has_order_number"
	ContentHasTxID          = "content.has_transaction_id"
	ContentHasPhone         = "content.has_phone"
	ContentHasDate          = "content.has_date"
	ContentURLDomain        = "content.url_domain"
	ContentURLCount         = "content.url_count"
	SubjectTokens           = "subject.tokens"

	AttachmentCount          = "attachment.count"
	AttachmentHasPDF         = "attachment.has_pdf"
	AttachmentHasImage       = "attachment.has_image"
	AttachmentHasSpreadsheet = "attachment.has_spreadsheet"
	AttachmentHasCalendar    = "attachment.has_calendar"
	AttachmentTypes          = "attachment.types"

	HeadersHasUnsubscribe = "headers.has_unsubscribe"
	HeadersIsReply        = "headers.is_reply"
	HeadersIsForward      = "headers.is_forward"
	HeadersIsBulk         = "headers.is_bulk"
	HeadersIsAutomated    = "headers.is_automated"
	HeadersListID         = "headers.list_id"
	GmailLabel            = "gmail.label" // one value per Gmail label (lower-cased)

	ThreadPrevCategory = "thread.previous_category"
	ThreadPrevSender   = "thread.previous_sender"
)

// Set is the collection of features of one email. It keeps insertion order out
// of the picture: iteration is always sorted so results are deterministic.
type Set struct {
	byName map[string][]Feature
}

// NewSet builds a Set from a slice, de-duplicating identical (name,value) pairs
// by keeping the highest confidence.
func NewSet(fs ...Feature) *Set {
	s := &Set{byName: map[string][]Feature{}}
	s.Add(fs...)
	return s
}

// Add inserts features; for an existing (name,value) the higher confidence wins,
// except that a user-sourced feature always wins.
func (s *Set) Add(fs ...Feature) {
	for _, f := range fs {
		if f.Name == "" {
			continue
		}
		list := s.byName[f.Name]
		found := false
		for i := range list {
			if list[i].Value == f.Value {
				found = true
				if f.Source == SourceUser || (list[i].Source != SourceUser && f.Confidence > list[i].Confidence) {
					list[i] = f
				}
			}
		}
		if !found {
			list = append(list, f)
		}
		s.byName[f.Name] = list
	}
}

// Values returns all features with the given name, sorted by value.
func (s *Set) Values(name string) []Feature {
	list := append([]Feature(nil), s.byName[name]...)
	sort.Slice(list, func(i, j int) bool { return list[i].Value < list[j].Value })
	return list
}

// Has reports whether (name,value) is present.
func (s *Set) Has(name, value string) bool {
	for _, f := range s.byName[name] {
		if f.Value == value {
			return true
		}
	}
	return false
}

// Exists reports whether any value exists for name.
func (s *Set) Exists(name string) bool { return len(s.byName[name]) > 0 }

// First returns the highest-confidence value of name (ties broken by value).
func (s *Set) First(name string) (Feature, bool) {
	list := s.byName[name]
	if len(list) == 0 {
		return Feature{}, false
	}
	best := list[0]
	for _, f := range list[1:] {
		if f.Confidence > best.Confidence || (f.Confidence == best.Confidence && f.Value < best.Value) {
			best = f
		}
	}
	return best, true
}

// Str returns the best value of name or "".
func (s *Set) Str(name string) string {
	f, _ := s.First(name)
	return f.Value
}

// Bool returns (value, known). A feature is "known true" only when a "true"
// value is present; "known false" when only "false" values are present.
func (s *Set) Bool(name string) (value, known bool) {
	list := s.byName[name]
	if len(list) == 0 {
		return false, false
	}
	for _, f := range list {
		if strings.EqualFold(f.Value, "true") {
			return true, true
		}
	}
	return false, true
}

// Confidence returns the highest confidence among the values of name, or 0.
func (s *Set) Confidence(name string) float64 {
	best := 0.0
	for _, f := range s.byName[name] {
		if f.Confidence > best {
			best = f.Confidence
		}
	}
	return best
}

// All returns every feature sorted by (name, value).
func (s *Set) All() []Feature {
	var out []Feature
	for _, l := range s.byName {
		out = append(out, l...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// Names returns the sorted distinct feature names.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Clone returns an independent copy.
func (s *Set) Clone() *Set { return NewSet(s.All()...) }

// True builds a boolean "true" feature.
func True(name string, conf float64, src Source) Feature {
	return Feature{Name: name, Value: "true", Confidence: conf, Source: src}
}
