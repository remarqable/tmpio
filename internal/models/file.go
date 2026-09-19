package models

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/remarqable/tmpio/internal/platform/errors"
)

// ValidateFileContent checks a text file before it is stored. Structured
// formats must parse so an agent learns about a broken file at write time.
func ValidateFileContent(info *PathInfo, content string) error {
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return errors.New(errors.CodeValidationFailed, "file content must be valid UTF-8 text")
	}
	switch info.Ext {
	case ".json":
		var v any
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return errors.New(errors.CodeValidationFailed, "invalid JSON: "+err.Error())
		}
	case ".yaml", ".yml":
		var v any
		if err := yaml.Unmarshal([]byte(content), &v); err != nil {
			return errors.New(errors.CodeValidationFailed, "invalid YAML: "+err.Error())
		}
	}
	return nil
}

// filePlainText is the searchable text of a file, bounded so the index stays small.
func filePlainText(content string) string {
	if len(content) > 64*1024 {
		content = content[:64*1024]
	}
	return strings.Join(strings.Fields(content), " ")
}
