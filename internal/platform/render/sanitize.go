package render

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// policy is the bluemonday allowlist applied to all rendered HTML. It admits
// only the markup the renderer itself emits; everything else (event handlers,
// style, hx-* attributes, forms, iframes, scripts) is stripped.
var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()

	tmpClass := regexp.MustCompile(`^tmp-[a-z0-9-]+(?: tmp-[a-z0-9-]+)*$`)

	p.AllowElements(
		"p", "br", "hr", "pre", "blockquote", "em", "strong", "del",
		"ul", "ol", "li", "table", "thead", "tbody", "tr", "th", "td",
		"details", "summary", "div", "span", "code", "a", "img", "input",
		"h1", "h2", "h3", "h4", "h5", "h6",
	)

	p.AllowAttrs("id").Matching(regexp.MustCompile(`^[a-z0-9-]+$`)).
		OnElements("h1", "h2", "h3", "h4", "h5", "h6")

	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("rel").Matching(regexp.MustCompile(`^noopener noreferrer$`)).OnElements("a")
	p.AllowAttrs("class").Matching(tmpClass).OnElements("a", "div", "span", "details")

	// img src: https or relative only (never http). Relative URLs may carry a
	// colon in later segments (e.g. "/o:XS67DF65/...") but never in the first.
	p.AllowAttrs("src").Matching(regexp.MustCompile(`^(?i:https://)\S+$|^/[^\s]*$|^[^:\s/]+(?:/[^\s]*)?$|^\.{1,2}/[^\s]*$`)).OnElements("img")
	p.AllowAttrs("alt", "title").OnElements("img")

	p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[A-Za-z0-9_+#.-]+$`)).OnElements("code")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^(?:tmp-[a-z0-9-]+|task-list(?:-item)?)$`)).OnElements("ul", "ol", "li")
	p.AllowAttrs("start").Matching(regexp.MustCompile(`^[0-9]+$`)).OnElements("ol")

	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("disabled", "checked").Matching(regexp.MustCompile(`^(?:disabled|checked|)$`)).OnElements("input")

	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(?:left|center|right)$`)).OnElements("th", "td")
	p.AllowAttrs("role").Matching(regexp.MustCompile(`^note$`)).OnElements("div")

	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)
	return p
}

// sanitizeHTML applies the allowlist policy.
func sanitizeHTML(html string) string {
	return policy.Sanitize(html)
}
