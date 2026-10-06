// Package config loads and validates the YAML configuration (§28, §29): the
// category tree, the question catalogue, deterministic rules and tunables.
// Everything user-facing is configurable without recompiling.
package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jaimesoad/mail-categorizer/internal/categories"
	"github.com/jaimesoad/mail-categorizer/internal/classifier"
	"github.com/jaimesoad/mail-categorizer/internal/questions"
	"github.com/jaimesoad/mail-categorizer/internal/rules"
)

//go:embed default.yaml
var defaultYAML []byte

// DefaultYAML returns the embedded example configuration (used by `mailcat init`).
func DefaultYAML() []byte { return append([]byte(nil), defaultYAML...) }

// Thresholds mirrors §15.
type Thresholds struct {
	Auto      float64 `yaml:"auto"`       // >= auto: classify automatically
	Ask       float64 `yaml:"ask"`        // >= ask: good prediction; below: low confidence
	MinMargin float64 `yaml:"min_margin"` // required gap to the runner-up for automatic classification
}

// Similarity configures the TF-IDF k-NN signal (§22).
type Similarity struct {
	Enabled   bool    `yaml:"enabled"`
	K         int     `yaml:"k"`
	MinSim    float64 `yaml:"min_similarity"`
	MaxWeight float64 `yaml:"max_weight"`
}

// Confirmation configures sampled confirmation requests (§16).
type Confirmation struct {
	BaseRate float64 `yaml:"base_rate"` // probability of asking to confirm an automatic classification
	Seed     string  `yaml:"seed"`      // keeps sampling deterministic yet configurable
}

// QuestionParams configures the selector (§13).
type QuestionParams struct {
	MinGain float64 `yaml:"min_gain"`
}

// Privacy controls what is stored (§24).
type Privacy struct {
	StoreBody    bool `yaml:"store_body"`     // false: bodies are never persisted (disables similarity)
	MaxBodyChars int  `yaml:"max_body_chars"` // normalization limit
}

// API configures the REST server.
type API struct {
	Addr     string `yaml:"addr"`
	TokenEnv string `yaml:"token_env"` // name of the env var holding the bearer token ("" = no auth)
}

