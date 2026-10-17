package accounting

import (
	"regexp"
	"sort"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/testblocks"
)

// matchKind says how an old test was found again in the new code.
type matchKind int

const (
	unmatched matchKind = iota
	byName              // same file, same qualified name
	byBare              // same file, same name under a different suite
	moved               // same name, another file
	renamed             // another name, (nearly) the same body
	inPlace             // another name and body, written where the old test was
)

// ledger is the test accounting of one change: every test before and
// after, how they correspond, and what was lost. It is computed once per
// analysis and shared by the four rules.
type ledger struct {
	olds, news []*instance

	// netLoss counts assertion statements that exist fewer times after the
	// change than before, across all files. An assertion removed from one
	// test but present in another test or helper after the change was
	// moved, not lost.
	netLoss map[string]int
	// pool holds the tokens of added test code and of new tests, used to
	// recognize assertions folded into tables or merged assertions.
	pool tokenPool
	// removedDefs are functions/types deleted from source code; a test of
	// deleted code may be deleted with it.
	removedDefs map[string]bool
	// deletedStems are the stems of deleted source files.
	deletedStems map[string]bool
	// helpers counts the assertions of functions defined after the change,
	// by name, so that a test calling balanceIs(t, a, 150) is credited.
	helpers map[string]int
	// defined holds every function and type defined in the change's code
	// after the change (see definedNames).
	defined map[string]bool
	// sourceChanged lists modified non-test source files;
	// sourceAdded/sourceRemoved count their added/removed lines of code.
	sourceChanged              []string
	sourceAdded, sourceRemoved int

	deletions []*instance // old tests that disappeared
	drops     []drop      // surviving tests that lost assertions

	// renamedTo maps a test file that the test runner no longer collects
	// (deleted and re-added, or renamed, under a non-test name) to its new
	// path; uncollected holds those new paths.
	renamedTo   map[*core.FileInfo]string
	uncollected map[string]bool
	// inline caches rustInlineTests per file, segs the segments of each
	// side of a file, imports the import lines of each file.
	inline    map[*core.FileInfo]bool
	segs      map[segKey][]*segment
	imports   map[*core.FileInfo][]lang.LexedLine
	framework map[segKey]bool
	withTests map[segKey]bool

	// src describes the change's edits to production code (see source);
	// fresh counts the new, unmatched tests per file (see newTestsIn).
	src    *sourceModel
	fresh  map[*core.FileInfo]int
	locals map[*core.FileInfo]map[string]map[string]bool
	// edited holds the functions called by tests the change wrote or
	// edited (see testedElsewhere).
	edited map[string]bool
	// titles holds the string literals of added test code: the case
	// titles of it.each / parametrize tables, among others.
	titles map[string]bool

	// a gives access to unchanged files (helpers the change did not touch).
	a *core.Analysis
	// external caches the assertion helpers found in unchanged files, by
	// test file; nearby the tests of unchanged test files, by directory.
	external map[string]map[string]int
	nearby   map[string]map[string][]copyTest
}

// drop is a surviving test that lost assertions.
type drop struct {
	old, new *instance
	lost     []site // assertions that vanished from the change entirely
	// rewritten: the test gained assertions it did not have, or its body
	// changed substantially, so the drop may be a rework, not a deletion.
	rewritten bool
	// atLeast: the old test was cut off by the end of a hunk, so its
	// assertion count is a lower bound.
	atLeast bool
	// helped counts the assertions the new test makes through helpers;
	// via names a helper with assertions that the old test did not call,
	// to which the test now hands its checks.
	helped int
	via    string
}

// ledgerKey is the Analysis.Memo key of the ledger.
type ledgerKey struct{}

// ledgerFor returns the ledger of an analysis. The engine runs all rules
// over the same *core.Analysis, so the work is done once.
func ledgerFor(a *core.Analysis) *ledger {
	return a.Memo(ledgerKey{}, func() any { return build(a) }).(*ledger)
}

// definedNames returns every function and type defined in the changed
// files after the change. It is computed on first use: most changes never
// need it.
func (l *ledger) definedNames() map[string]bool {
	if l.defined == nil {
		l.defined = map[string]bool{}
		for _, f := range l.a.Files {
			if !f.Lang.IsProgramming() {
				continue
			}
			for _, ln := range f.Added {
				definitions(f.Lang, ln.Code, l.defined)
			}
			for _, ll := range f.NewLines() {
				definitions(f.Lang, ll.Code, l.defined)
			}
		}
	}
	return l.defined
}

