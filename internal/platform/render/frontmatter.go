package render

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxFrontmatterBytes = 16 * 1024
	maxFrontmatterDepth = 8
	maxTitleLen         = 200
	maxDescriptionLen   = 500
	maxTags             = 20
	maxTagLen           = 64
)

var yamlLineRe = regexp.MustCompile(`line (\d+):`)

// ParseFrontmatter splits an optional leading YAML frontmatter block
// (delimited by "---" lines) from source. It returns the validated metadata,
// the remaining body (byte-for-byte), the 1-based line number on which the
// body starts, and a *ValidationError on invalid frontmatter.
func ParseFrontmatter(source string) (meta Meta, body string, bodyStartLine int, err error) {
	firstLine, _, _ := strings.Cut(source, "\n")
	if strings.TrimRight(firstLine, "\r") != "---" {
		return Meta{}, source, 1, nil
	}
	meta.HasFrontmatter = true

	// Walk lines by byte offset so the body is preserved exactly.
	pos := len(firstLine) + 1 // start of line 2
	yamlStart := pos
	lineNo := 2
	for pos <= len(source) {
		end := strings.IndexByte(source[pos:], '\n')
		var line string
		next := len(source)
		if end >= 0 {
			line = source[pos : pos+end]
			next = pos + end + 1
		} else {
			line = source[pos:]
		}
		if strings.TrimRight(line, "\r") == "---" {
			yamlText := source[yamlStart:pos]
			if len(yamlText) > maxFrontmatterBytes {
				return meta, "", 0, &ValidationError{Errors: []FieldError{{
					Field: "frontmatter", Line: 1,
					Message: fmt.Sprintf("frontmatter is %d bytes; the maximum is %d", len(yamlText), maxFrontmatterBytes),
				}}}
			}
			if verr := parseFrontmatterYAML(yamlText, &meta); verr != nil {
				return meta, "", 0, verr
			}
			return meta, source[next:], lineNo + 1, nil
		}
		if end < 0 {
			break
		}
		pos = next
		lineNo++
	}
	return meta, "", 0, &ValidationError{Errors: []FieldError{{
		Field: "frontmatter", Line: 1, Message: "unterminated frontmatter: missing closing ---",
	}}}
}

// parseFrontmatterYAML validates yamlText and fills meta. Line numbers are
// shifted by one to account for the opening "---" line.
func parseFrontmatterYAML(yamlText string, meta *Meta) *ValidationError {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &root); err != nil {
		line := 1
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			n, _ := strconv.Atoi(m[1])
			line = n + 1
		}
		return &ValidationError{Errors: []FieldError{{Field: "frontmatter", Line: line, Message: "invalid YAML: " + err.Error()}}}
	}
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil // empty frontmatter
	}
	top := root.Content[0]
	var errs []FieldError
	checkYAMLNode(top, 1, &errs)
	if top.Kind != yaml.MappingNode {
		errs = append(errs, FieldError{Field: "frontmatter", Line: top.Line + 1, Message: "frontmatter must be a mapping of keys to values"})
		return &ValidationError{Errors: errs}
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i], top.Content[i+1]
		line := key.Line + 1
		field := "frontmatter." + key.Value
		if val.Kind == yaml.ScalarNode && val.Tag == "!!null" && key.Value != "extra" {
			continue // "title:" with no value means no title
		}
		switch key.Value {
		case "title":
			if s, ok := scalarString(val); !ok {
				errs = append(errs, FieldError{Field: field, Line: line, Message: "title must be a string"})
			} else if utf8.RuneCountInString(s) > maxTitleLen {
				errs = append(errs, FieldError{Field: field, Line: line, Message: fmt.Sprintf("title is %d characters; the maximum is %d", utf8.RuneCountInString(s), maxTitleLen)})
			} else {
				meta.Title = s
			}
		case "description":
			if s, ok := scalarString(val); !ok {
				errs = append(errs, FieldError{Field: field, Line: line, Message: "description must be a string"})
			} else if utf8.RuneCountInString(s) > maxDescriptionLen {
				errs = append(errs, FieldError{Field: field, Line: line, Message: fmt.Sprintf("description is %d characters; the maximum is %d", utf8.RuneCountInString(s), maxDescriptionLen)})
			} else {
				meta.Description = s
			}
		case "tags":
			meta.Tags = parseTags(val, field, line, &errs)
		case "order":
			var n int
			if val.Kind != yaml.ScalarNode || val.Tag != "!!int" || val.Decode(&n) != nil {
				errs = append(errs, FieldError{Field: field, Line: line, Message: "order must be an integer"})
			} else {
				meta.Order = &n
			}
		case "extra":
			if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
				continue
			}
			if val.Kind != yaml.MappingNode {
				errs = append(errs, FieldError{Field: field, Line: line, Message: "extra must be a mapping"})
				continue
			}
			m := map[string]any{}
			if err := val.Decode(&m); err != nil {
				errs = append(errs, FieldError{Field: field, Line: line, Message: "invalid extra mapping: " + err.Error()})
				continue
			}
			meta.Extra = m
		default:
			errs = append(errs, FieldError{Field: field, Line: line, Message: fmt.Sprintf("unknown frontmatter key %q; custom fields belong under extra:", key.Value)})
		}
	}
	if len(errs) > 0 {
		return &ValidationError{Errors: errs}
	}
	return nil
}

