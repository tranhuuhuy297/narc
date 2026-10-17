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
	Summary:  "A test disappeared, or stopped being collected as a test, without being moved, renamed, split or merged elsewhere in the change; an error when source code changed too.",
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
		"The finding is a warning; it becomes an error when the same change also modifies non-test source code, " +
		"unless those edits are mostly deletions (dead code or a feature being removed).\n\n" +
		"```ts\n// before\nit('rejects expired tokens', () => {\n  expect(() => verify(expired)).toThrow(TokenExpiredError)\n})\n" +
		"// after: the test is gone and src/jwt.ts changed  <- BUSTED\n```",
}

var testFileDeleted = &core.Rule{
	ID:       "test-file-deleted",
	Title:    "Test file deleted",
	Category: "accounting",
	Severity: core.Warning,
	Summary:  "A test file was deleted, or renamed out of the test runner's reach, and its tests were not re-added elsewhere; an error when source code changed too.",
	Hint: "Restore the test file, or move its tests to their new home in the same change. If the code it tested was " +
		"removed, delete that code in the same change. Otherwise keep the deletion with a reason in .narc.yml " +
		"(allow: - rule: test-file-deleted, path: ..., reason: ...).",
	Docs: "Deleting a whole test file removes every check in it at once. narc lists the tests of a deleted test file " +
		"that do not reappear anywhere else in the change (by name or by body). A file whose tests all moved, or whose " +
		"code under test was deleted with it, is not reported. Renaming a test file to a name the test runner does " +
		"not collect (`pricing.test.js` → `pricing.disabled.js`) counts as deleting it.\n\n" +
		"The finding is a warning; it becomes an error when the same change also modifies non-test source code, " +
		"unless those edits are mostly deletions (dead code or a feature being removed).",
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
		"It is an error when assertions were simply deleted, and a warning when the test was rewritten with other " +
		"assertions or the change mostly deletes source code (a feature being removed). Assertions that call a " +
		"function the change deleted, or that became a run-time skip (reported by skip-added), are not counted.\n\n```py\n# before\ndef test_refund():\n    assert refund(order) == 40\n    assert order.status == \"refunded\"\n" +
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

func checkTestDeleted(a *core.Analysis) []core.Finding {
	l := ledgerFor(a)
	var out []core.Finding
	for _, o := range l.deletions {
		if l.fileGone(o.file) {
			continue // reported per file by test-file-deleted
		}
		var fd core.Finding
		if fm := o.former; fm != nil {
			// Anchored on the function that is left, where a waiver goes.
			fd = testDeleted.Finding(o.file, fm.line, formerMessage(o))
			if ln, ok := o.file.OldLine(o.header()); ok {
				fd.Before = strings.TrimSpace(ln.Raw)
			}
			fd.Hint = formerHint
			l.escalate(&fd)
			out = append(out, fd)
			continue
		}
		fd = testDeleted.Removal(o.file, o.header(), fmt.Sprintf("test %q was deleted%s", o.name(), assertionNote(o)))
		if why := l.excuse(o); why != "" {
			fd.Severity = core.Info
			fd.Message += why
		} else {
			linked := l.refsTouched(o) || l.coversModified(o.file)
			switch {
			case !linked:
			case l.reworkIn(o.file) > 0:
				// Other tests of the file were written anew: a rework of
				// the suite, not a lone deletion.
				fd.Message += fmt.Sprintf("; the same change wrote %s in this file", plural(l.reworkIn(o.file), "new test"))
				linked = false
			case l.testedElsewhere(o):
				fd.Message += "; tests the same change edited still call the code it exercised"
				linked = false
			}
			l.escalateLinked(&fd, linked)
		}
		out = append(out, fd)
	}
	return out
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

// escalateLinked raises a deletion finding to an error when the change
// also edited source code that the deleted test exercised (linked): the
// "break the code, delete the test" move. Source edits the test did not
// exercise, or that mostly delete code, leave it a warning.
func (l *ledger) escalateLinked(fd *core.Finding, linked bool) {
	if len(l.sourceChanged) == 0 {
		return
	}
	src := l.sourceList()
	switch {
	case l.removesSource():
		fd.Message += l.removalNote(src)
	case linked:
		fd.Severity = core.Error
		fd.Message += fmt.Sprintf(" while the same change modified source code (%s)", src)
	default:
		fd.Message += fmt.Sprintf(" while the same change modified source code it does not exercise (%s)", src)
	}
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
		} else if why := l.fileExcuse(f, tests); why != "" {
			fd.Severity = core.Info
			fd.Message += why
		} else {
			if linked && elsewhere {
				fd.Message += "; tests the same change edited still call the code they exercised"
				linked = false
			}
			l.escalateLinked(&fd, linked)
		}
		out = append(out, fd)
	}
	return out
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
		fd.Severity = l.dropSeverity(d, left)
		switch {
		case d.via != "":
			fd.Message += fmt.Sprintf(" while the test now hands its checks to %s", d.via)
		case d.rewritten:
			fd.Message += " while the test was rewritten with other assertions"
		case l.removesSource():
			// Checks of behavior that is being removed (see escalate).
			fd.Message += l.removalNote(l.sourceList())
		}
		if folded := d.old.assertions() - left - len(d.lost); folded > 0 {
			// Some removed assertions live on in new ones; say which did not.
			if len(d.lost) == 1 {
				fd.Message += "; no longer checked anywhere: " + d.lost[0].raw
			} else {
				fd.Message += fmt.Sprintf("; %d of them are no longer checked anywhere, such as: %s", len(d.lost), d.lost[0].raw)
			}
		}
		out = append(out, fd)
	}
	return out
}