// rustInline reports a Rust source file with inline unit tests (cached).
func (l *ledger) rustInline(f *core.FileInfo) bool {
	v, ok := l.inline[f]
	if !ok {
		v = l.rustInlineTests(f)
		l.inline[f] = v
	}
	return v
}

// testOn reports whether one side of a file is test code. A file renamed
// from a test path to another path is a test only on its old side: the
// test runner no longer collects it under its new name.
func (l *ledger) testOn(f *core.FileInfo, sd side) bool {
	if f.IsTest || l.rustInline(f) {
		return true
	}
	p := f.Path
	if sd == before {
		if f.OldPath == "" {
			return false
		}
		p = f.OldPath
		if p != f.Path && (core.IsTestPath(p) || core.MatchAny(l.a.Settings.TestGlobs, p)) {
			return true
		}
	}
	// Tests in a file that narc does not classify but whose directory or
	// name mentions tests (Kotlin's src/jvmTest/kotlin, cal.com's
	// *.integration-test.ts) get the benefit of the doubt: the test runner
	// may well be configured to collect them.
	return testishPath(p) && l.hasTests(f, sd)
}

// hasTests reports whether one side of a file contains tests (cached).
func (l *ledger) hasTests(f *core.FileInfo, sd side) bool {
	k := segKey{f, sd}
	if v, ok := l.withTests[k]; ok {
		return v
	}
	v := false
	if testblocks.Supported(langOf(f, sd)) {
		for _, seg := range l.segments(f, sd) {
			v = v || len(testblocks.Find(seg.lang, seg.lines)) > 0
		}
	}
	l.withTests[k] = v
	return v
}

// scanned reports whether the tests on one side of a file are accounted for.
func (l *ledger) scanned(f *core.FileInfo, sd side) bool {
	return testblocks.Supported(langOf(f, sd)) && l.testOn(f, sd)
}

// testSide reports whether the assertions on one side of a file verify
// something: test code, test-support code (by path, or because it imports
// a test framework), or a Rust file with inline tests. An assertion that
// moved into production code, or into a file the test runner does not
// collect, no longer checks anything.
func (l *ledger) testSide(f *core.FileInfo, sd side) bool {
	p := f.Path
	if sd == before && f.OldPath != "" {
		p = f.OldPath
	}
	if l.uncollected[f.Path] && sd == after {
		return false
	}
	return l.testOn(f, sd) || testSupport(p) || l.importsTestFramework(f, sd)
}

// productionCode reports a file whose changes count as source changes:
// non-test code, or Rust code outside its inline tests. A test file
// renamed to a name the test runner does not collect, and test-support
// code that imports a test framework, are not source code.
func (l *ledger) productionCode(f *core.FileInfo) bool {
	if l.uncollected[f.Path] {
		return false
	}
	if l.rustInline(f) {
		return l.changesOutsideTests(f)
	}
	if !isSource(f) {
		return false
	}
	sd := after
	if f.Diff.Kind == diff.Deleted {
		sd = before
	}
	return !l.testOn(f, sd) && !l.importsTestFramework(f, sd)
}

// frameworkImportRe matches an import of a test framework or assertion
// library. A non-test file with one is test-support code (fixtures,
// custom matchers, assertion helpers). Node's plain assert module is left
// out: production code uses it for invariants.
var frameworkImportRe = regexp.MustCompile(`(?:\bfrom\s*|\brequire\s*\(\s*|^\s*import\s+)['"](?:vitest|@jest/globals|chai|@playwright/test|@testing-library/[\w-]+|node:test|uvu|ava|tap|supertest)['"]` +
	`|^\s*(?:import|from)\s+(?:pytest|unittest|hypothesis)\b` +
	`|^\s*(?:import\s+)?(?:\w+\s+)?"testing"\s*$` +
	`|^\s*(?:import|using)\s+(?:static\s+)?(?:org\.junit|org\.assertj|org\.hamcrest|org\.testng|io\.kotest|kotlin\.test|Xunit|NUnit|Microsoft\.VisualStudio\.TestTools|FluentAssertions|XCTest|Testing)\b` +
	`|PHPUnit\\Framework|^\s*require\s+['"](?:rspec|minitest)|package:(?:test|flutter_test)/|\bExUnit\b`)