// checkYAMLNode rejects anchors, aliases, duplicate keys and excessive depth.
func checkYAMLNode(n *yaml.Node, depth int, errs *[]FieldError) {
	line := n.Line + 1
	if n.Anchor != "" {
		*errs = append(*errs, FieldError{Field: "frontmatter", Line: line, Message: fmt.Sprintf("YAML anchors are not allowed (&%s)", n.Anchor)})
	}
	if n.Kind == yaml.AliasNode {
		*errs = append(*errs, FieldError{Field: "frontmatter", Line: line, Message: fmt.Sprintf("YAML aliases are not allowed (*%s)", n.Value)})
		return
	}
	if depth > maxFrontmatterDepth {
		*errs = append(*errs, FieldError{Field: "frontmatter", Line: line, Message: fmt.Sprintf("frontmatter nests deeper than %d levels", maxFrontmatterDepth)})
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if prev, dup := seen[key.Value]; dup {
				*errs = append(*errs, FieldError{Field: "frontmatter", Line: key.Line + 1, Message: fmt.Sprintf("duplicate key %q (already defined on line %d)", key.Value, prev)})
			} else {
				seen[key.Value] = key.Line + 1
			}
			checkYAMLNode(key, depth+1, errs)
			checkYAMLNode(val, depth+1, errs)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			checkYAMLNode(c, depth+1, errs)
		}
	}
}

// scalarString returns the string value of a non-null scalar node.
func scalarString(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return "", false
	}
	return n.Value, true
}

func parseTags(val *yaml.Node, field string, line int, errs *[]FieldError) []string {
	if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
		return nil
	}
	if val.Kind != yaml.SequenceNode {
		*errs = append(*errs, FieldError{Field: field, Line: line, Message: "tags must be a list of strings"})
		return nil
	}
	if len(val.Content) > maxTags {
		*errs = append(*errs, FieldError{Field: field, Line: line, Message: fmt.Sprintf("%d tags given; the maximum is %d", len(val.Content), maxTags)})
		return nil
	}
	tags := make([]string, 0, len(val.Content))
	for _, item := range val.Content {
		s, ok := scalarString(item)
		if !ok {
			*errs = append(*errs, FieldError{Field: field, Line: item.Line + 1, Message: "each tag must be a string"})
			continue
		}
		if len(s) > maxTagLen {
			*errs = append(*errs, FieldError{Field: field, Line: item.Line + 1, Message: fmt.Sprintf("tag %q is %d characters; the maximum is %d", s, len(s), maxTagLen)})
			continue
		}
		tags = append(tags, s)
	}
	return tags
}
