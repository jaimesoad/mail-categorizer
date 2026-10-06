// Package engine wires the independent components (extraction, classification,
// question selection, learning, storage) into the interactive workflow:
// Process → (NextQuestion → Answer)* → Feedback. It owns the session state
// machine of §30 and the decision rules of §15 and §41.
package engine

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/config"
	"github.com/jaimesoad/mail-categorizer/internal/email"
	"github.com/jaimesoad/mail-categorizer/internal/features"
	"github.com/jaimesoad/mail-categorizer/internal/learning"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/similarity"
	"github.com/jaimesoad/mail-categorizer/internal/storage"
)

// Errors returned by the engine; the API maps them to HTTP statuses.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("conflict")
	ErrNotFound = storage.ErrNotFound
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Enricher adds extra features, e.g. an optional LLM intent detector (§23).
// Failures must never break classification: the engine ignores its errors.
type Enricher interface {
	Enrich(ctx context.Context, e email.Email, set *features.Set) ([]features.Feature, error)
}

// QuestionProposer generates a question when no existing one separates the
// candidates (§14). Proposed questions are never enabled permanently without
// operator approval.
type QuestionProposer interface {
	Propose(ctx context.Context, in questions.ProposeInput) (*questions.Question, error)
}

// Engine is safe for concurrent use.
type Engine struct {
	db        *storage.DB
	cfg       *config.Config
	built     *config.Built
	tree      *categories.Tree
	extractor features.Extractor
	cls       *classifier.Classifier
	sel       *questions.Selector
	learner   *learning.Learner
	enricher  Enricher
	proposer  QuestionProposer
	now       func() time.Time
	locks     *lockSet

	sim *simCache // shared by views created with WithoutLearning
}

type simCache struct {
	mu    sync.Mutex
	ver   uint64
	index similarity.Index
	init  bool
}

// Option customizes an Engine.
type Option func(*Engine)

// WithClock injects a clock (tests, evaluation).
func WithClock(now func() time.Time) Option { return func(e *Engine) { e.now = now } }

// WithEnricher plugs an optional feature enricher.
func WithEnricher(x Enricher) Option { return func(e *Engine) { e.enricher = x } }

// WithProposer plugs an optional question generator.
func WithProposer(p QuestionProposer) Option { return func(e *Engine) { e.proposer = p } }

// New builds an engine over a database and a validated configuration. It
// synchronizes categories and configured questions into the database.
func New(ctx context.Context, db *storage.DB, built *config.Built, opts ...Option) (*Engine, error) {
	e := &Engine{
		db: db, cfg: built.Config, built: built, tree: built.Tree,
		now: func() time.Time { return time.Now().UTC() }, locks: newLockSet(), sim: &simCache{},
	}
	for _, o := range opts {
		o(e)
	}
	if err := db.SyncCategories(ctx, built.Categories); err != nil {
		return nil, err
	}
	if err := e.syncQuestions(ctx); err != nil {
		return nil, err
	}

	e.extractor = features.Default(db)
	signals := []classifier.Signal{
		built.Rules,
		learning.Signal{Repo: db, Name: e.tree.Path},
	}
	if built.Config.Similarity.Enabled && built.Config.Privacy.StoreBody {
		s := built.Config.Similarity
		signals = append(signals, similarity.Signal{
			Load: e.loadIndex, K: s.K, MinSim: s.MinSim, MaxWeight: s.MaxWeight, Name: e.tree.Path,
		})
	}
	e.cls = &classifier.Classifier{Tree: e.tree, Signals: signals, Thresholds: built.Config.ClassifierThresholds()}

	params := questions.DefaultParams()
	if g := built.Config.Selector.MinGain; g > 0 {
		params.MinGain = g
	}
	e.sel = &questions.Selector{Tree: e.tree, Stats: db, Params: params}
	if err := e.ReloadQuestions(ctx); err != nil {
		return nil, err
	}
	if built.Config.Learning.Enabled {
		e.learner = &learning.Learner{Repo: db, Tree: e.tree, Now: e.now}
	}
	return e, nil
}

