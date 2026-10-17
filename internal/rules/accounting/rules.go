package accounting

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/testblocks"
)

var testDeleted = &core.Rule{
	ID:       "test-deleted",
	Title:    "Test deleted",
	Category: "accounting",
	Severity: core.Warning,
	Summary:  "A test disappeared, or stopped being collected as a test, without being moved, renamed, split or merged elsewhere in the change; an error when the change also edited the code it exercised.",
	Hint: "Restore the deleted test. If it fails, fix the code it exercises instead of removing the check. " +
		"Moving, renaming, splitting or parametrizing tests is fine: narc matches them across the whole change. " +
		"If the behavior was removed on purpose, delete the code with its test, or keep the deletion with " +
		`narc:allow test-deleted reason="why the test is obsolete".`,
	Docs: "Deleting a failing test is the quickest way to a green build, and the classic move is to change the code " +
		"and delete the test that caught it in the same change. narc compares the tests before and after the change " +
		"across all files. A test counts as deleted only if it was not found again by name, by suite-less name, or by " +
		"a (nearly) identical body, and its assertions do not live on in another test, a helper, or a table. Tests of " +
		"code that was itself deleted, already-skipped tests and exact duplicates are not reported.\n\n" +
		"A test that was switched off without being deleted counts too: renamed so the runner no longer collects " +
		"it (`test_x` → `_test_x`, `TestX` → `testX`), or stripped of its `@Test` / `#[test]` / `[Fact]` marker.\n\n" +
		"The finding is an error when the same change also edited the production code the test exercised (the " +
		"\"break the code, delete the test\" move), and a warning when the change touches no source code or only " +
		"code the test does not exercise. Deletions that read as a rework or a removal are info: the change mostly " +
		"deletes source code, wrote new tests in the same file, retired a feature flag, option or module the test " +
		"used, or wrote a test whose title reads like the deleted one's. A test file rewritten with far fewer tests " +
		"while its source file changed gets one warning. Deletions are grouped: one finding per file, and one per " +
		"change for warnings that span several files (a pruning pass).\n\n" +
		"```ts\n// before\nit('rejects expired tokens', () => {\n  expect(() => verify(expired)).toThrow(TokenExpiredError)\n})\n" +
		"// after: the test is gone and src/jwt.ts changed  <- BUSTED\n```",
}

var testFileDeleted = &core.Rule{
	ID:       "test-file-deleted",
	Title:    "Test file deleted",
	Category: "accounting",
	Severity: core.Warning,
	Summary:  "A test file was deleted, or renamed out of the test runner's reach, and its tests were not re-added elsewhere; an error when the change also edited the code they exercised.",
	Hint: "Restore the test file, or move its tests to their new home in the same change. If the code it tested was " +
		"removed, delete that code in the same change. Otherwise keep the deletion with a reason in .narc.yml " +
		"(allow: - rule: test-file-deleted, path: ..., reason: ...).",
	Docs: "Deleting a whole test file removes every check in it at once. narc lists the tests of a deleted test file " +
		"that do not reappear anywhere else in the change (by name or by body). A file whose tests all moved, or whose " +
		"code under test was deleted with it, is not reported. Renaming a test file to a name the test runner does " +
		"not collect (`pricing.test.js` → `pricing.disabled.js`) counts as deleting it.\n\n" +
		"The finding is an error when the same change also edited the production code the tests exercised, and a " +
		"warning when it touches no source code or only code the tests do not exercise. It is info when the file " +
		"reads as retired with its code: the change mostly deletes source code, removed every import of the module " +
		"it covered, retired the code most of its tests used, or wrote tests next to it that replace them.",
}

