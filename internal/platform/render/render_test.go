package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustRender(t *testing.T, src string, opts Options) *Result {
	t.Helper()
	res, err := Render(src, opts)
	require.NoError(t, err)
	return res
}

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"Intro":                 "intro",
		"  Hello, World!  ":     "hello-world",
		"Ünïcödé & ASCII 42":    "n-c-d-ascii-42",
		"---":                   "section",
		"":                      "section",
		"multiple---hyphens--x": "multiple-hyphens-x",
		"CamelCase Words":       "camelcase-words",
	}
	for in, want := range tests {
		assert.Equal(t, want, Slugify(in), "Slugify(%q)", in)
	}
}

func TestHeadingIDsAndTOC(t *testing.T) {
	src := "# Page\n\n## Intro\n\n### Intro\n\n## Intro\n\n#### Deep\n\n## Other *emph* `code`\n"
	res := mustRender(t, src, Options{})
	assert.Contains(t, res.HTML, `<h2 id="intro">Intro</h2>`)
	assert.Contains(t, res.HTML, `<h3 id="intro-2">Intro</h3>`)
	assert.Contains(t, res.HTML, `<h2 id="intro-3">Intro</h2>`)
	assert.Contains(t, res.HTML, `<h4 id="deep">Deep</h4>`)
	assert.Contains(t, res.HTML, `<h2 id="other-emph-code">`)
	require.Len(t, res.TOC, 4, "TOC only has H2-H3")
	assert.Equal(t, []TOCEntry{
		{Level: 2, ID: "intro", Text: "Intro"},
		{Level: 3, ID: "intro-2", Text: "Intro"},
		{Level: 2, ID: "intro-3", Text: "Intro"},
		{Level: 2, ID: "other-emph-code", Text: "Other emph code"},
	}, res.TOC)
}

func TestHeadingIDCollisionWithExplicitSuffix(t *testing.T) {
	res := mustRender(t, "## Intro 2\n\n## Intro\n\n## Intro\n", Options{})
	assert.Contains(t, res.HTML, `id="intro-2"`)
	assert.Contains(t, res.HTML, `id="intro"`)
	assert.Contains(t, res.HTML, `id="intro-3"`)
}

func TestTitleResolution(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		opts      Options
		wantTitle string
		wantH1    bool
	}{
		{"frontmatter wins", "---\ntitle: FM\n---\n# Heading\n\nx\n", Options{}, "FM", true},
		{"frontmatter equals H1 removes it", "---\ntitle: Same\n---\n# Same\n\nx\n", Options{}, "Same", false},
		{"first H1 becomes title and is removed", "# My Doc\n\nx\n", Options{}, "My Doc", false},
		{"H1 not first block is kept", "intro\n\n# My Doc\n", Options{}, "My Doc", true},
		{"filename fallback", "just text\n", Options{FilenameFallbackTitle: "file.md"}, "file.md", false},
		{"filename fallback ignores later H1 mismatch", "text\n\n# Real\n", Options{FilenameFallbackTitle: "f"}, "Real", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := mustRender(t, tc.src, tc.opts)
			assert.Equal(t, tc.wantTitle, res.Title)
			assert.Equal(t, tc.wantH1, strings.Contains(res.HTML, "<h1"), res.HTML)
		})
	}
}

func TestRawHTMLRejected(t *testing.T) {
	tests := []struct {
		name string
		src  string
		line int
	}{
		{"block script", "# T\n\n<script>alert(1)</script>\n", 3},
		{"block div", "para\n\n<div class=\"x\">hi</div>\n", 3},
		{"inline img onerror", "para\n\nsome <img src=x onerror=alert(1)> text\n", 3},
		{"inline span", "text <span>x</span>\n", 1},
		{"html comment", "\n\n<!-- hidden -->\n", 3},
		{"inside callout", ":::callout type=\"note\"\nsafe\n\n<b>x</b>\n:::\n", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.src, Options{})
			requireError(t, err, tc.line, "raw HTML is not allowed")
		})
	}
}

func TestHTMLInCodeIsFine(t *testing.T) {
	res := mustRender(t, "```html\n<div onclick=\"x\">hi</div>\n```\n\nInline `<script>` ok.\n", Options{})
	assert.Contains(t, res.HTML, `<pre><code class="language-html">&lt;div onclick=&#34;x&#34;&gt;hi&lt;/div&gt;`)
	assert.Contains(t, res.HTML, `<code>&lt;script&gt;</code>`)
}

