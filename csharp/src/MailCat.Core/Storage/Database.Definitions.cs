using MailCat.Categories;
using MailCat.Questions;

namespace MailCat.Storage;

public sealed partial class Database
{
    /// <summary>
    /// Upserts the configured categories. Categories removed from the configuration are
    /// disabled, never deleted, so history stays intact.
    /// </summary>
    public void SyncCategories(IEnumerable<Category> cats)
    {
        var list = cats.ToList();
        var byId = list.ToDictionary(c => c.Id, StringComparer.Ordinal);
        int Depth(Category c)
        {
            int d = 0;
            for (var p = c.ParentId; p != "" && byId.TryGetValue(p, out var pc); p = pc.ParentId) d++;
            return d;
        }
        Transaction(() =>
        {
            // parents first so the FK is satisfied
            foreach (var c in list.OrderBy(Depth).ThenBy(c => c.Id, StringComparer.Ordinal))
            {
                Exec("""
                    INSERT INTO categories(id,parent_id,name,description,enabled) VALUES (?,?,?,?,?)
                    ON CONFLICT(id) DO UPDATE SET parent_id=excluded.parent_id, name=excluded.name, description=excluded.description, enabled=excluded.enabled
                    """, c.Id, NullStr(c.ParentId), c.Name, NullStr(c.Description), c.Enabled);
            }
            var stale = Query("SELECT id FROM categories WHERE enabled=1", r => r.GetString(0))
                .Where(id => !byId.ContainsKey(id)).ToList();
            foreach (var id in stale) Exec("UPDATE categories SET enabled=0 WHERE id=?", id);
        });
    }

    /// <summary>Every category row (including disabled ones).</summary>
    public List<Category> StoredCategories() =>
        Query("SELECT id,parent_id,name,description,enabled FROM categories ORDER BY id",
            r => new Category(r.GetString(0), r.Str(1), r.GetString(2), r.Str(3), r.Bool(4)));

    /// <summary>
    /// Upserts a question and its answer mapping (§5.5/5.6). When keepState is true an
    /// existing row's enabled/validated flags are preserved.
    /// </summary>
    public void SaveQuestion(Question q, bool keepState)
    {
        var def = Json.Serialize(q);
        Transaction(() =>
        {
            var sql = """
                INSERT INTO questions(id,text,feature,type,enabled,source,validated,definition) VALUES (?,?,?,?,?,?,?,?)
                ON CONFLICT(id) DO UPDATE SET text=excluded.text, feature=excluded.feature, type=excluded.type, source=excluded.source, definition=excluded.definition
                """;
            if (!keepState) sql += ", enabled=excluded.enabled, validated=excluded.validated";
            Exec(sql, q.Id, q.Text, q.Feature, q.Type, !q.Disabled, q.Source, q.Validated, def);
            Exec("DELETE FROM question_answers WHERE question_id=?", q.Id);
            foreach (var a in q.Answers)
            {
                Exec("INSERT INTO question_answers(id,question_id,answer_value,feature,feature_value) VALUES (?,?,?,?,?)",
                    q.Id + ":" + a.Id, q.Id, a.Id, q.Feature, a.FeatureValue);
            }
        });
    }

    /// <summary>Approves/rejects (validated) and enables/disables a question.</summary>
    public void SetQuestionState(string id, bool validated, bool enabled)
    {
        if (Exec("UPDATE questions SET validated=?, enabled=? WHERE id=?", validated, enabled, id) == 0)
            throw new NotFoundException();
        Bump();
    }

    /// <summary>Every stored question with its current state.</summary>
    public List<Question> Questions() =>
        Query("SELECT definition,enabled,validated FROM questions ORDER BY id", r =>
        {
            var q = Json.Deserialize<Question>(r.GetString(0));
            q.Disabled = !r.Bool(1);
            q.Validated = r.Bool(2);
            return q;
        });
}
