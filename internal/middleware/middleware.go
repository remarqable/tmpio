// Package middleware holds Gin middleware: correlation, security headers,
// session resolution, CSRF, bearer credentials and rate limiting.
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/obs"
	"github.com/remarqable/tmpio/internal/platform/ratelimit"
)

// Context keys.
const (
	KeyPrincipal = "principal"
	KeySession   = "session"
	KeyUser      = "user"
	KeyTenant    = "tenant"
	KeyRole      = "role"
	KeyBearer    = "bearer_present"
)

// SessionCookie is the browser session cookie name.
const SessionCookie = "tmp_session"

// RequestID assigns a correlation ID to every request.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" || len(id) > 64 {
			id = ulid.Make().String()
		}
		c.Header("X-Request-ID", id)
		ctx := obs.With(c.Request.Context(), obs.Trace{RequestID: id, Route: c.FullPath()})
		c.Request = c.Request.WithContext(ctx)
		c.Set("request_id", id)
		c.Next()
	}
}

// Logger emits one structured line per request without tokens or content.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "content"
		}
		if strings.HasPrefix(c.Request.URL.Path, "/s/") {
			route = "/s/:token" + shareRouteSuffix(c.Request.URL.Path)
		}
		obs.Default.ObserveRequest(route, c.Request.Method, c.Writer.Status(), time.Since(start))
		switch c.Writer.Status() {
		case 401:
			obs.Default.Inc("auth_failures")
		case 412:
			obs.Default.Inc("write_conflicts")
		case 429:
			obs.Default.Inc("rate_limited")
		}
		l := obs.From(c.Request.Context())
		ev := l.Info()
		if c.Writer.Status() >= 500 {
			ev = l.Error()
		}
		ev.Str("event", "http.request").Str("method", c.Request.Method).Str("route", route).
			Int("status", c.Writer.Status()).Dur("took", time.Since(start)).Msg("")
	}
}

func shareRouteSuffix(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, "/s/"), "/", 2)
	if len(parts) < 2 {
		return ""
	}
	seg := strings.SplitN(parts[1], "/", 2)[0]
	return "/" + seg
}

// SecurityHeaders sets baseline headers. Content responses add their own cache rules.
func SecurityHeaders(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' https: data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
		if cfg.Secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// NoStore marks tenant or capability content uncacheable.
func NoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
}

// Session resolves the browser session cookie into an owner principal. It
// never aborts; handlers decide. A bearer header, when present, is validated by
// Bearer() instead and cookies are ignored for that request.
func Session() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h := c.GetHeader("Authorization"); h != "" && !strings.HasPrefix(strings.ToLower(h), "basic ") {
			c.Next()
			return
		}
		token, err := c.Cookie(SessionCookie)
		if err != nil || token == "" {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		s, err := models.GetSession(ctx, token)
		if err != nil {
			c.Next()
			return
		}
		u, err := models.GetUser(ctx, s.UserID)
		if err != nil {
			c.Next()
			return
		}
		// The role comes from the membership, read on every request. A session
		// that was an owner yesterday is a viewer today if someone changed it,
		// and is nothing at all if they were removed.
		//
		// The organization is the one this session chose, when the membership
		// backing that choice still exists; otherwise the oldest.
		var prefer int64
		if s.ActingTenantID != nil {
			prefer = *s.ActingTenantID
		}
		t, role, err := models.TenantForUser(ctx, u.ID, prefer)
		if err != nil {
			c.Next()
			return
		}
		scopes := models.RoleScopes(role)
		if len(scopes) == 0 {
			// A role we do not recognise grants nothing rather than everything.
			c.Next()
			return
		}
		c.Set(KeySession, s)
		c.Set(KeyUser, u)
		c.Set(KeyTenant, t)
		c.Set(KeyRole, role)
		c.Set(KeyPrincipal, models.Principal{Kind: models.PrincipalOwner, TenantID: t.ID, UserID: u.ID, SessionID: s.ID, Scopes: scopes, Role: role})
		tr := obs.Get(ctx)
		tr.TenantID, tr.UserID = t.ID, u.ID
		c.Request = c.Request.WithContext(obs.With(ctx, tr))
		c.Next()
	}
}

