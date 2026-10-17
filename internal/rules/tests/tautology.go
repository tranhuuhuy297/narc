package tests

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var tautology = &core.Rule{
	ID:       "tautology",
	Title:    "Assertion that cannot fail",
	Category: "tests",
	Severity: core.Error,
	Summary:  "A new assertion can never fail: expect(true).toBe(true), assert True, assert_eq!(x, x), Assert.Pass().",
	Hint: "An assertion that cannot fail verifies nothing. Assert on the real result of the code under test " +
		"(e.g. expect(total).toBe(42)) or remove the assertion — don't add one just so the test \"has\" an assertion.",
	Docs: "Agents sometimes satisfy \"this test must assert something\" with an assertion that holds no matter " +
		"what the code does: a literal compared with itself, a value compared with itself, a constant that is always " +
		"truthy, or an explicit pass.\n\n" +
		"narc flags new assertions whose outcome is fixed: `expect(true).toBe(true)`, `expect(x).toEqual(x)`, " +
		"`expect(\"ok\").toBeDefined()`, `assert True`, `assert x == x`, `assert (cond, \"msg\")` (a tuple is always " +
		"truthy), `self.assertEqual(1, 1)`, `assert.ok(true)`, `assert!(true)`, `assert_eq!(x, x)`, `assertTrue(true)`, " +
		"`Assert.True(true)`, `Assert.Pass()`, `assert.True(t, true)`, `$this->assertTrue(true)`, `XCTAssertTrue(true)`, " +
		"`expect(true).to eq(true)` and the like. Comparing a call with itself (`expect(hash(a)).toBe(hash(a))`) is a " +
		"legitimate determinism check and is not flagged.\n\n" +
		"```py\n# bad\ndef test_refund():\n    refund(order)\n    assert True\n\n# good\ndef test_refund():\n    refund(order)\n    assert order.status == \"refunded\"\n```",
}

func init() {
	tautology.Check = checkTautology
	core.Register(tautology)
}

type tautologyHit struct {
	a   assertion
	why string
}

func checkTautology(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, o := range introduced(testFiles(a), detectTautologies, cancelling[tautologyHit]{}) {
		msg := fmt.Sprintf("%s can never fail: %s", o.Val.a, o.Val.why)
		if name := testAround(o.File, o.Line.No); name.ok() {
			msg += fmt.Sprintf(" (in %s)", name)
		}
		out = append(out, tautology.Finding(o.File, o.Line.No, msg))
	}
	return out
}

func detectTautologies(f *core.FileInfo, l core.Line, v version) []occurrence[tautologyHit] {
	l = chained(f, l, v)
	_, litOK := valueText(l, f.Lang)
	var out []occurrence[tautologyHit]
	for _, a := range parseAssertions(f.Lang, l) {
		why := cannotFail(a, f.Lang, litOK)
		if why == "" {
			continue
		}
		if strings.HasSuffix(why, "with itself") && (reflexivityTest(v.getter(f), l.No) || identityCheck(a, f.Lang)) {
			continue // `assert money == money` in a test of __eq__'s reflexivity, or of memoization
		}
		if (a.fn == "pass" || a.fn == "SUCCEED") && !inTestBody(v.getter(f), l) {
			// t.pass() or SUCCEED() inside a callback or branch checks that the
			// code got there; only NUnit's Assert.Pass() ends the test.
			continue
		}
		out = append(out, occurrence[tautologyHit]{File: f, Line: l, Kind: rulekit.Normalize(a.text), Val: tautologyHit{a, why}})
	}
	return out
}

// inTestBody reports whether line l sits directly in the body of a test,
// rather than in a callback, branch or loop nested inside it.
func inTestBody(get lineGetter, l core.Line) bool {
	ind := indent(l.Raw)
	for k := l.No - 1; k >= 1 && k >= l.No-120; k-- {
		prev, ok := get(k)
		if !ok {
			return false
		}
		if punctuationOnly(prev.Code) || indent(prev.Raw) >= ind {
			continue
		}
		t := declName(prev)
		return t.ok() && !t.suite
	}
	return false
}

