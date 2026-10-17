package accounting

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
)

// Deleted tests are graded one by one (deletionVerdict) and then reported
// in groups, so that a change that prunes a suite reads as one finding
// rather than hundreds:
//
//   - errors (the "break the code, delete the test" move) once per file;
//   - warnings once per change when they span several files (a pruning
//     pass), else once per file;
//   - info once per file and reason.
//
// A lone deletion keeps its own message, which names the test.

// verdict is the grade of one deleted test.
type verdict struct {
	o   *instance
	sev core.Severity
	why string // appended to the message
}

func checkTestDeleted(a *core.Analysis) []core.Finding {
	l := ledgerFor(a)
	var out []core.Finding
	var vs []verdict
	for _, o := range l.deletions {
		if l.fileGone(o.file) {
			continue // reported per file by test-file-deleted
		}
		if fm := o.former; fm != nil {
			// Switched off rather than deleted: anchored on the function
			// that is left, where a waiver goes.
			fd := testDeleted.Finding(o.file, fm.line, formerMessage(o))
			if ln, ok := o.file.OldLine(o.header()); ok {
				fd.Before = strings.TrimSpace(ln.Raw)
			}
			fd.Hint = formerHint
			l.escalate(&fd)
			out = append(out, fd)
			continue
		}
		sev, why := l.deletionVerdict(o)
		vs = append(vs, verdict{o, sev, why})
	}
	vs = l.foldShrunk(vs, &out)
	return append(out, l.groupDeletions(vs)...)
}

// deletionVerdict grades a deleted test that was not found again.
//
//   - info when the deletion reads as a rework or a removal: an excuse
//     (see excuse), a test that asserted nothing, source edits that are
//     mostly deletions, new tests written in the same file, tests the
//     change edited that still call the code it exercised, a new test
//     whose title reads like this one's, a flag or option key the change
//     removed from source code, or a deleted module the file stopped
//     importing;
//   - an error when the change edited production code the test exercised
//     (it calls a function the change modified, or its file is named after
//     a modified source file): the "break the code, delete the test" move;
//   - else a warning: no source change at all, or source edits the test
//     does not exercise.
func (l *ledger) deletionVerdict(o *instance) (core.Severity, string) {
	if why := l.excuse(o); why != "" {
		return core.Info, why
	}
	if o.assertions() == 0 && l.helperCalls(o) == 0 {
		return core.Info, "; it asserted nothing"
	}
	if len(l.sourceChanged) == 0 {
		return core.Warning, ""
	}
	src := l.sourceList()
	switch {
	case l.removesSource():
		return core.Info, l.removalNote(src)
	case l.reworkIn(o.file) > 0:
		return core.Info, fmt.Sprintf("; the same change wrote %s in this file", plural(l.reworkIn(o.file), "new test"))
	case l.testedElsewhere(o):
		return core.Info, "; tests the same change edited still call the code it exercised"
	case l.lookalike(o) != nil:
		return core.Info, fmt.Sprintf("; the same change wrote %q, which reads like its replacement", l.lookalike(o).name())
	case l.retiredKey(o) != "":
		return core.Info, fmt.Sprintf("; it uses %q, which the same change removed from source code", l.retiredKey(o))
	case l.droppedGoneModule(o.file) != "":
		return core.Info, fmt.Sprintf("; the same change deleted %s, which this file used", l.droppedGoneModule(o.file))
	case l.refsTouched(o) || l.coversModified(o.file):
		return core.Error, fmt.Sprintf(" while the same change modified source code (%s)", src)
	}
	return core.Warning, fmt.Sprintf(" while the same change modified source code it does not exercise (%s)", src)
}