// importsTestFramework reports whether one side of a file imports a test
// framework near its top (cached).
func (l *ledger) importsTestFramework(f *core.FileInfo, sd side) bool {
	k := segKey{f, sd}
	if v, ok := l.framework[k]; ok {
		return v
	}
	v := false
	for _, seg := range l.segments(f, sd) {
		for i, ll := range seg.lines {
			if i >= 120 {
				break
			}
			if frameworkImportRe.MatchString(ll.NoComment) {
				v = true
			}
		}
	}
	l.framework[k] = v
	return v
}

// changesOutsideTests reports whether a Rust file with inline tests changed
// lines outside its tests and above its #[cfg(test)] module (when the
// module header is visible).
func (l *ledger) changesOutsideTests(f *core.FileInfo) bool {
	inTests := func(ins []*instance, sd side, no int) bool {
		for _, in := range ins {
			if in.file == f && in.side == sd && in.line(in.block.Start) <= no && no <= in.line(in.block.End) {
				return true
			}
		}
		return false
	}
	module := func(sd side) int {
		for _, seg := range l.segments(f, sd) {
			for i, ll := range seg.lines {
				if strings.Contains(ll.Code, "#[cfg(test)]") {
					return seg.lineNo(i)
				}
			}
		}
		return 1 << 30
	}
	oldModule, newModule := module(before), module(after)
	for _, ln := range f.Removed {
		if strings.TrimSpace(ln.Code) != "" && ln.No < oldModule && !inTests(l.olds, before, ln.No) {
			return true
		}
	}
	for _, ln := range f.Added {
		if strings.TrimSpace(ln.Code) != "" && ln.No < newModule && !inTests(l.news, after, ln.No) {
			return true
		}
	}
	return false
}

func build(a *core.Analysis) *ledger {
	l := &ledger{netLoss: map[string]int{}, pool: tokenPool{}, removedDefs: map[string]bool{}, helpers: map[string]int{},
		deletedStems: map[string]bool{}, a: a, external: map[string]map[string]int{}, nearby: map[string]map[string][]copyTest{},
		renamedTo: map[*core.FileInfo]string{}, uncollected: map[string]bool{}, inline: map[*core.FileInfo]bool{},
		segs: map[segKey][]*segment{}, imports: map[*core.FileInfo][]lang.LexedLine{}, framework: map[segKey]bool{},
		withTests: map[segKey]bool{}, locals: map[*core.FileInfo]map[string]map[string]bool{}}
	order := 0
	var testFiles []*core.FileInfo // files with test code after the change
	oldKeys, newKeys := map[string]int{}, map[string]int{}
	for _, f := range a.Files {
		if l.scanned(f, before) {
			l.olds = append(l.olds, l.collect(f, before, &order)...)
		}
		if l.scanned(f, after) {
			l.news = append(l.news, l.collect(f, after, &order)...)
			testFiles = append(testFiles, f)
		}
	}
	if len(l.olds) == 0 {
		return l // no test existed before the change: nothing can be lost
	}
	l.findUncollected()
	for _, f := range a.Files {
		if langOf(f, before).IsProgramming() && l.testSide(f, before) {
			l.assertionKeys(f, before, oldKeys)
		}
		if f.Lang.IsProgramming() && l.testSide(f, after) {
			l.assertionKeys(f, after, newKeys)
			l.assertionHelpers(f, after, l.helpers)
		}
		if l.productionCode(f) {
			inline := l.rustInline(f)
			for _, ln := range f.Removed {
				if !inline || !l.inOldTest(f, ln.No) { // inline tests are not production code
					definitions(f.Lang, ln.Code, l.removedDefs)
					if strings.TrimSpace(ln.Code) != "" {
						l.sourceRemoved++
					}
				}
			}
			for _, ln := range f.Added {
				if strings.TrimSpace(ln.Code) != "" && (!inline || !l.inNewTest(f, ln.No)) {
					l.sourceAdded++
				}
			}
			if f.Diff.Kind == diff.Deleted {
				l.deletedStems[stem(f.Path, false)] = true
			}
			l.sourceChanged = append(l.sourceChanged, f.Path)
		}
	}
	if len(l.removedDefs) > 0 {
		defined := l.definedNames()
		for k := range l.removedDefs {
			if defined[k] {
				delete(l.removedDefs, k) // still defined (edited or moved)
			}
		}
	}
	for k, n := range oldKeys {
		if d := n - newKeys[k]; d > 0 {
			l.netLoss[k] = d
		}
	}
	sort.Strings(l.sourceChanged)

	match(l.olds, l.news)
	l.findFormerTests()
	l.matchInPlace()
	l.fillPool(testFiles)
	l.titles = map[string]bool{}
	for _, f := range testFiles {
		for _, ln := range f.Added {
			for _, s := range ln.Strings {
				l.titles[strings.TrimSpace(s)] = true
			}
		}
	}
	for _, o := range l.olds {
		if d, ok := l.dropOf(o); ok {
			l.drops = append(l.drops, d)
		}
	}
	for _, o := range l.olds {
		if o.match == nil && !o.partial && !o.skipped() && !l.explained(o) {
			l.deletions = append(l.deletions, o)
		}
	}
	return l
}

