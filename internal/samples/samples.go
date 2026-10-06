// Package samples generates a reproducible synthetic mailbox (as real RFC 5322
// messages) for demos and for the offline evaluation of §35. The corpus mimics
// the difficulty of a real inbox: several senders per category, bilingual
// text, and deliberately ambiguous cases (a store saying "payment received").
package samples

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Sample is one synthetic message with its true category.
type Sample struct {
	ID       string
	Category string
	EML      []byte
}

type profile struct {
	category string
	from     []string // "Name <addr>"
	subjects []string
	bodies   []string
	unsub    bool // adds List-Unsubscribe
	listID   string
}

// {n} expands to a number, {amt} to an amount, {name} to a first name.
var profiles = []profile{
	{category: "finance.bank",
		from:     []string{"MyBank Alerts <alerts@mybank.example>", "First Bank <notify@firstbank.example>", "Banco Norte <avisos@banconorte.example>"},
		subjects: []string{"Transaction completed", "Aviso de transacción en tu cuenta", "Your monthly statement is ready", "Movimiento detectado en tu cuenta"},
		bodies: []string{
			"A transaction of {amt} was completed on your account ending in {n4}. If you do not recognize it call your bank.",
			"Se realizó un cargo de {amt} en tu cuenta bancaria terminación {n4}. Referencia: {n}.",
			"Your account statement for the period is available. Closing balance {amt}. Transaction reference {n}.",
		}},
	{category: "finance.card",
		from:     []string{"Visa Services <cards@visaservices.example>", "Tarjeta Oro <tarjeta@oro-card.example>"},
		subjects: []string{"Your credit card statement", "Pago de tu tarjeta de crédito recibido", "Credit card payment due soon"},
		bodies: []string{
			"Your credit card statement is ready. Minimum payment {amt}, due date 15/11/2026. Card ending {n4}.",
			"Recibimos el pago de tu tarjeta de crédito por {amt}. Gracias por tu pago.",
			"Reminder: credit card payment of {amt} due on 20/11/2026. Pay now to avoid fees.",
		}},
	{category: "finance.purchase",
		from:     []string{"ShopMart <orders@shopmart.example>", "BigStore <store@bigstore.example>", "Tienda Online <pedidos@tienda-online.example>"},
		subjects: []string{"Order confirmation #{n}", "Thank you for your purchase", "Recibo de tu compra", "Payment received for order {n}", "Gracias por tu pedido"},
		bodies: []string{
			"Thank you for your order #{n}. Payment received: {amt}. Your receipt is attached below. We will notify you when it ships.",
			"Gracias por tu compra. Pedido {n} por {amt}. Pago recibido correctamente. Conserva este recibo.",
			"Order confirmation: your order {n} totalling {amt} was placed. Payment of {amt} received.",
		}},
	{category: "finance.taxes",
		from:     []string{"Tax Office <noreply@taxoffice.example>", "SAT Notificaciones <avisos@sat-notifica.example>"},
		subjects: []string{"Tax return deadline approaching", "Declaración anual de impuestos", "Factura fiscal disponible"},
		bodies: []string{
			"Your annual tax return deadline is 30/04/2027. Invoice {n} for {amt} is available for download.",
			"Recordatorio: presenta tu declaración anual de impuestos. Factura {n} por {amt} disponible. Fecha límite 30/04/2027.",
		}},
	{category: "work",
		from:     []string{"Laura Gómez <laura.gomez@acmecorp.example>", "Carlos Ruiz <carlos.ruiz@acmecorp.example>", "Dana Lee <dana.lee@acmecorp.example>"},
		subjects: []string{"Re: Q3 report draft", "Proyecto Atlas: avances de la semana", "Quick question about the roadmap", "Revisión del documento de arquitectura"},
		bodies: []string{
			"Hi, I reviewed the draft and left comments on the roadmap section. Can you update the architecture doc before Friday? Thanks, {name}",
			"Hola, te comparto los avances del proyecto Atlas. Necesito tu revisión del documento antes del viernes. Gracias, {name}",
			"Could you send me the latest numbers for the project plan? We need them for the client call. {name}",
		}},
	{category: "work.meetings",
		from:     []string{"Calendar <calendar@acmecorp.example>", "Dana Lee <dana.lee@acmecorp.example>"},
		subjects: []string{"Invitation: Sprint review @ Tue 10:00", "Meeting tomorrow: planning", "Invitación: reunión de equipo", "Reminder: standup moved"},
		bodies: []string{
			"You are invited to the Sprint review. Join the Zoom meeting tomorrow at 10:00. Agenda attached. RSVP please.",
			"Te invitamos a la reunión de equipo mañana a las 10:00. Agenda: avances y bloqueos. Confirma tu asistencia.",
			"The standup has moved to 9:30. Calendar updated, join the Google Meet link in the invitation.",
		}},
	{category: "personal",
		from:     []string{"Mamá <mama.perez@gmail.com>", "Alex <alex.ortiz@outlook.com>", "Sofi <sofia.m@gmail.com>"},
		subjects: []string{"Cena el sábado?", "Fotos del viaje", "hey!", "Feliz cumpleaños"},
		bodies: []string{
			"Hola, ¿vienes a cenar el sábado? Hace mucho que no nos vemos. Te quiero, {name}",
			"Oye te paso las fotos del viaje, estuvo genial. Hablamos pronto, {name}",
			"Hey, are you free this weekend? We are thinking of going to the beach. Let me know! {name}",
		}},
	{category: "newsletters", unsub: true, listID: "<weekly.techweekly.example>",
		from:     []string{"Tech Weekly <news@techweekly.example>", "El Boletín <hola@elboletin.example>"},
		subjects: []string{"Tech Weekly: top stories this week", "Tu resumen semanal", "Weekly digest #{n}"},
		bodies: []string{
			"Newsletter: this week in tech — five stories you may have missed. View in browser. To stop receiving this newsletter unsubscribe below.",
			"Resumen semanal de noticias: lo más leído de la semana. Ver en el navegador. Boletín número {n}.",
		}},
	{category: "promotions", unsub: true,
		from:     []string{"Deals <deals@megadeals.example>", "Ofertas <promo@ofertas-hoy.example>", "ShopMart Offers <offers@shopmart.example>"},
		subjects: []string{"50% off everything — today only", "Flash sale ends tonight!", "Ofertas de temporada: hasta 40% de descuento", "Cupón exclusivo para ti"},
		bodies: []string{
			"Don't miss our flash sale! 50% off sitewide. Limited time only. Shop now and get free shipping.",
			"Aprovecha las ofertas: hasta 40% de descuento. Cupón válido solo por hoy. Compra ahora.",
			"Exclusive coupon inside: save {amt} on your next order. Shop now before it ends.",
		}},
	{category: "social",
		from:     []string{"LinkedIn <notifications@linkedin.com>", "Facebook <notification@facebookmail.com>", "Instagram <no-reply@mail.instagram.com>"},
		subjects: []string{"{name} liked your post", "You have a new follower", "{name} commented on your photo", "Solicitud de conexión de {name}"},
		bodies: []string{
			"{name} liked your post. See who else reacted. You have 3 new connection requests.",
			"{name} commented on your photo: 'Great shot!'. Reply to the comment.",
			"{name} started following you. Nuevo seguidor en tu perfil.",
		}},
	{category: "shipping",
		from:     []string{"DHL Tracking <tracking@dhl-notify.example>", "ParcelPost <noreply@parcelpost.example>", "Envíos Rápidos <guias@enviosrapidos.example>"},
		subjects: []string{"Your package is out for delivery", "Paquete en camino — guía {n}", "Delivered: your shipment {n}"},
		bodies: []string{
			"Your package is out for delivery. Tracking number {n}. Estimated arrival today before 18:00.",
			"Tu paquete está en camino. Número de guía {n}. Entrega estimada mañana.",
			"Your shipment {n} was delivered. Package left at the front door.",
		}},
	{category: "security",
		from:     []string{"Accounts Security <security@accounts-secure.example>", "Seguridad <alertas@seguridad-cuentas.example>"},
		subjects: []string{"New sign-in detected on your account", "Restablece tu contraseña", "Security alert: unusual activity", "Your verification code is {n6}"},
		bodies: []string{
			"We noticed a new sign-in to your account from an unknown device. If this was not you, reset your password immediately.",
			"Recibimos una solicitud para restablecer tu contraseña. Si no fuiste tú ignora este mensaje. Código de verificación {n6}.",
			"Security alert: unusual activity detected. Your verification code is {n6}. Never share it.",
		}},
}

