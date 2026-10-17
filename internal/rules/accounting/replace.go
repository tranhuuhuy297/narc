package accounting

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
)

// A test is often rewritten where it stands: the change renames it after
// the behavior it now checks and replaces most of its body, so neither its
// name nor its body identifies it any more. The diff still does: the hunk
// that removed the old test's header added the new test's header, and the
// two occupy the same stretch of the file.

// matchInPlace pairs each vanished old test with a new test written in its
// place (see above) that is related to it (a shared name word, or a
// somewhat similar body) and checks something. The pair is then compared
// like any other match, so assertions the rewrite dropped are still
// accounted for (as a rewrite).
func (l *ledger) matchInPlace() {
	news := map[*core.FileInfo][]*instance{}
	for _, n := range l.news {
		if n.match == nil && !n.partial && n.file.Diff.Kind != diff.Added {
			news[n.file] = append(news[n.file], n)
		}
	}
	if len(news) == 0 {
		return
	}
	type hunks struct{ added, removed map[int]int }
	byFile := map[*core.FileInfo]hunks{}
	hunksOf := func(f *core.FileInfo) hunks {
		h, ok := byFile[f]
		if !ok {
			h = hunks{added: map[int]int{}, removed: map[int]int{}}
			for _, ln := range f.Added {
				h.added[ln.No] = ln.Hunk
			}
			for _, ln := range f.Removed {
				h.removed[ln.No] = ln.Hunk
			}
			byFile[f] = h
		}
		return h
	}
	var cs []candidate
	for _, o := range l.olds {
		if o.match != nil || o.partial || o.former != nil || o.skipped() || len(news[o.file]) == 0 {
			continue
		}
		h := hunksOf(o.file)
		hunk, removed := h.removed[o.header()]
		if !removed {
			continue
		}
		from, to := newPos(o.file, o.line(o.block.Start)), newPos(o.file, o.line(o.block.End))
		for _, n := range news[o.file] {
			if nh, added := h.added[n.header()]; !added || nh != hunk {
				continue
			}
			nFrom, nTo := n.line(n.block.Start), n.line(n.block.End)
			overlap := min(to, nTo) - max(from, nFrom) + 1
			if overlap <= 0 {
				continue
			}
			sim := dice(o, n)
			if sim < 0.3 && !shareNameWord(o.block.Bare, n.block.Bare) {
				continue
			}
			if n.assertions()+l.helperCalls(n) == 0 {
				continue // an empty test in its place: hollowed, not rewritten
			}
			cs = append(cs, candidate{o, n, float64(overlap) + sim})
		}
	}
	linkBest(cs, inPlace)
}

// newPos maps an old-side line number of f to the new side: the same line
// when it survived, else the place where it was removed.
func newPos(f *core.FileInfo, oldNo int) int {
	delta := 0
	for _, h := range f.Diff.Hunks {
		if oldNo < h.OldStart {
			break
		}
		last := h.NewStart - 1
		for _, ln := range h.Lines {
			if ln.NewNo > 0 {
				last = ln.NewNo
			}
			if ln.OldNo == oldNo {
				if ln.NewNo > 0 {
					return ln.NewNo
				}
				return last + 1
			}
		}
		delta = (h.NewStart + h.NewLines) - (h.OldStart + h.OldLines)
	}
	return oldNo + delta
}

// nameWordRe splits test names into words (camelCase humps included).
var nameWordRe = regexp.MustCompile(`[A-Z]?[a-z]+|[A-Z]+(?:[a-z]+)?|\d+`)

// nameStop are words too common in test names to relate two tests.
var nameStop = map[string]bool{"test": true, "tests": true, "should": true, "when": true, "with": true,
	"without": true, "returns": true, "return": true, "handles": true, "handle": true, "that": true,
	"from": true, "into": true, "does": true, "works": true, "correctly": true, "properly": true, "case": true,
	"cases": true, "given": true, "then": true, "this": true, "valid": true, "invalid": true, "error": true,
	"errors": true, "data": true, "value": true, "values": true, "able": true, "have": true, "uses": true}

// shareNameWord reports whether two test names share a word that says
// what they are about (four letters or more, not a filler word).
func shareNameWord(a, b string) bool {
	words := map[string]bool{}
	for _, w := range nameWordRe.FindAllString(a, -1) {
		if w = strings.ToLower(w); len(w) >= 4 && !nameStop[w] {
			words[strings.TrimSuffix(w, "s")] = true
		}
	}
	for _, w := range nameWordRe.FindAllString(b, -1) {
		if w = strings.ToLower(w); len(w) >= 4 && !nameStop[w] && words[strings.TrimSuffix(w, "s")] {
			return true
		}
	}
	return false
}

// newTestsIn counts, per file, the tests that are new in the change: not
// matched to any old test.
func (l *ledger) newTestsIn() map[*core.FileInfo]int {
	if l.fresh == nil {
		l.fresh = map[*core.FileInfo]int{}
		for _, n := range l.news {
			if n.match == nil && !n.skipped() {
				l.fresh[n.file]++
			}
		}
	}
	return l.fresh
}

// reworkIn counts the tests of file f that the change wrote anew: new
// tests, tests rewritten in place of old ones, and renamed tests that
// gained assertions (a parametrized test absorbing several old ones).
func (l *ledger) reworkIn(f *core.FileInfo) int {
	n := l.newTestsIn()[f]
	for _, t := range l.news {
		if t.file != f || t.match == nil {
			continue
		}
		if t.how == inPlace || t.how == renamed && t.assertions() > t.match.assertions() {
			n++
		}
	}
	return n
}