// findUncollected finds test files that now live under a name the test
// runner does not collect: renamed from a test path to another path, or
// deleted while a non-test file with the same tests was added
// (pricing.test.js → pricing.disabled.js).
func (l *ledger) findUncollected() {
	for _, f := range l.a.Files {
		if f.Diff.Kind != diff.Deleted && l.testOn(f, before) && !l.testOn(f, after) {
			l.renamedTo[f], l.uncollected[f.Path] = f.Path, true
		}
	}
	var gone []*core.FileInfo
	for _, f := range l.a.Files {
		if f.Diff.Kind == diff.Deleted && l.scanned(f, before) {
			gone = append(gone, f)
		}
	}
	if len(gone) == 0 {
		return
	}
	for _, g := range l.a.Files {
		if g.Diff.Kind != diff.Added || l.testOn(g, after) || !testblocks.Supported(g.Lang) || l.uncollected[g.Path] {
			continue
		}
		var names map[string]bool
		for _, f := range gone {
			if _, done := l.renamedTo[f]; done || !sameFamily(f.Lang, g.Lang) {
				continue
			}
			if names == nil {
				names = map[string]bool{}
				for _, seg := range l.segments(g, after) {
					for _, b := range testblocks.Find(g.Lang, seg.lines) {
						names[b.Bare] = true
					}
				}
			}
			total, found := 0, 0
			for _, o := range l.olds {
				if o.file == f {
					total++
					if names[o.block.Bare] {
						found++
					}
				}
			}
			if total > 0 && 2*found >= total {
				l.renamedTo[f], l.uncollected[g.Path] = g.Path, true
				break
			}
		}
	}
}

// testWords are path words that mark test code.
var testWords = map[string]bool{"test": true, "tests": true, "spec": true, "specs": true, "testing": true,
	"e2e": true, "unittest": true, "unittests": true, "integrationtest": true, "integrationtests": true}

// testishPath reports a path one of whose words, in its directories or
// file name, marks tests (src/jvmTest/kotlin, e2e-specs/,
// login.integration-test.ts), outside test-data directories. Words are
// split at punctuation and camelCase humps, so src/latest/ or
// attestation.py do not count.
func testishPath(p string) bool {
	if fixture(p) {
		return false
	}
	var word strings.Builder
	flush := func() bool {
		w := strings.ToLower(word.String())
		word.Reset()
		return testWords[w]
	}
	prev := rune(0)
	for _, r := range p {
		isLetter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		hump := r >= 'A' && r <= 'Z' && prev >= 'a' && prev <= 'z'
		if (!isLetter || hump) && flush() {
			return true
		}
		if isLetter {
			word.WriteRune(r)
		}
		prev = r
	}
	return flush()
}