// Bearer validates an Authorization: Bearer credential for REST and content
// routes. A present but invalid credential is a 401 with no cookie fallback.
// restAudience is the resource identifier REST/content tokens must carry; MCP
// tokens have the MCP audience and are refused here.
func Bearer(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if h == "" || strings.HasPrefix(c.Request.URL.Path, "/mcp") || strings.HasPrefix(c.Request.URL.Path, "/oauth/") {
			// /mcp authenticates its own audience-bound tokens; /oauth/* uses client authentication.
			c.Next()
			return
		}
		if strings.HasPrefix(strings.ToLower(h), "basic ") {
			// HTTP basic auth belongs to a proxy in front of the app (for example a
			// gate on the sign-in page). Browsers keep sending it on every request
			// of the origin, so it is ignored here and cookies apply as usual.
			c.Next()
			return
		}
		c.Set(KeyBearer, true)
		if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
			abort401(c, "unsupported authorization scheme")
			return
		}
		token := strings.TrimSpace(h[7:])
		ctx := c.Request.Context()
		var p *models.Principal
		var err error
		switch {
		case strings.HasPrefix(token, "tmpk_"):
			p, err = models.AuthenticateAPIToken(ctx, token)
		case strings.HasPrefix(token, "tmpa_"):
			p, _, err = models.AuthenticateAccessToken(ctx, token, cfg.AppOrigin+"/mcp")
			if err == nil {
				// MCP tokens are audience-bound to /mcp and are not REST credentials.
				err = errors.New(errors.CodeUnauthorized, "this token is bound to the MCP resource; use it at /mcp")
			}
		default:
			err = errors.New(errors.CodeUnauthorized, "invalid token")
		}
		if err != nil || p == nil {
			abort401(c, "invalid or expired credential")
			return
		}
		c.Set(KeyPrincipal, *p)
		tr := obs.Get(ctx)
		tr.TenantID, tr.UserID = p.TenantID, p.UserID
		c.Request = c.Request.WithContext(obs.With(ctx, tr))
		c.Next()
	}
}

func abort401(c *gin.Context, msg string) {
	c.Header("WWW-Authenticate", `Bearer realm="tmp"`)
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": errors.CodeUnauthorized, "message": msg}, "request_id": c.GetString("request_id")})
}

// GetPrincipal returns the request principal if any.
func GetPrincipal(c *gin.Context) (models.Principal, bool) {
	v, ok := c.Get(KeyPrincipal)
	if !ok {
		return models.Principal{}, false
	}
	p, ok := v.(models.Principal)
	return p, ok
}

// OwnerSession returns the owner principal, session, user and tenant when the
// request carries a valid owner cookie session (never for bearer requests).
func OwnerSession(c *gin.Context) (models.Principal, *models.Session, *models.User, *models.Tenant, bool) {
	p, ok := GetPrincipal(c)
	if !ok || !p.IsOwnerSession() {
		return models.Principal{}, nil, nil, nil, false
	}
	s, _ := c.Get(KeySession)
	u, _ := c.Get(KeyUser)
	t, _ := c.Get(KeyTenant)
	sess, _ := s.(*models.Session)
	user, _ := u.(*models.User)
	tenant, _ := t.(*models.Tenant)
	if sess == nil || user == nil || tenant == nil {
		return models.Principal{}, nil, nil, nil, false
	}
	return p, sess, user, tenant, true
}

// CSRF enforces the per-session token plus an origin check on cookie-authenticated mutations.
func CSRF(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if _, bearer := c.Get(KeyBearer); bearer {
			c.Next()
			return
		}
		_, sess, _, _, ok := OwnerSession(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": errors.CodeUnauthorized, "message": "sign in required"}})
			return
		}
		if !OriginAllowed(c, cfg.AppOrigin) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{"code": errors.CodeForbidden, "message": "cross-origin request refused"}})
			return
		}
		token := c.GetHeader("X-CSRF-Token")
		if token == "" {
			token = c.PostForm("csrf_token")
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRFToken)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": gin.H{"code": errors.CodeForbidden, "message": "invalid CSRF token"}})
			return
		}
		c.Next()
	}
}

// OriginAllowed checks Origin (or Referer) against the application origin
// when present. Browser mutations always carry one; non-browser clients may omit it.
func OriginAllowed(c *gin.Context, origin string) bool {
	// The request's own origin (scheme + Host) is same-origin by definition. This
	// keeps http://localhost working while APP_ORIGIN points at a public tunnel.
	self := requestOrigin(c)
	o := c.GetHeader("Origin")
	if o != "" {
		return o == origin || o == self
	}
	if r := c.GetHeader("Referer"); r != "" {
		return strings.HasPrefix(r, origin+"/") || r == origin || strings.HasPrefix(r, self+"/") || r == self
	}
	if sf := c.GetHeader("Sec-Fetch-Site"); sf != "" && sf != "same-origin" && sf != "none" {
		return false
	}
	return true
}

// RateLimit applies a limiter keyed by principal (or client IP).
func RateLimit(l *ratelimit.Limiter, keyFn func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := keyFn(c)
		if ok, retry := l.Allow(key); !ok {
			c.Header("Retry-After", itoa(retry))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"code": errors.CodeRateLimited, "message": "too many requests; retry after " + itoa(retry) + " seconds"}, "request_id": c.GetString("request_id")})
			return
		}
		c.Next()
	}
}

// KeyByPrincipalOrIP keys limits on the principal when present, else the IP.
func KeyByPrincipalOrIP(c *gin.Context) string {
	if p, ok := GetPrincipal(c); ok {
		return p.Key()
	}
	return "ip:" + c.ClientIP()
}

// KeyByIP keys limits on the client IP.
func KeyByIP(c *gin.Context) string { return "ip:" + c.ClientIP() }

func itoa(n int) string {
	if n <= 0 {
		return "1"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// BodyLimit caps request bodies.
func BodyLimit(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}

// requestOrigin reconstructs scheme://host for the current request, honouring
// a reverse proxy's X-Forwarded-Proto.
func requestOrigin(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}
