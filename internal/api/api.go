// Package api exposes the engine over REST (§27). It is deliberately thin: it
// validates and translates HTTP, and leaves every decision to the engine.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/engine"
	"github.com/jaimesoad/mail-categorizer/internal/explain"
	"github.com/jaimesoad/mail-categorizer/internal/metrics"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

const maxBody = 8 << 20 // 8 MiB: enough for a raw .eml

// Server is the HTTP API.
type Server struct {
	eng   *engine.Engine
	token string // bearer token; "" disables auth
	log   *slog.Logger
	now   func() time.Time
}

// New creates a server. An empty token disables authentication (the default
// listen address is loopback, see config.api.addr).
func New(eng *engine.Engine, token string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{eng: eng, token: token, log: log, now: time.Now}
}

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /v1/emails/classify", s.classify)
	mux.HandleFunc("GET /v1/emails/{id}", s.get)
	mux.HandleFunc("DELETE /v1/emails/{id}", s.remove)
	mux.HandleFunc("POST /v1/emails/{id}/question", s.question)
	mux.HandleFunc("POST /v1/emails/{id}/answer", s.answer)
	mux.HandleFunc("POST /v1/emails/{id}/skip", s.skip)
	mux.HandleFunc("POST /v1/emails/{id}/feedback", s.feedback)
	mux.HandleFunc("GET /v1/emails/{id}/explain", s.explain)
	mux.HandleFunc("GET /v1/categories", s.categories)
	mux.HandleFunc("GET /v1/questions", s.questions)
	mux.HandleFunc("POST /v1/questions/{id}/approve", s.approveQuestion)
	mux.HandleFunc("POST /v1/questions/{id}/reject", s.rejectQuestion)
	mux.HandleFunc("GET /v1/metrics", s.metrics)
	return s.middleware(mux)
}

// Serve runs the server until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr: addr, Handler: s.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if s.token != "" && r.URL.Path != "/healthz" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="mailcat"`)
				writeErr(sw, 401, "missing or invalid bearer token")
				return
			}
		}
		next.ServeHTTP(sw, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", s.now().Sub(start).Milliseconds())
	})
}

// ---- wire types ----

type classifyRequest struct {
	ID         string            `json:"id"`
	Sender     string            `json:"sender"`
	SenderName string            `json:"sender_name"`
	To         []string          `json:"to"`
	Subject    string            `json:"subject"`
	Body       string            `json:"body"`
	MessageID  string            `json:"message_id"`
	InReplyTo  string            `json:"in_reply_to"`
	References []string          `json:"references"`
	ReceivedAt time.Time         `json:"received_at"`
	Headers    map[string]string `json:"headers"`
	RawEML     string            `json:"raw_eml"` // alternative to the fields above
}

type classification struct {
	Category string  `json:"category"`
	Path     string  `json:"path"`
	Score    float64 `json:"score"`
	Method   string  `json:"method"`
	Decision string  `json:"decision"`
}

type answerOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type questionView struct {
	ID              string         `json:"id"`
	Text            string         `json:"text"`
	Type            string         `json:"type"`
	Answers         []answerOption `json:"answers,omitempty"`
	InformationGain float64        `json:"information_gain,omitempty"`
}

type response struct {
	EmailID             string               `json:"email_id,omitempty"`
	SessionID           string               `json:"session_id,omitempty"`
	Classification      *classification      `json:"classification,omitempty"`
	Candidates          []candidateView      `json:"candidates,omitempty"`
	NeedsQuestion       bool                 `json:"needs_question"`
	Question            *questionView        `json:"question,omitempty"`
	NeedsManual         bool                 `json:"needs_manual,omitempty"`
	RequestConfirmation bool                 `json:"request_confirmation,omitempty"`
	Learned             *bool                `json:"learned,omitempty"`
	AlreadyProcessed    bool                 `json:"already_processed,omitempty"`
	Explanation         []string             `json:"explanation,omitempty"`
	ExplanationDetail   *explain.Explanation `json:"explanation_detail,omitempty"`
}

type candidateView struct {
	Category string  `json:"category"`
	Path     string  `json:"path"`
	Score    float64 `json:"score"`
}

func (s *Server) view(o *engine.Outcome, withLearned bool) response {
	r := response{
		EmailID: o.EmailID, SessionID: o.SessionID,
		Classification: &classification{Category: o.Category, Path: o.Path, Score: round(o.Score), Method: string(o.Method), Decision: o.Decision},
		NeedsQuestion:  o.NeedsQuestion, NeedsManual: o.NeedsManual, RequestConfirmation: o.RequestConfirmation,
		AlreadyProcessed: o.AlreadyProcessed, Explanation: o.Explanation.Reasons, ExplanationDetail: &o.Explanation,
	}
	for _, c := range o.Candidates {
		if c.Score < 0.01 {
			continue
		}
		r.Candidates = append(r.Candidates, candidateView{c.Category, s.eng.Tree().Path(c.Category), round(c.Score)})
	}
	if o.Question != nil {
		r.Question = toQuestionView(o.Question)
	}
	if withLearned {
		l := o.Learned
		r.Learned = &l
	}
	return r
}

