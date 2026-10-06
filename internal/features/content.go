package features

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/textutil"
)

// ContentExtractor derives features from the subject and body (layer 7.3).
// Raw values (amounts, ids, phone numbers) are intentionally not stored:
// only their presence is, which is all classification needs and is safer.
type ContentExtractor struct{}

var (
	reAmount = regexp.MustCompile(`(?i)(?:[$€£]\s?\d[\d.,]*|\b\d[\d.,]*\s?(?:usd|eur|mxn|gbp|cop|ars|clp|dolares|euros|pesos)\b)`)
	reInvNum = regexp.MustCompile(`(?i)\b(?:invoice|factura|folio|inv)\b[^\n\d]{0,16}[a-z]{0,4}-?\d{3,}`)
	reOrdNum = regexp.MustCompile(`(?i)\b(?:order|pedido|orden)\b[^\n\d]{0,16}[a-z]{0,4}-?\d{4,}`)
	reTxID   = regexp.MustCompile(`(?i)\b(?:transaction|transaccion|referencia|reference|confirmation|confirmacion|auth(?:orization)?|autorizacion)\b[^\n\d]{0,16}[a-z0-9-]{0,6}\d{4,}`)
	rePhone  = regexp.MustCompile(`\+?\d[\d\s().-]{7,}\d`)
	reDate   = regexp.MustCompile(`(?i)\b(?:\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|\d{4}-\d{2}-\d{2}|\d{1,2}\s+(?:de\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec|ene|abr|ago|dic)[a-z]*\.?(?:\s+(?:de\s+)?\d{4})?)\b`)
	reURL    = regexp.MustCompile(`(?i)https?://[^\s<>"')\]]+`)
)

func (ContentExtractor) Extract(_ context.Context, e email.Email) ([]Feature, error) {
	text := e.Subject + "\n" + e.BodyText
	folded := textutil.Fold(text)
	var out []Feature
	add := func(name, v string, c float64) { out = append(out, Feature{name, v, c, SourcePattern}) }

	if reAmount.MatchString(text) {
		add(ContentContainsAmount, "true", 0.95)
	}
	if reInvNum.MatchString(folded) {
		add(ContentHasInvoiceNumber, "true", 0.9)
	}
	if reOrdNum.MatchString(folded) {
		add(ContentHasOrderNumber, "true", 0.9)
	}
	if reTxID.MatchString(folded) {
		add(ContentHasTxID, "true", 0.85)
	}
	if hasPhone(text) {
		add(ContentHasPhone, "true", 0.7)
	}
	if reDate.MatchString(folded) {
		add(ContentHasDate, "true", 0.8)
	}

	// URLs: keep only registrable domains, never full URLs (they often carry tokens).
	urls := reURL.FindAllString(text, -1)
	if len(urls) > 0 {
		add(ContentURLCount, strconv.Itoa(len(urls)), 1)
		seen := map[string]struct{}{}
		var doms []string
		for _, u := range urls {
			pu, err := url.Parse(u)
			if err != nil || pu.Hostname() == "" {
				continue
			}
			d := RegistrableDomain(pu.Hostname())
			if _, ok := seen[d]; !ok {
				seen[d] = struct{}{}
				doms = append(doms, d)
			}
		}
		sort.Strings(doms)
		for i, d := range doms {
			if i >= 5 {
				break
			}
			add(ContentURLDomain, d, 0.9)
		}
	}

	if lang, conf := detectLanguage(folded); lang != "" {
		add(ContentLanguage, lang, conf)
	}

	for _, t := range uniqueTokens(textutil.Tokens(e.Subject), 12) {
		out = append(out, Feature{SubjectTokens, t, 0.8, SourceParser})
	}
	for _, k := range topKeywords(textutil.Tokens(text), 8) {
		add(ContentKeyword, k, 0.5)
	}
	return out, nil
}

func hasPhone(s string) bool {
	for _, m := range rePhone.FindAllString(s, 5) {
		digits := 0
		for _, r := range m {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits >= 9 && digits <= 15 && (strings.ContainsAny(m, "+()- ") || strings.HasPrefix(m, "+")) {
			return true
		}
	}
	return false
}

func detectLanguage(folded string) (string, float64) {
	words := strings.FieldsFunc(folded, func(r rune) bool { return !(r >= 'a' && r <= 'z') })
	if len(words) < 4 {
		return "", 0
	}
	langs := textutil.LanguageStopwords()
	names := make([]string, 0, len(langs))
	for l := range langs {
		names = append(names, l)
	}
	sort.Strings(names)
	best, bestN, total := "", 0, 0
	for _, l := range names {
		n := 0
		for _, w := range words {
			if _, ok := langs[l][w]; ok {
				n++
			}
		}
		total += n
		if n > bestN {
			best, bestN = l, n
		}
	}
	if bestN < 2 || total == 0 {
		return "", 0
	}
	share := float64(bestN) / float64(total)
	if share < 0.5 {
		return "", 0
	}
	// confidence grows with both dominance and amount of evidence
	ev := float64(bestN) / (float64(bestN) + 3)
	return best, round2(share * ev)
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func uniqueTokens(in []string, max int) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, t := range in {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) == max {
			break
		}
	}
	return out
}

func topKeywords(tokens []string, n int) []string {
	freq := map[string]int{}
	for _, t := range tokens {
		freq[t]++
	}
	type kv struct {
		k string
		n int
	}
	var kvs []kv
	for k, c := range freq {
		if c >= 2 || len(freq) <= n {
			kvs = append(kvs, kv{k, c})
		}
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].n != kvs[j].n {
			return kvs[i].n > kvs[j].n
		}
		return kvs[i].k < kvs[j].k
	})
	var out []string
	for i := 0; i < len(kvs) && i < n; i++ {
		out = append(out, kvs[i].k)
	}
	return out
}