// LLM configures the optional language model (§23). Off by default.
type LLM struct {
	Enabled       bool          `yaml:"enabled"`
	BaseURL       string        `yaml:"base_url"` // OpenAI-compatible, e.g. http://localhost:11434/v1 (Ollama)
	Model         string        `yaml:"model"`
	APIKeyEnv     string        `yaml:"api_key_env"`
	AllowExternal bool          `yaml:"allow_external"` // required for any non-loopback base_url
	SendBody      bool          `yaml:"send_body"`      // false: only features and candidates leave the process
	Timeout       time.Duration `yaml:"timeout"`
	// ReasoningEffort ("low"|"medium"|"high") is forwarded to reasoning models such as gpt-oss.
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// Telegram configures the question/confirmation bot. Off by default.
type Telegram struct {
	Enabled        bool   `yaml:"enabled"`
	TokenEnv       string `yaml:"token_env"`         // env var holding the bot token (from @BotFather)
	ChatID         int64  `yaml:"chat_id"`           // the only chat the bot talks to and obeys
	ShowSubject    bool   `yaml:"show_subject"`      // false: only the sender's domain is sent to Telegram
	MaxOpenPrompts int    `yaml:"max_open_prompts"`  // do not flood: at most N unanswered prompts
	LLMAnswers     bool   `yaml:"llm_answers_first"` // let the local LLM try to answer questions before bothering you
}

// Sync configures how categories are mirrored as labels in the external mailbox.
type Sync struct {
	LabelPrefix string `yaml:"label_prefix"` // Gmail label root: "mailcat" => mailcat/Finanzas/Banco
}

// CategoryCfg is a category as written in YAML.
type CategoryCfg struct {
	ID          string `yaml:"id"`
	Parent      string `yaml:"parent"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Disabled    bool   `yaml:"disabled"`
}

// AffinityRow accepts either a bare number (P(yes) of a boolean question) or an
// answer->probability map (choice questions).
type AffinityRow map[string]float64

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *AffinityRow) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var f float64
		if err := n.Decode(&f); err != nil {
			return err
		}
		*r = AffinityRow{questions.Yes: f}
		return nil
	}
	m := map[string]float64{}
	if err := n.Decode(&m); err != nil {
		return err
	}
	*r = m
	return nil
}

// QuestionCfg is a question as written in YAML.
type QuestionCfg struct {
	questions.Question `yaml:",inline"`
	Affinity           map[string]AffinityRow `yaml:"affinity"`
}

// Config is the whole configuration.
type Config struct {
	Database     string                 `yaml:"database"`
	Thresholds   Thresholds             `yaml:"thresholds"`
	MaxQuestions int                    `yaml:"max_questions_per_email"`
	Similarity   Similarity             `yaml:"similarity"`
	Learning     struct{ Enabled bool } `yaml:"learning"`
	Confirmation Confirmation           `yaml:"confirmation"`
	Selector     QuestionParams         `yaml:"question_selection"`
	Privacy      Privacy                `yaml:"privacy"`
	API          API                    `yaml:"api"`
	LLM          LLM                    `yaml:"llm"`
	Telegram     Telegram               `yaml:"telegram"`
	Sync         Sync                   `yaml:"sync"`

	Categories []CategoryCfg `yaml:"categories"`
	Questions  []QuestionCfg `yaml:"questions"`
	Rules      []rules.Rule  `yaml:"rules"`
}

// Defaults returns the built-in tunables (no categories, questions or rules).
func Defaults() Config {
	return Config{
		Database:     "mailcat.db",
		Thresholds:   Thresholds{Auto: 0.85, Ask: 0.60, MinMargin: 0.20},
		MaxQuestions: 5,
		Similarity:   Similarity{Enabled: true, K: 10, MinSim: 0.25, MaxWeight: 0.75},
		Confirmation: Confirmation{BaseRate: 0.10, Seed: "mailcat"},
		Selector:     QuestionParams{MinGain: 0.05},
		Privacy:      Privacy{StoreBody: true, MaxBodyChars: 20000},
		API:          API{Addr: "127.0.0.1:8080", TokenEnv: "MAILCAT_TOKEN"},
		LLM:          LLM{Timeout: 120 * time.Second},
		Telegram:     Telegram{TokenEnv: "TELEGRAM_BOT_TOKEN", ShowSubject: true, MaxOpenPrompts: 5},
		Sync:         Sync{LabelPrefix: "mailcat"},
		Learning:     struct{ Enabled bool }{Enabled: true},
	}
}

// Load reads a YAML file over the defaults, rejecting unknown keys.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes YAML bytes over the defaults.
func Parse(b []byte) (*Config, error) {
	c := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && err.Error() != "EOF" {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

// LoadOrDefault loads path, or the embedded example configuration when path is "".
func LoadOrDefault(path string) (*Config, error) {
	if path == "" {
		return Parse(defaultYAML)
	}
	return Load(path)
}

// Built holds the validated, compiled artifacts derived from a Config.
type Built struct {
	Config     *Config
	Tree       *categories.Tree
	Categories []categories.Category
	Questions  []questions.Question
	Rules      *rules.Engine
}

// Build validates the configuration and compiles categories, questions and rules (M15).
func (c *Config) Build() (*Built, error) {
	if t := c.Thresholds; !(t.Ask > 0 && t.Ask <= t.Auto && t.Auto <= 1) || t.MinMargin < 0 || t.MinMargin > 1 {
		return nil, fmt.Errorf("thresholds: need 0 < ask <= auto <= 1 and 0 <= min_margin <= 1 (got ask=%v auto=%v margin=%v)", t.Ask, t.Auto, t.MinMargin)
	}
	if c.MaxQuestions < 0 || c.MaxQuestions > 20 {
		return nil, fmt.Errorf("max_questions_per_email must be in [0,20]")
	}
	b := &Built{Config: c}
	for _, cc := range c.Categories {
		b.Categories = append(b.Categories, categories.Category{
			ID: cc.ID, ParentID: cc.Parent, Name: cc.Name, Description: cc.Description, Enabled: !cc.Disabled,
		})
	}
	var err error
	if b.Tree, err = categories.NewTree(b.Categories); err != nil {
		return nil, fmt.Errorf("categories: %w", err)
	}
	b.Categories = b.Tree.All() // includes the reserved Unknown category

	seen := map[string]bool{}
	for _, qc := range c.Questions {
		q := qc.Question
		q.Affinity = map[string]map[string]float64{}
		for cat, row := range qc.Affinity {
			if !b.Tree.Has(cat) {
				return nil, fmt.Errorf("question %q: affinity for unknown category %q", q.ID, cat)
			}
			q.Affinity[cat] = row
		}
		q.Normalize()
		q.Validated = true
		if seen[q.ID] {
			return nil, fmt.Errorf("duplicate question id %q", q.ID)
		}
		seen[q.ID] = true
		if err := q.Validate(); err != nil {
			return nil, err
		}
		b.Questions = append(b.Questions, q)
	}

	for _, r := range c.Rules {
		if !b.Tree.Has(r.Category) {
			return nil, fmt.Errorf("rule %q: unknown category %q", r.ID, r.Category)
		}
	}
	if b.Rules, err = rules.NewEngine(c.Rules); err != nil {
		return nil, err
	}
	return b, nil
}

// ClassifierThresholds converts to the classifier type.
func (c *Config) ClassifierThresholds() classifier.Thresholds {
	return classifier.Thresholds{High: c.Thresholds.Auto, Medium: c.Thresholds.Ask, MinMargin: c.Thresholds.MinMargin}
}
