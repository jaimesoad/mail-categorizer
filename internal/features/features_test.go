package features

import (
	"context"
	"strings"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/email"
)

func extract(t *testing.T, e email.Email, h History) *Set {
	t.Helper()
	fs, err := Default(h).Extract(context.Background(), email.Normalize(e, 0))
	if err != nil {
		t.Fatal(err)
	}
	return NewSet(fs...)
}

func mail(from, subject, body string) email.Email {
	return email.Email{From: email.ParseAddress(from), Subject: subject, BodyText: body}
}

func TestSpecExampleFeatures(t *testing.T) {
	s := extract(t, mail("alerts@mybank.example", "Transaction completed", "A transaction of $85.40 was completed on your account."), nil)
	for _, c := range []struct{ name, val string }{
		{SenderDomain, "mybank.example"}, {SenderEmail, "alerts@mybank.example"},
		{ContentContainsAmount, "true"}, {ContentContainsTx, "true"}, {ContentIntent, "transaction"},
		{ContentLanguage, "en"}, {SenderIsFreemail, "false"}, {HeadersHasUnsubscribe, "false"},
	} {
		if !s.Has(c.name, c.val) {
			t.Errorf("missing %s=%s\nall: %v", c.name, c.val, s.All())
		}
	}
	if s.Confidence(ContentIntent) < 0.7 {
		t.Errorf("intent confidence %.2f too low", s.Confidence(ContentIntent))
	}
}

