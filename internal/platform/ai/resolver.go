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
// The stored setting wins. ANTHROPIC_API_KEY seeds it once at startup when
// nothing is stored, so an instance configured by environment keeps working,
// but from then on the settings page is the one place the key lives and the
// one place it changes. A page that shows a key it refuses to edit is worse
// than no page.
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

// EnvConfigured reports whether the environment carries a key, which startup
// uses to decide whether to seed the store.
func (r *Resolver) EnvConfigured() bool { return r.env.Enabled() }

// EnvSettings are the credentials the environment supplies, for seeding.
func (r *Resolver) EnvSettings() Settings {
	return Settings{APIKey: r.env.APIKey, Model: r.env.Model, BaseURL: r.env.BaseURL, WorkspaceID: r.env.WorkspaceID}
}

// settings resolves the credentials for this call.
func (r *Resolver) settings(ctx context.Context) Settings {
	var s Settings
	if r.load != nil {
		loaded, err := r.load(ctx)
		if err == nil {
			s = loaded
		}
	}
	// Nothing stored yet: fall back to the environment, which is also what
	// seeds the store on the next startup.
	if s.APIKey == "" {
		s.APIKey = r.env.APIKey
	}
	// The environment supplies the defaults the operator did not set.
	if s.Model == "" {
		s.Model = r.env.Model
	}
	if s.BaseURL == "" {
		s.BaseURL = r.env.BaseURL
	}
	if s.WorkspaceID == "" {
		s.WorkspaceID = r.env.WorkspaceID
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
