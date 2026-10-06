package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/rules"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

const chat = int64(4242)

var bg = context.Background()

// fakeTG is a tiny Telegram Bot API.
type fakeTG struct {
	mu       sync.Mutex
	sent     []map[string]any // sendMessage payloads
	edits    []map[string]any
	acks     []string
	updates  []Update // queued for getUpdates
	nextID   int64
	failSend bool
}

func (f *fakeTG) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p map[string]any
		json.Unmarshal(body, &p)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		defer f.mu.Unlock()
		ok := func(v any) { json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v}) }
		switch method {
		case "sendMessage":
			if f.failSend {
				json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": "boom"})
				return
			}
			f.sent = append(f.sent, p)
			f.nextID++
			ok(map[string]any{"message_id": f.nextID, "chat": map[string]any{"id": p["chat_id"]}})
		case "editMessageText":
			f.edits = append(f.edits, p)
			ok(true)
		case "answerCallbackQuery":
			f.acks = append(f.acks, fmt.Sprint(p["text"]))
			ok(true)
		case "getUpdates":
			ups := f.updates
			f.updates = nil
			if len(ups) == 0 {
				time.Sleep(20 * time.Millisecond)
			}
			ok(ups)
		default:
			t.Errorf("unexpected method %s", method)
		}
	})
}

func (f *fakeTG) lastSent() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return nil
	}
	return f.sent[len(f.sent)-1]
}

func (f *fakeTG) sentCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

// buttons returns the callback_data of every button of a payload.
func buttons(p map[string]any) []string {
	var out []string
	rm, _ := p["reply_markup"].(map[string]any)
	rows, _ := rm["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any)["callback_data"].(string))
		}
	}
	return out
}

func find(btns []string, prefix string) string {
	for _, b := range btns {
		if strings.HasPrefix(b, prefix) {
			return b
		}
	}
	return ""
}

type fixture struct {
	eng *engine.Engine
	bot *Bot
	tg  *fakeTG
	srv *httptest.Server
}

func setup(t *testing.T, chatID int64, mutate ...func(*Options)) *fixture {
	t.Helper()
	cfg, _ := config.LoadOrDefault("")
	cfg.Rules = append(cfg.Rules,
		rules.Rule{ID: "pay-bank", Category: "finance.bank", Weight: 0.43, When: rules.Condition{Feature: "subject.tokens", Value: "payment"}},
		rules.Rule{ID: "pay-purchase", Category: "finance.purchase", Weight: 0.41, When: rules.Condition{Feature: "subject.tokens", Value: "payment"}},
	)
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	db, _ := storage.Open(":memory:")
	t.Cleanup(func() { db.Close() })
	eng, err := engine.New(bg, db, built)
	if err != nil {
		t.Fatal(err)
	}
	tg := &fakeTG{}
	srv := httptest.NewServer(tg.handler(t))
	t.Cleanup(srv.Close)
	opt := Options{ChatID: chatID, ShowSubject: true, MaxOpenPrompts: 5, SweepEvery: time.Hour}
	for _, m := range mutate {
		m(&opt)
	}
	bot := New(&Client{Token: "SECRET-TOKEN", BaseURL: srv.URL, HTTP: srv.Client()}, eng, opt)
	return &fixture{eng, bot, tg, srv}
}

func (f *fixture) ambiguous(t *testing.T, id string) {
	t.Helper()
	out, err := f.eng.Process(bg, email.Email{ID: id, From: email.ParseAddress("Tienda <store@store.example>"),
		Subject: "Payment received " + id, BodyText: "Payment received for your order #123."})
	if err != nil || !out.NeedsQuestion {
		t.Fatalf("setup %s: %+v %v", id, out, err)
	}
}

