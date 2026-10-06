using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using Microsoft.Data.Sqlite;

namespace MailCat.Storage;

/// <summary>
/// The SQLite persistence layer (§5): emails, features, categories, classifications,
/// questions, sessions, interactions and learned rules, with versioned migrations. All
/// access goes through a single connection (a CLI process is single-threaded, and it keeps
/// ":memory:" databases coherent).
/// </summary>
public sealed partial class Database : IDisposable
{
    readonly SqliteConnection conn;
    SqliteTransaction? tx;
    ulong version;
    ulong labelVersion;
    Dictionary<string, object> cache = [];

    public string Path { get; }

    Database(string path, SqliteConnection conn)
    {
        Path = path;
        this.conn = conn;
    }

    /// <summary>Opens (creating if needed) the database at path and migrates it. Use ":memory:" for an ephemeral database.</summary>
    public static Database Open(string path)
    {
        var cs = new SqliteConnectionStringBuilder
        {
            DataSource = path,
            Mode = SqliteOpenMode.ReadWriteCreate,
            ForeignKeys = true,
            Pooling = false,
        };
        var conn = new SqliteConnection(cs.ToString());
        conn.Open();
        var db = new Database(path, conn);
        try
        {
            db.Exec("PRAGMA busy_timeout=5000");
            if (path != ":memory:")
            {
                db.Scalar("PRAGMA journal_mode=WAL");
                db.Exec("PRAGMA synchronous=NORMAL");
            }
            db.Migrate();
        }
        catch
        {
            conn.Dispose();
            throw;
        }
        return db;
    }

    public void Dispose()
    {
        tx?.Dispose();
        conn.Dispose();
    }

    void Migrate()
    {
        Exec("CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
        int cur = int.TryParse(Scalar("SELECT value FROM schema_meta WHERE key='version'")?.ToString(), out var v) ? v : 0;
        if (cur > Migrations.All.Length)
            throw new InvalidOperationException($"database schema version {cur} is newer than this binary ({Migrations.All.Length})");
        for (int i = cur; i < Migrations.All.Length; i++)
        {
            Transaction(() =>
            {
                try { Exec(Migrations.All[i]); }
                catch (SqliteException ex) { throw new InvalidOperationException($"migration {i + 1}: {ex.Message}", ex); }
                Exec("INSERT INTO schema_meta(key,value) VALUES('version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                    (i + 1).ToString(CultureInfo.InvariantCulture));
            });
        }
    }

    // ── low level helpers ─────────────────────────────────────────────────────────────

    /// <summary>Rewrites positional "?" placeholders (outside string literals) to named parameters.</summary>
    static string Bind(SqliteCommand cmd, string sql, object?[] args)
    {
        if (args.Length == 0) return sql;
        var sb = new StringBuilder(sql.Length + 8);
        int n = 0;
        bool inString = false;
        foreach (var ch in sql)
        {
            if (ch == '\'') inString = !inString;
            if (ch == '?' && !inString)
            {
                sb.Append("$p").Append(n++);
            }
            else sb.Append(ch);
        }
        if (n != args.Length) throw new ArgumentException($"SQL expects {n} parameters, got {args.Length}");
        for (int i = 0; i < args.Length; i++) cmd.Parameters.AddWithValue("$p" + i, ToDb(args[i]));
        return sb.ToString();
    }

    static object ToDb(object? v) => v switch
    {
        null => DBNull.Value,
        bool b => b ? 1 : 0,
        DateTime d => Ts(d),
        _ => v,
    };

    SqliteCommand Command(string sql, object?[] args)
    {
        var cmd = conn.CreateCommand();
        cmd.Transaction = tx;
        cmd.CommandText = Bind(cmd, sql, args);
        return cmd;
    }

    /// <summary>Executes a non-query and returns the affected rows.</summary>
    public int Exec(string sql, params object?[] args)
    {
        using var cmd = Command(sql, args);
        return cmd.ExecuteNonQuery();
    }

    public object? Scalar(string sql, params object?[] args)
    {
        using var cmd = Command(sql, args);
        var o = cmd.ExecuteScalar();
        return o is DBNull ? null : o;
    }

    public long Count(string sql, params object?[] args) => Convert.ToInt64(Scalar(sql, args) ?? 0L, CultureInfo.InvariantCulture);

    /// <summary>Runs a query and maps every row. Never issue another query from inside <paramref name="map"/>.</summary>
    public List<T> Query<T>(string sql, Func<SqliteDataReader, T> map, params object?[] args)
    {
        using var cmd = Command(sql, args);
        using var r = cmd.ExecuteReader();
        var o = new List<T>();
        while (r.Read()) o.Add(map(r));
        return o;
    }

    /// <summary>Runs <paramref name="fn"/> inside a transaction (re-entrant: nested calls join the outer one).</summary>
    public void Transaction(Action fn)
    {
        if (tx != null)
        {
            fn();
            return;
        }
        tx = conn.BeginTransaction();
        try
        {
            fn();
            tx.Commit();
        }
        catch
        {
            tx.Rollback();
            throw;
        }
        finally
        {
            tx.Dispose();
            tx = null;
        }
    }

    // ── versions and caches ───────────────────────────────────────────────────────────

    /// <summary>A counter that changes whenever knowledge-relevant data changes.</summary>
    public ulong Version => version;

    /// <summary>Changes only when the trusted labels (or email text) change: it keys the similarity index cache.</summary>
    public ulong LabelVersion => labelVersion;

    internal void BumpLabels() => labelVersion++;

    internal void Bump()
    {
        version++;
        cache = [];
    }

    internal T? Cached<T>(string key) where T : class => cache.TryGetValue(key, out var v) ? (T)v : null;

    internal void SetCached(string key, object v, ulong atVersion)
    {
        if (version == atVersion) cache[key] = v;
    }

    /// <summary>Writes a consistent copy of the database to dst (VACUUM INTO).</summary>
    public void Backup(string dst)
    {
        if (dst.Contains('\0')) throw new ArgumentException("invalid path");
        Exec("VACUUM INTO ?", dst);
    }

    /// <summary>A random 128-bit hex id.</summary>
    public static string NewId() => Convert.ToHexString(RandomNumberGenerator.GetBytes(16)).ToLowerInvariant();

    // ── value conversion ──────────────────────────────────────────────────────────────

    internal static string Ts(DateTime t) => t.ToUniversalTime().ToString("O", CultureInfo.InvariantCulture);

    internal static DateTime ParseTs(string? s) =>
        string.IsNullOrEmpty(s)
            ? default
            : DateTime.Parse(s, CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind).ToUniversalTime();

    internal static string? NullStr(string? s) => string.IsNullOrEmpty(s) ? null : s;
}

/// <summary>Reading helpers for nullable columns.</summary>
static class ReaderExtensions
{
    public static string Str(this SqliteDataReader r, int i) => r.IsDBNull(i) ? "" : r.GetString(i);
    public static double Dbl(this SqliteDataReader r, int i) => r.IsDBNull(i) ? 0 : r.GetDouble(i);
    public static int Int(this SqliteDataReader r, int i) => r.IsDBNull(i) ? 0 : r.GetInt32(i);
    public static bool Bool(this SqliteDataReader r, int i) => !r.IsDBNull(i) && r.GetInt32(i) != 0;
}
