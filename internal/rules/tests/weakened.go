package tests

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var assertionWeakened = &core.Rule{
	ID:       "assertion-weakened",
	Title:    "Assertion weakened",
	Category: "tests",
	Severity: core.Warning,
	Summary:  "A specific assertion was replaced by a vaguer one (toEqual(x) → toBeDefined(), assertRaises(ValueError) → assertRaises(Exception)), or a tolerance was widened.",
	Hint: "Keep the specific check. If the expected value really changed, assert the new correct value (and say why in " +
		"the commit); don't trade an exact assertion for one that passes on almost anything.",
	Docs: "A test can be defeated without deleting it: replace `expect(total).toEqual(42)` with " +
		"`expect(total).toBeDefined()` and it passes whatever the total is.\n\n" +
		"narc compares assertions that were replaced in place (a removed line directly followed by an added one) and " +
		"flags these weakenings on the same value:\n\n" +
		"- a value check (`toEqual`, `toBe`, `toMatchObject`, `toHaveBeenCalledWith`, `toHaveLength`, `toContain`, " +
		"`assertEqual`, `assertIn`, `assert a == b`, testify `Equal`/`ErrorIs`, `assertEquals`, `Assert.Equal`, " +
		"`assert_eq!`, RSpec `eq`, ...) replaced by an existence check (`toBeTruthy`, `toBeDefined`, " +
		"`not.toBeNull`, `toHaveBeenCalled()`, `assertTrue(x)`, `assertIsNotNone`, `assert a`, `NotNil`, " +
		"`NotEmpty`, `assertNotNull`, `assert!(x.is_ok())`, `be_truthy`, ...) or by a matcher that accepts " +
		"anything (`expect.anything()`, `expect.any(Object)`, `toMatchObject({})`, `expect.objectContaining({})`);\n" +
		"- a specific exception replaced by any exception, or its message check dropped " +
		"(`pytest.raises(ValueError, match=...)` → `pytest.raises(Exception)`, `toThrow(TypeError)` → `toThrow()`, " +
		"`#[should_panic(expected = ...)]` → `#[should_panic]`);\n" +
		"- a numeric tolerance widened (`toBeCloseTo` digits lowered, `pytest.approx` rel/abs raised, " +
		"`assertAlmostEqual` places lowered or delta raised, `InDelta`, `assertEquals(a, b, delta)`, `accuracy:`, " +
		"`.Within(...)`, `EXPECT_NEAR`, ...).\n\n" +
		"Swapping `toBe(true)` for `toBeTruthy()` is treated as style, not weakening. A boolean literal is a " +
		"specific value: `toBe(false)` → `toBe(true)` or `toBe(flag)` → `toBe(true)` on the same subject is a " +
		"changed expectation, and so is an absence check flipped to a presence check (`toHaveCount(0)` → " +
		"`toBeVisible()`). Assertions are compared as whole statements (`toBe(\\n  true\\n)`) and only within " +
		"the same test; when the function under test changed its signature in the same change the finding is a " +
		"note, and `pytest.raises(E) as exc_info` followed by assertions on `exc_info.value` still checks the " +
		"exception.",
}

func init() {
	assertionWeakened.Check = checkWeakened
	core.Register(assertionWeakened)
}

func checkWeakened(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range testFiles(a) {
		// Parse every added line once: the assertions of each replacement
		// block, and the set of all added assertions, which tells whether a
		// removed assertion survives somewhere else in the file.
		added := map[int][]assertion{}
		survivors := map[string]bool{}
		for _, l := range f.Added {
			as := parseAssertions(f.Lang, statement(f, l, newVersion))
			added[l.No] = as
			for _, x := range as {
				survivors[rulekit.Normalize(x.text)] = true
			}
		}
		for _, p := range rulekit.Pairs(f) {
			if len(p.Removed) == 0 || len(p.Added) == 0 {
				continue
			}
			var news []located
			for i, l := range p.Added {
				for _, x := range added[l.No] {
					news = append(news, located{assertion: x, line: statement(f, l, newVersion), idx: i})
				}
				for _, h := range helperChecks(f.Lang, l) {
					// a custom assertion helper: expectInvoice(res.body, want)
					news = append(news, located{assertion: assertion{fn: h.name, str: checksValue, subjects: h.args, helper: true}, line: l, idx: i})
				}
			}
			olds := assertionsIn(f, p.Removed, oldVersion)
			out = append(out, weakenedAssertions(a.Files, f, olds, news, survivors, len(p.Removed), len(p.Added))...)
			out = append(out, widenedTolerances(f, p)...)
		}
	}
	return out
}