func cbUpdate(id int64, data string, msgID int64, text string) Update {
	u := Update{ID: id}
	u.Callback = &struct {
		ID      string             `json:"id"`
		Data    string             `json:"data"`
		From    struct{ ID int64 } `json:"from"`
		Message *Msg               `json:"message"`
	}{ID: fmt.Sprint("cb", id), Data: data, Message: &Msg{ID: msgID, Text: text}}
	u.Callback.Message.Chat.ID = chat
	return u
}

func TestQuestionIsSentAndAnsweredWithButtons(t *testing.T) {
	f := setup(t, chat)
	f.ambiguous(t, "e1")
	f.bot.Sweep(bg)
	msg := f.tg.lastSent()
	if msg == nil {
		t.Fatal("nothing sent")
	}
	text := msg["text"].(string)
	if !strings.Contains(text, "store@store.example") || !strings.Contains(text, "Payment received e1") || !strings.Contains(text, "?") {
		t.Errorf("the message must say which email and what is asked: %q", text)
	}
	if _, has := msg["parse_mode"]; has {
		t.Error("plain text only: email content must never be parsed as markup")
	}
	btns := buttons(msg)
	yes := find(btns, "q:")
	if yes == "" || find(btns, "s:") == "" || find(btns, "m:") == "" {
		t.Fatalf("buttons: %v", btns)
	}
	for _, b := range btns {
		if len(b) > 64 {
			t.Errorf("callback_data over 64 bytes: %q", b)
		}
	}
	// no duplicate prompt on the next sweep
	f.bot.Sweep(bg)
	if f.tg.sentCount() != 1 {
		t.Fatalf("sweep must not resend: %d", f.tg.sentCount())
	}
	// press the "yes"-ish button for the question (answer id is the suffix)
	f.bot.Handle(bg, cbUpdate(1, yes, 1, text))
	if len(f.tg.edits) != 1 || !strings.Contains(fmt.Sprint(f.tg.edits[0]["text"]), "e1") {
		t.Fatalf("message should be edited with the result: %+v", f.tg.edits)
	}
	if len(f.tg.acks) != 1 || f.tg.acks[0] == "No autorizado" {
		t.Errorf("acks: %v", f.tg.acks)
	}
	ints, _ := f.eng.DB().Interactions(bg, "e1")
	if len(ints) == 0 || ints[0].Kind != storage.KindAnswer || ints[0].Actor != "user" {
		t.Fatalf("the answer must be recorded as the user's: %+v", ints)
	}
	// pressing the same button again is harmless
	f.bot.Handle(bg, cbUpdate(2, yes, 1, text))
	if last := f.tg.acks[len(f.tg.acks)-1]; last != "Ya respondido" {
		t.Errorf("double press: %q", last)
	}
}

func TestUnauthorizedChatsAreIgnored(t *testing.T) {
	f := setup(t, chat)
	f.ambiguous(t, "e1")
	f.bot.Sweep(bg)
	btns := buttons(f.tg.lastSent())
	attacker := cbUpdate(1, find(btns, "q:"), 1, "x")
	attacker.Callback.Message.Chat.ID = 666
	f.bot.Handle(bg, attacker)
	if ints, _ := f.eng.DB().Interactions(bg, "e1"); len(ints) != 0 {
		t.Fatal("a stranger answered a question")
	}
	if f.tg.acks[0] != "No autorizado" {
		t.Errorf("acks: %v", f.tg.acks)
	}
	// strangers' commands get no reply and no data
	before := f.tg.sentCount()
	m := Msg{Text: "/metricas"}
	m.Chat.ID = 666
	f.bot.Handle(bg, Update{Message: &m})
	if f.tg.sentCount() != before {
		t.Error("the bot answered an unauthorized chat")
	}
}