var assertionsRemoved = &core.Rule{
	ID:       "assertions-removed",
	Title:    "Assertions removed from a test",
	Category: "accounting",
	Severity: core.Error,
	Summary:  "A test that still exists lost assertions (e.g. 3 → 1) that do not reappear anywhere in the change.",
	Hint: "Restore the removed assertions and make the code satisfy them. If an assertion was wrong, replace it " +
		"with a correct one that is just as specific. Moving assertions into a helper, a table-driven or " +
		"parametrized test, or one structural assertion is fine.",
	Docs: "An agent that cannot make a test pass may delete the assertions that fail and keep the rest, so the test " +
		"still runs and looks healthy. narc counts the assertions of every test before and after the change. A drop " +
		"is reported when the removed assertions are gone from the change altogether: not moved to another test or " +
		"helper, not folded into a parametrize table or a merged `toEqual`.\n\n" +
		"It is an error when assertions were simply deleted: in a change that touches no source code, or leaving the " +
		"test with at most one assertion beside a source change. Other drops beside a source change are warnings, and " +
		"so is a sub-scenario removed with its setup. Drops that read as a rework are info: the test was rewritten " +
		"with other assertions or renamed to a narrower name, the change mostly deletes source code, asserts the " +
		"same thing in a new form, or only dropped absence checks. A change that touches no source code and trims " +
		"many tests is reported once. Assertions that call a function the change deleted, or that became a run-time " +
		"skip (reported by skip-added), are not counted.\n\n```py\n# before\ndef test_refund():\n    assert refund(order) == 40\n    assert order.status == \"refunded\"\n" +
		"# after\ndef test_refund():\n    assert refund(order) == 40      # assertions in \"test_refund\" dropped 2 → 1\n```",
}

var testHollowed = &core.Rule{
	ID:       "test-hollowed",
	Title:    "Test hollowed out",
	Category: "accounting",
	Severity: core.Error,
	Summary:  "A test that asserted something now asserts nothing, so it passes no matter what the code does.",
	Hint: "A test without assertions cannot fail. Put the assertions back and fix the code under test. If the test " +
		"is obsolete, delete it openly instead of leaving an empty shell.",
	Docs: "Replacing a test's body with `pass`, an empty block or an early `return`, or deleting every assertion in " +
		"it, keeps the test name in the report while removing the check. narc reports a test that had at least one " +
		"assertion and has none after the change, unless the assertions moved into a helper or another test.\n\n" +
		"```go\nfunc TestTransfer(t *testing.T) {\n\t// before: if got := Transfer(a, b, 10); got != nil { t.Fatal(got) }\n" +
		"\tTransfer(a, b, 10) // after: nothing is checked  <- BUSTED\n}\n```",
}

func init() {
	// Checks are assigned here, not in the literals, to avoid an
	// initialization cycle (the checks refer back to the rules).
	testDeleted.Check = checkTestDeleted
	testFileDeleted.Check = checkTestFileDeleted
	assertionsRemoved.Check = checkAssertionsRemoved
	testHollowed.Check = checkTestHollowed
	core.Register(testDeleted, testFileDeleted, assertionsRemoved, testHollowed)
}

// escalate raises a deletion finding to an error when the change also
// modified non-test source code: "break the code, delete the test".
//
// A change whose source edits are mostly deletions (dead code or a
// feature being removed, at least 3 lines removed per line added) is
// left a warning: its tests usually go with the code they covered, which
// a human should confirm, but it is not the edit-and-delete pattern.
func (l *ledger) escalate(fd *core.Finding) {
	if len(l.sourceChanged) == 0 {
		return
	}
	src := l.sourceList()
	if l.removesSource() {
		fd.Message += l.removalNote(src)
		return
	}
	fd.Severity = core.Error
	fd.Message += fmt.Sprintf(" while the same change modified source code (%s)", src)
}

// sourceList names the changed source files: the first, and how many more.
func (l *ledger) sourceList() string {
	src := l.sourceChanged[0]
	if n := len(l.sourceChanged) - 1; n > 0 {
		src += fmt.Sprintf(" and %d more", n)
	}
	return src
}

// removesSource reports a change whose source edits are mostly deletions:
// at least 20 lines of code removed, and 3 removed per line added.
func (l *ledger) removesSource() bool {
	return l.sourceRemoved >= 20 && l.sourceRemoved >= 3*l.sourceAdded
}

// removalNote describes source edits that are mostly deletions.
func (l *ledger) removalNote(src string) string {
	return fmt.Sprintf(" while the same change removed source code (%d lines removed, %d added in %s)",
		l.sourceRemoved, l.sourceAdded, src)
}

// excuse returns why a deleted test is probably part of a rework rather
// than a removed check, or "": its file gained at least as many new tests
// as it lost, it covered a feature flag the change retired, or the source
// file it covered was deleted. Such deletions are reported as info.
func (l *ledger) excuse(o *instance) string {
	if n := l.newTestsIn()[o.file]; n > 0 && n >= l.deletedIn(o.file) {
		return fmt.Sprintf("; the same change added %s to this file", plural(n, "new test"))
	}
	if l.aboutFlag(o) {
		return "; it covers a feature flag whose checks the same change removed from source code"
	}
	if s := stem(o.file.Path, true); l.deletedStems[s] {
		return fmt.Sprintf("; the source file it covered (%s) was deleted", s)
	}
	return ""
}

