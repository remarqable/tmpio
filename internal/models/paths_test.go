package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/remarqable/tmpio/internal/platform/errors"
)

func TestValidatePath(t *testing.T) {
	cases := []struct {
		in       string
		kind     string
		rendered string
		parent   string
		err      bool
	}{
		{"/", KindDirectory, "/", "", false},
		{"/research/circle.md", KindPage, "/research/circle", "/research", false},
		{"/research/index.md", KindPage, "/research/index", "/research", false},
		{"/index.md", KindPage, "/index", "/", false},
		{"/tmp.yaml", KindConfig, "/tmp.yaml", "/", false},
		{"/research", KindDirectory, "/research", "/", false},
		{"/research/", KindDirectory, "/research", "/", false},
		{"/assets/diagram.png", KindAsset, "/assets/diagram.png", "/assets", false},
		{"/a_b-c/d.webp", KindAsset, "/a_b-c/d.webp", "/a_b-c", false},
		{"/abcd1234/page.md", KindPage, "/abcd1234/page", "/abcd1234", false}, // eight-char names stay valid
		{"/data/report.csv", KindFile, "/data/report.csv", "/data", false},
		{"/data/config.v2.json", KindFile, "/data/config.v2.json", "/data", false},
		{"/scripts/build.sh", KindFile, "/scripts/build.sh", "/scripts", false},
		{"/settings.yaml", KindFile, "/settings.yaml", "/", false},
		{"/docs/spec.pdf", KindAsset, "/docs/spec.pdf", "/docs", false},
		{"", "", "", "", true},
		{"research/circle.md", "", "", "", true},
		{"/Research/Circle.md", "", "", "", true},
		{"/research//circle.md", "", "", "", true},
		{"/../etc/passwd", "", "", "", true},
		{"/research/./x.md", "", "", "", true},
		{"/index.html", "", "", "", true},
		{"/data/.hidden.csv", "", "", "", true},
		{"/s/x.md", "", "", "", true},
		{"/app.md", "", "", "", true},
		{"/o:abcdefgh/x.md", "", "", "", true},
		{"/O:ABCDEFGH/x.md", "", "", "", true},
		{"/search.md", "", "", "", true},
		{"/bad name.md", "", "", "", true},
		{"/bad\\name.md", "", "", "", true},
		{"/x\x00y.md", "", "", "", true},
		{"/a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p/q.md", "", "", "", true},
	}
	for _, c := range cases {
		info, err := ValidatePath(c.in)
		if c.err {
			assert.Error(t, err, c.in)
			continue
		}
		require.NoError(t, err, c.in)
		assert.Equal(t, c.kind, info.Kind, c.in)
		assert.Equal(t, c.rendered, info.Rendered, c.in)
		assert.Equal(t, c.parent, info.Parent, c.in)
	}
}

func TestValidatePathSuggestsLowercase(t *testing.T) {
	_, err := ValidatePath("/Research/Circle.md")
	ae := errors.As(err)
	assert.Equal(t, errors.CodeInvalidPath, ae.Code)
	assert.Equal(t, "/research/circle.md", ae.SuggestedPath)
}

func TestRequestPath(t *testing.T) {
	ok := []string{"/", "/research/circle", "/o:ABCDEFGH/x", "/a%20b", "/research/"}
	for _, p := range ok {
		_, err := RequestPath(p)
		assert.NoError(t, err, p)
	}
	bad := []string{"/a%2Fb", "/a%2fb", "/a%5Cb", "/a%00b", "/%2e%2e/x", "/a%252Fb", "/../x", "/a//b", "/a\\b"}
	for _, p := range bad {
		_, err := RequestPath(p)
		assert.Error(t, err, p)
	}
}

