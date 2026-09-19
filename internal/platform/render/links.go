package render

import (
	"errors"
	stdhtml "html"
	"path"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// relExternal is the rel attribute placed on every external link.
var relExternal = []byte("noopener noreferrer")

var errEscapesRoot = errors.New("path escapes the site root")

// schemeOf reports the lower-cased URL scheme of dest, if any. Whitespace and
// control characters are removed first, mirroring browser URL parsing, so
// "java\tscript:" and " javascript:" are both detected.
func schemeOf(dest string) (string, bool) {
	var b strings.Builder
	for _, r := range dest {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	clean := b.String()
	for i := 0; i < len(clean); i++ {
		c := clean[i]
		switch {
		case c == ':':
			if i == 0 {
				return "", false
			}
			return strings.ToLower(clean[:i]), true
		case c == '/' || c == '?' || c == '#':
			return "", false
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return "", false
		}
	}
	return "", false
}

// resolveSitePath resolves ref (an absolute or relative site reference without
// query or fragment) against the directory of sourcePath. Directory references
// keep their trailing slash. References climbing above the root are rejected.
func resolveSitePath(sourcePath, ref string) (string, error) {
	base := "/"
	if sourcePath != "" {
		base = path.Dir(sourcePath)
		if !strings.HasPrefix(base, "/") {
			base = "/" + strings.TrimPrefix(base, ".")
		}
	}
	full := ref
	if !strings.HasPrefix(ref, "/") {
		full = strings.TrimSuffix(base, "/") + "/" + ref
	}
	isDir := strings.HasSuffix(full, "/") || strings.HasSuffix(full, "/.") || strings.HasSuffix(full, "/..")
	var segs []string
	for _, seg := range strings.Split(full, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(segs) == 0 {
				return "", errEscapesRoot
			}
			segs = segs[:len(segs)-1]
		default:
			segs = append(segs, seg)
		}
	}
	out := "/" + strings.Join(segs, "/")
	if isDir && len(segs) > 0 {
		out += "/"
	}
	return out, nil
}

// renderedPath converts a site path to its rendered URL form: ".md" is
// dropped and "index.md" becomes its directory.
func renderedPath(sitePath string) string {
	switch {
	case sitePath == "/index.md":
		return "/"
	case strings.HasSuffix(sitePath, "/index.md"):
		return strings.TrimSuffix(sitePath, "index.md")
	case strings.HasSuffix(sitePath, ".md"):
		return strings.TrimSuffix(sitePath, ".md")
	}
	return sitePath
}

// splitRef separates a link destination into path, query and fragment parts.
func splitRef(dest string) (p, query, frag string, hasQuery bool) {
	p, frag, _ = strings.Cut(dest, "#")
	p, query, hasQuery = strings.Cut(p, "?")
	return p, query, frag, hasQuery
}

// resolveLinkDest validates and rewrites a link destination. ok is false when
// a validation error was recorded.
func (s *renderState) resolveLinkDest(dest string, line int) (newDest string, external, ok bool) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return dest, false, true
	}
	if scheme, has := schemeOf(dest); has {
		switch scheme {
		case "https", "http", "mailto":
			return dest, true, true
		}
		s.errorf("link", line, "unsupported URL scheme %q; only https:, http: and mailto: are allowed", scheme)
		return "", false, false
	}
	if strings.HasPrefix(dest, "#") {
		return dest, false, true
	}
	p, _, frag, hasQuery := splitRef(dest)
	if hasQuery {
		s.warnf(line, "query string removed from internal link %q", dest)
	}
	sitePath := s.opts.SourcePath
	if p != "" {
		var err error
		if sitePath, err = resolveSitePath(s.opts.SourcePath, p); err != nil {
			s.errorf("link", line, "link %q escapes the site root", dest)
			return "", false, false
		}
	}
	if s.opts.ResolveLink != nil && !s.opts.ResolveLink(sitePath) {
		s.warnf(line, "broken internal link: %s", sitePath)
	}
	out := s.opts.Prefix + renderedPath(sitePath)
	if frag != "" {
		out += "#" + frag
	}
	return out, false, true
}

// processLink rewrites a Markdown link node in place.
func (s *renderState) processLink(n *ast.Link, f *fragment) {
	newDest, external, ok := s.resolveLinkDest(string(n.Destination), f.lineOf(n))
	if !ok {
		return
	}
	n.Destination = []byte(newDest)
	if external {
		n.SetAttributeString("rel", relExternal)
	}
}

// processImage validates an image and resolves local sources through
// Options.ResolveAsset. It returns a placeholder node when the asset is
// unavailable, or nil when the image node should be kept.
func (s *renderState) processImage(n *ast.Image, f *fragment) ast.Node {
	line := f.lineOf(n)
	dest := strings.TrimSpace(string(n.Destination))
	if scheme, has := schemeOf(dest); has {
		switch scheme {
		case "https":
			return nil
		case "http":
			s.errorf("image", line, "insecure image URL %q; images must use https:", dest)
		default:
			s.errorf("image", line, "unsupported image URL scheme %q; only https: and local paths are allowed", scheme)
		}
		return nil
	}
	p, _, _, _ := splitRef(dest)
	sitePath, err := resolveSitePath(s.opts.SourcePath, p)
	if err != nil || p == "" {
		s.errorf("image", line, "image %q escapes the site root", dest)
		return nil
	}
	if !s.assetSeen[sitePath] {
		s.assetSeen[sitePath] = true
		s.assets = append(s.assets, sitePath)
	}
	if s.opts.ResolveAsset == nil {
		n.Destination = []byte(sitePath)
		return nil
	}
	assetURL, ok := s.opts.ResolveAsset(sitePath)
	if !ok {
		s.warnf(line, "image unavailable: %s", sitePath)
		return &rawOutput{html: `<span class="tmp-asset-unavailable">Image unavailable: ` +
			stdhtml.EscapeString(path.Base(sitePath)) + `</span>`}
	}
	n.Destination = []byte(assetURL)
	return nil
}

// ExtractLocalImagePaths returns the resolved site paths of every local
// (scheme-less) image referenced by source, in document order and without
// duplicates. sourcePath is the page's own site path.
func ExtractLocalImagePaths(source string, sourcePath string) ([]string, error) {
	_, body, bodyStart, err := ParseFrontmatter(source)
	if err != nil {
		return nil, err
	}
	src := []byte(body)
	f := newFragment(src, bodyStart-1)
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(parser.NewContext()))
	var out []string
	seen := map[string]bool{}
	var verr ValidationError
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		img, ok := n.(*ast.Image)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		dest := strings.TrimSpace(string(img.Destination))
		if _, has := schemeOf(dest); has {
			return ast.WalkContinue, nil
		}
		p, _, _, _ := splitRef(dest)
		sitePath, err := resolveSitePath(sourcePath, p)
		if err != nil || p == "" {
			verr.Errors = append(verr.Errors, FieldError{Field: "image", Line: f.lineOf(n), Message: "image \"" + dest + "\" escapes the site root"})
			return ast.WalkContinue, nil
		}
		if !seen[sitePath] {
			seen[sitePath] = true
			out = append(out, sitePath)
		}
		return ast.WalkContinue, nil
	})
	if len(verr.Errors) > 0 {
		return nil, &verr
	}
	return out, nil
}
