package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/metrics"
	"github.com/jaimesoad/mail-categorizer/internal/pretrain"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Answerer lets a local model try to answer a question before the person is
// bothered (config telegram.llm_answers_first). Optional.
type Answerer interface {
	Answer(ctx context.Context, e email.Email, q questions.Question) (answerID string, ok bool, err error)
}

// Options configure the bot.
type Options struct {
	ChatID         int64
	ShowSubject    bool
	MaxOpenPrompts int
	SweepEvery     time.Duration
	Answerer       Answerer
	Log            *slog.Logger
	Now            func() time.Time
}

// Bot asks, confirms and audits through Telegram. Only ChatID is obeyed.
type Bot struct {
	c   *Client
	eng *engine.Engine
	db  *storage.DB
	opt Options
	log *slog.Logger
}

// New creates a bot.
func New(c *Client, eng *engine.Engine, opt Options) *Bot {
	if opt.MaxOpenPrompts <= 0 {
		opt.MaxOpenPrompts = 5
	}
	if opt.SweepEvery <= 0 {
		opt.SweepEvery = 15 * time.Second
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Bot{c: c, eng: eng, db: eng.DB(), opt: opt, log: opt.Log}
}

// Run polls Telegram and sweeps for new prompts until ctx ends.
func (b *Bot) Run(ctx context.Context) error {
	sweep := time.NewTicker(b.opt.SweepEvery)
	defer sweep.Stop()
	updates := make(chan []Update)
	errs := make(chan error)
	go func() {
		backoff := time.Second
		var last int64 // owned by this goroutine: no sharing, no race
		for ctx.Err() == nil {
			ups, err := b.c.GetUpdates(ctx, last+1, 25)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				select {
				case errs <- err:
				case <-ctx.Done():
					return
				}
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				if backoff < 30*time.Second {
					backoff *= 2
				}
				continue
			}
			backoff = time.Second
			if len(ups) == 0 {
				continue
			}
			for _, u := range ups {
				if u.ID > last {
					last = u.ID
				}
			}
			select {
			case updates <- ups:
			case <-ctx.Done():
				return
			}
		}
	}()
	b.Sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ups := <-updates:
			for _, u := range ups {
				b.Handle(ctx, u)
			}
		case err := <-errs:
			b.log.Warn("telegram: error de red", "err", err)
		case <-sweep.C:
			b.Sweep(ctx)
		}
	}
}

// ---- sending ----

func (b *Bot) describe(em email.Email) string {
	if b.opt.ShowSubject {
		from := em.From.Email
		if em.From.Name != "" {
			from = em.From.Name + " <" + em.From.Email + ">"
		}
		return fmt.Sprintf("📧 %s\n%s", from, trunc(em.Subject, 140))
	}
	return "📧 Correo de @" + features.RegistrableDomain(em.From.Domain())
}

func trunc(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// Sweep looks for emails that need the person: pending questions first, then
// predictions to confirm and unknowns to file. It never exceeds MaxOpenPrompts.
func (b *Bot) Sweep(ctx context.Context) {
	if b.opt.ChatID == 0 {
		return
	}
	open, err := b.db.OpenPrompts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			b.log.Error("telegram: sweep", "err", err)
		}
		return
	}
	room := b.opt.MaxOpenPrompts - open

	ids, err := b.db.EmailsAwaitingQuestion(ctx, 50)
	if err != nil {
		b.log.Error("telegram: sweep", "err", err)
		return
	}
	for _, id := range ids {
		out, err := b.eng.Get(ctx, id)
		if err != nil || !out.NeedsQuestion || out.Question == nil {
			continue
		}
		if b.opt.Answerer != nil {
			if done := b.tryLLM(ctx, id, out); done {
				continue
			}
		}
		if room <= 0 {
			break
		}
		if b.sendQuestion(ctx, id, out) {
			room--
		}
	}
	if room <= 0 {
		return
	}
	ids, err = b.db.EmailsAwaitingReview(ctx, 100)
	if err != nil {
		b.log.Error("telegram: sweep", "err", err)
		return
	}
	for _, id := range ids {
		if room <= 0 {
			return
		}
		out, err := b.eng.Get(ctx, id)
		if err != nil {
			continue
		}
		switch {
		case out.NeedsManual:
			if b.sendCategory(ctx, id, out) {
				room--
			}
		case out.RequestConfirmation:
			if b.sendConfirm(ctx, id, out, "confirm") {
				room--
			}
		}
	}
}

