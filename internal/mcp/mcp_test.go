package mcp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/assets"
	"github.com/remarqable/tmpio/internal/controllers"
	tmpmcp "github.com/remarqable/tmpio/internal/mcp"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/auth"
	"github.com/remarqable/tmpio/internal/platform/config"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/i18n"
)

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	if b.token != "" {
		r2.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r2)
}

func setup(t *testing.T) (*httptest.Server, *config.Config, *models.Ops, models.Principal, *models.Tenant) {
	db.ConnectTest(t)
	cfg := &config.Config{AppEnv: "test", DatabaseURL: "x", SessionSecret: []byte("0123456789abcdef0123456789abcdef"), DevLoginBypass: true,
		Quotas: config.Quotas{MaxPages: 1000, MaxDirectories: 100, MaxPageBytes: 256 * 1024, MaxConfigBytes: 32 * 1024, MaxAssetBytes: 5 << 20, MaxDocumentBytes: 20 << 20, MaxFileBytes: 1 << 20, MaxAssetPixels: 20_000_000, MaxCurrentAssetBytes: 100 << 20, MaxRetainedBytes: 500 << 20}}
	cfg.OAuthClients = []config.OAuthClient{{ID: "local-test", Name: "Local test client", RedirectURIs: []string{"http://127.0.0.1:9876/cb"}, Public: true}}
	require.NoError(t, i18n.Preload("en"))
	tmpl, err := assets.Templates(controllers.FuncMap())
	require.NoError(t, err)
	ops := &models.Ops{Quotas: cfg.Quotas, CursorKey: cfg.SessionSecret}
	deps := &controllers.Deps{Cfg: cfg, Ops: ops, Google: auth.NewGoogle(cfg), Tmpl: tmpl}
	srv := httptest.NewUnstartedServer(nil)
	cfg.AppOrigin = "http://" + srv.Listener.Addr().String()
	require.NoError(t, models.SyncOAuthClients(t.Context(), cfg.OAuthClients))
	srv.Config.Handler = deps.SetupRouter(tmpmcp.New(cfg, ops).Handler())
	srv.Start()
	t.Cleanup(srv.Close)
	u, tn, _, err := models.SignIn(t.Context(), models.ExternalIdentity{Issuer: "dev", Subject: "mcp@x.test", Email: "mcp@x.test", EmailVerified: true}, models.DefaultSiteConfigYAML, models.DefaultIndexMarkdown, true)
	require.NoError(t, err)
	return srv, cfg, ops, models.Principal{Kind: models.PrincipalOwner, TenantID: tn.ID, UserID: u.ID, Scopes: models.AllScopes}, tn
}

func issue(t *testing.T, cfg *config.Config, p models.Principal, scopes ...string) (*models.TokenPair, int64) {
	code, err := models.AuthorizeClient(t.Context(), p, "local-test", "http://127.0.0.1:9876/cb", cfg.AppOrigin+"/mcp", "chal", "S256", scopes)
	require.NoError(t, err)
	rec, err := models.ConsumeAuthCode(t.Context(), code)
	require.NoError(t, err)
	pair, err := models.IssueTokens(t.Context(), rec.TenantID, rec.GrantID, cfg.AppOrigin+"/mcp", "")
	require.NoError(t, err)
	return pair, rec.GrantID
}

func connect(t *testing.T, srv *httptest.Server, token string) *mcp.ClientSession {
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}}}
	sess, err := client.Connect(t.Context(), tr, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (map[string]any, bool) {
	res, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	var out map[string]any
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			_ = json.Unmarshal([]byte(tc.Text), &out)
		}
	}
	return out, res.IsError
}

