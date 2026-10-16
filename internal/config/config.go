// Package config loads narc's .narc.yml configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/huyth45/narc/internal/core"
)

// FileNames are the config file names narc looks for, in order.
var FileNames = []string{".narc.yml", ".narc.yaml", "narc.yml", "narc.yaml"}

// Allow is a config-level waiver.
type Allow struct {
	Rule   string `yaml:"rule"`
	Path   string `yaml:"path"`
	Reason string `yaml:"reason"`
}

// Hook configures agent-hook behaviour.
type Hook struct {
	// BlockOn is the minimum severity that blocks an agent action.
	BlockOn string `yaml:"block_on"`
	// MaxBlocks is how many times narc will refuse to let an agent stop
	// in one session before letting it go (and warning the human).
	MaxBlocks int `yaml:"max_blocks"`
}

// Config is the parsed .narc.yml.
type Config struct {
	Version   int               `yaml:"version"`
	FailOn    string            `yaml:"fail_on"`
	Ignore    []string          `yaml:"ignore"`
	Protected []string          `yaml:"protected"`
	Tests     []string          `yaml:"tests"`
	Rules     map[string]string `yaml:"rules"`
	Allow     []Allow           `yaml:"allow"`
	Hook      Hook              `yaml:"hook"`

	// Path is the file the config was loaded from ("" for defaults).
	Path string `yaml:"-"`

	severities map[string]core.Severity
}

// Default returns the built-in configuration.
func Default() *Config {
	c := &Config{Version: 1, FailOn: "error", Hook: Hook{BlockOn: "error", MaxBlocks: 3}}
	_ = c.validate()
	return c
}

// Load finds and parses the config in dir (usually the repository root).
// A missing config yields Default().
func Load(dir string) (*Config, error) {
	for _, n := range FileNames {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return LoadFile(p)
		}
	}
	return Default(), nil
}

// LoadFile parses a specific config file.
func LoadFile(p string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	c, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	c.Path = p
	return c, nil
}

// Parse parses config YAML.
func Parse(b []byte) (*Config, error) {
	c := Default()
	if len(bytes.TrimSpace(b)) == 0 {
		return c, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	if c.FailOn == "" {
		c.FailOn = "error"
	}
	if c.FailOn != "never" {
		if _, err := core.ParseSeverity(c.FailOn); err != nil {
			return fmt.Errorf("fail_on: %w", err)
		}
	}
	if c.Hook.BlockOn == "" {
		c.Hook.BlockOn = "error"
	}
	if _, err := core.ParseSeverity(c.Hook.BlockOn); err != nil {
		return fmt.Errorf("hook.block_on: %w", err)
	}
	if c.Hook.MaxBlocks <= 0 {
		c.Hook.MaxBlocks = 3
	}
	c.severities = map[string]core.Severity{}
	var errs []string
	for id, v := range c.Rules {
		s, err := core.ParseSeverity(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("rules.%s: %v", id, err))
			continue
		}
		c.severities[id] = s
	}
	for i, a := range c.Allow {
		if a.Rule == "" {
			errs = append(errs, fmt.Sprintf("allow[%d]: rule is required (use \"*\" for all rules)", i))
		}
		if strings.TrimSpace(a.Reason) == "" {
			errs = append(errs, fmt.Sprintf("allow[%d]: reason is required — say why this is acceptable", i))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// UnknownRules returns rule IDs referenced in the config that are not
// registered, so callers can warn about typos.
func (c *Config) UnknownRules() []string {
	var out []string
	for id := range c.Rules {
		if core.RuleByID(id) == nil {
			out = append(out, id)
		}
	}
	for _, a := range c.Allow {
		if a.Rule != "*" && core.RuleByID(a.Rule) == nil {
			out = append(out, a.Rule)
		}
	}
	return out
}

// SeverityFor returns the effective severity of a rule.
func (c *Config) SeverityFor(r *core.Rule) core.Severity {
	if s, ok := c.severities[r.ID]; ok {
		return s
	}
	return r.Severity
}

// FailThreshold returns the severity at which `narc scan` exits non-zero,
// and false if it never fails.
func (c *Config) FailThreshold() (core.Severity, bool) {
	if c.FailOn == "never" {
		return core.Error, false
	}
	s, _ := core.ParseSeverity(c.FailOn)
	return s, true
}

// BlockThreshold returns the severity at which hooks block the agent.
func (c *Config) BlockThreshold() core.Severity {
	s, _ := core.ParseSeverity(c.Hook.BlockOn)
	return s
}

// Settings returns the subset of config that rules see.
func (c *Config) Settings() core.Settings {
	return core.Settings{Protected: c.Protected, TestGlobs: c.Tests}
}

// Template is written by `narc init`.
const Template = `# narc configuration — https://github.com/huyth45/narc
version: 1

# Exit non-zero when a finding at this severity (or worse) is present.
# One of: error, warning, info, never.
fail_on: error

# Paths narc never analyzes (gitignore-style globs).
ignore:
  # - "legacy/**"

# Any change to these paths is flagged (golden files, contract tests, ...).
protected:
  # - "tests/golden/**"

# Extra globs that should be treated as test code.
tests:
  # - "qa/**"

# Per-rule severity overrides: error, warning, info, or off.
rules:
  # any-cast: info

# Repository-level waivers. A reason is required.
allow:
  # - rule: test-file-deleted
  #   path: "tests/legacy/**"
  #   reason: "legacy suite replaced by tests/v2 in #123"

# Agent hooks (Claude Code, Codex, ...).
hook:
  block_on: error   # minimum severity that blocks the agent
  max_blocks: 3     # stop blocking after this many attempts in one session
`
