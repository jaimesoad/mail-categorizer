// Package metrics computes the quality and effort indicators of §34 and §40
// from what the database already records: sessions and interactions.
package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// CategoryStats is precision/recall for one category over user-verified emails.
type CategoryStats struct {
	Category  string  `json:"category"`
	Support   int     `json:"support"` // verified emails whose true category is this one
	Predicted int     `json:"predicted"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

// QuestionStat describes how useful one question is in practice (§34).
type QuestionStat struct {
	QuestionID      string  `json:"question_id"`
	Shown           int     `json:"shown"`
	Answered        int     `json:"answered"`
	ResponseRate    float64 `json:"response_rate"`
	AvgExpectedGain float64 `json:"avg_expected_information_gain"`
	AvgRealizedGain float64 `json:"avg_realized_information_gain"`
}

// Report is the full set of indicators.
type Report struct {
	Emails            int     `json:"emails"`
	Resolved          int     `json:"resolved"`
	OpenSessions      int     `json:"open_sessions"`
	VerifiedByUser    int     `json:"verified_by_user"`
	QuestionsAnswered int     `json:"questions_answered"`
	QuestionsSkipped  int     `json:"questions_skipped"`
	Corrections       int     `json:"corrections"`
	ManualLabels      int     `json:"manual_labels"`
	LLMLabels         int     `json:"llm_labels"`           // emails labelled by the local LLM teacher
	AwaitingAudit     int     `json:"awaiting_human_check"` // of those, not yet verified by a person
	TeacherChecked    int     `json:"teacher_checked"`      // teacher labels a person has verified
	TeacherAccuracy   float64 `json:"teacher_accuracy"`     // share of those the person agreed with

	ClassificationAccuracy float64 `json:"classification_accuracy"` // over user-verified emails
	AutoClassificationRate float64 `json:"auto_classification_rate"`
	QuestionRate           float64 `json:"question_rate"`
	CorrectionRate         float64 `json:"correction_rate"` // corrections / verdicts on a prediction
	AvgQuestionsPerEmail   float64 `json:"average_questions_per_email"`
	UnknownRate            float64 `json:"unknown_rate"`
	// EffortRatio is §40's headline metric: correct classifications per unit of
	// user effort (answers + skips + corrections + manual filings). Correct =
	// resolved, non-unknown emails that were not corrected. Higher is better.
	EffortRatio float64 `json:"effort_ratio"`

	ByCategory []CategoryStats `json:"by_category"`
	Questions  []QuestionStat  `json:"questions"`
}

type verdict struct{ predicted, truth string }

// Compute builds the report from the database.
func Compute(ctx context.Context, db *storage.DB) (Report, error) {
	var r Report

	// sessions
	rows, err := db.QueryContext(ctx, `SELECT email_id, status, initial_decision, questions_asked, COALESCE(final_category,'') FROM sessions`)
	if err != nil {
		return r, err
	}
	type sess struct {
		status, initial, final string
		asked                  int
	}
	sessions := map[string]sess{}
	for rows.Next() {
		var id string
		var s sess
		if err := rows.Scan(&id, &s.status, &s.initial, &s.asked, &s.final); err != nil {
			rows.Close()
			return r, err
		}
		sessions[id] = s
	}
	rows.Close()

	// interactions
	irows, err := db.QueryContext(ctx, `SELECT email_id, kind, COALESCE(question_id,''), COALESCE(previous_prediction,''), COALESCE(resulting_category,''), COALESCE(expected_gain,0), COALESCE(realized_gain,0) FROM interactions WHERE actor='user' ORDER BY created_at, rowid`)
	if err != nil {
		return r, err
	}
	defer irows.Close()
	type qacc struct {
		shown, answered int
		exp, real       float64
	}
	qs := map[string]*qacc{}
	verdictKind := map[string]string{}
	verdicts := map[string]verdict{} // first verdict per email: it judges the system's own output
	corrected := map[string]bool{}
	for irows.Next() {
		var eid, kind, qid, prev, res string
		var exp, real float64
		if err := irows.Scan(&eid, &kind, &qid, &prev, &res, &exp, &real); err != nil {
			return r, err
		}
		switch kind {
		case storage.KindAnswer, storage.KindSkip:
			a := qs[qid]
			if a == nil {
				a = &qacc{}
				qs[qid] = a
			}
			a.shown++
			if kind == storage.KindAnswer {
				a.answered++
				a.exp += exp
				a.real += real
				r.QuestionsAnswered++
			} else {
				r.QuestionsSkipped++
			}
		case storage.KindConfirm, storage.KindCorrect, storage.KindManual:
			if _, seen := verdicts[eid]; !seen {
				if prev == "" {
					prev = categories.UnknownID
				}
				verdicts[eid] = verdict{prev, res}
				verdictKind[eid] = kind
			}
			switch kind {
			case storage.KindCorrect:
				r.Corrections++
				corrected[eid] = true
			case storage.KindManual:
				r.ManualLabels++
			}
		}
	}
	if err := irows.Err(); err != nil {
		return r, err
	}

	var llmRows, auditedRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT email_id) FROM interactions WHERE actor='llm' AND kind IN ('confirm','correct','manual')`).Scan(&llmRows); err != nil {
		return r, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT email_id) FROM interactions WHERE actor='llm' AND kind IN ('confirm','correct','manual')
		AND email_id IN (SELECT email_id FROM interactions WHERE actor='user' AND kind IN ('confirm','correct','manual'))`).Scan(&auditedRows); err != nil {
		return r, err
	}
	r.LLMLabels, r.AwaitingAudit = llmRows, llmRows-auditedRows
	llmSet := map[string]bool{}
	lrows, err := db.QueryContext(ctx, `SELECT DISTINCT email_id FROM interactions WHERE actor='llm' AND kind IN ('confirm','correct','manual')`)
	if err != nil {
		return r, err
	}
	for lrows.Next() {
		var id string
		if err := lrows.Scan(&id); err != nil {
			lrows.Close()
			return r, err
		}
		llmSet[id] = true
	}
	lrows.Close()

	r.Emails = len(sessions)
	autos, withQ, unknown, askedTotal, correctFinal := 0, 0, 0, 0, 0
	for id, s := range sessions {
		if s.status == "open" {
			r.OpenSessions++
			continue
		}
		r.Resolved++
		if s.initial == "know" && s.asked == 0 {
			autos++
		}
		if s.asked > 0 {
			withQ++
		}
		askedTotal += s.asked
		if s.final == categories.UnknownID {
			unknown++
		} else if !corrected[id] {
			correctFinal++
		}
	}
	// the rates are over emails that have been through the whole pipeline
	if n := float64(r.Resolved); n > 0 {
		r.AutoClassificationRate = float64(autos) / n
		r.QuestionRate = float64(withQ) / n
		r.AvgQuestionsPerEmail = float64(askedTotal) / n
		r.UnknownRate = float64(unknown) / n
	}

	// how good is the teacher? a person's first verdict on a teacher-labelled email
	agree := 0
	for id, k := range verdictKind {
		if llmSet[id] {
			r.TeacherChecked++
			if k == storage.KindConfirm {
				agree++
			}
		}
	}
	if r.TeacherChecked > 0 {
		r.TeacherAccuracy = float64(agree) / float64(r.TeacherChecked)
	}

	// accuracy / precision / recall over verified emails
	r.VerifiedByUser = len(verdicts)
	hits := 0
	tp, predicted, support := map[string]int{}, map[string]int{}, map[string]int{}
	ids := make([]string, 0, len(verdicts))
	for id := range verdicts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := verdicts[id]
		support[v.truth]++
		predicted[v.predicted]++
		if v.predicted == v.truth {
			hits++
			tp[v.truth]++
		}
	}
	if r.VerifiedByUser > 0 {
		r.ClassificationAccuracy = float64(hits) / float64(r.VerifiedByUser)
	}
	judged := 0 // verdicts on an actual prediction (not manual filings from nothing)
	for _, v := range verdicts {
		if v.predicted != categories.UnknownID {
			judged++
		}
	}
	if judged > 0 {
		r.CorrectionRate = float64(r.Corrections) / float64(judged)
	}
	cats := map[string]bool{}
	for c := range support {
		cats[c] = true
	}
	for c := range predicted {
		cats[c] = true
	}
	names := make([]string, 0, len(cats))
	for c := range cats {
		if c != categories.UnknownID {
			names = append(names, c)
		}
	}
	sort.Strings(names)
	for _, c := range names {
		cs := CategoryStats{Category: c, Support: support[c], Predicted: predicted[c]}
		if predicted[c] > 0 {
			cs.Precision = float64(tp[c]) / float64(predicted[c])
		}
		if support[c] > 0 {
			cs.Recall = float64(tp[c]) / float64(support[c])
		}
		r.ByCategory = append(r.ByCategory, cs)
	}

	effort := r.QuestionsAnswered + r.QuestionsSkipped + r.Corrections + r.ManualLabels
	if effort == 0 {
		effort = 1
	}
	r.EffortRatio = float64(correctFinal) / float64(effort)

	qids := make([]string, 0, len(qs))
	for id := range qs {
		qids = append(qids, id)
	}
	sort.Strings(qids)
	for _, id := range qids {
		a := qs[id]
		st := QuestionStat{QuestionID: id, Shown: a.shown, Answered: a.answered}
		if a.shown > 0 {
			st.ResponseRate = float64(a.answered) / float64(a.shown)
		}
		if a.answered > 0 {
			st.AvgExpectedGain = a.exp / float64(a.answered)
			st.AvgRealizedGain = a.real / float64(a.answered)
		}
		r.Questions = append(r.Questions, st)
	}
	return r, nil
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

// Text renders the report for terminals.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Correos: %d (resueltos %d, con pregunta pendiente %d)\n", r.Emails, r.Resolved, r.OpenSessions)
	fmt.Fprintf(&b, "Auto-clasificación : %s\n", pct(r.AutoClassificationRate))
	fmt.Fprintf(&b, "Tasa de preguntas  : %s  (media %.2f preguntas/correo)\n", pct(r.QuestionRate), r.AvgQuestionsPerEmail)
	fmt.Fprintf(&b, "Desconocidos       : %s\n", pct(r.UnknownRate))
	fmt.Fprintf(&b, "Veredictos del usuario: %d  → acierto %s, tasa de corrección %s\n", r.VerifiedByUser, pct(r.ClassificationAccuracy), pct(r.CorrectionRate))
	fmt.Fprintf(&b, "Esfuerzo: %d respuestas, %d omitidas, %d correcciones, %d etiquetados manuales\n", r.QuestionsAnswered, r.QuestionsSkipped, r.Corrections, r.ManualLabels)
	fmt.Fprintf(&b, "Correctas por unidad de esfuerzo: %.2f\n", r.EffortRatio)
	if r.LLMLabels > 0 {
		fmt.Fprintf(&b, "Etiquetados por el LLM local: %d (pendientes de verificación humana: %d)\n", r.LLMLabels, r.AwaitingAudit)
		if r.TeacherChecked > 0 {
			fmt.Fprintf(&b, "  Fiabilidad del LLM según tus verificaciones: %s (%d revisados)\n", pct(r.TeacherAccuracy), r.TeacherChecked)
		}
	}
	if len(r.ByCategory) > 0 {
		b.WriteString("\nPor categoría (precisión / exhaustividad):\n")
		for _, c := range r.ByCategory {
			fmt.Fprintf(&b, "  %-22s P=%s R=%s (n=%d)\n", c.Category, pct(c.Precision), pct(c.Recall), c.Support)
		}
	}
	if len(r.Questions) > 0 {
		b.WriteString("\nUtilidad de las preguntas:\n")
		for _, q := range r.Questions {
			fmt.Fprintf(&b, "  %-18s mostrada %d, respondida %s, ganancia esperada %.2f / real %.2f bits\n",
				q.QuestionID, q.Shown, pct(q.ResponseRate), q.AvgExpectedGain, q.AvgRealizedGain)
		}
	}
	return b.String()
}