// tryLLM lets the local model answer the pending question. true = handled.
func (b *Bot) tryLLM(ctx context.Context, id string, out *engine.Outcome) bool {
	em, err := b.db.GetEmail(ctx, id)
	if err != nil {
		return false
	}
	var q questions.Question
	for _, c := range b.eng.Questions() {
		if c.ID == out.Question.ID {
			q = c
		}
	}
	ans, ok, err := b.opt.Answerer.Answer(ctx, em, q)
	if err != nil || !ok {
		return false
	}
	_, err = b.eng.Answer(engine.WithActor(ctx, engine.ActorLLM), id, q.ID, ans)
	return err == nil
}

func (b *Bot) newPrompt(ctx context.Context, id, kind, qid string) (int64, bool) {
	pid, created, err := b.db.CreatePrompt(ctx, storage.Prompt{EmailID: id, Kind: kind, QuestionID: qid, ChatID: b.opt.ChatID}, b.opt.Now())
	if err != nil || !created {
		return 0, false
	}
	return pid, true
}

func (b *Bot) deliver(ctx context.Context, pid int64, text string, rows [][]Button) bool {
	mid, err := b.c.SendMessage(ctx, b.opt.ChatID, text, rows)
	if err != nil {
		_ = b.db.DeletePrompt(ctx, pid) // retry on the next sweep
		b.log.Warn("telegram: no se pudo enviar", "err", err)
		return false
	}
	_ = b.db.SetPromptMessage(ctx, pid, mid)
	return true
}

func (b *Bot) sendQuestion(ctx context.Context, id string, out *engine.Outcome) bool {
	q := out.Question
	pid, ok := b.newPrompt(ctx, id, "question", q.ID)
	if !ok {
		return false
	}
	em, err := b.db.GetEmail(ctx, id)
	if err != nil {
		_ = b.db.DeletePrompt(ctx, pid)
		return false
	}
	var row []Button
	for _, a := range q.Answers {
		row = append(row, Button{a.Label, fmt.Sprintf("q:%d:%s", pid, a.ID)})
	}
	rows := [][]Button{row, {{"⏭ Omitir", fmt.Sprintf("s:%d", pid)}, {"🗂 Otra categoría", fmt.Sprintf("m:%d", pid)}}}
	return b.deliver(ctx, pid, fmt.Sprintf("%s\n\n❓ %s", b.describe(em), q.Text), rows)
}

func (b *Bot) sendConfirm(ctx context.Context, id string, out *engine.Outcome, kind string) bool {
	pid, ok := b.newPrompt(ctx, id, kind, "")
	if !ok {
		return false
	}
	em, err := b.db.GetEmail(ctx, id)
	if err != nil {
		_ = b.db.DeletePrompt(ctx, pid)
		return false
	}
	who := fmt.Sprintf("Lo clasifiqué como «%s» (%.0f%%).", out.Path, out.Score*100)
	if out.Decision == engine.DecisionTeacher {
		who = fmt.Sprintf("El asistente local lo puso en «%s».", out.Path)
	}
	rows := [][]Button{{{"✅ Correcto", fmt.Sprintf("k:%d:y", pid)}, {"✏️ Cambiar", fmt.Sprintf("k:%d:n", pid)}}}
	return b.deliver(ctx, pid, fmt.Sprintf("%s\n\n%s\n¿Es correcto?", b.describe(em), who), rows)
}

func (b *Bot) sendCategory(ctx context.Context, id string, out *engine.Outcome) bool {
	pid, ok := b.newPrompt(ctx, id, "category", "")
	if !ok {
		return false
	}
	em, err := b.db.GetEmail(ctx, id)
	if err != nil {
		_ = b.db.DeletePrompt(ctx, pid)
		return false
	}
	text, rows := b.categoryKeyboard(ctx, pid, fmt.Sprintf("%s\n\n🤷 No sé dónde va. ¿En qué categoría lo archivo?", b.describe(em)))
	return b.deliver(ctx, pid, text, rows)
}

