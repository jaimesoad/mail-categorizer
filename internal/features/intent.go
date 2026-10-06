package features

import (
	"context"
	"regexp"
	"sort"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/textutil"
)

// IntentPattern is one weighted signal for an intent. Patterns run over
// accent-folded, lower-cased text, so they are written without diacritics.
type IntentPattern struct {
	Intent string
	Re     *regexp.Regexp
	Weight float64
}

// IntentExtractor detects the common intents of §7.4 with bilingual (es/en)
// patterns. The pattern list is extensible via NewIntentExtractor(extra...).
type IntentExtractor struct{ patterns []IntentPattern }

// Intent names.
const (
	IntentTransaction = "transaction"
	IntentReceipt     = "receipt"
	IntentInvoice     = "invoice"
	IntentStatement   = "statement"
	IntentPromotion   = "promotion"
	IntentNewsletter  = "newsletter"
	IntentNotify      = "notification"
	IntentSecurity    = "security_alert"
	IntentMeeting     = "meeting"
	IntentPersonal    = "personal_message"
	IntentShipping    = "shipping"
	IntentPassword    = "password_reset"
	IntentAccount     = "account_update"
	IntentSocial      = "social_notification"
)

func p(intent, re string, w float64) IntentPattern {
	return IntentPattern{Intent: intent, Re: regexp.MustCompile(re), Weight: w}
}

// DefaultIntentPatterns returns the built-in pattern set.
func DefaultIntentPatterns() []IntentPattern {
	return []IntentPattern{
		p(IntentTransaction, `\b(transaction|transacciones?|transaccion)\b`, 0.7),
		p(IntentTransaction, `\b(payment|pago) (was |has been |fue |ha sido )?(received|completed|processed|recibido|realizado|aprobado|procesado|exitoso)\b`, 0.75),
		p(IntentTransaction, `\b(debited|credited|debitado|acreditado|cargo a (tu|su)|cargo en (tu|su)|retiro|deposito|transferencia)\b`, 0.6),
		p(IntentTransaction, `\b(was|has been|fue|ha sido) (completed|approved|declined|completada|aprobada|rechazada)\b`, 0.45),
		p(IntentReceipt, `\b(receipt|recibo|comprobante de (pago|compra))\b`, 0.75),
		p(IntentReceipt, `\b(thank you for your (order|purchase)|gracias por tu (compra|pedido)|gracias por su (compra|pedido))\b`, 0.75),
		p(IntentReceipt, `\b(order confirmation|confirmacion de (pedido|compra|orden)|your order)\b`, 0.7),
		p(IntentInvoice, `\b(invoice|factura|cfdi|nota de credito)\b`, 0.75),
		p(IntentInvoice, `\b(amount due|importe a pagar|total a pagar|fecha de vencimiento|due date|payment due)\b`, 0.6),
		p(IntentStatement, `\b(statement|estado de cuenta|resumen de (cuenta|movimientos)|extracto)\b`, 0.8),
		p(IntentPromotion, `\b\d{1,2}\s?% ?(off|de descuento|dto|descuento)\b`, 0.85),
		p(IntentPromotion, `\b(descuento|oferta|ofertas|promo|promocion|cupon|coupon|flash sale|black friday|cyber monday|clearance|liquidacion)\b`, 0.6),
		p(IntentPromotion, `\b(limited time|tiempo limitado|shop now|comprar ahora|compra ahora|solo por hoy|ultimos dias|free shipping|envio gratis)\b`, 0.6),
		p(IntentNewsletter, `\b(newsletter|boletin|weekly (digest|roundup)|daily digest|resumen semanal|resumen diario|this week in|lo mas leido)\b`, 0.8),
		p(IntentNewsletter, `\b(view (this email )?in (your )?browser|ver en (el )?navegador|ver en linea)\b`, 0.5),
		p(IntentNotify, `\b(notification|notificacion|automated message|mensaje automatico|do not reply|no respondas)\b`, 0.5),
		p(IntentSecurity, `\b(security alert|alerta de seguridad|new (sign.?in|login)|nuevo inicio de sesion|unusual (activity|sign.?in)|actividad (inusual|sospechosa)|suspicious)\b`, 0.85),
		p(IntentSecurity, `\b(verification code|codigo de verificacion|one.time (code|password)|codigo de un solo uso|2fa|two.factor)\b`, 0.7),
		p(IntentPassword, `\b(reset (your )?password|restablec\w+ (tu |su |la )?contrasena|forgot (your )?password|recuperar (tu |su |la )?contrasena|cambio de contrasena)\b`, 0.9),
		p(IntentAccount, `\b(update(d)? (to )?(our |your )?(account|profile|settings|terms|privacy|policy)|actualizacion de (cuenta|terminos|privacidad|politica)|cambios? en (nuestros|tus|los) terminos|terms of service|terminos de servicio)\b`, 0.65),
		p(IntentMeeting, `\b(meeting|reunion|invitation:|invitacion:|calendar|calendario|agenda|zoom|google meet|teams meeting|standup|1:1|junta)\b`, 0.6),
		p(IntentMeeting, `\b(you are invited|has been invited|te invita|estas invitado|rsvp|accept(ed)?|tentative)\b`, 0.45),
		p(IntentShipping, `\b(tracking( number)?|numero de (guia|rastreo|seguimiento)|shipped|out for delivery|delivered|entregado|shipment|envio|paquete|package|en camino|on its way)\b`, 0.7),
		p(IntentSocial, `\b((liked|commented|mentioned|tagged|followed|reacted) (on )?(you|your)|te (siguio|etiqueto|menciono|comento|dio me gusta)|new follower|nuevo seguidor|friend request|solicitud de amistad|connection request|solicitud de conexion|le gusta tu)\b`, 0.85),
	}
}