// dropSeverity grades a drop to `left` assertions.
//
//   - An assertion turned into a statement that checks nothing (t.Errorf →
//     t.Logf, assert → print) is an error.
//   - A test that now hands its checks to a new helper is info, and so is
//     one rewritten with other assertions, unless it is left with at most
//     one of three or more (warning).
//   - When the test's file gained new tests (a rework) or the change
//     mostly deletes source code (a feature being removed), a warning.
//   - In a change that touches no source code, the assertions were simply
//     deleted: an error, unless all of them were absence checks (`not in`,
//     `.not.toBeInTheDocument()`), the weakest kind (warning).
//   - When source code changed too, the drop may follow a deliberate
//     change of behavior: an error only when the test is left with at most
//     one assertion and the change did not remove the value it expected
//     from the code; info when the test keeps three or more and lost at
//     most a quarter; else a warning.
func (l *ledger) dropSeverity(d drop, left int) core.Severity {
	old := d.old.assertions()
	switch {
	case l.neutered(d):
		return core.Error
	case d.via != "":
		return core.Info
	case d.rewritten:
		if old >= 3 && left <= 1 {
			return core.Warning
		}
		return core.Info
	case l.reworkIn(d.new.file) > 0, l.removesSource():
		return core.Warning
	case len(l.sourceChanged) == 0:
		if absenceOnly(d.lost) {
			return core.Warning
		}
		return core.Error
	case left <= 1 && !l.valueRemoved(d.lost):
		return core.Error
	case left >= 3 && 4*(old-left) <= old:
		return core.Info // a few of many checks, beside a change of the code
	}
	return core.Warning
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
var absenceRe = regexp.MustCompile(`\.\s*not\s*\.\s*(?:toBeInTheDocument|toBeVisible|toExist|toContain\w*|toHaveProperty|toHaveClass|toHaveAttribute|toMatch\w*|toHaveTextContent)\b|\bassertNotIn\b|\bassert\b.*\bnot\s+in\b|\bNotContains\b|\bassertNotContains\b`)

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
	for _, rel := range n.block.AssertionLines {
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
	if len(n.block.Unreachable) > 0 {
		if n.block.Return > 0 {
			return "; it returns before reaching its assertions"
		}
		return "; its assertions sit in a block that never runs"
	}
	if len(n.block.Inert) > 0 {
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