// genericStems are file stems too common to tie a test file to one
// source file.
var genericStems = map[string]bool{"index": true, "main": true, "utils": true, "util": true, "types": true,
	"helpers": true, "helper": true, "mod": true, "lib": true, "__init__": true, "init": true, "common": true,
	"constants": true, "config": true, "app": true, "test": true, "tests": true, "spec": true}

// coversModified reports whether test file f is named after a production
// file the change modified (spec/models/user_spec.rb and
// app/models/user.rb): its tests cover the code that changed.
func (l *ledger) coversModified(f *core.FileInfo) bool {
	s := stem(f.Path, true)
	if genericStems[s] {
		return false
	}
	for _, g := range l.a.Files {
		if g.Diff.Kind == diff.Modified && l.productionCode(g) && stem(g.Path, false) == s {
			return true
		}
	}
	return false
}

// deletedIn counts the deletions in file f.
func (l *ledger) deletedIn(f *core.FileInfo) int {
	n := 0
	for _, o := range l.deletions {
		if o.file == f {
			n++
		}
	}
	return n
}

func checkTestFileDeleted(a *core.Analysis) []core.Finding {
	l := ledgerFor(a)
	var out []core.Finding
	byFile := map[*core.FileInfo][]*instance{}
	var files []*core.FileInfo
	for _, o := range l.deletions {
		if !l.fileGone(o.file) {
			continue
		}
		if byFile[o.file] == nil {
			files = append(files, o.file)
		}
		byFile[o.file] = append(byFile[o.file], o)
	}
	for _, f := range files {
		tests := byFile[f]
		asserts := 0
		var names []string
		linked := false
		elsewhere := true
		for _, o := range tests {
			asserts += o.assertions()
			names = append(names, fmt.Sprintf("%q", o.name()))
			linked = linked || l.refsModule(o)
			elsewhere = elsewhere && l.testedElsewhere(o)
		}
		msg := fmt.Sprintf("deleted test file had %s (%s) that %s not moved elsewhere: %s",
			plural(len(tests), "test"), plural(asserts, "assertion"), were(len(tests)), list(names, 4))
		to, renamed := l.renamedTo[f]
		if renamed {
			msg = fmt.Sprintf("test file was renamed to %s, which the test runner does not collect, so its %s (%s) no longer run: %s",
				to, plural(len(tests), "test"), plural(asserts, "assertion"), list(names, 4))
		}
		fd := testFileDeleted.Finding(f, 0, msg)
		if renamed {
			fd.Hint = uncollectedHint
			l.escalate(&fd)
		} else {
			sev, why := l.fileVerdict(f, tests, linked, elsewhere)
			fd.Severity = sev
			fd.Message += why
		}
		out = append(out, fd)
	}
	return out
}

// fileVerdict grades a deleted test file whose tests were not found
// again, and says why. It is an error only when the change edited the
// production code the tests exercised (linked) and nothing else accounts
// for the deletion; a change that touches no source code, or whose source
// edits the tests do not exercise, leaves a warning. Deletions that read
// as a rework or a removal (see fileExcuse, removesSource, obsoleteShare)
// or whose code is still exercised by tests the change edited are info.
func (l *ledger) fileVerdict(f *core.FileInfo, tests []*instance, linked, elsewhere bool) (core.Severity, string) {
	if why := l.fileExcuse(f, tests); why != "" {
		return core.Info, why
	}
	asserts := 0
	for _, o := range tests {
		asserts += o.assertions() + l.helperCalls(o)
	}
	if asserts == 0 {
		return core.Info, "; its tests asserted nothing"
	}
	if len(l.sourceChanged) == 0 {
		return core.Warning, ""
	}
	src := l.sourceList()
	switch {
	case l.removesSource():
		return core.Info, l.removalNote(src)
	case linked && elsewhere:
		return core.Info, "; tests the same change edited still call the code they exercised"
	case l.lookalikeShare(tests):
		return core.Info, "; the same change wrote tests that read like most of its tests, so the file was probably moved and reworded"
	case l.obsoleteShare(f, len(tests)):
		return core.Info, "; most of its tests exercised code the same change removed, so the file went with that code"
	case linked:
		return core.Error, fmt.Sprintf(" while the same change modified source code (%s)", src)
	}
	return core.Warning, fmt.Sprintf(" while the same change modified source code it does not exercise (%s)", src)
}