// match pairs old tests with new ones across the whole change: by name in
// the same file, by bare name in the same file (suites reorganized), by
// name in another file (moved), then by body similarity (renamed).
func match(olds, news []*instance) {
	type key struct {
		f    *core.FileInfo
		name string
	}
	pass := func(how matchKind, sameFile bool, nameOf func(*instance) string, accept func(o, n *instance) bool) {
		keyOf := func(in *instance) key {
			k := key{name: nameOf(in)}
			if sameFile {
				k.f = in.file
			}
			return k
		}
		idx := map[key][]*instance{}
		for _, n := range news {
			if n.match == nil && nameOf(n) != "" {
				idx[keyOf(n)] = append(idx[keyOf(n)], n)
			}
		}
		var cs []candidate
		for _, o := range olds {
			if o.match != nil || nameOf(o) == "" {
				continue
			}
			for _, n := range idx[keyOf(o)] {
				if accept == nil || accept(o, n) {
					cs = append(cs, candidate{o, n, dice(o, n)})
				}
			}
		}
		// Several tests may share a name ("returns 404" under two suites):
		// pair the most similar bodies first.
		linkBest(cs, how)
	}
	qualified := func(in *instance) string { return in.block.Name } // "" never matches
	bare := func(in *instance) string { return in.block.Bare }
	pass(byName, true, qualified, nil)
	pass(byBare, true, bare, nil)
	pass(moved, false, qualified, nil)
	// A bare name such as "works" is too common to identify a test across
	// files on its own; the bodies must agree too.
	pass(moved, false, bare, func(o, n *instance) bool { return o.partial || n.partial || dice(o, n) >= 0.5 })
	matchRenames(olds, news)
}

// candidate is a possible pairing of an old and a new test.
type candidate struct {
	o, n *instance
	sim  float64
}

// linkBest links candidates greedily, most similar first; ties prefer the
// same file, then source order.
func linkBest(cs []candidate, how matchKind) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.sim != b.sim {
			return a.sim > b.sim
		}
		if sa, sb := a.o.file == a.n.file, b.o.file == b.n.file; sa != sb {
			return sa
		}
		if a.o.order != b.o.order {
			return a.o.order < b.o.order
		}
		return a.n.order < b.n.order
	})
	for _, c := range cs {
		if c.o.match == nil && c.n.match == nil {
			link(c.o, c.n, how)
		}
	}
}

func link(o, n *instance, how matchKind) {
	o.match, n.match = n, o
	o.how, n.how = how, how
}

// matchRenames pairs the remaining tests whose bodies are similar.
func matchRenames(olds, news []*instance) {
	var os, ns []*instance
	for _, o := range olds {
		if o.match == nil && !o.partial && o.size >= 3 {
			os = append(os, o)
		}
	}
	for _, n := range news {
		if n.match == nil && !n.partial && n.size >= 3 {
			ns = append(ns, n)
		}
	}
	if len(os) == 0 || len(ns) == 0 || len(os)*len(ns) > 250000 {
		return
	}
	var cs []candidate
	for _, o := range os {
		for _, n := range ns {
			if s := dice(o, n); s >= 0.6 {
				cs = append(cs, candidate{o, n, s})
			}
		}
	}
	linkBest(cs, renamed)
}

// fillPool collects the tokens that can vouch for a removed assertion:
// added test code outside tests (helpers, decorators, tables), and inside
// tests only assertions, data rows and the expected values that an
// assertion compares against (`expected = pd.DataFrame({...})` before
// `assert_frame_equal(df, expected)`). A plain call that replaced an
// assertion (`_ = format(1999)` for `XCTAssertEqual(format(1999), ...)`)
// must not make the assertion look folded into something else. Tests that
// are new, moved or renamed contribute their whole body on those terms.
func (l *ledger) fillPool(files []*core.FileInfo) {
	bodies := map[*core.FileInfo][]*instance{}
	for _, n := range l.news {
		bodies[n.file] = append(bodies[n.file], n)
		if n.how == byName {
			continue
		}
		used := assertedNames(n)
		for rel := n.block.Start; rel <= n.block.End && rel-1 < len(n.seg.lines); rel++ {
			ll, next := n.seg.lines[rel-1], ""
			if rel < len(n.seg.lines) {
				next = n.seg.lines[rel].Code
			}
			if rel < n.block.Header || l.vouches(n.file.Lang, ll.Code, next) || assigns(ll.Code, used) {
				l.pool.add(tokens(ll.Code, ll.Strings))
			}
		}
	}
	for _, f := range files {
		for _, ln := range f.Added {
			var body *instance
			for _, n := range bodies[f] {
				if n.header() <= ln.No && ln.No <= n.line(n.block.End) {
					body = n
					break
				}
			}
			next, _ := f.NewLine(ln.No + 1)
			if body == nil || l.vouches(f.Lang, ln.Code, next.Code) || assigns(ln.Code, assertedNames(body)) {
				l.pool.add(tokens(ln.Code, ln.Strings))
			}
		}
	}
}

