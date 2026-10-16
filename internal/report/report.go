// Package report renders narc results for people and machines: a pretty
// terminal report, JSON, SARIF 2.1.0 (GitHub code scanning), Markdown (PR
// comments and $GITHUB_STEP_SUMMARY) and GitHub Actions workflow commands,
// plus the rule listings behind `narc rules` and `narc explain`.
//
// Every renderer is deterministic for a given result: findings are ordered
// by path, line, severity and rule, and nothing time-dependent is printed
// except the run duration, which Options.HideDuration removes.
package report

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/huyth45/narc/internal/config"
	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/engine"
)

const (
	// ProjectURL is narc's home page.
	ProjectURL = "https://github.com/huyth45/narc"
	// RulesDocURL is the generated rule reference (docs/rules.md, written
	// by RulesMarkdown). Each rule has an anchor named after its ID.
	RulesDocURL = ProjectURL + "/blob/main/docs/rules.md"
	// MarkdownMarker is a hidden comment at the top of every Markdown
	// report, so a bot can find and update its sticky PR comment.
	MarkdownMarker = "<!-- narc-report -->"
	// DefaultWidth is the terminal width used when Options.Width is unset.
	DefaultWidth = 100

	minWidth = 40
)

// Options controls rendering. The zero value renders a plain report
// without hints at DefaultWidth.
type Options struct {
	// Color enables ANSI colors (see ColorEnabled).
	Color bool
	// ASCII replaces emoji and box-drawing glyphs with plain ASCII, for
	// terminals or fonts that cannot show them.
	ASCII bool
	// Width is the terminal width in columns; 0 means DefaultWidth.
	Width int
	// ShowWaived lists waived findings in the pretty and GitHub formats.
	// JSON, SARIF and Markdown always include them: waivers are an audit
	// trail that reviewers and tools must be able to see.
	ShowWaived bool
	// ShowHints prints each finding's fix advice.
	ShowHints bool
	// HideDuration omits the run duration, for reproducible output.
	HideDuration bool
	// Version is narc's version; "" means "dev".
	Version string
	// Root is the absolute repository root. SARIF uses it as the
	// %SRCROOT% base URI; finding paths are always repository-relative.
	Root string
	// Command is the command line that produced the result, e.g.
	// "narc scan --base main" (SARIF invocation, Markdown footer).
	Command string
	// LinkBase, when set, turns Markdown locations into links to
	// LinkBase/<path>#L<line>. Use a blob URL such as
	// https://github.com/OWNER/REPO/blob/SHA.
	LinkBase string
	// Config is the loaded .narc.yml, if any. SARIF uses its allow:
	// entries to tell repository-level waivers ("external" suppressions)
	// from inline narc:allow comments ("inSource").
	Config *config.Config
}

func (o Options) width() int {
	switch {
	case o.Width <= 0:
		return DefaultWidth
	case o.Width < minWidth:
		return minWidth
	}
	return o.Width
}

func (o Options) version() string {
	if v := strings.TrimSpace(o.Version); v != "" {
		return v
	}
	return "dev"
}