// lookalikeShare reports whether most of a deleted file's tests have a
// lookalike that the change wrote.
func (l *ledger) lookalikeShare(tests []*instance) bool {
	n := 0
	for _, o := range tests {
		if l.lookalike(o) != nil {
			n++
		}
	}
	return n > 0 && 2*n >= len(tests)
}

// obsoleteShare reports whether at least as many of file f's vanished
// tests were explained by the code they exercised being removed from
// production code as were left unexplained (left): the file is being
// retired with the code it covered, and the remaining tests went with it.
func (l *ledger) obsoleteShare(f *core.FileInfo, left int) bool {
	n := 0
	for _, o := range l.olds {
		if o.file == f && (o.obsolete || o.match == nil && l.retiredKey(o) != "") {
			n++
		}
	}
	return n > 0 && n >= left
}

// fileExcuse returns why a deleted test file is probably part of a rework,
// or "": test files next to it gained at least as many new tests as it
// had, production code stopped importing the module it covered, or all of
// its tests covered feature flags the change retired.
func (l *ledger) fileExcuse(f *core.FileInfo, tests []*instance) string {
	dir := path.Dir(f.Path)
	fresh := 0
	counted := map[*core.FileInfo]bool{}
	for _, n := range l.news {
		if g := n.file; path.Dir(g.Path) == dir && !counted[g] {
			counted[g] = true
			fresh += l.reworkIn(g)
		}
	}
	if fresh > 0 && fresh >= len(tests) {
		return fmt.Sprintf("; the same change wrote %s next to it", plural(fresh, "new test"))
	}
	if s := stem(f.Path, true); l.source().importsDropped[s] {
		return fmt.Sprintf("; the same change removed every import of %s from source code", s)
	}
	flags := true
	for _, o := range tests {
		flags = flags && l.aboutFlag(o)
	}
	if flags {
		return "; its tests cover feature flags whose checks the same change removed from source code"
	}
	return ""
}

func checkAssertionsRemoved(a *core.Analysis) []core.Finding {
	l := ledgerFor(a)
	var out []core.Finding
	var sweep []int // indexes in out of the drops a cleanup finding may absorb
	var names []string
	lost := 0
	for _, d := range l.drops {
		if d.new.assertions()+d.helped == 0 {
			continue // test-hollowed
		}
		left := d.new.assertions() + d.helped
		msg := fmt.Sprintf("assertions in %q dropped %d → %d%s", d.old.name(), d.old.assertions(), left, whereNow(d))
		if d.atLeast {
			msg += " in the part of the test the patch shows"
		}
		fd := anchorDrop(assertionsRemoved, d, msg)
		sev, why := l.dropVerdict(d, left)
		fd.Severity = sev
		fd.Message += why
		if folded := d.old.assertions() - left - len(d.lost); folded > 0 {
			// Some removed assertions live on in new ones; say which did not.
			if len(d.lost) == 1 {
				fd.Message += "; no longer checked anywhere: " + d.lost[0].raw
			} else {
				fd.Message += fmt.Sprintf("; %d of them are no longer checked anywhere, such as: %s", len(d.lost), d.lost[0].raw)
			}
		}
		out = append(out, fd)
		if sev >= core.Warning && !l.neutered(d) {
			sweep = append(sweep, len(out)-1)
			names = append(names, fmt.Sprintf("%q", d.old.name()))
			lost += d.old.assertions() - left
		}
	}
	if len(l.sourceChanged) > 0 || len(sweep) < sweepMin {
		return out
	}
	// A change that touches no source code and trims the assertions of many
	// tests is a deliberate cleanup of the suite, reported once.
	first := out[sweep[0]]
	drop := map[int]bool{}
	for _, i := range sweep {
		drop[i] = true
	}
	first.Severity = core.Warning
	first.Message = fmt.Sprintf("%d tests lost %s in a change that touches no source code: %s",
		len(sweep), plural(lost, "assertion"), list(names, 4))
	kept := []core.Finding{first}
	for i, fd := range out {
		if !drop[i] {
			kept = append(kept, fd)
		}
	}
	return kept
}

