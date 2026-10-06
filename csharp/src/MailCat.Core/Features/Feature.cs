namespace MailCat.Features;

/// <summary>Where a feature came from (§6).</summary>
public static class FeatureSource
{
    public const string Header = "header";
    public const string Parser = "parser";
    public const string Pattern = "pattern";
    public const string Classifier = "classifier";
    public const string User = "user";
    public const string History = "history";
}

/// <summary>
/// One observation: hierarchical name, value, confidence, source. Multi-valued features
/// (e.g. subject.tokens) are several Features sharing a name.
/// </summary>
public sealed record Feature(string Name, string Value, double Confidence, string Source)
{
    public static Feature True(string name, double conf, string source) => new(name, "true", conf, source);
}

/// <summary>Names of the features emitted by the built-in extractors.</summary>
public static class FeatureNames
{
    public const string SenderEmail = "sender.email";
    public const string SenderDomain = "sender.domain";
    public const string SenderHost = "sender.host";
    public const string SenderName = "sender.name";
    public const string SenderIsKnown = "sender.is_known";
    public const string SenderDomainIsKnown = "sender.domain.is_known";
    public const string SenderIsFreemail = "sender.is_freemail";
    public const string SenderIsCorporate = "sender.domain.is_corporate";
    public const string SenderIsNoReply = "sender.is_noreply";
    public const string SenderPrevCategory = "sender.previous_category";
    public const string DomainPrevCategory = "sender.domain.previous_category";

    public const string ContentLanguage = "content.language";
    public const string ContentIntent = "content.intent";
    public const string ContentKeyword = "content.keyword";
    public const string ContentContainsAmount = "content.contains_amount";
    public const string ContentContainsInvoice = "content.contains_invoice";
    public const string ContentContainsReceipt = "content.contains_receipt";
    public const string ContentContainsTx = "content.contains_transaction";
    public const string ContentContainsMeeting = "content.contains_meeting";
    public const string ContentContainsNewsletter = "content.contains_newsletter";
    public const string ContentHasInvoiceNumber = "content.has_invoice_number";
    public const string ContentHasOrderNumber = "content.has_order_number";
    public const string ContentHasTxId = "content.has_transaction_id";
    public const string ContentHasPhone = "content.has_phone";
    public const string ContentHasDate = "content.has_date";
    public const string ContentUrlDomain = "content.url_domain";
    public const string ContentUrlCount = "content.url_count";
    public const string SubjectTokens = "subject.tokens";

    public const string AttachmentCount = "attachment.count";
    public const string AttachmentHasPdf = "attachment.has_pdf";
    public const string AttachmentHasImage = "attachment.has_image";
    public const string AttachmentHasSpreadsheet = "attachment.has_spreadsheet";
    public const string AttachmentHasCalendar = "attachment.has_calendar";
    public const string AttachmentTypes = "attachment.types";

    public const string HeadersHasUnsubscribe = "headers.has_unsubscribe";
    public const string HeadersIsReply = "headers.is_reply";
    public const string HeadersIsForward = "headers.is_forward";
    public const string HeadersIsBulk = "headers.is_bulk";
    public const string HeadersIsAutomated = "headers.is_automated";
    public const string HeadersListId = "headers.list_id";

    public const string ThreadPrevCategory = "thread.previous_category";
    public const string ThreadPrevSender = "thread.previous_sender";
}