// located is an assertion with the line it is on and the position of that
// line in its side of a replacement block.
type located struct {
	assertion
	line core.Line
	idx  int
}

// statement returns line l of version v of f as the whole assertion
// statement it belongs to: joined with the lines above it that a formatter
// split off (see chained), and with the lines below it when a matcher's
// arguments continue there:
//
//	expect(rows.every(isPaid)).toBe(      await expect(p).rejects.toThrow(
//	  true                                  'No claimed thread owner responded',
//	);                                    );
func statement(f *core.FileInfo, l core.Line, v version) core.Line {
	l = chained(f, l, v)
	code := strings.TrimSpace(l.Code)
	switch {
	case strings.HasSuffix(code, "(") || strings.HasSuffix(code, ","):
		m := openMatcherRe.FindStringSubmatch(code)
		if (m == nil || !knownAssertion(m[1]) && !assertNameRe.MatchString(m[1])) && !assertStmtRe.MatchString(code) {
			return l
		}
	case strings.HasSuffix(code, "["):
		// assert [(u, p) for u, p in sent] == [\n    ("a", 1),\n]
		if !assertStmtRe.MatchString(code) {
			return l
		}
	default:
		return l
	}
	get := v.getter(f)
	depth := bracketDepth(l.Code)
	for k := l.No + 1; k <= l.No+maxJoin && depth > 0; k++ {
		next, ok := get(k)
		if !ok {
			break
		}
		if continuesChain(next.Code) {
			// `expect(` / `  value,` / `).toBe(true)`: the chain line below
			// reads the whole statement (see chained); reading it here too
			// would count the assertion twice.
			return l
		}
		depth += bracketDepth(next.Code)
	}
	return forwardJoin(get, l, maxJoin)
}

var (
	// openMatcherRe matches a line that ends inside the argument list of a
	// call; group 1 is the called name.
	openMatcherRe = regexp.MustCompile(`([A-Za-z_$][\w$]*!?)\s*\((?:[^()]*,)?\s*$`)
	// assertStmtRe matches an assert statement (Python, Elixir).
	assertStmtRe = regexp.MustCompile(`^assert\b`)
	// assertNameRe matches names of assertion functions.
	assertNameRe = regexp.MustCompile(`^(?:assert|expect|verify|check|should|must|ensure|require)\w*!?$|^(?:Equal|NotEqual|Equals|True|False|Nil|NotNil|Error|ErrorIs|ErrorContains|Contains|Len|Same|Throws|ThrowsAsync|Raises)$`)
)

// assertionsIn parses the assertions on lines of version v of f.
func assertionsIn(f *core.FileInfo, lines []core.Line, v version) []located {
	var out []located
	for i, l := range lines {
		l = statement(f, l, v)
		for _, a := range parseAssertions(f.Lang, l) {
			out = append(out, located{assertion: a, line: l, idx: i})
		}
	}
	return out
}

// pairWindow bounds how far apart (in lines, relative to the size of each
// side) an old and a new assertion of one replacement block may be to be
// compared. Small blocks are compared in full; a file rewritten in one
// block stays linear.
const pairWindow = 40

// near returns the range of news (sorted by idx) whose lines sit near the
// position of the old line oldIdx, scaled from nOld to nNew lines.
func near(news []located, oldIdx, nOld, nNew int) []located {
	if nOld <= 2*pairWindow && nNew <= 2*pairWindow {
		return news
	}
	center := oldIdx * nNew / max(nOld, 1)
	lo := sort.Search(len(news), func(i int) bool { return news[i].idx >= center-pairWindow })
	hi := sort.Search(len(news), func(i int) bool { return news[i].idx > center+pairWindow })
	return news[lo:hi]
}

