package tests

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// Helpers for JavaScript/TypeScript test declarations, shared by skip-added,
// focus-added and retry-added. They read the forms that a plain
// `it.skip(` / `{ retry: 3 }` search would miss or over-match:
//
//   - bracket access to a modifier: it['skip'](...), describe["only"](...);
//   - a modifier on a continuation line: `it` then `.only(...)` below it;
//   - options passed to the declaration itself, as opposed to options that
//     the test body passes to the code under test.

// jsBracketRe matches bracket access to a test modifier. In value text the
// modifier name survives encoding, because it only has word characters.
var jsBracketRe = regexp.MustCompile("\\s*\\[\\s*[\"'`](skip|only|todo|fixme|skipIf|runIf|failing|concurrent|serial|sequential|each|describe)[\"'`]\\s*\\]")

// jsChainEndRe matches a line that ends with a bare test function (or a
// chain of its modifiers), so that the next line can continue the chain.
var jsChainEndRe = regexp.MustCompile(`(?:^|[^\w$.])((?:it|test|describe|suite|context|specify|bench|fixture)(?:\.\w+)*)\s*$`)

// jsContinuationRe matches a line that continues a chain with a modifier.
var jsContinuationRe = regexp.MustCompile(`^\s*\.\s*(?:skip|only|todo|fixme|skipIf|runIf|failing|concurrent|serial|sequential|each|describe)\b`)

// jsScanText returns the value text that the JavaScript detectors match:
// bracket access is rewritten as member access (it['skip'] → it.skip) and a
// continuation line is joined to the chain it continues (`it` / `.only(` →
// `it.only(`). Positions in the result are positions in this text, so
// every caller that needs them must use jsScanText too.
func jsScanText(f *core.FileInfo, l core.Line, v version, vt string) string {
	if strings.Contains(vt, "[") {
		vt = jsBracketRe.ReplaceAllString(vt, ".$1")
	}
	if jsContinuationRe.MatchString(vt) {
		if prev, ok := prevCode(f, l.No, v); ok {
			if m := jsChainEndRe.FindStringSubmatch(prev); m != nil {
				vt = m[1] + strings.TrimSpace(vt)
			}
		}
	}
	return vt
}

// jsDeclName names the test declared on new line l as jsScanText reads it:
// it['only']('name', ...) or a `.only('name', ...)` continuation line.
func jsDeclName(f *core.FileInfo, l core.Line) testName {
	vt, _ := valueText(l, f.Lang)
	st := jsScanText(f, l, newVersion, vt)
	if st == vt {
		return testName{}
	}
	return declName(lang.LexedLine{Code: st, NoComment: display(st)})
}

// jsLayoutName names the test declared around new line n when Prettier put
// the declaration's arguments on their own lines: `fit(` followed by
// `'name',`, or an options line below `test(` and `'name',`.
func jsLayoutName(f *core.FileInfo, n int) testName {
	first := 0 // the line holding the first argument
	if l, ok := f.NewLine(n); ok && strings.HasSuffix(strings.TrimSpace(l.Code), "(") {
		first = n + 1
	} else {
		for k := n - 1; k >= 1 && k >= n-5; k-- {
			prev, ok := f.NewLine(k)
			if !ok {
				return testName{}
			}
			code := strings.TrimSpace(prev.Code)
			if strings.HasSuffix(code, ",") {
				continue
			}
			if ms := jsDeclRe.FindAllStringIndex(code, -1); len(ms) > 0 && ms[len(ms)-1][1] == len(code) {
				first = k + 1
			}
			break
		}
	}
	if first == 0 {
		return testName{}
	}
	l, ok := f.NewLine(first)
	if !ok || len(l.Strings) == 0 || !strings.HasPrefix(strings.TrimSpace(l.Code), `''`) &&
		!strings.HasPrefix(strings.TrimSpace(l.Code), `""`) && !strings.HasPrefix(strings.TrimSpace(l.Code), "``") {
		return testName{}
	}
	return testName{name: l.Strings[0], quoted: true}
}

// jsxTextRe matches a JSX opening tag followed by plain text up to the end
// of the string: <p>Never commit it.only(...)</p> is text that a test
// renders, not a call. The tag name must touch its "<" and the text may not
// contain braces, angle brackets, '=' or ';', so a comparison such as
// `a < b && c > d ? it : it.skip` stays code.
var jsxTextRe = regexp.MustCompile(`<[A-Za-z][\w.:-]*(?:\s[^<>{}]*)?>[^<>{}=;]*$`)

// inJSXText reports whether vt[pos:] starts inside JSX text.
func inJSXText(vt string, pos int) bool {
	return strings.Contains(vt[:pos], ">") && jsxTextRe.MatchString(vt[:pos])
}

// prevCode returns the code of the nearest non-blank line above line n in
// version v of f.
func prevCode(f *core.FileInfo, n int, v version) (string, bool) {
	for k := n - 1; k >= 1 && k >= n-3; k-- {
		l, ok := v.line(f, k)
		if !ok {
			return "", false
		}
		if c := strings.TrimSpace(l.Code); c != "" {
			return c, true
		}
	}
	return "", false
}

// jsOption is one key of an options object given to a test declaration:
// it('name', { retries: 2 }, fn), test('name', fn, { skip: true }),
// Deno.test({ name, ignore: true, fn }).
type jsOption struct {
	key   string
	value expr
	// closes is set when the options follow the test's callback on the line
	// that closes it (`}, { retry: 3 });`): the test is declared above.
	closes bool
}

