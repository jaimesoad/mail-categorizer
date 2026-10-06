using MailCat.Emails;

namespace MailCat.Tests;

public class EmailTests
{
    const string Multipart =
        "From: =?UTF-8?B?QmFuY28gTm9ydGU=?= <Avisos@BancoNorte.example>\r\n" +
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
        "--B\r\nContent-Type: application/pdf; name=\"estado.pdf\"\r\nContent-Disposition: attachment; filename=\"estado.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--B--\r\n";

    static Email Parse(string s) => EmlParser.Parse(System.Text.Encoding.UTF8.GetBytes(s));

    [Fact]
    public void ParseMultipartCharsetsAndAttachments()
    {
        var e = Parse(Multipart);
        Assert.Equal("avisos@banconorte.example", e.From.Email);
        Assert.Equal("Banco Norte", e.From.Name);
        Assert.Equal("Transacción completada", e.Subject);
        Assert.Contains("Se realizó un cargo de $85.40.", e.BodyText);
        Assert.DoesNotContain("<p>", e.BodyText);
        Assert.DoesNotContain("\n\n\n", e.BodyText);
        Assert.False(e.BodyText.EndsWith(' '));
        Assert.Equal("abc123@banconorte.example", e.MessageId);
        Assert.Equal("prev@x.example", e.InReplyTo);
        Assert.Equal(2, e.To.Count);
        Assert.Equal("me@example.net", e.To[0].Email);
        var att = Assert.Single(e.Attachments);
        Assert.Equal("estado.pdf", att.Filename);
        Assert.True(e.HasHeader("List-Unsubscribe"));
        Assert.Equal(2026, e.ReceivedAt!.Value.Year);
        Assert.StartsWith("em_", e.Id);
        Assert.Equal(64, e.RawHash.Length);
    }

    [Fact]
    public void ParseIsDeterministicAndHashDedupes()
    {
        var a = Parse(Multipart);
        var b = Parse(Multipart);
        Assert.Equal(a.RawHash, b.RawHash);
        Assert.Equal(a.Id, b.Id);
        var c = Parse(Multipart.Replace("85.40", "99.99"));
        Assert.NotEqual(a.RawHash, c.RawHash);
    }

    [Fact]
    public void HtmlOnlyMessageFallsBackToText()
    {
        var e = Parse("From: a@b.example\r\nSubject: x\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><p>Hola <b>mundo</b></p><script>evil()</script></body></html>");
        Assert.Contains("Hola mundo", e.BodyText);
        Assert.DoesNotContain("evil", e.BodyText);
    }

    [Fact]
    public void ParseToleratesGarbage()
    {
        try
        {
            var e = Parse("just some text without headers");
            Assert.Equal("", e.From.Email); // never invent a sender
        }
        catch (FormatException) { /* an error is fine too */ }
    }

    [Fact]
    public void NormalizeCanonicalizes()
    {
        var input = new Email
        {
            From = new Address(" Ana ", " ANA@Example.COM "), Subject = "  Re:   hola \n mundo ",
            BodyText = "a\r\nb  \r\n\r\n\r\n\r\nc", MessageId = " <id@x> ", References = ["<r1@x>", " ", "<r2@x>"],
            Headers = new Dictionary<string, IReadOnlyList<string>> { ["List-ID"] = ["x"] },
        };
        var o = EmailNormalizer.Normalize(input);
        Assert.Equal("ana@example.com", o.From.Email);
        Assert.Equal("Ana", o.From.Name);
        Assert.Equal("Re: hola mundo", o.Subject);
        Assert.Equal("a\nb\n\nc", o.BodyText);
        Assert.Equal("id@x", o.MessageId);
        Assert.Equal(["r1@x", "r2@x"], o.References);
        Assert.True(o.HasHeader("list-id"), "header names must be lower-cased");
        Assert.Equal(" ANA@Example.COM ", input.From.Email);

        var long_ = EmailNormalizer.Normalize(new Email { BodyText = new string('é', 100) }, 10);
        Assert.Equal(10, long_.BodyText.Length);
        Assert.NotEqual("", EmailNormalizer.Normalize(new Email { Subject = "x" }).Id);
    }

    [Theory]
    [InlineData("Ana <ANA@x.com>", "ana@x.com")]
    [InlineData("b@y.org", "b@y.org")]
    [InlineData("garbage", "")]
    public void ParseAddress(string input, string want) => Assert.Equal(want, EmailNormalizer.ParseAddress(input).Email);

    [Fact]
    public void AddressDomain() => Assert.Equal("sub.example.com", new Address("a@sub.example.com").Domain);
}
