using MailCat.Emails;
using MailCat.Features;

namespace MailCat.Tests;

public class FeatureTests
{
    static FeatureSet Extract(Email e, IHistory? h = null) =>
        new(Extractors.Default(h).Extract(EmailNormalizer.Normalize(e)));

    static Email Mail(string from, string subject, string body) => new()
    {
        From = EmailNormalizer.ParseAddress(from), Subject = subject, BodyText = body,
    };

    [Fact]
    public void SpecExampleFeatures()
    {
        var s = Extract(Mail("alerts@mybank.example", "Transaction completed", "A transaction of $85.40 was completed on your account."));
        foreach (var (name, val) in new[]
                 {
                     (FeatureNames.SenderDomain, "mybank.example"), (FeatureNames.SenderEmail, "alerts@mybank.example"),
                     (FeatureNames.ContentContainsAmount, "true"), (FeatureNames.ContentContainsTx, "true"),
                     (FeatureNames.ContentIntent, "transaction"), (FeatureNames.ContentLanguage, "en"),
                     (FeatureNames.SenderIsFreemail, "false"), (FeatureNames.HeadersHasUnsubscribe, "false"),
                 })
            Assert.True(s.Has(name, val), $"missing {name}={val}");
        Assert.True(s.Confidence(FeatureNames.ContentIntent) >= 0.7);
    }

    [Theory]
    [InlineData("alerts.mybank.com", "mybank.com")]
    [InlineData("a.b.mybank.co.uk", "mybank.co.uk")]
    [InlineData("mybank.example", "mybank.example")]
    [InlineData("foo.github.io", "foo.github.io")] // private suffixes count, like the Go implementation
    [InlineData("city.kawasaki.jp", "city.kawasaki.jp")] // exception rule
    [InlineData("a.b.kawasaki.jp", "a.b.kawasaki.jp")] // wildcard rule
    [InlineData("co.uk", "co.uk")] // the host is itself a suffix: falls back to the host
    [InlineData("", "")]
    public void RegistrableDomain(string host, string want) => Assert.Equal(want, PublicSuffix.RegistrableDomain(host));

    [Fact]
    public void FreemailAndSpanishPersonalMessage()
    {
        var s = Extract(Mail("juan@gmail.com", "hola", "¿vienes a cenar el sábado? te espero en casa"));
        Assert.True(s.Has(FeatureNames.SenderIsFreemail, "true"));
        Assert.False(s.Has(FeatureNames.SenderIsCorporate, "true"));
        Assert.True(s.Has(FeatureNames.ContentIntent, "personal_message"));
        Assert.True(s.Has(FeatureNames.ContentLanguage, "es"));
    }

    [Theory]
    [InlineData("s@shop.example", "Order confirmation #123456", "Thank you for your order", "receipt")]
    [InlineData("s@shop.example", "Gracias por tu compra", "Tu pedido 998877 fue recibido", "receipt")]
    [InlineData("b@bank.example", "Tu estado de cuenta", "Resumen de movimientos del mes", "statement")]
    [InlineData("x@x.example", "Invoice 4455", "Amount due: $30. Due date 01/12/2026", "invoice")]
    [InlineData("n@x.example", "50% off today only", "Shop now! Limited time", "promotion")]
    [InlineData("n@x.example", "Weekly digest", "View in browser. Newsletter", "newsletter")]
    [InlineData("s@acc.example", "New sign-in detected", "Unusual activity on your account", "security_alert")]
    [InlineData("s@acc.example", "Restablece tu contraseña", "Para restablecer tu contraseña haz clic", "password_reset")]
    [InlineData("c@x.example", "Invitation: Sprint review", "Join the Zoom meeting tomorrow", "meeting")]
    [InlineData("d@x.example", "Paquete en camino", "Número de guía 123456789", "shipping")]
    [InlineData("notification@facebookmail.com", "Ana commented on your photo", "Ana commented", "social_notification")]
    [InlineData("friend@outlook.com", "hey", "are you free this weekend? let me know", "personal_message")]
    public void IntentsBilingual(string from, string subject, string body, string intent)
    {
        var s = Extract(Mail(from, subject, body));
        Assert.True(s.Has(FeatureNames.ContentIntent, intent), $"{subject}: got [{string.Join(',', s.Values(FeatureNames.ContentIntent).Select(f => f.Value))}]");
    }

    [Fact]
    public void AccentFoldingDoesNotDependOnTheGlobalizationMode()
    {
        Assert.Equal("transaccion", MailCat.Text.TextUtil.Fold("Transacción"));
        Assert.Equal("senor nino", MailCat.Text.TextUtil.Fold("Señor NIÑO"));
        Assert.Equal("cafe", MailCat.Text.TextUtil.Fold("café")); // combining accent
    }

