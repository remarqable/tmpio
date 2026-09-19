package models

import (
	"net/url"
	"path"
	"strings"
	"unicode"

	"github.com/remarqable/tmpio/internal/platform/errors"
)

// Entry kinds.
const (
	KindDirectory = "directory"
	KindPage      = "page"
	KindConfig    = "config"
	KindAsset     = "asset"
	KindFile      = "file" // text file that is not a page: csv, json, yaml, code, ...
)

// ConfigPath is the single reserved site configuration file.
const ConfigPath = "/tmp.yaml"

// Path limits (specification section 4).
const (
	MaxPathBytes = 512
	MaxPathDepth = 16
)

// reservedRoot lists first path segments owned by the platform. A source or
// rendered path whose first segment matches one of these can never be created.
// IsReservedRoot reports whether a first path segment belongs to the platform.
// Anything it covers is answered by the application and can never be shadowed
// by tenant content or by an operator's static files.
func IsReservedRoot(seg string) bool { return reservedRoot[seg] }

var reservedRoot = map[string]bool{
	"s": true, "app": true, "auth": true, "oauth": true, "api": true, "mcp": true,
	".well-known": true, "static": true, "healthz": true, "readyz": true, "robots.txt": true,
	"sitemap.xml": true, "llms.txt": true, "search": true, "metrics": true, "login": true, "logout": true, "favicon.ico": true,
	// Owner operation verbs and the admin area (section 4 reservation, extended).
	"edit": true, "new": true, "history": true, "share": true, "move": true, "delete": true, "upload": true, "trash": true, "preview": true, "admin": true, "format": true,
}

// assetExtensions maps allowed binary asset extensions to their MIME types.
var assetExtensions = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp",
	".pdf": "application/pdf",
}

// fileExtensions maps text file extensions an AI commonly produces to the MIME
// type they are served with. They are stored as source text, versioned and
// searched like pages, and shown in a read-only viewer, never interpreted as markup.
var fileExtensions = map[string]string{
	".txt": "text/plain", ".log": "text/plain", ".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".json": "application/json", ".yaml": "application/yaml", ".yml": "application/yaml", ".toml": "application/toml",
	".xml": "application/xml", ".ini": "text/plain",
	".py": "text/x-python", ".js": "text/javascript", ".ts": "text/typescript", ".go": "text/x-go", ".rs": "text/x-rust",
	".rb": "text/x-ruby", ".sh": "text/x-shellscript", ".sql": "application/sql", ".css": "text/css",
}

// FileMIME returns the MIME type for a text file path, or "" if it is not a file kind.
func FileMIME(p string) string { return fileExtensions[path.Ext(p)] }

// IsImageMIME reports whether a MIME type is one of the raster image types.
func IsImageMIME(m string) bool { return m == "image/png" || m == "image/jpeg" || m == "image/webp" }

// PathInfo is a validated, normalized source path.
type PathInfo struct {
	Path     string // canonical source path, e.g. /research/circle.md
	Rendered string // rendered namespace path, e.g. /research/circle
	Kind     string // directory | page | config | asset
	Parent   string // parent directory path ("/" for root children, "" for root)
	Name     string // last segment
	Depth    int
	Ext      string
	MIME     string // for assets
	IsIndex  bool   // page named index.md
	IsRoot   bool
}

