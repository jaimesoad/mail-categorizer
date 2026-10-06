using MailCat.Emails;
using MailCat.Features;

namespace MailCat.Storage;

/// <summary>One stored candidate score.</summary>
public sealed class StoredClassification
{
    public string EmailId { get; set; } = "";
    public string CategoryId { get; set; } = "";
    public double Score { get; set; }
    public string Method { get; set; } = "";
    public string Explanation { get; set; } = "";
    public bool Final { get; set; }
    public DateTime CreatedAt { get; set; }
}

public sealed partial class Database
{
    /// <summary>
    /// Stores an email and its features atomically. If an email with the same raw_hash
    /// already exists, nothing is written and (existingId, true) is returned: the pipeline is
    /// idempotent per message. Throws <see cref="ConflictException"/> if the id exists with different content.
    /// </summary>
    public (string Id, bool Existed) InsertEmail(Email e, IEnumerable<Feature> fs, DateTime now)
    {
        string id = "";
        bool existed = false;
        Transaction(() =>
        {
            var existing = Scalar("SELECT id FROM emails WHERE raw_hash=?", e.RawHash) as string;
            if (existing != null)
            {
                id = existing;
                existed = true;
                return;
            }
            if (Count("SELECT COUNT(*) FROM emails WHERE id=?", e.Id) > 0)
                throw new ConflictException("email id already exists with different content");
            Exec("""
                INSERT INTO emails
                (id,message_id,thread_id,sender_email,sender_name,sender_domain,subject,body_text,received_at,raw_hash,created_at)
                VALUES (?,?,?,?,?,?,?,?,?,?,?)
                """,
                e.Id, NullStr(e.MessageId), NullStr(e.ThreadId), e.From.Email, e.From.Name,
                PublicSuffix.RegistrableDomain(e.From.Domain), e.Subject, e.BodyText, e.ReceivedAt, e.RawHash, now);
            id = e.Id;
            InsertFeatures(e.Id, fs);
        });
        return (id, existed);
    }

    void InsertFeatures(string id, IEnumerable<Feature> fs)
    {
        foreach (var f in fs)
        {
            Exec("INSERT OR REPLACE INTO email_features(email_id,feature,value,confidence,source) VALUES (?,?,?,?,?)",
                id, f.Name, f.Value, f.Confidence, f.Source);
        }
    }

    /// <summary>Upserts features without removing existing ones (e.g. answers).</summary>
    public void AddFeatures(string id, params Feature[] fs) => Transaction(() => InsertFeatures(id, fs));

    /// <summary>Loads the stored feature set of an email.</summary>
    public FeatureSet Features(string id)
    {
        var fs = Query("SELECT feature,value,confidence,source FROM email_features WHERE email_id=?",
            r => new Feature(r.GetString(0), r.GetString(1), r.GetDouble(2), r.GetString(3)), id);
        return new FeatureSet(fs);
    }

    /// <summary>Loads an email by id; throws <see cref="NotFoundException"/> if missing.</summary>
    public Email GetEmail(string id)
    {
        var rows = Query("SELECT id,message_id,thread_id,sender_email,sender_name,subject,body_text,received_at,raw_hash FROM emails WHERE id=?",
            r => new Email
            {
                Id = r.GetString(0), MessageId = r.Str(1), ThreadId = r.Str(2),
                From = new Address(r.Str(4), r.Str(3)),
                Subject = r.Str(5), BodyText = r.Str(6),
                ReceivedAt = r.IsDBNull(7) ? null : ParseTs(r.GetString(7)),
                RawHash = r.Str(8),
            }, id);
        return rows.Count > 0 ? rows[0] : throw new NotFoundException();
    }

    /// <summary>
    /// Erases the textual content of an email (subject, body and the features derived from
    /// them) while keeping sender/header features, the final classification and everything
    /// learned from it (§24).
    /// </summary>
    public void PurgeContent(string id, DateTime now)
    {
        Transaction(() =>
        {
            if (Exec("UPDATE emails SET subject='', body_text='', purged_at=? WHERE id=?", now, id) == 0)
                throw new NotFoundException();
            Exec("DELETE FROM email_features WHERE email_id=? AND (feature LIKE 'content.%' OR feature LIKE 'subject.%')", id);
        });
        Bump();
        BumpLabels();
    }

    /// <summary>Applies <see cref="PurgeContent"/> to every email; returns how many were purged.</summary>
    public int PurgeAllContent(DateTime now)
    {
        int n = 0;
        Transaction(() =>
        {
            n = Exec("UPDATE emails SET subject='', body_text='', purged_at=? WHERE purged_at IS NULL", now);
            Exec("DELETE FROM email_features WHERE feature LIKE 'content.%' OR feature LIKE 'subject.%'");
        });
        Bump();
        BumpLabels();
        return n;
    }

    /// <summary>Removes an email and everything attached to it (features, classification, session, interactions). Learned rules are kept.</summary>
    public void DeleteEmail(string id)
    {
        if (Exec("DELETE FROM emails WHERE id=?", id) == 0) throw new NotFoundException();
        Bump();
        BumpLabels();
    }

    /// <summary>The id of an already stored email with this raw hash.</summary>
    public string? EmailIdByHash(string hash) => Scalar("SELECT id FROM emails WHERE raw_hash=?", hash) as string;

    /// <summary>Replaces the stored candidates of an email; the row marked Final carries the explanation.</summary>
    public void SaveClassification(string emailId, IEnumerable<StoredClassification> rows, DateTime now)
    {
        Transaction(() =>
        {
            Exec("DELETE FROM classifications WHERE email_id=?", emailId);
            foreach (var r in rows)
            {
                Exec("INSERT INTO classifications(email_id,category_id,score,method,explanation,is_final,created_at) VALUES (?,?,?,?,?,?,?)",
                    emailId, r.CategoryId, r.Score, r.Method, NullStr(r.Explanation), r.Final, now);
            }
        });
        Bump();
    }

    /// <summary>The final classification row of an email; throws <see cref="NotFoundException"/> if none.</summary>
    public StoredClassification FinalClassification(string emailId)
    {
        var rows = Query("SELECT email_id,category_id,score,method,explanation,created_at FROM classifications WHERE email_id=? AND is_final=1",
            r => new StoredClassification
            {
                EmailId = r.GetString(0), CategoryId = r.GetString(1), Score = r.GetDouble(2), Method = r.GetString(3),
                Explanation = r.Str(4), CreatedAt = ParseTs(r.Str(5)), Final = true,
            }, emailId);
        return rows.Count > 0 ? rows[0] : throw new NotFoundException();
    }

    /// <summary>All stored candidate rows, highest score first.</summary>
    public List<StoredClassification> Candidates(string emailId) =>
        Query("SELECT category_id,score,method,is_final FROM classifications WHERE email_id=? ORDER BY score DESC, category_id",
            r => new StoredClassification { EmailId = emailId, CategoryId = r.GetString(0), Score = r.GetDouble(1), Method = r.GetString(2), Final = r.Bool(3) },
            emailId);

    /// <summary>Email ids, newest first, optionally filtered by final category.</summary>
    public List<string> ListEmailIds(string category = "", int limit = 100)
    {
        var sql = "SELECT e.id FROM emails e";
        var args = new List<object?>();
        if (category != "")
        {
            sql += " JOIN classifications c ON c.email_id=e.id AND c.is_final=1 WHERE c.category_id=?";
            args.Add(category);
        }
        sql += " ORDER BY e.created_at DESC, e.id LIMIT ?";
        args.Add(limit <= 0 ? 100 : limit);
        return Query(sql, r => r.GetString(0), [.. args]);
    }
}
