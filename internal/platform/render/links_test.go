package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemeOf(t *testing.T) {
	tests := []struct {
		in     string
		scheme string
		has    bool
	}{
		{"https://x", "https", true},
		{"HTTP://x", "http", true},
		{"mailto:a@b", "mailto", true},
		{"javascript:alert(1)", "javascript", true},
		{"JaVaScRiPt:alert(1)", "javascript", true},
		{"java\tscript:alert(1)", "javascript", true},
		{" javascript:alert(1)", "javascript", true},
		{"java\nscript:x", "javascript", true},
		{"data:text/html,x", "data", true},
		{"vbscript:x", "vbscript", true},
		{"file:///etc/passwd", "file", true},
		{"/o:XS67/x.md", "", false},
		{"circle.md", "", false},
		{"./a/b.md", "", false},
		{"../x.md", "", false},
		{"#frag", "", false},
		{"dir/with:colon", "", false},
		{"?q=1", "", false},
		{":nope", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		s, has := schemeOf(tc.in)
		assert.Equal(t, tc.has, has, "schemeOf(%q) has", tc.in)
		assert.Equal(t, tc.scheme, s, "schemeOf(%q) scheme", tc.in)
	}
}

func TestResolveSitePath(t *testing.T) {
	tests := []struct {
		source, ref, want string
		wantErr           bool
	}{
		{"/research/circle.md", "square.md", "/research/square.md", false},
		{"/research/circle.md", "./square.md", "/research/square.md", false},
		{"/research/circle.md", "../notes/x.md", "/notes/x.md", false},
		{"/research/circle.md", "/abs/x.md", "/abs/x.md", false},
		{"/research/circle.md", "sub/", "/research/sub/", false},
		{"/research/circle.md", "..", "/", false},
		{"/research/circle.md", "../", "/", false},
		{"/research/circle.md", "../../x.md", "", true},
		{"/research/circle.md", "/../x.md", "", true},
		{"/top.md", "other.md", "/other.md", false},
		{"", "other.md", "/other.md", false},
		{"/a/b/c.md", "../../d/./e.md", "/d/e.md", false},
	}
	for _, tc := range tests {
		got, err := resolveSitePath(tc.source, tc.ref)
		if tc.wantErr {
			assert.Error(t, err, "%s + %s", tc.source, tc.ref)
			continue
		}
		require.NoError(t, err, "%s + %s", tc.source, tc.ref)
		assert.Equal(t, tc.want, got, "%s + %s", tc.source, tc.ref)
	}
}

func TestRenderedPath(t *testing.T) {
	tests := map[string]string{
		"/research/circle.md": "/research/circle",
		"/research/index.md":  "/research/",
		"/index.md":           "/",
		"/research/":          "/research/",
		"/research/circle":    "/research/circle",
		"/img/x.png":          "/img/x.png",
	}
	for in, want := range tests {
		assert.Equal(t, want, renderedPath(in), in)
	}
}

func TestInternalLinks(t *testing.T) {
	opts := Options{SourcePath: "/research/circle.md", Prefix: "/o:XS67DF65"}
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"relative md", "[x](square.md)", `href="/o:XS67DF65/research/square"`},
		{"relative with fragment", "[x](square.md#part)", `href="/o:XS67DF65/research/square#part"`},
		{"parent dir", "[x](../notes/a.md)", `href="/o:XS67DF65/notes/a"`},
		{"absolute", "[x](/guide/start.md)", `href="/o:XS67DF65/guide/start"`},
		{"index.md to dir", "[x](/guide/index.md)", `href="/o:XS67DF65/guide/"`},
		{"root index", "[x](/index.md)", `href="/o:XS67DF65/"`},
		{"directory ref", "[x](/guide/)", `href="/o:XS67DF65/guide/"`},
		{"extensionless", "[x](/guide/start)", `href="/o:XS67DF65/guide/start"`},
		{"same page fragment", "[x](#section)", `href="#section"`},
		{"angle brackets", "[x](<sub dir/a.md>)", `href="/o:XS67DF65/research/sub%20dir/a"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := mustRender(t, tc.src+"\n", opts)
			assert.Contains(t, res.HTML, tc.want, res.HTML)
			assert.NotContains(t, res.HTML, "rel=", "internal links carry no rel")
		})
	}
}

func TestInternalLinkNoPrefix(t *testing.T) {
	res := mustRender(t, "[x](a.md)\n", Options{SourcePath: "/dir/p.md"})
	assert.Contains(t, res.HTML, `href="/dir/a"`)
}

func TestInternalLinkQueryStripped(t *testing.T) {
	res := mustRender(t, "\n\n[x](a.md?v=2#h)\n", Options{SourcePath: "/dir/p.md"})
	assert.Contains(t, res.HTML, `href="/dir/a#h"`)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, 3, res.Warnings[0].Line)
	assert.Contains(t, res.Warnings[0].Message, "query string removed")
}

func TestBrokenInternalLinkWarning(t *testing.T) {
	var asked []string
	opts := Options{SourcePath: "/d/p.md", ResolveLink: func(p string) bool {
		asked = append(asked, p)
		return p == "/d/exists.md"
	}}
	res := mustRender(t, "[a](exists.md) [b](missing.md) [c](https://x.y)\n", opts)
	assert.Equal(t, []string{"/d/exists.md", "/d/missing.md"}, asked)
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0].Message, "broken internal link: /d/missing.md")
}

func TestExternalLinks(t *testing.T) {
	res := mustRender(t, "[a](https://e.com/x?q=1) [b](http://plain.org) [c](mailto:me@e.com) <https://auto.link> me@auto.com www.example.com\n", Options{})
	assert.Contains(t, res.HTML, `<a href="https://e.com/x?q=1" rel="noopener noreferrer">a</a>`)
	assert.Contains(t, res.HTML, `<a href="http://plain.org" rel="noopener noreferrer">b</a>`)
	assert.Contains(t, res.HTML, `<a href="mailto:me@e.com" rel="noopener noreferrer">c</a>`)
	assert.Contains(t, res.HTML, `<a href="https://auto.link" rel="noopener noreferrer">https://auto.link</a>`)
	assert.Contains(t, res.HTML, `<a href="mailto:me@auto.com" rel="noopener noreferrer">me@auto.com</a>`)
	assert.Contains(t, res.HTML, `<a href="http://www.example.com" rel="noopener noreferrer">www.example.com</a>`)
}

func TestLinkErrors(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		line   int
		substr string
	}{
		{"javascript", "[x](javascript:alert(1))", 1, `unsupported URL scheme "javascript"`},
		{"mixed case", "[x](JaVaScRiPt:alert(1))", 1, `unsupported URL scheme "javascript"`},
		{"tab inside", "[x](<java\tscript:alert(1)>)", 1, `unsupported URL scheme "javascript"`},
		{"leading space", "[x]( javascript:alert(1))", 1, `unsupported URL scheme "javascript"`},
		{"data", "[x](data:text/html,hi)", 1, `unsupported URL scheme "data"`},
		{"vbscript", "[x](vbscript:msgbox)", 1, `unsupported URL scheme "vbscript"`},
		{"file", "[x](file:///etc/passwd)", 1, `unsupported URL scheme "file"`},
		{"ftp", "[x](ftp://host/f)", 1, `unsupported URL scheme "ftp"`},
		{"escape root", "[x](../../up.md)", 1, "escapes the site root"},
		{"escape root absolute", "[x](/../up.md)", 1, "escapes the site root"},
		{"line number", "# T\n\ntext\n\n[x](javascript:1)", 5, "unsupported URL scheme"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.src+"\n", Options{SourcePath: "/d/p.md"})
			requireError(t, err, tc.line, tc.substr)
		})
	}
}