// groupDeletions turns verdicts into findings (see the top of this file).
func (l *ledger) groupDeletions(vs []verdict) []core.Finding {
	type key struct {
		sev  core.Severity
		why  string
		file *core.FileInfo // nil: the whole change
	}
	groups := map[key][]verdict{}
	var order []key
	add := func(k key, v verdict) {
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], v)
	}
	// Warnings that share a reason across several files are one finding.
	spread := map[string]map[*core.FileInfo]bool{}
	for _, v := range vs {
		if v.sev == core.Warning {
			if spread[v.why] == nil {
				spread[v.why] = map[*core.FileInfo]bool{}
			}
			spread[v.why][v.o.file] = true
		}
	}
	for _, v := range vs {
		k := key{v.sev, v.why, v.o.file}
		if v.sev == core.Warning && len(spread[v.why]) > 1 {
			k.file = nil
		}
		add(k, v)
	}
	var out []core.Finding
	for _, k := range order {
		g := groups[k]
		first := g[0].o
		if len(g) == 1 {
			fd := testDeleted.Removal(first.file, first.header(), fmt.Sprintf("test %q was deleted%s%s", first.name(), assertionNote(first), k.why))
			fd.Severity = k.sev
			out = append(out, fd)
			continue
		}
		asserts := 0
		files := map[*core.FileInfo]bool{}
		var names []string
		for _, v := range g {
			asserts += v.o.assertions()
			files[v.o.file] = true
			names = append(names, fmt.Sprintf("%q", v.o.name()))
		}
		where := ""
		if len(files) > 1 {
			where = fmt.Sprintf(" from %d test files", len(files))
		}
		msg := fmt.Sprintf("%d tests were deleted%s (%s): %s%s", len(g), where, plural(asserts, "assertion"), list(names, 4), k.why)
		fd := testDeleted.Removal(first.file, first.header(), msg)
		fd.Severity = k.sev
		out = append(out, fd)
	}
	return out
}

