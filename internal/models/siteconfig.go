package models

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/remarqable/tmpio/internal/platform/errors"
)

// NavItem is one primary navigation link.
type NavItem struct {
	Title string `yaml:"title" json:"title"`
	Path  string `yaml:"path" json:"path"`
}

// SiteConfig is the validated schema of /tmp.yaml (section 5).
type SiteConfig struct {
	Version     int       `yaml:"version" json:"version"`
	Name        string    `yaml:"name,omitempty" json:"name,omitempty"`
	Description string    `yaml:"description,omitempty" json:"description,omitempty"`
	Theme       string    `yaml:"theme" json:"theme"`
	Appearance  string    `yaml:"appearance" json:"appearance"`
	Navigation  []NavItem `yaml:"navigation" json:"navigation"`
	Sidebar     struct {
		Auto bool `yaml:"auto" json:"auto"`
	} `yaml:"sidebar" json:"sidebar"`
	Features struct {
		Search bool `yaml:"search" json:"search"`
		TOC    bool `yaml:"toc" json:"toc"`
	} `yaml:"features" json:"features"`
}

// DefaultSiteConfigYAML is written at signup.
const DefaultSiteConfigYAML = `version: 1
theme: docs
appearance: dark
navigation: []
sidebar:
  auto: true
features:
  search: true
  toc: true
`

// DefaultIndexMarkdown is the first page of a new site.
const DefaultIndexMarkdown = `---
title: Welcome
description: Your private knowledge site
---

# Welcome

This is your site. Nobody but you can see it until you create a sharing link for a document.

:::callout type="tip"
Connect your AI from the dashboard, then ask it to create a page, for example ` + "`/research/circle.md`" + `. Read any existing page before changing it.
:::

Edit this page from **Content** in the dashboard, or ask your connected AI to replace it.
`

// DefaultSiteConfig returns the parsed default.
func DefaultSiteConfig() *SiteConfig {
	cfg, _ := ParseSiteConfig(DefaultSiteConfigYAML)
	return cfg
}

// ParseSiteConfig strictly parses and validates /tmp.yaml source.
func ParseSiteConfig(src string) (*SiteConfig, error) {
	if len(src) > 32*1024 {
		return nil, errors.New(errors.CodeTooLarge, "tmp.yaml exceeds 32 KiB")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		return nil, errors.Wrap(errors.CodeValidationFailed, "tmp.yaml: "+err.Error(), err)
	}
	if err := checkYAMLNode(&root, 0); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(strings.NewReader(src))
	dec.KnownFields(true)
	// Defaults apply when a key is absent; the file may still set them to false.
	var cfg SiteConfig
	cfg.Sidebar.Auto = true
	cfg.Features.Search = true
	cfg.Features.TOC = true
	if err := dec.Decode(&cfg); err != nil {
		if strings.Contains(err.Error(), "EOF") {
			return nil, errors.New(errors.CodeValidationFailed, "tmp.yaml is empty")
		}
		return nil, errors.Wrap(errors.CodeValidationFailed, "tmp.yaml: "+err.Error(), err)
	}
	ae := errors.New(errors.CodeValidationFailed, "tmp.yaml is invalid")
	if cfg.Version != 1 {
		ae.WithField("version", 0, "version must be 1")
	}
	switch cfg.Theme {
	case "docs", "editorial":
	case "":
		cfg.Theme = "docs"
	default:
		ae.WithField("theme", 0, fmt.Sprintf("theme %q is not supported (docs, editorial)", cfg.Theme))
	}
	switch cfg.Appearance {
	case "light", "dark", "system":
	case "":
		cfg.Appearance = "dark"
	default:
		ae.WithField("appearance", 0, fmt.Sprintf("appearance %q is not supported (light, dark, system)", cfg.Appearance))
	}
	if len(cfg.Name) > 120 {
		ae.WithField("name", 0, "name must be 120 characters or fewer")
	}
	if len(cfg.Description) > 500 {
		ae.WithField("description", 0, "description must be 500 characters or fewer")
	}
	if len(cfg.Navigation) > 12 {
		ae.WithField("navigation", 0, "navigation may have at most 12 links")
	}
	for i, n := range cfg.Navigation {
		field := fmt.Sprintf("navigation[%d]", i)
		if strings.TrimSpace(n.Title) == "" || len(n.Title) > 80 {
			ae.WithField(field+".title", 0, "title is required (max 80 characters)")
		}
		if !strings.HasPrefix(n.Path, "/") || strings.ContainsAny(n.Path, " \t\n") || strings.Contains(n.Path, "://") || strings.HasPrefix(n.Path, "//") {
			ae.WithField(field+".path", 0, "path must be a site-local path starting with /")
		}
		if _, err := RequestPath(n.Path); err != nil {
			ae.WithField(field+".path", 0, "path is not valid")
		}
	}
	if len(ae.FieldErrors) > 0 {
		return nil, ae
	}
	return &cfg, nil
}

// checkYAMLNode rejects aliases, anchors, excessive depth and duplicate keys.
func checkYAMLNode(n *yaml.Node, depth int) error {
	if depth > 8 {
		return errors.New(errors.CodeValidationFailed, "tmp.yaml nesting is too deep")
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.Newf(errors.CodeValidationFailed, "tmp.yaml: YAML anchors and aliases are not allowed (line %d)", n.Line)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				return errors.Newf(errors.CodeValidationFailed, "tmp.yaml: duplicate key %q (line %d)", k, n.Content[i].Line)
			}
			seen[k] = true
		}
	}
	for _, c := range n.Content {
		if err := checkYAMLNode(c, depth+1); err != nil {
			return err
		}
	}
	return nil
}
