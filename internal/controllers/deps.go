// Package controllers holds the thin HTTP adapters: browser pages, REST, secret
// links and OAuth. They parse input, call models and render; they never
// implement permission or storage logic of their own.
package controllers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/remarqable/tmpio/internal/assets"
	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/auth"
	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/errors"
	"github.com/remarqable/tmpio/internal/platform/i18n"
	"github.com/remarqable/tmpio/internal/platform/obs"
	"github.com/remarqable/tmpio/internal/platform/ratelimit"
)

// Deps are the shared dependencies of every controller.
type Deps struct {
	Cfg    *config.Config
	Ops    *models.Ops
	Google *auth.Google
	Tmpl   map[string]*template.Template

	limOnce     sync.Once
	shareReads  *ratelimit.Limiter // per grant
	shareWrites *ratelimit.Limiter // per grant
}

// limiters lazily creates the per-grant limiters (30 writes and 120 reads per minute).
func (d *Deps) limiters() (*ratelimit.Limiter, *ratelimit.Limiter) {
	d.limOnce.Do(func() {
		d.shareReads = ratelimit.New(120)
		d.shareWrites = ratelimit.New(30)
	})
	return d.shareReads, d.shareWrites
}

// FuncMap returns the template functions.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"t": func(key string, vars ...string) string { return i18n.T("en", key, vars...) },
		"date": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format("2006-01-02 15:04 UTC")
		},
		"dateptr": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.UTC().Format("2006-01-02 15:04 UTC")
		},
		"join":      strings.Join,
		"lower":     strings.ToLower,
		"hasPrefix": strings.HasPrefix,
		"query":     url.QueryEscape,
		"json": func(v any) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				if k, ok := kv[i].(string); ok {
					m[k] = kv[i+1]
				}
			}
			return m
		},
		"add":   func(a, b int) int { return a + b },
		"asset": func(p string) string { return "/static/" + p + "?v=" + assets.Version },
		"htmlpath": func(p string) string { // source path → rendered path
			if strings.HasSuffix(p, ".md") {
				return (&models.Entry{Kind: models.KindPage, Path: p}).HTMLPath()
			}
			return p
		},
		// bytesize renders a file size the way a file browser does. Directories
		// carry no size of their own, so a zero is shown as a dash.
		"bytesize": func(n int64) string {
			switch {
			case n <= 0:
				return "\u2014"
			case n < 1024:
				return fmt.Sprintf("%d B", n)
			case n < 1024*1024:
				return fmt.Sprintf("%.1f KB", float64(n)/1024)
			default:
				return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
			}
		},
		// ago is a coarse relative time. Anything older than a month falls back
		// to a date, because "37 days ago" is not how anyone thinks about it.
		"ago": func(t time.Time) string {
			if t.IsZero() {
				return "\u2014"
			}
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%d min ago", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%d hr ago", int(d.Hours()))
			case d < 48*time.Hour:
				return "yesterday"
			case d < 30*24*time.Hour:
				return fmt.Sprintf("%d days ago", int(d.Hours()/24))
			default:
				return t.UTC().Format("2 Jan 2006")
			}
		},
		"int64": func(v any) int64 {
			switch n := v.(type) {
			case int64:
				return n
			case int:
				return int64(n)
			}
			return 0
		},
	}
}

