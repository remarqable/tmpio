package controllers

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/db"
	"github.com/remarqable/tmpio/internal/platform/errors"
)

func postForm(c *client, path string, vals url.Values, origin string) (int, string) {
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if origin != "" {
		h["Origin"] = origin
	}
	res := c.do("POST", path, strings.NewReader(vals.Encode()), h)
	res.Body.Close()
	return res.StatusCode, res.Header.Get("Location")
}

func waitlistCount(t *testing.T) int64 {
	var n int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT count(*) FROM launch_signup`).Scan(&n).Error)
	return n
}

func TestWaitlistAndClosedSignups(t *testing.T) {
	h := newHarness(t)
	anon := h.anon()

	// Valid address: recorded once, same answer on repeat.
	status, loc := postForm(anon, "/waitlist", url.Values{"email": {"Person@Example.com"}, "source": {"landing"}}, h.origin)
	require.Equal(t, 303, status)
	require.Equal(t, "/?joined=1#notify", loc)
	status, loc = postForm(anon, "/waitlist", url.Values{"email": {"person@example.com"}}, h.origin)
	require.Equal(t, 303, status)
	require.Equal(t, "/?joined=1#notify", loc)
	require.EqualValues(t, 1, waitlistCount(t))

	// Invalid address, honeypot and cross-origin: nothing stored.
	status, loc = postForm(anon, "/waitlist", url.Values{"email": {"not-an-address"}}, h.origin)
	require.Equal(t, 303, status)
	require.Equal(t, "/?waitlist_error=1#notify", loc)
	status, _ = postForm(anon, "/waitlist", url.Values{"email": {"bot@example.com"}, "website": {"http://spam"}}, h.origin)
	require.Equal(t, 303, status)
	status, _ = postForm(anon, "/waitlist", url.Values{"email": {"evil@example.com"}}, "https://evil.example")
	require.Equal(t, 403, status)
	require.EqualValues(t, 1, waitlistCount(t))

	// An existing account, then sign-ups close.
	existing := h.signIn("existing@x.test")
	res := existing.form("/logout", url.Values{})
	res.Body.Close()
	h.cfg.SignupsEnabled = false

	// Landing page offers the waitlist and no account creation.
	res = anon.do("GET", "/", nil, nil)
	body := readAll(res)
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, body, `action="/waitlist"`)
	require.Contains(t, body, "https://example.test/src")
	require.NotContains(t, body, `href="/auth/google"`)

	// Existing identity still signs in.
	status, loc = postForm(anon, "/auth/dev", url.Values{"email": {"existing@x.test"}}, h.origin)
	require.Equal(t, 302, status)
	require.Equal(t, "/", loc)

	// A new identity is refused, nothing is created, and the visitor lands on the waitlist.
	newcomer := h.anon()
	status, loc = postForm(newcomer, "/auth/dev", url.Values{"email": {"newcomer@x.test"}}, h.origin)
	require.Equal(t, 302, status)
	require.Equal(t, "/?closed=1#notify", loc)
	var users int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT count(*) FROM "user" WHERE email = 'newcomer@x.test'`).Scan(&users).Error)
	require.EqualValues(t, 0, users)
	res = newcomer.do("GET", "/admin", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	res.Body.Close()
	require.Equal(t, 302, res.StatusCode, "no session was issued")

	h.cfg.SignupsEnabled = true
}