// sweepMin is how many tests a change that touches no source code must
// trim before their drops are reported as one cleanup finding.
const sweepMin = 3

// dropVerdict grades a drop to `left` assertions and says why.
//
//   - An assertion turned into a statement that checks nothing (t.Errorf →
//     t.Logf, assert → print) is an error.
//   - Info when the drop reads as a deliberate rework: the test now hands
//     its checks to a new helper, it was rewritten with other assertions,
//     it was renamed to a narrower name (the case left its title), its file
//     gained new tests, the change mostly deletes source code (a feature
//     being removed), the lost checks were only absence checks or calls the
//     test still observes, or the same change asserts what they checked in
//     a new form.
//   - A warning when the lost checks went with the setup they checked (an
//     input or mock the new test no longer has): a sub-scenario was
//     removed, which needs a look but is not a check silenced in place.
//   - In a change that touches no source code, assertions were simply
//     deleted: an error.
//   - When source code changed too, the drop may follow a deliberate
//     change of behavior: an error only when the test is left with at most
//     one assertion and the change did not remove the value it expected
//     from the code; else a warning.
func (l *ledger) dropVerdict(d drop, left int) (core.Severity, string) {
	switch {
	case l.neutered(d):
		return core.Error, ""
	case d.via != "":
		return core.Info, fmt.Sprintf(" while the test now hands its checks to %s", d.via)
	case d.rewritten:
		return core.Info, " while the test was rewritten with other assertions"
	case narrowed(d.old.block.Bare, d.new.block.Bare):
		return core.Info, " while the test was renamed to a narrower name"
	case l.reworkIn(d.new.file) > 0:
		return core.Info, fmt.Sprintf(" while the same change wrote %s in this file", plural(l.reworkIn(d.new.file), "new test"))
	case len(l.sourceChanged) > 0 && l.removesSource():
		// Checks of behavior that is being removed (see escalate).
		return core.Info, l.removalNote(l.sourceList())
	case l.variantRemoved(d.lost):
		return core.Info, "; the input they passed is no longer accepted by the source code the change edited"
	case weakOnly(d):
		return core.Info, "; the lost checks only asserted an absence or a call the test still observes"
	case l.reasserted(d):
		return core.Info, "; the same change asserts what they checked in a new form"
	case setupGone(d):
		// A sub-scenario left the test with its setup: a coverage loss to
		// look at, not a check silenced in place.
		return core.Warning, "; the test no longer sets up what they checked (" + setupName(d) + ")"
	case len(l.sourceChanged) == 0:
		return core.Error, ""
	case left <= 1 && !l.valueRemoved(d.lost):
		return core.Error, ""
	}
	return core.Warning, ""
}

// setupGone reports whether every lost assertion checked something that
// the test no longer sets up: it names a variable, parameter or mock that
// the old test also used outside the assertion (its setup) and that the
// new test does not mention at all. The test's scenario changed (an input
// or a collaborator went away), rather than a check being silenced.
func setupGone(d drop) bool { return len(d.lost) > 0 && setupName(d) != "" }

// setupName returns, for setupGone, a name each lost assertion relied on
// (the first lost assertion's), or "" when one of them has none.
func setupName(d drop) string {
	ob, nb := d.old.bag(), d.new.bag()
	first := ""
	for _, s := range d.lost {
		_, ids := distinctive(s.stmt)
		found := ""
		for _, id := range ids {
			if len(id) >= 3 && nb[id] == 0 && ob[id] >= 2 && !commonMember[id] {
				found = id
				break
			}
		}
		if found == "" {
			return ""
		}
		if first == "" {
			first = found
		}
	}
	return first
}

// commonMember are names of methods and fields too common to stand for a
// test's setup.
var commonMember = map[string]bool{"length": true, "size": true, "value": true, "data": true, "result": true,
	"called": true, "calls": true, "status": true, "body": true, "text": true, "name": true, "type": true,
	"mock": true, "args": true, "kwargs": true, "call_count": true, "return_value": true, "current": true}

// nullArgRe captures a null-like keyword passed as an argument:
// resolve(undefined), parse(None), f(a, nil).
var nullArgRe = regexp.MustCompile(`[(,]\s*(undefined|null|None|nil)\s*[,)]`)