// assignRe matches an assignment and captures the assigned name:
// `expected = ...`, `want := ...`, `const expected: Row[] = ...`,
// `val expected = ...`, `DataFrame expected = ...`.
var assignRe = regexp.MustCompile(`^\s*(?:(?:const|let|var|val|final|auto|my|local)\s+)?(?:[\w<>\[\],.?]+\s+)?([A-Za-z_$][\w$]*)\s*(?::\s*[^=]+?)?\s*:?=(?:[^=>]|$)`)

// assigns reports whether a line assigns one of the names in used.
func assigns(code string, used map[string]bool) bool {
	if len(used) == 0 {
		return false
	}
	m := assignRe.FindStringSubmatch(code)
	return m != nil && used[m[1]]
}

// assertedNames returns the identifiers that the live assertions of a test
// use, without stop words (cached on the instance).
func assertedNames(n *instance) map[string]bool {
	if n.asserted == nil {
		n.asserted = map[string]bool{}
		for _, s := range n.sites {
			for _, tk := range s.toks {
				if !strings.HasPrefix(tk, `"`) && !stopWords[tk] && len(tk) > 1 {
					n.asserted[tk] = true
				}
			}
		}
	}
	return n.asserted
}

// dropOf reports whether old test o lost assertions in its new version.
func (l *ledger) dropOf(o *instance) (drop, bool) {
	n := o.match
	if n == nil || n.skipped() || n.assertions() >= o.assertions() {
		return drop{}, false
	}
	// A test cut off by the end of a hunk (patch-only mode) is judged on
	// what the hunk shows when both versions come from the same hunk: they
	// end at the same unchanged context, so the visible parts differ by
	// exactly the change. An added bare return that makes everything after
	// it unreachable is enough on its own.
	if (o.partial || n.partial) && !sameWindow(o, n) && !returnsEarly(n) {
		return drop{}, false
	}
	helped := l.helperCalls(n)
	if n.assertions()+helped >= o.assertions() {
		return drop{}, false // the checks moved into helpers the test calls
	}
	if _, ok := l.unresolvedHelper(o, n); ok {
		return drop{}, false // the checks may live in a helper narc cannot read
	}
	if snapshotted(o, n) {
		return drop{}, false // one snapshot comparison took over the checks
	}
	remaining := map[string]int{}
	for _, s := range o.sites {
		remaining[s.key]++
	}
	kept := map[string]bool{}
	gained := false
	for _, s := range n.sites {
		kept[s.key] = true
		if remaining[s.key] > 0 {
			remaining[s.key]--
		} else {
			gained = true
		}
	}
	dead := unreachable(n)
	var lost []site
	for _, s := range o.sites {
		if remaining[s.key] <= 0 {
			continue
		}
		remaining[s.key]--
		if kept[s.key] {
			continue // a repeated assertion; the test still makes it
		}
		if l.checksRemovedCode(s) || l.siteVanished(s) {
			continue // the function, type or value it checked was deleted
		}
		if dead[s.key] > 0 { // still there, after an early return
			dead[s.key]--
			lost = append(lost, s)
		} else if l.lose(s) {
			lost = append(lost, s)
		}
	}
	lost = skippedInstead(o, n, lost)
	if len(lost) == 0 {
		return drop{}, false
	}
	return drop{old: o, new: n, lost: lost, rewritten: gained || dice(o, n) < 0.5 || n.how == inPlace, atLeast: o.partial,
		helped: helped, via: l.newHelper(o, n)}, true
}

// snapshotRe matches a snapshot assertion: toMatchSnapshot,
// toMatchInlineSnapshot, syrupy's `== snapshot`, Home Assistant's
// snapshot_platform, insta's assert_*_snapshot!, Verify.
var snapshotRe = regexp.MustCompile(`(?i)snapshot|\bVerify\s*\(`)

