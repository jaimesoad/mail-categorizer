namespace MailCat.Emails;

/// <summary>
/// A parsed mailbox. Both parts are never null, not even for <c>default(Address)</c> (an
/// absent sender), so callers can compare against "" without guarding.
/// </summary>
public readonly struct Address : IEquatable<Address>
{
    readonly string? name;
    readonly string? email;

    public Address(string name, string email)
    {
        this.name = name;
        this.email = email;
    }

    public Address(string email) : this("", email) { }

    public string Name => name ?? "";
    public string Email => email ?? "";

    /// <summary>The host part of the address (after the last '@'), or "".</summary>
    public string Domain
    {
        get
        {
            var e = Email;
            int i = e.LastIndexOf('@');
            return i < 0 ? "" : e[(i + 1)..];
        }
    }

    public bool Equals(Address other) => Name == other.Name && Email == other.Email;
    public override bool Equals(object? obj) => obj is Address a && Equals(a);
    public override int GetHashCode() => HashCode.Combine(Name, Email);
    public static bool operator ==(Address a, Address b) => a.Equals(b);
    public static bool operator !=(Address a, Address b) => !a.Equals(b);
    public override string ToString() => Name == "" ? Email : $"{Name} <{Email}>";
    public void Deconstruct(out string name, out string email) => (name, email) = (Name, Email);
}

/// <summary>Describes an attachment without keeping its payload.</summary>
public sealed record Attachment(string Filename, string ContentType, int Size);

/// <summary>
/// The normalized representation consumed by the rest of the system. Mail
/// sources (.eml files, ...) only have to produce this type.
/// </summary>
public sealed record Email
{
    public string Id { get; init; } = "";
    public string MessageId { get; init; } = "";
    public string ThreadId { get; init; } = "";
    public string InReplyTo { get; init; } = "";
    public IReadOnlyList<string> References { get; init; } = [];
    public Address From { get; init; }
    public IReadOnlyList<Address> To { get; init; } = [];
    public IReadOnlyList<Address> Cc { get; init; } = [];
    public IReadOnlyList<Address> ReplyTo { get; init; } = [];
    public string Subject { get; init; } = "";
    public string BodyText { get; init; } = "";
    public DateTime? ReceivedAt { get; init; }
    /// <summary>Lower-cased header names.</summary>
    public IReadOnlyDictionary<string, IReadOnlyList<string>> Headers { get; init; } =
        new Dictionary<string, IReadOnlyList<string>>();
    public IReadOnlyList<Attachment> Attachments { get; init; } = [];
    public string RawHash { get; init; } = "";

    /// <summary>First value of a header (case-insensitive) or "".</summary>
    public string Header(string name) =>
        Headers.TryGetValue(EmailNormalizer.Lower(name), out var v) && v.Count > 0 ? v[0] : "";

    public bool HasHeader(string name) => Headers.ContainsKey(EmailNormalizer.Lower(name));
}
