package render

import (
	"bytes"
	stdhtml "html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// extBlock is a ":::name key=\"value\"" container block. The parser collects
// the raw inner lines; the transform step renders them recursively and stores
// the result in rendered/plain for the renderer.
type extBlock struct {
	ast.BaseBlock
	name    string
	attrs   map[string]string
	openErr string // syntax error on the opening line
	openPos int    // source offset of the opening line
	indent  int    // indentation of the opening fence
	closed  bool
	stray   bool  // a bare ":::" with no block to close
	nested  []int // source offsets of ":::name" lines found inside
	// fence tracking so ::: inside inner code fences is ignored
	fenceChar byte
	fenceLen  int

	rendered []byte
	plain    string
}

var kindExtBlock = ast.NewNodeKind("TmpExtBlock")

// Kind implements ast.Node.
func (n *extBlock) Kind() ast.NodeKind { return kindExtBlock }

// Dump implements ast.Node.
func (n *extBlock) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"name": n.name}, nil)
}

// rawOutput is an inline node carrying pre-built, trusted HTML (for example
// the "image unavailable" placeholder).
type rawOutput struct {
	ast.BaseInline
	html string
}

var kindRawOutput = ast.NewNodeKind("TmpRawOutput")

// Kind implements ast.Node.
func (n *rawOutput) Kind() ast.NodeKind { return kindRawOutput }

// Dump implements ast.Node.
func (n *rawOutput) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// extBlockParser is a goldmark block parser for ::: containers. It mirrors the
// fenced code block parser: the block is a leaf whose lines are kept raw.
type extBlockParser struct{}

func (p *extBlockParser) Trigger() []byte                             { return []byte{':'} }
func (p *extBlockParser) CanInterruptParagraph() bool                 { return true }
func (p *extBlockParser) CanAcceptIndentedLine() bool                 { return false }
func (p *extBlockParser) Close(ast.Node, text.Reader, parser.Context) {}

var (
	extNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*`)
	extAttrRe = regexp.MustCompile(`^\s+([A-Za-z][A-Za-z0-9_-]*)="([^"]*)"`)
)

func (p *extBlockParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || !bytes.HasPrefix(line[pos:], []byte(":::")) {
		return nil, parser.NoChildren
	}
	n := &extBlock{openPos: segment.Start + pos, indent: pos, attrs: map[string]string{}}
	rest := strings.TrimSpace(string(line[pos+3:]))
	if rest == "" {
		n.stray = true
		n.openErr = "closing ::: without an open extension block"
		return n, parser.NoChildren
	}
	n.name = extNameRe.FindString(rest)
	if n.name == "" {
		n.openErr = "invalid extension syntax; expected :::name key=\"value\""
		return n, parser.NoChildren
	}
	rest = rest[len(n.name):]
	for rest != "" {
		m := extAttrRe.FindStringSubmatch(rest)
		if m == nil {
			n.openErr = "invalid attribute syntax; expected key=\"value\" pairs"
			break
		}
		if _, dup := n.attrs[m[1]]; dup {
			n.openErr = "duplicate attribute " + m[1]
			break
		}
		n.attrs[m[1]] = m[2]
		rest = rest[len(m[0]):]
	}
	return n, parser.NoChildren
}

func (p *extBlockParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	n := node.(*extBlock)
	if n.stray {
		return parser.Close
	}
	line, segment := reader.PeekLine()
	w, pos := util.IndentWidth(line, reader.LineOffset())
	if w < 4 && pos < len(line) {
		trimmed := line[pos:]
		if n.fenceChar == 0 {
			if bytes.HasPrefix(trimmed, []byte(":::")) {
				if util.IsBlank(trimmed[3:]) {
					n.closed = true
					reader.AdvanceToEOL()
					return parser.Close
				}
				n.nested = append(n.nested, segment.Start+pos)
			} else if c := trimmed[0]; c == '`' || c == '~' {
				if run := fenceRun(trimmed, c); run >= 3 {
					n.fenceChar, n.fenceLen = c, run
				}
			}
		} else if run := fenceRun(trimmed, n.fenceChar); run >= n.fenceLen && util.IsBlank(trimmed[run:]) {
			n.fenceChar, n.fenceLen = 0, 0
		}
	}
	pos, padding := util.IndentPositionPadding(line, reader.LineOffset(), segment.Padding, n.indent)
	if pos < 0 {
		pos = max(0, util.FirstNonSpacePosition(line)) - segment.Padding
		padding = 0
	}
	seg := text.NewSegmentPadding(segment.Start+pos, segment.Stop, padding)
	seg.ForceNewline = true
	node.Lines().Append(seg)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func fenceRun(line []byte, c byte) int {
	i := 0
	for i < len(line) && line[i] == c {
		i++
	}
	return i
}

// extRenderer writes the pre-rendered HTML of custom nodes.
type extRenderer struct{}

func (r *extRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindExtBlock, func(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.Write(node.(*extBlock).rendered)
			_ = w.WriteByte('\n')
		}
		return ast.WalkSkipChildren, nil
	})
	reg.Register(kindRawOutput, func(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.WriteString(node.(*rawOutput).html)
		}
		return ast.WalkSkipChildren, nil
	})
}

var calloutTitles = map[string]string{
	"note": "Note", "tip": "Tip", "warning": "Warning", "important": "Important",
}

var extAllowedAttrs = map[string]map[string]bool{
	"callout": {"type": true},
	"details": {"title": true},
	"cards":   {},
}