func TestSplitOrgPrefix(t *testing.T) {
	code, rest, ok := SplitOrgPrefix("/o:xs67df65/research/circle")
	assert.True(t, ok)
	assert.Equal(t, "XS67DF65", code)
	assert.Equal(t, "/research/circle", rest)

	code, rest, ok = SplitOrgPrefix("/O:XS67DF65")
	assert.True(t, ok)
	assert.Equal(t, "XS67DF65", code)
	assert.Equal(t, "/", rest)

	code, _, ok = SplitOrgPrefix("/o:short/x")
	assert.True(t, ok, "prefix recognized")
	assert.Equal(t, "", code, "invalid code never falls back")

	_, rest, ok = SplitOrgPrefix("/xs67df65/research")
	assert.False(t, ok, "an eight-character directory is not an org prefix")
	assert.Equal(t, "/xs67df65/research", rest)

	_, _, ok = SplitOrgPrefix("/o:")
	assert.True(t, ok)
}

func TestRenderedToCandidates(t *testing.T) {
	p, d, isDir := RenderedToCandidates("/")
	assert.Equal(t, "/index.md", p)
	assert.Equal(t, "/", d)
	assert.True(t, isDir)
	p, d, isDir = RenderedToCandidates("/research/")
	assert.Equal(t, "/research/index.md", p)
	assert.Equal(t, "/research", d)
	assert.True(t, isDir)
	p, d, isDir = RenderedToCandidates("/research/circle")
	assert.Equal(t, "/research/circle.md", p)
	assert.Equal(t, "/research/circle", d)
	assert.False(t, isDir)
}

func TestAncestors(t *testing.T) {
	assert.Nil(t, Ancestors("/"))
	assert.Equal(t, []string{"/"}, Ancestors("/a"))
	assert.Equal(t, []string{"/", "/a", "/a/b"}, Ancestors("/a/b/c"))
}

func TestOrgCode(t *testing.T) {
	c := NewOrgCode()
	assert.Len(t, c, 8)
	assert.True(t, ValidOrgCode(c))
	assert.False(t, ValidOrgCode("ABCDEFGI")) // I is not in Crockford
	assert.False(t, ValidOrgCode("ABC"))
}

func TestEntryPaths(t *testing.T) {
	e := &Entry{Kind: KindPage, Path: "/research/index.md"}
	assert.Equal(t, "/research/", e.HTMLPath())
	assert.Equal(t, "/research/index.json", e.JSONPath())
	e = &Entry{Kind: KindPage, Path: "/index.md"}
	assert.Equal(t, "/", e.HTMLPath())
	assert.Equal(t, "/index.json", e.JSONPath())
	e = &Entry{Kind: KindPage, Path: "/about.md"}
	assert.Equal(t, "/about", e.HTMLPath())
	e = &Entry{Kind: KindDirectory, Path: "/research"}
	assert.Equal(t, "/research/", e.HTMLPath())
}

func TestNormalizeScopes(t *testing.T) {
	s, err := NormalizeScopes([]string{"content:write"})
	require.NoError(t, err)
	assert.Equal(t, []string{ScopeRead, ScopeWrite}, s, "write implies read")
	_, err = NormalizeScopes([]string{"admin"})
	assert.Error(t, err)
	_, err = NormalizeScopes(nil)
	assert.Error(t, err)
}

func TestStringArray(t *testing.T) {
	var a StringArray
	require.NoError(t, a.Scan(`{content:read,"with space","q\"uote",NULL}`))
	assert.Equal(t, StringArray{"content:read", "with space", `q"uote`, ""}, a)
	v, err := StringArray{"a", `b"c`}.Value()
	require.NoError(t, err)
	assert.Equal(t, `{"a","b\"c"}`, v)
	require.NoError(t, a.Scan("{}"))
	assert.Equal(t, StringArray{}, a)
}

func TestCursorRoundTrip(t *testing.T) {
	o := &Ops{CursorKey: []byte("0123456789abcdef0123456789abcdef")}
	s := o.encodeCursor(cursor{Scope: "list:/", After: "1/x"})
	c, ok := o.decodeCursor(s, "list:/")
	require.True(t, ok)
	assert.Equal(t, "1/x", c.After)
	_, ok = o.decodeCursor(s, "list:/other")
	assert.False(t, ok, "cursor is bound to its query")
	_, ok = o.decodeCursor(s+"x", "list:/")
	assert.False(t, ok, "tampered cursor rejected")
}
