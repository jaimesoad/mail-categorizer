package features

import (
	"context"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

// SenderExtractor derives sender features (layer 7.2) except history-based ones.
type SenderExtractor struct{}

// freemail providers: a domain-level rule for them would generalize across
// unrelated people, so they are flagged and never used for domain learning.
var freemail = map[string]struct{}{}

func init() {
	for _, d := range strings.Fields(`gmail.com googlemail.com outlook.com hotmail.com live.com msn.com
		yahoo.com yahoo.es yahoo.com.mx ymail.com icloud.com me.com mac.com aol.com proton.me protonmail.com
		gmx.com gmx.net mail.com zoho.com yandex.com qq.com 163.com hotmail.es outlook.es`) {
		freemail[d] = struct{}{}
	}
}

// IsFreemail reports whether the registrable domain belongs to a free mailbox provider.
func IsFreemail(domain string) bool { _, ok := freemail[domain]; return ok }

// RegistrableDomain returns the eTLD+1 of host ("alerts.mybank.co.uk" ->
// "mybank.co.uk"), falling back to the host itself.
func RegistrableDomain(host string) string {
	host = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if host == "" {
		return ""
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

func (SenderExtractor) Extract(_ context.Context, e email.Email) ([]Feature, error) {
	if e.From.Email == "" {
		return nil, nil
	}
	host := e.From.Domain()
	domain := RegistrableDomain(host)
	local := strings.TrimSuffix(e.From.Email, "@"+host)
	out := []Feature{
		{SenderEmail, e.From.Email, 1, SourceHeader},
		{SenderHost, host, 1, SourceHeader},
		{SenderDomain, domain, 1, SourceHeader},
	}
	if e.From.Name != "" {
		out = append(out, Feature{SenderName, strings.ToLower(e.From.Name), 1, SourceHeader})
	}
	free := IsFreemail(domain)
	out = append(out,
		Feature{SenderIsFreemail, boolStr(free), 1, SourceParser},
		Feature{SenderIsCorporate, boolStr(!free), 0.8, SourceParser},
		Feature{SenderIsNoReply, boolStr(isNoReply(local)), 1, SourceParser},
	)
	return out, nil
}

func isNoReply(local string) bool {
	l := strings.ReplaceAll(strings.ReplaceAll(local, "-", ""), "_", "")
	return strings.HasPrefix(l, "noreply") || strings.HasPrefix(l, "donotreply") || l == "mailerdaemon"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// HistoryExtractor adds features that depend on the knowledge base: whether
// the sender is known, which categories it was filed under, and thread context.
type HistoryExtractor struct{ H History }

func (x HistoryExtractor) Extract(ctx context.Context, e email.Email) ([]Feature, error) {
	var out []Feature
	if e.From.Email != "" {
		domain := RegistrableDomain(e.From.Domain())
		sh, err := x.H.SenderHistory(ctx, e.From.Email, domain)
		if err != nil {
			return nil, err
		}
		out = append(out,
			Feature{SenderIsKnown, boolStr(sh.EmailCount > 0), 1, SourceHistory},
			Feature{"sender.domain.is_known", boolStr(sh.DomainCount > 0), 1, SourceHistory},
		)
		for i, c := range sh.EmailCategories {
			if i >= 3 {
				break
			}
			out = append(out, Feature{SenderPrevCategory, c, 0.9, SourceHistory})
		}
		for i, c := range sh.DomainCategories {
			if i >= 3 {
				break
			}
			out = append(out, Feature{DomainPrevCategory, c, 0.8, SourceHistory})
		}
	}
	ids := make([]string, 0, 1+len(e.References))
	if e.InReplyTo != "" {
		ids = append(ids, e.InReplyTo)
	}
	for i := len(e.References) - 1; i >= 0; i-- { // closest ancestors first
		ids = append(ids, e.References[i])
	}
	if len(ids) > 0 {
		ti, err := x.H.ThreadInfo(ctx, ids)
		if err != nil {
			return nil, err
		}
		if ti.Found {
			if ti.Category != "" {
				out = append(out, Feature{ThreadPrevCategory, ti.Category, 0.9, SourceHistory})
			}
			if ti.Sender != "" {
				out = append(out, Feature{ThreadPrevSender, ti.Sender, 1, SourceHistory})
			}
		}
	}
	return out, nil
}
