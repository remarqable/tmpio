package controllers

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/models"
)

func rev64(n int64) string { return strconv.FormatInt(n, 10) }

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// writePage puts a page through the owner session and returns its revision.
func writePage(t *testing.T, c *client, path, content string) int64 {
	t.Helper()
	rev, _ := writePageTag(t, c, path, content)
	return rev
}

// writePageTag also hands back the ETag, which is what If-Match wants.
func writePageTag(t *testing.T, c *client, path, content string) (int64, string) {
	t.Helper()
	res, out := c.api("PUT", "/api/v1/orgs/"+c.org+"/entries?path="+url.QueryEscape(path),
		map[string]string{"content": content},
		map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	require.Equal(t, 201, res.StatusCode, out)
	e, _ := out["entry"].(map[string]any)
	require.NotNil(t, e, out)
	rev, _ := e["revision"].(float64)
	return int64(rev), res.Header.Get("ETag")
}

// pageVisible asks the question the owner actually cares about: is the page
// still on the site? The REST read deliberately still serves a tombstone to
// its owner so a delete can be undone, so it cannot answer this.
func pageVisible(t *testing.T, c *client, path string) bool {
	t.Helper()
	res := c.do("GET", strings.TrimSuffix(path, ".md"), nil, nil)
	res.Body.Close()
	return res.StatusCode == 200
}

// TestDriveListingShowsColumns is the read half of the directory page: the
// listing carries the metadata the table renders, for the owner and for a
// visitor, and only the owner is offered the checkboxes.
func TestDriveListingShowsColumns(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	writePage(t, owner, "/travel/japan.md", "# Japan\n\nFourteen days.\n")
	writePage(t, owner, "/travel/packing.md", "# Packing\n")

	res := owner.do("GET", "/travel/", nil, nil)
	body := readAll(res)
	require.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, `class="tmp-drive"`, "the folder page renders the drive table")
	assert.Contains(t, body, `data-drive-sort="updated"`)
	assert.Contains(t, body, `name="path" value="/travel/japan.md"`, "rows are selectable")
	assert.Contains(t, body, `name="rev[/travel/japan.md]"`, "each row carries the revision on screen")
	assert.Contains(t, body, "Japan")

	// A stranger gets no listing at all: the site is private, so the selection
	// controls are never the thing standing between them and the content.
	stranger := h.signIn("stranger@x.test")
	res = stranger.do("GET", "/o:"+owner.org+"/travel/", nil,
		map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	res.Body.Close()
	assert.Equal(t, 404, res.StatusCode)
}

// TestDriveBulkMove is the whole point of option B: pick several rows, name a
// destination, and they land there together.
func TestDriveBulkMove(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	r1 := writePage(t, owner, "/inbox/japan.md", "# Japan\n")
	r2 := writePage(t, owner, "/inbox/packing.md", "# Packing\n")
	writePage(t, owner, "/travel/index.md", "# Travel\n")

	vals := url.Values{
		"dir":                    {"/inbox"},
		"action":                 {"move"},
		"to":                     {"/travel"},
		"path":                   {"/inbox/japan.md", "/inbox/packing.md"},
		"rev[/inbox/japan.md]":   {rev64(r1)},
		"rev[/inbox/packing.md]": {rev64(r2)},
	}
	res := owner.form("/bulk", vals)
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/inbox/", res.Header.Get("Location"), "a clean run returns to the folder with no error")

	assert.True(t, pageVisible(t, owner, "/travel/japan.md"))
	assert.True(t, pageVisible(t, owner, "/travel/packing.md"))
}

// TestDriveBulkDelete removes several rows at once and reports the ones it
// could not remove instead of failing the whole run.
func TestDriveBulkDelete(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	r1 := writePage(t, owner, "/notes/one.md", "# One\n")
	r2 := writePage(t, owner, "/notes/two.md", "# Two\n")

	vals := url.Values{
		"dir":                {"/notes"},
		"action":             {"delete"},
		"path":               {"/notes/one.md", "/notes/two.md"},
		"rev[/notes/one.md]": {rev64(r1)},
		"rev[/notes/two.md]": {rev64(r2)},
	}
	res := owner.form("/bulk", vals)
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	assert.NotContains(t, res.Header.Get("Location"), "error=")

	assert.False(t, pageVisible(t, owner, "/notes/one.md"))
	assert.False(t, pageVisible(t, owner, "/notes/two.md"))
}

// TestDriveBulkRefusesStaleRevision is why each row carries its revision: a
// page that changed since the listing was drawn is not moved blind, and the
// rest of the selection still goes through.
func TestDriveBulkRefusesStaleRevision(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	stale, staleTag := writePageTag(t, owner, "/inbox/a.md", "# A\n")
	fresh := writePage(t, owner, "/inbox/b.md", "# B\n")
	writePage(t, owner, "/filed/index.md", "# Filed\n")

	// Something writes to a.md after the browser drew the listing.
	res, out := owner.api("PUT", "/api/v1/orgs/"+owner.org+"/entries?path=/inbox/a.md",
		map[string]string{"content": "# A, changed\n"},
		map[string]string{"Idempotency-Key": uuidHex(), "If-Match": staleTag})
	require.Equal(t, 200, res.StatusCode, out)

	vals := url.Values{
		"dir":              {"/inbox"},
		"action":           {"move"},
		"to":               {"/filed"},
		"path":             {"/inbox/a.md", "/inbox/b.md"},
		"rev[/inbox/a.md]": {rev64(stale)},
		"rev[/inbox/b.md]": {rev64(fresh)},
	}
	res = owner.form("/bulk", vals)
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)

	loc := res.Header.Get("Location")
	assert.Contains(t, loc, "error=", "the stale row is reported")
	assert.Contains(t, loc, "a.md", "the message names what did not move")

	assert.True(t, pageVisible(t, owner, "/inbox/a.md"), "the stale page stays put")
	assert.True(t, pageVisible(t, owner, "/filed/b.md"), "the rest of the selection still moved")
}