var (
	// jsClosingOptsRe matches the line that closes a declaration's callback
	// and passes options after it: `}, { retry: 3 });` (Vitest, Bun).
	jsClosingOptsRe = regexp.MustCompile(`^\s*\}\s*,\s*(\{.*)$`)
	// jsOptionLineRe matches one `key: value,` line of a multi-line object.
	jsOptionLineRe = regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*:\s*(.*?)\s*,?\s*$`)
	// jsOptsOpenRe matches the end of a declaration line that opens an
	// options object: `test('name', {` or `Deno.test({`.
	jsOptsOpenRe = regexp.MustCompile(`(?:,|\()\s*\{\s*$`)
)

// jsDeclOptions returns the options given to the test declarations on line
// l (value text vt, version v of f). Only objects that are arguments of the
// declaration call count: an object in the test body, such as
// `parse(csv, { skip: true })` in a one-line test, configures the code under
// test, not the test. Two multi-line layouts are recognized as well: a line
// that closes the callback and passes options (`}, { retry: 3 });`) and a
// `key: value,` line of an options object opened on the declaration line.
func jsDeclOptions(f *core.FileInfo, l core.Line, v version, vt string) []jsOption {
	var out []jsOption
	for _, m := range jsDeclRe.FindAllStringIndex(vt, -1) {
		for _, a := range looseArgs(vt, m[1]-1) {
			out = append(out, objectKeys(a.raw)...)
		}
	}
	if len(out) > 0 {
		return out
	}
	if m := jsClosingOptsRe.FindStringSubmatch(vt); m != nil {
		if t := testAboveIn(v.getter(f), l.No, true); !t.ok() {
			return nil
		}
		opts := objectKeys(m[1])
		for i := range opts {
			opts[i].closes = true
		}
		return opts
	}
	if t := strings.TrimSpace(vt); strings.HasPrefix(t, "{") && declArgLine(f, v, l.No) {
		// test(\n  'name',\n  { skip: 'flaky', timeout: 5_000 },\n  async () => {
		return objectKeys(t)
	}
	m := jsOptionLineRe.FindStringSubmatch(vt)
	if m == nil {
		return nil
	}
	// Walk up over the other keys of the object to the line that opens it.
	ind := indent(l.Raw)
	for k := l.No - 1; k >= 1 && k >= l.No-8; k-- {
		prev, ok := v.line(f, k)
		if !ok {
			return nil
		}
		code := strings.TrimSpace(prev.Code)
		if code == "" {
			continue
		}
		if indent(prev.Raw) >= ind {
			if !jsOptionLineRe.MatchString(code) {
				return nil // a nested object or a statement: not a flat options object
			}
			continue
		}
		if code == "{" && declArgLine(f, v, k) {
			return []jsOption{{key: m[1], value: newExpr(m[2])}} // the object opens on its own line
		}
		pvt, _ := valueText(core.Line{LexedLine: prev}, f.Lang)
		decl := jsDeclRe.FindAllStringIndex(pvt, -1)
		if len(decl) == 0 || !jsOptsOpenRe.MatchString(pvt) {
			return nil
		}
		rest := pvt[decl[len(decl)-1][1]:]
		if strings.Contains(rest, "=>") || strings.Contains(rest, "function") {
			return nil // the object is inside the test body
		}
		return []jsOption{{key: m[1], value: newExpr(m[2])}}
	}
	return nil
}

// declArgLine reports whether line n (version v of f) is one of the
// arguments of a test declaration whose arguments are laid out one per line:
// the lines above it are arguments (ending in ",") up to a line that ends
// with the declaration's "(": `test(`, `it.each(cases)(`.
func declArgLine(f *core.FileInfo, v version, n int) bool {
	for k := n - 1; k >= 1 && k >= n-5; k-- {
		prev, ok := v.line(f, k)
		if !ok {
			return false
		}
		code := strings.TrimSpace(prev.Code)
		switch {
		case code == "":
			continue
		case strings.HasSuffix(code, ","):
			continue // an earlier argument: 'name',
		case strings.HasSuffix(code, "("):
			ms := jsDeclRe.FindAllStringIndex(code, -1)
			return len(ms) > 0 && ms[len(ms)-1][1] == len(code)
		}
		return false
	}
	return false
}

// looseArgs returns the arguments of the call whose "(" is at vt[open]. A
// call that continues on the next line yields the arguments on this line.
func looseArgs(vt string, open int) []expr {
	if as, _, ok := args(vt, open); ok {
		return as
	}
	var out []expr
	for _, a := range splitTop(vt[open+1:], ',') {
		if e := newExpr(a); e.raw != "" {
			out = append(out, e)
		}
	}
	return out
}

// objectKeys returns the top-level keys of an object literal (which may be
// cut off at the end of the line): "{ retry: 3, skip }" → retry=3, skip=skip.
func objectKeys(s string) []jsOption {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil
	}
	inner := s[1:]
	if end, ok := closing(s, 0); ok {
		inner = s[1:end]
	}
	var out []jsOption
	for _, part := range splitTop(inner, ',') {
		e := newExpr(part)
		if name, val, ok := keyword(e); ok {
			out = append(out, jsOption{key: name, value: val})
		} else if isIdent(e.raw) {
			out = append(out, jsOption{key: e.raw, value: e}) // shorthand { skip }
		}
	}
	return out
}
