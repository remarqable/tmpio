package models

import (
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
