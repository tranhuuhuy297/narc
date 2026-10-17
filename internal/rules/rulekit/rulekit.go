// Package rulekit has helpers shared by narc's rule packages.
package rulekit

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
)

// Text selects which part of a lexed line a pattern is matched against.
type Text func(core.Line) string

var (
	// Code is the line without comments and without string contents.
	Code Text = func(l core.Line) string { return l.Code }
	// NoComment is the line without comments, strings intact.
	NoComment Text = func(l core.Line) string { return l.NoComment }
	// Comment is only the comment text of the line.
	Comment Text = func(l core.Line) string { return l.Comment }
	// Raw is the full original line.
	Raw Text = func(l core.Line) string { return l.Raw }
)

// Match is a pattern match on an added line.
type Match struct {
	File   *core.FileInfo
	Line   core.Line
	Text   string   // the matched text
	Groups []string // submatches (Groups[0] is the full match)
}

var (
	wsRe    = regexp.MustCompile(`\s+`)
	quoteRe = regexp.MustCompile("[\"'`]")
)

// Normalize canonicalizes a line of code so that pure reformatting (quote
// style, whitespace, trailing punctuation) compares equal.
func Normalize(s string) string {
	s = quoteRe.ReplaceAllString(s, `"`)
	s = wsRe.ReplaceAllString(strings.TrimSpace(s), "")
	return strings.TrimRight(s, ";,")
}

// Introduced returns pattern matches on added lines that were not already
// present on a removed line of the same files. A removed line cancels an
// added line when both normalize to the same text and contain the same
// match, so moved or reformatted code is not reported as new.
func Introduced(files []*core.FileInfo, re *regexp.Regexp, text Text) []Match {
	type key struct{ line, match string }
	removed := map[key]int{}
	for _, f := range files {
		for _, l := range f.Removed {
			t := text(l)
			for _, m := range re.FindAllString(t, -1) {
				removed[key{Normalize(t), Normalize(m)}]++
			}
		}
	}
	var out []Match
	for _, f := range files {
		for _, l := range f.Added {
			t := text(l)
			for _, sm := range re.FindAllStringSubmatch(t, -1) {
				k := key{Normalize(t), Normalize(sm[0])}
				if removed[k] > 0 {
					removed[k]--
					continue
				}
				out = append(out, Match{File: f, Line: l, Text: sm[0], Groups: sm})
			}
		}
	}
	return out
}

// IntroducedNet is like Introduced but cancels on the match text alone, per
// file: an added match is only reported if the file gained more matches
// than it lost. Use it for markers that agents often re-indent or move
// within a file (e.g. @ts-ignore).
func IntroducedNet(f *core.FileInfo, re *regexp.Regexp, text Text) []Match {
	removed := map[string]int{}
	for _, l := range f.Removed {
		for _, m := range re.FindAllString(text(l), -1) {
			removed[Normalize(m)]++
		}
	}
	var out []Match
	for _, l := range f.Added {
		for _, sm := range re.FindAllStringSubmatch(text(l), -1) {
			k := Normalize(sm[0])
			if removed[k] > 0 {
				removed[k]--
				continue
			}
			out = append(out, Match{File: f, Line: l, Text: sm[0], Groups: sm})
		}
	}
	return out
}

// Pair is a replacement inside one hunk: a run of removed lines directly
// followed by a run of added lines.
type Pair struct {
	Removed []core.Line
	Added   []core.Line
}

// Pairs returns the replacement blocks of a file. Pure additions or pure
// removals are returned with an empty other side.
func Pairs(f *core.FileInfo) []Pair {
	var out []Pair
	// f.Removed and f.Added are in hunk order; walk the diff to find runs.
	ri, ai := 0, 0
	for hi, h := range f.Diff.Hunks {
		var cur Pair
		inChange := false
		flush := func() {
			if inChange && (len(cur.Removed) > 0 || len(cur.Added) > 0) {
				out = append(out, cur)
			}
			cur = Pair{}
			inChange = false
		}
		for _, l := range h.Lines {
			switch l.Op {
			case '-':
				if len(cur.Added) > 0 {
					flush()
				}
				inChange = true
				if ri < len(f.Removed) && f.Removed[ri].Hunk == hi {
					cur.Removed = append(cur.Removed, f.Removed[ri])
				}
				ri++
			case '+':
				inChange = true
				if ai < len(f.Added) && f.Added[ai].Hunk == hi {
					cur.Added = append(cur.Added, f.Added[ai])
				}
				ai++
			default:
				flush()
			}
		}
		flush()
	}
	return out
}

// CallArgs returns the top-level, comma-separated arguments of the call
// whose opening parenthesis is at s[open]. It returns ok=false if the
// parentheses are not balanced on this line.
func CallArgs(s string, open int) (args []string, end int, ok bool) {
	if open < 0 || open >= len(s) || s[open] != '(' {
		return nil, 0, false
	}
	depth := 0
	start := open + 1
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				if a := strings.TrimSpace(s[start:i]); a != "" || len(args) > 0 {
					args = append(args, a)
				}
				return args, i, true
			}
		case ',':
			if depth == 1 {
				args = append(args, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return nil, 0, false
}

// HasIssueRef reports whether text references an issue or URL, which makes
// a deliberate skip more credible (e.g. "flaky, see #123").
func HasIssueRef(s string) bool { return issueRe.MatchString(s) }

var issueRe = regexp.MustCompile(`(#\d+\b|https?://|\b[A-Z][A-Z0-9]+-\d+\b|\bissue\b|\bticket\b|\bTODO\(\w+\))`)

// Files filters an analysis' files.
func Files(a *core.Analysis, keep func(*core.FileInfo) bool) []*core.FileInfo {
	var out []*core.FileInfo
	for _, f := range a.Files {
		if keep(f) {
			out = append(out, f)
		}
	}
	return out
}
