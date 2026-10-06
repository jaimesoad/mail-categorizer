package features

import (
	"context"
	"regexp"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

// HeaderExtractor derives features from RFC 5322 headers (layer 7.1).
type HeaderExtractor struct{}

// subjectPrefix matches one leading "Re:"/"Fwd:" style marker (several languages).
var subjectPrefix = regexp.MustCompile(`(?i)^\s*(re|res|aw|sv|fwd?|rv|reenv|wg)\s*(?:\[\d+\])?\s*:`)

// subjectMarkers walks the chain of leading prefixes ("Re: Fwd: x") and reports
// whether it contains a reply marker and/or a forward marker.
func subjectMarkers(subject string) (reply, forward bool) {
	for {
		m := subjectPrefix.FindStringSubmatchIndex(subject)
		if m == nil {
			return
		}
		switch strings.ToLower(subject[m[2]:m[3]]) {
		case "fwd", "fw", "rv", "reenv", "wg":
			forward = true
		default:
			reply = true
		}
		subject = subject[m[1]:]
	}
}

func (HeaderExtractor) Extract(_ context.Context, e email.Email) ([]Feature, error) {
	var out []Feature
	add := func(name, v string, c float64) { out = append(out, Feature{name, v, c, SourceHeader}) }
	boolf := func(name string, v bool) {
		val := "false"
		if v {
			val = "true"
		}
		add(name, val, 1.0)
	}

	boolf(HeadersHasUnsubscribe, e.HasHeader("List-Unsubscribe"))
	if lid := e.Header("List-Id"); lid != "" {
		add(HeadersListID, strings.ToLower(strings.Trim(lid, "<> ")), 1.0)
	}
	prec := strings.ToLower(e.Header("Precedence"))
	bulk := prec == "bulk" || prec == "list" || prec == "junk" || e.HasHeader("List-Id") || e.HasHeader("List-Unsubscribe")
	boolf(HeadersIsBulk, bulk)

	auto := strings.ToLower(e.Header("Auto-Submitted"))
	boolf(HeadersIsAutomated, (auto != "" && auto != "no") || e.HasHeader("X-Auto-Response-Suppress"))

	subjReply, subjFwd := subjectMarkers(e.Subject)
	boolf(HeadersIsReply, e.InReplyTo != "" || len(e.References) > 0 || subjReply)
	boolf(HeadersIsForward, subjFwd)
	return out, nil
}