// snapshotted reports whether new test n compares with a snapshot and old
// test o did not: the snapshot records everything the old assertions
// checked (and the snapshot file shows it), so they were folded into it.
func snapshotted(o, n *instance) bool {
	for _, s := range o.sites {
		if snapshotRe.MatchString(s.code) {
			return false
		}
	}
	for _, s := range n.sites {
		if snapshotRe.MatchString(s.code) {
			return true
		}
	}
	return false
}

// siteVanished reports whether a lost assertion mentions a name that the
// change removed from production code: it checked an input, an option or
// a value that no longer exists.
func (l *ledger) siteVanished(s site) bool {
	if len(l.sourceChanged) == 0 {
		return false
	}
	v := l.source().vanished
	for _, tk := range s.toks {
		if v[strings.TrimPrefix(tk, `"`)] {
			return true
		}
	}
	return false
}

// newHelper returns a helper with assertions that new test n calls and
// old test o did not: the test now hands its checks to it.
func (l *ledger) newHelper(o, n *instance) string {
	for rel := n.block.Header + 1; rel <= n.block.End && rel-1 < len(n.seg.lines); rel++ {
		code := n.seg.lines[rel-1].Code
		calls := callRe.FindAllStringSubmatch(code, -1)
		if n.seg.lang == lang.Go {
			calls = append(calls, goLiteralRe.FindAllStringSubmatch(code, -1)...)
		}
		for _, m := range calls {
			name := lastName(m[1])
			if name == n.block.Bare || stopWords[name] || o.bag[name] > 0 {
				continue
			}
			if l.helpers[name] > 0 || l.externalHelpers(n.file)[name] > 0 {
				return name
			}
		}
	}
	return ""
}

// checksRemovedCode reports whether an assertion exercises a function or
// type that the change deleted from production code, so the assertion went
// with the code it checked ("remove dead code").
func (l *ledger) checksRemovedCode(s site) bool {
	return l.callsRemoved(s.code, "")
}

// callsRemoved reports whether code calls (or constructs) a function or
// type, other than self, that the change deleted from production code. A
// mere mention does not count: a deleted onClick handler says nothing
// about a test that passes some other onClick.
func (l *ledger) callsRemoved(code, self string) bool {
	if len(l.removedDefs) == 0 {
		return false
	}
	for _, m := range callRe.FindAllStringSubmatch(code, -1) {
		if name := lastName(m[1]); name != self && l.removedDefs[name] {
			return true
		}
	}
	return false
}

// inOldTest reports whether old-side line no of f lies inside a test.
func (l *ledger) inOldTest(f *core.FileInfo, no int) bool {
	for _, o := range l.olds {
		if o.file == f && o.line(o.block.Start) <= no && no <= o.line(o.block.End) {
			return true
		}
	}
	return false
}

// sameWindow reports two cut-off versions of a test taken from the same
// hunk of the same file.
func sameWindow(o, n *instance) bool {
	return o.partial && n.partial && o.file == n.file && o.seg.hunk >= 0 && o.seg.hunk == n.seg.hunk
}

// returnsEarly reports a test that the change made return before any of
// its assertions: an added bare return with no live assertion before it.
func returnsEarly(n *instance) bool {
	if n.block.Return == 0 || n.assertions() > 0 || len(n.block.Unreachable) == 0 {
		return false
	}
	at := n.line(n.block.Return)
	for _, ln := range n.file.Added {
		if ln.No == at {
			return true
		}
	}
	return false
}

// skipCallRe matches a statement that skips the rest of a test at run
// time: t.Skipf(...), pytest.skip(...), self.skipTest(...), raise
// SkipTest, markTestSkipped(...), XCTSkip(...), assumeTrue(...),
// Assert.Inconclusive(...), this.skip(), RSpec skip/pending.
var skipCallRe = regexp.MustCompile(`\b(?:(?:t|b|tb|f|s|suite)\s*\.\s*Skip(?:f|Now)?|pytest\s*\.\s*(?:skip|xfail)|(?:self|cls)\s*\.\s*skipTest|markTest(?:Skipped|Incomplete)|XCTSkip(?:If|Unless)?|(?:Assumptions\s*\.\s*|Assume\s*\.\s*)?assume(?:True|False|That)|Assert\s*\.\s*(?:Ignore|Inconclusive)|(?:this|ctx|context|test)\s*\.\s*skip)\s*\(|\braise\s+(?:unittest\s*\.\s*)?SkipTest\b|^\s*(?:skip|pending)\b`)

