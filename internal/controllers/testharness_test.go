package controllers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/assets"
	tmpmcp "github.com/remarqable/tmpio/internal/mcp"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/auth"
	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/i18n"
)

// harness is a full application server on a clean test database.
type harness struct {
	t      *testing.T
	cfg    *config.Config
	srv    *httptest.Server
	origin string
	deps   *Deps
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db.ConnectTest(t)
	cfg := &config.Config{
		AppEnv: "test", Port: "0", DatabaseURL: "x", SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		DevLoginBypass: true, SignupsEnabled: true, SourceURL: "https://example.test/src",
		TrustedProxies: []string{"127.0.0.1", "::1"},
		Quotas:         config.Quotas{MaxPages: 1000, MaxDirectories: 100, MaxPageBytes: 256 * 1024, MaxConfigBytes: 32 * 1024, MaxAssetBytes: 5 << 20, MaxDocumentBytes: 20 << 20, MaxFileBytes: 1 << 20, MaxAssetPixels: 20_000_000, MaxCurrentAssetBytes: 100 << 20, MaxRetainedBytes: 500 << 20},
	}
	cfg.OAuthClients = append(cfg.OAuthClients, config.OAuthClient{ID: "local-test", Name: "Local test client", RedirectURIs: []string{"http://127.0.0.1:9876/cb"}, Public: true})
	require.NoError(t, i18n.Preload("en"))
	tmpl, err := assets.Templates(FuncMap())
	require.NoError(t, err)
	ops := &models.Ops{Quotas: cfg.Quotas, CursorKey: cfg.SessionSecret}
	deps := &Deps{Cfg: cfg, Ops: ops, Google: auth.NewGoogle(cfg), Tmpl: tmpl}
	var srv *httptest.Server
	// The MCP handler needs the origin before the server exists; use a listener first.
	srv = httptest.NewUnstartedServer(nil)
	cfg.AppOrigin = "http://" + srv.Listener.Addr().String()
	require.NoError(t, models.SyncOAuthClients(t.Context(), cfg.OAuthClients))
	srv.Config.Handler = deps.SetupRouter(tmpmcp.New(cfg, ops).Handler())
	srv.Start()
	t.Cleanup(srv.Close)
	return &harness{t: t, cfg: cfg, srv: srv, origin: cfg.AppOrigin, deps: deps}
}

// client is a browser-like client with its own cookie jar; redirects are not followed.
type client struct {
	h    *harness
	http *http.Client
	csrf string
	org  string
	tok  string // bearer, if any
}

func (h *harness) anon() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// signIn creates/logs in a dev user and captures CSRF token and org code.
func (h *harness) signIn(email string) *client {
	c := h.anon()
	res := c.do("POST", "/auth/dev", strings.NewReader(url.Values{"email": {email}, "name": {email}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	require.Equal(h.t, 302, res.StatusCode, "dev login")
	res.Body.Close()
	res = c.do("GET", "/admin", nil, nil)
	body := readAll(res)
	require.Equal(h.t, 200, res.StatusCode, "admin: %s", body[:min(len(body), 300)])
	c.csrf = regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(body)[1]
	c.org = regexp.MustCompile(`o:([A-Z0-9]{8})`).FindStringSubmatch(body)[1]
	return c
}

func (c *client) do(method, path string, body io.Reader, headers map[string]string) *http.Response {
	req, err := http.NewRequest(method, c.h.origin+path, body)
	require.NoError(c.h.t, err)
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	require.NoError(c.h.t, err)
	return res
}

// form posts a CSRF-protected dashboard form.
func (c *client) form(path string, vals url.Values) *http.Response {
	vals.Set("csrf_token", c.csrf)
	return c.do("POST", path, strings.NewReader(vals.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": c.h.origin})
}

// api performs a JSON REST call using either the session (CSRF) or bearer token.
func (c *client) api(method, path string, body any, headers map[string]string) (*http.Response, map[string]any) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if c.tok == "" {
		h["X-CSRF-Token"] = c.csrf
		h["Origin"] = c.h.origin
	}
	for k, v := range headers {
		h[k] = v
	}
	res := c.do(method, path, rdr, h)
	var out map[string]any
	raw := readAll(res)
	_ = json.Unmarshal([]byte(raw), &out)
	if out == nil {
		out = map[string]any{"_raw": raw}
	}
	return res, out
}

func (c *client) apiToken(scopes ...string) string {
	vals := url.Values{"name": {"cli"}}
	for _, s := range scopes {
		vals.Add("scopes", s)
	}
	res := c.form("/admin/tokens", vals)
	res.Body.Close()
	require.Equal(c.h.t, 303, res.StatusCode)
	loc, _ := url.Parse(res.Header.Get("Location"))
	return loc.Query().Get("token")
}

func readAll(res *http.Response) string {
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b)
}
