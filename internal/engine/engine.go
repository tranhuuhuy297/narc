// Package engine runs narc's rules over a changeset.
package engine

import (
	"fmt"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
)

// WaiverRuleID is the rule reported for waivers that lack a reason.
const WaiverRuleID = "waiver-missing-reason"

var waiverRule = &core.Rule{
	ID:       WaiverRuleID,
	Title:    "Waiver without a reason",
	Category: "integrity",
	Severity: core.Warning,
	Summary:  "A narc:allow comment must say why the waived change is acceptable.",
	Hint:     `Add a reason: narc:allow <rule> reason="why this is fine (link to issue)"`,
	Docs: "Waivers are an audit trail. A waiver without a reason does not apply, and the original finding stays.\n\n" +
		"```js\n// narc:allow skip-added                      <- rejected\n// narc:allow skip-added reason=\"flaky upstream API, see #812\"  <- accepted\n```",
	Check: func(*core.Analysis) []core.Finding { return nil }, // produced by the engine
}

func init() { core.Register(waiverRule) }

// Options controls a run.
type Options struct {
	Config *config.Config
	// Rules to run; nil means every registered rule.
	Rules []*core.Rule
}

// Result is the outcome of a run.
type Result struct {
	Label    string
	Findings []core.Finding // sorted; includes waived findings (Waived=true)
	Files    int            // files analyzed
	Skipped  int            // files skipped (binary, vendored, generated, ignored)
	Added    int            // added lines analyzed
	Removed  int            // removed lines analyzed
	Errors   []string       // internal rule errors (never fatal)
	Duration time.Duration
}

// Active returns findings that were not waived.
func (r *Result) Active() []core.Finding {
	var out []core.Finding
	for _, f := range r.Findings {
		if !f.Waived {
			out = append(out, f)
		}
	}
	return out
}

// Waived returns the waived findings.
func (r *Result) Waived() []core.Finding {
	var out []core.Finding
	for _, f := range r.Findings {
		if f.Waived {
			out = append(out, f)
		}
	}
	return out
}

// Count returns the number of active findings at exactly severity s.
func (r *Result) Count(s core.Severity) int {
	n := 0
	for _, f := range r.Findings {
		if !f.Waived && f.Severity == s {
			n++
		}
	}
	return n
}

// AtLeast returns active findings with severity >= s.
func (r *Result) AtLeast(s core.Severity) []core.Finding {
	var out []core.Finding
	for _, f := range r.Findings {
		if !f.Waived && f.Severity >= s {
			out = append(out, f)
		}
	}
	return out
}

// Score is an integrity score from 0 (busted) to 100 (clean).
func (r *Result) Score() int {
	s := 100 - 25*r.Count(core.Error) - 8*r.Count(core.Warning) - 2*r.Count(core.Info)
	if s < 0 {
		s = 0
	}
	return s
}

// Verdict is "clean", "suspicious" or "busted".
func (r *Result) Verdict() string {
	switch {
	case r.Count(core.Error) > 0:
		return "busted"
	case r.Count(core.Warning) > 0:
		return "suspicious"
	}
	return "clean"
}

// Run analyzes a changeset.
func Run(cs *core.Changeset, opts Options) *Result {
	start := time.Now()
	cfg := opts.Config
	if cfg == nil {
		cfg = config.Default()
	}
	rules := opts.Rules
	if rules == nil {
		rules = core.Rules()
	}
	res := &Result{Label: cs.Label}
	settings := cfg.Settings()

	a := &core.Analysis{Settings: settings, Source: cs.Source, Message: cs.Message}
	for _, fd := range cs.Files {
		p := fd.Path()
		if fd.Binary || core.IsIgnoredByDefault(p) || core.MatchAny(cfg.Ignore, p) {
			res.Skipped++
			continue
		}
		fi := core.NewFileInfo(fd, cs.Source, settings)
		if isGenerated(fi) {
			res.Skipped++
			continue
		}
		a.Files = append(a.Files, fi)
		res.Added += len(fi.Added)
		res.Removed += len(fi.Removed)
	}
	res.Files = len(a.Files)
	byPath := map[string]*core.FileInfo{}
	for _, f := range a.Files {
		byPath[f.Path] = f
	}

	var all []core.Finding
	for _, r := range rules {
		sev := cfg.SeverityFor(r)
		if sev == core.Off || r.ID == WaiverRuleID {
			continue
		}
		fs, err := runRule(r, a)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			continue
		}
		_, overridden := configured(cfg, r.ID)
		for _, f := range fs {
			if f.RuleID == "" {
				f.RuleID = r.ID
			}
			if f.Title == "" {
				f.Title = r.Title
			}
			if f.Category == "" {
				f.Category = r.Category
			}
			if f.Hint == "" {
				f.Hint = r.Hint
			}
			if overridden {
				f.Severity = sev
			}
			all = append(all, f)
		}
	}

	all = applyWaivers(all, byPath, cfg)
	for i := range all {
		all[i].ComputeFingerprint()
	}
	res.Findings = dedupe(all)
	sort.SliceStable(res.Findings, func(i, j int) bool {
		a, b := res.Findings[i], res.Findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Severity != b.Severity {
			return a.Severity > b.Severity
		}
		return a.RuleID < b.RuleID
	})
	res.Duration = time.Since(start)
	return res
}

