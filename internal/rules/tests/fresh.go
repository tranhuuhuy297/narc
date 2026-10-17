package tests

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// Tests added by the change. A marker or assertion on a test that did not
// exist before the change takes nothing away that used to run: a new test
// added already skipped, guarded by an environment check, or holding a
// placeholder assertion is worth a look, but it is not a test that was
// switched off. These helpers tell the two apart.

// testAboveLine is testAboveIn that also returns the declaring line.
func testAboveLine(get lineGetter, n int, sameLevel bool) (testName, int) {
	cur, ok := get(n)
	if !ok {
		return testName{}, 0
	}
	ind := indent(cur.Raw)
	for k := n - 1; k >= 1 && k >= n-120; k-- {
		l, ok := get(k)
		if !ok {
			break
		}
		if punctuationOnly(l.Code) {
			continue
		}
		li := indent(l.Raw)
		if li > ind || li == ind && !sameLevel {
			continue
		}
		if t := declName(l); t.ok() {
			return t, k
		}
		if sameLevel {
			return testName{}, 0
		}
		ind = li
	}
	return testName{}, 0
}

// testAfterLine is testAfterIn that also returns the declaring line.
func testAfterLine(get lineGetter, n int) (testName, int) {
	for k := n; k <= n+10; k++ {
		l, ok := get(k)
		if !ok {
			break
		}
		if t := declName(l); t.ok() {
			return t, k
		}
	}
	return testName{}, 0
}

// markerTarget returns the test or suite that the skip marker o (on a new
// line) applies to and the new line that declares it; the line is 0 when it
// is not known.
func markerTarget(o occurrence[skipHit]) (testName, int) {
	get := newVersion.getter(o.File)
	h := o.Val
	switch h.form {
	case formDecl:
		if t := declName(o.Line.LexedLine); t.ok() {
			return t, o.Line.No
		}
		if h.name.ok() {
			return h.name, o.Line.No
		}
		if t, n := testAboveLine(get, o.Line.No, true); t.ok() {
			return t, n
		}
		if o.File.Lang.IsJSLike() {
			if t := jsLayoutName(o.File, o.Line.No); t.ok() {
				return t, o.Line.No
			}
			if t, n := jsNameBelow(o.File, o.Line.No); t.ok() {
				return t, n
			}
		}
	case formAttr:
		if h.name.ok() {
			return h.name, o.Line.No
		}
		return testAfterLine(get, o.Line.No)
	default:
		return testAboveLine(get, o.Line.No, false)
	}
	return h.name, 0
}

// jsNameBelow names the test whose declaration starts on new line n and
// continues below it, as Prettier lays out a long chain:
//
//	it
//	  .skipIf(process.platform === 'win32')
//	  .each(['ancestor', 'descendant'])(
//	  'rejects a %s workspace outside the bundle',
//
// The name is the first line below that holds only a string literal.
func jsNameBelow(f *core.FileInfo, n int) (testName, int) {
	l, ok := f.NewLine(n)
	if !ok {
		return testName{}, 0
	}
	if code := strings.TrimSpace(l.Code); !strings.HasSuffix(code, "(") && !strings.HasSuffix(code, ")") {
		return testName{}, 0
	}
	for k := n + 1; k <= n+6; k++ {
		next, ok := f.NewLine(k)
		if !ok {
			break
		}
		code := strings.TrimSpace(next.Code)
		if len(next.Strings) > 0 && (strings.HasPrefix(code, `''`) || strings.HasPrefix(code, `""`) || strings.HasPrefix(code, "``")) {
			return testName{name: next.Strings[0], quoted: true}, k
		}
		if !strings.HasPrefix(code, ".") && !strings.HasSuffix(code, "(") {
			break
		}
	}
	return testName{}, 0
}

