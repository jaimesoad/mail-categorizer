package storage

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/similarity"
)

// Label is the best known truth about an email's category.
type Label struct {
	Category string
	// Source: "user" (explicit human verdict) | "answers" (resolved through the
	// user's answers) | "llm" (decided by the local LLM "teacher"). A human
	// verdict always overrides the teacher.
	Source string
}

// Labels returns the trusted labels of all emails. Fully automatic
// classifications are deliberately excluded: learning from the system's own
// output would only reinforce its mistakes.
func (d *DB) Labels(ctx context.Context) (map[string]Label, error) {
	v := d.Version()
	if c, ok := d.cached("labels"); ok {
		return c.(map[string]Label), nil
	}
	out := map[string]Label{}
	rows, err := d.QueryContext(ctx, `SELECT email_id, final_category, resolved_by FROM sessions WHERE resolved_by IN ('answers','teacher') AND final_category IS NOT NULL AND final_category!='unknown'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, cat, by string
		if err := rows.Scan(&id, &cat, &by); err != nil {
			rows.Close()
			return nil, err
		}
		src := "answers"
		if by == "teacher" {
			src = "llm"
		}
		out[id] = Label{cat, src}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	// teacher verdicts first, human ones last: a person always has the final word
	rows, err = d.QueryContext(ctx, `SELECT email_id, resulting_category, actor FROM interactions WHERE kind IN ('confirm','correct','manual') AND resulting_category IS NOT NULL ORDER BY (actor='user'), created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, cat, actor string
		if err := rows.Scan(&id, &cat, &actor); err != nil {
			return nil, err
		}
		src := "user"
		if actor == ActorLLM {
			src = "llm"
		}
		out[id] = Label{cat, src} // later interactions override earlier ones
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.setCached("labels", out, v)
	return out, nil
}

// QuestionStats implements questions.StatsProvider: how often each question
// was shown/answered and, per category, how users answered when the final
// category was known.
func (d *DB) QuestionStats(ctx context.Context) (map[string]questions.Stats, error) {
	v := d.Version()
	if c, ok := d.cached("qstats"); ok {
		return c.(map[string]questions.Stats), nil
	}
	labels, err := d.Labels(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT question_id, email_id, kind, COALESCE(answer,'') FROM interactions WHERE kind IN ('answer','skip') AND question_id IS NOT NULL AND actor='user'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]questions.Stats{}
	for rows.Next() {
		var qid, eid, kind, ans string
		if err := rows.Scan(&qid, &eid, &kind, &ans); err != nil {
			return nil, err
		}
		st := out[qid]
		st.Shown++
		if kind == KindAnswer {
			st.Answered++
			if lab, ok := labels[eid]; ok && lab.Source == "user" { // only explicit labels: no self-reinforcement
				if st.Counts == nil {
					st.Counts = map[string]map[string]float64{}
				}
				if st.Counts[lab.Category] == nil {
					st.Counts[lab.Category] = map[string]float64{}
				}
				st.Counts[lab.Category][ans]++
			}
		}
		out[qid] = st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	d.setCached("qstats", out, v)
	return out, nil
}

// LabeledDocs returns the labeled emails (with remaining text) for similarity.
func (d *DB) LabeledDocs(ctx context.Context, maxChars int) ([]similarity.Doc, error) {
	labels, err := d.Labels(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `SELECT id, COALESCE(subject,''), COALESCE(body_text,'') FROM emails WHERE body_text IS NOT NULL AND body_text!='' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []similarity.Doc
	for rows.Next() {
		var id, subj, body string
		if err := rows.Scan(&id, &subj, &body); err != nil {
			return nil, err
		}
		lab, ok := labels[id]
		if !ok {
			continue
		}
		if maxChars > 0 && len(body) > maxChars {
			body = body[:maxChars]
		}
		trust := 1.0
		switch lab.Source {
		case "answers":
			trust = 0.7
		case "llm":
			trust = 0.6
		}
		docs = append(docs, similarity.Doc{ID: id, Category: lab.Category, Text: subj + " " + subj + " " + body, Trust: trust})
	}
	return docs, rows.Err()
}

// SenderHistory implements features.History.
func (d *DB) SenderHistory(ctx context.Context, senderEmail, senderDomain string) (features.SenderHistory, error) {
	var h features.SenderHistory
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM emails WHERE sender_email=?`, senderEmail).Scan(&h.EmailCount); err != nil {
		return h, err
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM emails WHERE sender_domain=?`, senderDomain).Scan(&h.DomainCount); err != nil {
		return h, err
	}
	var err error
	if h.EmailCategories, err = d.topCategories(ctx, `e.sender_email=?`, senderEmail); err != nil {
		return h, err
	}
	h.DomainCategories, err = d.topCategories(ctx, `e.sender_domain=?`, senderDomain)
	return h, err
}

func (d *DB) topCategories(ctx context.Context, where, arg string) ([]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT c.category_id FROM emails e JOIN classifications c ON c.email_id=e.id AND c.is_final=1
WHERE `+where+` AND c.category_id!=? GROUP BY c.category_id ORDER BY COUNT(*) DESC, c.category_id LIMIT 3`, arg, categories.UnknownID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ThreadInfo implements features.History: the first of ids (ordered by
// proximity) that is a known message, with its final category and sender.
func (d *DB) ThreadInfo(ctx context.Context, ids []string) (features.ThreadInfo, error) {
	for _, id := range ids {
		var email, cat sql.NullString
		err := d.QueryRowContext(ctx, `SELECT e.sender_email, (SELECT category_id FROM classifications WHERE email_id=e.id AND is_final=1)
FROM emails e WHERE e.message_id=? LIMIT 1`, strings.Trim(id, "<>")).Scan(&email, &cat)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return features.ThreadInfo{}, err
		}
		c := cat.String
		if c == categories.UnknownID {
			c = ""
		}
		return features.ThreadInfo{Found: true, Category: c, Sender: email.String}, nil
	}
	return features.ThreadInfo{}, nil
}
