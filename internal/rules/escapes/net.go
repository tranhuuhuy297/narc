package escapes

import (
	"sort"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// occ is one occurrence of a rule's pattern on an added line.
type occ[T any] struct {
	file *core.FileInfo
	line core.Line
	v    T
	norm *string // the normalized line, shared by the line's occurrences
}

// netNew returns the pattern occurrences a change introduced. scan finds
// the occurrences on one line (removed lines have old set, so a scanner can
// look at the lines around it with sideLine); keys gives each one's
// identity: keys[0] is what it is, further keys name removed occurrences it
// may replace without counting as new (e.g. @ts-expect-error replacing
// @ts-ignore is an improvement, not a new escape).
//
// An added occurrence is cancelled by a removed one with a matching key in
// the same file (moved or re-indented code), or else by a removed line
// anywhere in the changeset that is identical after normalization (code
// moved to another file).
func netNew[T any](files []*core.FileInfo, scan func(f *core.FileInfo, l core.Line, old bool) []T, keys func(T) []string) []occ[T] {
	type moved struct{ line, key string }
	pool := map[moved]int{}
	var survivors []occ[T]
	for _, f := range files {
		removed := map[string]int{}
		var removedLines []moved
		for _, l := range f.Removed {
			l.LexedLine = withoutBOM(l.No, l.LexedLine)
			norm := ""
			for i, v := range scan(f, l, true) {
				if i == 0 {
					norm = rulekit.Normalize(l.Raw)
				}
				k := keys(v)[0]
				removed[k]++
				removedLines = append(removedLines, moved{norm, k})
			}
		}
		used := map[string]int{}
		for _, l := range f.Added {
			l.LexedLine = withoutBOM(l.No, l.LexedLine)
			norm := new(string)
			for _, v := range scan(f, l, false) {
				if k, ok := take(removed, keys(v)); ok {
					used[k]++
					continue
				}
				survivors = append(survivors, occ[T]{f, l, v, norm})
			}
		}
		// Removed occurrences that nothing in this file replaced may still
		// account for code that moved to another file.
		for _, r := range removedLines {
			if used[r.key] > 0 {
				used[r.key]--
				continue
			}
			pool[r]++
		}
	}
	if len(pool) == 0 {
		return survivors
	}
	out := survivors[:0]
	for _, o := range survivors {
		if *o.norm == "" {
			*o.norm = rulekit.Normalize(o.line.Raw)
		}
		if k := (moved{*o.norm, keys(o.v)[0]}); pool[k] > 0 {
			pool[k]--
			continue
		}
		out = append(out, o)
	}
	return out
}

func take(counts map[string]int, keys []string) (string, bool) {
	for _, k := range keys {
		if counts[k] > 0 {
			counts[k]--
			return k, true
		}
	}
	return "", false
}

// construct is a pattern that may span several lines of one file version.
type construct interface {
	lines() (start, end int)
	ident() string
}

// found is a construct the change introduced, with its file.
type found[T construct] struct {
	file *core.FileInfo
	side *side
	v    T
}

// newConstructs returns the constructs a change introduced. find locates
// constructs in one version of a file. A construct in the new version
// counts when it overlaps a changed line and its file now has more
// constructs with the same identity than before; surplus that is balanced
// by constructs that disappeared from other files (moved code) is dropped.
func newConstructs[T construct](files []*core.FileInfo, find func(*core.FileInfo, *side) []T) []found[T] {
	deficit := map[string]int{}
	var kept []found[T]
	for _, f := range files {
		olds, news := oldSide(f), newSide(f)
		before := map[string]int{}
		for _, c := range find(f, olds) {
			before[c.ident()]++
		}
		after := find(f, news)
		count := map[string]int{}
		for _, c := range after {
			count[c.ident()]++
		}
		for k, n := range before {
			if n > count[k] {
				deficit[k] += n - count[k]
			}
		}
		ch := changedLines(f)
		touched := func(c T) (bool, bool) {
			s, e := c.lines()
			for n := s; n <= e; n++ {
				if ch[n] {
					return true, ch[s]
				}
			}
			return false, false
		}
		var cand []T
		var headChanged []bool
		for _, c := range after {
			if t, head := touched(c); t {
				cand = append(cand, c)
				headChanged = append(headChanged, head)
			}
		}
		// Prefer constructs whose first line is new when only some of the
		// touched ones can be new.
		idx := make([]int, len(cand))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool { return headChanged[idx[i]] && !headChanged[idx[j]] })
		surplus := map[string]int{}
		for k, n := range count {
			if n > before[k] {
				surplus[k] = n - before[k]
			}
		}
		var mine []found[T]
		for _, i := range idx {
			c := cand[i]
			if surplus[c.ident()] > 0 {
				surplus[c.ident()]--
				mine = append(mine, found[T]{f, news, c})
			}
		}
		sort.SliceStable(mine, func(i, j int) bool {
			a, _ := mine[i].v.lines()
			b, _ := mine[j].v.lines()
			return a < b
		})
		kept = append(kept, mine...)
	}
	out := kept[:0]
	for _, k := range kept {
		if deficit[k.v.ident()] > 0 {
			deficit[k.v.ident()]--
			continue
		}
		out = append(out, k)
	}
	return out
}