// processExt validates an extension block and renders its inner content.
func (s *renderState) processExt(n *extBlock, f *fragment, depth int) {
	line := f.line(n.openPos)
	if n.openErr != "" {
		s.errorf("extension", line, "%s", n.openErr)
		return
	}
	allowed, known := extAllowedAttrs[n.name]
	if !known {
		s.errorf("extension", line, "unknown extension %q", n.name)
		return
	}
	for k := range n.attrs {
		if !allowed[k] {
			s.errorf("extension", line, "unknown attribute %q for :::%s", k, n.name)
			return
		}
	}
	if !n.closed {
		s.errorf("extension", line, "unclosed :::%s block", n.name)
		return
	}
	if len(n.nested) > 0 {
		for _, p := range n.nested {
			s.errorf("extension", f.line(p), "nested extension blocks are not allowed")
		}
		return
	}
	inner, innerOffset := n.innerSource(f)
	switch n.name {
	case "callout":
		typ := n.attrs["type"]
		title, ok := calloutTitles[typ]
		if !ok {
			s.errorf("extension", line, "callout type must be one of note, tip, warning, important")
			return
		}
		body, plain := s.renderFragment(inner, innerOffset, depth+1)
		n.rendered = []byte(`<div class="tmp-callout tmp-callout-` + typ + `" role="note"><div class="tmp-callout-title">` +
			title + `</div><div class="tmp-callout-body">` + body + `</div></div>`)
		n.plain = title + " " + plain
	case "details":
		title := strings.TrimSpace(n.attrs["title"])
		if title == "" {
			s.errorf("extension", line, `details requires a title="..." attribute`)
			return
		}
		body, plain := s.renderFragment(inner, innerOffset, depth+1)
		n.rendered = []byte(`<details class="tmp-details"><summary>` + stdhtml.EscapeString(title) +
			`</summary><div class="tmp-details-body">` + body + `</div></details>`)
		n.plain = title + " " + plain
	case "cards":
		s.renderCards(n, inner, innerOffset, line)
	}
}

// innerSource reconstructs the block's inner lines and their line offset.
func (n *extBlock) innerSource(f *fragment) ([]byte, int) {
	lines := n.Lines()
	if lines.Len() == 0 {
		return nil, f.line(n.openPos)
	}
	var buf bytes.Buffer
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		buf.Write(seg.Value(f.src))
	}
	return buf.Bytes(), f.line(lines.At(0).Start) - 1
}

// renderCards renders a :::cards block: a single list whose items each hold
// exactly one link plus an optional description.
func (s *renderState) renderCards(n *extBlock, inner []byte, innerOffset, line int) {
	f := newFragment(inner, innerOffset)
	doc := md.Parser().Parse(text.NewReader(inner), parser.WithContext(parser.NewContext()))
	var list *ast.List
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		if l, ok := c.(*ast.List); ok && list == nil {
			list = l
			continue
		}
		s.errorf("extension", f.lineOf(c), "cards may only contain a single list of links")
		return
	}
	if list == nil {
		s.errorf("extension", line, "cards must contain a list of links")
		return
	}
	var sb, plain strings.Builder
	sb.WriteString(`<div class="tmp-cards">`)
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		itemLine := f.lineOf(item)
		var links []ast.Node
		var desc strings.Builder
		_ = ast.Walk(item, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch t := c.(type) {
			case *ast.Link, *ast.AutoLink:
				links = append(links, c)
				return ast.WalkSkipChildren, nil
			case *ast.HTMLBlock, *ast.RawHTML:
				s.errorf("body", f.lineOf(c), "raw HTML is not allowed")
			case *ast.Text:
				if len(links) > 0 {
					desc.Write(t.Segment.Value(inner))
					desc.WriteByte(' ')
				}
			}
			return ast.WalkContinue, nil
		})
		if len(links) != 1 {
			s.errorf("extension", itemLine, "each card must contain exactly one link (found %d)", len(links))
			continue
		}
		href, external, ok := s.cardHref(links[0], inner, itemLine)
		if !ok {
			continue
		}
		title := nodeText(links[0], inner)
		if al, isAuto := links[0].(*ast.AutoLink); isAuto {
			title = string(al.Label(inner))
		}
		description := strings.Join(strings.Fields(desc.String()), " ")
		description = strings.TrimSpace(strings.TrimLeft(description, "-–—:"))
		sb.WriteString(`<a class="tmp-card" href="` + stdhtml.EscapeString(href) + `"`)
		if external {
			sb.WriteString(` rel="noopener noreferrer"`)
		}
		sb.WriteString(`><span class="tmp-card-title">` + stdhtml.EscapeString(title) + `</span>`)
		if description != "" {
			sb.WriteString(`<span class="tmp-card-desc">` + stdhtml.EscapeString(description) + `</span>`)
		}
		sb.WriteString(`</a>`)
		plain.WriteString(title + " " + description + " ")
	}
	sb.WriteString(`</div>`)
	n.rendered = []byte(sb.String())
	n.plain = plain.String()
}

// cardHref resolves the destination of a card link.
func (s *renderState) cardHref(link ast.Node, src []byte, line int) (href string, external, ok bool) {
	if al, isAuto := link.(*ast.AutoLink); isAuto {
		u := string(al.URL(src))
		if al.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(strings.ToLower(u), "mailto:") {
			u = "mailto:" + u
		}
		return u, true, true
	}
	return s.resolveLinkDest(string(link.(*ast.Link).Destination), line)
}