// TestDriveBulkRequiresOwner keeps the endpoint behind the owner session, the
// same as every other write verb.
func TestDriveBulkRequiresOwner(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	rev := writePage(t, owner, "/inbox/a.md", "# A\n")

	// Anonymous.
	res := h.anon().do("POST", "/bulk",
		strings.NewReader(url.Values{"dir": {"/inbox"}, "action": {"delete"}, "path": {"/inbox/a.md"}}.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	res.Body.Close()
	assert.NotEqual(t, 303, res.StatusCode)
	assert.True(t, pageVisible(t, owner, "/inbox/a.md"))

	// Cross-origin, with a valid session.
	res = owner.do("POST", "/bulk",
		strings.NewReader(url.Values{"csrf_token": {owner.csrf}, "dir": {"/inbox"}, "action": {"delete"}, "path": {"/inbox/a.md"}, "rev[/inbox/a.md]": {rev64(rev)}}.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://evil.example"})
	res.Body.Close()
	assert.Equal(t, 403, res.StatusCode)
	assert.True(t, pageVisible(t, owner, "/inbox/a.md"))

	// An action nobody implements is refused rather than guessed at.
	res = owner.form("/bulk", url.Values{"dir": {"/inbox"}, "action": {"shred"}, "path": {"/inbox/a.md"}, "rev[/inbox/a.md]": {rev64(rev)}})
	res.Body.Close()
	assert.Equal(t, 303, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "error=")
	assert.True(t, pageVisible(t, owner, "/inbox/a.md"))
}

// TestDriveBulkEmptySelectionIsNotAnError covers the submit that a stray click
// can produce: nothing selected means nothing happens.
func TestDriveBulkEmptySelectionIsNotAnError(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@x.test")
	res := owner.form("/bulk", url.Values{"dir": {"/inbox"}, "action": {"delete"}})
	res.Body.Close()
	require.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/inbox/", res.Header.Get("Location"))
}

// TestRollUpGivesFoldersATimestamp checks the column that would otherwise be
// empty: a directory reports when its newest child changed.
func TestRollUpGivesFoldersATimestamp(t *testing.T) {
	child := &NavNode{Kind: models.KindPage, Updated: mustTime("2026-03-01T10:00:00Z")}
	older := &NavNode{Kind: models.KindPage, Updated: mustTime("2026-01-01T10:00:00Z")}
	dir := &NavNode{Kind: models.KindDirectory, Children: []*NavNode{older, child}}
	root := &NavNode{Kind: models.KindDirectory, Children: []*NavNode{dir}}

	rollUp(root)
	assert.Equal(t, child.Updated, dir.Updated, "a folder takes the newest timestamp beneath it")
	assert.Equal(t, child.Updated, root.Updated, "and it climbs all the way up")
}