// skippedInstead drops the lost assertions that were replaced by a
// run-time skip: the hunk that removed them added a skip statement to the
// same test (`t.Fatalf(...)` became `t.Skipf(...)`). skip-added reports
// that change; reporting it here as well would only demand a second waiver.
func skippedInstead(o, n *instance, lost []site) []site {
	if o.file != n.file {
		return lost
	}
	f := n.file
	skipHunks := map[int]bool{}
	from, to := n.line(n.block.Start), n.line(n.block.End)
	for _, ln := range f.Added {
		if from <= ln.No && ln.No <= to && skipCallRe.MatchString(ln.Code) {
			skipHunks[ln.Hunk] = true
		}
	}
	if len(skipHunks) == 0 {
		return lost
	}
	hunkOf := map[int]int{}
	for _, ln := range f.Removed {
		hunkOf[ln.No] = ln.Hunk
	}
	var out []site
	for _, s := range lost {
		if h, ok := hunkOf[s.line]; !ok || !skipHunks[h] {
			out = append(out, s)
		}
	}
	return out
}

// lose accounts for an assertion that disappeared from a test. It reports
// whether the assertion is gone from the change altogether: not still
// present elsewhere (moved to another test or a helper) and not folded
// into added test code (a parametrize table, a merged assertion).
func (l *ledger) lose(s site) bool {
	if l.netLoss[s.key] <= 0 {
		return false
	}
	l.netLoss[s.key]--
	return !coveredBy(s.toks, l.pool)
}

// stillVisible reports whether every assertion of o appears on the new
// side of its file as far as the diff shows it.
func (l *ledger) stillVisible(o *instance) bool {
	keys := map[string]int{}
	l.assertionKeys(o.file, after, keys)
	for _, s := range o.sites {
		if keys[s.key] == 0 {
			return false
		}
		keys[s.key]--
	}
	return true
}

// explained reports whether a vanished test is accounted for: its code was
// deleted, an identical copy remains, or its assertions live on elsewhere
// in the change (split, merged, converted to a table or parametrized test).
// A test that was turned into a plain function is never accounted for:
// its assertions are still there, but nothing runs them.
func (l *ledger) explained(o *instance) bool {
	if o.former != nil {
		return false
	}
	f := o.file
	if f.Diff.Kind == diff.Deleted && (l.rustInline(f) || l.deletedStems[stem(f.Path, true)] || l.importsGone(f)) {
		return true // the code under test (or the module holding the tests) was deleted
	}
	if f.Diff.Kind == diff.Deleted && migrationTestRe.MatchString(f.Path) {
		return true // a one-off data migration's test, retired with the migration
	}
	if l.refsVanished(o) != "" {
		return true // it exercises a function, type, option or value the change removed
	}
	if len(o.block.Bare) >= 8 && strings.ContainsAny(o.block.Bare, " :") && l.titles[o.block.Bare] {
		return true // its title is now a row of a table of cases (it.each, parametrize)
	}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		if l.callsRemoved(o.seg.lines[rel-1].Code, o.block.Bare) {
			return true // the function or type it exercises was deleted
		}
	}
	for _, n := range l.news {
		if n.size == o.size && n.size >= 3 && dice(o, n) == 1 {
			return true // a duplicate was removed; an identical test remains
		}
	}
	if o.size >= 3 && l.copyElsewhere(o) {
		return true // a duplicate of a test in an unchanged file nearby
	}
	if !fullContent(f) && len(o.sites) > 0 && l.stillVisible(o) {
		// Patch-only: a twin test may lie mostly outside the hunks, so
		// assertions still visible in the new code are given the benefit
		// of the doubt.
		return true
	}
	if len(o.sites) == 0 {
		var toks []string
		for tk := range o.bag {
			toks = append(toks, tk)
		}
		return coveredBy(toks, l.pool)
	}
	for _, s := range o.sites {
		if l.lose(s) {
			return false
		}
	}
	return true
}
