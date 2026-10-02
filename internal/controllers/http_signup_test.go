package controllers

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/models"
	"github.com/remarqable/tmpio/internal/platform/db"
)

// devSignIn signs in a new or existing dev identity with an optional return
// path, the way the sign-in form does, and returns the client and where the
// server sent it.
func devSignIn(h *harness, email, ret string) (*client, string) {
	c := h.anon()
	vals := url.Values{"email": {email}, "name": {email}}
	if ret != "" {
		vals.Set("return", ret)
	}
	res := c.do("POST", "/auth/dev", strings.NewReader(vals.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	res.Body.Close()
	require.Equal(h.t, 302, res.StatusCode)
	return c, res.Header.Get("Location")
}

// memberships lists the organizations an address belongs to, by role.
func memberships(t *testing.T, email string) []string {
	t.Helper()
	var roles []string
	require.NoError(t, db.OwnerForTest(t).Raw(
		`SELECT m.role FROM membership m JOIN "user" u ON u.id = m.user_id WHERE u.email = ? ORDER BY m.id`, email).Scan(&roles).Error)
	return roles
}

func csrfFrom(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(body)
	require.NotNil(t, m, "no CSRF token on the page")
	return m[1]
}

func userCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT count(*) FROM "user"`).Scan(&n).Error)
	return n
}

// instanceAdminClient signs in an owner and makes them the installation's
// administrator, as the local owner account is on a real server.
func instanceAdminClient(t *testing.T, h *harness, email string) *client {
	t.Helper()
	h.signIn(email)
	var id int64
	require.NoError(t, db.OwnerForTest(t).Raw(`SELECT id FROM "user" WHERE email = ?`, email).Scan(&id).Error)
	require.NoError(t, models.SetInstanceAdmin(t.Context(), id))
	return h.signIn(email)
}

// TestClosedSignupsRefuseAStranger: with sign-ups closed, a new identity gets
// nothing, and the sign-in page says why rather than silently starting over.
func TestClosedSignupsRefuseAStranger(t *testing.T) {
	h := newHarness(t)
	h.cfg.SignupsEnabled = false
	before := userCount(t)

	_, loc := devSignIn(h, "stranger@x.test", "")
	assert.Equal(t, "/login?closed=1", loc)
	assert.Equal(t, before, userCount(t), "nothing is created")

	body := readAll(h.anon().do("GET", loc, nil, nil))
	assert.Contains(t, body, "not taking new accounts")
}

// TestAllowlistLetsAnAddressSignUp: the operator allows an address; that
// person signs up while sign-ups are closed and gets a site of their own.
func TestAllowlistLetsAnAddressSignUp(t *testing.T) {
	h := newHarness(t)
	admin := instanceAdminClient(t, h, "admin@x.test")
	h.cfg.SignupsEnabled = false

	res := admin.form("/admin/server/allow", url.Values{"email": {"  Friend@X.test "}, "note": {"beta"}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "allowed=friend%40x.test", "stored lowercased and trimmed")

	_, loc := devSignIn(h, "friend@x.test", "")
	assert.Equal(t, "/", loc, "an allowed address signs straight in")
	assert.Equal(t, []string{"owner"}, memberships(t, "friend@x.test"), "with their own site")

	body := readAll(admin.do("GET", "/admin/server", nil, nil))
	assert.Contains(t, body, "friend@x.test")
	assert.Contains(t, body, "Signed up")

	// Removing the address does not touch the account it created.
	res = admin.form("/admin/server/allow/remove", url.Values{"email": {"friend@x.test"}})
	res.Body.Close()
	_, loc = devSignIn(h, "friend@x.test", "")
	assert.Equal(t, "/", loc, "an existing account keeps signing in")

	// A different address is still refused.
	_, loc = devSignIn(h, "other@x.test", "")
	assert.Equal(t, "/login?closed=1", loc)
}

// TestAllowlistIsForTheOperatorOnly: an ordinary site owner cannot add to the
// installation's allowlist.
func TestAllowlistIsForTheOperatorOnly(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	res := owner.form("/admin/server/allow", url.Values{"email": {"mate@x.test"}})
	res.Body.Close()
	assert.Equal(t, 404, res.StatusCode)
	rows, err := models.ListSignupAllow(t.Context())
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestAllowlistRejectsANonAddress: a typo is reported, not stored.
func TestAllowlistRejectsANonAddress(t *testing.T) {
	h := newHarness(t)
	admin := instanceAdminClient(t, h, "admin@x.test")
	res := admin.form("/admin/server/allow", url.Values{"email": {"not an address"}})
	res.Body.Close()
	assert.Contains(t, res.Header.Get("Location"), "allow_error=")
	rows, err := models.ListSignupAllow(t.Context())
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestInviteLetsSomeoneInWhileSignupsAreClosed: an invitation link is enough
// to get in, but only as a member of the site that invited them. They get no
// site of their own, and the link cannot be used twice.
func TestInviteLetsSomeoneInWhileSignupsAreClosed(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	writePage(t, owner, "/business/plan.md", "# Plan\n\nShared with the team.\n")
	token := inviteLink(t, owner, "guest@x.test", models.RoleEditor)
	h.cfg.SignupsEnabled = false

	guest, loc := devSignIn(h, "guest@x.test", "/invite/"+token)
	assert.Equal(t, "/", loc, "the invite was accepted on the way in")
	assert.Equal(t, []string{models.RoleEditor}, memberships(t, "guest@x.test"), "a member of the inviting site and nothing else")

	res := guest.do("GET", "/business/plan", nil, nil)
	body := readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, "Shared with the team")

	// Somebody else holding the same link is refused, and nothing is created.
	before := userCount(t)
	_, loc = devSignIn(h, "forwarded@x.test", "/invite/"+token)
	assert.Equal(t, "/login?closed=1", loc)
	assert.Equal(t, before, userCount(t))
}

// TestBogusInviteDoesNotOpenSignups: a made-up token is not a way in.
func TestBogusInviteDoesNotOpenSignups(t *testing.T) {
	h := newHarness(t)
	h.cfg.SignupsEnabled = false
	before := userCount(t)
	_, loc := devSignIn(h, "chancer@x.test", "/invite/not-a-real-token")
	assert.Equal(t, "/login?closed=1", loc)
	assert.Equal(t, before, userCount(t))
}

// TestAllowedInviteeGetsBoth: someone who is on the allowlist and was invited
// gets their own site, and goes on to the invite page to accept it there.
func TestAllowedInviteeGetsBoth(t *testing.T) {
	h := newHarness(t)
	admin := instanceAdminClient(t, h, "admin@x.test")
	token := inviteLink(t, admin, "both@x.test", models.RoleViewer)
	res := admin.form("/admin/server/allow", url.Values{"email": {"both@x.test"}})
	res.Body.Close()
	h.cfg.SignupsEnabled = false

	guest, loc := devSignIn(h, "both@x.test", "/invite/"+token)
	assert.Equal(t, "/invite/"+token, loc, "the invite is still theirs to accept")
	assert.Equal(t, []string{"owner"}, memberships(t, "both@x.test"))

	guest.csrf = csrfFrom(t, readAll(guest.do("GET", "/admin", nil, nil)))
	res = guest.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	assert.Equal(t, []string{"owner", models.RoleViewer}, memberships(t, "both@x.test"))
}