// foldShrunk reports test files that a rework left with far fewer tests,
// while the source file they are named after was edited: most of the old
// tests vanished (deleted, or retired with code the change removed) and
// only a few were written in their place. The behaviors the old tests
// covered may now be untested even though each deletion has an
// explanation of its own, so the file gets one warning, which absorbs the
// file's individual deletion findings. It returns the remaining verdicts.
func (l *ledger) foldShrunk(vs []verdict, out *[]core.Finding) []verdict {
	if len(l.sourceChanged) == 0 || l.removesSource() {
		return vs
	}
	gone := map[*core.FileInfo][]*instance{}
	for _, o := range l.olds {
		if o.match == nil && !o.skipped() && !o.partial && o.former == nil && !l.fileGone(o.file) {
			gone[o.file] = append(gone[o.file], o)
		}
	}
	var files []*core.FileInfo
	for f := range gone {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	shrunk := map[*core.FileInfo]bool{}
	for _, f := range files {
		var lost []*instance
		deleted := map[*instance]bool{}
		for _, v := range vs {
			if v.o.file == f {
				deleted[v.o] = true
			}
		}
		for _, o := range gone[f] {
			if deleted[o] || o.obsolete {
				lost = append(lost, o)
			}
		}
		written := 0 // tests written anew: unmatched, renamed or rewritten in place
		for _, n := range l.news {
			if n.file == f && !n.skipped() && (n.match == nil || n.how == renamed || n.how == inPlace) {
				written++
			}
		}
		if len(lost) < 6 || written == 0 || 3*written > len(lost) || !l.coversModified(f) {
			continue
		}
		shrunk[f] = true
		asserts := 0
		var names []string
		for _, o := range lost {
			asserts += o.assertions()
			names = append(names, fmt.Sprintf("%q", o.name()))
		}
		wrote := plural(written, "new test") + " were written"
		if written == 1 {
			wrote = "1 new test was written"
		}
		msg := fmt.Sprintf("the test file lost most of its tests in a rewrite: %d old tests (%s) are gone and only %s in their place, while the same change modified the source file they cover (%s); behavior the old tests checked may now be untested: %s",
			len(lost), plural(asserts, "assertion"), wrote, l.coveredSource(f), list(names, 4))
		first := lost[0]
		fd := testDeleted.Removal(first.file, first.header(), msg)
		fd.Severity = core.Warning
		*out = append(*out, fd)
	}
	if len(shrunk) == 0 {
		return vs
	}
	var rest []verdict
	for _, v := range vs {
		if !shrunk[v.o.file] {
			rest = append(rest, v)
		}
	}
	return rest
}

// coveredSource returns the modified production file that test file f is
// named after (see coversModified).
func (l *ledger) coveredSource(f *core.FileInfo) string {
	s := stem(f.Path, true)
	for _, g := range l.a.Files {
		if l.productionCode(g) && stem(g.Path, false) == s {
			return g.Path
		}
	}
	return s
}

// jsMockRe captures the module specifier of a jest/vitest module mock.
var jsMockRe = regexp.MustCompile(`\b(?:vi|jest)\s*\.\s*(?:mock|doMock)\s*\(\s*['"]([^'"]+)['"]`)

// droppedGoneModule returns a module that the change deleted and that
// modified test file f stopped importing or mocking (`vi.mock('./Footer')`
// next to a deleted Footer.tsx), or "": tests of f that went away probably
// covered what that module did.
func (l *ledger) droppedGoneModule(f *core.FileInfo) string {
	if v, ok := l.goneMods[f]; ok {
		return v
	}
	l.goneMods[f] = ""
	gone := l.source().gone
	if len(gone) == 0 {
		return ""
	}
	dir := path.Dir(f.Path)
	lg := langOf(f, before)
	for _, ln := range f.Removed {
		if !strings.Contains(ln.Code, "import") && !strings.Contains(ln.Code, "mock") && !importLineRe.MatchString(ln.Code) {
			continue
		}
		specs := importSpecs(lg, ln.LexedLine)
		if lg.IsJSLike() {
			for _, m := range jsMockRe.FindAllStringSubmatch(ln.NoComment, -1) {
				specs = append(specs, m[1])
			}
		}
		for _, spec := range specs {
			if strings.HasPrefix(spec, ".") {
				if gone[modulePath(path.Join(dir, spec))] {
					l.goneMods[f] = spec
					return spec
				}
			} else if goneSuffix(gone, spec) {
				l.goneMods[f] = spec
				return spec
			}
		}
	}
	return ""
}

// lookalike returns a test the change wrote (new, renamed or rewritten in
// place, in any file) whose title says much the same as deleted test o's:
// at least three of their title words are shared, and the shared words
// make up at least half of the words of both titles together ("throws
// ForbiddenError when the API key lacks workflow:import scope" and
// "rejects import when the API key lacks workflow:import scope"), or, in
// the same file, the new title keeps every word of the old one. The test
// was most likely moved and reworded, often along with the code it
// covers.
func (l *ledger) lookalike(o *instance) *instance {
	if v, ok := l.alike[o]; ok {
		return v
	}
	var best *instance
	bestScore := 0.0
	ow := l.titleWordsOf(o)
	if len(ow) >= 2 {
		for _, n := range l.news {
			if n.skipped() || n.match != nil && n.how != renamed && n.how != inPlace {
				continue
			}
			nw := l.titleWordsOf(n)
			shared := 0
			for w := range ow {
				if nw[w] {
					shared++
				}
			}
			union := len(ow) + len(nw) - shared
			// Shared words make up most of both titles, or the new title
			// keeps every word of the old one (test_auto_returns_preprod →
			// test_auto_returns_preprod_snapshots).
			alike := shared >= 3 && 2*shared >= union || shared == len(ow) && n.file == o.file
			if score := float64(shared) / float64(union); alike && score > bestScore {
				best, bestScore = n, score
			}
		}
	}
	l.alike[o] = best
	return best
}

// titleWordsOf returns titleWords of a test's own name (cached).
func (l *ledger) titleWordsOf(in *instance) map[string]bool {
	w, ok := l.words[in]
	if !ok {
		w = titleWords(in.block.Bare)
		l.words[in] = w
	}
	return w
}

// titleWords returns the distinctive words of a test title, lowercased.
func titleWords(name string) map[string]bool {
	out := map[string]bool{}
	for _, w := range nameWordRe.FindAllString(name, -1) {
		if w = strings.ToLower(w); len(w) >= 3 && !nameStop[w] && !titleStop[w] {
			out[w] = true
		}
	}
	return out
}

// titleStop are short filler words of test titles.
var titleStop = map[string]bool{"the": true, "and": true, "for": true, "not": true, "its": true, "are": true,
	"has": true, "but": true, "all": true, "any": true, "one": true, "can": true, "via": true, "per": true}
