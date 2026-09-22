// Package ai is a minimal client for the Anthropic Messages API. It is used for
// one bounded, synchronous job: deciding where an incoming document belongs.
// Nothing here is a background job or an agent loop; every call has a timeout
// and a small token budget.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/remarqable/tmpio/internal/platform/config"
)

// ErrNotConfigured is returned when a call is attempted with no model
// credential anywhere. Callers fall back to the heuristic rather than failing
// the request.
var ErrNotConfigured = errors.New("no model credential is configured")

// Completer is the narrow interface models depend on, so tests can stub it.
type Completer interface {
	// Complete sends one user message under a system prompt and returns the
	// text of the reply with token usage.
	Complete(ctx context.Context, system, user string, maxTokens int) (Result, error)
	// Model names the model in use, for audit rows.
	Model() string
	// Enabled reports whether a call would reach a model at all. It takes a
	// context because the credential can live in the database and change
	// while the server is running.
	Enabled(ctx context.Context) bool
}

// Result is a completed reply.
type Result struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// Client calls the Anthropic Messages API.
type Client struct {
	cfg  config.AI
	http *http.Client
}

// New returns a client, or nil when no API key is configured.
func New(cfg config.AI) *Client {
	if !cfg.Enabled() {
		return nil
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// Model implements Completer.
func (c *Client) Model() string { return c.cfg.Model }

// Enabled implements Completer. A constructed client always has a key: New
// returns nil without one.
func (c *Client) Enabled(context.Context) bool { return true }

type request struct {
	Model       string      `json:"model"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature float64     `json:"temperature"`
	System      string      `json:"system,omitempty"`
	Messages    []message   `json:"messages"`
	Tools       []tool      `json:"tools,omitempty"`
	ToolChoice  *toolChoice `json:"tool_choice,omitempty"`
}

// A tool the model must call. Its input schema is the reply shape, which the
// API validates - so the answer arrives as an object rather than as prose that
// happens to contain one.
type tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type response struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete implements Completer.
func (c *Client) Complete(ctx context.Context, system, user string, maxTokens int) (Result, error) {
	return c.send(ctx, request{Model: c.cfg.Model, MaxTokens: maxTokens, Temperature: 0, System: system, Messages: []message{{Role: "user", Content: user}}})
}

// CompleteJSON makes the model answer by calling a tool whose input schema is
// the reply shape. The API validates the arguments, so the result is an object
// rather than prose that has to be scraped for one. Result.Text carries that
// object.
func (c *Client) CompleteJSON(ctx context.Context, system, user string, maxTokens int, name string, schema any) (Result, error) {
	return c.send(ctx, request{
		Model: c.cfg.Model, MaxTokens: maxTokens, Temperature: 0, System: system,
		Messages:   []message{{Role: "user", Content: user}},
		Tools:      []tool{{Name: name, Description: "Record the answer.", InputSchema: schema}},
		ToolChoice: &toolChoice{Type: "tool", Name: name},
	})
}

func (c *Client) send(ctx context.Context, r request) (Result, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if c.cfg.WorkspaceID != "" {
		req.Header.Set("anthropic-workspace-id", c.cfg.WorkspaceID)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("ai: request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	var out response
	if jsonErr := json.Unmarshal(raw, &out); jsonErr != nil && res.StatusCode == http.StatusOK {
		return Result{}, fmt.Errorf("ai: unreadable response: %w", jsonErr)
	}
	if res.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if out.Error != nil {
			msg = out.Error.Type + ": " + out.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return Result{}, fmt.Errorf("ai: status %d: %s", res.StatusCode, msg)
	}
	var text strings.Builder
	for _, part := range out.Content {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "tool_use":
			// The validated arguments are the answer; nothing else matters.
			return Result{Text: string(part.Input), InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}, nil
		}
	}
	return Result{Text: text.String(), InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}, nil
}
