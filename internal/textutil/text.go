// Package textutil holds the small text helpers shared by feature extraction
// and similarity: accent folding, tokenization and stop-word filtering.
package textutil

import (
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var foldPool = sync.Pool{New: func() any {
	return transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
}}

// Fold lower-cases s and strips diacritics ("Transacción" -> "transaccion").
func Fold(s string) string {
	t := foldPool.Get().(transform.Transformer)
	defer foldPool.Put(t)
	t.Reset()
	out, _, err := transform.String(t, strings.ToLower(s))
	if err != nil {
		return strings.ToLower(s)
	}
	return out
}

// Tokens splits folded text into alphanumeric tokens of at least 3 runes, with
// stop-words removed. Pure-number tokens are dropped.
func Tokens(s string) []string {
	f := Fold(s)
	fields := strings.FieldsFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := make([]string, 0, len(fields))
	for _, t := range fields {
		if len([]rune(t)) < 3 || isNumber(t) || IsStopword(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isNumber(t string) bool {
	for _, r := range t {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// IsStopword reports whether the (folded) token is a stop-word in any
// supported language.
func IsStopword(t string) bool { _, ok := stop[t]; return ok }

// LanguageStopwords returns the stop-word set used for language detection.
func LanguageStopwords() map[string]map[string]struct{} { return langStop }

var stop = map[string]struct{}{}
var langStop = map[string]map[string]struct{}{}

func init() {
	lists := map[string]string{
		"es": "el la los las un una unos unas de del al y o u en por para con sin que se su sus es son fue ser esta este estos estas esto como mas pero muy ya no si le les lo me mi tu te nos nuestro hemos has han hay ha fue desde entre sobre tambien cuando donde hasta todo todos usted ustedes gracias hola saludos",
		"en": "the a an and or of to in on for with without that this these those is are was were be been it its as at by from your you we our they their he she his her not have has had will would can could do does did but if so than then there here all any please thanks hello regards",
		"pt": "o os as um uma uns umas de do da dos das em no na nos nas por para com sem que se seu sua seus suas e ou mas muito ja nao sim voce voces nosso obrigado ola",
		"fr": "le la les un une des du de et ou en dans pour avec sans que qui est sont ete etre vous nous votre vos notre nos ce cette ces mais pas plus tres merci bonjour",
	}
	for lang, l := range lists {
		set := map[string]struct{}{}
		for _, w := range strings.Fields(l) {
			set[w] = struct{}{}
			stop[w] = struct{}{}
		}
		langStop[lang] = set
	}
}
