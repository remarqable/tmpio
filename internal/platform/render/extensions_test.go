package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalloutRenders(t *testing.T) {
	for typ, title := range calloutTitles {
		src := ":::callout type=\"" + typ + "\"\nBody with **bold**.\n:::\n"
		res := mustRender(t, src, Options{})
		assert.Contains(t, res.HTML, `<div class="tmp-callout tmp-callout-`+typ+`" role="note"><div class="tmp-callout-title">`+title+`</div><div class="tmp-callout-body"><p>Body with <strong>bold</strong>.</p>`)
		assert.Contains(t, res.HTML, "</div></div>")
		assert.Contains(t, res.PlainText, title+" Body with bold.")
	}
}

func TestDetailsRenders(t *testing.T) {
	res := mustRender(t, ":::details title=\"More <information>\"\nHidden *text*\n\n- item\n:::\n", Options{})
	assert.Contains(t, res.HTML, `<details class="tmp-details"><summary>More &lt;information&gt;</summary><div class="tmp-details-body"><p>Hidden <em>text</em></p>`)
	assert.Contains(t, res.HTML, "<li>item</li>")
	assert.Contains(t, res.HTML, "</div></details>")
}

func TestCardsRenders(t *testing.T) {
	src := ":::cards\n- [Circle](circle.md) - The circle page\n- [Root](/index.md)\n  A nested paragraph description\n- [External](https://example.com)\n:::\n"
	res := mustRender(t, src, Options{SourcePath: "/research/page.md", Prefix: "/o:AB12"})
	assert.Contains(t, res.HTML, `<div class="tmp-cards">`)
	assert.Contains(t, res.HTML, `<a class="tmp-card" href="/o:AB12/research/circle"><span class="tmp-card-title">Circle</span><span class="tmp-card-desc">The circle page</span></a>`)
	assert.Contains(t, res.HTML, `<a class="tmp-card" href="/o:AB12/"><span class="tmp-card-title">Root</span><span class="tmp-card-desc">A nested paragraph description</span></a>`)
	assert.Contains(t, res.HTML, `<a class="tmp-card" href="https://example.com" rel="noopener noreferrer"><span class="tmp-card-title">External</span></a>`)
	assert.Contains(t, res.PlainText, "Circle The circle page")
}

func TestExtensionHeadingsJoinTOCInOrder(t *testing.T) {
	src := "## Before\n\n:::callout type=\"tip\"\n### Inside\n:::\n\n## After\n"
	res := mustRender(t, src, Options{})
	require.Len(t, res.TOC, 3)
	assert.Equal(t, []string{"before", "inside", "after"}, []string{res.TOC[0].ID, res.TOC[1].ID, res.TOC[2].ID})
}

func TestExtensionErrors(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		line   int
		substr string
	}{
		{"unknown ext", "para\n\n:::spoiler\nx\n:::\n", 3, `unknown extension "spoiler"`},
		{"unknown ext line offset", "# T\n\ntext\n\n\n:::foo\n:::\n", 6, "unknown extension"},
		{"unclosed", "a\n\n:::callout type=\"note\"\nnever\nclosed\n", 3, "unclosed :::callout"},
		{"nested", ":::details title=\"t\"\ntext\n:::callout type=\"note\"\ninner\n:::\n:::\n", 3, "nested extension blocks"},
		{"callout missing type", ":::callout\nx\n:::\n", 1, "callout type must be one of"},
		{"callout bad type", "\n:::callout type=\"danger\"\nx\n:::\n", 2, "callout type must be one of"},
		{"details missing title", ":::details\nx\n:::\n", 1, `details requires a title`},
		{"unknown attribute", ":::callout type=\"note\" color=\"red\"\nx\n:::\n", 1, `unknown attribute "color"`},
		{"bad attribute syntax", ":::callout type=note\nx\n:::\n", 1, "invalid attribute syntax"},
		{"stray close", "para\n\n:::\n", 3, "closing ::: without an open extension block"},
		{"cards item without link", ":::cards\n- [ok](a.md)\n- no link here\n:::\n", 3, "exactly one link (found 0)"},
		{"cards item with two links", ":::cards\n- [a](a.md) and [b](b.md)\n:::\n", 2, "exactly one link (found 2)"},
		{"cards non-list content", ":::cards\nJust a paragraph\n:::\n", 2, "cards may only contain a single list"},
		{"cards empty", ":::cards\n:::\n", 1, "cards must contain a list"},
		{"cards bad link scheme", ":::cards\n- [bad](javascript:alert(1))\n:::\n", 2, "unsupported URL scheme"},
		{"error inside callout body has real line", "# T\n\n:::callout type=\"note\"\nfine\n\n[bad](javascript:x)\n:::\n", 6, "unsupported URL scheme"},
		{"frontmatter shifts lines", "---\ntitle: x\n---\n\n:::nope\n:::\n", 5, "unknown extension"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(tc.src, Options{SourcePath: "/p/page.md"})
			requireError(t, err, tc.line, tc.substr)
		})
	}
}

func TestExtensionFenceInteractions(t *testing.T) {
	t.Run("code fence containing ::: is ignored", func(t *testing.T) {
		res := mustRender(t, "```md\n:::callout type=\"note\"\ntext\n:::\n```\n", Options{})
		assert.Contains(t, res.HTML, "<pre><code class=\"language-md\">:::callout type=&#34;note&#34;\ntext\n:::\n</code></pre>")
		assert.NotContains(t, res.HTML, "tmp-callout")
	})
	t.Run("fence inside block hides ::: from the block", func(t *testing.T) {
		res := mustRender(t, ":::callout type=\"note\"\n```\n:::\n```\nafter\n:::\n", Options{})
		assert.Contains(t, res.HTML, "<pre><code>:::\n</code></pre>")
		assert.Contains(t, res.HTML, "<p>after</p>")
	})
	t.Run("tilde fence inside block", func(t *testing.T) {
		res := mustRender(t, ":::details title=\"x\"\n~~~\n:::callout type=\"note\"\n~~~\n:::\n", Options{})
		assert.Contains(t, res.HTML, "<code>:::callout")
	})
	t.Run("colon text is a paragraph", func(t *testing.T) {
		res := mustRender(t, ":smile: is fine\n\n:: two colons\n", Options{})
		assert.Contains(t, res.HTML, "<p>:smile: is fine</p>")
		assert.Contains(t, res.HTML, "<p>:: two colons</p>")
	})
	t.Run("block interrupts paragraph", func(t *testing.T) {
		res := mustRender(t, "para\n:::callout type=\"note\"\nx\n:::\n", Options{})
		assert.Contains(t, res.HTML, "<p>para</p>")
		assert.Contains(t, res.HTML, "tmp-callout-note")
	})
	t.Run("block inside list item", func(t *testing.T) {
		res := mustRender(t, "- item\n\n  :::callout type=\"tip\"\n  inner\n  :::\n", Options{})
		assert.Contains(t, res.HTML, `<div class="tmp-callout-body"><p>inner</p>`)
	})
	t.Run("empty block", func(t *testing.T) {
		res := mustRender(t, ":::callout type=\"note\"\n:::\n", Options{})
		assert.Contains(t, res.HTML, `<div class="tmp-callout-body"></div>`)
	})
}
