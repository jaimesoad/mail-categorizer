using System.Globalization;
using System.Text;

namespace MailCat.Samples;

/// <summary>One synthetic message with its true category.</summary>
public sealed record Sample(string Id, string Category, byte[] Eml);

/// <summary>
/// Generates a reproducible synthetic mailbox (as real RFC 5322 messages) for demos and for
/// the offline evaluation of §35. The corpus mimics the difficulty of a real inbox: several
/// senders per category, bilingual text, and deliberately ambiguous cases (a store saying
/// "payment received").
/// </summary>
public static class SampleGenerator
{
    sealed record Profile(string Category, string[] From, string[] Subjects, string[] Bodies, bool Unsub = false, string ListId = "");

    // {n} expands to a number, {amt} to an amount, {name} to a first name.
    static readonly Profile[] Profiles =
    [
        new("finance.bank",
            ["MyBank Alerts <alerts@mybank.example>", "First Bank <notify@firstbank.example>", "Banco Norte <avisos@banconorte.example>"],
            ["Transaction completed", "Aviso de transacción en tu cuenta", "Your monthly statement is ready", "Movimiento detectado en tu cuenta"],
            [
                "A transaction of {amt} was completed on your account ending in {n4}. If you do not recognize it call your bank.",
                "Se realizó un cargo de {amt} en tu cuenta bancaria terminación {n4}. Referencia: {n}.",
                "Your account statement for the period is available. Closing balance {amt}. Transaction reference {n}.",
            ]),
        new("finance.card",
            ["Visa Services <cards@visaservices.example>", "Tarjeta Oro <tarjeta@oro-card.example>"],
            ["Your credit card statement", "Pago de tu tarjeta de crédito recibido", "Credit card payment due soon"],
            [
                "Your credit card statement is ready. Minimum payment {amt}, due date 15/11/2026. Card ending {n4}.",
                "Recibimos el pago de tu tarjeta de crédito por {amt}. Gracias por tu pago.",
                "Reminder: credit card payment of {amt} due on 20/11/2026. Pay now to avoid fees.",
            ]),
        new("finance.purchase",
            ["ShopMart <orders@shopmart.example>", "BigStore <store@bigstore.example>", "Tienda Online <pedidos@tienda-online.example>"],
            ["Order confirmation #{n}", "Thank you for your purchase", "Recibo de tu compra", "Payment received for order {n}", "Gracias por tu pedido"],
            [
                "Thank you for your order #{n}. Payment received: {amt}. Your receipt is attached below. We will notify you when it ships.",
                "Gracias por tu compra. Pedido {n} por {amt}. Pago recibido correctamente. Conserva este recibo.",
                "Order confirmation: your order {n} totalling {amt} was placed. Payment of {amt} received.",
            ]),
        new("finance.taxes",
            ["Tax Office <noreply@taxoffice.example>", "SAT Notificaciones <avisos@sat-notifica.example>"],
            ["Tax return deadline approaching", "Declaración anual de impuestos", "Factura fiscal disponible"],
            [
                "Your annual tax return deadline is 30/04/2027. Invoice {n} for {amt} is available for download.",
                "Recordatorio: presenta tu declaración anual de impuestos. Factura {n} por {amt} disponible. Fecha límite 30/04/2027.",
            ]),
        new("work",
            ["Laura Gómez <laura.gomez@acmecorp.example>", "Carlos Ruiz <carlos.ruiz@acmecorp.example>", "Dana Lee <dana.lee@acmecorp.example>"],
            ["Re: Q3 report draft", "Proyecto Atlas: avances de la semana", "Quick question about the roadmap", "Revisión del documento de arquitectura"],
            [
                "Hi, I reviewed the draft and left comments on the roadmap section. Can you update the architecture doc before Friday? Thanks, {name}",
                "Hola, te comparto los avances del proyecto Atlas. Necesito tu revisión del documento antes del viernes. Gracias, {name}",
                "Could you send me the latest numbers for the project plan? We need them for the client call. {name}",
            ]),
        new("work.meetings",
            ["Calendar <calendar@acmecorp.example>", "Dana Lee <dana.lee@acmecorp.example>"],
            ["Invitation: Sprint review @ Tue 10:00", "Meeting tomorrow: planning", "Invitación: reunión de equipo", "Reminder: standup moved"],
            [
                "You are invited to the Sprint review. Join the Zoom meeting tomorrow at 10:00. Agenda attached. RSVP please.",
                "Te invitamos a la reunión de equipo mañana a las 10:00. Agenda: avances y bloqueos. Confirma tu asistencia.",
                "The standup has moved to 9:30. Calendar updated, join the Google Meet link in the invitation.",
            ]),
        new("personal",
            ["Mamá <mama.perez@gmail.com>", "Alex <alex.ortiz@outlook.com>", "Sofi <sofia.m@gmail.com>"],
            ["Cena el sábado?", "Fotos del viaje", "hey!", "Feliz cumpleaños"],
            [
                "Hola, ¿vienes a cenar el sábado? Hace mucho que no nos vemos. Te quiero, {name}",
                "Oye te paso las fotos del viaje, estuvo genial. Hablamos pronto, {name}",
                "Hey, are you free this weekend? We are thinking of going to the beach. Let me know! {name}",
            ]),
        new("newsletters",
            ["Tech Weekly <news@techweekly.example>", "El Boletín <hola@elboletin.example>"],
            ["Tech Weekly: top stories this week", "Tu resumen semanal", "Weekly digest #{n}"],
            [
                "Newsletter: this week in tech — five stories you may have missed. View in browser. To stop receiving this newsletter unsubscribe below.",
                "Resumen semanal de noticias: lo más leído de la semana. Ver en el navegador. Boletín número {n}.",
            ], Unsub: true, ListId: "<weekly.techweekly.example>"),
        new("promotions",
            ["Deals <deals@megadeals.example>", "Ofertas <promo@ofertas-hoy.example>", "ShopMart Offers <offers@shopmart.example>"],
            ["50% off everything — today only", "Flash sale ends tonight!", "Ofertas de temporada: hasta 40% de descuento", "Cupón exclusivo para ti"],
            [
                "Don't miss our flash sale! 50% off sitewide. Limited time only. Shop now and get free shipping.",
                "Aprovecha las ofertas: hasta 40% de descuento. Cupón válido solo por hoy. Compra ahora.",
                "Exclusive coupon inside: save {amt} on your next order. Shop now before it ends.",
            ], Unsub: true),
        new("social",
            ["LinkedIn <notifications@linkedin.com>", "Facebook <notification@facebookmail.com>", "Instagram <no-reply@mail.instagram.com>"],
            ["{name} liked your post", "You have a new follower", "{name} commented on your photo", "Solicitud de conexión de {name}"],
            [
                "{name} liked your post. See who else reacted. You have 3 new connection requests.",
                "{name} commented on your photo: 'Great shot!'. Reply to the comment.",
                "{name} started following you. Nuevo seguidor en tu perfil.",
            ]),
        new("shipping",
            ["DHL Tracking <tracking@dhl-notify.example>", "ParcelPost <noreply@parcelpost.example>", "Envíos Rápidos <guias@enviosrapidos.example>"],
            ["Your package is out for delivery", "Paquete en camino — guía {n}", "Delivered: your shipment {n}"],
            [
                "Your package is out for delivery. Tracking number {n}. Estimated arrival today before 18:00.",
                "Tu paquete está en camino. Número de guía {n}. Entrega estimada mañana.",
                "Your shipment {n} was delivered. Package left at the front door.",
            ]),
        new("security",
            ["Accounts Security <security@accounts-secure.example>", "Seguridad <alertas@seguridad-cuentas.example>"],
            ["New sign-in detected on your account", "Restablece tu contraseña", "Security alert: unusual activity", "Your verification code is {n6}"],
            [
                "We noticed a new sign-in to your account from an unknown device. If this was not you, reset your password immediately.",
                "Recibimos una solicitud para restablecer tu contraseña. Si no fuiste tú ignora este mensaje. Código de verificación {n6}.",
                "Security alert: unusual activity detected. Your verification code is {n6}. Never share it.",
            ]),
    ];