// variantRemoved reports whether every lost assertion passes a null-like
// input (undefined, null, None, nil) that the change stopped handling in
// production code: the keyword occurs on fewer added than removed source
// lines (`mode: Mode | undefined` → `mode: Mode`, `case undefined:` gone).
func (l *ledger) variantRemoved(lost []site) bool {
	if len(l.sourceChanged) == 0 {
		return false
	}
	out := l.source().nullsOut
	for _, s := range lost {
		m := nullArgRe.FindStringSubmatch(s.code)
		if m == nil || !out[m[1]] {
			return false
		}
	}
	return len(lost) > 0
}

// narrowed reports a test renamed to a strict subset of the words of its
// old name ("stays on legacy without a project or a source" → "stays on
// legacy without a project"): the case it no longer checks left its title.
func narrowed(old, now string) bool {
	if old == now {
		return false
	}
	words := func(s string) []string {
		var out []string
		for _, w := range nameWordRe.FindAllString(s, -1) {
			out = append(out, strings.ToLower(w))
		}
		return out
	}
	ow, nw := words(old), words(now)
	if len(nw) < 2 || len(nw) >= len(ow) {
		return false
	}
	have := map[string]int{}
	for _, w := range ow {
		have[w]++
	}
	for _, w := range nw {
		if have[w] == 0 {
			return false
		}
		have[w]--
	}
	return true
}

// weakOnly reports whether every lost assertion of a drop is a weak one:
// an absence check (see absenceRe), or a bare "it was called" check on a
// mock the new test still uses, whose effect the remaining assertions
// observe.
func weakOnly(d drop) bool {
	for _, s := range d.lost {
		if absenceRe.MatchString(s.code) {
			continue
		}
		m := calledRe.FindStringSubmatch(s.code)
		if m == nil || d.new.bag()[lastName(m[1]+m[2])] == 0 {
			return false
		}
	}
	return len(d.lost) > 0
}

// calledRe matches a bare check that a mock was called, without arguments:
// expect(fetchUser).toHaveBeenCalled(), mock.assert_called_once().
var calledRe = regexp.MustCompile(`(?:\bexpect\s*\(\s*([\w$.]+)\s*\)\s*\.\s*toHaveBeenCalled\s*\(\s*\)|\b([\w.]+)\s*\.\s*assert_called(?:_once)?\s*\(\s*\))`)

