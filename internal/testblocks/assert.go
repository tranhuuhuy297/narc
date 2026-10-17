package testblocks

import (
	"regexp"
	"sort"
	"strings"

	"github.com/huyth45/narc/internal/lang"
)

// IsAssertion reports whether a line of code (lexed Code text: comments
// removed, string contents blanked) contains an assertion.
func IsAssertion(l lang.Lang, code string) bool { return CountAssertions(l, code) > 0 }

// CountAssertions counts the assertions on a line of code (lexed Code
// text). A chained assertion such as expect(x).not.toBe(y) counts once;
// two statements on one line count twice.
func CountAssertions(l lang.Lang, code string) int { return len(assertionSpans(l, code, true)) }

// IsHelperCall reports whether the only assertions on a line of code are
// statement-position calls to helpers named verify*, check*, should*,
// must* or ensure*. Such helpers usually wrap assertions, but some are
// setup (mustExec(t, db, sql), ensureDir(p)), so callers may want to
// weigh them less than a direct assertion.
func IsHelperCall(l lang.Lang, code string) bool {
	return len(assertionSpans(l, code, false)) == 0 && len(assertionSpans(l, code, true)) > 0
}

// assertionSpans returns the column ranges of the assertions on a line of
// Code text, in order, one per assertion. helpers includes the
// verify*/check*/... helper calls.
func assertionSpans(l lang.Lang, code string, helpers bool) []span {
	return matchAssertions(l, code, helpers, true)
}

