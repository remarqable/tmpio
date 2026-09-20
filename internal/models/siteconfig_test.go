package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSiteConfig(t *testing.T) {
	cfg, err := ParseSiteConfig(DefaultSiteConfigYAML)
	require.NoError(t, err)
	assert.Equal(t, "docs", cfg.Theme)
	assert.Equal(t, "dark", cfg.Appearance)
	assert.True(t, cfg.Sidebar.Auto)

	cfg, err = ParseSiteConfig("version: 1\nname: R\ntheme: editorial\nappearance: dark\nnavigation:\n  - title: Research\n    path: /research/\n")
	require.NoError(t, err)
	assert.Equal(t, "editorial", cfg.Theme)
	assert.Len(t, cfg.Navigation, 1)

	bad := map[string]string{
		"unknown key":    "version: 1\nnav: []\n",
		"bad theme":      "version: 1\ntheme: neon\n",
		"bad version":    "version: 2\n",
		"external nav":   "version: 1\nnavigation:\n  - title: X\n    path: https://evil\n",
		"too many nav":   "version: 1\nnavigation:\n" + repeatNav(13),
		"alias":          "version: 1\nname: &a x\ndescription: *a\n",
		"duplicate key":  "version: 1\ntheme: docs\ntheme: docs\n",
		"empty":          "",
		"bad appearance": "version: 1\nappearance: auto\n",
	}
	for name, src := range bad {
		_, err := ParseSiteConfig(src)
		assert.Error(t, err, name)
	}
}

func repeatNav(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += "  - title: T\n    path: /x\n"
	}
	return s
}

// TestWelcomeMarkdownNamesThisInstance: the first page tells you where to point
// your AI, so it has to carry this server's address rather than an example.
func TestWelcomeMarkdownNamesThisInstance(t *testing.T) {
	page := WelcomeMarkdown("https://tmp.example.com")
	if !strings.Contains(page, "https://tmp.example.com/mcp") {
		t.Fatalf("the welcome page should name this instance's MCP address:\n%s", page)
	}
	if strings.Contains(page, "your-instance") {
		t.Fatal("the placeholder address survived into a configured instance")
	}
	// A trailing slash on the origin must not double up.
	if strings.Contains(WelcomeMarkdown("https://tmp.example.com/"), "com//mcp") {
		t.Fatal("a trailing slash produced a doubled path")
	}
	// Without an origin it still has to be a sensible page, not an empty URL.
	if strings.Contains(WelcomeMarkdown(""), "\n/mcp\n") {
		t.Fatal("an unknown origin produced a bare /mcp")
	}
	// It parses as a page with frontmatter the site can use.
	if !strings.HasPrefix(page, "---\ntitle: Welcome\n") {
		t.Fatal("the welcome page lost its frontmatter")
	}
}
