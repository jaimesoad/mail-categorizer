using MailCat.Categories;
using MailCat.Features;
using MailCat.Questions;
using MailCat.Similarity;

namespace MailCat.Storage;

/// <summary>The best known truth about an email's category.</summary>
/// <param name="Source">"user" (explicit human verdict) or "answers" (resolved through the user's answers).</param>
public sealed record Label(string Category, string Source);

public sealed partial class Database : IHistory, IQuestionStatsProvider
{
    /// <summary>
    /// The trusted labels of all emails. Fully automatic classifications are deliberately
    /// excluded: learning from the system's own output would only reinforce its mistakes.
    /// </summary>
    public Dictionary<string, Label> Labels()
    {
        ulong v = Version;
        if (Cached<Dictionary<string, Label>>("labels") is { } c) return c;

        var o = new Dictionary<string, Label>(StringComparer.Ordinal);
        foreach (var (id, cat) in Query(
                     "SELECT email_id, final_category FROM sessions WHERE resolved_by='answers' AND final_category IS NOT NULL AND final_category!='unknown'",
                     r => (r.GetString(0), r.GetString(1))))
            o[id] = new Label(cat, "answers");

        // later interactions override earlier ones
        foreach (var (id, cat) in Query(
                     "SELECT email_id, resulting_category FROM interactions WHERE kind IN ('confirm','correct','manual') AND resulting_category IS NOT NULL ORDER BY created_at, rowid",
                     r => (r.GetString(0), r.GetString(1))))
            o[id] = new Label(cat, "user");

        SetCached("labels", o, v);
        return o;
    }

    /// <summary>
    /// How often each question was shown/answered and, per category, how users answered when
    /// the final category was known.
    /// </summary>
    public Dictionary<string, QuestionStats> QuestionStats()
    {
        ulong v = Version;
        if (Cached<Dictionary<string, QuestionStats>>("qstats") is { } c) return c;

        var labels = Labels();
        var o = new Dictionary<string, QuestionStats>(StringComparer.Ordinal);
        var rows = Query(
            "SELECT question_id, email_id, kind, COALESCE(answer,'') FROM interactions WHERE kind IN ('answer','skip') AND question_id IS NOT NULL",
            r => (Qid: r.GetString(0), Eid: r.GetString(1), Kind: r.GetString(2), Ans: r.GetString(3)));
        foreach (var (qid, eid, kind, ans) in rows)
        {
            if (!o.TryGetValue(qid, out var st)) o[qid] = st = new QuestionStats();
            st.Shown++;
            if (kind != InteractionKind.Answer) continue;
            st.Answered++;
            if (labels.TryGetValue(eid, out var lab) && lab.Source == "user") // only explicit labels: no self-reinforcement
            {
                if (!st.Counts.TryGetValue(lab.Category, out var row)) st.Counts[lab.Category] = row = [];
                row[ans] = row.GetValueOrDefault(ans) + 1;
            }
        }
        SetCached("qstats", o, v);
        return o;
    }

    /// <summary>The labeled emails (with remaining text) for similarity.</summary>
    public List<Doc> LabeledDocs(int maxChars)
    {
        var labels = Labels();
        var rows = Query("SELECT id, COALESCE(subject,''), COALESCE(body_text,'') FROM emails WHERE body_text IS NOT NULL AND body_text!='' ORDER BY id",
            r => (Id: r.GetString(0), Subj: r.GetString(1), Body: r.GetString(2)));
        var docs = new List<Doc>();
        foreach (var (id, subj, body0) in rows)
        {
            if (!labels.TryGetValue(id, out var lab)) continue;
            var body = maxChars > 0 && body0.Length > maxChars ? body0[..maxChars] : body0;
            double trust = lab.Source == "answers" ? 0.7 : 1.0;
            docs.Add(new Doc(id, lab.Category, subj + " " + subj + " " + body, trust));
        }
        return docs;
    }

    // ── IHistory ──────────────────────────────────────────────────────────────────────

    public SenderHistory SenderHistory(string senderEmail, string senderDomain) => new()
    {
        EmailCount = (int)Count("SELECT COUNT(*) FROM emails WHERE sender_email=?", senderEmail),
        DomainCount = (int)Count("SELECT COUNT(*) FROM emails WHERE sender_domain=?", senderDomain),
        EmailCategories = TopCategories("e.sender_email=?", senderEmail),
        DomainCategories = TopCategories("e.sender_domain=?", senderDomain),
    };

    List<string> TopCategories(string where, string arg) =>
        Query($"""
            SELECT c.category_id FROM emails e JOIN classifications c ON c.email_id=e.id AND c.is_final=1
            WHERE {where} AND c.category_id!=? GROUP BY c.category_id ORDER BY COUNT(*) DESC, c.category_id LIMIT 3
            """, r => r.GetString(0), arg, CategoryTree.UnknownId);

    /// <summary>The first of ids (ordered by proximity) that is a known message, with its final category and sender.</summary>
    public ThreadInfo ThreadInfo(IReadOnlyList<string> ids)
    {
        foreach (var id in ids)
        {
            var rows = Query("""
                SELECT e.sender_email, (SELECT category_id FROM classifications WHERE email_id=e.id AND is_final=1)
                FROM emails e WHERE e.message_id=? LIMIT 1
                """, r => (Sender: r.Str(0), Cat: r.Str(1)), id.Trim('<', '>'));
            if (rows.Count == 0) continue;
            var cat = rows[0].Cat == CategoryTree.UnknownId ? "" : rows[0].Cat;
            return new ThreadInfo(true, cat, rows[0].Sender);
        }
        return MailCat.Features.ThreadInfo.None;
    }
}