// matchAssertions implements assertionSpans. prefilter enables the cheap
// substring checks that skip patterns which cannot match; tests turn it
// off to prove that it never changes the result.
func matchAssertions(l lang.Lang, code string, helpers, prefilter bool) []span {
	set := assertSets[l]
	if prefilter && !mayAssert(code, set.triggers) {
		return nil
	}
	var spans []span
	collect := func(res []*regexp.Regexp, stmt bool) {
		for _, re := range res {
			if ws := needs[re]; prefilter && ws != nil && !containsAny(code, ws) {
				continue
			}
			for _, m := range re.FindAllStringSubmatchIndex(code, -1) {
				s, e := m[0], m[1]
				if len(m) >= 4 && m[2] >= 0 {
					s, e = m[2], m[3]
				}
				if defBefore.MatchString(code[:s]) {
					continue // the definition of a helper, not a call
				}
				if stmt && !atStatement(code, s) {
					continue
				}
				if set.not != nil && set.not.MatchString(code[s:e]) {
					continue
				}
				spans = append(spans, span{s, e})
			}
		}
	}
	collect(genericAny, false)
	if helpers {
		collect(genericStmt, true)
	}
	collect(set.any, false)
	collect(set.stmt, true)
	if len(spans) == 0 {
		return nil
	}
	// Several patterns may match the same assertion; merge overlaps.
	sort.Slice(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
	out := spans[:0]
	for _, s := range spans {
		if n := len(out); n > 0 && s.from < out[n-1].to {
			out[n-1].to = max(out[n-1].to, s.to)
			continue
		}
		out = append(out, s)
	}
	return out
}

// needs lists, for the patterns tried on most lines, substrings without
// which they cannot match, so a line that only mentions expect does not
// run every assertion pattern.
var needs = map[*regexp.Regexp][]string{}

func init() {
	need := func(re *regexp.Regexp, words ...string) { needs[re] = words }
	need(genericAny[0], "assert", "expect")
	need(genericAny[1], "assert", "expect", "verify")
	need(genericAny[2], "assert")
	need(genericAny[3], "expect")
	need(genericStmt[0], "verify", "check", "should", "must", "ensure")
	need(jsAsserts.any[0], "assert")
	need(jsAsserts.any[1], "should")
	need(jsAsserts.any[2], "expect")
	need(jsAsserts.any[3], "assert")
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// genericTriggers are substrings that every generic assertion contains.
var genericTriggers = []string{"assert", "expect", "verify", "check", "should", "must", "ensure"}

// mayAssert is a cheap filter before the regular expressions: most lines
// of a test file contain none of the words an assertion needs.
func mayAssert(code string, extra []string) bool {
	for _, w := range genericTriggers {
		if strings.Contains(code, w) {
			return true
		}
	}
	for _, w := range extra {
		if strings.Contains(code, w) {
			return true
		}
	}
	return false
}

// defBefore matches the text before a helper's name in its definition
// (function assertValid(..., def check_user(..., func (s *S) assertOK(...).
var defBefore = regexp.MustCompile(`(?:^|[^\w])(?:function\*?|def|defp|func|fn|fun|void|sub)\s+(?:\([^)]*\)\s*)?$`)

// stmtWordBefore matches keywords after which a call is in statement
// position (await check(x), try verifyAll(), return expectOk(r), ...).
var stmtWordBefore = regexp.MustCompile(`(?:^|[^\w])(?:await|try|return|do|then|else|yield|defer|and)[?!]?$`)

// atStatement reports whether the call starting at code[head] is a
// statement of its own, i.e. its result is not assigned or passed on.
// Helpers such as verifyToken(t) only count as assertions there:
// `claims := verifyToken(t)` exercises the code under test instead.
func atStatement(code string, head int) bool {
	p := strings.TrimRight(code[:head], " \t")
	if p == "" {
		return true
	}
	// Inside an argument list, as in expect(() => verify(token)), the call
	// is an operand of something else, even after an arrow.
	depth := 0
scan:
	for i := len(p) - 1; i >= 0; i-- {
		switch p[i] {
		case ')', ']':
			depth--
		case '(', '[':
			depth++
		case '{', ';':
			break scan
		}
	}
	if depth > 0 {
		return false
	}
	switch p[len(p)-1] {
	case ';', '{', '}', ':', '|':
		return true
	}
	if strings.HasSuffix(p, "=>") || strings.HasSuffix(p, "->") {
		return true
	}
	return stmtWordBefore.MatchString(p)
}

// assertSet holds the language-specific assertion patterns. When a pattern
// has a capture group, the assertion starts at group 1 (the leading
// context is only there to reject e.g. method calls).
type assertSet struct {
	any  []*regexp.Regexp // count wherever they occur
	stmt []*regexp.Regexp // count only in statement position
	// triggers: every match of any/stmt contains one of these substrings
	// or one of genericTriggers.
	triggers []string
	// not rejects matches that look like assertions but check nothing,
	// such as NUnit's Assert.Pass() or Assert.Inconclusive().
	not *regexp.Regexp
}

func res(exprs ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(exprs))
	for i, e := range exprs {
		out[i] = regexp.MustCompile(e)
	}
	return out
}

var (
	// genericAny applies to every language: assert*/expect* calls (incl.
	// Rust macros and Ruby/Swift block forms), assertion methods on
	// self/this, mock/response assertion methods, expect.<x>(...).
	genericAny = res(
		`(?:^|[^\w$.>:@#])((?:assert|expect)(?:[A-Z_]\w*)?\s*(?:<[^<>()]*>)?\s*!?\s*[({])`,
		`(?:\bself|\bthis|\$this|\bstatic|\bparent|\bcls|\bsuper)\s*(?:\.|->|::)\s*((?:assert|expect|verify)\w*\s*\()`,
		`(?:\.|->)\s*(assert\w*\s*\()`,
		// Jest/Vitest/Playwright expect.soft(x).toBe(1), expect.poll(fn),
		// expect.assertions(2). Asymmetric matchers such as
		// expect.objectContaining({...}) are arguments of an assertion,
		// not assertions of their own.
		`\b(expect\s*\.\s*(?:soft|poll|element|assertions|hasAssertions|fail|unreachable)\s*\()`,
	)
	// genericStmt: helpers such as verifyUser(u), checkInvariants(t),
	// shouldRender(x), mustMatch(t, ...), ensureNoLeaks().
	genericStmt = res(
		`(?:^|[^\w$.>:@#])((?:verify|check|should|must|ensure)(?:[A-Z_]\w*)?\s*(?:<[^<>()]*>)?\s*!?\s*[({])`,
	)
)

var jsAsserts = assertSet{triggers: []string{"t."}, any: res(
	`(?:^|[^\w$.])(assert(?:\s*\.\s*\w+)+\s*\()`, // node:assert, chai assert.*
	`\.\s*(should)\b`,               // chai should, Cypress .should(...)
	`\.\s*(expect\s*\()`,            // supertest .expect(200)
	`\.\s*(assert\s*\.\s*\w+\s*\()`, // sinon.assert.x, t.assert.x
	`(?:^|[^\w$.])(t\s*\.\s*(?:is|not|deepEqual|notDeepEqual|like|true|false|truthy|falsy|ok|notOk|equal|equals|notEqual|same|notSame|strictSame|strictEqual|notStrictEqual|match|notMatch|regex|throws|throwsAsync|notThrows|notThrowsAsync|rejects|resolves|snapshot|error|ifError)\s*\()`, // AVA, tap
)}

var assertSets = map[lang.Lang]assertSet{
	lang.JavaScript: jsAsserts,
	lang.TypeScript: jsAsserts,
	lang.Python: {
		triggers: []string{"raises", "warns", "deprecated_call"},
		any: res(
			`\b(pytest\s*\.\s*(?:raises|warns|deprecated_call)\s*\()`,
			`\b(check\s*\.\s*\w+\s*\()`, // pytest-check
		),
		stmt: res(`(?:^|[^\w.])(assert)\b`),
	},
	lang.Go: {
		triggers: []string{"Error", "Fatal", "Fail", "require", "Require", "Assert", "Expect", "Eventually", "Consistently", "Ω", "Check", "Verify", "is.", "s."},
		any: res(
			`(?:\b(?:t|tb|b|f|tt)|\bT\s*\(\s*\))\s*\.\s*((?:Errorf?|Fatalf?|Fail|FailNow)\s*\()`,
			`\b((?:assert|require)\s*\.\s*\w+\s*\()`,              // testify
			`\.\s*((?:Require|Assert)\s*\(\s*\)\s*\.\s*\w+\s*\()`, // suite.Require().Equal
			`\b(?:s|suite)\s*\.\s*((?:Equal|NotEqual|EqualValues|Exactly|True|False|Nil|NotNil|NoError|Error|ErrorIs|ErrorAs|ErrorContains|EqualError|Len|Empty|NotEmpty|Contains|NotContains|ElementsMatch|Subset|Panics|NotPanics|Greater|GreaterOrEqual|Less|LessOrEqual|Zero|NotZero|JSONEq|Eventually|InDelta|IsType|Implements|Same|Regexp|FileExists)\s*\()`,
			`(?:^|[^\w.])((?:g\s*\.\s*)?(?:Expect|Eventually|Consistently|Ω)\s*\()`, // gomega
			`\b\w+\s*\.\s*((?:Assert|Check)\s*\()`,                                  // gocheck, quicktest
			`\bis\s*\.\s*((?:Equal|True|NoErr|Fail)\s*\()`,                          // matryer/is
		),
		stmt: res(`(?:^|[^\w])((?:\w+\.)?(?:Assert|Expect|Check|Verify|Require)[A-Z]\w*\s*\()`),
	},
	lang.Rust: {any: res(`\b((?:debug_|prop_)?assert\w*\s*!)`)},
	lang.Java: {
		triggers: []string{"Assert"},
		any: res(
			`\b((?:Assert|Assertions)\s*\.\s*\w+\s*\()`,
			`\.\s*(should\s*\()`, // BDDMockito then(x).should()
		),
		stmt: res(`(?:^|[^\w.])(assert)\s+[^\s=(]`),
	},
	lang.Kotlin: {triggers: []string{"Assert", "erif"}, any: res(
		`\b((?:Assert|Assertions)\s*\.\s*\w+\s*\()`,
		`\b(should[A-Z]\w*)\b`,                                              // Kotest shouldBe / shouldThrow<..> { }
		`\b((?:co)?[vV]erify(?:All|Order|Sequence)?\s*(?:\([^)]*\)\s*)?\{)`, // MockK verify { }
		`\b(confirmVerified\s*\()`,
	)},
	lang.Scala: {triggers: []string{"intercept"}, any: res(
		`\b(should[A-Z]\w*|must(?:Be|Equal|[A-Z]\w*))\b`,
		`\b(should\s+(?:be|equal|have|contain|not|matchPattern|startWith|endWith|include)\b)`,
		`\b((?:assert\w*|intercept)\s*\[)`,
	)},
	lang.CSharp: {triggers: []string{"Assert", "Should", "Verify", "Received", "DidNotReceive"},
		// Pass/Ignore/Inconclusive end the test without failing it, Warn
		// does not fail it, and Multiple only groups other assertions.
		not: regexp.MustCompile(`^(?:Classic)?Assert\s*\.\s*(?:Pass|Ignore|Inconclusive|Warn|Multiple|MultipleAsync)\s*\(`),
		any: res(
			`\b((?:Assert|CollectionAssert|StringAssert|ClassicAssert|FileAssert|DirectoryAssert)\s*\.\s*\w+\s*[<(])`,
			`\.\s*(Should(?:Not)?[A-Z]?\w*\s*[<(])`,                                      // FluentAssertions, Shouldly
			`\.\s*((?:Verify|VerifyAll|VerifyNoOtherCalls|Received|DidNotReceive)\s*\()`, // Moq, NSubstitute
			`(?:^|[^\w.])(Verify\s*\()`,                                                  // Verify snapshots
		)},
	lang.PHP: {any: res(
		`->\s*((?:expects|shouldReceive|shouldHaveReceived|shouldNotReceive|shouldNotHaveReceived)\s*\()`,
	)},
	lang.Ruby: {
		triggers: []string{"refute", "wont_"},
		any: res(
			`\b(is_expected)\b`,
			`(?:^|[^\w.:@$])(expect\s+do)\b`, // expect do ... end.to raise_error(...)
			`\.\s*(should(?:_not)?)\b`,
			`\.\s*((?:must|wont)_\w+)`,
		),
		stmt: res(
			`(?:^|[^\w.:@$])((?:assert|refute)(?:_\w+)?[?!]?)(?:\s|\(|$)`,
			`(?:^|[^\w.:@$])(should(?:_not)?)\b`,
		),
	},
	lang.Elixir: {triggers: []string{"refute"}, stmt: res(`(?:^|[^\w.:@])((?:assert|refute)(?:_\w+)?)\b`)},
	lang.Swift: {triggers: []string{"XCT", "require"}, any: res(
		`\b(XCT(?:Assert\w*|Fail|Unwrap)\s*\()`,
		`(#(?:expect|require)\s*\()`,
	)},
	lang.C:   cAsserts,
	lang.Cpp: cAsserts,
}

var cAsserts = assertSet{triggers: []string{"EXPECT_", "ASSERT_", "REQUIRE", "CHECK"}, any: res(
	`\b((?:EXPECT|ASSERT)_[A-Z_]+\s*\()`,      // GoogleTest
	`\b((?:REQUIRE|CHECK)(?:_[A-Z_]+)?\s*\()`, // Catch2, doctest
	`\b(static_assert\s*\()`,
)}

// attrAssert matches attributes that are assertions themselves: the test
// fails unless the code panics/throws.
var attrAssert = map[lang.Lang]*regexp.Regexp{
	lang.Rust:   regexp.MustCompile(`#\[\s*should_panic\b`),
	lang.Java:   regexp.MustCompile(`@(?:[\w.]+\.)?Test\s*\([^)]*\bexpected\s*=`),
	lang.Kotlin: regexp.MustCompile(`@(?:[\w.]+\.)?Test\s*\([^)]*\bexpected\s*=`),
	lang.CSharp: regexp.MustCompile(`\bExpectedException\b`),
}
