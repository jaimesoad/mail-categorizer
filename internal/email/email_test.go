package email

import (
	"strings"
	"testing"
)

const multipart = "From: =?UTF-8?B?QmFuY28gTm9ydGU=?= <Avisos@BancoNorte.example>\r\n" +
	"To: Yo <me@example.net>, otro@example.net\r\n" +
	"Subject: =?UTF-8?Q?Transacci=C3=B3n_completada?=\r\n" +
	"Date: Mon, 05 Oct 2026 10:00:00 +0000\r\n" +
	"Message-ID: <abc123@banconorte.example>\r\n" +
	"In-Reply-To: <prev@x.example>\r\n" +
	"List-Unsubscribe: <mailto:u@banconorte.example>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"B\"\r\n\r\n" +
	"--B\r\nContent-Type: multipart/alternative; boundary=\"A\"\r\n\r\n" +
	"--A\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
	"Se realiz=C3=B3 un cargo de $85.40.\r\n\r\n\r\n\r\nSaludos   \r\n" +
	"--A\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Se realizó un cargo</p>\r\n--A--\r\n" +
	"--B\r\nContent-Type: application/pdf; name=\"estado.pdf\"\r\nContent-Disposition: attachment; filename=\"estado.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--B--\r\n"

func TestParseEMLMultipartCharsetsAndAttachments(t *testing.T) {
	e, err := ParseEML([]byte(multipart))
	if err != nil {
		t.Fatal(err)
	}
	if e.From.Email != "avisos@bancon orte.example"[:0]+"avisos@banconorte.example" || e.From.Name != "Banco Norte" {
		t.Errorf("from = %+v", e.From)
	}
	if e.Subject != "Transacción completada" {
		t.Errorf("subject = %q", e.Subject)
	}
	if !strings.Contains(e.BodyText, "Se realizó un cargo de $85.40.") || strings.Contains(e.BodyText, "<p>") {
		t.Errorf("body = %q", e.BodyText)
	}
	if strings.Contains(e.BodyText, "\n\n\n") || strings.HasSuffix(e.BodyText, " ") {
		t.Errorf("body not normalized: %q", e.BodyText)
	}
	if e.MessageID != "abc123@banconorte.example" || e.InReplyTo != "prev@x.example" {
		t.Errorf("ids: %q %q", e.MessageID, e.InReplyTo)
	}
	if len(e.To) != 2 || e.To[0].Email != "me@example.net" {
		t.Errorf("to = %+v", e.To)
	}
	if len(e.Attach) != 1 || e.Attach[0].Filename != "estado.pdf" {
		t.Errorf("attachments = %+v", e.Attach)
	}
	if !e.HasHeader("List-Unsubscribe") || e.ReceivedAt.Year() != 2026 {
		t.Errorf("headers/date: %v %v", e.Headers, e.ReceivedAt)
	}
	if !strings.HasPrefix(e.ID, "em_") || len(e.RawHash) != 64 {
		t.Errorf("id/hash: %q %q", e.ID, e.RawHash)
	}
}

func TestParseEMLIsDeterministicAndHashDedupes(t *testing.T) {
	a, _ := ParseEML([]byte(multipart))
	b, _ := ParseEML([]byte(multipart))
	if a.RawHash != b.RawHash || a.ID != b.ID {
		t.Fatal("same bytes must give same hash and id")
	}
	c, _ := ParseEML([]byte(strings.Replace(multipart, "85.40", "99.99", 1)))
	if c.RawHash == a.RawHash {
		t.Fatal("different content must give a different hash")
	}
}

func TestParseEMLToleratesGarbage(t *testing.T) {
	// no headers at all: must not panic; either an error or an empty email is fine
	if e, err := ParseEML([]byte("just some text without headers")); err == nil && e.From.Email != "" {
		t.Errorf("invented a sender: %+v", e.From)
	}
	if _, err := ParseEML(nil); err == nil {
		t.Log("empty input accepted")
	}
}

func TestNormalize(t *testing.T) {
	in := Email{
		From: Address{Name: " Ana ", Email: " ANA@Example.COM "}, Subject: "  Re:   hola \n mundo ",
		BodyText: "a\r\nb  \r\n\r\n\r\n\r\nc", MessageID: " <id@x> ", References: []string{"<r1@x>", " ", "<r2@x>"},
		Headers: map[string][]string{"List-ID": {"x"}},
	}
	out := Normalize(in, 0)
	if out.From.Email != "ana@example.com" || out.From.Name != "Ana" {
		t.Errorf("from: %+v", out.From)
	}
	if out.Subject != "Re: hola mundo" {
		t.Errorf("subject: %q", out.Subject)
	}
	if out.BodyText != "a\nb\n\nc" {
		t.Errorf("body: %q", out.BodyText)
	}
	if out.MessageID != "id@x" || len(out.References) != 2 || out.References[0] != "r1@x" {
		t.Errorf("ids: %q %v", out.MessageID, out.References)
	}
	if !out.HasHeader("list-id") {
		t.Error("header names must be lower-cased")
	}
	if in.From.Email != " ANA@Example.COM " {
		t.Error("Normalize must not mutate its input")
	}
	long := Normalize(Email{BodyText: strings.Repeat("é", 100)}, 10)
	if len([]rune(long.BodyText)) != 10 {
		t.Errorf("body not truncated by runes: %d", len([]rune(long.BodyText)))
	}
	if Normalize(Email{Subject: "x"}, 0).ID == "" {
		t.Error("ID must be generated")
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]string{
		"Ana <ANA@x.com>": "ana@x.com", "b@y.org": "b@y.org", "garbage": "",
	} {
		if got := ParseAddress(in).Email; got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
	if (Address{Email: "a@sub.example.com"}).Domain() != "sub.example.com" {
		t.Error("Domain")
	}
}