// reasserted reports whether the checks a drop lost are made again, in a
// new form, by assertions the same change added to the test's file: an
// added assertion uses every identifier of the lost one and shares a word
// of its expected string ("DTSTART:20230101T100000Z" checked again as
// "DTSTART;TZID=America/Chicago:..."), so the format it checked changed
// on purpose.
func (l *ledger) reasserted(d drop) bool {
	f := d.new.file
	var added [][]string
	for _, ln := range f.Added {
		if testblocks.CountAssertions(f.Lang, ln.Code) > 0 {
			added = append(added, tokens(ln.Code, ln.Strings))
		}
	}
	if len(added) == 0 {
		return false
	}
	for _, s := range d.lost {
		lits, ids := distinctive(s.toks)
		var pieces []string
		for _, v := range lits {
			if strings.HasPrefix(v, `"`) {
				for _, p := range strPieceRe.FindAllString(v[1:], -1) {
					if len(p) >= 4 {
						pieces = append(pieces, strings.ToLower(p))
					}
				}
			}
		}
		if len(pieces) == 0 || len(ids) == 0 {
			return false
		}
		found := false
		for _, toks := range added {
			have := map[string]bool{}
			for _, tk := range toks {
				if strings.HasPrefix(tk, `"`) {
					for _, p := range strPieceRe.FindAllString(tk[1:], -1) {
						have[`"`+strings.ToLower(p)] = true
					}
				} else {
					have[tk] = true
				}
			}
			all := true
			for _, id := range ids {
				all = all && have[id]
			}
			shared := false
			for _, p := range pieces {
				shared = shared || have[`"`+p]
			}
			if all && shared {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// valueRemoved reports whether a lost assertion expected a value (a string
// literal) that the change removed from production code: the behavior it
// checked was changed on purpose.
func (l *ledger) valueRemoved(lost []site) bool {
	out := l.source().valuesOut
	for _, s := range lost {
		lits, _ := distinctive(s.toks)
		for _, v := range lits {
			if strings.HasPrefix(v, `"`) && out[v[1:]] {
				return true
			}
		}
	}
	return false
}

// absenceRe matches an assertion that checks that something is absent:
// an element not rendered, a key or text not contained.
var absenceRe = regexp.MustCompile(`\.\s*not\s*\.\s*(?:toBeInTheDocument|toBeVisible|toExist|toContain\w*|toHaveProperty|toHaveClass|toHaveAttribute|toMatch\w*|toHaveTextContent)\b|\bqueryBy\w*\s*\(.*\)\s*\)\s*\.\s*(?:toBeNull|toBeFalsy|toBeUndefined)\s*\(|\bassertNotIn\b|\bassert\b.*\bnot\s+in\b|\bNotContains\b|\bassertNotContains\b`)

// absenceOnly reports whether every lost assertion checks an absence.
func absenceOnly(lost []site) bool {
	for _, s := range lost {
		if !absenceRe.MatchString(s.code) {
			return false
		}
	}
	return len(lost) > 0
}

// neutered reports whether a lost assertion was rewritten in place into a
// statement that checks nothing: the hunk that removed it added a line
// that is not an assertion and reads like it, token for token, but for
// the assertion itself, such as t.Errorf("Tax() = %d, want 250", got)
// becoming t.Logf("Tax() = %d, want 250", got), or assert total == 42
// becoming print(total == 42).
func (l *ledger) neutered(d drop) bool {
	f := d.old.file
	if f != d.new.file {
		return false
	}
	hunkOf := map[int]int{}
	for _, ln := range f.Removed {
		hunkOf[ln.No] = ln.Hunk
	}
	// Lines that belong to an assertion of the new test, such as the
	// arguments of an expect( that opens on the line above.
	inAssert := map[int]bool{}
	n := d.new
	for _, rel := range n.counted().AssertionLines {
		depth := 0
		for k := rel - 1; k < len(n.seg.lines) && k < rel+7; k++ {
			inAssert[n.seg.lineNo(k)] = true
			for _, c := range n.seg.lines[k].Code {
				switch c {
				case '(', '[', '{':
					depth++
				case ')', ']', '}':
					depth--
				}
			}
			if depth <= 0 {
				break
			}
		}
	}
	for _, s := range d.lost {
		h, ok := hunkOf[s.line]
		if !ok || len(s.stmt) < 3 {
			continue
		}
		for _, ln := range f.Added {
			if ln.Hunk != h || inAssert[ln.No] || testblocks.CountAssertions(f.Lang, ln.Code) > 0 {
				continue
			}
			if !stmtCallRe.MatchString(ln.Code) {
				continue // not a call statement: a data row, a continuation
			}
			now := tokens(ln.Code, ln.Strings)
			if len(now) == 0 || len(now) > len(s.stmt)+2 {
				continue
			}
			if 3*2*lcs(s.stmt, now) >= 2*(len(s.stmt)+len(now)) {
				return true
			}
		}
	}
	return false
}

// stmtCallRe matches a line that is a call statement: log(x), t.Logf(...),
// await probe(...).
var stmtCallRe = regexp.MustCompile(`^\s*(?:await\s+)?[A-Za-z_$][\w$.]*\s*!?\(`)

// lcs returns the length of the longest common subsequence of a and b.
func lcs(a, b []string) int {
	prev, cur := make([]int, len(b)+1), make([]int, len(b)+1)
	for i := range a {
		for j := range b {
			switch {
			case a[i] == b[j]:
				cur[j+1] = prev[j] + 1
			case prev[j+1] >= cur[j]:
				cur[j+1] = prev[j+1]
			default:
				cur[j+1] = cur[j]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func checkTestHollowed(a *core.Analysis) []core.Finding {
	l := ledgerFor(a)
	var out []core.Finding
	for _, d := range l.drops {
		if d.new.assertions() != 0 || d.helped > 0 {
			continue // assertions-removed: it still checks something, if only through a helper
		}
		count := fmt.Sprint(d.old.assertions())
		if d.atLeast {
			count += " or more"
		}
		msg := fmt.Sprintf("test %q no longer asserts anything (%s → 0 assertions)%s%s",
			d.old.name(), count, whereNow(d), hollowDetail(d.new))
		out = append(out, anchorDrop(testHollowed, d, msg))
	}
	return out
}

// anchorDrop places a finding on the first lost assertion, so a waiver can
// sit where it was; for a test that moved files, on the test's new header.
func anchorDrop(r *core.Rule, d drop, msg string) core.Finding {
	lost := d.lost[0]
	if d.old.file == d.new.file {
		if fd := r.Removal(d.old.file, lost.line, msg); fd.Line > 0 {
			return fd
		}
	}
	fd := r.Finding(d.new.file, d.new.header(), msg)
	fd.Before = lost.raw
	return fd
}

// whereNow describes a test that was found under a new name or path.
func whereNow(d drop) string {
	var parts []string
	if d.new.name() != d.old.name() {
		parts = append(parts, fmt.Sprintf("now %q", d.new.name()))
	}
	if d.new.file.Path != d.old.file.Path {
		parts = append(parts, "in "+d.new.file.Path)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

// hollowDetail says how a test lost its last assertion.
func hollowDetail(n *instance) string {
	if len(n.counted().Unreachable) > 0 {
		if n.counted().Return > 0 {
			return "; it returns before reaching its assertions"
		}
		return "; its assertions sit in a block that never runs"
	}
	if len(n.counted().Inert) > 0 {
		return "; its expectations have no matcher, so they check nothing"
	}
	for rel := n.block.Header + 1; rel <= n.block.End && rel-1 < len(n.seg.lines); rel++ {
		switch strings.Trim(strings.TrimSpace(n.seg.lines[rel-1].Code), "{}();, \t") {
		case "", "pass", "...", "return", "end", "=>":
		default:
			return "" // it still runs code, it just checks nothing
		}
	}
	return "; its body is empty"
}

// fileGone reports a test file that no longer exists as a test file:
// deleted, or renamed to a name the test runner does not collect.
func (l *ledger) fileGone(f *core.FileInfo) bool {
	_, renamed := l.renamedTo[f]
	return renamed || f.Diff.Kind == diff.Deleted
}

// uncollectedHint coaches a test file renamed out of the test runner's
// reach.
const uncollectedHint = "Renaming a test file to a name the test runner does not collect switches all of its tests " +
	"off. Give it back a test file name (e.g. *.test.js, test_*.py, *_test.go, *Test.java) and fix the code the " +
	"tests exercise. If the new name is intended, change the runner's configuration in the same change so the " +
	"tests keep running."

// formerHint coaches a test that was switched off by renaming it or by
// removing its test marker.
const formerHint = "Turning a test into a plain function switches it off just like deleting it: the test runner " +
	"no longer collects it, so its assertions never run. Restore the test's name or its test annotation and fix " +
	"the code it exercises. If the test must be disabled, use the framework's skip marker with a reason that " +
	"links an issue, so the skip shows up in test reports."

// formerMessage describes a test that became a plain function.
func formerMessage(o *instance) string {
	fm := o.former
	var how string
	switch marker := removedMarker(o); {
	case fm.name != o.block.Bare:
		how = fmt.Sprintf("it was renamed to %q, which the test runner does not collect", fm.name)
	case marker != "":
		how = fmt.Sprintf("its %s marker was removed", marker)
	case o.seg.lang == lang.Python && strings.Contains(o.block.Name, "::"):
		how = "its class is no longer collected (pytest collects Test* classes and unittest.TestCase subclasses)"
	default:
		how = fmt.Sprintf("%q is no longer collected as a test", fm.name)
	}
	msg := fmt.Sprintf("test %q no longer runs: %s", o.name(), how)
	switch n := o.assertions(); n {
	case 0:
	case 1:
		msg += "; its assertion is still in the code"
	default:
		msg += fmt.Sprintf("; its %d assertions are still in the code", n)
	}
	return msg
}

// removedMarker returns the test attribute or annotation the change
// removed from above old test o (@Test, #[test], [Fact]), or "".
func removedMarker(o *instance) string {
	from, to := o.line(o.block.Start), o.header()
	for _, ln := range o.file.Removed {
		c := strings.TrimSpace(ln.Code)
		if from <= ln.No && ln.No < to && (strings.HasPrefix(c, "@") || strings.HasPrefix(c, "#[") || strings.HasPrefix(c, "[")) {
			return c
		}
	}
	return ""
}

func assertionNote(o *instance) string {
	if n := o.assertions(); n > 0 {
		return fmt.Sprintf(" (it had %s)", plural(n, "assertion"))
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func were(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// list joins up to max items and says how many more there are.
func list(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(" and %d more", len(items)-max)
}