// displayVersion is the version as shown to people: "v1.2.3" or "dev".
func (o Options) displayVersion() string {
	v := o.version()
	if semverRe.MatchString(v) && !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

var errNilResult = errors.New("report: nil result")

// RuleDocURL links to a rule's section in the rule reference.
func RuleDocURL(id string) string { return RulesDocURL + "#" + url.PathEscape(id) }

// verdict describes the overall outcome of a run.
type verdict struct {
	key   string        // "busted", "suspicious" or "clean" (engine.Result.Verdict)
	label string        // "BUSTED"
	sev   core.Severity // severity whose color the verdict takes
}

func verdictOf(res *engine.Result) verdict {
	switch res.Verdict() {
	case "busted":
		return verdict{"busted", "BUSTED", core.Error}
	case "suspicious":
		return verdict{"suspicious", "SUSPICIOUS", core.Warning}
	}
	return verdict{"clean", "CLEAN", core.Info}
}

// view is a result prepared for rendering: findings in canonical order and
// split into active and waived, with the counts every format shows.
type view struct {
	res     *engine.Result
	all     []core.Finding
	active  []core.Finding
	waived  []core.Finding
	errors  int
	warns   int
	infos   int
	score   int
	verdict verdict
}

func newView(res *engine.Result) *view {
	v := &view{res: res, all: sortedFindings(res.Findings), score: res.Score(), verdict: verdictOf(res)}
	for _, f := range v.all {
		if f.Waived {
			v.waived = append(v.waived, f)
			continue
		}
		v.active = append(v.active, f)
		switch f.Severity {
		case core.Error:
			v.errors++
		case core.Warning:
			v.warns++
		default:
			v.infos++
		}
	}
	return v
}

// sortedFindings returns a copy of fs in the engine's canonical order (path,
// line, severity descending, rule), so every format agrees even when a
// caller hands over findings that were not produced by engine.Run. Ties
// (one rule firing twice on a line, e.g. two removed assertions anchored
// at the same place) are broken by old line, message and fingerprint, so
// the output never depends on the order findings arrived in.
func sortedFindings(fs []core.Finding) []core.Finding {
	out := append([]core.Finding(nil), fs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Line != b.Line:
			return a.Line < b.Line
		case a.Severity != b.Severity:
			return a.Severity > b.Severity
		case a.RuleID != b.RuleID:
			return a.RuleID < b.RuleID
		case a.OldLine != b.OldLine:
			return a.OldLine < b.OldLine
		case a.Message != b.Message:
			return a.Message < b.Message
		}
		return fingerprint(a) < fingerprint(b)
	})
	return out
}

// fileGroup is the findings for one file, in order.
type fileGroup struct {
	path     string
	findings []core.Finding
}

// groupByFile groups sorted findings by path, keeping their order.
func groupByFile(fs []core.Finding) []fileGroup {
	var out []fileGroup
	for _, f := range fs {
		if n := len(out); n > 0 && out[n-1].path == f.Path {
			out[n-1].findings = append(out[n-1].findings, f)
			continue
		}
		out = append(out, fileGroup{path: f.Path, findings: []core.Finding{f}})
	}
	return out
}

// severityCounts counts findings by severity.
func severityCounts(fs []core.Finding) (errs, warns, infos int) {
	for _, f := range fs {
		switch f.Severity {
		case core.Error:
			errs++
		case core.Warning:
			warns++
		default:
			infos++
		}
	}
	return
}

// sevWord is the user-facing name of a severity: info findings are
// "notes", as in the BUSTED / SUSPICIOUS / NOTE labels.
func sevWord(s core.Severity) string {
	switch s {
	case core.Error:
		return "error"
	case core.Warning:
		return "warning"
	}
	return "note"
}

// plural renders "1 error" / "3 errors".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// countPhrase renders "2 errors and 3 warnings" for the non-zero counts.
func countPhrase(errs, warns, infos int) string {
	var parts []string
	for _, c := range []struct {
		n    int
		word string
	}{{errs, "error"}, {warns, "warning"}, {infos, "note"}} {
		if c.n > 0 {
			parts = append(parts, plural(c.n, c.word))
		}
	}
	switch len(parts) {
	case 0:
		return "no findings"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// where describes a finding's position for people: "line 42", "old line 7"
// for code removed from a deleted file, or "whole file".
func where(f core.Finding) string {
	switch {
	case f.Line > 0:
		return fmt.Sprintf("line %d", f.Line)
	case f.OldLine > 0:
		return fmt.Sprintf("old line %d", f.OldLine)
	}
	return "whole file"
}

// formatDuration renders a run duration compactly: "<1ms", "42ms", "1.3s".
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// firstLine returns the first line of s (internal errors carry stacks).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// waivedByConfig reports whether a waived finding was waived by an allow:
// entry in .narc.yml rather than an inline narc:allow comment. The engine
// checks repository-level waivers first with exactly this match, so a hit
// here means the config entry is what waived it.
func waivedByConfig(f core.Finding, cfg *config.Config) bool {
	if cfg == nil {
		// Without the config, a file-level finding can only have been
		// waived externally: inline waivers need a line to sit on.
		return f.Line <= 0
	}
	for _, a := range cfg.Allow {
		if (a.Rule == "*" || a.Rule == f.RuleID) && (a.Path == "" || core.MatchGlob(a.Path, f.Path)) {
			return true
		}
	}
	return f.Line <= 0
}