    [Fact]
    public void PatternFeaturesStoreOnlyPresence()
    {
        var s = Extract(Mail("a@b.example", "Factura 12345",
            "Pedido 884211, referencia 99887766. Llámanos +52 55 1234 5678. Detalle en https://pay.shop.example/i?token=SECRET123 el 15/11/2026."));
        foreach (var n in new[]
                 {
                     FeatureNames.ContentHasInvoiceNumber, FeatureNames.ContentHasOrderNumber, FeatureNames.ContentHasTxId,
                     FeatureNames.ContentHasPhone, FeatureNames.ContentHasDate,
                 })
            Assert.True(s.Has(n, "true"), $"missing {n}");
        Assert.True(s.Has(FeatureNames.ContentUrlDomain, "shop.example"));
        foreach (var f in s.All())
        {
            foreach (var leak in new[] { "SECRET123", "5678", "884211", "99887766" })
                Assert.DoesNotContain(leak, f.Value);
        }
    }

    [Fact]
    public void HeaderAndAttachmentFeatures()
    {
        var e = Mail("no-reply@news.example", "Re: Fwd: hello", "body words here for language detection and more words") with
        {
            Headers = new Dictionary<string, IReadOnlyList<string>>
            {
                ["List-Unsubscribe"] = ["<mailto:x@y>"], ["List-Id"] = ["<weekly.news.example>"],
                ["Precedence"] = ["bulk"], ["Auto-Submitted"] = ["auto-generated"],
            },
            InReplyTo = "p@x",
            Attachments =
            [
                new("a.pdf", "application/pdf", 0), new("i.png", "image/png", 0), new("x.xlsx", "", 0), new("e.ics", "text/calendar", 0),
            ],
        };
        var s = Extract(e);
        foreach (var (n, v) in new[]
                 {
                     (FeatureNames.HeadersHasUnsubscribe, "true"), (FeatureNames.HeadersIsBulk, "true"), (FeatureNames.HeadersIsAutomated, "true"),
                     (FeatureNames.HeadersIsReply, "true"), (FeatureNames.HeadersIsForward, "true"), (FeatureNames.HeadersListId, "weekly.news.example"),
                     (FeatureNames.SenderIsNoReply, "true"), (FeatureNames.AttachmentCount, "4"), (FeatureNames.AttachmentHasPdf, "true"),
                     (FeatureNames.AttachmentHasImage, "true"), (FeatureNames.AttachmentHasSpreadsheet, "true"), (FeatureNames.AttachmentHasCalendar, "true"),
                 })
            Assert.True(s.Has(n, v), $"missing {n}={v}");
    }

    sealed class FakeHistory : IHistory
    {
        public SenderHistory SenderHistory(string e, string d) => new()
        {
            EmailCount = 3, DomainCount = 5, EmailCategories = ["finance.bank"], DomainCategories = ["finance.bank", "finance"],
        };

        public ThreadInfo ThreadInfo(IReadOnlyList<string> ids) =>
            ids.Count > 0 && ids[0] == "parent@x" ? new ThreadInfo(true, "work", "boss@acme.example") : MailCat.Features.ThreadInfo.None;
    }

    [Fact]
    public void HistoryAndThreadFeatures()
    {
        var e = Mail("alerts@mybank.example", "Re: hello", "text") with { InReplyTo = "parent@x" };
        var s = Extract(e, new FakeHistory());
        foreach (var (n, v) in new[]
                 {
                     (FeatureNames.SenderIsKnown, "true"), (FeatureNames.SenderPrevCategory, "finance.bank"),
                     (FeatureNames.DomainPrevCategory, "finance"), (FeatureNames.ThreadPrevCategory, "work"),
                     (FeatureNames.ThreadPrevSender, "boss@acme.example"),
                 })
            Assert.True(s.Has(n, v), $"missing {n}={v}");
        Assert.False(Extract(e).Exists(FeatureNames.SenderIsKnown), "history features must be absent without a History");
    }

    [Fact]
    public void SetSemantics()
    {
        var s = new FeatureSet(
        [
            new Feature("a.b", "x", 0.5, FeatureSource.Pattern),
            new Feature("a.b", "x", 0.9, FeatureSource.Pattern), // higher confidence wins
            new Feature("a.b", "y", 0.3, FeatureSource.Pattern),
        ]);
        Assert.Equal(2, s.Values("a.b").Count);
        Assert.Equal(0.9, s.Confidence("a.b"));
        Assert.Equal("x", s.Str("a.b"));

        s.Add(new Feature("a.b", "x", 0.1, FeatureSource.User)); // user always wins
        Assert.Contains(s.Values("a.b"), f => f.Source == FeatureSource.User);
        s.Add(new Feature("a.b", "x", 0.99, FeatureSource.Pattern)); // and is never overridden by a pattern
        Assert.Contains(s.Values("a.b"), f => f.Source == FeatureSource.User);

        Assert.Equal((false, false), s.Bool("nope"));
        s.Add(new Feature("c.d", "false", 1, ""));
        Assert.Equal((false, true), s.Bool("c.d"));

        var c = s.Clone();
        c.Add(new Feature("z.z", "1", 1, ""));
        Assert.False(s.Exists("z.z"), "Clone must be independent");
        Assert.Equal(s.Names().OrderBy(n => n, StringComparer.Ordinal), s.Names());
    }

    [Fact]
    public void ExtractionIsDeterministic()
    {
        var e = Mail("a@b.example", "Weekly digest newsletter", string.Concat(Enumerable.Repeat("news about tech and more tech stories today ", 5)));
        Assert.Equal(Extract(e).All(), Extract(e).All());
    }
}
