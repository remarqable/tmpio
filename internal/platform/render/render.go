// Package render converts a "tmp" Markdown page (optional YAML frontmatter
// followed by GitHub-flavoured Markdown with a few custom ::: container
// extensions) into sanitized HTML plus page metadata.
//
// The pipeline is: ParseFrontmatter -> goldmark parse -> AST validation and
// rewriting (heading IDs, link resolution, extension blocks) -> goldmark HTML
// rendering -> bluemonday sanitization.
package render

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// md is the shared goldmark instance. Every parser and renderer registered on
// it is stateless (per-parse state lives in the AST or the parser.Context),
// so it is safe for concurrent use.
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)),
		extension.Strikethrough,
		extension.Linkify,
		extension.TaskList,
	),
	goldmark.WithParserOptions(
		parser.WithBlockParsers(util.Prioritized(&extBlockParser{}, 100)),
	),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(util.Prioritized(&extRenderer{}, 100)),
	),
)

// Render converts source to sanitized HTML. On invalid input the returned
// error is a *ValidationError listing every problem found.
func Render(source string, opts Options) (*Result, error) {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if len(source) > maxBytes {
		return nil, &ValidationError{Errors: []FieldError{{
			Field:   "source",
			Message: fmt.Sprintf("input is %d bytes; the maximum is %d", len(source), maxBytes),
		}}}
	}
	meta, body, bodyStart, err := ParseFrontmatter(source)
	if err != nil {
		return nil, err
	}
	s := &renderState{
		opts:      opts,
		title:     meta.Title,
		usedIDs:   map[string]bool{},
		assetSeen: map[string]bool{},
	}
	html, plain := s.renderFragment([]byte(body), bodyStart-1, 0)
	if len(s.errs) > 0 {
		sort.SliceStable(s.errs, func(i, j int) bool { return s.errs[i].Line < s.errs[j].Line })
		return nil, &ValidationError{Errors: s.errs}
	}
	return &Result{
		HTML:        sanitizeHTML(html),
		Title:       s.title,
		Meta:        meta,
		TOC:         s.toc,
		PlainText:   plain,
		Warnings:    s.warnings,
		LocalAssets: s.assets,
	}, nil
}

// renderState accumulates page-wide results across nested fragments.
type renderState struct {
	opts      Options
	title     string
	errs      []FieldError
	warnings  []Warning
	toc       []TOCEntry
	usedIDs   map[string]bool
	assets    []string
	assetSeen map[string]bool
}

func (s *renderState) errorf(field string, line int, format string, args ...any) {
	s.errs = append(s.errs, FieldError{Field: field, Line: line, Message: fmt.Sprintf(format, args...)})
}

func (s *renderState) warnf(line int, format string, args ...any) {
	s.warnings = append(s.warnings, Warning{Line: line, Message: fmt.Sprintf(format, args...)})
}

// fragment is a piece of Markdown source with a line-number offset into the
// original document.
type fragment struct {
	src        []byte
	lineStarts []int
	offset     int // number of original lines preceding src
}

func newFragment(src []byte, offset int) *fragment {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' && i+1 < len(src) {
			starts = append(starts, i+1)
		}
	}
	return &fragment{src: src, lineStarts: starts, offset: offset}
}

// line returns the 1-based original line number of byte offset pos.
func (f *fragment) line(pos int) int {
	idx := sort.Search(len(f.lineStarts), func(i int) bool { return f.lineStarts[i] > pos })
	return f.offset + idx
}

// lineOf returns the best-known original line number of n.
func (f *fragment) lineOf(n ast.Node) int {
	for cur := n; cur != nil; cur = cur.Parent() {
		if pos, ok := nodePos(cur); ok {
			return f.line(pos)
		}
	}
	return f.offset + 1
}

// nodePos finds the first source offset belonging to n or its descendants.
func nodePos(n ast.Node) (int, bool) {
	switch t := n.(type) {
	case *ast.Text:
		return t.Segment.Start, true
	case *ast.RawHTML:
		if t.Segments.Len() > 0 {
			return t.Segments.At(0).Start, true
		}
	case *extBlock:
		return t.openPos, true
	}
	if n.Type() != ast.TypeInline && n.Lines() != nil && n.Lines().Len() > 0 {
		return n.Lines().At(0).Start, true
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if pos, ok := nodePos(c); ok {
			return pos, true
		}
	}
	return 0, false
}

