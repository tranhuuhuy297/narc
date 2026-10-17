package tests

import (
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// occurrence is a pattern found on one diff line.
type occurrence[T any] struct {
	File *core.FileInfo
	Line core.Line
	// Kind decides what cancels what: an added occurrence only survives if no
	// removed occurrence of the same kind accounts for it.
	Kind string
	Val  T
}

// detector finds the occurrences of a pattern on line l of version v of f.
type detector[T any] func(f *core.FileInfo, l core.Line, v version) []occurrence[T]

// version says which version of a file a line belongs to: removed lines are
// numbered in the old version, added and context lines in the new one.
// Detectors that look at neighbouring lines must read the same version.
type version int

const (
	newVersion version = iota
	oldVersion
)

// line returns line n of version v of f.
func (v version) line(f *core.FileInfo, n int) (lang.LexedLine, bool) {
	if v == oldVersion {
		return f.OldLine(n)
	}
	return f.NewLine(n)
}

// getter returns v.line bound to f.
func (v version) getter(f *core.FileInfo) lineGetter {
	return func(n int) (lang.LexedLine, bool) { return v.line(f, n) }
}

// cancelling says which removed occurrences cancel an added one besides
// those on the same code, and those in the same file that the change
// edited into it (same replacement block, or same string literals, e.g. a
// skipped test made async or renamed). A marker that moved to another test
// in the same file is still new.
type cancelling[T any] struct {
	// scope, when set, also lets a removed occurrence cancel an added one in
	// the same test or in a test inside the suite it applied to, whatever
	// their kinds: a skip that moved from a decorator into the test body, or
	// a describe.skip narrowed to the one failing it.skip, disables nothing
	// new.
	scope scopeFunc[T]
	// compatible, when set, must hold for a removed occurrence (old version)
	// to cancel an added one (new version), e.g. a conditional skip does not
	// cancel an unconditional one that replaced it.
	compatible func(removed, added occurrence[T]) bool
}

// introduced runs detect over the removed and added lines of files and
// returns the added occurrences the change actually introduced. An added
// occurrence is cancelled by a removed occurrence of the same kind on the
// same code after normalization (moved, re-indented or reformatted code,
// possibly in another file), or by one it was edited from; see cancelling.
func introduced[T any](files []*core.FileInfo, detect detector[T], c cancelling[T]) []occurrence[T] {
	type side struct {
		occurrence[T]
		v      version
		key    string // normalized code, strings intact
		strs   string // the line's string literals
		block  int    // replacement block (rulekit.Pairs index)
		used   bool
		path   []string // c.scope's result, computed on demand
		scoped bool
	}
	var removed, added []*side
	for _, f := range files {
		oldBlock, newBlock := blocks(f)
		for _, l := range f.Removed {
			for _, o := range detect(f, l, oldVersion) {
				removed = append(removed, &side{occurrence: o, v: oldVersion, key: lineKey(l), strs: strings.Join(l.Strings, "\x00"), block: oldBlock[l.No]})
			}
		}
		for _, l := range f.Added {
			for _, o := range detect(f, l, newVersion) {
				added = append(added, &side{occurrence: o, v: newVersion, key: lineKey(l), strs: strings.Join(l.Strings, "\x00"), block: newBlock[l.No]})
			}
		}
	}
	if len(added) == 0 {
		return nil
	}
	cancel := func(a *side, match func(r *side) bool) {
		for _, r := range removed {
			if !r.used && match(r) && (c.compatible == nil || c.compatible(r.occurrence, a.occurrence)) {
				r.used, a.used = true, true
				return
			}
		}
	}
	pathOf := func(s *side) []string {
		if !s.scoped {
			s.path, s.scoped = c.scope(s.occurrence, s.v), true
		}
		return s.path
	}
	// sameSuites reports whether two occurrences sit in the same suites, when
	// scope can tell: `it.skip('works')` moved from describe('a') to
	// describe('b') skips a different test that happens to share its name.
	sameSuites := func(a, r *side) bool {
		if c.scope == nil {
			return true
		}
		ap, rp := pathOf(a), pathOf(r)
		return len(ap) > 0 && len(rp) > 0 && equalStrings(ap[1:], rp[1:])
	}
	// Strictest survivors first, so they are not consumed by looser matches:
	// the same code in the same file, then the same file with the same
	// string literals (the same test, edited), then the same file and
	// replacement block, and only then the same code in another file (moved
	// code). A marker edited in place must not lose its removed half to an
	// identical line that the change adds to another file: a gate rewritten
	// across a suite while a new helper test copies the old gate.
	passes := []func(a, r *side) bool{
		// The same code in the same test first: a test that moved keeps its
		// marker even when a new test next to it copies the same line.
		func(a, r *side) bool {
			if r.Kind != a.Kind || r.key != a.key || c.scope == nil {
				return false
			}
			ap := pathOf(a)
			return len(ap) > 0 && ap[0] != "" && equalStrings(ap, pathOf(r))
		},
		func(a, r *side) bool { return r.Kind == a.Kind && r.key == a.key && r.File == a.File },
		func(a, r *side) bool {
			return r.Kind == a.Kind && r.File == a.File && r.strs != "" && r.strs == a.strs && sameSuites(a, r)
		},
		func(a, r *side) bool {
			return r.Kind == a.Kind && r.File == a.File && a.block != 0 && r.block == a.block
		},
		func(a, r *side) bool { return r.Kind == a.Kind && r.key == a.key },
	}
	if c.scope != nil {
		passes = append(passes, func(a, r *side) bool { return r.File == a.File && within(pathOf(r), pathOf(a)) })
	}
	for _, match := range passes {
		for _, a := range added {
			if !a.used {
				cancel(a, func(r *side) bool { return match(a, r) })
			}
		}
	}
	var out []occurrence[T]
	for _, a := range added {
		if !a.used {
			out = append(out, a.occurrence)
		}
	}
	return out
}

func lineKey(l core.Line) string { return rulekit.Normalize(l.NoComment) }

// blocks maps old and new line numbers to the index of their replacement
// block (see rulekit.Pairs).
func blocks(f *core.FileInfo) (old, new map[int]int) {
	old, new = map[int]int{}, map[int]int{}
	for i, p := range rulekit.Pairs(f) {
		for _, l := range p.Removed {
			old[l.No] = i + 1
		}
		for _, l := range p.Added {
			new[l.No] = i + 1
		}
	}
	return old, new
}

// scopeFunc returns where an occurrence applies, innermost first: the test
// or suite it disables, then the suites around that, out to the file. An
// empty first element means the test is unknown; fileScope means the whole
// file.
type scopeFunc[T any] func(o occurrence[T], v version) []string

// fileScope is the scope of a marker that applies to every test in a file.
const fileScope = "\x00file"

// within reports whether the removed occurrence with scope r already
// covered the place of the added occurrence with scope a: r applied to a
// itself or to a suite around it, at the same position in the suite tree.
func within(r, a []string) bool {
	if len(r) == 0 || r[0] == "" {
		return false
	}
	if r[0] == fileScope {
		return true
	}
	// Both paths run out to the file, so r's suites must be a's outermost.
	i := len(a) - len(r)
	return i >= 0 && a[i] == r[0] && equalStrings(a[i+1:], r[1:])
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
