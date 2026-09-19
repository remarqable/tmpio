package controllers

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uuidHex() string { return uuidV4() }

func TestAddressingAndAuthorization(t *testing.T) {
	h := newHarness(t)
	anon := h.anon()

	// Signed-out root is the landing page; other navigation goes to login with a return path.
	res := anon.do("GET", "/", nil, map[string]string{"Accept": "text/html"})
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, readAll(res), "second brain for your AI")
	res = anon.do("GET", "/research/circle", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	assert.Equal(t, 302, res.StatusCode)
	assert.Equal(t, "/login?return=%2Fresearch%2Fcircle", res.Header.Get("Location"))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	res.Body.Close()
	// Noninteractive requests get 401, never login HTML.
	for _, p := range []string{"/index.md", "/research/circle.json", "/assets/x.png", "/tmp.yaml", "/search.json?q=x"} {
		res = anon.do("GET", p, nil, nil)
		assert.Equal(t, 401, res.StatusCode, p)
		assert.Contains(t, res.Header.Get("WWW-Authenticate"), "Bearer")
		res.Body.Close()
	}
	// Unknown or malformed explicit org identifiers never fall back.
	for _, p := range []string{"/o:ZZZZZZZZ/index.md", "/o:short/index.md", "/O:%3A/x", "/o%3AZZZZZZZZ/index.md"} {
		res = anon.do("GET", p, nil, nil)
		assert.Equal(t, 404, res.StatusCode, p)
		res.Body.Close()
	}
	// Reserved routes.
	for _, p := range []string{"/sitemap.xml", "/llms.txt"} {
		res = anon.do("GET", p, nil, nil)
		assert.Equal(t, 404, res.StatusCode, p)
		res.Body.Close()
	}
	res = anon.do("GET", "/robots.txt", nil, nil)
	body := readAll(res)
	assert.Contains(t, body, "Disallow: /s/")
	assert.NotContains(t, body, "/s/A")

	a := h.signIn("a@x.test")
	b := h.signIn("b@x.test")
	require.NotEqual(t, a.org, b.org)

	// Each user creates a page at the same shortcut; each resolves to their own org.
	for _, c := range []*client{a, b} {
		res := c.form("/new/research/", url.Values{"kind": {"page"}, "name": {"circle"}, "content": {"# Circle of " + c.org + "\n"}, "request_id": {uuidHex()}})
		assert.Equal(t, 303, res.StatusCode, readAllShort(res))
	}
	for _, c := range []*client{a, b} {
		res := c.do("GET", "/research/circle.md", nil, nil)
		assert.Equal(t, 200, res.StatusCode)
		assert.Contains(t, readAll(res), c.org)
		res = c.do("GET", "/o:"+c.org+"/research/circle.md", nil, nil)
		assert.Equal(t, 200, res.StatusCode)
		assert.Contains(t, readAll(res), c.org)
		// HTML in both forms, same content, and the shortcut is not redirected.
		res = c.do("GET", "/research/circle", nil, map[string]string{"Accept": "text/html"})
		assert.Equal(t, 200, res.StatusCode)
		assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		page := readAll(res)
		assert.Contains(t, page, "Circle of "+c.org)
		assert.Contains(t, page, `href="/research/circle.md"`, "personal view keeps personal links")
		res = c.do("GET", "/o:"+strings.ToLower(c.org)+"/research/circle", nil, map[string]string{"Accept": "text/html"})
		assert.Equal(t, 200, res.StatusCode, "case-insensitive org lookup")
		page = readAll(res)
		assert.Contains(t, page, `href="/o:`+c.org+`/research/circle.md"`, "explicit view keeps qualified links")
	}
	// A cannot read B's explicit URL: 404, not 403.
	res = a.do("GET", "/o:"+b.org+"/research/circle.md", nil, nil)
	assert.Equal(t, 404, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/o:"+b.org+"/research/circle", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	assert.Equal(t, 404, res.StatusCode)
	res.Body.Close()

	// A present but invalid bearer never falls back to the cookie.
	res = a.do("GET", "/research/circle.md", nil, map[string]string{"Authorization": "Bearer tmpk_bogus"})
	assert.Equal(t, 401, res.StatusCode)
	res.Body.Close()
	// A Basic header (from a proxy gate) is ignored; the cookie session still applies.
	res = a.do("GET", "/research/circle.md", nil, map[string]string{"Authorization": "Basic YXNpbTpwdw=="})
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()

	// Directory redirects preserve address form; JSON excludes internal ids.
	res = a.do("GET", "/research", nil, nil)
	assert.Equal(t, 307, res.StatusCode)
	assert.Equal(t, "/research/", res.Header.Get("Location"))
	res.Body.Close()
	res = a.do("GET", "/o:"+a.org+"/research", nil, nil)
	assert.Equal(t, 307, res.StatusCode)
	assert.Equal(t, "/o:"+a.org+"/research/", res.Header.Get("Location"))
	res.Body.Close()
	res = a.do("GET", "/research/circle.json", nil, nil)
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.NotContains(t, body, `"id"`)
	assert.Contains(t, body, `"org":"`+a.org+`"`)
	assert.Contains(t, body, h.origin+"/research/circle")
	res = a.do("GET", "/research/index.json", nil, nil)
	assert.Equal(t, 200, res.StatusCode, "directory without index.md returns a listing")
	assert.Contains(t, readAll(res), `"entries"`)

	// Content URLs are read-only.
	res = a.do("POST", "/research/circle.md", nil, nil)
	assert.Equal(t, 405, res.StatusCode)
	res.Body.Close()

	// Search in both forms.
	res = a.do("GET", "/search?q=circle", nil, map[string]string{"Accept": "text/html"})
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/o:"+a.org+"/search.json?q=circle", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, readAll(res), "/o:"+a.org+"/research/circle")

	// Login return-path safety.
	res = anon.do("GET", "/login?return=//evil.example/x", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.NotContains(t, readAll(res), "evil.example")
	c := h.anon()
	res = c.do("POST", "/auth/dev", strings.NewReader(url.Values{"email": {"c@x.test"}, "return": {"https://evil.example/"}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	assert.Equal(t, 302, res.StatusCode)
	assert.Equal(t, "/", res.Header.Get("Location"))
	res.Body.Close()
	c = h.anon()
	res = c.do("POST", "/auth/dev", strings.NewReader(url.Values{"email": {"c@x.test"}, "return": {"/research/circle"}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	assert.Equal(t, "/research/circle", res.Header.Get("Location"))
	res.Body.Close()
	// Dev login refuses cross-origin posts.
	res = h.anon().do("POST", "/auth/dev", strings.NewReader("email=d@x.test"), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "http://evil.example"})
	assert.Equal(t, 403, res.StatusCode)
	res.Body.Close()
}

func readAllShort(res *http.Response) string {
	s := readAll(res)
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func TestCSRFAndOwnerOperations(t *testing.T) {
	h := newHarness(t)
	a := h.signIn("csrf@x.test")
	// Missing token.
	res := a.do("POST", "/new/", strings.NewReader("kind=page&name=x&content=%23+x"), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	assert.Equal(t, 403, res.StatusCode)
	res.Body.Close()
	// Wrong origin.
	res = a.do("POST", "/new/", strings.NewReader("csrf_token="+a.csrf+"&kind=page&name=x&content=%23+x"), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "http://evil.example"})
	assert.Equal(t, 403, res.StatusCode)
	res.Body.Close()
	// Create at the folder's verb URL, then land on the page.
	res = a.form("/new/", url.Values{"kind": {"page"}, "name": {"x"}, "content": {"# x\n"}, "request_id": {uuidHex()}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/x?saved=1", res.Header.Get("Location"))
	res.Body.Close()
	res = a.do("GET", "/x", nil, map[string]string{"Accept": "text/html"})
	body := readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, `href="/edit/x"`, "owner actions are on the page")
	assert.Contains(t, body, `href="/new/"`, "owner tools in the tree")
	assert.Contains(t, body, `href="/admin"`)
	assert.Contains(t, body, `id="tmp-quickadd"`, "quick add dialog is in the owner shell")
	// Quick add: the placement suggestion pre-fills the create form; nothing is written yet.
	res = a.form("/organize", url.Values{"content": {"Weekly numbers for the x project"}, "filename": {"weekly numbers"}})
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, `<code>/inbox/weekly-numbers.md</code>`, "suggested location shown")
	assert.Contains(t, body, `value="weekly-numbers"`, "name pre-filled")
	assert.Contains(t, body, `action="/new/inbox/"`, "form posts to the suggested folder")
	res = a.do("GET", "/inbox/weekly-numbers", nil, map[string]string{"Accept": "text/html"})
	assert.Equal(t, 404, res.StatusCode, "quick add writes nothing until the form is submitted")
	res.Body.Close()
	res = a.form("/organize", url.Values{"content": {"   "}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "/new/?error=")
	res.Body.Close()
	// Edit with a stale revision keeps the text.
	res = a.form("/edit/x", url.Values{"content": {"# my unsaved text"}, "expected_revision": {"5"}, "request_id": {uuidHex()}})
	body = readAll(res)
	assert.Equal(t, 412, res.StatusCode)
	assert.Contains(t, body, "my unsaved text")
	assert.Contains(t, body, "changed while you were editing")
	// Invalid content is reported with line numbers and the text is preserved.
	res = a.form("/edit/x", url.Values{"content": {"# x\n\n<b>raw</b>\n"}, "expected_revision": {"1"}, "request_id": {uuidHex()}})
	body = readAll(res)
	assert.Equal(t, 422, res.StatusCode)
	assert.Contains(t, body, "Line 3")
	assert.Contains(t, body, "&lt;b&gt;raw&lt;/b&gt;")
	// Preview renders without saving.
	res = a.do("POST", "/preview", strings.NewReader(url.Values{"content": {"# T\n\n:::callout type=\"tip\"\nhi\n:::\n"}, "path": {"/x.md"}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin, "X-CSRF-Token": a.csrf})
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, "tmp-callout-tip")
	res = a.do("GET", "/x.md", nil, nil)
	assert.Equal(t, "# x\n", readAll(res))
	// History, compare and restore at verb URLs.
	res = a.form("/edit/x", url.Values{"content": {"# x\n\nv2\n"}, "expected_revision": {"1"}, "request_id": {uuidHex()}})
	assert.Equal(t, 303, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/history/x?rev=1", nil, nil)
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, "diff-add")
	res = a.form("/history/x", url.Values{"revision": {"1"}, "expected_revision": {"2"}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/history/x?restored=3", res.Header.Get("Location"))
	res.Body.Close()
	// Move, then the old address redirects and the new page renders.
	res = a.form("/move/x", url.Values{"to": {"/notes/y.md"}, "expected_revision": {"3"}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/notes/y", res.Header.Get("Location"))
	res.Body.Close()
	res = a.do("GET", "/edit/x", nil, nil)
	assert.Equal(t, 307, res.StatusCode)
	assert.Equal(t, "/edit/notes/y", res.Header.Get("Location"))
	res.Body.Close()
	// Share links from the page's verb URL; the token is shown once.
	res = a.form("/share/notes/y", url.Values{"action": {"create"}, "label": {"R"}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Contains(t, res.Header.Get("Location"), "/share/notes/y?new=")
	res.Body.Close()
	// Delete goes to the trash; restore from there.
	res = a.form("/delete/notes/y", url.Values{"expected_revision": {"4"}})
	assert.Equal(t, 303, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/trash", nil, nil)
	assert.Contains(t, readAll(res), "/notes/y.md")
	res = a.form("/trash", url.Values{"path": {"/notes/y.md"}, "revision": {"5"}})
	assert.Equal(t, 303, res.StatusCode)
	res.Body.Close()
	// Settings: invalid config leaves the old one active; valid config applies.
	res = a.form("/edit/tmp.yaml", url.Values{"content": {"version: 1\ntheme: neon\n"}, "expected_revision": {"1"}, "request_id": {uuidHex()}})
	assert.Equal(t, 422, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/", nil, map[string]string{"Accept": "text/html"})
	assert.Contains(t, readAll(res), `data-theme="docs"`)
	res = a.form("/edit/tmp.yaml", url.Values{"content": {"version: 1\ntheme: editorial\nappearance: dark\n"}, "expected_revision": {"1"}, "request_id": {uuidHex()}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/admin/settings?saved=1", res.Header.Get("Location"))
	res.Body.Close()
	res = a.do("GET", "/", nil, map[string]string{"Accept": "text/html"})
	page := readAll(res)
	assert.Contains(t, page, `data-theme="editorial"`)
	assert.Contains(t, page, `data-appearance="dark"`)
	// Admin: org naming changes the display name, not the code; legacy /app redirects.
	res = a.form("/admin/settings/org", url.Values{"name": {"Acme Research"}})
	assert.Equal(t, 303, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/admin", nil, nil)
	body = readAll(res)
	assert.Contains(t, body, "Acme Research")
	assert.Contains(t, body, "o:"+a.org)
	res = a.do("GET", "/app/edit?path=/notes/y.md", nil, nil)
	assert.Equal(t, 301, res.StatusCode)
	assert.Equal(t, "/edit/notes/y", res.Header.Get("Location"))
	res.Body.Close()
	// Explicit-org verb URLs redirect to the personal form.
	res = a.do("GET", "/o:"+a.org+"/edit/notes/y", nil, nil)
	assert.Equal(t, 307, res.StatusCode)
	assert.Equal(t, "/edit/notes/y", res.Header.Get("Location"))
	res.Body.Close()
	// Text files: created from the same form, viewer for browsers, raw for agents, CSV as a table.
	res = a.form("/new/data/", url.Values{"kind": {"page"}, "name": {"report.csv"}, "content": {"name,score\nCircle,9\nDiscord,7\n"}, "request_id": {uuidHex()}})
	assert.Equal(t, 303, res.StatusCode)
	assert.Equal(t, "/data/report.csv?saved=1", res.Header.Get("Location"))
	res.Body.Close()
	res = a.do("GET", "/data/report.csv", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, "<th>score</th>")
	assert.Contains(t, body, "<td>Discord</td>")
	assert.Contains(t, body, `href="/edit/data/report.csv"`)
	res = a.do("GET", "/data/report.csv", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "text/csv; charset=utf-8", res.Header.Get("Content-Type"))
	assert.Equal(t, "name,score\nCircle,9\nDiscord,7\n", readAll(res))
	res = a.do("GET", "/data/report.csv?raw=1", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	assert.Contains(t, res.Header.Get("Content-Disposition"), "attachment")
	res.Body.Close()
	res = a.do("GET", "/edit/data/report.csv", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.NotContains(t, readAll(res), `id="preview"`, "files have no Markdown preview")
	res = a.form("/edit/data/report.csv", url.Values{"content": {"name\nonly\n"}, "expected_revision": {"1"}, "request_id": {uuidHex()}})
	assert.Equal(t, 303, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/", nil, map[string]string{"Accept": "text/html"})
	assert.Contains(t, readAll(res), " report.csv</a>", "files appear in the tree")
	// Logout ends the session.
	res = a.form("/logout", url.Values{})
	assert.Equal(t, 302, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/admin", nil, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	assert.Equal(t, 302, res.StatusCode)
	res.Body.Close()
}

func TestRESTContract(t *testing.T) {
	h := newHarness(t)
	a := h.signIn("rest@x.test")
	tok := a.apiToken("content:read", "content:write", "content:delete")
	require.True(t, strings.HasPrefix(tok, "tmpk_"))
	api := h.anon()
	api.tok = tok
	api.org = a.org
	base := "/api/v1/orgs/" + a.org

	// Unauthenticated and foreign org.
	res, _ := h.anon().api("GET", base+"/tree", nil, nil)
	assert.Equal(t, 401, res.StatusCode)

	// Filing suggestions: read scope, nothing written, validation on empty content.
	res, pj := api.api("POST", base+"/organize", map[string]any{"content": "# Plan\n\nNotes.", "filename": "plan"}, nil)
	assert.Equal(t, 200, res.StatusCode, pj)
	assert.Equal(t, "/inbox/plan.md", pj["placement"].(map[string]any)["path"])
	assert.Equal(t, "/inbox/plan.md", pj["write_with"].(map[string]any)["path"])
	res, pj = api.api("POST", base+"/organize", map[string]any{"content": ""}, nil)
	assert.Equal(t, 422, res.StatusCode, pj)
	res, _ = api.api("GET", base+"/entries?path=/inbox/plan.md", nil, nil)
	assert.Equal(t, 404, res.StatusCode, "organize writes nothing")
	res, _ = api.api("GET", "/api/v1/orgs/ZZZZZZZZ/tree", nil, nil)
	assert.Equal(t, 404, res.StatusCode)

	// Create requires If-None-Match and Idempotency-Key.
	res, out := api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Circle\n"}, nil)
	assert.Equal(t, 428, res.StatusCode, out)
	res, out = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Circle\n"}, map[string]string{"Idempotency-Key": "not-a-uuid", "If-None-Match": "*"})
	assert.Equal(t, 422, res.StatusCode, out)
	res, out = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Circle\n"}, map[string]string{"Idempotency-Key": uuidHex()})
	assert.Equal(t, 428, res.StatusCode, out)
	k := uuidHex()
	res, out = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Circle\n", "summary": "first"}, map[string]string{"Idempotency-Key": k, "If-None-Match": "*"})
	assert.Equal(t, 201, res.StatusCode, out)
	etag := res.Header.Get("ETag")
	assert.NotEmpty(t, etag)
	urls := out["urls"].(map[string]any)
	assert.Equal(t, h.origin+"/o:"+a.org+"/research/circle", urls["html"])
	// Same key replays; different payload conflicts.
	res, _ = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Circle\n", "summary": "first"}, map[string]string{"Idempotency-Key": k, "If-None-Match": "*"})
	assert.Equal(t, 201, res.StatusCode)
	res, out = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Other\n"}, map[string]string{"Idempotency-Key": k, "If-None-Match": "*"})
	assert.Equal(t, 409, res.StatusCode, out)
	// Stale and missing preconditions.
	res, out = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# X\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": strings.Replace(etag, "-r1", "-r9", 1)})
	assert.Equal(t, 412, res.StatusCode)
	assert.EqualValues(t, 1, out["error"].(map[string]any)["current_revision"])
	res, _ = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# X\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": `"ezzz-r1"`})
	assert.Equal(t, 412, res.StatusCode, "ETag for another entry")
	res, _ = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# X\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	assert.Equal(t, 412, res.StatusCode, "create on existing")
	// Replace with the ETag.
	res, out = api.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "# Circle\n\nv2\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": etag})
	assert.Equal(t, 200, res.StatusCode, out)
	etag2 := res.Header.Get("ETag")
	// Validation errors are 422 with field errors; bad path 400; oversize 413.
	res, out = api.api("PUT", base+"/entries?path=/bad.md", map[string]string{"content": "# x\n\n<script>1</script>\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	assert.Equal(t, 422, res.StatusCode)
	assert.NotEmpty(t, out["error"].(map[string]any)["field_errors"])
	res, _ = api.api("PUT", base+"/entries?path=/Bad.md", map[string]string{"content": "# x\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	assert.Equal(t, 400, res.StatusCode)
	res, _ = api.api("PUT", base+"/entries?path=/big.md", map[string]string{"content": strings.Repeat("x", 300*1024)}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	assert.Equal(t, 413, res.StatusCode)

	// Reads, tree, search, history.
	res, out = api.api("GET", base+"/entries?path=/research/circle.md", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, etag2, res.Header.Get("ETag"))
	assert.Contains(t, out["content"], "v2")
	res, out = api.api("GET", base+"/tree?path=/", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.NotEmpty(t, out["entries"])
	res, out = api.api("GET", base+"/search?q=circle", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.NotEmpty(t, out["hits"])
	res, out = api.api("GET", base+"/history?path=/research/circle.md", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Len(t, out["items"], 2)

	// Directories, moves, restores, delete.
	res, _ = api.api("POST", base+"/directories", map[string]string{"path": "/notes"}, map[string]string{"Idempotency-Key": uuidHex()})
	assert.Equal(t, 201, res.StatusCode)
	res, out = api.api("POST", base+"/moves", map[string]string{"from": "/research/circle.md", "to": "/notes/circle.md"}, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": etag2})
	assert.Equal(t, 200, res.StatusCode, out)
	assert.Equal(t, []any{"/research/circle.md"}, out["aliases"])
	etag3 := res.Header.Get("ETag")
	res, out = api.api("POST", base+"/restores", map[string]any{"path": "/notes/circle.md", "revision": 1}, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": etag3})
	assert.Equal(t, 200, res.StatusCode, out)
	etag4 := res.Header.Get("ETag")
	res, _ = api.api("DELETE", base+"/entries?path=/notes/circle.md", nil, map[string]string{"Idempotency-Key": uuidHex()})
	assert.Equal(t, 428, res.StatusCode)
	res, out = api.api("DELETE", base+"/entries?path=/notes/circle.md", nil, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": etag4})
	assert.Equal(t, 200, res.StatusCode, out)
	assert.Equal(t, true, out["entry"].(map[string]any)["deleted"])

	// Scope enforcement: a read-only token cannot write.
	ro := h.anon()
	ro.tok = a.apiToken("content:read")
	res, _ = ro.api("PUT", base+"/entries?path=/ro.md", map[string]string{"content": "# x\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	assert.Equal(t, 403, res.StatusCode)
	res, _ = ro.api("GET", base+"/tree", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	// API tokens read content routes in both forms, but never manage sharing or export.
	res = api.do("GET", "/index.md", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()
	res = api.do("GET", "/o:"+a.org+"/index.md", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()
	res, _ = api.api("POST", base+"/share-links", map[string]string{"path": "/index.md"}, nil)
	assert.Equal(t, 403, res.StatusCode)
	res, _ = api.api("GET", base+"/export", nil, nil)
	assert.Equal(t, 403, res.StatusCode)
	// Owner session export works.
	res = a.do("GET", "/admin/export.zip", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "application/zip", res.Header.Get("Content-Type"))
	res.Body.Close()
	// Revoked token stops immediately.
	res = a.do("GET", "/admin/connections", nil, nil)
	body := readAll(res)
	for _, m := range regexpMust(`name="id" value="(\d+)"`).FindAllStringSubmatch(body, -1) {
		res = a.form("/admin/tokens/revoke", url.Values{"id": {m[1]}})
		res.Body.Close()
	}
	res, _ = api.api("GET", base+"/tree", nil, nil)
	assert.Equal(t, 401, res.StatusCode)
}

func regexpFirst(pattern, s string) string {
	m := regexpMust(pattern).FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func TestDynamicClientRegistration(t *testing.T) {
	h := newHarness(t)
	anon := h.anon()
	// Metadata advertises registration.
	res, out := anon.api("GET", "/.well-known/oauth-authorization-server", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, h.origin+"/oauth/register", out["registration_endpoint"])
	// Bad redirect schemes are refused.
	res, _ = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"http://evil.example/cb"}, "client_name": "x"}, nil)
	assert.Equal(t, 400, res.StatusCode)
	// Confidential web clients (Claude, ChatGPT) get a secret, shown once.
	res, out = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"https://claude.ai/api/mcp/auth_callback"}, "token_endpoint_auth_method": "client_secret_post", "client_name": "Claude", "application_type": "web"}, nil)
	require.Equal(t, 201, res.StatusCode, out)
	assert.True(t, strings.HasPrefix(out["client_secret"].(string), "tmps_"))
	assert.Equal(t, "client_secret_post", out["token_endpoint_auth_method"])
	// Web clients may not use loopback http or custom schemes; native clients may.
	res, _ = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"http://localhost:1/cb"}, "application_type": "web"}, nil)
	assert.Equal(t, 400, res.StatusCode)
	res, out = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"claude://claude.ai/mcp-auth-callback/sdk"}, "application_type": "native", "token_endpoint_auth_method": "none"}, nil)
	assert.Equal(t, 201, res.StatusCode, out)
	res, _ = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"javascript://x"}, "application_type": "native"}, nil)
	assert.Equal(t, 400, res.StatusCode)
	res, _ = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"https://ok.example/cb"}, "token_endpoint_auth_method": "private_key_jwt"}, nil)
	assert.Equal(t, 400, res.StatusCode, "unsupported auth methods are refused")
	// Loopback http and https are accepted; a public client id is issued.
	res, out = anon.api("POST", "/oauth/register", map[string]any{"redirect_uris": []string{"http://localhost:53421/callback", "https://claude.ai/api/mcp/auth_callback"}, "client_name": "Claude Code", "token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}}, nil)
	require.Equal(t, 201, res.StatusCode, out)
	cid := out["client_id"].(string)
	assert.True(t, strings.HasPrefix(cid, "dyn_"))
	assert.Equal(t, "none", out["token_endpoint_auth_method"])
	assert.Nil(t, out["client_secret"])

	// The registered client completes the authorization-code + PKCE flow.
	a := h.signIn("dcr@x.test")
	res = a.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&redirect_uri=http%3A%2F%2Flocalhost%3A53421%2Fcallback&state=s&code_challenge="+strings.Repeat("a", 43)+"&code_challenge_method=S256", nil, nil)
	body := readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, "Claude Code")
	assert.Contains(t, body, "registered itself")
	assert.Contains(t, res.Header.Get("Content-Security-Policy"), "form-action 'self' http://localhost:53421", "the consent form may redirect to the client")
	res = a.form("/oauth/authorize", url.Values{"decision": {"allow"}, "response_type": {"code"}, "client_id": {cid}, "redirect_uri": {"http://localhost:53421/callback"}, "state": {"s"}, "code_challenge": {strings.Repeat("a", 43)}, "code_challenge_method": {"S256"}})
	assert.Equal(t, 302, res.StatusCode)
	loc := res.Header.Get("Location")
	res.Body.Close()
	assert.True(t, strings.HasPrefix(loc, "http://localhost:53421/callback?code="), loc)
	// Unregistered redirect for that client is refused without redirecting.
	res = a.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&redirect_uri=http%3A%2F%2Flocalhost%3A1%2Fother&code_challenge="+strings.Repeat("a", 43)+"&code_challenge_method=S256", nil, nil)
	assert.Equal(t, 400, res.StatusCode)
	res.Body.Close()
}
