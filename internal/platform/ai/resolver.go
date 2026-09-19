package ai

import (
	"context"
	"sync"

	"github.com/remarqable/tmpio/internal/platform/config"
)

// Settings is the credential set a call needs. The resolver asks for it on
// every call, so an operator can change the key from the settings page without
// restarting the server.
type Settings struct {
	APIKey      string
	Model       string
	BaseURL     string
	WorkspaceID string
}

// Resolver is a Completer that decides which credentials to use per call.
//
// The environment wins when it carries a key. A hosted deployment sets
// ANTHROPIC_API_KEY and its operators cannot change it through a web form; a
// self-hosted one leaves the variable unset and manages the key in the
// settings page. Making the environment authoritative keeps that precedence
// visible rather than surprising.
type Resolver struct {
	env  config.AI
	load func(ctx context.Context) (Settings, error)

	mu     sync.Mutex
	cached *Client
	key    Settings // the settings the cached client was built from
}

// NewResolver returns a Completer backed by the environment and, when the
// environment has no key, by load.
func NewResolver(env config.AI, load func(ctx context.Context) (Settings, error)) *Resolver {
	return &Resolver{env: env, load: load}
}

// EnvConfigured reports whether the environment supplies the key, which the
// settings page shows so nobody edits a field that cannot take effect.
func (r *Resolver) EnvConfigured() bool { return r.env.Enabled() }

// settings resolves the credentials for this call.
func (r *Resolver) settings(ctx context.Context) Settings {
	if r.env.Enabled() {
		return Settings{APIKey: r.env.APIKey, Model: r.env.Model, BaseURL: r.env.BaseURL, WorkspaceID: r.env.WorkspaceID}
	}
	if r.load == nil {
		return Settings{}
	}
	s, err := r.load(ctx)
	if err != nil {
		return Settings{}
	}
	// The environment still supplies the defaults the operator did not set.
	if s.Model == "" {
		s.Model = r.env.Model
	}
	if s.BaseURL == "" {
		s.BaseURL = r.env.BaseURL
	}
	return s
}

// client returns a client for the current settings, rebuilding it only when
// they change.
func (r *Resolver) client(ctx context.Context) *Client {
	s := r.settings(ctx)
	if s.APIKey == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached != nil && r.key == s {
		return r.cached
	}
	cfg := r.env
	cfg.APIKey, cfg.Model, cfg.BaseURL, cfg.WorkspaceID = s.APIKey, s.Model, s.BaseURL, s.WorkspaceID
	r.cached, r.key = New(cfg), s
	return r.cached
}

// Enabled implements Completer.
func (r *Resolver) Enabled(ctx context.Context) bool { return r.client(ctx) != nil }

// Model implements Completer.
func (r *Resolver) Model() string {
	if c := r.client(context.Background()); c != nil {
		return c.Model()
	}
	return r.env.Model
}

// Complete implements Completer.
func (r *Resolver) Complete(ctx context.Context, system, user string, maxTokens int) (Result, error) {
	c := r.client(ctx)
	if c == nil {
		return Result{}, ErrNotConfigured
	}
	return c.Complete(ctx, system, user, maxTokens)
}
