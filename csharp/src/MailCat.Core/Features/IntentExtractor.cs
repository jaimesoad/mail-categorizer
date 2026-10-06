using System.Text.RegularExpressions;
using MailCat.Emails;
using MailCat.Text;

namespace MailCat.Features;

/// <summary>
/// One weighted signal for an intent. Patterns run over accent-folded, lower-cased text,
/// so they are written without diacritics.
/// </summary>
public sealed record IntentPattern(string Intent, Regex Re, double Weight);

/// <summary>
/// Detects the common intents of §7.4 with bilingual (es/en) patterns. The pattern list
/// is extensible via the constructor.
/// </summary>
public sealed class IntentExtractor : IFeatureExtractor
{
    public const string Transaction = "transaction";
    public const string Receipt = "receipt";
    public const string Invoice = "invoice";
    public const string Statement = "statement";
    public const string Promotion = "promotion";
    public const string Newsletter = "newsletter";
    public const string Notify = "notification";
    public const string Security = "security_alert";
    public const string Meeting = "meeting";
    public const string Personal = "personal_message";
    public const string Shipping = "shipping";
    public const string Password = "password_reset";
    public const string Account = "account_update";
    public const string Social = "social_notification";

    static IntentPattern P(string intent, string re, double w) =>
        new(intent, new Regex(re, RegexOptions.CultureInvariant), w);

    public static List<IntentPattern> DefaultPatterns() =>
    [
        P(Transaction, @"\b(transaction|transacciones?|transaccion)\b", 0.7),
        P(Transaction, @"\b(payment|pago) (was |has been |fue |ha sido )?(received|completed|processed|recibido|realizado|aprobado|procesado|exitoso)\b", 0.75),
        P(Transaction, @"\b(debited|credited|debitado|acreditado|cargo a (tu|su)|cargo en (tu|su)|retiro|deposito|transferencia)\b", 0.6),
        P(Transaction, @"\b(was|has been|fue|ha sido) (completed|approved|declined|completada|aprobada|rechazada)\b", 0.45),
        P(Receipt, @"\b(receipt|recibo|comprobante de (pago|compra))\b", 0.75),
        P(Receipt, @"\b(thank you for your (order|purchase)|gracias por tu (compra|pedido)|gracias por su (compra|pedido))\b", 0.75),
        P(Receipt, @"\b(order confirmation|confirmacion de (pedido|compra|orden)|your order)\b", 0.7),
        P(Invoice, @"\b(invoice|factura|cfdi|nota de credito)\b", 0.75),
        P(Invoice, @"\b(amount due|importe a pagar|total a pagar|fecha de vencimiento|due date|payment due)\b", 0.6),
        P(Statement, @"\b(statement|estado de cuenta|resumen de (cuenta|movimientos)|extracto)\b", 0.8),
        P(Promotion, @"\b\d{1,2}\s?% ?(off|de descuento|dto|descuento)\b", 0.85),
        P(Promotion, @"\b(descuento|oferta|ofertas|promo|promocion|cupon|coupon|flash sale|black friday|cyber monday|clearance|liquidacion)\b", 0.6),
        P(Promotion, @"\b(limited time|tiempo limitado|shop now|comprar ahora|compra ahora|solo por hoy|ultimos dias|free shipping|envio gratis)\b", 0.6),
        P(Newsletter, @"\b(newsletter|boletin|weekly (digest|roundup)|daily digest|resumen semanal|resumen diario|this week in|lo mas leido)\b", 0.8),
        P(Newsletter, @"\b(view (this email )?in (your )?browser|ver en (el )?navegador|ver en linea)\b", 0.5),
        P(Notify, @"\b(notification|notificacion|automated message|mensaje automatico|do not reply|no respondas)\b", 0.5),
        P(Security, @"\b(security alert|alerta de seguridad|new (sign.?in|login)|nuevo inicio de sesion|unusual (activity|sign.?in)|actividad (inusual|sospechosa)|suspicious)\b", 0.85),
        P(Security, @"\b(verification code|codigo de verificacion|one.time (code|password)|codigo de un solo uso|2fa|two.factor)\b", 0.7),
        P(Password, @"\b(reset (your )?password|restablec\w+ (tu |su |la )?contrasena|forgot (your )?password|recuperar (tu |su |la )?contrasena|cambio de contrasena)\b", 0.9),
        P(Account, @"\b(update(d)? (to )?(our |your )?(account|profile|settings|terms|privacy|policy)|actualizacion de (cuenta|terminos|privacidad|politica)|cambios? en (nuestros|tus|los) terminos|terms of service|terminos de servicio)\b", 0.65),
        P(Meeting, @"\b(meeting|reunion|invitation:|invitacion:|calendar|calendario|agenda|zoom|google meet|teams meeting|standup|1:1|junta)\b", 0.6),
        P(Meeting, @"\b(you are invited|has been invited|te invita|estas invitado|rsvp|accept(ed)?|tentative)\b", 0.45),
        P(Shipping, @"\b(tracking( number)?|numero de (guia|rastreo|seguimiento)|shipped|out for delivery|delivered|entregado|shipment|envio|paquete|package|en camino|on its way)\b", 0.7),
        P(Social, @"\b((liked|commented|mentioned|tagged|followed|reacted) (on )?(you|your)|te (siguio|etiqueto|menciono|comento|dio me gusta)|new follower|nuevo seguidor|friend request|solicitud de amistad|connection request|solicitud de conexion|le gusta tu)\b", 0.85),
    ];

