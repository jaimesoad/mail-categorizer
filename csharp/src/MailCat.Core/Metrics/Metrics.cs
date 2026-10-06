using System.Globalization;
using System.Text;
using MailCat.Categories;
using MailCat.Storage;

namespace MailCat.Metrics;

/// <summary>Precision/recall for one category over user-verified emails.</summary>
public sealed class CategoryStats
{
    public string Category { get; set; } = "";
    /// <summary>Verified emails whose true category is this one.</summary>
    public int Support { get; set; }
    public int Predicted { get; set; }
    public double Precision { get; set; }
    public double Recall { get; set; }
}

/// <summary>How useful one question is in practice (§34).</summary>
public sealed class QuestionStat
{
    public string QuestionId { get; set; } = "";
    public int Shown { get; set; }
    public int Answered { get; set; }
    public double ResponseRate { get; set; }
    public double AvgExpectedGain { get; set; }
    public double AvgRealizedGain { get; set; }
}

/// <summary>The quality and effort indicators of §34 and §40, computed from sessions and interactions.</summary>
public sealed class Report
{
    public int Emails { get; set; }
    public int Resolved { get; set; }
    public int OpenSessions { get; set; }
    public int VerifiedByUser { get; set; }
    public int QuestionsAnswered { get; set; }
    public int QuestionsSkipped { get; set; }
    public int Corrections { get; set; }
    public int ManualLabels { get; set; }

    /// <summary>Over user-verified emails.</summary>
    public double ClassificationAccuracy { get; set; }
    public double AutoClassificationRate { get; set; }
    public double QuestionRate { get; set; }
    /// <summary>Corrections / verdicts on a prediction.</summary>
    public double CorrectionRate { get; set; }
    public double AvgQuestionsPerEmail { get; set; }
    public double UnknownRate { get; set; }

    /// <summary>
    /// §40's headline metric: correct classifications per unit of user effort (answers + skips
    /// + corrections + manual filings). Correct = resolved, non-unknown emails that were not
    /// corrected. Higher is better.
    /// </summary>
    public double EffortRatio { get; set; }

    public List<CategoryStats> ByCategory { get; set; } = [];
    public List<QuestionStat> Questions { get; set; } = [];

    sealed record Session(string Status, string Initial, int Asked, string Final);
    sealed record Verdict(string Predicted, string Truth);
    sealed class QAcc { public int Shown, Answered; public double Exp, Real; }

