package render

import (
	"bytes"
	"path"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// RewriteRelativeLinks rewrites the relative link and image destinations in
// source so that, after the page moves from fromPath to toPath, they still
// point at the same site paths. Absolute ("/..."), fragment-only and external
// destinations are untouched, as is the frontmatter; everything else is
// preserved byte-for-byte. Inline links, inline images and link reference
// definitions are handled. rewrites lists each change as "old -> new".
func RewriteRelativeLinks(source, fromPath, toPath string) (newSource string, rewrites []string, err error) {
	if !strings.HasPrefix(fromPath, "/") || !strings.HasPrefix(toPath, "/") {
		return "", nil, &ValidationError{Errors: []FieldError{{Field: "path", Message: "fromPath and toPath must be absolute site paths"}}}
	}
	_, body, bodyStart, err := ParseFrontmatter(source)
	if err != nil {
		return "", nil, err
	}
	head := source[:len(source)-len(body)]
	// Exact-capacity copy so that sub-slices can be mapped back to offsets.
	src := make([]byte, len(body))
	copy(src, body)
	f := newFragment(src, bodyStart-1)
	ctx := parser.NewContext()
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(ctx))

	type edit struct {
		off, length int
		repl        string
	}
	edits := map[int]edit{}
	var verr ValidationError
	toDir := path.Dir(toPath)

	handle := func(dest []byte, container ast.Node, line int) {
		trimmed := strings.TrimSpace(string(dest))
		if trimmed == "" || strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "#") {
			return
		}
		if _, has := schemeOf(trimmed); has {
			return
		}
		p, query, frag, hasQuery := splitRef(trimmed)
		if p == "" {
			return
		}
		off, ok := locateDest(src, dest, container)
		if !ok {
			return
		}
		if _, done := edits[off]; done {
			return
		}
		sitePath, rerr := resolveSitePath(fromPath, p)
		if rerr != nil {
			verr.Errors = append(verr.Errors, FieldError{Field: "link", Line: line, Message: "link \"" + trimmed + "\" escapes the site root"})
			return
		}
		newRel := relativePath(toDir, sitePath)
		if hasQuery {
			newRel += "?" + query
		}
		if frag != "" {
			newRel += "#" + frag
		}
		if newRel == trimmed {
			return
		}
		edits[off] = edit{off: off, length: len(dest), repl: newRel}
		rewrites = append(rewrites, trimmed+" -> "+newRel)
	}

	// visit handles links, images and reference definitions of one parsed
	// document, descending into extension blocks whose raw inner lines are
	// re-parsed (as a sub-slice of src when possible, so offsets still map).
	var visit func(doc ast.Node, ctx parser.Context)
	visit = func(doc ast.Node, ctx parser.Context) {
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch t := n.(type) {
			case *ast.Link:
				handle(t.Destination, t, f.lineOf(t))
			case *ast.Image:
				handle(t.Destination, t, f.lineOf(t))
			case *extBlock:
				inner := extInnerSlice(src, t)
				if len(inner) > 0 {
					innerCtx := parser.NewContext()
					innerDoc := md.Parser().Parse(text.NewReader(inner), parser.WithContext(innerCtx))
					visit(innerDoc, innerCtx)
				}
				return ast.WalkSkipChildren, nil
			}
			return ast.WalkContinue, nil
		})
		for _, ref := range ctx.References() {
			handle(ref.Destination(), nil, 0)
		}
	}
	visit(doc, ctx)
	if len(verr.Errors) > 0 {
		return "", nil, &verr
	}

	sorted := make([]edit, 0, len(edits))
	for _, e := range edits {
		sorted = append(sorted, e)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].off < sorted[j].off })
	var out strings.Builder
	out.WriteString(head)
	last := 0
	for _, e := range sorted {
		out.Write(src[last:e.off])
		out.WriteString(e.repl)
		last = e.off + e.length
	}
	out.Write(src[last:])
	return out.String(), rewrites, nil
}

// extInnerSlice returns the inner lines of an extension block. When the lines
// are contiguous and unpadded the result is a sub-slice of src; otherwise the
// lines are copied and destinations fall back to a text search.
func extInnerSlice(src []byte, n *extBlock) []byte {
	lines := n.Lines()
	if lines.Len() == 0 {
		return nil
	}
	contiguous := true
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		if seg.Padding != 0 || (i > 0 && seg.Start != lines.At(i-1).Stop) {
			contiguous = false
			break
		}
	}
	if contiguous {
		return src[lines.At(0).Start:lines.At(lines.Len()-1).Stop]
	}
	var buf bytes.Buffer
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		buf.Write(seg.Value(src))
	}
	return buf.Bytes()
}

// locateDest finds the byte offset of dest inside src. goldmark hands out
// destinations as sub-slices of the source whenever the line carries no
// padding, so the capacity difference gives the offset directly; otherwise
// the enclosing block's lines are searched.
func locateDest(src, dest []byte, container ast.Node) (int, bool) {
	if len(dest) == 0 {
		return 0, false
	}
	if cap(dest) > 0 && cap(dest) <= cap(src) {
		off := cap(src) - cap(dest)
		if off+len(dest) <= len(src) && bytes.Equal(src[off:off+len(dest)], dest) {
			return off, true
		}
	}
	lo, hi := 0, len(src)
	if container != nil {
		blk := container
		for blk != nil && blk.Type() == ast.TypeInline {
			blk = blk.Parent()
		}
		if blk != nil && blk.Lines() != nil && blk.Lines().Len() > 0 {
			lo = blk.Lines().At(0).Start
			hi = blk.Lines().At(blk.Lines().Len() - 1).Stop
		}
	}
	region := src[lo:hi]
	for start := 0; start < len(region); {
		idx := bytes.Index(region[start:], dest)
		if idx < 0 {
			break
		}
		abs := lo + start + idx
		if abs > 0 {
			switch src[abs-1] {
			case '(', '<', ':', ' ', '\t':
				return abs, true
			}
		}
		start += idx + 1
	}
	return 0, false
}

// relativePath expresses target (a site path) relative to the directory
// fromDir, keeping a trailing slash on directory targets.
func relativePath(fromDir, target string) string {
	isDir := strings.HasSuffix(target, "/")
	from := splitSegments(fromDir)
	tgt := splitSegments(target)
	i := 0
	for i < len(from) && i < len(tgt) && from[i] == tgt[i] {
		i++
	}
	parts := make([]string, 0, len(from)-i+len(tgt)-i)
	for j := i; j < len(from); j++ {
		parts = append(parts, "..")
	}
	parts = append(parts, tgt[i:]...)
	out := strings.Join(parts, "/")
	if out == "" {
		return "./"
	}
	if isDir {
		out += "/"
	}
	return out
}

func splitSegments(p string) []string {
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}
	return segs
}
