package render

import (
	"fmt"
	"strings"
)

// DefaultMaxBytes is the input size limit used when Options.MaxBytes is zero.
const DefaultMaxBytes = 256 * 1024

// Warning is a non-fatal finding recorded while rendering.
type Warning struct {
	Line    int
	Message string
}

// TOCEntry is a level-2 or level-3 heading in document order.
type TOCEntry struct {
	Level int
	ID    string
	Text  string
}

// Meta holds the validated frontmatter of a page.
type Meta struct {
	Title          string
	Description    string
	Tags           []string
	Order          *int
	Extra          map[string]any
	HasFrontmatter bool
}

// Result is the output of a successful Render.
type Result struct {
	HTML        string
	Title       string
	Meta        Meta
	TOC         []TOCEntry
	PlainText   string
	Warnings    []Warning
	LocalAssets []string // site paths of local images referenced
}

// FieldError describes a single validation failure. Line is 1-based and
// refers to the original source (including frontmatter); 0 means unknown.
type FieldError struct {
	Field   string
	Line    int
	Message string
}

// ValidationError is returned by Render and friends when the input is invalid.
type ValidationError struct {
	Errors []FieldError
}

// Error implements error.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, fe := range e.Errors {
		if fe.Line > 0 {
			parts = append(parts, fmt.Sprintf("line %d: %s: %s", fe.Line, fe.Field, fe.Message))
		} else {
			parts = append(parts, fmt.Sprintf("%s: %s", fe.Field, fe.Message))
		}
	}
	return strings.Join(parts, "; ")
}

// Options controls how a page is rendered.
type Options struct {
	// SourcePath is the site path of the page being rendered, e.g.
	// "/research/circle.md". Relative links resolve against its directory.
	SourcePath string
	// FilenameFallbackTitle is used when neither frontmatter nor an H1
	// provides a title.
	FilenameFallbackTitle string
	// Prefix is prepended to every rendered internal link: "" for personal
	// pages or an explicit address form such as "/o:XS67DF65".
	Prefix string
	// ResolveAsset maps a local image site path to a servable URL. When it
	// returns ok=false the image is replaced by a placeholder and a Warning
	// is recorded. When nil, the site path itself is used as the src.
	ResolveAsset func(sitePath string) (assetURL string, ok bool)
	// ResolveLink, when set, is consulted for every internal link; a false
	// result records a "broken internal link" Warning.
	ResolveLink func(sitePath string) (exists bool)
	// MaxBytes bounds the input size; zero means DefaultMaxBytes.
	MaxBytes int
}