// categoryKeyboard stores the offered ids in the prompt (callback data is
// limited to 64 bytes, so buttons carry an index).
func (b *Bot) categoryKeyboard(ctx context.Context, pid int64, text string) (string, [][]Button) {
	var ids []string
	var rows [][]Button
	var row []Button
	b.eng.Tree().Walk(func(c categories.Category, depth int) {
		if c.ID == categories.UnknownID || !b.eng.Tree().Enabled(c.ID) {
			return
		}
		ids = append(ids, c.ID)
		label := c.Name
		if depth > 0 {
			label = b.eng.Tree().Path(c.ID)
		}
		row = append(row, Button{trunc(label, 28), fmt.Sprintf("c:%d:%d", pid, len(ids)-1)})
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	})
	if len(row) > 0 {
		rows = append(rows, row)
	}
	payload, _ := json.Marshal(ids)
	_ = b.db.SetPromptPayload(ctx, pid, string(payload))
	return text, rows
}

// ---- receiving ----

// Handle processes one update. Anything not coming from the configured chat is ignored.
func (b *Bot) Handle(ctx context.Context, u Update) {
	switch {
	case u.Message != nil:
		b.onMessage(ctx, u.Message)
	case u.Callback != nil:
		cb := u.Callback
		if cb.Message == nil || cb.Message.Chat.ID != b.opt.ChatID || b.opt.ChatID == 0 {
			b.log.Warn("telegram: botón de un chat no autorizado ignorado", "chat", chatOf(cb.Message))
			_ = b.c.AnswerCallback(ctx, cb.ID, "No autorizado")
			return
		}
		note := b.onCallback(ctx, cb.Data, cb.Message)
		_ = b.c.AnswerCallback(ctx, cb.ID, note)
	}
}

func chatOf(m *Msg) int64 {
	if m == nil {
		return 0
	}
	return m.Chat.ID
}

func (b *Bot) onMessage(ctx context.Context, m *Msg) {
	cmd := strings.Fields(strings.ToLower(m.Text))
	if len(cmd) == 0 {
		return
	}
	if b.opt.ChatID == 0 {
		if cmd[0] == "/start" {
			_, _ = b.c.SendMessage(ctx, m.Chat.ID, fmt.Sprintf("Tu chat id es %d.\nPonlo en mailcat.yaml (telegram.chat_id) y reinicia el servicio. Hasta entonces no hago nada más.", m.Chat.ID), nil)
		}
		return
	}
	if m.Chat.ID != b.opt.ChatID {
		b.log.Warn("telegram: mensaje de un chat no autorizado ignorado", "chat", m.Chat.ID)
		return
	}
	reply := func(t string) { _, _ = b.c.SendMessage(ctx, m.Chat.ID, t, nil) }
	switch strings.SplitN(cmd[0], "@", 2)[0] {
	case "/start", "/ayuda", "/help":
		reply("Soy tu clasificador de correo.\n\n/pendientes — preguntas sin responder\n/auditar — verifica una etiqueta del asistente local\n/metricas — cómo voy\n\nTe pregunto solo cuando una respuesta tuya aporta información.")
	case "/pendientes":
		n, _ := b.db.OpenPrompts(ctx)
		reply(fmt.Sprintf("Tienes %d mensajes sin responder.", n))
		b.Sweep(ctx)
	case "/metricas", "/métricas":
		rep, err := metrics.Compute(ctx, b.db)
		if err != nil {
			reply("No pude calcular las métricas.")
			return
		}
		reply(rep.Text())
	case "/auditar":
		b.sendAudit(ctx, reply)
	}
}

func (b *Bot) sendAudit(ctx context.Context, reply func(string)) {
	labels, err := b.db.Labels(ctx)
	if err != nil {
		reply("Error leyendo etiquetas.")
		return
	}
	for _, id := range pretrain.AuditSample(labels, 20) {
		out, err := b.eng.Get(ctx, id)
		if err != nil {
			continue
		}
		if b.sendConfirm(ctx, id, out, "audit") {
			return
		}
	}
	reply("No hay etiquetas del asistente local pendientes de verificar. 🎉")
}