// ValidatePath validates a source path for a mutation. It rejects uppercase
// with a suggested canonical path rather than silently changing the name.
func ValidatePath(p string) (*PathInfo, error) {
	if p == "" {
		return nil, errors.New(errors.CodeInvalidPath, "path is required")
	}
	if len(p) > MaxPathBytes {
		return nil, errors.New(errors.CodeInvalidPath, "path exceeds 512 bytes")
	}
	if !strings.HasPrefix(p, "/") {
		return nil, errors.Newf(errors.CodeInvalidPath, "path must start with /; did you mean %q?", "/"+p)
	}
	for _, r := range p {
		if r == 0 || r < 0x20 || r == 0x7f || r == '\\' {
			return nil, errors.New(errors.CodeInvalidPath, "path contains control or backslash characters")
		}
	}
	if p == "/" {
		return &PathInfo{Path: "/", Rendered: "/", Kind: KindDirectory, Name: "", IsRoot: true}, nil
	}
	trimmed := strings.TrimSuffix(p, "/")
	segs := strings.Split(trimmed[1:], "/")
	if len(segs) > MaxPathDepth {
		return nil, errors.New(errors.CodeInvalidPath, "path exceeds 16 levels")
	}
	lower := strings.ToLower(p)
	if lower != p {
		ae := errors.Newf(errors.CodeInvalidPath, "path must be lowercase; canonical form is %q", lower)
		ae.SuggestedPath = lower
		return nil, ae
	}
	for i, s := range segs {
		if s == "" {
			return nil, errors.New(errors.CodeInvalidPath, "path contains an empty segment")
		}
		if s == "." || s == ".." {
			return nil, errors.New(errors.CodeInvalidPath, "path contains a traversal segment")
		}
		last := i == len(segs)-1
		if !last {
			if !validSegment(s, false) {
				return nil, errors.Newf(errors.CodeInvalidPath, "directory name %q may only use a-z, 0-9, hyphen and underscore", s)
			}
		}
	}
	name := segs[len(segs)-1]
	info := &PathInfo{Name: name, Depth: len(segs)}
	if len(segs) == 1 {
		info.Parent = "/"
	} else {
		info.Parent = "/" + strings.Join(segs[:len(segs)-1], "/")
	}
	ext := path.Ext(name)
	switch {
	case strings.HasSuffix(p, "/"):
		if !validSegment(name, false) {
			return nil, errors.Newf(errors.CodeInvalidPath, "directory name %q may only use a-z, 0-9, hyphen and underscore", name)
		}
		info.Kind = KindDirectory
		info.Path = "/" + strings.Join(segs, "/")
		info.Rendered = info.Path
	case ext == ".md":
		base := strings.TrimSuffix(name, ".md")
		if !validSegment(base, false) {
			return nil, errors.Newf(errors.CodeInvalidPath, "file name %q may only use a-z, 0-9, hyphen and underscore before .md", name)
		}
		info.Kind = KindPage
		info.Ext = ".md"
		info.Path = p
		info.IsIndex = base == "index"
		info.Rendered = strings.TrimSuffix(p, ".md")
	case p == ConfigPath:
		info.Kind = KindConfig
		info.Ext = ".yaml"
		info.Path = p
		info.Rendered = p
	default:
		if mime, ok := fileExtensions[ext]; ok {
			base := strings.TrimSuffix(name, ext)
			if !validSegment(base, true) || strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".") {
				return nil, errors.Newf(errors.CodeInvalidPath, "file name %q may only use a-z, 0-9, hyphen, underscore and dots", name)
			}
			info.Kind = KindFile
			info.Ext = ext
			info.MIME = mime
			info.Path = p
			info.Rendered = p
		} else if mime, ok := assetExtensions[ext]; ok {
			base := strings.TrimSuffix(name, ext)
			if !validSegment(base, false) {
				return nil, errors.Newf(errors.CodeInvalidPath, "asset name %q may only use a-z, 0-9, hyphen and underscore", name)
			}
			info.Kind = KindAsset
			info.Ext = ext
			info.MIME = mime
			info.Path = p
			info.Rendered = p
		} else if ext == "" {
			if !validSegment(name, false) {
				return nil, errors.Newf(errors.CodeInvalidPath, "name %q may only use a-z, 0-9, hyphen and underscore", name)
			}
			// A bare name is a directory. Write APIs require explicit extensions for files.
			info.Kind = KindDirectory
			info.Path = p
			info.Rendered = p
		} else {
			return nil, errors.Newf(errors.CodeInvalidPath, "unsupported file extension %q (pages: .md; files: .txt .csv .json .yaml .toml .xml and common code files; assets: .png .jpg .jpeg .webp .pdf)", ext)
		}
	}
	first := segs[0]
	firstRendered := strings.SplitN(strings.TrimPrefix(info.Rendered, "/"), "/", 2)[0]
	if reservedRoot[first] || reservedRoot[firstRendered] || strings.HasPrefix(strings.ToLower(first), "o:") {
		return nil, errors.Newf(errors.CodeInvalidPath, "%q is reserved by the platform", first)
	}
	if info.Kind != KindConfig && first == "tmp.yaml" {
		return nil, errors.New(errors.CodeInvalidPath, "tmp.yaml is reserved")
	}
	return info, nil
}