func weakenedAssertions(files []*core.FileInfo, f *core.FileInfo, olds, news []located, survivors map[string]bool, nOld, nNew int) []core.Finding {
	if len(olds) == 0 || len(news) == 0 {
		return nil
	}
	used := map[*located]bool{}
	// Tautologies are the tautology rule's business, not a weakening.
	taut := map[*located]bool{}
	for i := range news {
		n := &news[i]
		if n.helper {
			continue
		}
		if _, litOK := valueText(n.line, f.Lang); cannotFail(n.assertion, f.Lang, litOK) != "" {
			taut[n] = true
		}
	}
	tests := testPairing{f: f}
	var out []core.Finding
	for _, o := range olds {
		if o.str != checksValue && o.str != checksThrow {
			continue
		}
		if survivors[rulekit.Normalize(o.text)] {
			continue // the specific assertion is still there (moved or duplicated)
		}
		cands := near(news, o.idx, nOld, nNew)
		if replacedBySpecific(o, cands) {
			continue
		}
		for i := range cands {
			n := &cands[i]
			if used[n] || taut[n] || n.helper {
				continue
			}
			why := weakerBy(o, *n, f.Lang)
			if why == "" || !tests.same(o, *n) {
				continue
			}
			if o.str == checksThrow && inspectsCaught(f, *n) {
				continue // pytest.raises(E) as exc_info, then asserts on exc_info.value
			}
			used[n] = true
			sev := assertionWeakened.Severity
			if fn := calledFunc(o.assertion); fn != "" && signatureChanged(files, fn) {
				// The function under test now returns something else (an enum
				// became a bool, a string an object): the assertion follows it.
				sev = core.Info
				why += "; " + fn + "() changed its signature in this change, so the check may just follow its new return type"
			}
			msg := fmt.Sprintf("assertion weakened: %s → %s (%s)", o.assertion, n.assertion, why)
			name := testAround(f, n.line.No)
			if strings.HasPrefix(n.text, "#[") || strings.HasPrefix(n.text, "@") {
				name = testAfter(f, n.line.No) // an attribute: #[should_panic], @Test(expected = ...)
			}
			if name.ok() {
				msg += fmt.Sprintf(" in %s", name)
			}
			fd := assertionWeakened.Finding(f, n.line.No, msg)
			fd.Before = strings.TrimSpace(o.line.Raw)
			fd.Severity = sev
			out = append(out, fd)
			break
		}
	}
	return out
}

// replacedBySpecific reports whether the new block still checks the same
// subject at least as specifically as o did, possibly through a custom
// assertion helper.
func replacedBySpecific(o located, news []located) bool {
	for _, n := range news {
		if n.helper && o.str == checksValue && related(o.assertion, n.assertion) {
			return true
		}
		if n.str != o.str || !related(o.assertion, n.assertion) {
			continue
		}
		if o.str == checksValue || throwWhy(o.assertion, n.assertion) == "" {
			return true
		}
	}
	return false
}

// helperCallRe matches calls to custom assertion helpers: expectInvoice(...),
// assertValidToken(...), verify_receipt(...), checkResponse(...).
var helperCallRe = regexp.MustCompile(`(?:^|[^\w$.])((?:expect|assert|verify|check|should)(?:[A-Z]|_[a-z])\w*)\s*\(`)

// helperCall is a call to a custom assertion helper and its arguments.
type helperCall struct {
	name string
	args []expr
}

// helperChecks returns the custom assertion helpers called on line l: a
// specific check moved into a helper is not lost.
func helperChecks(lg lang.Lang, l core.Line) []helperCall {
	if !strings.Contains(l.Code, "(") {
		return nil
	}
	vt, _ := valueText(l, lg)
	var out []helperCall
	for _, m := range helperCallRe.FindAllStringSubmatchIndex(vt, -1) {
		name := vt[m[2]:m[3]]
		if knownAssertion(name) {
			continue
		}
		if as, _, ok := args(vt, m[1]-1); ok && len(as) > 0 {
			out = append(out, helperCall{name: name, args: as})
		}
	}
	return out
}

// knownAssertion reports whether name is an assertion the parser knows,
// rather than a project's own helper.
func knownAssertion(name string) bool {
	if _, ok := anyRecv[name]; ok {
		return true
	}
	if _, ok := jsRecv[name]; ok {
		return true
	}
	if _, ok := capRecv[name]; ok {
		return true
	}
	if _, ok := matchers[name]; ok {
		return true
	}
	switch name {
	case "expectThat", "assertThat", "assert_that", "assertThatThrownBy", "assertThatExceptionOfType", "assertThatCode":
		return true
	}
	return false
}