// reflexivityRe matches names of tests where comparing a value with itself
// is the point: equality is reflexive, or a memoized, cached or singleton
// value comes back as the same object ("memoises the executable").
var reflexivityRe = regexp.MustCompile(`(?i)reflexiv|equals?_?(?:to_?)?itself|equal_?to_?self|self_?equal|is_?equal_?to_?self|memoi[sz]|cach|singleton|same_?(?:instance|object|reference)|identity|idempot`)

// identityCheck reports whether a is an identity assertion on a Ruby
// method call: `expect(f.python3).to equal(f.python3)` calls python3 twice
// and checks that the result is memoized, which can fail.
func identityCheck(a assertion, lg lang.Lang) bool {
	if lg != lang.Ruby || a.fn != "equal" && a.fn != "be" && a.fn != "equal?" || len(a.values) == 0 {
		return false
	}
	return strings.Contains(a.values[0].raw, ".")
}

// reflexivityTest reports whether the line n is in such a test.
func reflexivityTest(get lineGetter, n int) bool {
	t := testAboveIn(get, n, false)
	return t.ok() && reflexivityRe.MatchString(strings.ReplaceAll(t.name, " ", "_"))
}

// cannotFail explains why an assertion always passes, or returns "".
func cannotFail(a assertion, lg lang.Lang, litOK bool) string {
	if a.async || len(a.values) == 0 && a.pass != passAlways {
		return ""
	}
	if (a.pass == passEqual || a.pass == passNotEqual) && len(a.values) < 2 {
		return ""
	}
	if !litOK {
		// String literal contents are unknown; never compare them.
		for _, v := range a.values {
			if strings.ContainsAny(v.raw, "\"'`") {
				return ""
			}
		}
	}
	switch a.pass {
	case passAlways:
		return "it passes unconditionally"
	case passTruthy:
		return alwaysTrue(a.values[0], lg)
	case passFalsy:
		return alwaysFalse(a.values[0], lg)
	case passEqual:
		if why := equalWhy(a.values[0], a.values[1], lg); why != "" {
			return why
		}
		// expect(x === x).toBe(true): the condition itself cannot be false.
		for i, v := range a.values[:2] {
			if l := lit(v, lg); l.kind == litBool {
				if l.value == "true" {
					return alwaysTrue(a.values[1-i], lg)
				}
				return alwaysFalse(a.values[1-i], lg)
			}
		}
	case passNotEqual:
		if differentLiterals(a.values[0], a.values[1], lg) {
			return fmt.Sprintf("the literals %s and %s always differ", a.values[0], a.values[1])
		}
	case passNotNull, passDefined:
		v := a.values[0]
		if l := lit(v, lg); l.kind != notLiteral && l.kind != litNull {
			return fmt.Sprintf("the literal %s is never null", v)
		}
		if a.pass == passDefined && v.raw == "null" {
			return "null is defined (only undefined fails toBeDefined)"
		}
		if strings.HasPrefix(v.raw, "new ") && strings.HasSuffix(v.raw, ")") && lg != lang.Unknown {
			return fmt.Sprintf("a newly constructed object (%s) is never null", v)
		}
	case passNull:
		v := a.values[0]
		if lit(v, lg).kind != litNull {
			return ""
		}
		// JS tells null and undefined apart: expect(null).toBeUndefined() fails.
		switch a.fn {
		case "toBeUndefined", "undefined", "isUndefined":
			if v.raw != "undefined" {
				return ""
			}
		case "toBeNull", "null", "isNull":
			if lg.IsJSLike() && v.raw != "null" {
				return ""
			}
		}
		return fmt.Sprintf("it checks that %s is %s", v, v)
	}
	return ""
}