func TestImages(t *testing.T) {
	resolved := map[string]string{"/d/img/a.png": "https://cdn.example/abc.png", "/root.png": "/assets/root.png"}
	opts := Options{SourcePath: "/d/p.md", ResolveAsset: func(p string) (string, bool) {
		u, ok := resolved[p]
		return u, ok
	}}
	src := "![A](img/a.png \"Title\") ![R](/root.png) ![gone](missing.png) ![ext](https://x.y/z.png) ![again](./img/a.png)\n"
	res := mustRender(t, src, opts)
	assert.Contains(t, res.HTML, `<img src="https://cdn.example/abc.png" alt="A" title="Title">`)
	assert.Contains(t, res.HTML, `<img src="/assets/root.png" alt="R">`)
	assert.Contains(t, res.HTML, `<span class="tmp-asset-unavailable">Image unavailable: missing.png</span>`)
	assert.NotContains(t, res.HTML, `alt="gone"`)
	assert.Contains(t, res.HTML, `<img src="https://x.y/z.png" alt="ext">`)
	assert.Equal(t, []string{"/d/img/a.png", "/root.png", "/d/missing.png"}, res.LocalAssets)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, 1, res.Warnings[0].Line)
	assert.Contains(t, res.Warnings[0].Message, "image unavailable: /d/missing.png")
}

func TestImageWithoutResolverKeepsSitePath(t *testing.T) {
	res := mustRender(t, "![a](pic.png)\n", Options{SourcePath: "/d/p.md"})
	assert.Contains(t, res.HTML, `<img src="/d/pic.png" alt="a">`)
	assert.Equal(t, []string{"/d/pic.png"}, res.LocalAssets)
}

func TestImageErrors(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		line   int
		substr string
	}{
		{"http image", "\n![x](http://x.y/z.png)", 2, "insecure image URL"},
		{"data image", "![x](data:image/png;base64,AAAA)", 1, `unsupported image URL scheme "data"`},
		{"javascript image", "![x](javascript:alert(1))", 1, `unsupported image URL scheme "javascript"`},
		{"escape root", "![x](../../z.png)", 1, "escapes the site root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.src+"\n", Options{SourcePath: "/d/p.md"})
			requireError(t, err, tc.line, tc.substr)
		})
	}
}

func TestExtractLocalImagePaths(t *testing.T) {
	src := "---\ntitle: t\n---\n![a](img/a.png) ![b](/b.png)\n\n![ext](https://x/y.png) ![a again](./img/a.png)\n\n```\n![code](c.png)\n```\n\n[![nested](../n.png)](https://x)\n"
	paths, err := ExtractLocalImagePaths(src, "/d/p.md")
	require.NoError(t, err)
	assert.Equal(t, []string{"/d/img/a.png", "/b.png", "/n.png"}, paths)

	_, err = ExtractLocalImagePaths("![x](../../up.png)\n", "/d/p.md")
	requireError(t, err, 1, "escapes the site root")

	paths, err = ExtractLocalImagePaths("no images\n", "/d/p.md")
	require.NoError(t, err)
	assert.Empty(t, paths)
}