func configured(cfg *config.Config, id string) (string, bool) {
	v, ok := cfg.Rules[id]
	return v, ok
}

func runRule(r *core.Rule, a *core.Analysis) (fs []core.Finding, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("rule %s crashed: %v\n%s", r.ID, p, debug.Stack())
		}
	}()
	return r.Check(a), nil
}

// isGenerated reports whether a file is generated and should be skipped.
// A generated marker only counts when the file is new or was already
// generated: adding "@generated" to a hand-written file must not hide it
// from narc (the generated-marker-added rule reports that).
func isGenerated(f *core.FileInfo) bool {
	newB, hasNew := f.NewContent()
	oldB, hasOld := f.OldContent()
	switch {
	case hasNew:
		if !core.IsGeneratedContent(newB) {
			return false
		}
		return !hasOld || core.IsGeneratedContent(oldB)
	case hasOld && f.Diff.Kind == diff.Deleted:
		return core.IsGeneratedContent(oldB)
	case f.Diff.Kind == diff.Added:
		// Patch-only: look at the first added lines of a new file.
		var head strings.Builder
		for i, l := range f.Added {
			if i >= 5 {
				break
			}
			head.WriteString(l.Raw + "\n")
		}
		return core.IsGeneratedContent([]byte(head.String()))
	}
	return false
}

func dedupe(fs []core.Finding) []core.Finding {
	seen := map[string]bool{}
	out := fs[:0]
	for _, f := range fs {
		k := fmt.Sprintf("%s|%d|%d", f.Fingerprint, f.Line, f.OldLine)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// ---- waivers ----

var (
	waiverRe     = regexp.MustCompile(`^narc:allow\s*([A-Za-z0-9_*,\- ]*?)\s*(?:(?:reason\s*[=:]|--|—|:)\s*(.*))?$`)
	quotedReason = regexp.MustCompile(`^["'](.*?)["']`)
)

type waiver struct {
	rules  []string
	reason string
	line   int
}

func (w waiver) covers(rule string) bool {
	for _, r := range w.rules {
		if r == rule || r == "*" || r == "all" {
			return true
		}
	}
	return false
}

// parseWaiver extracts a waiver directive from comment text.
func parseWaiver(comment string) (waiver, bool) {
	i := strings.Index(comment, "narc:allow")
	if i < 0 {
		return waiver{}, false
	}
	m := waiverRe.FindStringSubmatch(strings.TrimSpace(comment[i:]))
	if m == nil {
		return waiver{}, false
	}
	var w waiver
	for _, r := range strings.FieldsFunc(m[1], func(c rune) bool { return c == ',' || c == ' ' }) {
		w.rules = append(w.rules, strings.TrimSpace(r))
	}
	if len(w.rules) == 0 {
		w.rules = []string{"*"}
	}
	reason := strings.TrimSpace(m[2])
	if q := quotedReason.FindStringSubmatch(reason); q != nil {
		reason = q[1]
	}
	reason = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(reason, "*/"), "-->"))
	w.reason = reason
	return w, true
}

func applyWaivers(fs []core.Finding, files map[string]*core.FileInfo, cfg *config.Config) []core.Finding {
	var extra []core.Finding
	reported := map[string]bool{}
	for i := range fs {
		f := &fs[i]
		// repository-level waivers
		for _, a := range cfg.Allow {
			if (a.Rule == "*" || a.Rule == f.RuleID) && (a.Path == "" || core.MatchGlob(a.Path, f.Path)) {
				f.Waived, f.WaiverReason, f.WaiverSource = true, a.Reason, "config"
				break
			}
		}
		if f.Waived || f.Line <= 0 {
			continue
		}
		fi := files[f.Path]
		if fi == nil {
			continue
		}
		for _, ln := range []int{f.Line, f.Line - 1} {
			l, ok := fi.NewLine(ln)
			if !ok || l.Comment == "" {
				continue
			}
			w, ok := parseWaiver(l.Comment)
			if !ok || !w.covers(f.RuleID) {
				continue
			}
			if len([]rune(w.reason)) < 3 {
				key := fmt.Sprintf("%s:%d", f.Path, ln)
				if !reported[key] {
					reported[key] = true
					wf := waiverRule.Finding(fi, ln, fmt.Sprintf("narc:allow for %s has no reason, so it was ignored", f.RuleID))
					extra = append(extra, wf)
				}
				continue
			}
			f.Waived, f.WaiverReason, f.WaiverSource = true, w.reason, "inline"
			break
		}
	}
	return append(fs, extra...)
}