// renderFragment parses, validates, rewrites and renders one Markdown
// fragment. depth is 0 for the page body and 1 inside extension blocks.
func (s *renderState) renderFragment(src []byte, lineOffset, depth int) (html, plain string) {
	f := newFragment(src, lineOffset)
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(parser.NewContext()))
	if depth == 0 {
		s.resolveTitle(doc, f)
	}
	s.transform(doc, f, depth)
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		s.errorf("body", lineOffset+1, "render failed: %v", err)
	}
	return buf.String(), plainText(doc, src)
}

// resolveTitle picks the page title (frontmatter, first H1, filename) and
// drops a leading H1 that duplicates it.
func (s *renderState) resolveTitle(doc ast.Node, f *fragment) {
	if s.title == "" {
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if h, ok := n.(*ast.Heading); ok && entering && h.Level == 1 {
				s.title = nodeText(h, f.src)
				return ast.WalkStop, nil
			}
			return ast.WalkContinue, nil
		})
	}
	if s.title == "" {
		s.title = s.opts.FilenameFallbackTitle
	}
	if first, ok := doc.FirstChild().(*ast.Heading); ok && first.Level == 1 && nodeText(first, f.src) == s.title {
		doc.RemoveChild(doc, first)
	}
}

// transform validates and rewrites the AST in place.
func (s *renderState) transform(doc ast.Node, f *fragment, depth int) {
	type replacement struct{ old, repl ast.Node }
	var repls []replacement
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Heading:
			s.processHeading(t, f)
		case *ast.HTMLBlock, *ast.RawHTML:
			s.errorf("body", f.lineOf(n), "raw HTML is not allowed")
		case *ast.Link:
			s.processLink(t, f)
		case *ast.AutoLink:
			t.SetAttributeString("rel", relExternal)
		case *ast.Image:
			if ph := s.processImage(t, f); ph != nil {
				repls = append(repls, replacement{t, ph})
			}
		case *extBlock:
			if depth == 0 {
				s.processExt(t, f, depth)
			} else {
				// Already reported as "nested" by the enclosing block.
				repls = append(repls, replacement{t, nil})
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for _, r := range repls {
		p := r.old.Parent()
		if r.repl == nil {
			p.RemoveChild(p, r.old)
		} else {
			p.ReplaceChild(p, r.old, r.repl)
		}
	}
}

// processHeading assigns a unique slug ID and records TOC entries.
func (s *renderState) processHeading(h *ast.Heading, f *fragment) {
	txt := nodeText(h, f.src)
	base := Slugify(txt)
	id := base
	for i := 2; s.usedIDs[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	s.usedIDs[id] = true
	h.SetAttributeString("id", []byte(id))
	if h.Level == 2 || h.Level == 3 {
		s.toc = append(s.toc, TOCEntry{Level: h.Level, ID: id, Text: txt})
	}
}

// Slugify turns heading text into an ASCII id: lowercase letters, digits and
// single hyphens, trimmed. Empty results become "section".
func Slugify(s string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
		default:
			pendingHyphen = true
		}
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

// nodeText returns the whitespace-normalized text content of n.
func nodeText(n ast.Node, src []byte) string {
	var sb strings.Builder
	collectPlain(n, src, &sb)
	return strings.Join(strings.Fields(sb.String()), " ")
}

// plainText returns the whitespace-normalized text of a whole document.
func plainText(doc ast.Node, src []byte) string {
	return nodeText(doc, src)
}

func collectPlain(n ast.Node, src []byte, sb *strings.Builder) {
	switch t := n.(type) {
	case *ast.Text:
		sb.Write(t.Segment.Value(src))
		if t.SoftLineBreak() || t.HardLineBreak() {
			sb.WriteByte(' ')
		}
		return
	case *ast.String:
		sb.Write(t.Value)
		return
	case *ast.AutoLink:
		sb.Write(t.Label(src))
		return
	case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock:
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			sb.Write(seg.Value(src))
			sb.WriteByte(' ')
		}
		return
	case *extBlock:
		sb.WriteByte(' ')
		sb.WriteString(t.plain)
		sb.WriteByte(' ')
		return
	case *rawOutput:
		return
	}
	block := n.Type() != ast.TypeInline
	if block {
		sb.WriteByte(' ')
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		collectPlain(c, src, sb)
	}
	if block {
		sb.WriteByte(' ')
	}
}