func TestAIFilingOptInAndAccountDeletion(t *testing.T) {
	h := newHarness(t)
	c := h.signIn("owner@x.test")
	ctx := t.Context()

	// AI filing is off by default and toggles from settings.
	tn, err := models.GetTenantByCode(ctx, c.org)
	require.NoError(t, err)
	require.False(t, tn.AIFilingEnabled)
	res := c.form("/admin/settings/ai", url.Values{"ai_filing": {"1"}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	tn, err = models.GetTenantByCode(ctx, c.org)
	require.NoError(t, err)
	require.True(t, tn.AIFilingEnabled)
	res = c.form("/admin/settings/ai", url.Values{"ai_filing": {"0"}})
	res.Body.Close()
	tn, err = models.GetTenantByCode(ctx, c.org)
	require.NoError(t, err)
	require.False(t, tn.AIFilingEnabled)

	// The settings page renders both new cards.
	res = c.do("GET", "/admin/settings", nil, nil)
	body := readAll(res)
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, body, `action="/admin/settings/delete-account"`)
	require.Contains(t, body, "AI-assisted filing")

	// Some content, a token and a share link, so the cascade has something to remove.
	res, out := c.api("PUT", "/api/v1/orgs/"+c.org+"/entries?path=/notes/a.md", map[string]string{"content": "# A\n\nhello"}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	require.Equal(t, 201, res.StatusCode, out)
	_ = c.apiToken("content:read")
	res, out = c.api("POST", "/api/v1/orgs/"+c.org+"/share-links", map[string]any{"path": "/notes/a.md", "label": "x"}, nil)
	require.Equal(t, 201, res.StatusCode, out)
	shareURL, _ := out["url"].(string)
	require.NotEmpty(t, shareURL)

	// Wrong confirmation deletes nothing.
	res = c.form("/admin/settings/delete-account", url.Values{"confirm": {"WRONGCODE"}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	require.Contains(t, res.Header.Get("Location"), "/admin/settings?error=")
	_, err = models.GetTenantByCode(ctx, c.org)
	require.NoError(t, err)

	// The organization code, lowercase and padded, confirms.
	res = c.form("/admin/settings/delete-account", url.Values{"confirm": {" " + strings.ToLower(c.org) + " "}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	require.Equal(t, "/?deleted=1", res.Header.Get("Location"))

	// Everything is gone: tenant, user, content, credentials, session.
	_, err = models.GetTenantByCode(ctx, c.org)
	require.True(t, errors.Is(err, errors.CodeNotFound))
	owner := db.OwnerForTest(t)
	for _, q := range []string{
		`SELECT count(*) FROM "user" WHERE email = 'owner@x.test'`,
		`SELECT count(*) FROM entry`, `SELECT count(*) FROM revision`, `SELECT count(*) FROM share_grant`,
		`SELECT count(*) FROM api_token`, `SELECT count(*) FROM membership`, `SELECT count(*) FROM session WHERE revoked_at IS NULL`,
	} {
		var n int64
		require.NoError(t, owner.Raw(q).Scan(&n).Error)
		require.EqualValues(t, 0, n, q)
	}
	res = c.do("GET", "/admin", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	res.Body.Close()
	require.Equal(t, 302, res.StatusCode, "the session no longer exists")
	if shareURL != "" {
		res = h.anon().do("GET", strings.TrimPrefix(shareURL, h.origin), nil, nil)
		res.Body.Close()
		require.Equal(t, 404, res.StatusCode)
	}

	// The same person can start over with a fresh account.
	again := h.signIn("owner@x.test")
	require.NotEqual(t, c.org, again.org)
}

// TestAuditRegressions covers the audit findings fixed in this change: the
// legacy /app redirect must stay on this origin, and an anonymous browser must
// not be able to tell a real organization code from an invented one.
func TestAuditRegressions(t *testing.T) {
	h := newHarness(t)
	real := h.signIn("oracle@x.test")
	anon := h.anon()

	// The legacy redirect never leaves the origin, whatever the query says.
	for _, path := range []string{
		"/app/content?path=//evil.example",
		"/app/content?path=/\\evil.example",
		"/app/content?path=https://evil.example",
		"/app/content?path=//evil.example/a/b",
	} {
		res := anon.do("GET", path, nil, nil)
		res.Body.Close()
		loc := res.Header.Get("Location")
		require.NotEmpty(t, loc, path)
		require.True(t, strings.HasPrefix(loc, "/"), "%s -> %s", path, loc)
		require.False(t, strings.HasPrefix(loc, "//"), "%s -> %s protocol-relative", path, loc)
		require.NotContains(t, loc, "evil.example", path)
	}
	// A legitimate directory still round-trips.
	res := anon.do("GET", "/app/content?path=/research", nil, nil)
	res.Body.Close()
	require.Equal(t, "/research/", res.Header.Get("Location"))

	// A real organization code and an invented one answer identically.
	nav := map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"}
	realRes := anon.do("GET", "/o:"+real.org+"/", nil, nav)
	realRes.Body.Close()
	fakeRes := anon.do("GET", "/o:ZZZZZZZZ/", nil, nav)
	fakeRes.Body.Close()
	require.Equal(t, realRes.StatusCode, fakeRes.StatusCode, "status distinguishes a real org code")
	// Both go to sign-in. The only difference is the return path, which is the
	// URL the visitor themselves asked for, so it reveals nothing they did not know.
	realLoc, err := url.Parse(realRes.Header.Get("Location"))
	require.NoError(t, err)
	fakeLoc, err := url.Parse(fakeRes.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "/login", realLoc.Path)
	require.Equal(t, "/login", fakeLoc.Path)
	require.Equal(t, "/o:"+real.org+"/", realLoc.Query().Get("return"))
	require.Equal(t, "/o:ZZZZZZZZ/", fakeLoc.Query().Get("return"))

	// Non-navigation requests are 404 for both, with no sign-in hint.
	realAPI := anon.do("GET", "/o:"+real.org+"/", nil, map[string]string{"Accept": "application/json"})
	realAPI.Body.Close()
	fakeAPI := anon.do("GET", "/o:ZZZZZZZZ/", nil, map[string]string{"Accept": "application/json"})
	fakeAPI.Body.Close()
	require.Equal(t, 404, realAPI.StatusCode)
	require.Equal(t, 404, fakeAPI.StatusCode)

	// A signed-in stranger still cannot read another organization.
	stranger := h.signIn("stranger@x.test")
	res = stranger.do("GET", "/o:"+real.org+"/", nil, nav)
	res.Body.Close()
	require.Equal(t, 404, res.StatusCode)
}