func validSegment(s string, allowDot bool) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		case r == '.' && allowDot:
		default:
			return false
		}
	}
	return true
}

// Ancestors returns every directory path above p, from "/" downward, excluding p itself.
func Ancestors(p string) []string {
	if p == "/" {
		return nil
	}
	out := []string{"/"}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i := 1; i < len(segs); i++ {
		out = append(out, "/"+strings.Join(segs[:i], "/"))
	}
	return out
}

// RequestPath decodes a request URL path exactly once and rejects the tricks
// section 4 enumerates. It returns the decoded path with any trailing slash
// preserved. It does not lowercase.
func RequestPath(escaped string) (string, error) {
	if escaped == "" {
		return "/", nil
	}
	up := strings.ToUpper(escaped)
	for _, bad := range []string{"%2F", "%5C", "%00", "%2E%2E", "%25"} {
		if strings.Contains(up, bad) {
			return "", errors.New(errors.CodeInvalidPath, "encoded separators or traversal are not allowed")
		}
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return "", errors.New(errors.CodeInvalidPath, "malformed path encoding")
	}
	if len(decoded) > MaxPathBytes+64 {
		return "", errors.New(errors.CodeInvalidPath, "path too long")
	}
	for _, r := range decoded {
		if r == 0 || r < 0x20 || r == 0x7f || r == '\\' || unicode.Is(unicode.Cf, r) {
			return "", errors.New(errors.CodeInvalidPath, "path contains disallowed characters")
		}
	}
	if !strings.HasPrefix(decoded, "/") {
		decoded = "/" + decoded
	}
	segs := strings.Split(decoded[1:], "/")
	for i, s := range segs {
		if s == "." || s == ".." {
			return "", errors.New(errors.CodeInvalidPath, "path contains a traversal segment")
		}
		if s == "" && i != len(segs)-1 {
			return "", errors.New(errors.CodeInvalidPath, "path contains an empty segment")
		}
	}
	return decoded, nil
}

// SplitOrgPrefix recognizes a leading /o:{code} segment case-insensitively.
// It returns the uppercase org code, the remainder path (starting with /) and
// whether a prefix was present. An /o: prefix with an invalid code returns
// ok=true with an empty code so callers 404 rather than fall back.
func SplitOrgPrefix(p string) (code string, rest string, ok bool) {
	if len(p) < 3 || p[0] != '/' {
		return "", p, false
	}
	seg := p[1:]
	end := strings.IndexByte(seg, '/')
	if end == -1 {
		end = len(seg)
	}
	first := seg[:end]
	if len(first) < 2 || !strings.EqualFold(first[:2], "o:") {
		return "", p, false
	}
	code = strings.ToUpper(first[2:])
	rest = seg[end:]
	if rest == "" {
		rest = "/"
	}
	if !ValidOrgCode(code) {
		return "", rest, true
	}
	return code, rest, true
}

// ValidOrgCode reports whether s is an eight-character Crockford Base32 code.
func ValidOrgCode(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(crockford, r) {
			return false
		}
	}
	return true
}

// RenderedToCandidates maps a rendered request path to the source paths that
// could serve it, in priority order. "/research/circle" → page /research/circle.md;
// "/research/" → page /research/index.md then directory /research; "/" → /index.md, then root.
func RenderedToCandidates(rendered string) (pagePath string, dirPath string, isDirURL bool) {
	if rendered == "/" {
		return "/index.md", "/", true
	}
	if strings.HasSuffix(rendered, "/") {
		d := strings.TrimSuffix(rendered, "/")
		return d + "/index.md", d, true
	}
	return rendered + ".md", rendered, false
}