// weakerBy explains how n is a weaker version of o, or returns "".
func weakerBy(o, n located, lg lang.Lang) string {
	switch {
	case o.str == checksValue && n.str == checksExist:
		if !related(o.assertion, n.assertion) || styleOnly(o.assertion, n.assertion, lg) || boolLiteralOnSame(o.assertion, n.assertion, lg) ||
			absenceCheck(o.assertion, lg) {
			return ""
		}
		if len(n.values) > 1 && vague(n.fn, n.values[1]) {
			return fmt.Sprintf("the expected value became %s, which accepts almost anything", n.values[1])
		}
		return "a specific value check became an existence check"
	case o.str == checksThrow && n.str == checksThrow:
		if len(o.subjects) > 0 && len(n.subjects) > 0 && !related(o.assertion, n.assertion) {
			return ""
		}
		return throwWhy(o.assertion, n.assertion)
	}
	return ""
}

// throwWhy explains how exception assertion n is looser than o.
func throwWhy(o, n assertion) string {
	switch {
	case o.exc != "" && !genericException(o.exc) && (n.exc == "" || genericException(n.exc)) && !(n.match && !o.match):
		// (toThrow(TypeError) → toThrow('Tenant ID cannot be "common"') trades the
		// type for the message, which is no vaguer.)
		any := "any exception"
		if n.exc != "" {
			any = n.exc
		}
		return fmt.Sprintf("expected exception %s widened to %s", o.exc, any)
	case o.match && !n.match && (n.exc == o.exc || n.exc == ""):
		return "the exception message is no longer checked"
	}
	return ""
}

// boolLiteralOnSame reports whether n compares the very subject that o
// checked with a boolean literal: toBe(native) → toBe(true), Equal(t,
// false, x) → Equal(t, true, x). A boolean literal is a specific value, so
// this is a changed expectation, not an existence check.
func boolLiteralOnSame(o, n assertion, lg lang.Lang) bool {
	if n.pass != passEqual || len(n.values) != 2 {
		return false
	}
	for i, v := range n.values {
		if lit(v, lg).kind != litBool {
			continue
		}
		other := n.values[1-i]
		for _, ov := range o.values {
			if ov.norm != "" && ov.norm == other.norm {
				return true
			}
		}
	}
	return false
}

// absenceCheck reports whether o checks that something is absent, a
// count or length of zero: toHaveCount(0), toHaveLength(0), HaveLen(0),
// assertEqual(len(x), 0). Replacing it with a presence check (toBeVisible,
// toBeTruthy) flips the expectation; it does not weaken it.
func absenceCheck(o assertion, lg lang.Lang) bool {
	switch o.fn {
	case "toHaveLength", "toHaveCount", "HaveLen", "hasSize", "toHaveSize", "lengthOf", "length", "HaveCount", "HaveLength", "Len", "assertCount", "hasLength":
		for _, v := range o.values {
			if l := lit(v, lg); l.kind == litNumber && l.num == 0 {
				return true
			}
		}
		if len(o.subjects) > 1 {
			if l := lit(o.subjects[len(o.subjects)-1], lg); l.kind == litNumber && l.num == 0 {
				return true
			}
		}
	}
	if o.pass == passEqual && len(o.values) == 2 {
		for i, v := range o.values {
			if l := lit(v, lg); l.kind == litNumber && l.num == 0 && lengthLike(o.values[1-i].raw) {
				return true
			}
		}
	}
	return false
}

// lengthLike reports whether an expression measures a size: len(x),
// x.length, x.size(), x.count.
func lengthLike(s string) bool {
	return strings.HasPrefix(s, "len(") || strings.HasSuffix(s, ".length") || strings.HasSuffix(s, ".size") ||
		strings.HasSuffix(s, ".size()") || strings.HasSuffix(s, ".count") || strings.HasSuffix(s, ".count()") || strings.HasSuffix(s, ".len()")
}

