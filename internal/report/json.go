package report

import (
	"encoding/json"
	"io"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/engine"
)

// JSONSchemaVersion is bumped whenever the JSON report changes in a way
// that could break consumers (fields renamed, removed or retyped). New
// fields may appear without a bump.
const JSONSchemaVersion = 1

// jsonReport is the stable JSON schema. Fields are never omitted at the
// top level, and lists are [] rather than null.
type jsonReport struct {
	Version    string        `json:"version"`
	Schema     int           `json:"schema"`
	Label      string        `json:"label"`
	Verdict    string        `json:"verdict"` // clean, suspicious, busted
	Score      int           `json:"score"`   // 0..100
	Counts     jsonCounts    `json:"counts"`
	Files      int           `json:"files"`
	Skipped    int           `json:"skipped"`
	Lines      jsonLines     `json:"lines"`
	Findings   []jsonFinding `json:"findings"` // active and waived
	Errors     []string      `json:"errors"`   // internal rule errors
	DurationMS int64         `json:"duration_ms"`
}

// jsonCounts counts active findings by severity, plus waived findings.
type jsonCounts struct {
	Error   int `json:"error"`
	Warning int `json:"warning"`
	Info    int `json:"info"`
	Waived  int `json:"waived"`
}

type jsonLines struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

type jsonFinding struct {
	Rule     string `json:"rule"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Severity string `json:"severity"` // error, warning, info
	Path     string `json:"path"`
	// Line is the 1-based new-side line; 0 means the whole file.
	Line         int    `json:"line"`
	OldLine      int    `json:"old_line,omitempty"`
	Message      string `json:"message"`
	Evidence     string `json:"evidence,omitempty"`
	Before       string `json:"before,omitempty"`
	Hint         string `json:"hint,omitempty"`
	Waived       bool   `json:"waived"`
	WaiverReason string `json:"waiver_reason,omitempty"`
	Fingerprint  string `json:"fingerprint"`
}

// JSON writes the machine-readable report. Findings are listed in
// canonical order and include waived ones (waived: true); counts.error,
// .warning and .info count active findings only. duration_ms is 0 when
// Options.HideDuration is set.
func JSON(w io.Writer, res *engine.Result, o Options) error {
	if res == nil {
		return errNilResult
	}
	v := newView(res)
	out := jsonReport{
		Version:  o.version(),
		Schema:   JSONSchemaVersion,
		Label:    res.Label,
		Verdict:  v.verdict.key,
		Score:    v.score,
		Counts:   jsonCounts{Error: v.errors, Warning: v.warns, Info: v.infos, Waived: len(v.waived)},
		Files:    res.Files,
		Skipped:  res.Skipped,
		Lines:    jsonLines{Added: res.Added, Removed: res.Removed},
		Findings: make([]jsonFinding, 0, len(v.all)),
		Errors:   append([]string{}, res.Errors...),
	}
	if !o.HideDuration {
		out.DurationMS = res.Duration.Milliseconds()
	}
	for _, f := range v.all {
		out.Findings = append(out.Findings, jsonFinding{
			Rule:         f.RuleID,
			Title:        f.Title,
			Category:     f.Category,
			Severity:     f.Severity.String(),
			Path:         f.Path,
			Line:         f.Line,
			OldLine:      f.OldLine,
			Message:      f.Message,
			Evidence:     f.Evidence,
			Before:       f.Before,
			Hint:         f.Hint,
			Waived:       f.Waived,
			WaiverReason: f.WaiverReason,
			Fingerprint:  fingerprint(f),
		})
	}
	return encodeJSON(w, out)
}

// encodeJSON writes v as indented JSON without HTML escaping, so code
// such as `a < b && c` stays readable.
func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// fingerprint returns the finding's fingerprint, computing it for findings
// that did not come through the engine.
func fingerprint(f core.Finding) string {
	if f.Fingerprint != "" {
		return f.Fingerprint
	}
	return f.ComputeFingerprint() // f is a copy
}
