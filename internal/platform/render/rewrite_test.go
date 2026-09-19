package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelativePath(t *testing.T) {
	tests := []struct{ fromDir, target, want string }{
		{"/c/d", "/a/img.png", "../../a/img.png"},
		{"/c/d", "/c/d/x.md", "x.md"},
		{"/c/d", "/c/x.md", "../x.md"},
		{"/", "/a/b.md", "a/b.md"},
		{"/c/d", "/a/", "../../a/"},
		{"/c/d", "/", "../../"},
		{"/c/d", "/c/d/", "./"},
		{"/a", "/a/b/c.md", "b/c.md"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, relativePath(tc.fromDir, tc.target), "%s -> %s", tc.fromDir, tc.target)
	}
}

func TestRewriteRelativeLinksBasics(t *testing.T) {
	src := "---\ntitle: T\nextra:\n  see: ./img.png\n---\n# Title\n\nSee ![pic](./img.png) and [page](x.md) and [abs](/abs.md) and [ext](https://example.com/x.md).\n\nAlso [frag](#top), [sub](sub/dir/), [q](y.md?v=1#sec), and <https://auto.link>.\n\n```\n[code](z.md)\n```\n"
	out, rewrites, err := RewriteRelativeLinks(src, "/a/b.md", "/c/d/e.md")
	require.NoError(t, err)
	want := "---\ntitle: T\nextra:\n  see: ./img.png\n---\n# Title\n\nSee ![pic](../../a/img.png) and [page](../../a/x.md) and [abs](/abs.md) and [ext](https://example.com/x.md).\n\nAlso [frag](#top), [sub](../../a/sub/dir/), [q](../../a/y.md?v=1#sec), and <https://auto.link>.\n\n```\n[code](z.md)\n```\n"
	assert.Equal(t, want, out)
	assert.Equal(t, []string{
		"./img.png -> ../../a/img.png",
		"x.md -> ../../a/x.md",
		"sub/dir/ -> ../../a/sub/dir/",
		"y.md?v=1#sec -> ../../a/y.md?v=1#sec",
	}, rewrites)
}

func TestRewriteRelativeLinksNoChange(t *testing.T) {
	src := "[same](x.md) [abs](/y.md)\n"
	out, rewrites, err := RewriteRelativeLinks(src, "/a/b.md", "/a/c.md")
	require.NoError(t, err)
	assert.Equal(t, src, out)
	assert.Empty(t, rewrites)
}

func TestRewriteRelativeLinksMoveUp(t *testing.T) {
	out, rewrites, err := RewriteRelativeLinks("[p](../top.md) [s](x.md)\n", "/a/b/c.md", "/top2.md")
	require.NoError(t, err)
	assert.Equal(t, "[p](a/top.md) [s](a/b/x.md)\n", out)
	assert.Len(t, rewrites, 2)
}

func TestRewriteRelativeLinksReferenceDefinitions(t *testing.T) {
	src := "Read [the doc][d] and ![img][i].\n\n[d]: ./doc.md \"Doc\"\n[i]: <pics/i.png>\n"
	out, rewrites, err := RewriteRelativeLinks(src, "/a/b.md", "/c/d/e.md")
	require.NoError(t, err)
	assert.Equal(t, "Read [the doc][d] and ![img][i].\n\n[d]: ../../a/doc.md \"Doc\"\n[i]: <../../a/pics/i.png>\n", out)
	assert.Len(t, rewrites, 2)
}

func TestRewriteRelativeLinksInsideContainers(t *testing.T) {
	// Tab-indented list content yields padded segments in goldmark; the
	// fallback locator must still find the destination.
	src := "- item\n\n\t[deep](x.md) and ![i](i.png)\n\n> [quoted](q.md)\n\n:::callout type=\"note\"\n[in ext](e.md)\n:::\n"
	out, rewrites, err := RewriteRelativeLinks(src, "/a/b.md", "/c/d/e.md")
	require.NoError(t, err)
	assert.Equal(t, "- item\n\n\t[deep](../../a/x.md) and ![i](../../a/i.png)\n\n> [quoted](../../a/q.md)\n\n:::callout type=\"note\"\n[in ext](../../a/e.md)\n:::\n", out)
	assert.Len(t, rewrites, 4)
}

func TestRewriteRelativeLinksErrors(t *testing.T) {
	_, _, err := RewriteRelativeLinks("[x](../../up.md)\n", "/a/b.md", "/c.md")
	requireError(t, err, 1, "escapes the site root")

	_, _, err = RewriteRelativeLinks("x", "a.md", "/b.md")
	fieldErrors(t, err)
}