// testPairing tells whether an old and a new assertion of f belong to the
// same test, caching what it looks up.
type testPairing struct {
	f        *core.FileInfo
	oldTests map[int]testName // old line → its test
	newTests map[int]testName // new line → its test
	inNew    map[string]bool  // test name → declared in the new version
	inOld    map[string]bool  // test name → declared in the old version
}

// same reports whether the old assertion o and the new assertion n belong
// to the same test, so that they may be compared: a replacement block can
// span a test that was moved and a neighbour that was deleted. Assertions
// in tests of different names are compared only when the old test is gone
// and the new one is new (a rename).
func (p *testPairing) same(o, n located) bool {
	if p.oldTests == nil {
		p.oldTests, p.newTests = map[int]testName{}, map[int]testName{}
		p.inNew, p.inOld = map[string]bool{}, map[string]bool{}
	}
	on, ok := p.oldTests[o.line.No]
	if !ok {
		on = testAboveIn(p.f.OldLine, o.line.No, false)
		p.oldTests[o.line.No] = on
	}
	nn, ok := p.newTests[n.line.No]
	if !ok {
		nn = testAround(p.f, n.line.No)
		p.newTests[n.line.No] = nn
	}
	if !on.ok() || !nn.ok() || on.name == nn.name {
		return true
	}
	declared := func(cache map[string]bool, v version, t testName) bool {
		d, ok := cache[t.name]
		if !ok {
			d = declaredIn(v.getter(p.f), lastLine(p.f, v), t)
			cache[t.name] = d
		}
		return d
	}
	return !declared(p.inNew, newVersion, on) && !declared(p.inOld, oldVersion, nn)
}

// caughtAsRe matches an exception assertion that binds what was raised:
// `with pytest.raises(E) as exc_info:`, `err := assert...`, `e = assertThrows(...)`,
// `val e = shouldThrow<E> {`.
var caughtAsRe = regexp.MustCompile(`\bas\s+([A-Za-z_]\w*)\s*[:,)]*\s*$|^\s*(?:(?:val|var|let|const|final)\s+)?(?:[\w.<>]+\s+)?([A-Za-z_]\w*)\s*:?=\s*(?:await\s+)?[\w.]*(?:raises|Raises|Throws|throws|Throw|shouldThrow|expectThrows|assertThrows|catchThrowable|assertRaises)\b`)

// inspectsCaught reports whether the exception assertion n binds the
// exception and a following assertion inspects it, which checks at least as
// much as a message match: `with pytest.raises(HomeAssistantError) as
// exc_info:` followed by `assert exc_info.value.translation_key == "x"`.
func inspectsCaught(f *core.FileInfo, n located) bool {
	m := caughtAsRe.FindStringSubmatch(n.line.Code)
	if m == nil {
		return false
	}
	name := m[1]
	if name == "" {
		name = m[2]
	}
	word := regexp.MustCompile(`(?:^|[^\w.$])` + regexp.QuoteMeta(name) + `\b`)
	for k := n.line.No + 1; k <= n.line.No+25; k++ {
		l, ok := f.NewLine(k)
		if !ok {
			break
		}
		if word.MatchString(l.Code) && len(parseAssertions(f.Lang, core.Line{LexedLine: l, No: k})) > 0 {
			return true
		}
	}
	return false
}