func equalWhy(x, y expr, lg lang.Lang) string {
	ok, how := sameValue(x, y, lg)
	switch {
	case !ok:
		return ""
	case how == "itself" && !isLiteral(x):
		return fmt.Sprintf("it compares %s with itself", x)
	default:
		return fmt.Sprintf("it compares the literal %s with an equal literal", x)
	}
}

// alwaysTrue explains why a condition always holds, or returns "".
func alwaysTrue(e expr, lg lang.Lang) string {
	if l := lit(e, lg); l.kind != notLiteral {
		switch {
		case !l.known || !l.truthy:
			return ""
		case l.kind == litCollection && lg == lang.Python && strings.HasPrefix(e.raw, "("):
			return "a non-empty tuple is always truthy — `assert (cond, msg)` asserts the tuple, not cond"
		case l.kind == litBool:
			return fmt.Sprintf("%s is always true", e)
		default:
			return fmt.Sprintf("%s is always truthy", e)
		}
	}
	if lhs, op, rhs, ok := splitCompare(e.raw); ok {
		return comparisonWhy(newExpr(lhs), op, newExpr(rhs), lg)
	}
	if m := equalsCallRe.FindStringSubmatch(e.raw); m != nil {
		x, y := newExpr(m[1]), newExpr(m[2])
		if !isLiteral(x) {
			// x.equals(x) calls a user-defined equals(): equality-contract tests
			// check that it is reflexive, and it can fail.
			return ""
		}
		return equalWhy(x, y, lg)
	}
	for _, or := range []string{"||", " or "} {
		parts := splitOp(e.raw, or)
		if len(parts) > 1 {
			last := newExpr(parts[len(parts)-1])
			if l := lit(last, lg); l.kind != notLiteral && l.known && l.truthy {
				return fmt.Sprintf("`%s %s` makes the condition always true", strings.TrimSpace(or), last)
			}
		}
	}
	for _, not := range []string{"!", "not "} {
		if rest, ok := strings.CutPrefix(e.raw, not); ok {
			if why := alwaysFalse(newExpr(rest), lg); why != "" {
				return why
			}
		}
	}
	return ""
}

// alwaysFalse explains why a condition never holds, or returns "".
func alwaysFalse(e expr, lg lang.Lang) string {
	if l := lit(e, lg); l.kind != notLiteral {
		if l.known && !l.truthy {
			if l.kind == litBool {
				return fmt.Sprintf("%s is always false", e)
			}
			return fmt.Sprintf("%s is always falsy", e)
		}
		return ""
	}
	for _, not := range []string{"!", "not "} {
		if rest, ok := strings.CutPrefix(e.raw, not); ok {
			if why := alwaysTrue(newExpr(rest), lg); why != "" {
				return why
			}
		}
	}
	return ""
}

// comparisonWhy explains why `lhs op rhs` always holds, or returns "".
func comparisonWhy(lhs expr, op string, rhs expr, lg lang.Lang) string {
	switch op {
	case "==", "===", "is", "eq", "<=", ">=":
		if why := equalWhy(lhs, rhs, lg); why != "" {
			return why
		}
	case "!=", "!==", "is not":
		if differentLiterals(lhs, rhs, lg) {
			return fmt.Sprintf("the literals %s and %s always differ", lhs, rhs)
		}
		return ""
	}
	ll, rl := lit(lhs, lg), lit(rhs, lg)
	if ll.kind != litNumber || rl.kind != litNumber {
		return ""
	}
	holds := false
	switch op {
	case "<":
		holds = ll.num < rl.num
	case ">":
		holds = ll.num > rl.num
	case "<=":
		holds = ll.num <= rl.num
	case ">=":
		holds = ll.num >= rl.num
	}
	if holds {
		return fmt.Sprintf("%s %s %s compares two constants", lhs, op, rhs)
	}
	return ""
}

// splitOp splits s at a top-level binary operator.
func splitOp(s, op string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], op) {
			out = append(out, s[start:i])
			start = i + len(op)
			i += len(op) - 1
		}
	}
	return append(out, s[start:])
}
