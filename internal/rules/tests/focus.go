package tests

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
)

var focusAdded = &core.Rule{
	ID:       "focus-added",
	Title:    "Test focused",
	Category: "tests",
	Severity: core.Error,
	Summary:  "A test or suite was focused (.only, fit, fdescribe), which silently skips every other test in the run.",
	Hint: "Remove the focus marker (.only / fit / fdescribe / focus: true). A focused test makes the runner skip the rest " +
		"of the suite, so a green run no longer means everything passes. To run one test, filter from the command line " +
		"instead (jest -t \"name\", vitest -t \"name\", rspec file:line, playwright test -g \"name\").",
	Docs: "Focus markers are meant for a local debugging session. Committed, they turn the whole suite into one " +
		"test: everything else is reported as skipped (or not at all), and CI goes green.\n\n" +
		"narc flags focus markers that the change introduced: `it.only`, `test.only`, `describe.only`, " +
		"`test.describe.only`, `context.only`, Jasmine/Jest `fit`/`fdescribe`, RSpec `fit`/`fdescribe`/`focus`/" +
		"`focus: true`, minitest-focus `focus`, node:test and Deno `only: true`, Dart `solo: true`, " +
		"Pest `->only()`, MUnit `.only`, `@pytest.mark.only`, `QUnit.only`, Spock `@IgnoreRest` and kotest's " +
		"`f:` name prefix.\n\n" +
		"```ts\n// bad\nit.only('creates an invoice', ...)\n// good\nit('creates an invoice', ...)   // and run: npx vitest -t 'creates an invoice'\n```",
}

func init() {
	focusAdded.Check = checkFocus
	core.Register(focusAdded)
}

func checkFocus(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, o := range introduced(testFiles(a), detectFocus, cancelling[string]{scope: focusScope}) {
		name := declName(o.Line.LexedLine)
		if !name.ok() && o.File.Lang.IsJSLike() {
			name = jsDeclName(o.File, o.Line)
		}
		switch {
		case name.ok():
		case strings.HasPrefix(o.Val, "@") || o.Val == "minitest-focus":
			name = testAfter(o.File, o.Line.No) // an annotation, or minitest-focus above the test
		default:
			name = testClosedBy(o.File, o.Line.No) // }, solo: true);  })->only();
		}
		if !name.ok() && o.File.Lang.IsJSLike() {
			name = jsLayoutName(o.File, o.Line.No) // fit(\n  'name',\n  async () => {
		}
		if !name.ok() {
			if n, ok := kotestPrefixed(o.File, o.Line, "f:"); ok {
				name = testName{name: n, quoted: true} // kotest StringSpec: "f:name" {
			}
		}
		var msg string
		if name.ok() {
			msg = fmt.Sprintf("%s is focused with %s — every other test in the run is silently skipped", named(unitNoun(o.Val, o.Line), name), o.Val)
		} else {
			msg = fmt.Sprintf("new focus marker %s — every other test in the run is silently skipped", o.Val)
		}
		out = append(out, focusAdded.Finding(o.File, o.Line.No, msg))
	}
	return out
}

// focusScope is the scopeFunc of focus markers: replacing describe.only
// with an it.only inside it narrows the focus, and adds none.
func focusScope(o occurrence[string], v version) []string {
	get := v.getter(o.File)
	own := declName(o.Line.LexedLine)
	if !own.ok() && (strings.HasPrefix(o.Val, "@") || o.Val == "minitest-focus") {
		own = testAfterIn(get, o.Line.No)
	}
	return markerScope(get, o.Line.No, own)
}

var (
	jsOnlyRe = regexp.MustCompile(`\b(?:it|test|describe|suite|context|specify|bench|fixture|tap|Deno\.test|QUnit(?:\s*\.\s*module)?)(?:\s*\.\s*(?:describe|serial|parallel|concurrent|sequential|each))*\s*\.\s*only\b`)
	// jsFocusFnRe: fit('name', ...), or fit( with the name on the next line.
	jsFocusFnRe = regexp.MustCompile("(?:^\\s*|[;{]\\s*)(fit|fdescribe|fcontext|ftest|fspecify)\\s*\\(\\s*(?:[\"'`]|$)")
	rbFocusFnRe = regexp.MustCompile(`^\s*(fit|focus|fdescribe|fcontext|fspecify|fexample|fscenario|ffeature)\b\s*(?:\(|["'])`)
	// rbMinitestFocusRe matches minitest-focus: `focus` on its own line (or
	// before the declaration) runs only the next test.
	rbMinitestFocusRe = regexp.MustCompile(`^\s*focus(?:\s+(?:def\s+test_\w*|it|test|specify)\b|\s*$)`)
	rbTestDeclRe      = regexp.MustCompile(`^\s*(?:def\s+test_\w*|it|test|specify)\b`)
	rbFocusMetaRe     = regexp.MustCompile(`,\s*(?::focus\b|focus:\s*true\b)|\(\s*(?::focus\b|focus:\s*true\b)`)
	pestOnlyRe        = regexp.MustCompile(`->\s*only\s*\(\s*\)`)
	munitOnlyRe       = regexp.MustCompile(`"\s*\.only\b`)
	pyOnlyRe          = regexp.MustCompile(`@pytest\.mark\.only\b`)
	spockOnlyRe       = regexp.MustCompile(`@IgnoreRest\b`)
)