// calledRe matches the function that a subject calls last: `x.is_ignored(`
// in project.is_ignored(&path), formatPrice( in formatPrice(0.5).value.
var calledRe = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*\(`)

// calledFunc returns the function the subject of a calls (the outermost
// call), or "".
func calledFunc(a assertion) string {
	for _, v := range a.values {
		if isLiteral(v) {
			continue
		}
		if m := calledRe.FindStringSubmatch(v.raw); m != nil {
			switch m[1] {
			case "len", "str", "int", "list", "dict", "set", "tuple", "String", "Number", "Boolean", "JSON", "parse", "stringify":
				continue
			}
			return m[1]
		}
	}
	return ""
}

// signatureChanged reports whether the change edited a declaration of the
// function fn: a removed and an added line both declare it, in any file.
func signatureChanged(files []*core.FileInfo, fn string) bool {
	decl := regexp.MustCompile(`\b(?:fn|def|func|function|fun|sub)\s+(?:\([^)]*\)\s*)?` + regexp.QuoteMeta(fn) + `\b|\b(?:const|let|var)\s+` + regexp.QuoteMeta(fn) + `\s*[:=]|^\s*(?:(?:export|public|private|protected|static|async|override|pub(?:\([^)]*\))?)\s+)*` + regexp.QuoteMeta(fn) + `\s*(?:<[^>]*>)?\s*\([^)]*\)\s*(?::|->|\{)`)
	removed, added := false, false
	for _, f := range files {
		for _, l := range f.Removed {
			if strings.Contains(l.Code, fn) && decl.MatchString(l.Code) {
				removed = true
				break
			}
		}
		for _, l := range f.Added {
			if strings.Contains(l.Code, fn) && decl.MatchString(l.Code) {
				added = true
				break
			}
		}
		if removed && added {
			return true
		}
	}
	return false
}

// styleOnly reports swaps that do not lose information:
// toBe(true) → toBeTruthy(), assertEquals(true, x) → assertTrue(x).
func styleOnly(o, n assertion, lg lang.Lang) bool {
	if o.pass != passEqual || len(o.values) < 2 {
		return false
	}
	for _, v := range o.values {
		if lit(v, lg).kind == litBool && (n.pass == passTruthy || n.pass == passFalsy) {
			return true
		}
	}
	return false
}

// related reports whether two assertions are about the same value: the same
// subject, or one subject nested in the other (res.body / res.body.total,
// items / len(items)).
func related(a, b assertion) bool {
	for _, x := range a.subjects {
		for _, y := range b.subjects {
			if relatedExpr(x, y) {
				return true
			}
		}
	}
	return false
}

func relatedExpr(x, y expr) bool {
	if x.norm == "" || y.norm == "" || isLiteral(x) || isLiteral(y) {
		return false
	}
	if x.norm == y.norm {
		return true
	}
	short, long := x.norm, y.norm
	if len(short) > len(long) {
		short, long = long, short
	}
	for i := strings.Index(long, short); i >= 0; {
		before := i == 0 || !isWordByte(long[i-1]) && long[i-1] != '.' && long[i-1] != '$'
		j := i + len(short)
		after := j == len(long) || !isWordByte(long[j])
		if before && after {
			return true
		}
		k := strings.Index(long[i+1:], short)
		if k < 0 {
			break
		}
		i += 1 + k
	}
	return false
}

// ---- tolerances ------------------------------------------------------------------

// tolParam describes one tolerance argument of an approximate assertion.
type tolParam struct {
	name   string  // shown in messages
	pos    int     // index among the positional arguments, -1 if keyword-only
	kw     string  // keyword name, "" if positional-only
	def    float64 // default when omitted
	hasDef bool
	looser bool // true: a higher value is looser (delta, rel); false: lower is looser (digits, places)
}

// tolSpecs maps approximate-assertion functions to their tolerance
// parameters. Positions count all positional arguments.
var tolSpecs = map[string][]tolParam{
	"toBeCloseTo":                {{name: "digits", pos: 1, def: 2, hasDef: true}},
	"closeTo":                    {{name: "delta", pos: 1, looser: true}},
	"approximately":              {{name: "delta", pos: 1, looser: true}},
	"approx":                     {{name: "rel", pos: 1, kw: "rel", def: 1e-6, hasDef: true, looser: true}, {name: "abs", pos: 2, kw: "abs", def: 1e-12, hasDef: true, looser: true}},
	"assertAlmostEqual":          {{name: "places", pos: 2, kw: "places", def: 7, hasDef: true}, {name: "delta", pos: -1, kw: "delta", looser: true}},
	"assert_allclose":            {{name: "rtol", pos: 2, kw: "rtol", def: 1e-7, hasDef: true, looser: true}, {name: "atol", pos: 3, kw: "atol", def: 0, hasDef: true, looser: true}},
	"allclose":                   {{name: "rtol", pos: 2, kw: "rtol", def: 1e-5, hasDef: true, looser: true}, {name: "atol", pos: 3, kw: "atol", def: 1e-8, hasDef: true, looser: true}},
	"isclose":                    {{name: "rel_tol", pos: -1, kw: "rel_tol", def: 1e-9, hasDef: true, looser: true}, {name: "abs_tol", pos: -1, kw: "abs_tol", def: 0, hasDef: true, looser: true}, {name: "rtol", pos: 2, kw: "rtol", looser: true}, {name: "atol", pos: 3, kw: "atol", looser: true}},
	"assert_almost_equal":        {{name: "decimal", pos: 2, kw: "decimal", def: 7, hasDef: true}},
	"assert_array_almost_equal":  {{name: "decimal", pos: 2, kw: "decimal", def: 6, hasDef: true}},
	"InDelta":                    {{name: "delta", pos: 3, looser: true}},
	"InEpsilon":                  {{name: "epsilon", pos: 3, looser: true}},
	"InDeltaSlice":               {{name: "delta", pos: 3, looser: true}},
	"XCTAssertEqual":             {{name: "accuracy", pos: -1, kw: "accuracy", looser: true}},
	"XCTAssertEqualWithAccuracy": {{name: "accuracy", pos: 2, looser: true}},
	"Within":                     {{name: "tolerance", pos: 0, looser: true}},
	"assert_relative_eq!":        {{name: "epsilon", pos: -1, kw: "epsilon", looser: true}, {name: "max_relative", pos: -1, kw: "max_relative", looser: true}},
	"assert_abs_diff_eq!":        {{name: "epsilon", pos: -1, kw: "epsilon", looser: true}},
	"assert_ulps_eq!":            {{name: "max_ulps", pos: -1, kw: "max_ulps", looser: true}, {name: "epsilon", pos: -1, kw: "epsilon", looser: true}},
	"EXPECT_NEAR":                {{name: "abs_error", pos: 2, looser: true}},
	"ASSERT_NEAR":                {{name: "abs_error", pos: 2, looser: true}},
	"assert_in_delta":            {{name: "delta", pos: 2, def: 0.001, hasDef: true, looser: true}},
	"assert_in_epsilon":          {{name: "epsilon", pos: 2, def: 0.001, hasDef: true, looser: true}},
	"be_within":                  {{name: "delta", pos: 0, looser: true}},
	"BeApproximately":            {{name: "precision", pos: 1, looser: true}},
	"isCloseTo":                  {{name: "offset", pos: 1, looser: true}},
	"assertEquals":               {{name: "delta", pos: 2, looser: true}, {name: "absoluteTolerance", pos: -1, kw: "absoluteTolerance", looser: true}},
	"Equal":                      {{name: "precision", pos: 2, def: 0, looser: false}, {name: "tolerance", pos: -1, kw: "tolerance", looser: true}},
}

var tolCallRe = regexp.MustCompile(`(?:^|[^\w$])(toBeCloseTo|closeTo|approximately|approx|assertAlmostEqual|assert_allclose|allclose|isclose|assert_almost_equal|assert_array_almost_equal|InDelta|InEpsilon|InDeltaSlice|XCTAssertEqual|XCTAssertEqualWithAccuracy|Within|assert_relative_eq!|assert_abs_diff_eq!|assert_ulps_eq!|EXPECT_NEAR|ASSERT_NEAR|assert_in_delta|assert_in_epsilon|be_within|BeApproximately|isCloseTo|assertEquals|Equal)\s*\(`)

var tolWrapRe = regexp.MustCompile(`^(?:within|offset|byLessThan|Offset\.offset|withinPercentage|Percentage\.withPercentage)\s*\((.*)\)$`)

// tolerance is one tolerance setting found on a line.
type tolerance struct {
	fn, param string
	value     float64
	text      string // as written, or "default 1e-06"
	looser    bool
	anchor    string // the compared values, to pair old and new
	line      core.Line
}

func tolerancesIn(f *core.FileInfo, lines []core.Line) []tolerance {
	var out []tolerance
	for _, l := range lines {
		vt, _ := valueText(l, f.Lang)
		for _, m := range tolCallRe.FindAllStringSubmatchIndex(vt, -1) {
			fn := vt[m[2]:m[3]]
			if !tolApplies(fn, f.Lang) {
				continue
			}
			as, _, ok := args(vt, m[1]-1)
			if !ok {
				continue
			}
			if f.Lang == lang.Go && len(as) > 0 && handles[as[0].norm] {
				as = as[1:]
			}
			pos := positional(as)
			if fn == "assertEquals" && isJVM(f.Lang) && len(pos) == 4 && isStr(pos[0]) {
				pos = pos[1:] // JUnit 4: assertEquals("msg", expected, actual, delta)
			}
			if f.Lang == lang.Go {
				// testify positions count the test handle: InDelta(t, a, b, d)
				pos = append([]expr{{}}, pos...)
			}
			var anchor []string
			for i, a := range pos {
				if i < 2 && a.norm != "" {
					anchor = append(anchor, a.norm)
				}
			}
			for _, tp := range tolSpecs[fn] {
				t := tolerance{fn: fn, param: tp.name, looser: tp.looser, anchor: strings.Join(anchor, ","), line: l}
				var v expr
				found := false
				if tp.kw != "" {
					v, found = kwarg(as, tp.kw)
				}
				if !found && tp.pos >= 0 && tp.pos < len(pos) {
					v, found = pos[tp.pos], true
				}
				switch {
				case found:
					if w := tolWrapRe.FindStringSubmatch(v.raw); w != nil {
						v = newExpr(w[1])
					}
					n, ok := parseNumber(v.raw)
					if !ok {
						continue
					}
					t.value, t.text = n, display(v.raw)
				case tp.hasDef:
					t.value, t.text = tp.def, "default "+strconv.FormatFloat(tp.def, 'g', -1, 64)
				default:
					continue
				}
				out = append(out, t)
			}
		}
	}
	return out
}

// tolApplies filters tolerance functions whose names are too common to
// trust in every language.
func tolApplies(fn string, lg lang.Lang) bool {
	switch fn {
	case "assertEquals":
		return isJVM(lg)
	case "Equal":
		return lg == lang.CSharp
	case "Within":
		return lg == lang.CSharp
	case "approx", "assertAlmostEqual", "assert_allclose", "allclose", "isclose", "assert_almost_equal", "assert_array_almost_equal":
		return lg == lang.Python
	case "InDelta", "InEpsilon", "InDeltaSlice":
		return lg == lang.Go
	case "assert_in_delta", "assert_in_epsilon", "be_within":
		return lg == lang.Ruby
	}
	return true
}

func widenedTolerances(f *core.FileInfo, p rulekit.Pair) []core.Finding {
	olds, news := tolerancesIn(f, p.Removed), tolerancesIn(f, p.Added)
	if len(olds) == 0 || len(news) == 0 {
		return nil
	}
	type param struct{ fn, name string }
	oldCount := map[param]int{}
	for _, t := range olds {
		oldCount[param{t.fn, t.param}]++
	}
	type anchored struct {
		param
		anchor string
	}
	newsOf := map[param][]int{} // indexes into news
	byAnchor := map[anchored][]int{}
	for i, t := range news {
		k := param{t.fn, t.param}
		newsOf[k] = append(newsOf[k], i)
		byAnchor[anchored{k, t.anchor}] = append(byAnchor[anchored{k, t.anchor}], i)
	}
	var out []core.Finding
	used := make([]bool, len(news))
	for _, o := range olds {
		// Pair by function, parameter and compared values; fall back to the
		// only candidate when the expected value changed too.
		k := param{o.fn, o.param}
		cands := newsOf[k]
		best := -1
		for _, i := range byAnchor[anchored{k, o.anchor}] {
			if !used[i] {
				best = i
				break
			}
		}
		if best < 0 && oldCount[k] == 1 && len(cands) == 1 && !used[cands[0]] {
			best = cands[0]
		}
		if best < 0 {
			continue
		}
		n := news[best]
		used[best] = true
		if n.value == o.value || (n.value > o.value) != o.looser {
			continue
		}
		fn := strings.TrimSuffix(o.fn, "!")
		if fn == "approx" {
			fn = "pytest.approx"
		}
		msg := fmt.Sprintf("tolerance widened: %s %s %s → %s", fn, o.param, o.text, n.text)
		if name := testAround(f, n.line.No); name.ok() {
			msg += fmt.Sprintf(" in %s", name)
		}
		fd := assertionWeakened.Finding(f, n.line.No, msg)
		fd.Before = strings.TrimSpace(o.line.Raw)
		fd.Hint = "Keep the original tolerance and fix the computation. Widen a tolerance only for a numerical reason " +
			"you can explain (and explain it in a comment)."
		out = append(out, fd)
	}
	return out
}
