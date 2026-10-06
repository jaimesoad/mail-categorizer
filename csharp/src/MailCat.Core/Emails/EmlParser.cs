using System.Security.Cryptography;
using MimeKit;

namespace MailCat.Emails;

/// <summary>Parses RFC 5322 / MIME bytes into an <see cref="Email"/>. No LLM is involved.</summary>
public static class EmlParser
{
    /// <summary>
    /// The only headers copied into the model. Everything else is dropped on purpose:
    /// less data stored, less data leaked.
    /// </summary>
    static readonly string[] KeptHeaders =
    [
        "List-Unsubscribe", "List-Id", "Precedence", "Auto-Submitted", "X-Auto-Response-Suppress",
        "X-Mailer", "Reply-To", "In-Reply-To", "References", "Return-Path", "Content-Type",
        "MIME-Version", "X-Priority", "Importance",
    ];

    public static Email Parse(byte[] raw)
    {
        MimeMessage msg;
        try
        {
            using var ms = new MemoryStream(raw);
            msg = MimeMessage.Load(ms);
        }
        catch (Exception ex)
        {
            throw new FormatException($"parse eml: {ex.Message}", ex);
        }

        var body = msg.TextBody;
        if (string.IsNullOrEmpty(body))
        {
            var html = msg.HtmlBody;
            if (!string.IsNullOrEmpty(html))
            {
                body = HtmlText.Convert(html);
            }
        }

        var from = msg.From.Mailboxes.FirstOrDefault(m => (m.Address ?? "").Contains('@'));
        var headers = new Dictionary<string, IReadOnlyList<string>>();
        foreach (var name in KeptHeaders)
        {
            var values = msg.Headers.Where(h => h.Field.Equals(name, StringComparison.OrdinalIgnoreCase))
                .Select(h => h.Value).ToList();
            if (values.Count > 0) headers[name.ToLowerInvariant()] = values;
        }

        var attachments = new List<Attachment>();
        foreach (var part in msg.BodyParts.OfType<MimePart>())
        {
            // everything that is not the readable body: attachments, inline images, calendar invites
            if (!(part.IsAttachment || part is not TextPart || part.ContentType.IsMimeType("text", "calendar")))
                continue;
            int size = 0;
            try
            {
                if (part.Content != null)
                {
                    using var sink = new MemoryStream();
                    part.Content.DecodeTo(sink);
                    size = (int)sink.Length;
                }
            }
            catch { /* size is informative only */ }
            attachments.Add(new Attachment(part.FileName ?? "", part.ContentType.MimeType, size));
        }

        var e = new Email
        {
            MessageId = msg.MessageId ?? "",
            InReplyTo = msg.InReplyTo ?? "",
            Subject = msg.Subject ?? "",
            BodyText = body ?? "",
            RawHash = Convert.ToHexString(SHA256.HashData(raw)).ToLowerInvariant(),
            References = msg.References.ToList(),
            From = from == null ? default : new Address(from.Name ?? "", from.Address ?? ""),
            To = Mailboxes(msg.To),
            Cc = Mailboxes(msg.Cc),
            ReplyTo = Mailboxes(msg.ReplyTo),
            ReceivedAt = msg.Date == DateTimeOffset.MinValue ? null : msg.Date.UtcDateTime,
            Headers = headers,
            Attachments = attachments,
        };
        return EmailNormalizer.Normalize(e);
    }

    static List<Address> Mailboxes(InternetAddressList list) =>
        list.Mailboxes.Where(m => (m.Address ?? "").Contains('@')).Select(m => new Address(m.Name ?? "", m.Address ?? "")).ToList();

    public static Email ParseFile(string path) => Parse(File.ReadAllBytes(path));
}