    static readonly string[] FirstNames = ["Ana", "Luis", "Marta", "Pedro", "Julia", "Diego", "Sam", "Nora"];

    /// <summary>The category ids covered by the generator.</summary>
    public static List<string> Categories() => Profiles.Select(p => p.Category).OrderBy(c => c, StringComparer.Ordinal).ToList();

    /// <summary>Builds perCategory messages for every category. The output is a pure function of (seed, perCategory).</summary>
    public static List<Sample> Generate(int seed, int perCategory)
    {
        var rng = new Random(seed);
        var baseTime = new DateTime(2026, 3, 1, 8, 0, 0, DateTimeKind.Utc);
        var o = new List<Sample>();
        foreach (var p in Profiles)
        {
            for (int i = 0; i < perCategory; i++)
            {
                var from = p.From[rng.Next(p.From.Length)];
                var subj = Fill(rng, p.Subjects[rng.Next(p.Subjects.Length)]);
                var body = Fill(rng, p.Bodies[rng.Next(p.Bodies.Length)]);
                var id = $"{p.Category.Replace('.', '_')}-{i:D3}";
                var date = baseTime.AddMinutes(o.Count * 37);
                var h = new StringBuilder();
                h.Append($"From: {EncodeHeader(from)}\r\nTo: me@example.net\r\nSubject: {EncodeHeader(subj)}\r\n");
                h.Append($"Date: {date.ToString("ddd, dd MMM yyyy HH:mm:ss", CultureInfo.InvariantCulture)} +0000\r\nMessage-ID: <{id}@sample.local>\r\n");
                if (p.Unsub) h.Append("List-Unsubscribe: <mailto:unsubscribe@sample.local>\r\nPrecedence: bulk\r\n");
                if (p.ListId != "") h.Append($"List-Id: {p.ListId}\r\n");
                h.Append("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n");
                h.Append(body).Append("\r\n");
                o.Add(new Sample(id, p.Category, new UTF8Encoding(false).GetBytes(h.ToString())));
            }
        }
        return o;
    }