func TestStartRevealsChatIDOnlyWhileUnconfigured(t *testing.T) {
	f := setup(t, 0)
	f.ambiguous(t, "e1")
	f.bot.Sweep(bg) // chat id 0: must not send anything
	if f.tg.sentCount() != 0 {
		t.Fatal("an unconfigured bot must not message anyone")
	}
	m := Msg{Text: "/start"}
	m.Chat.ID = 777
	f.bot.Handle(bg, Update{Message: &m})
	if got := fmt.Sprint(f.tg.lastSent()["text"]); !strings.Contains(got, "777") {
		t.Errorf("should tell the chat id: %q", got)
	}
	// other commands do nothing while unconfigured
	m2 := Msg{Text: "/metricas"}
	m2.Chat.ID = 777
	n := f.tg.sentCount()
	f.bot.Handle(bg, Update{Message: &m2})
	if f.tg.sentCount() != n {
		t.Error("only /start is served before configuration")
	}
}

func TestMaxOpenPromptsAvoidsFlooding(t *testing.T) {
	f := setup(t, chat, func(o *Options) { o.MaxOpenPrompts = 2 })
	for i := 1; i <= 5; i++ {
		f.ambiguous(t, fmt.Sprintf("e%d", i))
	}
	f.bot.Sweep(bg)
	f.bot.Sweep(bg)
	if f.tg.sentCount() != 2 {
		t.Fatalf("must stop at max_open_prompts: sent %d", f.tg.sentCount())
	}
}

func TestPrivacyShowSubjectOff(t *testing.T) {
	f := setup(t, chat, func(o *Options) { o.ShowSubject = false })
	f.ambiguous(t, "e1")
	f.bot.Sweep(bg)
	text := fmt.Sprint(f.tg.lastSent()["text"])
	if strings.Contains(text, "Payment received") || strings.Contains(text, "store@store.example") || !strings.Contains(text, "store.example") {
		t.Errorf("only the sender's domain may reach Telegram: %q", text)
	}
}

func TestPredictionConfirmAndCorrectFlow(t *testing.T) {
	f := setup(t, chat, func(o *Options) { o.MaxOpenPrompts = 10 })
	// resolve to "predict" by exhausting questions
	f.ambiguous(t, "p1")
	for guard := 0; guard < 6; guard++ {
		out, _ := f.eng.Get(bg, "p1")
		if !out.NeedsQuestion {
			break
		}
		f.eng.Skip(bg, "p1", out.Question.ID)
	}
	out, _ := f.eng.Get(bg, "p1")
	if out.NeedsQuestion {
		t.Skip("scenario still asking")
	}
	f.bot.Sweep(bg)
	msg := f.tg.lastSent()
	if msg == nil {
		t.Fatalf("a %s decision needs a prompt", out.Decision)
	}
	btns := buttons(msg)
	if out.NeedsManual {
		// "unknown": straight to the category keyboard
		pick := find(btns, "c:")
		if pick == "" {
			t.Fatalf("category keyboard expected: %v", btns)
		}
		f.bot.Handle(bg, cbUpdate(1, pick, 1, fmt.Sprint(msg["text"])))
	} else {
		no := find(btns, "k:")
		// "Cambiar" -> category keyboard
		for _, b := range btns {
			if strings.HasSuffix(b, ":n") {
				no = b
			}
		}
		f.bot.Handle(bg, cbUpdate(1, no, 1, fmt.Sprint(msg["text"])))
		if len(f.tg.edits) == 0 {
			t.Fatal("expected the keyboard to be edited into a category list")
		}
		kb := buttons(f.tg.edits[len(f.tg.edits)-1])
		pick := find(kb, "c:")
		if pick == "" {
			t.Fatalf("category list expected: %v", kb)
		}
		f.bot.Handle(bg, cbUpdate(2, pick, 1, fmt.Sprint(msg["text"])))
	}
	labels, _ := f.eng.DB().Labels(bg)
	if labels["p1"].Source != "user" {
		t.Fatalf("a category chosen in Telegram is a human label: %+v", labels["p1"])
	}
}

type fakeAnswerer struct {
	ans string
	ok  bool
}

func (a fakeAnswerer) Answer(context.Context, email.Email, questions.Question) (string, bool, error) {
	return a.ans, a.ok, nil
}