func TestRegistrableDomainAndFreemail(t *testing.T) {
	for in, want := range map[string]string{
		"alerts.mybank.com": "mybank.com", "a.b.mybank.co.uk": "mybank.co.uk", "mybank.example": "mybank.example", "": "",
	} {
		if got := RegistrableDomain(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
	s := extract(t, mail("juan@gmail.com", "hola", "¿vienes a cenar el sábado? te espero en casa"), nil)
	if !s.Has(SenderIsFreemail, "true") || s.Has(SenderIsCorporate, "true") {
		t.Errorf("gmail must be freemail: %v", s.All())
	}
	if !s.Has(ContentIntent, "personal_message") || !s.Has(ContentLanguage, "es") {
		t.Errorf("personal message in Spanish expected: %v", s.All())
	}
}

func TestIntentsBilingual(t *testing.T) {
	cases := []struct{ from, subj, body, intent string }{
		{"s@shop.example", "Order confirmation #123456", "Thank you for your order", IntentReceipt},
		{"s@shop.example", "Gracias por tu compra", "Tu pedido 998877 fue recibido", IntentReceipt},
		{"b@bank.example", "Tu estado de cuenta", "Resumen de movimientos del mes", IntentStatement},
		{"x@x.example", "Invoice 4455", "Amount due: $30. Due date 01/12/2026", IntentInvoice},
		{"n@x.example", "50% off today only", "Shop now! Limited time", IntentPromotion},
		{"n@x.example", "Weekly digest", "View in browser. Newsletter", IntentNewsletter},
		{"s@acc.example", "New sign-in detected", "Unusual activity on your account", IntentSecurity},
		{"s@acc.example", "Restablece tu contraseña", "Para restablecer tu contraseña haz clic", IntentPassword},
		{"c@x.example", "Invitation: Sprint review", "Join the Zoom meeting tomorrow", IntentMeeting},
		{"d@x.example", "Paquete en camino", "Número de guía 123456789", IntentShipping},
		{"notification@facebookmail.com", "Ana commented on your photo", "Ana commented", IntentSocial},
	}
	for _, c := range cases {
		s := extract(t, mail(c.from, c.subj, c.body), nil)
		if !s.Has(ContentIntent, c.intent) {
			t.Errorf("%q: want intent %s, got %v", c.subj, c.intent, s.Values(ContentIntent))
		}
	}
	// nothing automated about it => personal
	if s := extract(t, mail("friend@outlook.com", "hey", "are you free this weekend? let me know"), nil); !s.Has(ContentIntent, "personal_message") {
		t.Errorf("personal expected: %v", s.Values(ContentIntent))
	}
}

func TestPatternFeaturesStoreOnlyPresence(t *testing.T) {
	s := extract(t, mail("a@b.example", "Factura 12345", "Pedido 884211, referencia 99887766. Llámanos +52 55 1234 5678. Detalle en https://pay.shop.example/i?token=SECRET123 el 15/11/2026."), nil)
	for _, n := range []string{ContentHasInvoiceNumber, ContentHasOrderNumber, ContentHasTxID, ContentHasPhone, ContentHasDate} {
		if !s.Has(n, "true") {
			t.Errorf("missing %s: %v", n, s.All())
		}
	}
	if !s.Has(ContentURLDomain, "shop.example") {
		t.Errorf("url domain: %v", s.Values(ContentURLDomain))
	}
	for _, f := range s.All() {
		for _, leak := range []string{"SECRET123", "5678", "884211", "99887766"} {
			if strings.Contains(f.Value, leak) {
				t.Errorf("feature %s=%s leaks raw content", f.Name, f.Value)
			}
		}
	}
}

func TestHeaderAndAttachmentFeatures(t *testing.T) {
	e := mail("no-reply@news.example", "Re: Fwd: hello", "body words here for language detection and more words")
	e.Headers = map[string][]string{"List-Unsubscribe": {"<mailto:x@y>"}, "List-Id": {"<weekly.news.example>"}, "Precedence": {"bulk"}, "Auto-Submitted": {"auto-generated"}}
	e.InReplyTo = "p@x"
	e.Attach = []email.Attachment{{Filename: "a.pdf", ContentType: "application/pdf"}, {Filename: "i.png", ContentType: "image/png"}, {Filename: "x.xlsx"}, {Filename: "e.ics", ContentType: "text/calendar"}}
	s := extract(t, e, nil)
	for _, c := range []struct{ n, v string }{
		{HeadersHasUnsubscribe, "true"}, {HeadersIsBulk, "true"}, {HeadersIsAutomated, "true"}, {HeadersIsReply, "true"}, {HeadersIsForward, "true"},
		{HeadersListID, "weekly.news.example"}, {SenderIsNoReply, "true"}, {AttachmentCount, "4"},
		{AttachmentHasPDF, "true"}, {AttachmentHasImage, "true"}, {AttachmentHasSpreadsheet, "true"}, {AttachmentHasCalendar, "true"},
	} {
		if !s.Has(c.n, c.v) {
			t.Errorf("missing %s=%s", c.n, c.v)
		}
	}
}

type fakeHistory struct{}

func (fakeHistory) SenderHistory(context.Context, string, string) (SenderHistory, error) {
	return SenderHistory{EmailCount: 3, DomainCount: 5, EmailCategories: []string{"finance.bank"}, DomainCategories: []string{"finance.bank", "finance"}}, nil
}
func (fakeHistory) ThreadInfo(_ context.Context, ids []string) (ThreadInfo, error) {
	if len(ids) > 0 && ids[0] == "parent@x" {
		return ThreadInfo{Found: true, Category: "work", Sender: "boss@acme.example"}, nil
	}
	return ThreadInfo{}, nil
}

func TestHistoryAndThreadFeatures(t *testing.T) {
	e := mail("alerts@mybank.example", "Re: hello", "text")
	e.InReplyTo = "parent@x"
	s := extract(t, e, fakeHistory{})
	for _, c := range []struct{ n, v string }{
		{SenderIsKnown, "true"}, {SenderPrevCategory, "finance.bank"}, {DomainPrevCategory, "finance"},
		{ThreadPrevCategory, "work"}, {ThreadPrevSender, "boss@acme.example"},
	} {
		if !s.Has(c.n, c.v) {
			t.Errorf("missing %s=%s: %v", c.n, c.v, s.All())
		}
	}
	// without history those features simply do not exist (testability)
	if extract(t, e, nil).Exists(SenderIsKnown) {
		t.Error("history features must be absent without a History")
	}
}

func TestSetSemantics(t *testing.T) {
	s := NewSet(
		Feature{Name: "a.b", Value: "x", Confidence: 0.5, Source: SourcePattern},
		Feature{Name: "a.b", Value: "x", Confidence: 0.9, Source: SourcePattern}, // higher confidence wins
		Feature{Name: "a.b", Value: "y", Confidence: 0.3, Source: SourcePattern},
	)
	if len(s.Values("a.b")) != 2 || s.Confidence("a.b") != 0.9 || s.Str("a.b") != "x" {
		t.Errorf("set: %v", s.All())
	}
	s.Add(Feature{Name: "a.b", Value: "x", Confidence: 0.1, Source: SourceUser}) // user always wins
	if f, _ := s.First("a.b"); f.Source != SourceUser && s.Values("a.b")[0].Source != SourceUser {
		t.Errorf("user feature must override: %v", s.All())
	}
	if v, known := s.Bool("nope"); v || known {
		t.Error("missing bool is unknown")
	}
	s.Add(Feature{Name: "c.d", Value: "false", Confidence: 1})
	if v, known := s.Bool("c.d"); v || !known {
		t.Error("false is known false")
	}
	c := s.Clone()
	c.Add(Feature{Name: "z.z", Value: "1", Confidence: 1})
	if s.Exists("z.z") {
		t.Error("Clone must be independent")
	}
	names := s.Names()
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Error("Names must be sorted")
		}
	}
}

func TestExtractionIsDeterministic(t *testing.T) {
	e := mail("a@b.example", "Weekly digest newsletter", strings.Repeat("news about tech and more tech stories today ", 5))
	a, b := extract(t, e, nil).All(), extract(t, e, nil).All()
	if len(a) != len(b) {
		t.Fatal("different sizes")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non deterministic at %d: %v vs %v", i, a[i], b[i])
		}
	}
}
