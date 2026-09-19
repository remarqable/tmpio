// Package i18n loads JSON string catalogs. English ships first; every
// user-visible UI string lives in lang/<lang>.json.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed all:catalog
var catalogFS embed.FS

var (
	mu           sync.RWMutex
	translations = map[string]map[string]string{}
	loadEnglish  sync.Once
)

// Preload parses a language catalog. Missing catalogs are not an error.
func Preload(lang string) error {
	data, err := catalogFS.ReadFile("catalog/" + lang + ".json")
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("i18n %s: %w", lang, err)
	}
	mu.Lock()
	translations[lang] = m
	mu.Unlock()
	return nil
}

// T translates key for lang with {name} substitutions, falling back to English then the key.
func T(lang, key string, vars ...string) string {
	// The embedded English catalog is the fallback for every caller, including
	// model code exercised outside the server process (tests, tools).
	loadEnglish.Do(func() { _ = Preload("en") })
	mu.RLock()
	text, ok := translations[lang][key]
	if !ok {
		text, ok = translations["en"][key]
	}
	mu.RUnlock()
	if !ok {
		text = key
	}
	for i := 0; i+1 < len(vars); i += 2 {
		text = strings.ReplaceAll(text, "{"+vars[i]+"}", vars[i+1])
	}
	return text
}

// Keys returns all keys of a catalog (tests).
func Keys(lang string) []string {
	mu.RLock()
	defer mu.RUnlock()
	var out []string
	for k := range translations[lang] {
		out = append(out, k)
	}
	return out
}
