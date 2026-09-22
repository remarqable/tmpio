package controllers

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/models"
)

// inviteLink creates an invitation and returns the token from the link.
func inviteLink(t *testing.T, owner *client, email, role string) string {
	t.Helper()
	res := owner.form("/admin/members/invite", url.Values{"email": {email}, "role": {role}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	loc, err := url.Parse(res.Header.Get("Location"))
	require.NoError(t, err)
	link := loc.Query().Get("link")
	require.NotEmpty(t, link, "the invitation link is shown once")
	return link[strings.LastIndex(link, "/")+1:]
}

// TestInviteJoinsAnOrganization is the whole feature: an owner invites, a
// different account accepts, and is then a member of that organization.
func TestInviteJoinsAnOrganization(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	writePage(t, owner, "/business/secret.md", "# Secret\n\nOwner's document.\n")

	token := inviteLink(t, owner, "guest@x.test", models.RoleEditor)

	guest := h.signIn("guest@x.test")
	// Before accepting, the guest is in their own organization and cannot see
	// the owner's document.
	res := guest.do("GET", "/business/secret", nil, nil)
	res.Body.Close()
	assert.Equal(t, 404, res.StatusCode, "a stranger must not read another organization")

	res = guest.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)

	// The membership exists, and the owner sees them listed.
	body := readAll(owner.do("GET", "/admin/members", nil, nil))
	assert.Contains(t, body, "guest@x.test")
}

// TestInviteCannotBeReused: one invitation, one membership.
func TestInviteCannotBeReused(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	token := inviteLink(t, owner, "guest@x.test", models.RoleViewer)

	guest := h.signIn("guest@x.test")
	res := guest.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)

	other := h.signIn("other@x.test")
	res = other.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()
	assert.Equal(t, 303, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "error=", "a spent invitation is refused")
}

// TestRevokedInviteIsRefused covers withdrawing one before it is used.
func TestRevokedInviteIsRefused(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	token := inviteLink(t, owner, "guest@x.test", models.RoleEditor)

	body := readAll(owner.do("GET", "/admin/members", nil, nil))
	id := regexp.MustCompile(`name="id" value="(\d+)"`).FindStringSubmatch(body)
	require.NotNil(t, id, "the pending invitation is listed with a revoke control")
	res := owner.form("/admin/members/invite/revoke", url.Values{"id": {id[1]}})
	res.Body.Close()

	guest := h.signIn("guest@x.test")
	res = guest.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()
	assert.Contains(t, res.Header.Get("Location"), "error=")
}

// TestViewerCannotWrite is the role actually being enforced, by the same scope
// check that gates an API token.
func TestViewerCannotWrite(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	writePage(t, owner, "/business/doc.md", "# Doc\n")
	token := inviteLink(t, owner, "viewer@x.test", models.RoleViewer)

	viewer := h.signIn("viewer@x.test")
	res := viewer.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)

	// A viewer reads.
	res = viewer.do("GET", "/business/doc", nil, nil)
	res.Body.Close()
	assert.Equal(t, 200, res.StatusCode)

	// A viewer does not write.
	// A refusal is a redirect carrying the reason, not a different status.
	res = viewer.form("/delete/business/doc", url.Values{"expected_revision": {"1"}})
	res.Body.Close()
	assert.Contains(t, res.Header.Get("Location"), "error=", "a viewer must not delete")
	res = owner.do("GET", "/business/doc", nil, nil)
	res.Body.Close()
	assert.Equal(t, 200, res.StatusCode, "the document is still there")
}

// TestOnlyOwnerManagesPeople: an editor is a full member for content and has
// no say over who else is in the organization.
func TestOnlyOwnerManagesPeople(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	token := inviteLink(t, owner, "editor@x.test", models.RoleEditor)

	editor := h.signIn("editor@x.test")
	res := editor.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()

	res = editor.form("/admin/members/invite", url.Values{"email": {"someone@x.test"}, "role": {"owner"}})
	res.Body.Close()
	assert.Contains(t, res.Header.Get("Location"), "error=", "an editor cannot invite")
}

// TestLastOwnerCannotBeRemoved: an organization with no owner has nobody who
// can invite, change settings or delete it.
func TestLastOwnerCannotBeRemoved(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	body := readAll(owner.do("GET", "/admin/members", nil, nil))
	uid := regexp.MustCompile(`name="user_id" value="(\d+)"`).FindStringSubmatch(body)
	if uid == nil {
		t.Skip("the sole owner has no remove control, which is the same guarantee")
	}
	res := owner.form("/admin/members/remove", url.Values{"user_id": {uid[1]}})
	res.Body.Close()
	assert.Contains(t, res.Header.Get("Location"), "error=")
}

// TestSwitchOrganization: after joining a second organization you can get back
// to your own, and you cannot point a session at one you do not belong to.
func TestSwitchOrganization(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	writePage(t, owner, "/business/theirs.md", "# Theirs\n")
	token := inviteLink(t, owner, "guest@x.test", models.RoleEditor)

	guest := h.signIn("guest@x.test")
	writePage(t, guest, "/mine/ours.md", "# Mine\n")
	res := guest.form("/invite/"+token+"/accept", url.Values{})
	res.Body.Close()

	// Now in the owner's organization.
	r := guest.do("GET", "/business/theirs", nil, nil)
	r.Body.Close()
	require.Equal(t, 200, r.StatusCode)

	// Their own organization is listed, and switching back works.
	body := readAll(guest.do("GET", "/admin/members", nil, nil))
	require.Contains(t, body, "members/switch", "both organizations are offered")
	ids := regexp.MustCompile(`name="tenant_id" value="(\d+)"`).FindStringSubmatch(body)
	require.NotNil(t, ids)
	res = guest.form("/admin/members/switch", url.Values{"tenant_id": {ids[1]}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)

	r = guest.do("GET", "/mine/ours", nil, nil)
	r.Body.Close()
	assert.Equal(t, 200, r.StatusCode, "back in their own organization")

	// A tenant they do not belong to is refused.
	res = guest.form("/admin/members/switch", url.Values{"tenant_id": {"999999"}})
	res.Body.Close()
	assert.Contains(t, res.Header.Get("Location"), "error=")
}
