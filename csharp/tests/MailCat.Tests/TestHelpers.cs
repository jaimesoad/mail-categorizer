using MailCat.Config;
using MailCat.Emails;
using MailCat.Engine;
using MailCat.Rules;
using MailCat.Storage;

namespace MailCat.Tests;

static class TestHelpers
{
    public static readonly DateTime Now = new(2026, 10, 5, 12, 0, 0, DateTimeKind.Utc);

    public static Email Mail(string id, string from, string subject, string body) => new()
    {
        Id = id, From = EmailNormalizer.ParseAddress(from), Subject = subject, BodyText = body,
        ReceivedAt = new DateTime(2026, 10, 1, 9, 0, 0, DateTimeKind.Utc),
    };

    public static Condition Leaf(string feature, string op, object? value) => new() { Feature = feature, Op = op, Value = value };

    public static Condition Eq(string feature, object value) => new() { Feature = feature, Value = value };

    /// <summary>An engine over an in-memory database with the default config plus a few bank/purchase rules.</summary>
    public sealed class EngineFixture : IDisposable
    {
        public required MailCatEngine Engine { get; init; }
        public required Database Db { get; init; }
        public required BuiltConfig Built { get; init; }
        public void Dispose() => Db.Dispose();
    }

    public static EngineFixture TestEngine(Action<MailCatConfig>? mutate = null)
    {
        var cfg = MailCatConfig.LoadOrDefault("");
        cfg.Rules.AddRange(
        [
            new Rule
            {
                Id = "my-bank", Category = "finance.bank", Weight = 0.7, Reason = "El remitente coincide con un dominio bancario conocido.",
                When = Leaf("sender.domain", "in", new List<object> { "mybank.example" }),
            },
            new Rule
            {
                Id = "my-bank-tx", Category = "finance.bank", Weight = 0.6, Reason = "Contiene una transacción de un banco conocido.",
                When = new Condition
                {
                    All =
                    [
                        Leaf("sender.domain", "in", new List<object> { "mybank.example" }),
                        Eq("content.contains_transaction", "true"),
                    ],
                },
            },
            // ambiguous "payment received": equally bank-ish and purchase-ish (spec §39)
            new Rule { Id = "pay-bank", Category = "finance.bank", Weight = 0.43, When = Eq("subject.tokens", "payment") },
            new Rule { Id = "pay-purchase", Category = "finance.purchase", Weight = 0.41, When = Eq("subject.tokens", "payment") },
        ]);
        mutate?.Invoke(cfg);
        var built = cfg.Build();
        var db = Database.Open(":memory:");
        var clock = Now;
        var engine = new MailCatEngine(db, built, () => clock = clock.AddSeconds(1));
        return new EngineFixture { Engine = engine, Db = db, Built = built };
    }
}