func TestLocalLLMAnswersFirstAndFallsBackToThePerson(t *testing.T) {
	f := setup(t, chat, func(o *Options) { o.Answerer = fakeAnswerer{ans: "yes", ok: true} })
	f.ambiguous(t, "e1")
	f.bot.Sweep(bg)
	ints, _ := f.eng.DB().Interactions(bg, "e1")
	if len(ints) == 0 || ints[0].Actor != "llm" {
		t.Fatalf("the LLM should have answered: %+v", ints)
	}
	g := setup(t, chat, func(o *Options) { o.Answerer = fakeAnswerer{ok: false} })
	g.ambiguous(t, "e2")
	g.bot.Sweep(bg)
	if g.tg.sentCount() != 1 {
		t.Fatal("when the LLM is unsure the person must be asked")
	}
}

func TestDeliveryFailureRetriesOnNextSweep(t *testing.T) {
	f := setup(t, chat)
	f.ambiguous(t, "e1")
	f.tg.failSend = true
	f.bot.Sweep(bg)
	if n, _ := f.eng.DB().OpenPrompts(bg); n != 0 {
		t.Fatalf("a prompt that was never delivered must not count as open: %d", n)
	}
	f.tg.failSend = false
	f.bot.Sweep(bg)
	if f.tg.sentCount() != 1 {
		t.Fatal("should deliver on the next sweep")
	}
}

func TestTokenNeverAppearsInErrors(t *testing.T) {
	c := &Client{Token: "SECRET-TOKEN", BaseURL: "http://127.0.0.1:1"}
	_, err := c.GetUpdates(bg, 0, 0)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("token leaked into error: %v", err)
	}
}

func TestRunPollsHandlesUpdatesAndStops(t *testing.T) {
	f := setup(t, chat, func(o *Options) { o.SweepEvery = 30 * time.Millisecond })
	f.ambiguous(t, "e1")
	ctx, cancel := context.WithTimeout(bg, 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.bot.Run(ctx) }()

	// wait for the question, then press a button through getUpdates
	var msg map[string]any
	for i := 0; i < 100 && msg == nil; i++ {
		time.Sleep(20 * time.Millisecond)
		msg = f.tg.lastSent()
	}
	if msg == nil {
		t.Fatal("Run never swept")
	}
	f.tg.mu.Lock()
	f.tg.updates = append(f.tg.updates, cbUpdate(10, find(buttons(msg), "q:"), 1, fmt.Sprint(msg["text"])))
	f.tg.mu.Unlock()
	for i := 0; i < 100; i++ {
		time.Sleep(20 * time.Millisecond)
		if ints, _ := f.eng.DB().Interactions(bg, "e1"); len(ints) > 0 {
			break
		}
	}
	if ints, _ := f.eng.DB().Interactions(bg, "e1"); len(ints) == 0 {
		t.Fatal("the polled button press was not handled")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled && err != context.DeadlineExceeded {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestAuditCommandSendsATeacherLabel(t *testing.T) {
	f := setup(t, chat)
	f.eng.Process(bg, email.Email{ID: "a1", From: email.ParseAddress("x@acme.example"), Subject: "Hola", BodyText: "algo"})
	if _, err := f.eng.Move(engine.WithActor(bg, engine.ActorLLM), "a1", "work"); err != nil {
		t.Fatal(err)
	}
	m := Msg{Text: "/auditar"}
	m.Chat.ID = chat
	f.bot.Handle(bg, Update{Message: &m})
	msg := f.tg.lastSent()
	if msg == nil || !strings.Contains(fmt.Sprint(msg["text"]), "asistente local") {
		t.Fatalf("audit prompt expected: %+v", msg)
	}
	f.bot.Handle(bg, cbUpdate(1, find(buttons(msg), "k:"), 1, fmt.Sprint(msg["text"])))
	if l, _ := f.eng.DB().Labels(bg); l["a1"].Source != "user" {
		t.Fatalf("confirming an audited label makes it a human label: %+v", l["a1"])
	}
}