func TestMCPProtocolAndTools(t *testing.T) {
	srv, cfg, _, p, tn := setup(t)

	// Unauthenticated requests get a bearer challenge with resource metadata.
	res, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, 401, res.StatusCode)
	assert.Contains(t, res.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp")
	res, err = http.Get(srv.URL + "/.well-known/oauth-protected-resource/mcp")
	require.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()

	// A wrong-audience credential (REST API token) is refused at /mcp.
	plain, _, err := models.CreateAPIToken(t.Context(), p, "cli", []string{models.ScopeRead}, 0)
	require.NoError(t, err)
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+plain)
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, 401, res.StatusCode)

	pair, grantID := issue(t, cfg, p, models.ScopeRead, models.ScopeWrite)
	sess := connect(t, srv, pair.AccessToken)

	tools, err := sess.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = tl
	}
	for _, n := range []string{"tmp_info", "tmp_list", "tmp_read", "tmp_search", "tmp_write", "tmp_mkdir", "tmp_move", "tmp_delete", "tmp_history", "tmp_restore", "tmp_organize"} {
		require.Contains(t, names, n)
	}
	assert.True(t, names["tmp_read"].Annotations.ReadOnlyHint)
	assert.False(t, names["tmp_write"].Annotations.ReadOnlyHint)
	assert.True(t, *names["tmp_delete"].Annotations.DestructiveHint)
	assert.False(t, *names["tmp_write"].Annotations.DestructiveHint)
	assert.Contains(t, names["tmp_write"].Description, "immediately visible")
	assert.Contains(t, strings.ToLower(tools.Tools[0].Description+names["tmp_info"].Description), "tmp")

	info, isErr := call(t, sess, "tmp_info", map[string]any{})
	require.False(t, isErr)
	assert.Equal(t, tn.Code, info["org"])
	assert.Equal(t, true, info["private"])
	assert.Equal(t, cfg.AppOrigin+"/o:"+tn.Code, info["url_base"])
	assert.ElementsMatch(t, []any{"content:read", "content:write"}, info["scopes"])

	out, isErr := call(t, sess, "tmp_write", map[string]any{"path": "/research/circle.md", "content": "---\ntitle: Circle\n---\n\n# Circle\n\nFrom MCP.\n", "expected_revision": 0, "request_id": "11111111-2222-4333-8444-555555555555"})
	require.False(t, isErr, out)
	assert.Equal(t, true, out["created"])
	assert.EqualValues(t, 1, out["revision"])
	urls := out["urls"].(map[string]any)
	assert.Equal(t, cfg.AppOrigin+"/o:"+tn.Code+"/research/circle", urls["html"])
	assert.NotContains(t, urls["html"], "/s/")

	// The returned URLs resolve to the committed revision for an authorized reader.
	req, _ = http.NewRequest("GET", urls["raw"].(string), nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	res, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()

	// Filing: tmp_organize suggests a location without writing; tmp_write "auto" files and writes.
	org, isErr := call(t, sess, "tmp_organize", map[string]any{"content": "# Circle pricing\n\nCircle charges $89/mo. Research notes.\n"})
	require.False(t, isErr, org)
	placement := org["placement"].(map[string]any)
	assert.Equal(t, "/research/circle-pricing.md", placement["path"], "heuristic picks the folder named in the text")
	assert.Equal(t, "heuristic", placement["source"])
	assert.Equal(t, "/research/circle-pricing.md", org["write_with"].(map[string]any)["path"])
	rd, isErr := call(t, sess, "tmp_read", map[string]any{"path": "/research/circle-pricing.md"})
	assert.True(t, isErr, rd, "organize wrote nothing")
	auto, isErr := call(t, sess, "tmp_write", map[string]any{"path": "auto", "content": "# Circle pricing\n\nCircle charges $89/mo. Research notes.\n", "expected_revision": 0, "request_id": "11111111-2222-4333-8444-5555555555c6"})
	require.False(t, isErr, auto)
	assert.Equal(t, true, auto["created"])
	assert.Equal(t, "/research/circle-pricing.md", auto["path"])
	assert.NotNil(t, auto["placement"])
	auto2, isErr := call(t, sess, "tmp_write", map[string]any{"path": "auto", "content": "# Circle pricing\n\nSecond note about Circle research.\n", "expected_revision": 0, "request_id": "11111111-2222-4333-8444-5555555555c7"})
	require.False(t, isErr, auto2)
	assert.Equal(t, "/research/circle-pricing-2.md", auto2["path"], "auto never overwrites: an occupied path gets a numbered name")

	// Retry with the same request id is idempotent.
	out, isErr = call(t, sess, "tmp_write", map[string]any{"path": "/research/circle.md", "content": "---\ntitle: Circle\n---\n\n# Circle\n\nFrom MCP.\n", "expected_revision": 0, "request_id": "11111111-2222-4333-8444-555555555555"})
	require.False(t, isErr)
	assert.EqualValues(t, 1, out["revision"])

	// Stale write → structured conflict with next step.
	out, isErr = call(t, sess, "tmp_write", map[string]any{"path": "/research/circle.md", "content": "# X\n", "expected_revision": 9, "request_id": "11111111-2222-4333-8444-555555555556"})
	assert.True(t, isErr)
	assert.Equal(t, "revision_conflict", out["code"])
	assert.EqualValues(t, 1, out["current_revision"])
	assert.Contains(t, out["next_step"], "tmp_read")

	// Read, list, search, history.
	out, isErr = call(t, sess, "tmp_read", map[string]any{"path": "/research/circle.md"})
	require.False(t, isErr)
	assert.Contains(t, out["content"], "From MCP")
	out, isErr = call(t, sess, "tmp_list", map[string]any{"path": "/research"})
	require.False(t, isErr)
	assert.Len(t, out["entries"], 3, "circle plus the two auto-filed pages")
	out, isErr = call(t, sess, "tmp_search", map[string]any{"query": "MCP"})
	require.False(t, isErr)
	assert.Len(t, out["hits"], 1)
	out, isErr = call(t, sess, "tmp_history", map[string]any{"path": "/research/circle.md"})
	require.False(t, isErr)
	assert.Len(t, out["items"], 1)

	// Validation failures are line-numbered tool errors, not protocol errors.
	out, isErr = call(t, sess, "tmp_write", map[string]any{"path": "/bad.md", "content": "# x\n\n<script>1</script>\n", "expected_revision": 0, "request_id": "11111111-2222-4333-8444-555555555557"})
	assert.True(t, isErr)
	assert.Equal(t, "validation_failed", out["code"])
	assert.NotEmpty(t, out["field_errors"])

	// Missing scope.
	out, isErr = call(t, sess, "tmp_delete", map[string]any{"path": "/research/circle.md", "expected_revision": 1, "request_id": "11111111-2222-4333-8444-555555555558"})
	assert.True(t, isErr)
	assert.Equal(t, "insufficient_scope", out["code"])

	// Move and restore.
	out, isErr = call(t, sess, "tmp_move", map[string]any{"from": "/research/circle.md", "to": "/notes/circle.md", "expected_revision": 1, "request_id": "11111111-2222-4333-8444-555555555559"})
	require.False(t, isErr, out)
	assert.Equal(t, "/notes/circle.md", out["path"])
	out, isErr = call(t, sess, "tmp_restore", map[string]any{"path": "/notes/circle.md", "revision": 1, "expected_revision": 2, "request_id": "11111111-2222-4333-8444-55555555555a"})
	require.False(t, isErr, out)
	assert.EqualValues(t, 3, out["revision"])

	// Revoking the connection stops the existing session immediately.
	require.NoError(t, models.RevokeConnection(t.Context(), p, grantID))
	_, err = sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "tmp_info", Arguments: map[string]any{}})
	assert.Error(t, err, "revoked credential is rejected on the live session")

	// Delete scope works with a full grant.
	pair2, _ := issue(t, cfg, p, models.AllScopes...)
	sess2 := connect(t, srv, pair2.AccessToken)
	out, isErr = call(t, sess2, "tmp_delete", map[string]any{"path": "/notes/circle.md", "expected_revision": 3, "request_id": "11111111-2222-4333-8444-55555555555b"})
	require.False(t, isErr, out)
	assert.Equal(t, true, out["deleted"])
	out, isErr = call(t, sess2, "tmp_read", map[string]any{"path": "/notes/circle.md"})
	require.False(t, isErr)
	assert.Equal(t, true, out["deleted"], "authorized readers get a tombstone")
}