// base assembles the data every template can rely on.
func (d *Deps) base(c *gin.Context, extra gin.H) gin.H {
	data := gin.H{
		"Lang":          "en",
		"RequestID":     c.GetString("request_id"),
		"Origin":        d.Cfg.AppOrigin,
		"DevBypass":     d.Cfg.DevLoginBypass,
		"GoogleEnabled": d.Cfg.GoogleEnabled(),
		"SignupsOpen":   d.Cfg.SignupsEnabled,
		"LocalAuth":     d.Cfg.LocalAuth,
		"SourceURL":     d.Cfg.SourceURL,
		"Path":          c.Request.URL.Path,
		"Now":           time.Now(),
	}
	if p, sess, user, tenant, ok := middleware.OwnerSession(c); ok {
		data["Principal"] = p
		data["CSRF"] = sess.CSRFToken
		data["User"] = user
		data["Tenant"] = tenant
		data["OrgCode"] = tenant.Code
		data["OrgPrefix"] = "/o:" + tenant.Code
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// render executes a page inside a layout.
func (d *Deps) render(c *gin.Context, status int, page, layout string, data gin.H) {
	t, ok := d.Tmpl[page]
	if !ok {
		obs.From(c.Request.Context()).Error().Str("page", page).Msg("template missing")
		c.String(http.StatusInternalServerError, "template missing")
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := t.ExecuteTemplate(c.Writer, layout, d.base(c, data)); err != nil {
		obs.From(c.Request.Context()).Error().Err(err).Str("page", page).Msg("template error")
	}
}

// renderPartial executes one partial (an HTMX fragment) without a layout.
func (d *Deps) renderPartial(c *gin.Context, status int, name string, data gin.H) {
	t, ok := d.Tmpl["pages/errors/error.html"] // every page set includes all partials
	if !ok {
		c.String(http.StatusInternalServerError, "template missing")
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := t.ExecuteTemplate(c.Writer, name, data); err != nil {
		obs.From(c.Request.Context()).Error().Err(err).Str("partial", name).Msg("template error")
	}
}

// renderError shows an HTML error page for browser requests.
func (d *Deps) renderError(c *gin.Context, status int, message string) {
	if status == 0 {
		status = 500
	}
	title := i18n.T("en", "error.500")
	detail := message
	switch status {
	case 404:
		title = i18n.T("en", "error.404")
		if detail == "" {
			detail = i18n.T("en", "error.404_detail")
		}
	case 401:
		title = i18n.T("en", "error.401")
	case 403:
		title = i18n.T("en", "error.403")
	case 500:
		detail = i18n.T("en", "error.500_detail")
	}
	middleware.NoStore(c)
	d.render(c, status, "pages/errors/error.html", "layout/bare", gin.H{"Status": status, "Title": title, "Detail": detail})
}

// fail maps an error to the right response for the request kind.
func (d *Deps) fail(c *gin.Context, err error) {
	ae := errors.As(err)
	if ae.Code == errors.CodeUnknown {
		obs.From(c.Request.Context()).Error().Err(err).Msg("internal error")
	}
	if wantsJSON(c) {
		jsonError(c, ae)
		return
	}
	msg := ae.Message
	if ae.HTTPStatus() >= 500 {
		msg = ""
	}
	d.renderError(c, ae.HTTPStatus(), msg)
}

// jsonError writes the structured error envelope (section 9).
func jsonError(c *gin.Context, ae *errors.AppError) {
	body := gin.H{"code": ae.Code, "message": ae.Message}
	if ae.HTTPStatus() >= 500 {
		body["message"] = "internal error"
	}
	if len(ae.FieldErrors) > 0 {
		body["field_errors"] = ae.FieldErrors
	}
	if ae.CurrentRevision != 0 {
		body["current_revision"] = ae.CurrentRevision
	}
	if ae.ExpectedRevision != 0 {
		body["expected_revision"] = ae.ExpectedRevision
	}
	if ae.SuggestedPath != "" {
		body["suggested_path"] = ae.SuggestedPath
	}
	if ae.RetryAfter > 0 {
		c.Header("Retry-After", itoa(ae.RetryAfter))
	}
	middleware.NoStore(c)
	c.AbortWithStatusJSON(ae.HTTPStatus(), gin.H{"error": body, "request_id": c.GetString("request_id")})
}

func wantsJSON(c *gin.Context) bool {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		return true
	}
	if _, bearer := c.Get(middleware.KeyBearer); bearer {
		return true
	}
	accept := c.GetHeader("Accept")
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}

// isNavigation reports whether the request is a browser HTML document
// navigation without bearer credentials.
func isNavigation(c *gin.Context) bool {
	if _, bearer := c.Get(middleware.KeyBearer); bearer {
		return false
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		return false
	}
	if mode := c.GetHeader("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(c.GetHeader("Accept"), "text/html")
}

// safeReturnPath accepts only validated same-origin local paths.
func safeReturnPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "\\") || strings.ContainsAny(p, "\r\n") {
		return ""
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return ""
	}
	if _, err := models.RequestPath(u.EscapedPath()); err != nil {
		return ""
	}
	if strings.HasPrefix(u.Path, "/auth/") || strings.HasPrefix(u.Path, "/oauth/token") || u.Path == "/logout" {
		return ""
	}
	return u.String()
}

func (d *Deps) redirectToLogin(c *gin.Context) {
	ret := safeReturnPath(c.Request.URL.RequestURI())
	middleware.NoStore(c)
	if ret == "" || ret == "/" {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	c.Redirect(http.StatusFound, "/login?return="+url.QueryEscape(ret))
}

// abs makes a site-relative path absolute on the application origin.
func (d *Deps) abs(p string) string { return d.Cfg.AppOrigin + p }

// orgPrefix is the explicit address prefix for a tenant.
func orgPrefix(t *models.Tenant) string { return "/o:" + t.Code }

// entryURLs returns absolute, org-qualified representation URLs for tools and APIs.
func (d *Deps) entryURLs(t *models.Tenant, info models.EntryInfo) map[string]string {
	pre := orgPrefix(t)
	m := map[string]string{
		"html": d.abs(pre + info.HTMLPath),
		"raw":  d.abs(pre + info.RawPath),
	}
	if info.JSONPath != "" {
		m["json"] = d.abs(pre + info.JSONPath)
	}
	return m
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// siteName resolves config name → organization name → the unnamed-site label.
func siteName(cfg *models.SiteConfig, t *models.Tenant) string {
	if cfg != nil && cfg.Name != "" {
		return cfg.Name
	}
	if t != nil && t.DisplayName() != "" {
		return t.DisplayName()
	}
	return i18n.T("en", "dash.your_site")
}