    public static Report Compute(Database db)
    {
        var r = new Report();
        var sessions = db.Query(
            "SELECT email_id, status, initial_decision, questions_asked, COALESCE(final_category,'') FROM sessions",
            x => (Id: x.GetString(0), S: new Session(x.GetString(1), x.GetString(2), x.GetInt32(3), x.GetString(4))))
            .ToDictionary(t => t.Id, t => t.S, StringComparer.Ordinal);

        var inter = db.Query("""
            SELECT email_id, kind, COALESCE(question_id,''), COALESCE(previous_prediction,''), COALESCE(resulting_category,''),
                   COALESCE(expected_gain,0), COALESCE(realized_gain,0)
            FROM interactions ORDER BY created_at, rowid
            """, x => (Eid: x.GetString(0), Kind: x.GetString(1), Qid: x.GetString(2), Prev: x.GetString(3), Res: x.GetString(4),
                       Exp: x.GetDouble(5), Real: x.GetDouble(6)));

        var qs = new Dictionary<string, QAcc>(StringComparer.Ordinal);
        var verdicts = new Dictionary<string, Verdict>(StringComparer.Ordinal); // first verdict per email: it judges the system's own output
        var corrected = new HashSet<string>(StringComparer.Ordinal);
        foreach (var (eid, kind, qid, prev0, res, exp, real) in inter)
        {
            switch (kind)
            {
                case InteractionKind.Answer or InteractionKind.Skip:
                    if (!qs.TryGetValue(qid, out var a)) qs[qid] = a = new QAcc();
                    a.Shown++;
                    if (kind == InteractionKind.Answer)
                    {
                        a.Answered++;
                        a.Exp += exp;
                        a.Real += real;
                        r.QuestionsAnswered++;
                    }
                    else r.QuestionsSkipped++;
                    break;
                case InteractionKind.Confirm or InteractionKind.Correct or InteractionKind.Manual:
                    if (!verdicts.ContainsKey(eid))
                        verdicts[eid] = new Verdict(prev0 == "" ? CategoryTree.UnknownId : prev0, res);
                    if (kind == InteractionKind.Correct)
                    {
                        r.Corrections++;
                        corrected.Add(eid);
                    }
                    else if (kind == InteractionKind.Manual) r.ManualLabels++;
                    break;
            }
        }

        r.Emails = sessions.Count;
        int autos = 0, withQ = 0, unknown = 0, askedTotal = 0, correctFinal = 0;
        foreach (var (id, s) in sessions)
        {
            if (s.Status == "open")
            {
                r.OpenSessions++;
                continue;
            }
            r.Resolved++;
            if (s.Initial == "know" && s.Asked == 0) autos++;
            if (s.Asked > 0) withQ++;
            askedTotal += s.Asked;
            if (s.Final == CategoryTree.UnknownId) unknown++;
            else if (!corrected.Contains(id)) correctFinal++;
        }
        // the rates are over emails that have been through the whole pipeline
        if (r.Resolved > 0)
        {
            double n = r.Resolved;
            r.AutoClassificationRate = autos / n;
            r.QuestionRate = withQ / n;
            r.AvgQuestionsPerEmail = askedTotal / n;
            r.UnknownRate = unknown / n;
        }

        // accuracy / precision / recall over verified emails
        r.VerifiedByUser = verdicts.Count;
        int hits = 0;
        var tp = new Dictionary<string, int>(StringComparer.Ordinal);
        var predicted = new Dictionary<string, int>(StringComparer.Ordinal);
        var support = new Dictionary<string, int>(StringComparer.Ordinal);
        foreach (var v in verdicts.OrderBy(kv => kv.Key, StringComparer.Ordinal).Select(kv => kv.Value))
        {
            support[v.Truth] = support.GetValueOrDefault(v.Truth) + 1;
            predicted[v.Predicted] = predicted.GetValueOrDefault(v.Predicted) + 1;
            if (v.Predicted == v.Truth)
            {
                hits++;
                tp[v.Truth] = tp.GetValueOrDefault(v.Truth) + 1;
            }
        }
        if (r.VerifiedByUser > 0) r.ClassificationAccuracy = (double)hits / r.VerifiedByUser;
        int judged = verdicts.Values.Count(v => v.Predicted != CategoryTree.UnknownId); // verdicts on an actual prediction
        if (judged > 0) r.CorrectionRate = (double)r.Corrections / judged;

        var names = support.Keys.Concat(predicted.Keys).Where(c => c != CategoryTree.UnknownId)
            .Distinct().OrderBy(c => c, StringComparer.Ordinal);
        foreach (var c in names)
        {
            var cs = new CategoryStats { Category = c, Support = support.GetValueOrDefault(c), Predicted = predicted.GetValueOrDefault(c) };
            if (cs.Predicted > 0) cs.Precision = (double)tp.GetValueOrDefault(c) / cs.Predicted;
            if (cs.Support > 0) cs.Recall = (double)tp.GetValueOrDefault(c) / cs.Support;
            r.ByCategory.Add(cs);
        }

        int effort = r.QuestionsAnswered + r.QuestionsSkipped + r.Corrections + r.ManualLabels;
        if (effort == 0) effort = 1;
        r.EffortRatio = (double)correctFinal / effort;

        foreach (var (id, a) in qs.OrderBy(kv => kv.Key, StringComparer.Ordinal))
        {
            var st = new QuestionStat { QuestionId = id, Shown = a.Shown, Answered = a.Answered };
            if (a.Shown > 0) st.ResponseRate = (double)a.Answered / a.Shown;
            if (a.Answered > 0)
            {
                st.AvgExpectedGain = a.Exp / a.Answered;
                st.AvgRealizedGain = a.Real / a.Answered;
            }
            r.Questions.Add(st);
        }
        return r;
    }

    static string Pct(double f) => (f * 100).ToString("F1", CultureInfo.InvariantCulture) + "%";
    static string F2(double f) => f.ToString("F2", CultureInfo.InvariantCulture);

    /// <summary>Renders the report for terminals.</summary>
    public string Text()
    {
        var b = new StringBuilder();
        b.AppendLine($"Correos: {Emails} (resueltos {Resolved}, con pregunta pendiente {OpenSessions})");
        b.AppendLine($"Auto-clasificación : {Pct(AutoClassificationRate)}");
        b.AppendLine($"Tasa de preguntas  : {Pct(QuestionRate)}  (media {F2(AvgQuestionsPerEmail)} preguntas/correo)");
        b.AppendLine($"Desconocidos       : {Pct(UnknownRate)}");
        b.AppendLine($"Veredictos del usuario: {VerifiedByUser}  → acierto {Pct(ClassificationAccuracy)}, tasa de corrección {Pct(CorrectionRate)}");
        b.AppendLine($"Esfuerzo: {QuestionsAnswered} respuestas, {QuestionsSkipped} omitidas, {Corrections} correcciones, {ManualLabels} etiquetados manuales");
        b.AppendLine($"Correctas por unidad de esfuerzo: {F2(EffortRatio)}");
        if (ByCategory.Count > 0)
        {
            b.AppendLine().AppendLine("Por categoría (precisión / exhaustividad):");
            foreach (var c in ByCategory)
                b.AppendLine($"  {c.Category,-22} P={Pct(c.Precision)} R={Pct(c.Recall)} (n={c.Support})");
        }
        if (Questions.Count > 0)
        {
            b.AppendLine().AppendLine("Utilidad de las preguntas:");
            foreach (var q in Questions)
                b.AppendLine($"  {q.QuestionId,-18} mostrada {q.Shown}, respondida {Pct(q.ResponseRate)}, ganancia esperada {F2(q.AvgExpectedGain)} / real {F2(q.AvgRealizedGain)} bits");
        }
        return b.ToString();
    }
}