func (b *Bot) onCallback(ctx context.Context, data string, msg *Msg) string {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return "?"
	}
	pid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "?"
	}
	p, err := b.db.GetPrompt(ctx, pid)
	if err != nil || p.ChatID != b.opt.ChatID {
		return "Ese mensaje ya no existe"
	}
	if p.Resolved {
		return "Ya respondido"
	}
	edit := func(text string) { _ = b.c.EditMessage(ctx, msg.Chat.ID, msg.ID, text, nil) }
	done := func(text string) {
		_ = b.db.ResolvePrompt(ctx, pid, b.opt.Now())
		edit(text)
	}

	switch parts[0] {
	case "q": // answer to a question
		if len(parts) != 3 {
			return "?"
		}
		out, err := b.eng.Answer(ctx, p.EmailID, p.QuestionID, parts[2])
		if err != nil {
			return b.failNote(err)
		}
		done(b.resultText(msg.Text, out))
		b.followUp(ctx, p.EmailID, out)
		return "Anotado"
	case "s": // skip
		out, err := b.eng.Skip(ctx, p.EmailID, p.QuestionID)
		if err != nil {
			return b.failNote(err)
		}
		done(msg.Text + "\n\n⏭ Omitida")
		b.followUp(ctx, p.EmailID, out)
		return "Omitida"
	case "m": // "otra categoría" from a question prompt, or "Cambiar" handled below
		text, rows := b.categoryKeyboard(ctx, pid, msg.Text+"\n\n🗂 ¿En qué categoría lo archivo?")
		_ = b.c.EditMessage(ctx, msg.Chat.ID, msg.ID, text, rows)
		return ""
	case "k": // confirmation
		if len(parts) != 3 {
			return "?"
		}
		if parts[2] == "y" {
			out, err := b.eng.Feedback(ctx, p.EmailID, true, "")
			if err != nil {
				return b.failNote(err)
			}
			done(msg.Text + "\n\n✅ Confirmado: " + out.Path)
			return "Aprendido"
		}
		text, rows := b.categoryKeyboard(ctx, pid, msg.Text+"\n\n✏️ ¿Cuál es la categoría correcta?")
		_ = b.c.EditMessage(ctx, msg.Chat.ID, msg.ID, text, rows)
		return ""
	case "c": // chosen category
		if len(parts) != 3 {
			return "?"
		}
		var ids []string
		_ = json.Unmarshal([]byte(p.Payload), &ids)
		idx, err := strconv.Atoi(parts[2])
		if err != nil || idx < 0 || idx >= len(ids) {
			return "Categoría no válida"
		}
		var out *engine.Outcome
		if p.Kind == "question" {
			out, err = b.eng.Move(ctx, p.EmailID, ids[idx])
		} else {
			out, err = b.eng.Feedback(ctx, p.EmailID, false, ids[idx])
		}
		if err != nil {
			return b.failNote(err)
		}
		done(fmt.Sprintf("%s\n\n🗂 Archivado en %s", firstLines(msg.Text, 2), out.Path))
		return "Aprendido"
	}
	return "?"
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) failNote(err error) string {
	switch {
	case errors.Is(err, engine.ErrConflict):
		return "Ya estaba resuelto"
	case errors.Is(err, engine.ErrInvalid):
		return "Respuesta no válida"
	}
	b.log.Error("telegram: acción fallida", "err", err)
	return "Error interno"
}

func (b *Bot) resultText(orig string, out *engine.Outcome) string {
	head := firstLines(orig, 2)
	switch out.Decision {
	case engine.DecisionKnow:
		return fmt.Sprintf("%s\n\n✅ %s (%.0f%%)", head, out.Path, out.Score*100)
	case engine.DecisionAsk:
		return head + "\n\n👍 Anotado, una pregunta más…"
	case engine.DecisionPredict:
		return fmt.Sprintf("%s\n\n🤔 Mi mejor opción: %s (%.0f%%)", head, out.Path, out.Score*100)
	}
	return head + "\n\n🤷 Sigo sin saber dónde va."
}

// followUp sends the next question of the same email right away.
func (b *Bot) followUp(ctx context.Context, id string, out *engine.Outcome) {
	if out.NeedsQuestion && out.Question != nil {
		b.sendQuestion(ctx, id, out)
	}
}
