package hook

import (
	"fmt"
	"sort"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
)

// coachStyle selects the wording of a message to the model.
type coachStyle int

const (
	editCoach coachStyle = iota // a denied edit
	postCoach                   // feedback on an edit that already happened
	stopCoach                   // a blocked stop
)

const (
	maxListed   = 8    // findings listed in one message
	maxCoachLen = 2500 // characters in a message to the model
	maxMsgLen   = 240  // characters of one finding's message
	maxHintLen  = 420  // characters of one hint
)

// coach writes the message that tells the model what it did and what to
// do instead. found must not be empty.
func coach(style coachStyle, found []core.Finding, repeated bool) string {
	var head, fix string
	switch style {
	case editCoach:
		head = "narc blocked this edit: it weakens verification instead of fixing the code."
		fix = "Fix the underlying problem so the original checks pass."
	case postCoach:
		head = "narc: the edit you just made weakens verification instead of fixing the code."
		fix = "Undo or rework it, and fix the underlying problem so the original checks pass."
	case stopCoach:
		head = "narc: do not stop yet. Your changes in this session weaken verification instead of fixing the code."
		if repeated {
			head += " You tried to stop again without resolving any of these."
		}
		fix = "Fix the underlying problem so the original checks pass, then finish. Anything left unresolved is reported to the user."
	}
	if style == editCoach && anyRemoval(found) {
		fix += " If you are moving this code to another file, add it there first: narc judges each edit together with your other changes."
	}
	tail := fix + " Do not skip, delete or weaken tests, assertions, type checks, coverage or CI to get a passing run.\n" +
		"If the user explicitly asked for this change, " + waiverAdvice(found[0]) + ". Do not add a waiver the user did not ask for."

	// Fit the budget by dropping later hints first, then later findings.
	var msg string
	for n := min(len(found), maxListed); n >= 1; n-- {
		for hints := n; hints >= 0; hints-- {
			msg = head + "\n\n" + findingList(found, n, hints) + "\n" + tail
			if len(msg) <= maxCoachLen {
				return msg
			}
		}
	}
	return truncate(msg, maxCoachLen)
}

// anyRemoval reports whether a finding is about removed code.
func anyRemoval(found []core.Finding) bool {
	for _, f := range found {
		if f.OldLine > 0 || f.Before != "" {
			return true
		}
	}
	return false
}

// findingList renders the first n findings, one per line, with the hints
// of the first hints distinct rules, and a count of the rest.
func findingList(found []core.Finding, n, hints int) string {
	var b strings.Builder
	hinted := map[string]bool{}
	for _, f := range found[:n] {
		fmt.Fprintf(&b, "✗ %s %s — %s\n", f.RuleID, f.Location(), truncate(oneLine(f.Message), maxMsgLen))
		if f.Hint != "" && !hinted[f.RuleID] && len(hinted) < hints {
			hinted[f.RuleID] = true
			fmt.Fprintf(&b, "  → %s\n", truncate(oneLine(f.Hint), maxHintLen))
		}
	}
	if rest := len(found) - n; rest > 0 {
		fmt.Fprintf(&b, "…and %d more.\n", rest)
	}
	return b.String()
}

// waiverAdvice tells the model how to waive f. An inline waiver applies
// to its own line and the line below it, so it goes right above the
// finding.
func waiverAdvice(f core.Finding) string {
	ex := waiverExample(f)
	if strings.HasPrefix(ex, "an allow entry") {
		return "add a waiver with a reason as " + ex
	}
	return "add an inline waiver with a reason on the line above " + f.Location() + ", e.g. " + ex
}

// waiverExample shows how to waive f in its own file, or in .narc.yml for
// files that cannot hold a comment and for findings about a whole file
// (such as a deleted file), which an inline comment cannot reach.
func waiverExample(f core.Finding) string {
	w := fmt.Sprintf(`narc:allow %s reason="<why>"`, f.RuleID)
	if f.Line > 0 {
		switch commentMarker(f.Path) {
		case "#":
			return "# " + w
		case "//":
			return "// " + w
		}
	}
	return fmt.Sprintf(`an allow entry in .narc.yml (rule: %s, path: %q, reason: "<why>")`, f.RuleID, f.Path)
}

// commentMarker returns the line-comment marker narc recognizes in a
// file, or "" when the file cannot carry an inline waiver.
func commentMarker(path string) string {
	switch lang.Detect(path) {
	case lang.Python, lang.Ruby, lang.Elixir, lang.Shell, lang.YAML, lang.TOML, lang.INI, lang.Makefile, lang.Dockerfile:
		return "#"
	case lang.JSON, lang.Markdown, lang.Unknown:
		return ""
	}
	return "//"
}

// editNotice is the one-line note for the human when an edit is denied.
func editNotice(found []core.Finding) string {
	return fmt.Sprintf("narc blocked an edit to %s (%s)", listOf(pathsOf(found), 3), listOf(rulesOf(found), 4))
}

// releaseNotice tells the human that narc let the agent stop with
// unresolved findings.
func releaseNotice(found []core.Finding, attempts int) string {
	n := min(len(found), maxListed)
	return fmt.Sprintf("narc: the agent stopped with %s after %s. Review before merging:\n%sRun `narc scan` for details.",
		plural(len(found), "unresolved verification issue"), plural(attempts, "blocked attempt"), findingList(found, n, 0))
}

// reviewNotice tells the human about waivers and narc configuration
// changes that let this session's changes through ("" when there are none).
func reviewNotice(items []reviewItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("narc: please review these changes from this session:")
	for i, it := range items {
		if i == maxListed {
			fmt.Fprintf(&b, "\n…and %d more (narc scan --show-waived lists them).", len(items)-i)
			break
		}
		if it.config != "" {
			fmt.Fprintf(&b, "\n• %s changed: it decides what narc checks and blocks", it.config)
			continue
		}
		f := it.finding
		fmt.Fprintf(&b, "\n• %s %s was waived — %s (reason: %q)", f.RuleID, f.Location(),
			truncate(oneLine(f.Message), maxMsgLen), truncate(oneLine(f.WaiverReason), 160))
	}
	return b.String()
}

func pathsOf(fs []core.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return sortedUnique(out)
}

func rulesOf(fs []core.Finding) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range fs {
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			out = append(out, f.RuleID)
		}
	}
	sort.Strings(out)
	return out
}

// listOf joins up to limit items, counting the rest.
func listOf(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:limit], ", "), len(items)-limit)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// truncate shortens s to at most n bytes without splitting a UTF-8
// sequence, marking the cut with an ellipsis.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