    /// <summary>RFC 2047 encoding for headers with non-ASCII text (names, subjects) so the message stays valid ASCII.</summary>
    static string EncodeHeader(string s)
    {
        if (s.All(c => c < 128)) return s;
        // keep the "<addr>" part of a mailbox outside the encoded word
        int lt = s.LastIndexOf('<');
        if (lt > 0 && s.EndsWith('>'))
            return EncodeHeader(s[..lt].TrimEnd()) + " " + s[lt..];
        return "=?utf-8?B?" + Convert.ToBase64String(Encoding.UTF8.GetBytes(s)) + "?=";
    }

    static string Fill(Random rng, string s)
    {
        while (s.Contains('{'))
        {
            if (s.Contains("{amt}")) s = Replace1(s, "{amt}", string.Create(CultureInfo.InvariantCulture, $"${5 + rng.Next(900)}.{rng.Next(100):D2}"));
            else if (s.Contains("{n4}")) s = Replace1(s, "{n4}", rng.Next(10000).ToString("D4"));
            else if (s.Contains("{n6}")) s = Replace1(s, "{n6}", rng.Next(1000000).ToString("D6"));
            else if (s.Contains("{n}")) s = Replace1(s, "{n}", (100000 + rng.Next(900000)).ToString(CultureInfo.InvariantCulture));
            else if (s.Contains("{name}")) s = Replace1(s, "{name}", FirstNames[rng.Next(FirstNames.Length)]);
            else return s;
        }
        return s;
    }

    static string Replace1(string s, string what, string with)
    {
        int i = s.IndexOf(what, StringComparison.Ordinal);
        return s[..i] + with + s[(i + what.Length)..];
    }
}