// syncQuestions stores the configured questions and disables stored
// config-sourced ones that disappeared from the configuration.
func (e *Engine) syncQuestions(ctx context.Context) error {
	inConfig := map[string]bool{}
	for _, q := range e.built.Questions {
		inConfig[q.ID] = true
		if err := e.db.SaveQuestion(ctx, q, false); err != nil {
			return err
		}
	}
	stored, err := e.db.Questions(ctx)
	if err != nil {
		return err
	}
	for _, q := range stored {
		if q.Source == questions.SourceConfig && !inConfig[q.ID] && !q.Disabled {
			if err := e.db.SetQuestionState(ctx, q.ID, true, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReloadQuestions refreshes the selector's catalogue from the database (after
// approving a generated question, for instance).
func (e *Engine) ReloadQuestions(ctx context.Context) error {
	qs, err := e.db.Questions(ctx)
	if err != nil {
		return err
	}
	e.sel.Questions = qs
	return nil
}

// WithoutLearning returns a view of the engine that never updates learned
// rules. Used by offline evaluation on held-out data (§35).
func (e *Engine) WithoutLearning() *Engine {
	c := *e
	c.learner = nil
	return &c
}

// Tree exposes the category tree.
func (e *Engine) Tree() *categories.Tree { return e.tree }

// DB exposes the database (metrics, CLI inspection).
func (e *Engine) DB() *storage.DB { return e.db }

// Config returns the active configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

// Questions returns the current question catalogue.
func (e *Engine) Questions() []questions.Question { return e.sel.Questions }

func (e *Engine) loadIndex(ctx context.Context) (similarity.Index, error) {
	c := e.sim
	c.mu.Lock()
	defer c.mu.Unlock()
	v := e.db.LabelVersion()
	if c.init && c.ver == v {
		return c.index, nil
	}
	docs, err := e.db.LabeledDocs(ctx, 4000)
	if err != nil {
		return nil, err
	}
	c.index, c.ver, c.init = similarity.Build(docs), v, true
	return c.index, nil
}

// lockSet serializes the read-modify-write cycles of one email.
type lockSet struct{ m [64]sync.Mutex }

func newLockSet() *lockSet { return &lockSet{} }

func (l *lockSet) lock(id string) func() {
	h := fnv.New32a()
	h.Write([]byte(id))
	mu := &l.m[h.Sum32()%uint32(len(l.m))]
	mu.Lock()
	return mu.Unlock
}

// shouldConfirm samples automatic classifications for a "was this right?"
// prompt (§16). Deterministic per email; lower confidence is sampled more.
func (e *Engine) shouldConfirm(emailID string, score float64) bool {
	base := e.cfg.Confirmation.BaseRate
	if base <= 0 {
		return false
	}
	high := e.cfg.Thresholds.Auto
	p := base
	if high < 1 {
		p = base * (2 - (score-high)/(1-high)) // base..2*base as score goes 1..high
	}
	h := fnv.New32a()
	h.Write([]byte(e.cfg.Confirmation.Seed + "|" + emailID))
	return float64(h.Sum32())/float64(1<<32) < p
}

// ApproveQuestion validates and enables a (typically LLM-generated) question so
// the selector may use it for any email from now on (§14).
func (e *Engine) ApproveQuestion(ctx context.Context, id string) error {
	if err := e.db.SetQuestionState(ctx, id, true, true); err != nil {
		return err
	}
	return e.ReloadQuestions(ctx)
}

// RejectQuestion disables a question and keeps it out of the catalogue.
func (e *Engine) RejectQuestion(ctx context.Context, id string) error {
	if err := e.db.SetQuestionState(ctx, id, false, false); err != nil {
		return err
	}
	return e.ReloadQuestions(ctx)
}

// Actor says who is feeding answers and labels into the engine.
type Actor string

const (
	// ActorUser is a person: full weight.
	ActorUser Actor = "user"
	// ActorLLM is the local LLM "teacher" used to pre-train or to answer
	// questions on the user's behalf: lower trust, never overrides a person.
	ActorLLM Actor = "llm"
)

type actorKey struct{}

// WithActor returns a context whose answers/labels are attributed to a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

func actorOf(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok && a != "" {
		return a
	}
	return ActorUser
}

type quietKey struct{}

// WithQuiet marks processing as a bulk import: the resulting sessions are kept
// out of the notification flow (Telegram) and the label sync.
func WithQuiet(ctx context.Context) context.Context { return context.WithValue(ctx, quietKey{}, true) }

func isQuiet(ctx context.Context) bool { q, _ := ctx.Value(quietKey{}).(bool); return q }
