package render

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fieldErrors asserts err is a *ValidationError and returns its errors.
func fieldErrors(t *testing.T, err error) []FieldError {
	t.Helper()
	require.Error(t, err)
	var verr *ValidationError
	require.True(t, errors.As(err, &verr), "expected *ValidationError, got %T: %v", err, err)
	require.NotEmpty(t, verr.Errors)
	return verr.Errors
}

// requireError asserts that a validation error mentioning substr exists at line.
func requireError(t *testing.T, err error, line int, substr string) {
	t.Helper()
	for _, fe := range fieldErrors(t, err) {
		if fe.Line == line && strings.Contains(fe.Message, substr) {
			return
		}
	}
	t.Fatalf("no error at line %d containing %q in: %v", line, substr, err)
}

func TestParseFrontmatterValid(t *testing.T) {
	src := "---\ntitle: Hello\ndescription: A page\ntags:\n  - a\n  - b\norder: 7\nextra:\n  owner: me\n  nested:\n    k: [1, 2]\n---\n# Body\n\ntext\n"
	meta, body, start, err := ParseFrontmatter(src)
	require.NoError(t, err)
	assert.True(t, meta.HasFrontmatter)
	assert.Equal(t, "Hello", meta.Title)
	assert.Equal(t, "A page", meta.Description)
	assert.Equal(t, []string{"a", "b"}, meta.Tags)
	require.NotNil(t, meta.Order)
	assert.Equal(t, 7, *meta.Order)
	assert.Equal(t, "me", meta.Extra["owner"])
	assert.Equal(t, "# Body\n\ntext\n", body)
	assert.Equal(t, 13, start)
}

func TestParseFrontmatterAbsent(t *testing.T) {
	meta, body, start, err := ParseFrontmatter("# Just markdown\n")
	require.NoError(t, err)
	assert.False(t, meta.HasFrontmatter)
	assert.Equal(t, "# Just markdown\n", body)
	assert.Equal(t, 1, start)
}

func TestParseFrontmatterEmptyBlock(t *testing.T) {
	meta, body, start, err := ParseFrontmatter("---\n---\nbody\n")
	require.NoError(t, err)
	assert.True(t, meta.HasFrontmatter)
	assert.Equal(t, "body\n", body)
	assert.Equal(t, 3, start)
}

func TestParseFrontmatterErrors(t *testing.T) {
	longTitle := strings.Repeat("x", 201)
	manyTags := "tags:\n" + strings.Repeat("  - t\n", 21)
	deep := "extra:\n  a:\n    b:\n      c:\n        d:\n          e:\n            f:\n              g:\n                h: 1\n"

	tests := []struct {
		name   string
		src    string
		line   int
		substr string
	}{
		{"unknown key", "---\ntitle: x\nauthor: me\n---\n", 3, `unknown frontmatter key "author"`},
		{"unknown key suggests extra", "---\nauthor: me\n---\n", 2, "extra:"},
		{"alias rejected", "---\ntitle: &t x\ndescription: *t\n---\n", 2, "anchors are not allowed"},
		{"alias node rejected", "---\ntitle: &t x\ndescription: *t\n---\n", 3, "aliases are not allowed"},
		{"duplicate key", "---\ntitle: a\ntitle: b\n---\n", 3, `duplicate key "title"`},
		{"oversize", "---\ndescription: " + strings.Repeat("y", 17*1024) + "\n---\n", 1, "frontmatter is"},
		{"title too long", "---\ntitle: " + longTitle + "\n---\n", 2, "title is 201 characters"},
		{"title not string", "---\ntitle:\n  - a\n---\n", 2, "title must be a string"},
		{"tags > 20", "---\n" + manyTags + "---\n", 2, "21 tags given"},
		{"tag too long", "---\ntags: [" + strings.Repeat("z", 65) + "]\n---\n", 2, "maximum is 64"},
		{"tags not list", "---\ntags: hello\n---\n", 2, "tags must be a list"},
		{"order non-int", "---\norder: soon\n---\n", 2, "order must be an integer"},
		{"order quoted", "---\norder: \"3\"\n---\n", 2, "order must be an integer"},
		{"order float", "---\norder: 3.5\n---\n", 2, "order must be an integer"},
		{"extra not mapping", "---\nextra: [1]\n---\n", 2, "extra must be a mapping"},
		{"too deep", "---\n" + deep + "---\n", 10, "nests deeper than 8"},
		{"not a mapping", "---\n- a\n- b\n---\n", 2, "must be a mapping"},
		{"unterminated", "---\ntitle: x\n", 1, "unterminated frontmatter"},
		{"invalid yaml", "---\ntitle: [unclosed\n---\n", 2, "invalid YAML"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := ParseFrontmatter(tc.src)
			requireError(t, err, tc.line, tc.substr)
		})
	}
}

func TestRenderPropagatesFrontmatterErrors(t *testing.T) {
	_, err := Render("---\nbogus: 1\n---\n# Hi\n", Options{})
	requireError(t, err, 2, "unknown frontmatter key")
}

func TestRenderMetaAndTitle(t *testing.T) {
	res, err := Render("---\ntitle: Front Title\n---\n# Front Title\n\nbody\n", Options{})
	require.NoError(t, err)
	assert.Equal(t, "Front Title", res.Title)
	assert.Equal(t, "Front Title", res.Meta.Title)
	assert.True(t, res.Meta.HasFrontmatter)
	assert.NotContains(t, res.HTML, "<h1", "leading H1 equal to title is removed")
}

func TestFrontmatterEmptyValuesAreAbsent(t *testing.T) {
	meta, body, _, err := ParseFrontmatter("---\ntitle:\ndescription:\n---\n\n# Heading\n")
	if err != nil {
		t.Fatalf("empty scalar values should be ignored: %v", err)
	}
	if meta.Title != "" || meta.Description != "" || body == "" {
		t.Fatalf("unexpected meta %+v body %q", meta, body)
	}
}
