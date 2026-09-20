// Package config loads the application configuration from the environment.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultOwnerUser is who the single local account belongs to when nobody says
// otherwise. A private instance has one person on it and no mail to send.
const DefaultOwnerUser = "admin"

// MinOwnerPasswordRunes is the shortest password accepted for the local owner
// account. The sign-in form is public and rate limited but not otherwise
// protected, so the password carries the whole weight.
const MinOwnerPasswordRunes = 12

// OAuthClient is a preregistered OAuth client (an AI client such as Claude or ChatGPT).
type OAuthClient struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Public       bool     `json:"public"`
	Secret       string   `json:"secret,omitempty"`
}

// Quotas are per-tenant storage limits, enforced transactionally.
type Quotas struct {
	MaxPages             int64
	MaxDirectories       int64
	MaxPageBytes         int64
	MaxConfigBytes       int64
	MaxAssetBytes        int64
	MaxDocumentBytes     int64 // PDF and other non-image assets
	MaxFileBytes         int64 // text files that are not pages
	MaxAssetPixels       int64
	MaxCurrentAssetBytes int64
	MaxRetainedBytes     int64
}

// Config is the process configuration. It is loaded once at startup.
type Config struct {
	AppEnv           string
	Port             string
	AppOrigin        string
	DatabaseURL      string
	DatabaseOwnerURL string
	SessionSecret    []byte

	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string
	DevLoginBypass     bool

	OAuthClients []OAuthClient
	Quotas       Quotas
	MetricsToken string // if set, /metrics requires this bearer token; otherwise loopback only

	// SignupsEnabled allows a first sign-in to create a new account. When false,
	// existing users still sign in and newcomers are offered the launch waitlist.
	SignupsEnabled bool
	// SourceURL is the public repository, linked from the landing page and footer.
	SourceURL string

	// OwnerUser and OwnerPassword provision the single local owner account on a
	// self-hosted instance, so that signing in needs no external identity
	// provider. OwnerUser is a username, not necessarily an email address:
	// "admin" is the default, because a private instance with one account has
	// nothing to address mail to. An address still works for anyone who
	// prefers one, and for instances created before usernames existed.
	//
	// OwnerPassword is authoritative at every boot: setting a new one rotates
	// the password. Removing it leaves the stored credential alone.
	OwnerUser     string
	OwnerPassword string
	// LocalAuth enables the email-and-password sign-in form. It defaults to on
	// when OWNER_EMAIL is set, so a self-hoster never has to know about it.
	LocalAuth bool
	// TrustedProxies are the addresses allowed to set X-Forwarded-For. Every
	// per-IP decision in the server reads gin's ClientIP, so a proxy that is
	// trusted without being in front of the server hands any caller the ability
	// to claim an address. Empty means trust nobody and use the peer address.
	TrustedProxies []string
	// PublicSiteDir, when set, is a directory of static files served to
	// anonymous visitors before the built-in pages. It lets an operator put
	// their own front page on the instance without the application knowing
	// anything about its contents.
	PublicSiteDir string
	// PublicSiteCSP replaces the Content-Security-Policy on those files only.
	// Empty keeps the application's policy, which is strict: a marketing page
	// that wants an inline script or a third-party font has to say so here,
	// deliberately, because those files run on the application's origin.
	PublicSiteCSP string
	// AutoMigrate runs the embedded migrations as the owner role at startup.
	// On by default in a container, where no operator can run goose by hand.
	AutoMigrate bool

	AI AI
}

// AI configures the optional language-model helper used to file incoming content.
type AI struct {
	APIKey          string // ANTHROPIC_API_KEY; empty disables inference (heuristics only)
	WorkspaceID     string // AI_WORKSPACE_ID; required by Anthropic for keys that are not scoped to one workspace
	Model           string // AI_MODEL, a small fast model by default
	BaseURL         string // AI_BASE_URL, overridable for tests and proxies
	TimeoutSeconds  int64  // AI_TIMEOUT_SECONDS per request
	MaxCallsPerHour int64  // AI_MAX_CALLS_PER_HOUR per tenant; beyond it the heuristic is used
}

// Enabled reports whether inference is configured.
func (a AI) Enabled() bool { return a.APIKey != "" }

// IsProd reports whether the configuration is for a production environment.
func (c *Config) IsProd() bool { return c.AppEnv == "prod" || c.AppEnv == "production" }

// Secure reports whether cookies must carry the Secure attribute.
func (c *Config) Secure() bool { return strings.HasPrefix(c.AppOrigin, "https://") }

// GoogleEnabled reports whether Google sign-in is configured.
func (c *Config) GoogleEnabled() bool { return c.GoogleClientID != "" && c.GoogleClientSecret != "" }