func toQuestionView(q *engine.PendingQuestion) *questionView {
	v := &questionView{ID: q.ID, Text: q.Text, Type: string(q.Type), InformationGain: q.InformationGain}
	for _, a := range q.Answers {
		v.Answers = append(v.Answers, answerOption{a.ID, a.Label})
	}
	return v
}

func round(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// ---- handlers ----

func (s *Server) classify(w http.ResponseWriter, r *http.Request) {
	var req classifyRequest
	if !decode(w, r, &req) {
		return
	}
	var em email.Email
	if req.RawEML != "" {
		parsed, err := email.ParseEML([]byte(req.RawEML))
		if err != nil {
			writeErr(w, 400, "raw_eml: "+err.Error())
			return
		}
		em = parsed
		em.ID = req.ID
	} else {
		em = email.Email{
			ID: req.ID, From: email.ParseAddress(req.Sender), Subject: req.Subject, BodyText: req.Body,
			MessageID: req.MessageID, InReplyTo: req.InReplyTo, References: req.References, ReceivedAt: req.ReceivedAt.UTC(),
		}
		if req.SenderName != "" {
			em.From.Name = req.SenderName
		}
		for _, t := range req.To {
			em.To = append(em.To, email.ParseAddress(t))
		}
		if len(req.Headers) > 0 {
			em.Headers = map[string][]string{}
			for k, v := range req.Headers {
				em.Headers[strings.ToLower(k)] = []string{v}
			}
		}
	}
	out, err := s.eng.Process(r.Context(), em)
	if err != nil {
		s.fail(w, err)
		return
	}
	code := 200
	if !out.AlreadyProcessed {
		code = 201
	}
	writeJSON(w, code, s.view(out, false))
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	out, err := s.eng.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, s.view(out, false))
}

func (s *Server) question(w http.ResponseWriter, r *http.Request) {
	out, err := s.eng.NextQuestion(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if out.Question == nil {
		writeJSON(w, 200, map[string]any{"question": nil, "reason": "no question is needed: " + out.Decision})
		return
	}
	writeJSON(w, 200, map[string]any{"question": toQuestionView(out.Question)})
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		Answer     string `json:"answer"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.QuestionID == "" || req.Answer == "" {
		writeErr(w, 400, "question_id and answer are required")
		return
	}
	out, err := s.eng.Answer(r.Context(), r.PathValue("id"), req.QuestionID, req.Answer)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, s.view(out, true))
}

func (s *Server) skip(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	out, err := s.eng.Skip(r.Context(), r.PathValue("id"), req.QuestionID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, s.view(out, false))
}

func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Correct  *bool  `json:"correct"`
		Category string `json:"category"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Correct == nil {
		writeErr(w, 400, `"correct" (true/false) is required`)
		return
	}
	out, err := s.eng.Feedback(r.Context(), r.PathValue("id"), *req.Correct, req.Category)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, s.view(out, true))
}

func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	x, err := s.eng.Explain(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"explanation": x, "text": x.Lines()})
}

// remove purges an email: ?content_only=true erases text but keeps knowledge
// (§24); otherwise the whole record is deleted.
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	if r.URL.Query().Get("content_only") == "true" {
		err = s.eng.DB().PurgeContent(r.Context(), id, s.now().UTC())
	} else {
		err = s.eng.DB().DeleteEmail(r.Context(), id)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type categoryNode struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Children []categoryNode `json:"children,omitempty"`
}

func (s *Server) categories(w http.ResponseWriter, _ *http.Request) {
	t := s.eng.Tree()
	var build func(parent string) []categoryNode
	build = func(parent string) []categoryNode {
		var out []categoryNode
		for _, id := range t.Children(parent) {
			c, _ := t.Get(id)
			out = append(out, categoryNode{ID: id, Name: c.Name, Children: build(id)})
		}
		return out
	}
	writeJSON(w, 200, map[string]any{"categories": build(""), "unknown": "unknown"})
}

func (s *Server) questions(w http.ResponseWriter, r *http.Request) {
	type qv struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Feature   string `json:"feature"`
		Type      string `json:"type"`
		Source    string `json:"source"`
		Enabled   bool   `json:"enabled"`
		Validated bool   `json:"validated"`
	}
	qs, err := s.eng.DB().Questions(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	var out []qv
	for _, q := range qs {
		out = append(out, qv{q.ID, q.Text, q.Feature, string(q.Type), q.Source, !q.Disabled, q.Validated})
	}
	writeJSON(w, 200, map[string]any{"questions": out})
}

func (s *Server) approveQuestion(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ApproveQuestion(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "approved"})
}

func (s *Server) rejectQuestion(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.RejectQuestion(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "rejected"})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	rep, err := metrics.Compute(r.Context(), s.eng.DB())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

// ---- helpers ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, 413, "request body too large")
		} else {
			writeErr(w, 400, "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrInvalid):
		writeErr(w, 400, cleanErr(err, engine.ErrInvalid))
	case errors.Is(err, storage.ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.Is(err, engine.ErrConflict):
		writeErr(w, 409, cleanErr(err, engine.ErrConflict))
	default:
		s.log.Error("internal error", "err", err)
		writeErr(w, 500, "internal error")
	}
}

func cleanErr(err, base error) string {
	return strings.TrimPrefix(err.Error(), base.Error()+": ")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
