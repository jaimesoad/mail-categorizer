// Package llm contains the optional language-model components (§14, §23). The
// system works without any of this: nothing here is imported by the core
// pipeline. The model is only used for what is hard to do deterministically
// (intent detection on ambiguous mail, proposing a separating question) and its
// output is always validated before use. Privacy (§24): only features and
// redacted snippets are ever sent, and a non-local endpoint needs explicit opt-in.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jaimesoad/mail-categorizer/internal/config"
)

// Request is a single-turn prompt that must be answered with a JSON object.
type Request struct {
	System string
	User   string
}

// Provider is the model abstraction.
type Provider interface {
	// Complete returns the model's raw text answer.
	Complete(ctx context.Context, req Request) (string, error)
}

// OpenAICompat talks to any OpenAI-compatible /chat/completions endpoint
// (OpenAI, Ollama, llama.cpp, vLLM, LM Studio...).
type OpenAICompat struct {
	BaseURL string
	Model   string
	APIKey  string
	Client  *http.Client
}

// FromConfig builds the provider described by the configuration, enforcing the
// privacy rule: non-loopback endpoints require llm.allow_external.
func FromConfig(c config.LLM) (*OpenAICompat, error) {
	if !c.Enabled {
		return nil, fmt.Errorf("llm is disabled")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("llm.base_url %q is not a valid URL", c.BaseURL)
	}
	if !isLoopback(u.Hostname()) && !c.AllowExternal {
		return nil, fmt.Errorf("llm.base_url points to an external host (%s): set llm.allow_external: true to confirm that features may leave this machine", u.Hostname())
	}
	if c.Model == "" {
		return nil, fmt.Errorf("llm.model is required")
	}
	key := ""
	if c.APIKeyEnv != "" {
		key = os.Getenv(c.APIKeyEnv)
	}
	t := c.Timeout
	if t <= 0 {
		t = 20 * time.Second
	}
	return &OpenAICompat{BaseURL: strings.TrimRight(c.BaseURL, "/"), Model: c.Model, APIKey: key, Client: &http.Client{Timeout: t}}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Complete implements Provider.
func (p *OpenAICompat) Complete(ctx context.Context, req Request) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":           p.Model,
		"temperature":     0,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
	})
	if err != nil {
		return "", err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	cl := p.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(hr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: unexpected response")
	}
	return out.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// extractJSON returns the outermost {...} of s, tolerating code fences and prose.
func extractJSON(s string) (string, error) {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return "", fmt.Errorf("llm: no JSON object in answer")
	}
	return s[i : j+1], nil
}

var (
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reURL    = regexp.MustCompile(`https?://\S+`)
	reDigits = regexp.MustCompile(`\d[\d\s().\-]{5,}\d`)
)

// Redact removes the most sensitive tokens (addresses, URLs, long numbers)
// before any text is sent to a model.
func Redact(s string) string {
	s = reURL.ReplaceAllString(s, "[url]")
	s = reEmail.ReplaceAllString(s, "[email]")
	s = reDigits.ReplaceAllString(s, "[num]")
	return s
}