// jsAliasRe matches the start of an alias for a skipping declaration:
// `const itPosix = ` in `const itPosix = it.skipIf(isWindows);`, or
// `const describeIfCreds = process.env.CREDS ? describe : ` before
// `describe.skip`.
var jsAliasRe = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:[^;]*[?:]\s*)?$`)

// aliasFresh reports, for a skip marker o that defines an alias
// (`const itPosix = it.skipIf(isWindows)`), whether every test declared with
// the alias was added by the change; ok is false when o is no alias.
func aliasFresh(files []*core.FileInfo, o occurrence[skipHit]) (fresh, ok bool) {
	f, h := o.File, o.Val
	if !f.Lang.IsJSLike() || h.form != formDecl {
		return false, false
	}
	vt, _ := scanText(f, o.Line, newVersion)
	if h.pos > len(vt) {
		return false, false
	}
	m := jsAliasRe.FindStringSubmatch(vt[:h.pos])
	if m == nil {
		return false, false
	}
	use := regexp.MustCompile(`(?:^|[^\w$.])` + regexp.QuoteMeta(m[1]) + `(?:\s*\.\s*\w+(?:\([^()]*\))?)*\s*\(`)
	get := newVersion.getter(f)
	for k := 1; k <= lastLine(f, newVersion); k++ {
		l, found := get(k)
		if !found || k == o.Line.No || !use.MatchString(l.Code) {
			continue
		}
		name, decl := testName{}, k
		if len(l.Strings) > 0 {
			name = testName{name: l.Strings[0], quoted: true}
		} else if t, n := jsNameBelow(f, k); t.ok() {
			name, decl = t, n
		}
		if !name.ok() || !freshTest(files, f, name, decl) {
			return false, true
		}
	}
	return true, true
}

// declaredIn reports whether a line among 1..last of the version that get
// reads declares a test or suite named name. A display name also counts
// when it is a string literal of its own: Prettier puts a long name on the
// line below `test(`.
func declaredIn(get lineGetter, last int, name testName) bool {
	if !name.ok() {
		return false
	}
	for k := 1; k <= last; k++ {
		l, ok := get(k)
		if !ok || !strings.Contains(l.NoComment, name.name) {
			continue
		}
		if declName(l).name == name.name {
			return true
		}
		if name.quoted {
			for _, s := range l.Strings {
				if s == name.name {
					return true
				}
			}
		}
	}
	return false
}

// addedLine reports whether new line n of f is one the change added.
func addedLine(f *core.FileInfo, n int) bool {
	for _, l := range f.Added {
		if l.No == n {
			return true
		}
		if l.No > n {
			break
		}
	}
	return false
}

// helperFunc reports whether name, declared on new line decl of f, is a
// helper function rather than a test: a Go function that is not a Test,
// Benchmark or Fuzz function, a Python function whose name does not start
// with test, a JavaScript function declaration, or a JVM or Rust function
// without a test annotation.
func helperFunc(f *core.FileInfo, name testName, decl int) bool {
	if !name.ok() || name.quoted || name.suite || decl == 0 {
		return false
	}
	switch {
	case f.Lang == lang.Go:
		return !goTestFuncRe.MatchString(name.name)
	case f.Lang == lang.Python:
		return !strings.HasPrefix(name.name, "test")
	case f.Lang.IsJSLike():
		return true
	case isJVM(f.Lang), f.Lang == lang.Rust, f.Lang == lang.CSharp:
		for k := decl; k >= 1 && k >= decl-6; k-- {
			l, ok := f.NewLine(k)
			if !ok {
				break
			}
			if k < decl && strings.TrimSpace(l.Code) != "" && !strings.HasPrefix(strings.TrimSpace(l.Code), "@") &&
				!strings.HasPrefix(strings.TrimSpace(l.Code), "#[") && !strings.HasPrefix(strings.TrimSpace(l.Code), "[") {
				break // the end of the previous declaration
			}
			if jvmTestMarkRe.MatchString(l.Code) || rustTestAttrRe.MatchString(l.Code) || csTestAttrRe.MatchString(l.Code) {
				return false
			}
		}
		return true
	}
	return false
}

var (
	goTestFuncRe   = regexp.MustCompile(`^(?:Test|Benchmark|Fuzz|Example)`)
	rustTestAttrRe = regexp.MustCompile(`#\[\s*(?:\w+::)*test\b|#\[\s*(?:rstest|test_case|quickcheck|proptest)\b`)
	csTestAttrRe   = regexp.MustCompile(`\[\s*(?:Fact|Theory|Test|TestMethod|TestCase|SkippableFact|SkippableTheory)\b`)
)

// freshTest reports whether the test or suite name, declared on new line
// decl of f, was added by the change: its declaration is an added line, no
// test of that name exists in the old version of f or was removed from
// another changed file (a test moved here), and the declaration does not
// replace that of a test that disappeared (a rename). A brand-new file
// holds only fresh tests, unless they were moved from another file.
func freshTest(files []*core.FileInfo, f *core.FileInfo, name testName, decl int) bool {
	if !name.ok() {
		return false
	}
	if decl > 0 && !addedLine(f, decl) {
		return false
	}
	if f.OldPath != "" && declaredIn(oldVersion.getter(f), lastLine(f, oldVersion), name) {
		return false
	}
	for _, g := range files {
		if g == f {
			continue
		}
		for _, r := range g.Removed {
			if strings.Contains(r.NoComment, name.name) && declName(r.LexedLine).name == name.name {
				return false
			}
		}
	}
	if decl > 0 {
		// A declaration edited in place, in the same replacement block as the
		// declaration of a test that is gone, is that test renamed.
		oldBlock, newBlock := blocks(f)
		if b := newBlock[decl]; b != 0 {
			for _, r := range f.Removed {
				if oldBlock[r.No] != b {
					continue
				}
				if t := declName(r.LexedLine); t.ok() && t.name != name.name &&
					!declaredIn(newVersion.getter(f), lastLine(f, newVersion), t) {
					return false
				}
			}
		}
	}
	return true
}

// forwardJoin returns line l of the version that get reads joined with the
// lines below it while it leaves a bracket open, as in a call whose
// arguments a formatter put on their own lines:
//
//	@pytest.mark.skip(          expect(result).toBe(
//	    reason="..."              true
//	)                           );
//
// At most max lines are joined; a statement that does not close within
// reach is returned as far as it was read.
func forwardJoin(get lineGetter, l core.Line, max int) core.Line {
	depth := bracketDepth(l.Code)
	if depth <= 0 {
		return l
	}
	parts := []lang.LexedLine{l.LexedLine}
	for k := l.No + 1; k <= l.No+max && depth > 0; k++ {
		next, ok := get(k)
		if !ok {
			break
		}
		parts = append(parts, next)
		depth += bracketDepth(next.Code)
	}
	if len(parts) == 1 {
		return l
	}
	bottomUp := make([]lang.LexedLine, len(parts))
	for i, p := range parts {
		bottomUp[len(parts)-1-i] = p
	}
	return joinLines(bottomUp, l)
}

// maxJoin is how many lines below a marker or assertion forwardJoin reads.
const maxJoin = 8

// fullSkip returns the skip hit o as read from its whole statement, when
// the marker's arguments continue on the lines below it:
// `@pytest.mark.skip(` / `reason="..."` / `)`. The statement's value text
// is returned too.
func fullSkip(o occurrence[skipHit], v version) (skipHit, string) {
	joined := forwardJoin(v.getter(o.File), o.Line, maxJoin)
	vt, _ := scanText(o.File, joined, v)
	if joined.Raw == o.Line.Raw {
		return o.Val, vt
	}
	for _, h := range detectSkips(o.File, joined, v) {
		if h.Val.label == o.Val.label && h.Val.form == o.Val.form {
			return h.Val, vt
		}
	}
	return o.Val, vt
}

// sameSkipBefore reports whether the old version of f already had a skip
// statement identical to the one o adds (same marker, same reason): the
// new test follows a convention of the file, e.g. tests that cannot run on
// the test database.
func sameSkipBefore(o occurrence[skipHit]) bool {
	f := o.File
	if f.OldPath == "" {
		return false
	}
	_, vt := fullSkip(o, newVersion)
	key := rulekit.Normalize(vt)
	word := compact(o.Val.label)
	if i := strings.LastIndexAny(word, ".@"); i >= 0 && i+1 < len(word) {
		word = word[i+1:]
	}
	word = strings.Trim(word, "()[]#:! ")
	get := oldVersion.getter(f)
	for k := 1; k <= lastLine(f, oldVersion); k++ {
		l, ok := get(k)
		if !ok || word == "" || !strings.Contains(l.Code, word) {
			continue
		}
		ovt, _ := scanText(f, forwardJoin(get, core.Line{LexedLine: l, No: k}, maxJoin), oldVersion)
		if rulekit.Normalize(ovt) == key {
			return true
		}
	}
	return false
}

// leadingComments returns the comments on the lines right above new line n
// (doc comments, and attribute or decorator lines in between) and on n
// itself.
func leadingComments(f *core.FileInfo, n int) string {
	var parts []string
	if l, ok := f.NewLine(n); ok && l.Comment != "" {
		parts = append(parts, l.Comment)
	}
	for k := n - 1; k >= 1 && k >= n-10; k-- {
		l, ok := f.NewLine(k)
		if !ok {
			break
		}
		code := strings.TrimSpace(l.Code)
		switch {
		case code == "" && l.Comment != "":
			parts = append(parts, l.Comment)
		case code == "":
			if strings.TrimSpace(l.Raw) != "" {
				return strings.Join(parts, " ")
			}
		case strings.HasPrefix(code, "#[") || strings.HasPrefix(code, "@") || strings.HasPrefix(code, "["):
			if l.Comment != "" {
				parts = append(parts, l.Comment)
			}
		default:
			return strings.Join(parts, " ")
		}
	}
	return strings.Join(parts, " ")
}

var (
	// optInRe matches what says that a test is meant to run only on demand:
	// a benchmark, a slow or live test, run with `cargo test -- --ignored`.
	optInRe = regexp.MustCompile(`(?i)--(?:include-)?ignored\b|\bbench(?:mark)?s?\b|\bperf(?:ormance)?\b|\bmanual(?:ly)?\b|\bopt[- ]in\b|\bon demand\b|\bexpensive\b|\blong[- ]running\b|\bslow\b|\bnightly\b|\blive\b|\bstress\b|\bsoak\b|\bfuzz\w*\b|\brequire[sd]?\b|\bneeds?\b|\bonly (?:run|meant|works|on)\b|\brun (?:it |this |them )?(?:with|explicitly|locally|manually|by hand)\b`)
	// runIgnoredRe matches explicit instructions for running an ignored test.
	runIgnoredRe = regexp.MustCompile(`--(?:include-)?ignored\b`)
	// brokenRe matches reasons that say the test is known to fail.
	brokenRe = regexp.MustCompile(`(?i)\btodo\b|\bfixme\b|\bfix (?:this|that|it|me|later)\b|\bfixing\b|\bneeds? (?:a )?fix\b|\bbroken\b|\bfail(?:s|ing)\b|\bflak(?:y|e|es|iness)\b|\bbugs?\b|\bhack\b|\btemporar(?:y|ily)\b|\bwip\b|\bnot (?:yet )?(?:working|implemented)\b|\b(?:does|do)(?:n't| not) work\b|\bintermittent(?:ly)?\b|\bunstable\b|\bunreliable\b|\bnot (?:yet )?reliable\b|\buntil\b.*\bfixed\b`)
	// xfailStrictRe matches a strict expected failure: pytest then fails the
	// test as soon as the bug is fixed, so it still runs.
	xfailStrictRe = regexp.MustCompile(`\bstrict\s*=\s*True\b`)
)

// stubOverride reports whether the test declared on new line decl of f has
// an empty body (`pass`, `...`, `{}`) and shares its name with another test
// in the file: a subclass opting out of a test it inherits from a base
// class or mixin, which runs there.
func stubOverride(f *core.FileInfo, name testName, decl int) bool {
	if decl == 0 || f.Lang != lang.Python {
		return false
	}
	head, ok := f.NewLine(decl)
	if !ok {
		return false
	}
	ind := indent(head.Raw)
	stub := false
	for k := decl + 1; k <= decl+6; k++ {
		l, ok := f.NewLine(k)
		if !ok {
			break
		}
		code := strings.TrimSpace(l.Code)
		if code == "" {
			continue
		}
		if indent(l.Raw) <= ind {
			break
		}
		if code != "pass" && code != "..." && code != `""""""` && code != "''''''" {
			return false
		}
		stub = true
	}
	if !stub {
		return false
	}
	get := newVersion.getter(f)
	count := 0
	for k := 1; k <= lastLine(f, newVersion); k++ {
		l, ok := get(k)
		if ok && strings.Contains(l.NoComment, name.name) && declName(l).name == name.name {
			count++
		}
	}
	return count > 1
}

// freshSkip decides what to report about the skip o on a test that the
// change added; report is false when it is not worth reporting.
//
// A conditional skip of a new test (an environment check, a capability
// probe, a missing prerequisite) disables nothing that ran before. It is
// not reported when it is an environment gate, an assumption
// (assumeTrue(...)) or explains itself with a reason; otherwise it is a
// note. A new test that is skipped unconditionally from the start is a
// warning, or a note when the skip is deliberate: an opt-in benchmark or
// live test (not reported when it says how to run it), a strict xfail that
// links an issue, a reason that the file already gives for other skips, or
// an empty override that opts a subclass out of an inherited test.
func freshSkip(o occurrence[skipHit], name testName, decl int, hint string) (sev core.Severity, why string, report bool) {
	f, h := o.File, o.Val
	full, stmt := fullSkip(o, newVersion)
	reason := full.reason
	// Comments next to the marker say why it is there; the doc comment of
	// the test may also say how to run it.
	near := leadingComments(f, o.Line.No)
	comments := near
	if decl > 0 && decl != o.Line.No {
		comments += " " + leadingComments(f, decl)
	}
	broken := brokenRe.MatchString(reason) || brokenRe.MatchString(near)
	if skipGate(o, newVersion) != "" || helperFunc(f, name, decl) {
		// A helper (skipUnlessDocker(t)) skips only the tests that call it,
		// when they call it.
		switch {
		case hint == skipEnvHint || hint == skipFlagHint:
			return 0, "", false
		case strings.HasPrefix(h.label, "assume") || strings.HasPrefix(h.label, "Assume"):
			return 0, "", false // assumeTrue(precondition): what assumptions are for
		case !broken && (reason != "" || h.pos < h.end && h.end <= len(stmt) && strings.ContainsAny(stmt[h.pos:h.end], "\"'`")):
			return 0, "", false // explains itself (possibly in a template literal)
		}
		return core.Info, "the test is new, so nothing that ran before stops running", true
	}
	switch {
	case !broken && runIgnoredRe.MatchString(reason+" "+comments):
		return 0, "", false // an opt-in test with instructions for running it
	case strings.Contains(h.label, "xfail") && xfailStrictRe.MatchString(stmt) && (issueRef(f, o.Line, reason) || refersToIssue(display(stmt))):
		return core.Info, "a strict xfail that links an issue: the test still runs and fails once the bug is fixed", true
	case sameSkipBefore(o):
		return core.Info, "the file already skips other tests for the same reason", true
	case stubOverride(f, name, decl):
		return core.Info, "an empty override that opts this class out of an inherited test", true
	case !broken && (envReasonRe.MatchString(reason) || optInRe.MatchString(reason+" "+comments)):
		return core.Info, "the reason says it only runs on demand or in a particular environment", true
	case broken:
		return core.Warning, "the reason says it is known to be broken", true
	}
	return core.Warning, "", true
}
