// Package core holds the types shared by narc's engine, rules and reporters.
package core

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Severity of a finding.
type Severity int

const (
	Info Severity = iota
	Warning
	Error
	// Off disables a rule. It is only used in configuration.
	Off Severity = -1
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	case Off:
		return "off"
	}
	return fmt.Sprintf("severity(%d)", int(s))
}

// ParseSeverity parses "error", "warning"/"warn", "info"/"note", or "off".
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error", "err", "high":
		return Error, nil
	case "warning", "warn", "medium":
		return Warning, nil
	case "info", "note", "low":
		return Info, nil
	case "off", "none", "disable", "disabled", "false":
		return Off, nil
	}
	return Info, fmt.Errorf("unknown severity %q (want error, warning, info or off)", s)
}

func (s Severity) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s *Severity) UnmarshalText(b []byte) error {
	v, err := ParseSeverity(string(b))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Finding is a single integrity issue.
type Finding struct {
	RuleID   string   `json:"rule"`
	Title    string   `json:"title"`
	Category string   `json:"category"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	// Line is the 1-based line in the new version of the file. For findings
	// about removed code it is the new-side line where the removal happened.
	// 0 means the finding applies to the whole file.
	Line int `json:"line,omitempty"`
	// OldLine is the 1-based line in the old version, for removals.
	OldLine int `json:"old_line,omitempty"`
	// Message is a specific, one-line description of what happened.
	Message string `json:"message"`
	// Evidence is the offending code (trimmed), if any.
	Evidence string `json:"evidence,omitempty"`
	// Before is the code that was replaced or removed, if relevant.
	Before string `json:"before,omitempty"`
	// Hint tells a human or an agent how to fix the issue.
	Hint string `json:"hint,omitempty"`

	Waived       bool   `json:"waived,omitempty"`
	WaiverReason string `json:"waiver_reason,omitempty"`
	// WaiverSource is "inline" (a narc:allow comment) or "config" (an
	// allow: entry in .narc.yml) for waived findings.
	WaiverSource string `json:"waiver_source,omitempty"`

	Fingerprint string `json:"fingerprint"`
}

// Location renders path:line.
func (f Finding) Location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return f.Path
}

var wsRun = regexp.MustCompile(`\s+`)

// ComputeFingerprint returns a fingerprint that is stable when code moves
// between lines.
func (f *Finding) ComputeFingerprint() string {
	h := sha1.New()
	norm := func(s string) string { return wsRun.ReplaceAllString(strings.TrimSpace(s), " ") }
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", f.RuleID, f.Path, norm(f.Evidence), norm(f.Before), norm(f.Message))
	f.Fingerprint = hex.EncodeToString(h.Sum(nil))[:16]
	return f.Fingerprint
}

// Rule is a single integrity check.
type Rule struct {
	ID       string
	Title    string
	Category string // tests, accounting, assertions, escapes, ci, config, integrity
	Severity Severity
	// Summary is one sentence for `narc rules`.
	Summary string
	// Docs is a longer Markdown explanation, ideally with a bad/good example.
	Docs string
	// Hint is the default fix advice for findings of this rule.
	Hint string
	// Check runs the rule over an analysis and returns findings. Rules
	// should build findings with (*Rule).Finding so defaults are filled in.
	Check func(a *Analysis) []Finding
}

// Finding builds a finding for this rule at the given new-side line.
// The evidence is filled from the file's new content when line > 0 and
// evidence is empty.
func (r *Rule) Finding(f *FileInfo, line int, msg string) Finding {
	fd := Finding{
		RuleID:   r.ID,
		Title:    r.Title,
		Category: r.Category,
		Severity: r.Severity,
		Path:     f.Path,
		Line:     line,
		Message:  msg,
		Hint:     r.Hint,
	}
	if line > 0 {
		if l, ok := f.NewLine(line); ok {
			fd.Evidence = strings.TrimSpace(l.Raw)
		}
	}
	return fd
}

// Removal builds a finding about a removed old-side line. The finding is
// anchored at the new-side position of the removal so inline waivers work.
func (r *Rule) Removal(f *FileInfo, oldLine int, msg string) Finding {
	fd := r.Finding(f, 0, msg)
	fd.OldLine = oldLine
	fd.Line = f.AnchorForOld(oldLine)
	if l, ok := f.OldLine(oldLine); ok {
		fd.Before = strings.TrimSpace(l.Raw)
	}
	fd.Evidence = ""
	return fd
}

var (
	regMu    sync.Mutex
	registry = map[string]*Rule{}
)

// Register adds a rule to the global registry. It panics on duplicate IDs.
// Rule packages call it from init().
func Register(rules ...*Rule) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, r := range rules {
		if r.ID == "" || r.Check == nil {
			panic("narc: rule must have an ID and a Check function")
		}
		if _, dup := registry[r.ID]; dup {
			panic("narc: duplicate rule " + r.ID)
		}
		registry[r.ID] = r
	}
}

// Rules returns all registered rules ordered by category and ID.
func Rules() []*Rule {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]*Rule, 0, len(registry))
	for _, r := range registry {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if ci, cj := categoryOrder(out[i].Category), categoryOrder(out[j].Category); ci != cj {
			return ci < cj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// RuleByID returns the rule with the given ID, or nil.
func RuleByID(id string) *Rule {
	regMu.Lock()
	defer regMu.Unlock()
	return registry[id]
}

var categories = []string{"tests", "accounting", "assertions", "escapes", "ci", "config", "integrity"}

// Categories returns rule categories in display order.
func Categories() []string { return append([]string(nil), categories...) }

func categoryOrder(c string) int {
	for i, x := range categories {
		if x == c {
			return i
		}
	}
	return len(categories)
}
