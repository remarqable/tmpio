package controllers

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPublicSiteNeverShadowsTheApplication is the property that makes serving
// an operator's files from the application's origin safe to do at all: the
// platform's own paths, and every signed-in visitor, are untouchable.
func TestPublicSiteNeverShadowsTheApplication(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.deps.Cfg.PublicSiteDir = dir

	write := func(name, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write("index.html", "<h1>MARKETING HOME</h1>")
	write("pricing.html", "<h1>MARKETING PRICING</h1>")
	write("about/index.html", "<h1>MARKETING ABOUT</h1>")
	write("robots.txt", "User-agent: *\nDisallow: /admin\n")
	write("login.html", "<h1>FAKE SIGN IN</h1>")
	write("s.html", "<h1>FAKE SHARE</h1>")

	anon := h.anon()
	body := func(path string) string {
		res := anon.do("GET", path, nil, nil)
		return readAll(res)
	}

	// A stranger gets the operator's pages.
	require.Contains(t, body("/"), "MARKETING HOME")
	require.Contains(t, body("/pricing"), "MARKETING PRICING")
	require.Contains(t, body("/about"), "MARKETING ABOUT", "a directory index resolves")
	require.Contains(t, body("/robots.txt"), "Disallow: /admin", "a front page owns robots.txt")

	// Platform paths are never shadowed, however the files are named.
	require.NotContains(t, body("/login"), "FAKE SIGN IN")
	res := anon.do("GET", "/s/deadbeef", nil, nil)
	require.NotContains(t, readAll(res), "FAKE SHARE")
	res = anon.do("GET", "/healthz", nil, nil)
	require.Equal(t, 200, res.StatusCode)
	res.Body.Close()

	// No escaping the directory.
	res = anon.do("GET", "/../../etc/passwd", nil, nil)
	require.NotContains(t, readAll(res), "root:")

	// A signed-in owner sees their own site at the same address.
	ownerClient := h.signIn("owner@x.test")
	require.NotContains(t, readAll(ownerClient.do("GET", "/", nil, nil)), "MARKETING HOME")

	// Writes are never served from disk, whatever exists there.
	status, _ := postForm(anon, "/pricing", url.Values{}, h.origin)
	require.NotEqual(t, 200, status)
}

func TestPublicSiteHeaders(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.deps.Cfg.PublicSiteDir = dir
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hi</h1>"), 0o644))

	anon := h.anon()
	res := anon.do("GET", "/", nil, nil)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, res.Header.Get("Content-Type"), "text/html")
	require.Equal(t, "nosniff", res.Header.Get("X-Content-Type-Options"))
	require.Contains(t, res.Header.Get("Cache-Control"), "public", "static files are cacheable")

	// The application's strict policy applies until the operator relaxes it.
	appCSP := res.Header.Get("Content-Security-Policy")
	require.Contains(t, appCSP, "script-src 'self'")

	h.deps.Cfg.PublicSiteCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' https://plausible.io"
	res = h.anon().do("GET", "/", nil, nil)
	defer res.Body.Close()
	require.Contains(t, res.Header.Get("Content-Security-Policy"), "plausible.io")
}

// With no directory configured the built-in page answers, which is what every
// self-hosted instance sees until its operator publishes something.
func TestPublicSiteAbsentFallsBackToTheApp(t *testing.T) {
	h := newHarness(t)
	h.deps.Cfg.PublicSiteDir = ""
	res := h.anon().do("GET", "/", nil, nil)
	require.Equal(t, 200, res.StatusCode)
	require.Contains(t, readAll(res), "tmp")
}