    /// <summary>Boosts social_notification when the sender is a known network.</summary>
    static readonly HashSet<string> SocialDomains = new(StringComparer.Ordinal)
    {
        "facebookmail.com", "facebook.com", "linkedin.com", "twitter.com", "x.com", "instagram.com",
        "tiktok.com", "pinterest.com", "reddit.com", "redditmail.com", "discord.com", "mail.instagram.com", "youtube.com",
    };

    readonly List<IntentPattern> patterns;

    public IntentExtractor(params IntentPattern[] extra)
    {
        patterns = DefaultPatterns();
        patterns.AddRange(extra);
    }

    public IEnumerable<Feature> Extract(Email e)
    {
        var subj = TextUtil.Fold(e.Subject);
        var body = TextUtil.Fold(e.Subject + "\n" + e.BodyText);
        var score = new Dictionary<string, double>(StringComparer.Ordinal);
        void Combine(string intent, double w) => score[intent] = 1 - (1 - score.GetValueOrDefault(intent)) * (1 - w);

        foreach (var pt in patterns)
        {
            if (pt.Re.IsMatch(subj))
                Combine(pt.Intent, Math.Min(pt.Weight + 0.15, 0.95)); // subject hits are stronger evidence than body hits
            else if (pt.Re.IsMatch(body))
                Combine(pt.Intent, pt.Weight);
        }

        // structural signals
        bool hasUnsub = e.HasHeader("List-Unsubscribe");
        if (hasUnsub)
        {
            Combine(Newsletter, 0.3);
            Combine(Promotion, 0.2);
        }
        if (e.Attachments.Any(a => a.ContentType == "text/calendar")) Combine(Meeting, 0.8);
        if (SocialDomains.Contains(PublicSuffix.RegistrableDomain(e.From.Domain))) Combine(Social, 0.5);
        if (SenderExtractor.IsNoReply(LocalPart(e))) Combine(Notify, 0.35);

        // personal_message: nothing automated about it.
        var auto = e.Header("Auto-Submitted");
        if (score.Count == 0 && !hasUnsub && !e.HasHeader("List-Id") && (auto == "" || auto == "no")
            && !SenderExtractor.IsNoReply(LocalPart(e)) && e.From.Email != "")
            Combine(Personal, 0.5);

        var list = score.Where(kv => kv.Value >= 0.5)
            .OrderByDescending(kv => kv.Value).ThenBy(kv => kv.Key, StringComparer.Ordinal)
            .Take(3).ToList();
        var o = new List<Feature>();
        foreach (var (intent, s) in list)
        {
            double c = ContentExtractor.Round2(s);
            o.Add(new(FeatureNames.ContentIntent, intent, c, FeatureSource.Pattern));
            switch (intent)
            {
                case Transaction: o.Add(new(FeatureNames.ContentContainsTx, "true", c, FeatureSource.Pattern)); break;
                case Invoice: o.Add(new(FeatureNames.ContentContainsInvoice, "true", c, FeatureSource.Pattern)); break;
                case Receipt: o.Add(new(FeatureNames.ContentContainsReceipt, "true", c, FeatureSource.Pattern)); break;
                case Meeting: o.Add(new(FeatureNames.ContentContainsMeeting, "true", c, FeatureSource.Pattern)); break;
                case Newsletter: o.Add(new(FeatureNames.ContentContainsNewsletter, "true", c, FeatureSource.Pattern)); break;
            }
        }
        return o;
    }

    static string LocalPart(Email e)
    {
        var h = e.From.Domain;
        int n = e.From.Email.Length - h.Length - (h != "" ? 1 : 0);
        return e.From.Email[..Math.Max(0, n)];
    }
}
