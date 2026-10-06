namespace MailCat.Storage;

/// <summary>
/// The persisted envelope of a classification session. The engine owns the JSON State
/// (frozen evidence, candidates, asked, answers).
/// </summary>
public sealed class SessionRow
{
    public string Id { get; set; } = "";
    public string EmailId { get; set; } = "";
    /// <summary>open | resolved</summary>
    public string Status { get; set; } = "";
    public string InitialDecision { get; set; } = "";
    public int QuestionsAsked { get; set; }
    public string FinalCategory { get; set; } = "";
    public string ResolvedBy { get; set; } = "";
    public string State { get; set; } = "";
    public DateTime CreatedAt { get; set; }
    public DateTime UpdatedAt { get; set; }
}

public static class InteractionKind
{
    public const string Answer = "answer";
    public const string Skip = "skip";
    public const string Confirm = "confirm";
    public const string Correct = "correct";
    public const string Manual = "manual";
}

/// <summary>One user interaction (§5.7).</summary>
public sealed class Interaction
{
    public string Id { get; set; } = "";
    public string EmailId { get; set; } = "";
    public string SessionId { get; set; } = "";
    public string Kind { get; set; } = "";
    public string QuestionId { get; set; } = "";
    public string Answer { get; set; } = "";
    public string PreviousPrediction { get; set; } = "";
    public string ResultingCategory { get; set; } = "";
    public double ExpectedGain { get; set; }
    public double RealizedGain { get; set; }
    public DateTime CreatedAt { get; set; }
}

public sealed partial class Database
{
    /// <summary>Upserts a session by email id.</summary>
    public void SaveSession(SessionRow s, DateTime now)
    {
        if (s.Id == "") s.Id = NewId();
        Exec("""
            INSERT INTO sessions(id,email_id,status,initial_decision,questions_asked,final_category,resolved_by,state,created_at,updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?)
            ON CONFLICT(email_id) DO UPDATE SET status=excluded.status, questions_asked=excluded.questions_asked,
              final_category=excluded.final_category, resolved_by=excluded.resolved_by, state=excluded.state, updated_at=excluded.updated_at
            """, s.Id, s.EmailId, s.Status, s.InitialDecision, s.QuestionsAsked, NullStr(s.FinalCategory), NullStr(s.ResolvedBy), s.State, now, now);
        Bump();
        BumpLabels();
    }

    /// <summary>The session of an email; throws <see cref="NotFoundException"/> if none.</summary>
    public SessionRow GetSession(string emailId)
    {
        var rows = Query("SELECT id,email_id,status,initial_decision,questions_asked,final_category,resolved_by,state,created_at,updated_at FROM sessions WHERE email_id=?",
            r => new SessionRow
            {
                Id = r.GetString(0), EmailId = r.GetString(1), Status = r.GetString(2), InitialDecision = r.GetString(3),
                QuestionsAsked = r.GetInt32(4), FinalCategory = r.Str(5), ResolvedBy = r.Str(6), State = r.GetString(7),
                CreatedAt = ParseTs(r.Str(8)), UpdatedAt = ParseTs(r.Str(9)),
            }, emailId);
        return rows.Count > 0 ? rows[0] : throw new NotFoundException();
    }

    /// <summary>Records an interaction.</summary>
    public void AddInteraction(Interaction i, DateTime now)
    {
        if (i.Id == "") i.Id = NewId();
        Exec("""
            INSERT INTO interactions(id,email_id,session_id,kind,question_id,answer,previous_prediction,resulting_category,expected_gain,realized_gain,created_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?)
            """, i.Id, i.EmailId, NullStr(i.SessionId), i.Kind, NullStr(i.QuestionId),
            i.Kind == InteractionKind.Skip ? null : NullStr(i.Answer), NullStr(i.PreviousPrediction),
            NullStr(i.ResultingCategory), i.ExpectedGain, i.RealizedGain, now);
        Bump();
        if (i.Kind is InteractionKind.Confirm or InteractionKind.Correct or InteractionKind.Manual) BumpLabels();
    }

    /// <summary>An email's interactions in chronological order.</summary>
    public List<Interaction> Interactions(string emailId) =>
        Query("""
            SELECT id,COALESCE(session_id,''),kind,COALESCE(question_id,''),COALESCE(answer,''),COALESCE(previous_prediction,''),
                   COALESCE(resulting_category,''),COALESCE(expected_gain,0),COALESCE(realized_gain,0),created_at
            FROM interactions WHERE email_id=? ORDER BY created_at, rowid
            """, r => new Interaction
        {
            Id = r.GetString(0), EmailId = emailId, SessionId = r.GetString(1), Kind = r.GetString(2), QuestionId = r.GetString(3),
            Answer = r.GetString(4), PreviousPrediction = r.GetString(5), ResultingCategory = r.GetString(6),
            ExpectedGain = r.GetDouble(7), RealizedGain = r.GetDouble(8), CreatedAt = ParseTs(r.Str(9)),
        }, emailId);
}