func TestStandardMarkdownFeatures(t *testing.T) {
	src := "Para with *em* and **strong** and ~~del~~.\n\n> quote\n\n- a\n- b\n\n3. three\n4. four\n\n- [ ] todo\n- [x] done\n\n| h1 | h2 |\n|:---|---:|\n| c1 | c2 |\n\n---\n\n```go\nfmt.Println()\n```\n\nline one  \nline two\n"
	res := mustRender(t, src, Options{})
	for _, want := range []string{
		"<em>em</em>", "<strong>strong</strong>", "<del>del</del>",
		"<blockquote>", "<ul>", `<ol start="3">`, "<hr>",
		`<input disabled="" type="checkbox"> todo`, `<input checked="" disabled="" type="checkbox"> done`,
		`<th align="left">h1</th>`, `<td align="right">c2</td>`,
		`<pre><code class="language-go">fmt.Println()`, "<br>",
	} {
		assert.Contains(t, res.HTML, want)
	}
	assert.NotContains(t, res.HTML, "style=")
}

func TestPlainText(t *testing.T) {
	res := mustRender(t, "# T\n\nHello   *world*\nnext line.\n\n- item\n\n```\ncode here\n```\n", Options{})
	assert.Equal(t, "Hello world next line. item code here", res.PlainText)
}

func TestMaxBytes(t *testing.T) {
	_, err := Render(strings.Repeat("a", 100), Options{MaxBytes: 50})
	errs := fieldErrors(t, err)
	assert.Equal(t, "source", errs[0].Field)
	assert.Contains(t, errs[0].Message, "maximum is 50")

	_, err = Render(strings.Repeat("a", DefaultMaxBytes+1), Options{})
	fieldErrors(t, err)
}

func TestSanitizerStripsDisallowedMarkup(t *testing.T) {
	// A link title containing quote characters must not break out of the attribute.
	res := mustRender(t, `[x](https://e.com "a\" onclick=\"alert(1)")`+"\n", Options{})
	assert.NotContains(t, res.HTML, `onclick="alert`, "quote must stay escaped inside the title attribute")
	assert.Contains(t, res.HTML, `title="a&#34; onclick=&#34;alert(1)"`)

	// Direct policy checks on hand-crafted markup the renderer could never
	// produce, to prove the allowlist holds regardless of input.
	dirty := `<p onclick="x" style="color:red" hx-get="/x">t</p>` +
		`<a href="javascript:alert(1)" onmouseover="y">j</a>` +
		`<a href="https://ok" rel="nofollow" target="_blank">ok</a>` +
		`<img src="http://x/a.png" alt="a"><img src="data:image/png;base64,AAAA" alt="d">` +
		`<img src="https://x/a.png" alt="b" onerror="z">` +
		`<div class="evil tmp-x">d</div><span class="tmp-y">s</span>` +
		`<form action="/p"><input type="text" name="q"></form>` +
		`<iframe src="https://x"></iframe><script>alert(1)</script>` +
		`<h2 id="Bad ID" data-x="1">h</h2><input type="checkbox" disabled="" checked="">`
	clean := sanitizeHTML(dirty)
	for _, bad := range []string{"onclick", "style=", "hx-get", "javascript:", "onmouseover", "nofollow", "target=", "http://x", "data:", "onerror", "evil", "<form", `type="text"`, "<iframe", "<script", "alert(1)", "Bad ID", "data-x"} {
		assert.NotContains(t, clean, bad, "sanitizer must strip %q", bad)
	}
	for _, good := range []string{"<p>t</p>", `<a href="https://ok">ok</a>`, `<img src="https://x/a.png" alt="b">`, `<span class="tmp-y">s</span>`, `<input type="checkbox" disabled="" checked="">`, "<h2>h</h2>"} {
		assert.Contains(t, clean, good)
	}
}

func TestValidationErrorString(t *testing.T) {
	e := &ValidationError{Errors: []FieldError{{Field: "a", Line: 2, Message: "m"}, {Field: "b", Message: "n"}}}
	assert.Equal(t, "line 2: a: m; b: n", e.Error())
}

func TestConcurrentRenderIsSafe(t *testing.T) {
	src := "---\ntitle: T\n---\n## A\n\n:::callout type=\"note\"\n[l](x.md) ![i](i.png)\n:::\n\n:::cards\n- [c](c.md) desc\n:::\n\n| a |\n|---|\n| b |\n"
	want := mustRender(t, src, Options{SourcePath: "/d/p.md"})
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				got, err := Render(src, Options{SourcePath: "/d/p.md"})
				assert.NoError(t, err)
				assert.Equal(t, want.HTML, got.HTML)
			}
		}()
	}
	for i := 0; i < 16; i++ {
		<-done
	}
}