// socialDomains boosts social_notification when the sender is a known network.
var socialDomains = map[string]struct{}{
	"facebookmail.com": {}, "facebook.com": {}, "linkedin.com": {}, "twitter.com": {}, "x.com": {},
	"instagram.com": {}, "tiktok.com": {}, "pinterest.com": {}, "reddit.com": {}, "redditmail.com": {},
	"discord.com": {}, "mail.instagram.com": {}, "youtube.com": {},
}

// NewIntentExtractor builds an extractor with the default patterns plus extras.
func NewIntentExtractor(extra ...IntentPattern) IntentExtractor {
	return IntentExtractor{patterns: append(DefaultIntentPatterns(), extra...)}
}

func (x IntentExtractor) Extract(_ context.Context, e email.Email) ([]Feature, error) {
	subj := textutil.Fold(e.Subject)
	body := textutil.Fold(e.Subject + "\n" + e.BodyText)
	score := map[string]float64{}
	combine := func(intent string, w float64) { score[intent] = 1 - (1-score[intent])*(1-w) }

	for _, pt := range x.patterns {
		switch {
		case pt.Re.MatchString(subj):
			w := pt.Weight + 0.15 // subject hits are stronger evidence than body hits
			if w > 0.95 {
				w = 0.95
			}
			combine(pt.Intent, w)
		case pt.Re.MatchString(body):
			combine(pt.Intent, pt.Weight)
		}
	}

	// structural signals
	hasUnsub := e.HasHeader("List-Unsubscribe")
	if hasUnsub {
		combine(IntentNewsletter, 0.3)
		combine(IntentPromotion, 0.2)
	}
	for _, a := range e.Attach {
		if a.ContentType == "text/calendar" {
			combine(IntentMeeting, 0.8)
		}
	}
	if _, ok := socialDomains[RegistrableDomain(e.From.Domain())]; ok {
		combine(IntentSocial, 0.5)
	}
	if isNoReply(localPart(e)) {
		combine(IntentNotify, 0.35)
	}

	// personal_message: nothing automated about it.
	auto := e.Header("Auto-Submitted")
	if len(score) == 0 && !hasUnsub && !e.HasHeader("List-Id") && (auto == "" || auto == "no") && !isNoReply(localPart(e)) && e.From.Email != "" {
		combine(IntentPersonal, 0.5)
	}

	type is struct {
		intent string
		s      float64
	}
	var list []is
	for k, v := range score {
		if v >= 0.5 {
			list = append(list, is{k, v})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].s != list[j].s {
			return list[i].s > list[j].s
		}
		return list[i].intent < list[j].intent
	})
	var out []Feature
	for i, it := range list {
		if i >= 3 {
			break
		}
		c := round2(it.s)
		out = append(out, Feature{ContentIntent, it.intent, c, SourcePattern})
		switch it.intent {
		case IntentTransaction:
			out = append(out, Feature{ContentContainsTx, "true", c, SourcePattern})
		case IntentInvoice:
			out = append(out, Feature{ContentContainsInvoice, "true", c, SourcePattern})
		case IntentReceipt:
			out = append(out, Feature{ContentContainsReceipt, "true", c, SourcePattern})
		case IntentMeeting:
			out = append(out, Feature{ContentContainsMeeting, "true", c, SourcePattern})
		case IntentNewsletter:
			out = append(out, Feature{ContentContainsNewsltr, "true", c, SourcePattern})
		}
	}
	return out, nil
}

func localPart(e email.Email) string {
	h := e.From.Domain()
	return e.From.Email[:len(e.From.Email)-len(h)-boolToInt(h != "")]
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
