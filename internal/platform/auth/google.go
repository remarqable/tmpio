// Package auth implements Google OpenID Connect sign-in and the development bypass.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/config"
)

// GoogleIssuer is the OIDC issuer for Google accounts.
const GoogleIssuer = "https://accounts.google.com"

// Google performs authorization-code OIDC with state, nonce and PKCE.
type Google struct {
	cfg      *config.Config
	mu       sync.Mutex
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewGoogle creates the Google sign-in helper. Discovery runs lazily.
func NewGoogle(cfg *config.Config) *Google { return &Google{cfg: cfg} }

// Enabled reports whether Google credentials are configured.
func (g *Google) Enabled() bool { return g.cfg.GoogleEnabled() }

func (g *Google) init(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.provider != nil {
		return nil
	}
	p, err := oidc.NewProvider(ctx, GoogleIssuer)
	if err != nil {
		return fmt.Errorf("google discovery: %w", err)
	}
	g.provider = p
	g.oauth = &oauth2.Config{
		ClientID:     g.cfg.GoogleClientID,
		ClientSecret: g.cfg.GoogleClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  g.cfg.GoogleRedirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	g.verifier = p.Verifier(&oidc.Config{ClientID: g.cfg.GoogleClientID})
	return nil
}

// Begin stores pending state and returns the Google authorization URL.
func (g *Google) Begin(ctx context.Context, returnPath string, oauthRequest []byte) (string, error) {
	if err := g.init(ctx); err != nil {
		return "", err
	}
	nonce := models.NewToken()
	verifier := models.NewToken()
	state, err := models.CreateLoginState(ctx, nonce, verifier, returnPath, oauthRequest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return g.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	), nil
}

// Complete validates the callback and returns the verified identity plus the
// stored login state. Google refresh tokens are discarded.
func (g *Google) Complete(ctx context.Context, state, code string) (*models.ExternalIdentity, *models.LoginState, error) {
	if err := g.init(ctx); err != nil {
		return nil, nil, err
	}
	ls, err := models.ConsumeLoginState(ctx, state)
	if err != nil {
		return nil, nil, err
	}
	if code == "" {
		return nil, ls, fmt.Errorf("missing code")
	}
	tok, err := g.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", ls.PKCEVerifier))
	if err != nil {
		return nil, ls, fmt.Errorf("token exchange failed")
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, ls, fmt.Errorf("no id_token")
	}
	idt, err := g.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, ls, fmt.Errorf("id_token verification failed")
	}
	if idt.Nonce != ls.Nonce {
		return nil, ls, fmt.Errorf("nonce mismatch")
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := idt.Claims(&claims); err != nil {
		return nil, ls, err
	}
	if !claims.EmailVerified {
		return nil, ls, fmt.Errorf("email not verified")
	}
	return &models.ExternalIdentity{Issuer: idt.Issuer, Subject: idt.Subject, Email: claims.Email, EmailVerified: true, Name: claims.Name, AvatarURL: claims.Picture}, ls, nil
}

// DevIdentity returns a synthetic verified identity for the development bypass.
func DevIdentity(username, name string) models.ExternalIdentity {
	if name == "" {
		name = username
	}
	id := models.ExternalIdentity{Issuer: models.DevIssuer, Subject: username, Name: name}
	// "admin" is a username, not an address: do not claim to have verified one.
	if strings.Contains(username, "@") {
		id.Email, id.EmailVerified = username, true
	}
	return id
}

// RandomString returns n random bytes base64url-encoded.
func RandomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
