package models

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidUUID reports whether s is a canonical UUID string.
func ValidUUID(s string) bool { return uuidRe.MatchString(s) }

// ValidUTF8 reports whether s is valid UTF-8 without NUL bytes.
func ValidUTF8(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return false
		}
	}
	return true
}

// cursor is an opaque, signed pagination token bound to a query shape.
type cursor struct {
	Scope string  `json:"s"`
	After string  `json:"a,omitempty"`
	Rank  float64 `json:"r,omitempty"`
	ID    int64   `json:"i,omitempty"`
}

func (o *Ops) encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, o.CursorKey)
	mac.Write(b)
	sig := mac.Sum(nil)[:16]
	return base64.RawURLEncoding.EncodeToString(append(sig, b...))
}

func (o *Ops) decodeCursor(s, scope string) (*cursor, bool) {
	if s == "" {
		return nil, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) < 17 {
		return nil, false
	}
	sig, b := raw[:16], raw[16:]
	mac := hmac.New(sha256.New, o.CursorKey)
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)[:16]) {
		return nil, false
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Scope != scope {
		return nil, false
	}
	return &c, true
}
