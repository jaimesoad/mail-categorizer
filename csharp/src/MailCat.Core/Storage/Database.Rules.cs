using MailCat.Learning;

namespace MailCat.Storage;

public sealed partial class Database : ILearnedRuleRepo
{
    const string RuleCols = "id,category_id,key,kind,negative,conditions,weight,observations,confirmations,rejections,created_at,updated_at";

    static LearnedRule ScanRule(Microsoft.Data.Sqlite.SqliteDataReader r) => new()
    {
        Id = r.GetString(0), CategoryId = r.GetString(1), Key = r.GetString(2), Kind = r.GetString(3),
        Negative = r.Bool(4), Conditions = Json.Deserialize<List<Cond>>(r.GetString(5)), Weight = r.GetDouble(6),
        Observations = r.GetInt32(7), Confirmations = r.GetInt32(8), Rejections = r.GetInt32(9),
        CreatedAt = ParseTs(r.Str(10)), UpdatedAt = ParseTs(r.Str(11)),
    };

    public List<LearnedRule> RulesByKeys(IReadOnlyList<string> keys)
    {
        if (keys.Count == 0) return [];
        var ph = string.Join(',', keys.Select(_ => "?"));
        return Query($"SELECT {RuleCols} FROM learned_rules WHERE key IN ({ph}) ORDER BY category_id, key, negative",
            ScanRule, [.. keys]);
    }

    public LearnedRule? GetRule(string category, string key, bool negative) =>
        Query($"SELECT {RuleCols} FROM learned_rules WHERE category_id=? AND key=? AND negative=?",
            ScanRule, category, key, negative).FirstOrDefault();

    /// <summary>Upserts by (category, key, negative).</summary>
    public void SaveRule(LearnedRule r)
    {
        if (r.Id == "") r.Id = NewId();
        Exec($"""
            INSERT INTO learned_rules({RuleCols}) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
            ON CONFLICT(category_id,key,negative) DO UPDATE SET weight=excluded.weight, observations=excluded.observations,
              confirmations=excluded.confirmations, rejections=excluded.rejections, updated_at=excluded.updated_at, kind=excluded.kind
            """,
            r.Id, r.CategoryId, r.Key, r.Kind, r.Negative, Json.Serialize(r.Conditions), r.Weight, r.Observations,
            r.Confirmations, r.Rejections, r.CreatedAt, r.UpdatedAt);
        Bump();
    }

    /// <summary>Learned rules, strongest first, optionally for one category.</summary>
    public List<LearnedRule> ListRules(string category = "", int limit = 100)
    {
        var sql = $"SELECT {RuleCols} FROM learned_rules";
        var args = new List<object?>();
        if (category != "")
        {
            sql += " WHERE category_id=?";
            args.Add(category);
        }
        sql += " ORDER BY weight DESC, key, category_id LIMIT ?";
        args.Add(limit <= 0 ? 100 : limit);
        return Query(sql, ScanRule, [.. args]);
    }
}
