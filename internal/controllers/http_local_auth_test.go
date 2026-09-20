package controllers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/middleware"
	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/db"
)

const ownerPassword = "correct horse battery staple"

// provisionOwner does at test time what startup does on a self-hosted instance.
func provisionOwner(t *testing.T, email, password string) bool {
	t.Helper()
	created, err := models.EnsureLocalOwner(t.Context(), email, password, models.DefaultSiteConfigYAML, models.DefaultIndexMarkdown)
	require.NoError(t, err)
	return created
}

func hasSessionCookie(c *client) bool {
	u, _ := url.Parse(c.h.origin)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == middleware.SessionCookie && ck.Value != "" {
			return true
		}
	}
	return false
}

func TestLocalOwnerSignIn(t *testing.T) {
	h := newHarness(t)
	h.cfg.LocalAuth = true
	h.cfg.SignupsEnabled = false // the owner exists; nobody else may sign up

	// First boot creates the account, its organization and its starting pages.
	require.True(t, provisionOwner(t, "owner@x.test", ownerPassword))
	// Booting again with the same password changes nothing.
	require.False(t, provisionOwner(t, "owner@x.test", ownerPassword))

	// The sign-in page offers the password form.
	anon := h.anon()
	res := anon.do("GET", "/login", nil, nil)
	body := readAll(res)
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, body, `action="/auth/local"`)

	// A wrong password is refused, says nothing about which half was wrong,
	// and leaves no session behind.
	status, loc := postForm(anon, "/auth/local", url.Values{"username": {"owner@x.test"}, "password": {"wrong"}}, h.origin)
	require.Equal(t, 303, status)
	require.Equal(t, "/login?failed=1", loc)
	require.False(t, hasSessionCookie(anon))

	// An address with no credential is refused exactly the same way.
	status, loc = postForm(anon, "/auth/local", url.Values{"username": {"stranger@x.test"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 303, status)
	require.Equal(t, "/login?failed=1", loc)
	require.False(t, hasSessionCookie(anon))

	// The failure page names no address and no reason beyond the mismatch.
	res = anon.do("GET", "/login?failed=1", nil, nil)
	body = readAll(res)
	require.Contains(t, body, "do not match")
	require.NotContains(t, body, "owner@x.test")

	// The right password signs the owner in, and the address is not case bound.
	status, loc = postForm(anon, "/auth/local", url.Values{"username": {"Owner@X.test"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 302, status)
	require.Equal(t, "/", loc)
	require.True(t, hasSessionCookie(anon))

	res = anon.do("GET", "/admin", nil, nil)
	body = readAll(res)
	require.Equal(t, 200, res.StatusCode, "the owner should reach their own admin")
	require.Regexp(t, `o:[A-Z0-9]{8}`, body)

	// Cross-origin submissions are refused.
	status, _ = postForm(h.anon(), "/auth/local", url.Values{"username": {"owner@x.test"}, "password": {ownerPassword}}, "https://evil.example")
	require.Equal(t, 403, status)

	// With local sign-in off the route does not exist.
	h.cfg.LocalAuth = false
	status, _ = postForm(h.anon(), "/auth/local", url.Values{"username": {"owner@x.test"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 404, status)
	res = h.anon().do("GET", "/login", nil, nil)
	require.NotContains(t, readAll(res), `action="/auth/local"`)
}

func TestLocalOwnerPasswordRotation(t *testing.T) {
	h := newHarness(t)
	h.cfg.LocalAuth = true
	require.True(t, provisionOwner(t, "owner@x.test", ownerPassword))

	// A new password in the environment replaces the old one on the next boot.
	const rotated = "a different long password"
	require.False(t, provisionOwner(t, "owner@x.test", rotated))

	anon := h.anon()
	status, _ := postForm(anon, "/auth/local", url.Values{"username": {"owner@x.test"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 303, status, "the old password should no longer work")
	status, _ = postForm(anon, "/auth/local", url.Values{"username": {"owner@x.test"}, "password": {rotated}}, h.origin)
	require.Equal(t, 302, status, "the new password should work")

	// An empty password leaves the stored credential alone, so an operator can
	// drop OWNER_PASSWORD from the environment once the account exists.
	require.False(t, provisionOwner(t, "owner@x.test", ""))
	status, _ = postForm(h.anon(), "/auth/local", url.Values{"username": {"owner@x.test"}, "password": {rotated}}, h.origin)
	require.Equal(t, 302, status)

	// The account keeps one organization across all of that.
	var orgs int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT count(*) FROM tenant`).Scan(&orgs).Error)
	require.EqualValues(t, 1, orgs)
}

func TestLocalOwnerRefusesIncompleteConfiguration(t *testing.T) {
	newHarness(t)
	// No username at all.
	_, err := models.EnsureLocalOwner(t.Context(), "", ownerPassword, models.DefaultSiteConfigYAML, models.DefaultIndexMarkdown)
	require.Error(t, err)

	// A new account with no password cannot be created.
	_, err = models.EnsureLocalOwner(t.Context(), "admin", "", models.DefaultSiteConfigYAML, models.DefaultIndexMarkdown)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "owner_password")

	// A name that is not an address is a username, not a mistake. This is the
	// rule that changed: the account is "admin" on a private instance, and
	// there is no address to verify because this server issued the credential.
	created, err := models.EnsureLocalOwner(t.Context(), "not-an-address", ownerPassword, models.DefaultSiteConfigYAML, models.DefaultIndexMarkdown)
	require.NoError(t, err)
	require.True(t, created)
}

// TestForwardedForCannotForgeLoopback is the concrete attack the trusted-proxy
// list exists to stop: /metrics answers loopback clients only, so a stranger
// who can make the server believe their address is 127.0.0.1 reads it. Every
// per-IP rate limit key has the same shape.
func TestForwardedForCannotForgeLoopback(t *testing.T) {
	h := newHarness(t)

	probe := func(remote, forwarded string) int {
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.RemoteAddr = remote
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		rec := httptest.NewRecorder()
		h.deps.SetupRouter(nil).ServeHTTP(rec, req)
		return rec.Code
	}

	// A real loopback client still reads metrics.
	require.Equal(t, http.StatusOK, probe("127.0.0.1:5000", ""))

	// A stranger claiming to be loopback does not. Their address is not in the
	// trusted list, so the header is theirs to write and nobody's to believe.
	require.NotEqual(t, http.StatusOK, probe("203.0.113.9:5000", "127.0.0.1"),
		"X-Forwarded-For from an untrusted peer must not grant loopback access")

	// Behind a real proxy the header is the only way to see the caller, so a
	// trusted peer is believed: a forwarded public address is refused even
	// though the connection itself came from loopback.
	require.NotEqual(t, http.StatusOK, probe("127.0.0.1:5000", "203.0.113.9"),
		"a forwarded address from a trusted proxy must be honoured")

	// Trusting nobody ignores the header even from loopback.
	h.deps.Cfg.TrustedProxies = nil
	require.Equal(t, http.StatusOK, probe("127.0.0.1:5000", "203.0.113.9"))
}

// TestAdminIsTheDefaultAccount covers the shape a fresh self-install gets: a
// username rather than an address, which the verified-email rule used to
// forbid because it exists to distrust identity providers, not this server.
func TestAdminIsTheDefaultAccount(t *testing.T) {
	h := newHarness(t)
	h.cfg.LocalAuth = true
	h.cfg.SignupsEnabled = false

	require.True(t, provisionOwner(t, "admin", ownerPassword))

	anon := h.anon()
	status, loc := postForm(anon, "/auth/local", url.Values{"username": {"admin"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 302, status)
	require.Equal(t, "/", loc)
	require.True(t, hasSessionCookie(anon))

	res := anon.do("GET", "/admin", nil, nil)
	require.Equal(t, 200, res.StatusCode, "admin should reach their own site")
	res.Body.Close()

	// Wrong password for a real username is refused like anything else.
	status, _ = postForm(h.anon(), "/auth/local", url.Values{"username": {"admin"}, "password": {"wrong"}}, h.origin)
	require.Equal(t, 303, status)
}

// The form field was called email before usernames existed. A page cached in
// someone's browser must still be able to sign in.
func TestLegacyEmailFieldStillAccepted(t *testing.T) {
	h := newHarness(t)
	h.cfg.LocalAuth = true
	require.True(t, provisionOwner(t, "admin", ownerPassword))

	anon := h.anon()
	status, _ := postForm(anon, "/auth/local", url.Values{"email": {"admin"}, "password": {ownerPassword}}, h.origin)
	require.Equal(t, 302, status)
	require.True(t, hasSessionCookie(anon))
}
