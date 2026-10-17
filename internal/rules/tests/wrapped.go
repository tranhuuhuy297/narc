package tests

import (
	"fmt"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

// Assertions wrapped in a new block comment: the change adds a comment
// opener above unchanged assertions and a closer below them, so the diff
// shows only the two delimiter lines while every line in between stops
// running. assertion-commented reports these alongside assertions that were
// rewritten as comments line by line.

// maxWrapped bounds how far below its opening line a new block comment is
// followed, so a comment that never closes cannot make the rule scan the
// whole file.
const maxWrapped = 400

// wrappedIn finds assertions that a new block comment (or, in Python, a bare
// triple-quoted string) disabled without touching their lines. Both the
// opening and the closing line must be added lines, which rules out
// comments that only look open because the lexer misread something above
// them.
func wrappedIn(f *core.FileInfo) []core.Finding {
	added := map[int]bool{}
	for _, l := range f.Added {
		added[l.No] = true
	}
	var out []core.Finding
	for _, open := range f.Added {
		closer, ok := blockOpener(f.Lang, open.Raw)
		if !ok {
			continue
		}
		var hits []core.Line // the unchanged assertion lines inside the block
		var olds []lang.LexedLine
		closed := false
		for n := open.No + 1; n <= open.No+maxWrapped; n++ {
			nl, ok := f.NewLine(n)
			if !ok {
				break // past the end of the file, or outside the hunks of a bare patch
			}
			if added[n] {
				if strings.Contains(nl.Raw, closer) {
					closed = true
					break
				}
				continue
			}
			if strings.TrimSpace(nl.Code) != "" {
				break // live code: the block closed somewhere unexpected
			}
			oldNo, ok := oldLineOf(f, n)
			if !ok {
				continue
			}
			ol, ok := f.OldLine(oldNo)
			if !ok || strings.TrimSpace(ol.Code) == "" {
				continue
			}
			stmt := chained(f, core.Line{LexedLine: ol, No: oldNo}, oldVersion)
			if !isAssertion(f.Lang, stmt) {
				continue
			}
			if c, _ := commentedCode(f.Lang, core.Line{LexedLine: nl}); c != rulekit.Normalize(ol.NoComment) {
				continue
			}
			hits = append(hits, core.Line{LexedLine: nl, No: n})
			olds = append(olds, stmt.LexedLine)
		}
		if closed && len(hits) > 0 {
			out = append(out, wrappedFinding(f, open, hits, olds))
		}
	}
	return out
}

// blockOpener reports whether a line opens a multi-line block comment (or a
// Python triple-quoted string statement) without closing it, and returns
// the delimiter that closes it.
func blockOpener(lg lang.Lang, raw string) (closer string, ok bool) {
	t := strings.TrimSpace(raw)
	switch {
	case lg == lang.Python:
		for _, q := range []string{`"""`, `'''`} {
			if strings.HasPrefix(t, q) && strings.Count(t, q) == 1 {
				return q, true
			}
		}
	case lg == lang.Ruby:
		if strings.HasPrefix(raw, "=begin") {
			return "=end", true
		}
	case lg.IsProgramming():
		if strings.HasPrefix(t, "/*") && !strings.Contains(t[2:], "*/") {
			return "*/", true
		}
	}
	return "", false
}

// oldLineOf maps an unchanged new-side line to its old-side number. ok is
// false for added lines.
func oldLineOf(f *core.FileInfo, newNo int) (int, bool) {
	delta := 0 // old minus new line numbers after the hunks seen so far
	for _, h := range f.Diff.Hunks {
		if newNo < h.NewStart {
			break
		}
		if newNo < h.NewStart+h.NewLines {
			for _, l := range h.Lines {
				if l.NewNo == newNo {
					return l.OldNo, l.Op == diff.Context
				}
			}
			return 0, false
		}
		delta = (h.OldStart + h.OldLines) - (h.NewStart + h.NewLines)
	}
	return newNo + delta, true
}

func wrappedFinding(f *core.FileInfo, open core.Line, hits []core.Line, olds []lang.LexedLine) core.Finding {
	first := clip(strings.TrimRight(strings.TrimSpace(olds[0].NoComment), ";"), 100)
	what := "a block comment"
	if f.Lang == lang.Python {
		what = "a triple-quoted string"
	}
	var msg string
	if len(hits) == 1 {
		msg = fmt.Sprintf("assertion disabled by wrapping it in %s: %s", what, first)
	} else {
		msg = fmt.Sprintf("%d assertions disabled by wrapping them in %s, starting with %s", len(hits), what, first)
	}
	if name := testAround(f, hits[0].No); name.ok() {
		msg += fmt.Sprintf(" in %s", name)
	}
	fd := assertionCommented.Finding(f, hits[0].No, msg)
	fd.Before = strings.TrimSpace(olds[0].Raw)
	if issueRef(f, open, open.Comment) {
		fd.Severity = core.Warning
		fd.Message += " (the comment references an issue)"
		fd.Hint = "The comment links an issue, so this may be deliberate. Track it and restore the assertions as soon " +
			"as the issue is fixed; prefer a skip with a reason over silently disabled checks."
	}
	return fd
}