var firstNames = []string{"Ana", "Luis", "Marta", "Pedro", "Julia", "Diego", "Sam", "Nora"}

// Categories returns the category ids covered by the generator.
func Categories() []string {
	out := make([]string, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p.category)
	}
	sort.Strings(out)
	return out
}

// Generate builds perCategory messages for every category. The output is a pure
// function of (seed, perCategory).
func Generate(seed int64, perCategory int) []Sample {
	rng := rand.New(rand.NewSource(seed))
	base := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var out []Sample
	for _, p := range profiles {
		for i := 0; i < perCategory; i++ {
			from := p.from[rng.Intn(len(p.from))]
			subj := fill(rng, p.subjects[rng.Intn(len(p.subjects))])
			body := fill(rng, p.bodies[rng.Intn(len(p.bodies))])
			id := fmt.Sprintf("%s-%03d", strings.ReplaceAll(p.category, ".", "_"), i)
			date := base.Add(time.Duration(len(out)) * 37 * time.Minute)
			var h strings.Builder
			fmt.Fprintf(&h, "From: %s\r\nTo: me@example.net\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@sample.local>\r\n",
				from, subj, date.Format(time.RFC1123Z), id)
			if p.unsub {
				h.WriteString("List-Unsubscribe: <mailto:unsubscribe@sample.local>\r\nPrecedence: bulk\r\n")
			}
			if p.listID != "" {
				fmt.Fprintf(&h, "List-Id: %s\r\n", p.listID)
			}
			h.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
			h.WriteString(body)
			h.WriteString("\r\n")
			out = append(out, Sample{ID: id, Category: p.category, EML: []byte(h.String())})
		}
	}
	return out
}

func fill(rng *rand.Rand, s string) string {
	for strings.Contains(s, "{") {
		switch {
		case strings.Contains(s, "{amt}"):
			s = strings.Replace(s, "{amt}", fmt.Sprintf("$%d.%02d", 5+rng.Intn(900), rng.Intn(100)), 1)
		case strings.Contains(s, "{n4}"):
			s = strings.Replace(s, "{n4}", fmt.Sprintf("%04d", rng.Intn(10000)), 1)
		case strings.Contains(s, "{n6}"):
			s = strings.Replace(s, "{n6}", fmt.Sprintf("%06d", rng.Intn(1000000)), 1)
		case strings.Contains(s, "{n}"):
			s = strings.Replace(s, "{n}", fmt.Sprintf("%d", 100000+rng.Intn(900000)), 1)
		case strings.Contains(s, "{name}"):
			s = strings.Replace(s, "{name}", firstNames[rng.Intn(len(firstNames))], 1)
		default:
			return s
		}
	}
	return s
}