// DefaultOAuthClients are the two closed-MVP test clients. Redirect URIs follow
// the vendors' published custom-connector documentation.
var DefaultOAuthClients = []OAuthClient{
	{
		ID:   "claude",
		Name: "Claude",
		RedirectURIs: []string{
			"https://claude.ai/api/mcp/auth_callback",
			"https://claude.com/api/mcp/auth_callback",
		},
		Public: true,
	},
	{
		ID:   "chatgpt",
		Name: "ChatGPT",
		RedirectURIs: []string{
			"https://chatgpt.com/connector_platform_oauth_redirect",
			"https://chat.openai.com/connector_platform_oauth_redirect",
		},
		Public: true,
	},
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		AppEnv:             getEnv("APP_ENV", "dev"),
		Port:               getEnv("PORT", "8000"),
		AppOrigin:          strings.TrimRight(getEnv("APP_ORIGIN", "http://localhost:8000"), "/"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		DatabaseOwnerURL:   os.Getenv("DATABASE_OWNER_URL"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		DevLoginBypass:     os.Getenv("DEV_LOGIN_BYPASS") == "1" || os.Getenv("DEV_LOGIN_BYPASS") == "true",
		MetricsToken:       os.Getenv("METRICS_TOKEN"),
		SignupsEnabled:     getEnv("SIGNUPS_ENABLED", "1") == "1" || getEnv("SIGNUPS_ENABLED", "1") == "true",
		SourceURL:          getEnv("SOURCE_URL", "https://github.com/remarqable/tmpio"),
		// OWNER_EMAIL is what this was called before usernames; instances
		// configured with it keep working and keep their account.
		OwnerUser:      strings.ToLower(strings.TrimSpace(getEnv("OWNER_USER", os.Getenv("OWNER_EMAIL")))),
		OwnerPassword:  os.Getenv("OWNER_PASSWORD"),
		AutoMigrate:    truthy(getEnv("AUTO_MIGRATE", "0")),
		TrustedProxies: parseList(getEnv("TRUSTED_PROXIES", "127.0.0.1,::1")),
		PublicSiteDir:  strings.TrimRight(os.Getenv("PUBLIC_SITE_DIR"), "/"),
		PublicSiteCSP:  os.Getenv("PUBLIC_SITE_CSP"),
		AI: AI{
			APIKey:          os.Getenv("ANTHROPIC_API_KEY"),
			WorkspaceID:     os.Getenv("AI_WORKSPACE_ID"),
			Model:           getEnv("AI_MODEL", "claude-haiku-4-5-20251001"),
			BaseURL:         strings.TrimRight(getEnv("AI_BASE_URL", "https://api.anthropic.com"), "/"),
			TimeoutSeconds:  getEnvInt("AI_TIMEOUT_SECONDS", 8),
			MaxCallsPerHour: getEnvInt("AI_MAX_CALLS_PER_HOUR", 120),
		},
		Quotas: Quotas{
			MaxPages:             getEnvInt("QUOTA_MAX_PAGES", 1000),
			MaxDirectories:       getEnvInt("QUOTA_MAX_DIRECTORIES", 100),
			MaxPageBytes:         getEnvInt("QUOTA_MAX_PAGE_BYTES", 256*1024),
			MaxConfigBytes:       getEnvInt("QUOTA_MAX_CONFIG_BYTES", 32*1024),
			MaxAssetBytes:        getEnvInt("QUOTA_MAX_ASSET_BYTES", 5*1024*1024),
			MaxDocumentBytes:     getEnvInt("QUOTA_MAX_DOCUMENT_BYTES", 20*1024*1024),
			MaxFileBytes:         getEnvInt("QUOTA_MAX_FILE_BYTES", 1024*1024),
			MaxAssetPixels:       getEnvInt("QUOTA_MAX_ASSET_PIXELS", 20_000_000),
			MaxCurrentAssetBytes: getEnvInt("QUOTA_MAX_CURRENT_ASSET_BYTES", 100*1024*1024),
			MaxRetainedBytes:     getEnvInt("QUOTA_MAX_RETAINED_BYTES", 500*1024*1024),
		},
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	secret := os.Getenv("SESSION_SECRET")
	if len(secret) < 32 {
		return nil, fmt.Errorf("SESSION_SECRET must be at least 32 characters")
	}
	cfg.SessionSecret = []byte(secret)
	if cfg.GoogleRedirectURL == "" {
		cfg.GoogleRedirectURL = cfg.AppOrigin + "/auth/google/callback"
	}
	if cfg.IsProd() && cfg.DevLoginBypass {
		return nil, fmt.Errorf("DEV_LOGIN_BYPASS must not be set in production")
	}
	// A password with nobody to attach it to means the obvious account.
	if cfg.OwnerUser == "" && cfg.OwnerPassword != "" {
		cfg.OwnerUser = DefaultOwnerUser
	}
	cfg.LocalAuth = cfg.OwnerUser != ""
	if v := os.Getenv("LOCAL_AUTH"); v != "" {
		cfg.LocalAuth = truthy(v)
	}
	if cfg.LocalAuth && cfg.OwnerUser == "" {
		cfg.OwnerUser = DefaultOwnerUser
	}
	if cfg.OwnerUser != "" {
		if err := validOwnerUser(cfg.OwnerUser); err != nil {
			return nil, err
		}
	}
	if !cfg.LocalAuth && cfg.OwnerPassword != "" {
		return nil, fmt.Errorf("OWNER_PASSWORD is set but local sign-in is off: unset LOCAL_AUTH=0, or unset OWNER_PASSWORD")
	}
	if cfg.OwnerPassword != "" && len([]rune(cfg.OwnerPassword)) < MinOwnerPasswordRunes {
		return nil, fmt.Errorf("OWNER_PASSWORD must be at least %d characters", MinOwnerPasswordRunes)
	}
	if cfg.IsProd() && !cfg.GoogleEnabled() && !cfg.LocalAuth {
		return nil, fmt.Errorf("no production sign-in method: set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET, or OWNER_PASSWORD for a single local owner")
	}
	if cfg.IsProd() {
		if err := requireTLS("DATABASE_URL", cfg.DatabaseURL); err != nil {
			return nil, err
		}
		if err := requireTLS("DATABASE_OWNER_URL", cfg.DatabaseOwnerURL); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(cfg.AppOrigin, "https://") {
			return nil, fmt.Errorf("APP_ORIGIN must be an https:// URL in production")
		}
	}
	if !cfg.GoogleEnabled() && !cfg.LocalAuth && !cfg.DevLoginBypass {
		return nil, fmt.Errorf("no sign-in method configured: set Google credentials, OWNER_PASSWORD for a local owner, or DEV_LOGIN_BYPASS=1 in development")
	}

	for _, p := range cfg.TrustedProxies {
		if net.ParseIP(p) == nil {
			if _, _, err := net.ParseCIDR(p); err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: %q is neither an IP address nor a CIDR block", p)
			}
		}
	}

	cfg.OAuthClients = append(cfg.OAuthClients, DefaultOAuthClients...)
	if raw := os.Getenv("OAUTH_CLIENTS_JSON"); raw != "" {
		var extra []OAuthClient
		if err := json.Unmarshal([]byte(raw), &extra); err != nil {
			return nil, fmt.Errorf("OAUTH_CLIENTS_JSON: %w", err)
		}
		for _, e := range extra {
			if e.ID == "" || len(e.RedirectURIs) == 0 {
				return nil, fmt.Errorf("OAUTH_CLIENTS_JSON: every client needs id and redirect_uris")
			}
			replaced := false
			for i := range cfg.OAuthClients {
				if cfg.OAuthClients[i].ID == e.ID {
					cfg.OAuthClients[i] = e
					replaced = true
				}
			}
			if !replaced {
				cfg.OAuthClients = append(cfg.OAuthClients, e)
			}
		}
	}
	return cfg, nil
}

// requireTLS refuses a PostgreSQL URL that would connect without TLS. Content is
// encrypted at rest by the database host; this keeps it encrypted on the wire.
// An empty URL is allowed (the owner URL is optional at runtime).
//
// A database on a private address is exempt. The common self-hosted shape is a
// PostgreSQL container on a private Docker network or a loopback socket, where
// the bytes never leave the host and demanding TLS would only push people to
// disable the check entirely. A database reachable at a public address gets no
// such benefit of the doubt, and neither does a name that will not resolve.
func requireTLS(name, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	switch u.Query().Get("sslmode") {
	case "require", "verify-ca", "verify-full":
		return nil
	}
	private, err := hostIsPrivate(u.Hostname())
	if err != nil {
		return fmt.Errorf("%s: %w; set sslmode=require, verify-ca or verify-full", name, err)
	}
	if private {
		return nil
	}
	if mode := u.Query().Get("sslmode"); mode != "" {
		return fmt.Errorf("%s uses sslmode=%s to reach a public address; production requires require, verify-ca or verify-full", name, mode)
	}
	return fmt.Errorf("%s must set sslmode=require, verify-ca or verify-full to reach a public address", name)
}

// hostIsPrivate reports whether every address the host resolves to is loopback
// or private. An empty host is a Unix socket, which never leaves the machine.
func hostIsPrivate(host string) (bool, error) {
	if host == "" {
		return true, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return false, fmt.Errorf("cannot resolve database host %q to decide whether TLS is required: %w", host, err)
	}
	if len(addrs) == 0 {
		return false, fmt.Errorf("database host %q resolves to no address", host)
	}
	for _, a := range addrs {
		if !a.IP.IsLoopback() && !a.IP.IsPrivate() && !a.IP.IsLinkLocalUnicast() {
			return false, nil
		}
	}
	return true, nil
}

// validOwnerUser accepts a plain username or an email address, and refuses
// anything that would be confusing to type or impossible to distinguish from
// another account.
func validOwnerUser(u string) error {
	if n := len([]rune(u)); n < 2 || n > 254 {
		return fmt.Errorf("OWNER_USER must be between 2 and 254 characters")
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_' || r == '+' || r == '@':
		default:
			return fmt.Errorf("OWNER_USER may contain letters, digits, and . - _ + @ only; %q is not allowed", string(r))
		}
	}
	return nil
}

// parseList splits a comma-separated environment value. The literal "none"
// yields nothing, which is how an operator says "trust no proxy at all".
func parseList(v string) []string {
	if strings.TrimSpace(v) == "" || strings.TrimSpace(v) == "none" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// truthy reads the environment's spelling of yes.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
