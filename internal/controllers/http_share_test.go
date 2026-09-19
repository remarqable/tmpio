package controllers

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func regexpMust(p string) *regexp.Regexp { return regexp.MustCompile(p) }

func TestSecretLinkHTTPContract(t *testing.T) {
	h := newHarness(t)
	a := h.signIn("owner@x.test")
	base := "/api/v1/orgs/" + a.org
	res, out := a.api("PUT", base+"/entries?path=/research/circle.md", map[string]string{"content": "---\ntitle: Circle\n---\n\n# Circle\n\nDraft.\n"}, map[string]string{"Idempotency-Key": uuidHex(), "If-None-Match": "*"})
	require.Equal(t, 201, res.StatusCode, out)

	// Create link (owner session + CSRF only). Token is returned once.
	res, out = a.api("POST", base+"/share-links", map[string]string{"path": "/research/circle.md", "label": "Reviewer A"}, nil)
	require.Equal(t, 201, res.StatusCode, out)
	link := out["url"].(string)
	require.Contains(t, link, h.origin+"/s/")
	path := strings.TrimPrefix(link, h.origin)
	gid := int(out["link"].(map[string]any)["id"].(float64))
	res, out = a.api("GET", base+"/share-links", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.NotContains(t, out, "url", "listing never returns tokens")
	assert.NotContains(t, readAllShort(res), strings.TrimPrefix(path, "/s/"))

	anon := h.anon()
	// Read representations with the disclosure-reducing headers.
	res = anon.do("GET", path, nil, map[string]string{"Accept": "text/html"})
	body := readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "no-referrer", res.Header.Get("Referrer-Policy"))
	assert.Equal(t, "no-store", res.Header.Get("Cache-Control"))
	assert.Contains(t, res.Header.Get("X-Robots-Tag"), "noindex")
	assert.Contains(t, body, "Circle")
	assert.NotContains(t, body, "/o:"+a.org+"/research/circle", "shared HTML exposes no private canonical path")
	assert.NotContains(t, body, "tmp-sidebar", "no site navigation")
	assert.NotContains(t, body, "/search")
	res = anon.do("GET", path+"/raw", nil, nil)
	etag := res.Header.Get("ETag")
	tagFor := func(rev int) string { return strings.Replace(etag, "-r1", "-r"+itoa(rev), 1) }
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "text/markdown; charset=utf-8", res.Header.Get("Content-Type"))
	assert.Contains(t, readAll(res), "Draft.")
	res = anon.do("GET", path+"/json", nil, nil)
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, `"revision":1`)
	assert.NotContains(t, body, "/o:")
	assert.NotContains(t, body, `"id"`)
	res = anon.do("GET", path+"/edit", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	body = readAll(res)
	nonce := regexpFirst(`name="nonce" value="([^"]+)"`, body)
	require.NotEmpty(t, nonce)
	res = anon.do("GET", path+"/instructions", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, readAll(res), "If-Match")

	// Invalid selectors and methods.
	res = anon.do("GET", "/s/"+strings.Repeat("A", 43), nil, nil)
	assert.Equal(t, 404, res.StatusCode)
	res.Body.Close()
	res = anon.do("GET", path+"/assets/1", nil, nil)
	assert.Equal(t, 404, res.StatusCode)
	res.Body.Close()
	res = anon.do("GET", path+"/o:"+a.org+"/index.md", nil, nil)
	assert.Equal(t, 404, res.StatusCode)
	res.Body.Close()
	res = anon.do("DELETE", path, nil, nil)
	assert.Equal(t, 405, res.StatusCode)
	res.Body.Close()
	res = anon.do("POST", path+"/raw", strings.NewReader("x"), nil)
	assert.Equal(t, 405, res.StatusCode)
	res.Body.Close()

	// HTTP write contract.
	newSrc := "---\ntitle: Circle\n---\n\n# Circle\n\nEdited by HTTP.\n"
	put := func(headers map[string]string) (int, string) {
		hd := map[string]string{"Content-Type": "text/markdown"}
		for k, v := range headers {
			hd[k] = v
		}
		res := anon.do("PUT", path+"/raw", strings.NewReader(newSrc), hd)
		return res.StatusCode, readAll(res)
	}
	code, _ := put(map[string]string{"Idempotency-Key": uuidHex()})
	assert.Equal(t, 428, code)
	code, _ = put(map[string]string{"If-Match": etag})
	assert.Equal(t, 428, code, "idempotency key required")
	code, _ = put(map[string]string{"If-Match": `"e999-r1"`, "Idempotency-Key": uuidHex()})
	assert.Equal(t, 412, code, "ETag of another entry is rejected")
	code, _ = put(map[string]string{"If-Match": etag, "Idempotency-Key": uuidHex(), "Origin": "http://evil.example"})
	assert.Equal(t, 403, code)
	k := uuidHex()
	code, body = put(map[string]string{"If-Match": etag, "Idempotency-Key": k, "X-Author-Name": "Anon"})
	assert.Equal(t, 200, code, body)
	assert.Contains(t, body, `"revision":2`)
	code, body = put(map[string]string{"If-Match": etag, "Idempotency-Key": k})
	assert.Equal(t, 200, code, "replay: %s", body)
	assert.Contains(t, body, `"revision":2`)
	code, _ = put(map[string]string{"If-Match": etag, "Idempotency-Key": uuidHex()})
	assert.Equal(t, 412, code)
	res = anon.do("PUT", path+"/raw", strings.NewReader("# x\n\n<b>no</b>\n"), map[string]string{"Content-Type": "text/markdown", "If-Match": tagFor(2), "Idempotency-Key": uuidHex()})
	assert.Equal(t, 422, res.StatusCode)
	res.Body.Close()
	res = anon.do("PUT", path+"/raw", strings.NewReader(strings.Repeat("x", 300*1024)), map[string]string{"Content-Type": "text/markdown", "If-Match": tagFor(2), "Idempotency-Key": uuidHex()})
	assert.Equal(t, 413, res.StatusCode)
	res.Body.Close()

	// Owner sees the anonymous revision and attribution.
	res, out = a.api("GET", base+"/history?path=/research/circle.md", nil, nil)
	items := out["items"].([]any)
	first := items[0].(map[string]any)
	assert.Equal(t, "Anonymous via sharing link", first["actor"])
	assert.Equal(t, "Anon", first["author_name"])
	assert.Equal(t, "Reviewer A", first["share_label"])

	// Browser form save: nonce required; unsaved text preserved on conflict.
	res = anon.do("POST", path+"/edit", strings.NewReader(url.Values{"content": {"# Circle\n\nform\n"}, "expected_revision": {"2"}, "request_id": {uuidHex()}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	assert.Equal(t, 403, res.StatusCode, "missing nonce")
	res.Body.Close()
	res = anon.do("POST", path+"/edit", strings.NewReader(url.Values{"content": {"# Circle\n\nstale form text\n"}, "expected_revision": {"1"}, "request_id": {uuidHex()}, "nonce": {nonce}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	body = readAll(res)
	assert.Equal(t, 412, res.StatusCode)
	assert.Contains(t, body, "stale form text")
	res = anon.do("POST", path+"/edit", strings.NewReader(url.Values{"content": {"# Circle\n\nform ok\n"}, "expected_revision": {"2"}, "request_id": {uuidHex()}, "nonce": {nonce}, "author_name": {"Form User"}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	assert.Equal(t, 303, res.StatusCode)
	assert.Equal(t, path+"/edit?saved=3", res.Header.Get("Location"))
	res.Body.Close()
	// Preview under the link.
	res = anon.do("POST", path+"/preview", strings.NewReader(url.Values{"content": {"# P\n\n![x](/assets/private.png)\n"}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	body = readAll(res)
	assert.Equal(t, 200, res.StatusCode)
	assert.Contains(t, body, "Image unavailable", "unapproved private assets are flagged, never fetched")

	// A signed-in owner visiting the link acts as the link principal.
	res = a.do("PUT", path+"/raw", strings.NewReader("# Circle\n\nowner via link\n"), map[string]string{"Content-Type": "text/markdown", "If-Match": tagFor(3), "Idempotency-Key": uuidHex()})
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()
	res, out = a.api("GET", base+"/history?path=/research/circle.md", nil, nil)
	assert.Equal(t, "Anonymous via sharing link", out["items"].([]any)[0].(map[string]any)["actor"])

	// Move keeps the link working; deleting revokes permanently.
	res, out = a.api("POST", base+"/moves", map[string]string{"from": "/research/circle.md", "to": "/notes/circle.md"}, map[string]string{"Idempotency-Key": uuidHex(), "If-Match": tagFor(4)})
	require.Equal(t, 200, res.StatusCode, out)
	res = anon.do("GET", path+"/raw", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()

	// Revoke: reads, writes, replays and editor saves all stop; owner continues.
	res, _ = a.api("DELETE", base+"/share-links/"+itoa(gid), nil, nil)
	assert.Equal(t, 204, res.StatusCode)
	for _, p := range []string{path, path + "/raw", path + "/json", path + "/edit", path + "/instructions", path + "/assets/1"} {
		res = anon.do("GET", p, nil, nil)
		assert.Equal(t, 404, res.StatusCode, p)
		res.Body.Close()
	}
	code, _ = put(map[string]string{"If-Match": etag, "Idempotency-Key": k})
	assert.Equal(t, 404, code, "cached success cannot be replayed after revocation")
	res = anon.do("POST", path+"/edit", strings.NewReader(url.Values{"content": {"# x\n"}, "expected_revision": {"5"}, "request_id": {uuidHex()}, "nonce": {nonce}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": h.origin})
	assert.Equal(t, 404, res.StatusCode)
	res.Body.Close()
	res = a.do("GET", "/notes/circle.md", nil, nil)
	assert.Equal(t, 200, res.StatusCode)
	res.Body.Close()
}
