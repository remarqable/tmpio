package controllers

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/obs"
)

func errorsAs(err error) *errors.AppError { return errors.As(err) }

// MCPResource is the protected resource identifier.
func (d *Deps) MCPResource() string { return d.Cfg.AppOrigin + "/mcp" }

// ASMetadata serves RFC 8414 authorization-server metadata.
func (d *Deps) ASMetadata(c *gin.Context) {
	o := d.Cfg.AppOrigin
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                     o,
		"authorization_endpoint":                     o + "/oauth/authorize",
		"token_endpoint":                             o + "/oauth/token",
		"revocation_endpoint":                        o + "/oauth/revoke",
		"registration_endpoint":                      o + "/oauth/register",
		"response_types_supported":                   []string{"code"},
		"response_modes_supported":                   []string{"query"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":           []string{"S256"},
		"token_endpoint_auth_methods_supported":      []string{"none", "client_secret_post", "client_secret_basic"},
		"revocation_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                           models.AllScopes,
		"service_documentation":                      o + "/app/connections",
	})
}

// Register implements RFC 7591 dynamic client registration for public
// clients. The specification proposed preregistration only; real MCP clients
// (Claude Code, Claude, ChatGPT) discover the registration endpoint and refuse
// servers without it, so this is the documented revision of that section.
func (d *Deps) Register(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var body struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		ClientURI               string   `json:"client_uri"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		ApplicationType         string   `json:"application_type"`
	}
	reject := func(status int, code, desc string) {
		obs.From(c.Request.Context()).Warn().Str("event", "oauth.register_rejected").Str("reason", desc).Msg("")
		c.JSON(status, gin.H{"error": code, "error_description": desc})
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		reject(http.StatusBadRequest, "invalid_client_metadata", "body must be a JSON client metadata document")
		return
	}
	confidential := false
	switch body.TokenEndpointAuthMethod {
	case "", "none":
	case "client_secret_post", "client_secret_basic":
		confidential = true
	default:
		reject(http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method "+body.TokenEndpointAuthMethod)
		return
	}
	for _, g := range body.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			reject(http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+g)
			return
		}
	}
	for _, r := range body.ResponseTypes {
		if r != "code" {
			reject(http.StatusBadRequest, "invalid_client_metadata", "unsupported response_type "+r)
			return
		}
	}
	client, secret, err := models.RegisterDynamicClient(c.Request.Context(), models.DynamicClientInput{
		Name: body.ClientName, ClientURI: body.ClientURI, RedirectURIs: body.RedirectURIs,
		Confidential: confidential, Native: body.ApplicationType == "native" || body.ApplicationType == "", IP: c.ClientIP(),
	})
	if err != nil {
		ae := errorsAs(err)
		status := http.StatusBadRequest
		if ae.Code == "rate_limited" {
			status = http.StatusTooManyRequests
		}
		reject(status, "invalid_redirect_uri", ae.Message)
		return
	}
	obs.From(c.Request.Context()).Info().Str("event", "oauth.client_registered").Str("client_id", client.ID).Bool("confidential", confidential).Msg("")
	method := "none"
	if confidential {
		method = body.TokenEndpointAuthMethod
	}
	out := gin.H{
		"client_id":                  client.ID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"client_name":                client.Name,
		"redirect_uris":              []string(client.RedirectURIs),
		"token_endpoint_auth_method": method,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      strings.Join(models.AllScopes, " "),
	}
	if confidential {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	c.JSON(http.StatusCreated, out)
}

// PRMetadata serves RFC 9728 protected-resource metadata for /mcp.
func (d *Deps) PRMetadata(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, gin.H{
		"resource":                 d.MCPResource(),
		"authorization_servers":    []string{d.Cfg.AppOrigin},
		"scopes_supported":         models.AllScopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "tmp",
		"resource_documentation":   d.Cfg.AppOrigin + "/app/connections",
	})
}

type authzRequest struct {
	ClientID     string
	RedirectURI  string
	Scope        string
	State        string
	Challenge    string
	Method       string
	Resource     string
	ResponseType string
}

func parseAuthz(c *gin.Context, get func(string) string) authzRequest {
	return authzRequest{
		ClientID: get("client_id"), RedirectURI: get("redirect_uri"), Scope: get("scope"), State: get("state"),
		Challenge: get("code_challenge"), Method: get("code_challenge_method"), Resource: get("resource"), ResponseType: get("response_type"),
	}
}

func (r authzRequest) values() url.Values {
	q := url.Values{}
	q.Set("response_type", r.ResponseType)
	q.Set("client_id", r.ClientID)
	q.Set("redirect_uri", r.RedirectURI)
	q.Set("scope", r.Scope)
	q.Set("state", r.State)
	q.Set("code_challenge", r.Challenge)
	q.Set("code_challenge_method", r.Method)
	if r.Resource != "" {
		q.Set("resource", r.Resource)
	}
	return q
}

// validateAuthz checks client and redirect first (never redirect on failure),
// then the remaining parameters (errors redirect to the client).
func (d *Deps) validateAuthz(c *gin.Context, r authzRequest) (*models.OAuthClient, []string, string, bool) {
	client, err := models.GetOAuthClient(c.Request.Context(), r.ClientID)
	if err != nil || !client.HasRedirect(r.RedirectURI) {
		middleware.NoStore(c)
		d.render(c, http.StatusBadRequest, "pages/errors/error.html", "layout/bare", gin.H{"Status": 400, "Title": i18n.T("en", "oauth.rejected"), "Detail": i18n.T("en", "oauth.rejected_detail")})
		return nil, nil, "", false
	}
	redirectErr := func(code, desc string) {
		u, _ := url.Parse(r.RedirectURI)
		q := u.Query()
		q.Set("error", code)
		q.Set("error_description", desc)
		if r.State != "" {
			q.Set("state", r.State)
		}
		u.RawQuery = q.Encode()
		middleware.NoStore(c)
		c.Redirect(http.StatusFound, u.String())
	}
	if r.ResponseType != "code" {
		redirectErr("unsupported_response_type", "only response_type=code is supported")
		return nil, nil, "", false
	}
	if r.Method != "S256" || len(r.Challenge) < 43 || len(r.Challenge) > 128 {
		redirectErr("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return nil, nil, "", false
	}
	resource := r.Resource
	if resource == "" {
		resource = d.MCPResource()
	}
	resource = strings.TrimRight(resource, "/")
	if resource != d.MCPResource() {
		redirectErr("invalid_target", "resource must be "+d.MCPResource())
		return nil, nil, "", false
	}
	requested := strings.Fields(r.Scope)
	if len(requested) == 0 {
		requested = models.AllScopes
	}
	scopes, err := models.NormalizeScopes(requested)
	if err != nil {
		redirectErr("invalid_scope", err.Error())
		return nil, nil, "", false
	}
	return client, scopes, resource, true
}

// Authorize handles GET (login or consent) for the tmp authorization server.
func (d *Deps) Authorize(c *gin.Context) {
	middleware.NoStore(c)
	r := parseAuthz(c, c.Query)
	client, scopes, _, ok := d.validateAuthz(c, r)
	if !ok {
		return
	}
	_, _, _, tenant, signedIn := middleware.OwnerSession(c)
	if !signedIn {
		// Preserve the pending request server-side through sign-in.
		req, _ := json.Marshal(map[string]string{
			"response_type": r.ResponseType, "client_id": r.ClientID, "redirect_uri": r.RedirectURI, "scope": r.Scope, "state": r.State,
			"code_challenge": r.Challenge, "code_challenge_method": r.Method, "resource": r.Resource,
		})
		d.render(c, http.StatusOK, "pages/auth/landing.html", "layout/auth", gin.H{"LoginPage": true, "OAuthRequest": string(req), "ClientName": client.Name})
		return
	}
	cfg, _, _ := d.Ops.SiteConfigFor(c.Request.Context(), tenant.ID)
	// Browsers apply form-action to the redirect that follows the consent POST,
	// so the client's redirect origin must be allowed on this page only.
	if ru, err := url.Parse(r.RedirectURI); err == nil && ru.Scheme != "" && ru.Host != "" {
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; object-src 'none'; form-action 'self' "+ru.Scheme+"://"+ru.Host)
	}
	d.render(c, http.StatusOK, "pages/oauth/consent.html", "layout/auth", gin.H{
		"Title": i18n.T("en", "consent.title", "client", client.Name), "Client": client, "Scopes": scopes, "Request": r, "SiteName": siteName(cfg, tenant), "OrgCode": tenant.Code,
	})
}

// AuthorizePost records consent (owner session + CSRF) and issues the code.
func (d *Deps) AuthorizePost(c *gin.Context) {
	middleware.NoStore(c)
	r := parseAuthz(c, c.PostForm)
	client, scopes, resource, ok := d.validateAuthz(c, r)
	if !ok {
		return
	}
	p, _, _, _, signedIn := middleware.OwnerSession(c)
	if !signedIn {
		d.redirectToLogin(c)
		return
	}
	u, _ := url.Parse(r.RedirectURI)
	q := u.Query()
	if c.PostForm("decision") != "allow" {
		q.Set("error", "access_denied")
		if r.State != "" {
			q.Set("state", r.State)
		}
		u.RawQuery = q.Encode()
		c.Redirect(http.StatusFound, u.String())
		return
	}
	code, err := models.AuthorizeClient(c.Request.Context(), p, client.ID, r.RedirectURI, resource, r.Challenge, r.Method, scopes)
	if err != nil {
		d.fail(c, err)
		return
	}
	q.Set("code", code)
	if r.State != "" {
		q.Set("state", r.State)
	}
	u.RawQuery = q.Encode()
	c.Redirect(http.StatusFound, u.String())
}

func tokenError(c *gin.Context, status int, code, desc string) {
	// Diagnostic only: which check failed and which optional fields the client
	// sent. Never the values themselves.
	obs.From(c.Request.Context()).Warn().Str("event", "oauth.token_rejected").Str("error", code).Str("reason", desc).
		Str("grant_type", c.PostForm("grant_type")).Str("client_id", c.PostForm("client_id")).
		Bool("basic_auth", c.GetHeader("Authorization") != "").Bool("has_verifier", c.PostForm("code_verifier") != "").
		Bool("has_redirect_uri", c.PostForm("redirect_uri") != "").Bool("has_resource", c.PostForm("resource") != "").Msg("")
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	if status == http.StatusUnauthorized {
		c.Header("WWW-Authenticate", `Basic realm="tmp"`)
	}
	c.JSON(status, gin.H{"error": code, "error_description": desc})
}

// clientAuth authenticates the client for the token endpoint.
func (d *Deps) clientAuth(c *gin.Context) (*models.OAuthClient, bool) {
	clientID := c.PostForm("client_id")
	secret := c.PostForm("client_secret")
	if u, pw, ok := c.Request.BasicAuth(); ok {
		clientID, secret = u, pw
	}
	client, err := models.GetOAuthClient(c.Request.Context(), clientID)
	if err != nil {
		tokenError(c, http.StatusUnauthorized, "invalid_client", "unknown client")
		return nil, false
	}
	if !client.IsPublic {
		if secret == "" || subtle.ConstantTimeCompare(models.HashToken(secret), client.SecretHash) != 1 {
			tokenError(c, http.StatusUnauthorized, "invalid_client", "client authentication failed")
			return nil, false
		}
	}
	return client, true
}

// Token is the OAuth token endpoint (authorization_code + PKCE, refresh_token).
func (d *Deps) Token(c *gin.Context) {
	client, ok := d.clientAuth(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	switch c.PostForm("grant_type") {
	case "authorization_code":
		code := c.PostForm("code")
		verifier := c.PostForm("code_verifier")
		if code == "" || verifier == "" {
			tokenError(c, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
			return
		}
		rec, err := models.ConsumeAuthCode(ctx, code)
		if err != nil {
			tokenError(c, http.StatusBadRequest, "invalid_grant", "code is invalid, expired or already used")
			return
		}
		if rec.ClientID != client.ID {
			tokenError(c, http.StatusBadRequest, "invalid_grant", "code was issued to a different client")
			return
		}
		if ru := c.PostForm("redirect_uri"); ru != "" && ru != rec.RedirectURI {
			tokenError(c, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != rec.CodeChallenge {
			tokenError(c, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		if res := c.PostForm("resource"); res != "" && strings.TrimRight(res, "/") != rec.Resource {
			tokenError(c, http.StatusBadRequest, "invalid_target", "resource mismatch")
			return
		}
		pair, err := models.IssueTokens(ctx, rec.TenantID, rec.GrantID, rec.Resource, "")
		if err != nil {
			tokenError(c, http.StatusBadRequest, "invalid_grant", "grant is no longer valid")
			return
		}
		d.tokenResponse(c, pair)
	case "refresh_token":
		rt := c.PostForm("refresh_token")
		if rt == "" {
			tokenError(c, http.StatusBadRequest, "invalid_request", "refresh_token is required")
			return
		}
		pair, err := models.RefreshTokens(ctx, rt, client.ID, d.MCPResource())
		if err != nil {
			obs.From(ctx).Warn().Str("event", "oauth.refresh_failed").Msg("")
			tokenError(c, http.StatusBadRequest, "invalid_grant", "refresh token is invalid, expired, reused or revoked")
			return
		}
		d.tokenResponse(c, pair)
	default:
		tokenError(c, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

func (d *Deps) tokenResponse(c *gin.Context, pair *models.TokenPair) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.JSON(http.StatusOK, gin.H{
		"access_token": pair.AccessToken, "token_type": "Bearer", "expires_in": pair.ExpiresIn,
		"refresh_token": pair.RefreshToken, "scope": strings.Join(pair.Scopes, " "),
	})
}

// Revoke implements RFC 7009.
func (d *Deps) Revoke(c *gin.Context) {
	client, ok := d.clientAuth(c)
	if !ok {
		return
	}
	_ = models.RevokeOAuthToken(c.Request.Context(), c.PostForm("token"), client.ID)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
}