// detectFocus finds focus markers on a line of test code. The occurrence
// value is the marker as written.
func detectFocus(f *core.FileInfo, l core.Line, v version) []occurrence[string] {
	code := l.Code
	var found []string
	add := func(m string) { found = append(found, strings.Join(strings.Fields(m), " ")) }
	switch f.Lang {
	case lang.JavaScript, lang.TypeScript:
		// Value text, so that it['only'] and a `.only(` continuation line are
		// read as it.only.
		vt, _ := valueText(l, f.Lang)
		vt = jsScanText(f, l, v, vt)
		for _, m := range jsOnlyRe.FindAllStringIndex(vt, -1) {
			if !inJSXText(vt, m[0]) {
				add(compact(vt[m[0]:m[1]]))
			}
		}
		for _, m := range jsFocusFnRe.FindAllStringSubmatch(vt, -1) {
			add(m[1])
		}
		for _, o := range jsDeclOptions(f, l, v, vt) {
			if o.key == "only" && o.value.raw == "true" {
				add("only: true")
			}
		}
	case lang.Ruby:
		if m := rbFocusFnRe.FindStringSubmatch(code); m != nil {
			// `focus "name" do` declares an example only at group level;
			// inside an example, focus("#email") is a helper (Capybara).
			if m[1] != "focus" || !insideExample(f, l, v) {
				add(m[1])
			}
		} else if rbMinitestFocusRe.MatchString(code) && (strings.TrimSpace(code) != "focus" || nextIsRubyTest(f, l, v)) {
			add("minitest-focus")
		}
		if meta, ok := rbDeclArgs(code); ok {
			if m := rbFocusMetaRe.FindString(meta); m != "" {
				add(strings.TrimLeft(m, ",( "))
			}
		}
	case lang.Dart:
		vt, _ := valueText(l, f.Lang)
		if v, ok := dartTestOptions(vt)["solo"]; ok && v.raw == "true" {
			add("solo: true")
		}
	case lang.PHP:
		if pestOnlyRe.MatchString(code) {
			add("->only()")
		}
	case lang.Scala:
		if munitOnlyRe.MatchString(code) {
			add(".only")
		}
	case lang.Python:
		if pyOnlyRe.MatchString(code) {
			add("@pytest.mark.only")
		}
	case lang.Groovy:
		if spockOnlyRe.MatchString(code) {
			add("@IgnoreRest") // Spock: run only this feature
		}
	case lang.Kotlin:
		if _, ok := kotestPrefixed(f, l, "f:"); ok {
			add(`"f:" name prefix`) // kotest: run only this top-level test
		}
	}
	out := make([]occurrence[string], len(found))
	for i, m := range found {
		// fit and it.only (or fdescribe and describe.only) are the same marker:
		// eslint-plugin-jest's no-test-prefixes fix rewrites one into the other.
		out[i] = occurrence[string]{File: f, Line: l, Kind: "focus-" + unitNoun(m, l), Val: m}
	}
	return out
}

// nextIsRubyTest reports whether the next non-blank line after l (version
// v of f) declares a test, as a bare minitest-focus `focus` line requires.
func nextIsRubyTest(f *core.FileInfo, l core.Line, v version) bool {
	for k := l.No + 1; k <= l.No+3; k++ {
		next, ok := v.line(f, k)
		if !ok {
			return false
		}
		if strings.TrimSpace(next.Code) != "" {
			return rbTestDeclRe.MatchString(next.Code)
		}
	}
	return false
}

// insideExample reports whether line l (version v of f) is in the body of
// a test rather than directly in a suite.
func insideExample(f *core.FileInfo, l core.Line, v version) bool {
	t := testAboveIn(v.getter(f), l.No, false)
	return t.ok() && !t.suite
}
