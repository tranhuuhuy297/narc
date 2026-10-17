package tests

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

const (
	skipHint = "Don't skip a failing test to get a green run. Fix the code under test (or the test, if its " +
		"expectation is genuinely wrong) and remove the skip. If the test really must be disabled, give a reason " +
		"that links an issue and have a human approve it: narc:allow skip-added reason=\"...\"."
	skipEnvHint = "Environment-gated skips are usually deliberate. Make sure the test still runs in at least one " +
		"environment you rely on (normally CI) and that the gate does not hide a real failure."
	skipIssueHint = "This skip links an issue, which is usually deliberate. Make sure the issue is real and tracked, " +
		"and remove the skip as soon as it is fixed."
	todoHint = "test.todo is for tests that have not been written yet. Restore the real test and make it pass, or " +
		"skip it with a reason that links an issue."
	skipFlagHint = "The skip is driven by a flag in the test data. Make sure no case sets that flag to hide a " +
		"failure, and give every case that sets it a reason that links an issue."
	freshSkipHint = "This test is new and has never run. Make it pass and remove the skip; if it cannot run here, " +
		"say why in the skip's reason and link an issue, so the gap is tracked rather than hidden."
)

var skipAdded = &core.Rule{
	ID:       "skip-added",
	Title:    "Test skipped",
	Category: "tests",
	Severity: core.Error,
	Summary:  "A test was switched off with a skip marker (it.skip, xit, @pytest.mark.skip, t.Skip, #[ignore], @Disabled, ...).",
	Hint:     skipHint,
	Docs: "A skipped test reports as \"skipped\", not \"failed\", so the run turns green while the behaviour it " +
		"checked is no longer verified. Agents reach for skips when they cannot make a test pass.\n\n" +
		"narc flags skip and disable markers that the change introduced, in every major test framework: " +
		"`it.skip`/`xit`/`this.skip()`/`test.fixme`, `@pytest.mark.skip`/`skipif`/`xfail`/`pytest.skip()`, " +
		"`t.Skip`, `#[ignore]`, `@Disabled`/`@Ignore`, `[Fact(Skip = ...)]`, RSpec `xit`/`skip`/`pending`, " +
		"`markTestSkipped`, `XCTSkip`/`.disabled`, Dart `skip:`, Elixir `@tag :skip`, GoogleTest `DISABLED_`, " +
		"`QUnit.skip`/`QUnit.module.skip` and kotest's `!` name prefix — also under an aliased import " +
		"(`@pt.mark.skip`) or a differently named test handle (`st.Skip()`) — and a Go `TestMain` that no " +
		"longer calls `m.Run()`.\n\n" +
		"Skips that only apply in some environments (`if testing.Short()`, `skipif(sys.platform == ...)`, " +
		"`@DisabledOnOs`) or that link an issue are reported as warnings, and a gate that only excludes Windows " +
		"or macOS as a note; a constant condition (`skipif(True)`, `cfg_attr(all(), ignore)`) is an error. Skips " +
		"that are merely moved, reformatted, or narrowed (a `describe.skip` replaced by an `it.skip` inside it) are " +
		"not reported, but a gated skip that loses its condition is.\n\n" +
		"A skip on a test that the change adds switches off nothing that ran before. A new test that is skipped " +
		"from the start is a warning (a note when the skip is deliberate: an opt-in benchmark, a strict xfail that " +
		"links an issue, the reason the file already gives for its other skips); a conditional skip of a new test " +
		"(an environment check, a capability probe, `assumeTrue(...)`) is not reported when it gives a reason. A " +
		"renamed test is not a new one.\n\n" +
		"```ts\n// bad: the failing test is silenced\nit.skip('rejects expired tokens', () => { ... })\n\n" +
		"// good: fix verifyToken() so the test passes, and keep it running\nit('rejects expired tokens', () => { ... })\n```",
}

func init() {
	skipAdded.Check = checkSkip
	core.Register(skipAdded)
}

// skipForm says where a skip marker sits relative to the test it disables.
type skipForm int

const (
	formDecl skipForm = iota // part of the test declaration: it.skip("name", ...)
	formAttr                 // an annotation, decorator or attribute above the test: @Disabled
	formCall                 // a statement in the test body: t.Skip(), pytest.skip()
)

// skipHit is a skip marker found on a line.
type skipHit struct {
	label    string   // the marker as written, e.g. "it.skip", "@pytest.mark.skip"
	kind     string   // what cancels what (defaults to the compacted label)
	form     skipForm // where the marker sits
	pos, end int      // the marker's span in the line's value text
	cond     string   // the condition of a conditional skip, as value text
	inverse  bool     // cond enables the test (runIf, skipUnless, @EnabledIf)
	envGate  bool     // the marker is an environment gate by itself (@DisabledOnOs)
	reason   string   // the reason given, if any
	todo     bool     // a .todo placeholder
	module   bool     // the marker applies to every test in the file
	// programmatic is set for a pytest marker object that is not a
	// decorator: `skip_slow = pytest.mark.skip(...)` or
	// `item.add_marker(pytest.mark.skip(...))` in a collection hook. Which
	// tests it skips is decided elsewhere, usually by a command-line option.
	programmatic bool
	name         testName // the test, when the marker names it
}

func checkSkip(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool { return inTests(f) || f.Lang == lang.Rust })
	// was records, per added skip, the condition of a removed skip that it
	// made unconditional.
	type at struct {
		f *core.FileInfo
		n int
	}
	was := map[at]string{}
	compatible := func(removed, added occurrence[skipHit]) bool {
		gate := skipGate(removed, oldVersion)
		if gate == "" || skipGate(added, newVersion) != "" {
			return true
		}
		was[at{added.File, added.Line.No}] = gate
		return false
	}
	var out []core.Finding
	for _, o := range introduced(files, detectSkips, cancelling[skipHit]{scope: skipScope, compatible: compatible}) {
		if o.Val.todo {
			if fd, ok := todoFinding(o); ok {
				out = append(out, fd)
			}
			continue
		}
		fd, ok := skipFinding(files, o)
		if !ok {
			continue
		}
		if gate, ok := was[at{o.File, o.Line.No}]; ok {
			fd.Message += fmt.Sprintf(" — it used to skip only when %s, now it always does", display(gate))
		}
		out = append(out, fd)
	}
	for _, f := range files {
		if f.Lang == lang.Go && inTests(f) {
			if fd, ok := testMainSkipsAll(f); ok {
				out = append(out, fd)
			}
		}
	}
	return out
}

// ---- Go TestMain --------------------------------------------------------------

var (
	// goTestMainRe matches the declaration of a package's TestMain; group 1
	// is the name of its *testing.M.
	goTestMainRe = regexp.MustCompile(`^func\s+TestMain\s*\(\s*(\w+)\s+\*testing\.M\s*\)`)
)

// testMain locates the TestMain among lines 1..n of one version of a Go
// file (get reads them; without full contents only hunk lines exist): its
// declaration, the closing brace at column 0 (gofmt layout), and whether it
// runs the tests, by calling m.Run() or handing m to a helper that does
// (goleak.VerifyTestMain(m), testscript.RunMain(m, ...)). ok is false when
// there is no TestMain or part of it cannot be read.
func testMain(get lineGetter, n int) (first, last int, runs, ok bool) {
	for i := 1; i <= n; i++ {
		l, found := get(i)
		if !found {
			continue
		}
		m := goTestMainRe.FindStringSubmatch(l.Code)
		if m == nil {
			continue
		}
		passed := regexp.MustCompile(`\b` + m[1] + `\.Run\(|[(,]\s*` + m[1] + `\s*[,)]`)
		for j := i + 1; ; j++ {
			body, found := get(j)
			if !found {
				return 0, 0, false, false // the end of TestMain is not visible
			}
			if strings.HasPrefix(body.Code, "}") {
				return i, j, runs, true
			}
			runs = runs || passed.MatchString(body.Code)
		}
	}
	return 0, 0, false, false
}

// mentionsTestMain reports whether the new side of f may declare a
// TestMain: its contents, or the hunks of a bare patch, contain the name.
func mentionsTestMain(f *core.FileInfo) bool {
	const name = "func TestMain"
	if b, ok := f.NewContent(); ok {
		return bytes.Contains(b, []byte(name))
	}
	for _, h := range f.Diff.Hunks {
		for _, l := range h.Lines {
			if l.Op != diff.Del && strings.Contains(l.Text, name) {
				return true
			}
		}
	}
	return false
}

// lastLine returns the highest line number of one side of f that rules can
// read: the file's length, or the end of the last hunk of a bare patch.
func lastLine(f *core.FileInfo, v version) int {
	if v == oldVersion && f.HasOld() {
		return len(f.OldLines())
	}
	if v == newVersion && f.HasNew() {
		return len(f.NewLines())
	}
	n := 0
	for _, h := range f.Diff.Hunks {
		if v == oldVersion {
			n = max(n, h.OldStart+h.OldLines-1)
		} else {
			n = max(n, h.NewStart+h.NewLines-1)
		}
	}
	return n
}

// testMainSkipsAll reports a TestMain that the change left without running
// the tests: go test then reports ok for a package in which no test ran.
func testMainSkipsAll(f *core.FileInfo) (core.Finding, bool) {
	if !mentionsTestMain(f) {
		return core.Finding{}, false // the common case: no need to lex the whole file
	}
	first, last, runs, ok := testMain(f.NewLine, lastLine(f, newVersion))
	if !ok || runs {
		return core.Finding{}, false
	}
	if _, _, oldRuns, hadOld := testMain(f.OldLine, lastLine(f, oldVersion)); hadOld && !oldRuns {
		return core.Finding{}, false // it never ran them: not this change
	}
	touched := false
	for _, l := range f.Added {
		if l.No >= first && l.No <= last {
			touched = true
			break
		}
	}
	if !touched {
		return core.Finding{}, false
	}
	fd := skipAdded.Finding(f, first, "TestMain never calls m.Run(): go test reports ok for every test in the package without running any of them")
	fd.Hint = "TestMain must run the tests: end it with os.Exit(m.Run()). Do setup and teardown around that call " +
		"instead of returning early, and fix the failing tests rather than skipping the whole package."
	return fd, true
}

// skipGate returns the condition that the skip o (on a line of version v)
// only applies under, or "" when it always applies. A conditional skip that
// was made unconditional (`if testing.Short() { t.Skip() }` → `t.Skip()`,
// skipif → skip) is a new skip, not an edit of the old one.
func skipGate(o occurrence[skipHit], v version) string {
	h := o.Val
	switch {
	case h.envGate || h.programmatic:
		return h.label
	case h.cond != "":
		if alwaysSkips(h.cond, h.inverse, o.File.Lang) {
			return ""
		}
		return h.cond
	case h.form == formCall:
		vt, _ := scanText(o.File, o.Line, v)
		if g, ok := guardIn(v.getter(o.File), o.File.Lang, o.Line, vt, h.pos, h.end); ok {
			return g
		}
	}
	return ""
}

// skipScope is the scopeFunc of skip markers: the test a marker disables
// (the declaration it is part of, the test below an annotation, or the test
// whose body calls it) and the suites around it.
func skipScope(o occurrence[skipHit], v version) []string {
	if o.Val.module {
		return []string{fileScope}
	}
	get := v.getter(o.File)
	own := o.Val.name
	if !own.ok() {
		switch o.Val.form {
		case formDecl:
			if own = declName(o.Line.LexedLine); !own.ok() {
				own = testAboveIn(get, o.Line.No, true)
			}
		case formAttr:
			own = testAfterIn(get, o.Line.No)
		default:
			own = testAboveIn(get, o.Line.No, false)
		}
	}
	return markerScope(get, o.Line.No, own)
}

// skipFinding reports the skip o; ok is false when it is not worth
// reporting (an environment gate on a test the change added).
func skipFinding(files []*core.FileInfo, o occurrence[skipHit]) (fd core.Finding, ok bool) {
	f, h := o.File, o.Val
	vt, _ := scanText(f, o.Line, newVersion)
	sev, why, hint := assessSkip(f, o.Line, vt, h)

	// A skip on a test that the change added disables nothing that ran
	// before (see freshSkip).
	fresh := false
	var target testName
	switch {
	case h.programmatic:
	case h.module:
		fresh = f.OldPath == ""
		if fresh {
			if sev == core.Warning && hint == skipEnvHint {
				return core.Finding{}, false
			}
			sev, hint = core.Warning, freshSkipHint
		}
	default:
		var decl int
		target, decl = markerTarget(o)
		switch {
		case target.ok():
			fresh = freshTest(files, f, target, decl)
		case f.OldPath == "":
			fresh = true // a brand-new file: whatever the marker skips is new
		default:
			fresh, _ = aliasFresh(files, o)
		}
		if fresh {
			s, w, report := freshSkip(o, target, decl, hint)
			if !report {
				return core.Finding{}, false
			}
			sev, hint = s, freshSkipHint
			if w != "" {
				if why != "" {
					why += "; "
				}
				why += w
			}
		}
	}

	name := h.name
	switch {
	case name.ok():
	case h.form == formDecl:
		if name = declName(o.Line.LexedLine); !name.ok() {
			name = testClosedBy(f, o.Line.No)
		}
	case h.form == formAttr:
		name = testAfter(f, o.Line.No)
	default:
		name = testAround(f, o.Line.No)
	}
	if !name.ok() && f.Lang.IsJSLike() {
		name = jsLayoutName(f, o.Line.No)
	}
	if !name.ok() {
		name = target
	}

	var msg strings.Builder
	switch {
	case h.module:
		fmt.Fprintf(&msg, "every test in %s is now skipped with %s", f.Path, h.label)
	case h.programmatic:
		// The marker object is attached to tests elsewhere (item.add_marker):
		// name the hook or helper that creates it, not a test.
		if fn := testAround(f, o.Line.No); fn.ok() {
			fmt.Fprintf(&msg, "%s now creates a %s marker for tests", fn, h.label)
		} else {
			fmt.Fprintf(&msg, "new %s marker object", h.label)
		}
	case fresh && h.module:
		fmt.Fprintf(&msg, "every test in the new file %s is skipped with %s", f.Path, h.label)
	case fresh && h.form == formCall && name.ok():
		fmt.Fprintf(&msg, "new %s skips itself with %s", named(unitNoun(h.label, o.Line), name), h.label)
	case fresh && name.ok():
		fmt.Fprintf(&msg, "new %s is added already skipped with %s", named(unitNoun(h.label, o.Line), name), h.label)
	case h.form == formCall && name.ok():
		fmt.Fprintf(&msg, "%s now skips itself with %s", named(unitNoun(h.label, o.Line), name), h.label)
	case h.form == formCall:
		fmt.Fprintf(&msg, "new %s call skips the test", h.label)
	case name.ok():
		fmt.Fprintf(&msg, "%s is now skipped with %s", named(unitNoun(h.label, o.Line), name), h.label)
	default:
		fmt.Fprintf(&msg, "new skip marker %s", h.label)
	}
	if h.reason != "" {
		fmt.Fprintf(&msg, " (reason: %q)", clip(h.reason, 80))
	}
	if why != "" {
		msg.WriteString(" — " + why)
	}
	fd = skipAdded.Finding(f, o.Line.No, msg.String())
	fd.Severity, fd.Hint = sev, hint
	return fd, true
}

// assessSkip decides how serious a new skip is. Unconditional skips and
// skips that trigger on what the code under test does are errors; skips
// gated on the environment, or that link an issue, are warnings.
func assessSkip(f *core.FileInfo, l core.Line, vt string, h skipHit) (core.Severity, string, string) {
	when := "only when "
	if h.inverse {
		when = "unless "
	}
	switch {
	case h.envGate:
		return core.Warning, "environment-gated", skipEnvHint
	case h.programmatic:
		return core.Warning, "check which tests it is attached to, and when", skipEnvHint
	case h.cond != "" && alwaysSkips(h.cond, h.inverse, f.Lang):
		if strings.HasPrefix(h.label, "#[cfg(") {
			return core.Error, display(h.cond) + " never holds, so the test is compiled out of every build", skipHint
		}
		return core.Error, "the condition " + display(h.cond) + " always skips", skipHint
	case h.cond != "" && isEnvCondition(h.cond):
		if offPlatformGate(h.cond, h.inverse) && !brokenRe.MatchString(h.reason) {
			return core.Info, when + display(h.cond) + offPlatformNote, skipEnvHint
		}
		return core.Warning, when + display(h.cond) + " (environment-gated)", skipEnvHint
	case h.cond != "" && h.form != formCall:
		// A declarative gate (skipif, @DisabledIf, skip: cond) is evaluated
		// before the test runs, so it cannot depend on the code under test.
		return core.Warning, when + display(h.cond), skipEnvHint
	}
	cond := h.cond
	if cond == "" && h.form == formCall {
		if g, ok := guard(f, l, vt, h.pos, h.end); ok {
			cond = g
			if isEnvCondition(g) {
				if offPlatformGate(g, false) && !brokenRe.MatchString(h.reason) {
					return core.Info, "only when " + display(g) + offPlatformNote, skipEnvHint
				}
				return core.Warning, "only when " + display(g) + " (environment-gated)", skipEnvHint
			}
			if isSkipFlag(g) {
				// if tc.skip { t.Skip() }: which cases skip is decided by the
				// test table, not by what the code under test does.
				return core.Warning, "only when " + display(g) + " (a skip flag in the test data)", skipFlagHint
			}
		}
	}
	if cond != "" && envReasonRe.MatchString(h.reason) {
		return core.Warning, "when " + display(cond), skipEnvHint
	}
	why := ""
	if cond != "" {
		why = "whenever " + display(cond)
	}
	if issueRef(f, l, h.reason) {
		if why != "" {
			why += "; "
		}
		return core.Warning, why + "references an issue", skipIssueHint
	}
	return core.Error, why, skipHint
}

const offPlatformNote = " (a platform gate: the test still runs on Linux, where CI usually runs)"

var (
	// platformCheckRe matches a check of the operating system against
	// Windows or macOS: process.platform === 'win32', runtime.GOOS ==
	// "windows", sys.platform == "darwin", os.name == "nt". Group 1 is the
	// operator.
	platformCheckRe = regexp.MustCompile(`^(?:process\.platform|runtime\.GOOS|sys\.platform|os\.name|platform\.system\(\)|os\.platform\(\)|Deno\.build\.os)\s*(===|==|!==|!=)\s*["'](?:win32|windows|Windows|darwin|Darwin|nt|cygwin|msys)["']$|^sys\.platform\.startswith\(\s*["'](?:win|darwin|cygwin)["']\s*\)$`)
	// platformFlagRe matches a flag that says the tests run on Windows or
	// macOS: isWindows, onWindows, IS_WINDOWS, isMac, isDarwin.
	platformFlagRe = regexp.MustCompile(`^(?:is|on|IS_|ON_)_?(?:Windows|WINDOWS|Win32|WIN32|Mac|MAC|MacOS|MACOS|Darwin|DARWIN|windows|win32|macos|darwin)(?:\(\))?$`)
	// rootCheckRe matches a check that the tests run as root, where
	// permission bits are not enforced.
	rootCheckRe = regexp.MustCompile(`^(?:process\.(?:getuid|geteuid)\??\.?\(\)|os\.(?:Getuid|Geteuid|getuid|geteuid)\(\))\s*===?\s*0$`)
)

// offPlatformGate reports whether a skip condition only excludes Windows or
// macOS (possibly together with running as root): the test still runs on
// Linux, where CI usually runs, so the gate hides nothing there. Porting a
// suite to Windows adds many such gates to POSIX-only tests. inverse says
// that cond enables the test (runIf, skipUnless).
func offPlatformGate(cond string, inverse bool) bool {
	c := trimParens(strings.TrimSpace(display(cond)))
	if inverse {
		// runIf(process.platform !== 'win32'), runIf(!isWindows)
		if m := platformCheckRe.FindStringSubmatch(c); m != nil {
			return m[1] == "!==" || m[1] == "!="
		}
		rest, neg := strings.CutPrefix(c, "!")
		return neg && platformFlagRe.MatchString(strings.TrimSpace(rest))
	}
	platform := false
	for _, part := range splitOp(c, "||") {
		part = trimParens(strings.TrimSpace(part))
		switch m := platformCheckRe.FindStringSubmatch(part); {
		case m != nil && (m[1] == "===" || m[1] == "==" || m[1] == ""):
			platform = true
		case m == nil && platformFlagRe.MatchString(part):
			platform = true
		case m == nil && rootCheckRe.MatchString(part):
		default:
			return false
		}
	}
	return platform
}

// alwaysSkips reports whether a skip condition is a constant that always
// disables the test: skipif(True), skipIf(true), runIf(false).
func alwaysSkips(cond string, inverse bool, lg lang.Lang) bool {
	if lg == lang.Rust {
		v, ok := cfgConstant(cond)
		return ok && v != inverse
	}
	c := strings.TrimSpace(cond)
	neg := false
	for {
		switch {
		case strings.HasPrefix(c, "!"):
			c, neg = strings.TrimSpace(c[1:]), !neg
			continue
		case strings.HasPrefix(c, "not "):
			c, neg = strings.TrimSpace(c[4:]), !neg
			continue
		}
		break
	}
	l := lit(newExpr(c), lg)
	var v bool
	switch l.kind {
	case litBool:
		v = l.value == "true"
	case litNumber:
		v = l.num != 0
	default:
		return false
	}
	return v != neg != inverse
}

// isSkipFlag reports whether a guard condition reads a skip flag, as in
// table-driven tests: `if tc.skip`, `if tt.skipReason != ""`.
func isSkipFlag(cond string) bool {
	for _, w := range words(display(cond)) {
		if w == "skip" || w == "skipped" {
			return true
		}
	}
	return false
}

// named prefixes a test name with its noun unless the name says it already:
// `test "rejects expired tokens"`, `test appliesDiscount`, but `TestUpload`.
func named(noun string, n testName) string {
	if n.suite {
		noun = "suite"
	}
	if !n.quoted && strings.HasPrefix(strings.ToLower(n.name), noun) {
		return n.String()
	}
	return noun + " " + n.String()
}

var suiteDeclRe = regexp.MustCompile(`(?:^|[^\w$.])(?:[xf]?describe|[xf]?context|suite|group|feature|class)\b|\.describe\b|\bQUnit\.module\b`)

// unitNoun says whether a marker disables a single test or a whole suite.
func unitNoun(label string, l core.Line) string {
	lower := strings.ToLower(label)
	for _, s := range []string{"describe", "context", "suite", "fixture", "group", "module", "describetag"} {
		if strings.Contains(lower, s) {
			return "suite"
		}
	}
	if suiteDeclRe.MatchString(l.Code) {
		return "suite"
	}
	return "test"
}

// ---- .todo placeholders -----------------------------------------------------

var jsRealTestRe = regexp.MustCompile("\\b(?:it|test)(?:\\.(?:concurrent|serial|only|each))?\\s*\\(\\s*[\"'`]")

// todoFinding reports a test.todo that replaced a real test of the same
// name. A todo for a test that never existed is just a plan.
func todoFinding(o occurrence[skipHit]) (core.Finding, bool) {
	f := o.File
	name := declName(o.Line.LexedLine)
	if !name.ok() {
		return core.Finding{}, false
	}
	replaced := false
	for _, r := range f.Removed {
		if jsRealTestRe.MatchString(r.Code) && declName(r.LexedLine) == name {
			replaced = true
			break
		}
	}
	if !replaced {
		return core.Finding{}, false
	}
	for _, l := range f.NewLines() {
		if jsRealTestRe.MatchString(l.Code) && declName(l) == name {
			return core.Finding{}, false // the real test still exists
		}
	}
	fd := skipAdded.Finding(f, o.Line.No, fmt.Sprintf("test %s was replaced by a %s placeholder — its body and assertions no longer run", name, o.Val.label))
	fd.Severity, fd.Hint = core.Warning, todoHint
	return fd, true
}

// ---- detection ----------------------------------------------------------------

// scanText returns the value text that the skip detectors match on line l
// of version v: valueText, plus jsScanText's rewrites for JavaScript. ok is
// false when literal values on the line cannot be trusted.
func scanText(f *core.FileInfo, l core.Line, v version) (string, bool) {
	vt, ok := valueText(l, f.Lang)
	if f.Lang.IsJSLike() {
		vt = jsScanText(f, l, v, vt)
	}
	return vt, ok
}

// detectSkips finds skip markers on a line, dispatching on the language.
func detectSkips(f *core.FileInfo, l core.Line, v version) []occurrence[skipHit] {
	s := lineScan{f: f, l: l, v: v}
	s.vt, s.ok = scanText(f, l, v)
	var hs []skipHit
	switch f.Lang {
	case lang.JavaScript, lang.TypeScript:
		hs = s.skipsJS()
	case lang.Python:
		hs = s.skipsPython()
	case lang.Go:
		hs = s.skipsGo()
	case lang.Rust:
		hs = s.skipsRust()
	case lang.Java, lang.Kotlin, lang.Groovy, lang.Scala:
		hs = s.skipsJVM()
	case lang.CSharp:
		hs = s.skipsCSharp()
	case lang.Ruby:
		hs = s.skipsRuby()
	case lang.PHP:
		hs = s.skipsPHP()
	case lang.Swift:
		hs = s.skipsSwift()
	case lang.Dart:
		hs = s.skipsDart()
	case lang.Elixir:
		hs = s.skipsElixir()
	case lang.C, lang.Cpp:
		hs = s.skipsGTest()
	}
	out := make([]occurrence[skipHit], len(hs))
	for i, h := range hs {
		out[i] = occurrence[skipHit]{File: f, Line: l, Kind: skipKind(f.Lang, l, h), Val: h}
	}
	return out
}

// skipKind groups the spellings of a skip marker that mean the same thing, so
// that a mechanical rewrite from one to another is not mistaken for a new
// skip: xit → it.skip (eslint-plugin-jest's no-test-prefixes fix), it.skip
// → test.skip (consistent-test-it), JUnit 4 @Ignore → JUnit 5 @Disabled,
// @unittest.skip → @pytest.mark.skip, NUnit [Ignore] → xUnit
// [Fact(Skip = ...)], t.Skip → t.Skipf.
func skipKind(lg lang.Lang, l core.Line, h skipHit) string {
	if h.kind != "" {
		return h.kind
	}
	label := compact(h.label)
	switch {
	case lg.IsJSLike():
		switch {
		case h.todo:
			return "todo"
		case h.form == formCall:
			return "skip()"
		case h.cond != "":
			return "skipIf"
		case strings.HasSuffix(label, ".failing"):
			return "failing"
		}
		return "skip-" + unitNoun(label, l)
	case lg == lang.Python:
		xfail := strings.Contains(label, "xfail") || strings.Contains(label, "expectedFailure")
		switch {
		case h.form == formCall && xfail:
			return "xfail()"
		case h.form == formCall:
			return "skip()"
		case xfail:
			return "xfail"
		case h.cond != "":
			return "skipif"
		}
		return "skip"
	case lg == lang.Go:
		return "t.Skip"
	case isJVM(lg) && (label == "@Disabled" || label == "@Ignore"):
		return "disabled"
	case lg == lang.CSharp && (strings.HasPrefix(label, "[Ignore") || strings.Contains(label, "(Skip=")):
		return "disabled"
	}
	return label
}

// lineScan is one line prepared for marker detection.
type lineScan struct {
	f  *core.FileInfo
	l  core.Line
	v  version // the version of f that l belongs to
	vt string  // value text (see scanText)
	ok bool    // literal values in vt are trustworthy
}

// call parses the argument list that starts at or after vt[i] (only
// whitespace may come first). end is the index just past ")". A call that
// continues on the next line (it.skip('name', () => {) yields the arguments
// that are on this line.
func (s lineScan) call(i int) (as []expr, end int, ok bool) {
	for i < len(s.vt) && (s.vt[i] == ' ' || s.vt[i] == '\t') {
		i++
	}
	if i >= len(s.vt) || s.vt[i] != '(' {
		return nil, i, false
	}
	if as, e, ok := args(s.vt, i); ok {
		return as, e + 1, true
	}
	for _, a := range splitTop(s.vt[i+1:], ',') {
		if e := newExpr(a); e.raw != "" {
			as = append(as, e)
		}
	}
	return as, len(s.vt), true
}

// continuedArgs returns the arguments of a marker call that opens at
// vt[open:] (after the marker that re matches) and continues on the lines
// below: they are read from the statement joined over those lines. It
// returns nil when the call is not open at the end of the line.
func (s lineScan) continuedArgs(re *regexp.Regexp, open int) []expr {
	rest := strings.TrimSpace(s.vt[open:])
	if !strings.HasPrefix(rest, "(") {
		return nil
	}
	if _, closed := closing(rest, 0); closed {
		return nil
	}
	joined := forwardJoin(s.v.getter(s.f), s.l, maxJoin)
	if joined.Raw == s.l.Raw {
		return nil
	}
	js := lineScan{f: s.f, l: joined, v: s.v}
	js.vt, js.ok = scanText(s.f, joined, s.v)
	m := re.FindStringIndex(js.vt)
	if m == nil {
		return nil
	}
	as, _, ok := js.call(m[1])
	if !ok {
		return nil
	}
	if !js.ok {
		// Literal values in the joined text cannot be trusted: keep the
		// arguments' shape but not their contents.
		for i := range as {
			if isStr(as[i]) {
				as[i] = newExpr(`""`)
			}
		}
	}
	return as
}

// str returns the contents of a string literal argument.
func (s lineScan) str(e expr) (string, bool) {
	if !s.ok {
		return "", false
	}
	l := lit(e, s.f.Lang)
	if l.kind != litString {
		return "", false
	}
	return l.value, true
}

// isStr reports whether e is a string literal (even when its value is not
// trustworthy).
func isStr(e expr) bool {
	return stringRe.MatchString(e.raw) || strings.HasPrefix(e.raw, "`") || strings.HasPrefix(e.raw, "f\"") || strings.HasPrefix(e.raw, "f'")
}

// reasonOf returns the reason argument of a skip call: a reason=/msg=
// keyword, or else the first positional string literal.
func (s lineScan) reasonOf(as []expr) string {
	for _, k := range []string{"reason", "msg", "message"} {
		if v, ok := kwarg(as, k); ok {
			r, _ := s.str(v)
			return r
		}
	}
	for _, a := range positional(as) {
		if r, ok := s.str(a); ok {
			return r
		}
	}
	return ""
}

// optionValue returns the value of a "key: value" option starting at
// vt[i], up to the next top-level comma or closing bracket.
func (s lineScan) optionValue(i int) expr {
	depth := 0
	for j := i; j < len(s.vt); j++ {
		switch s.vt[j] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return newExpr(s.vt[i:j])
			}
			depth--
		case ',':
			if depth == 0 {
				return newExpr(s.vt[i:j])
			}
		}
	}
	return newExpr(s.vt[i:])
}

// optionHit interprets a skip option value: `skip: true` and
// `skip: "reason"` always skip, `skip: false` does not, anything else is a
// condition.
func (s lineScan) optionHit(h skipHit, v expr) (skipHit, bool) {
	l := lit(v, s.f.Lang)
	switch {
	case v.raw == "":
		return h, false
	case l.kind == litBool && l.value == "false", l.kind == litNull, l.kind == litNumber && l.num == 0:
		return h, false
	case l.kind == litBool, l.kind == litNumber:
	case isStr(v):
		h.reason, _ = s.str(v)
	case l.kind == litSymbol:
	default:
		h.cond = v.raw
	}
	return h, true
}

// compact removes all whitespace: "test . skip" → "test.skip".
func compact(s string) string { return strings.Join(strings.Fields(s), "") }

// ---- JavaScript / TypeScript --------------------------------------------------

var (
	jsSkipDotRe  = regexp.MustCompile(`\b(?:it|test|describe|suite|context|specify|bench|fixture|QUnit(?:\s*\.\s*module)?)(?:\s*\.\s*(?:describe|serial|parallel|concurrent|sequential))*\s*\.\s*(skip|fixme|skipIf|runIf|todo|failing)\b`)
	jsXRe        = regexp.MustCompile(`(?:^|[^\w$.])(xit|xdescribe|xtest|xcontext|xspecify|xsuite)\s*\(`)
	jsCtxSkipRe  = regexp.MustCompile(`\b(?:this|ctx|context|t)\.skip\s*\(`)
	jsPendingRe  = regexp.MustCompile(`^\s*pending\s*\(`)
	jsDeclRe     = regexp.MustCompile(`\b(?:it|test|describe|suite|context|specify)(?:\.\w+)*\s*\(|\bDeno\.test\s*\(`)
	denoTestRe   = regexp.MustCompile(`\bDeno\.test\b`)
	denoIgnoreRe = regexp.MustCompile(`\bDeno\.test\.ignore\b`)
	jsFuncRe     = regexp.MustCompile(`^(?:async\s*)?(?:\([^)]*\)|[\w$]+)\s*=>\s*(.*)$`)
)

func (s lineScan) skipsJS() []skipHit {
	var hs []skipHit
	at := map[int]bool{} // where the declaration markers start
	for _, m := range jsSkipDotRe.FindAllStringSubmatchIndex(s.vt, -1) {
		if inJSXText(s.vt, m[0]) {
			continue
		}
		at[m[0]] = true
		h := skipHit{label: compact(s.vt[m[0]:m[1]]), pos: m[0], end: m[1], form: formDecl}
		as, end, called := s.call(m[1])
		if called {
			h.end = end
		}
		switch typ := s.vt[m[2]:m[3]]; typ {
		case "todo":
			h.todo = true
		case "failing":
		case "skipIf", "runIf":
			if !called || len(as) == 0 {
				continue
			}
			h.cond, h.inverse = as[0].raw, typ == "runIf"
			// it.skipIf(cond)('name', fn)
			if as2, _, ok := s.call(end); ok && len(as2) > 0 {
				if n, ok := s.str(as2[0]); ok {
					h.name = testName{name: n, quoted: true}
				}
			}
		case "skip", "fixme":
			if called && len(as) == 0 {
				// test.skip(\n  cond,\n  'reason',\n): Prettier put the
				// arguments on their own lines.
				as = s.continuedArgs(jsSkipDotRe, m[1])
			}
			switch {
			case !called:
				// A reference rather than a call: `cond ? it : it.skip` or an
				// alias such as `const xdescribe = describe.skip`.
				if c, elseBranch, ok := ternaryCond(s.vt[:m[0]]); ok {
					h.cond, h.inverse = c, elseBranch // cond ? it : it.skip skips unless cond
				}
			case len(as) == 0:
				h.form = formCall // Playwright: test.skip() inside the test body
			case isStr(as[0]):
				if n, ok := s.str(as[0]); ok {
					h.name = testName{name: n, quoted: true}
				}
			case jsFuncRe.MatchString(as[0].raw):
				// Playwright: test.skip(({ browserName }) => browserName === 'webkit', 'reason')
				h.form, h.cond = formAttr, jsFuncRe.FindStringSubmatch(as[0].raw)[1]
				h.reason = s.reasonOf(as[1:])
			default:
				// Playwright: test.skip(condition, 'reason') inside the test body
				h.form, h.cond = formCall, as[0].raw
				h.reason = s.reasonOf(as[1:])
			}
		}
		hs = append(hs, h)
	}
	for _, m := range jsXRe.FindAllStringSubmatchIndex(s.vt, -1) {
		if inJSXText(s.vt, m[2]) {
			continue
		}
		as, end, ok := s.call(m[3])
		// xit(...) must name a test (anything else is someone's function),
		// possibly on the next line: xit(\n  'a long name', ...).
		named := ok && len(as) > 0 && isStr(as[0])
		if !named && !(ok && len(as) == 0 && strings.TrimSpace(s.vt[m[3]:]) == "(") {
			continue
		}
		h := skipHit{label: s.vt[m[2]:m[3]], form: formDecl, pos: m[2], end: end}
		if named {
			if n, ok := s.str(as[0]); ok {
				h.name = testName{name: n, quoted: true}
			}
		}
		hs = append(hs, h)
	}
	for _, m := range jsCtxSkipRe.FindAllStringIndex(s.vt, -1) {
		if at[m[0]] {
			continue // Mocha's context.skip('name', ...) is a declaration, found above
		}
		as, end, ok := s.call(m[1] - 1)
		if !ok {
			continue
		}
		h := skipHit{label: compact(s.vt[m[0] : m[1]-1]), form: formCall, pos: m[0], end: end}
		if len(as) > 0 && !isStr(as[0]) {
			if recv := strings.TrimSuffix(h.label, ".skip"); recv == "this" || recv == "t" {
				// Mocha's this.skip() and node:test's t.skip([message]) take no
				// condition: tokens.skip(2) on a tokenizer called t is not a skip.
				continue
			}
			h.cond, h.reason = as[0].raw, s.reasonOf(as[1:]) // Vitest: ctx.skip(condition, note)
		} else {
			h.reason = s.reasonOf(as)
		}
		hs = append(hs, h)
	}
	if m := jsPendingRe.FindStringIndex(s.vt); m != nil {
		// Jasmine's pending() is a statement of its own in a test body; an
		// array element `pending('id', 1),` or `pending()[0].id` calls a
		// fixture helper of the same name.
		if as, end, ok := s.call(m[1] - 1); ok && (len(as) == 0 || isStr(as[0])) &&
			strings.Trim(s.vt[end:], " \t;") == "" && !s.declaresJS("pending") {
			hs = append(hs, skipHit{label: "pending()", form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
		}
	}
	// { skip: true } counts only as an option of the test declaration itself;
	// elsewhere it is data (or an option of the code under test).
	for _, o := range jsDeclOptions(s.f, s.l, s.v, s.vt) {
		if o.key != "skip" && !(o.key == "ignore" && s.mentionsDenoTest()) {
			continue
		}
		h := skipHit{label: o.key + ": " + display(o.value.raw), kind: o.key + ":", form: formDecl}
		if h, ok := s.optionHit(h, o.value); ok {
			if h.cond != "" || h.reason != "" {
				h.label = o.key + ":"
			}
			hs = append(hs, h)
		}
	}
	if m := denoIgnoreRe.FindStringIndex(s.vt); m != nil {
		hs = append(hs, skipHit{label: "Deno.test.ignore", form: formDecl, pos: m[0], end: m[1]})
	}
	return hs
}

// declaresJS reports whether the scanned version of the file declares or
// imports name itself, shadowing the test framework's global of that name.
func (s lineScan) declaresJS(name string) bool {
	var b []byte
	var ok bool
	if s.v == oldVersion {
		b, ok = s.f.OldContent()
	} else {
		b, ok = s.f.NewContent()
	}
	if !ok {
		return false
	}
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`\b(?:const|let|var|function\*?)\s+` + q + `\b|\bimport\b[^;]*?[{,\s]` + q + `\s*[,}]|[(,]\s*` + q + `\s*(?::[^,)]*)?[,)]\s*(?:=>|\{)`)
	return re.Match(b)
}

func (s lineScan) mentionsDenoTest() bool {
	if denoTestRe.MatchString(s.l.Code) {
		return true
	}
	b, ok := s.f.NewContent()
	return ok && denoTestRe.Match(b)
}

// ternaryCond extracts the condition of `cond ? a : b` from the text that
// precedes a reference in one of its branches. elseBranch reports whether
// the reference is in the second branch.
func ternaryCond(before string) (cond string, elseBranch, ok bool) {
	q := strings.LastIndex(before, "?")
	if q < 0 {
		return "", false, false
	}
	elseBranch = strings.Contains(before[q:], ":")
	s := before[:q]
	// The condition starts after the nearest assignment, open paren, comma
	// or return keyword.
	start := 0
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c == '(' || c == ',' {
			start = i + 1
			break
		}
		if c == '=' && (i+1 >= len(s) || s[i+1] != '=') && (i == 0 || !strings.ContainsRune("=!<>", rune(s[i-1]))) {
			start = i + 1
			break
		}
	}
	cond = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[start:]), "return "))
	return cond, elseBranch, cond != ""
}

// ---- Python -----------------------------------------------------------------

var (
	// pyMarkRe also accepts `from pytest import mark` style (@mark.skip) and
	// an aliased import used as a decorator (import pytest as pt: @pt.mark.skip).
	pyMarkRe     = regexp.MustCompile(`(@?)(?:\bpytest\.|(?:^|[^\w.])@?|@[A-Za-z_]\w*\.)mark\.(skip|skipif|xfail)\b`)
	pyMarksArgRe = regexp.MustCompile(`\bmarks\s*=`)
	pyCallRe     = regexp.MustCompile(`\bpytest\.(skip|xfail)\s*\(`)
	pyUnittestRe = regexp.MustCompile(`@(?:unittest\.)?(skip|skipIf|skipUnless|expectedFailure)\b`)
	pySkipTestRe = regexp.MustCompile(`\b(?:self|cls)\.skipTest\s*\(`)
	pyRaiseRe    = regexp.MustCompile(`\braise\s+(?:unittest\.)?SkipTest\b`)
)

func (s lineScan) skipsPython() []skipHit {
	var hs []skipHit
	for _, m := range pyMarkRe.FindAllStringSubmatchIndex(s.vt, -1) {
		kind := s.vt[m[4]:m[5]]
		marker := strings.TrimLeft(s.vt[m[0]:m[1]], " \t([{,=:")
		decorator := strings.HasPrefix(marker, "@")
		h := skipHit{label: marker, form: formAttr, pos: m[0], end: m[1]}
		h.module = strings.Contains(s.vt[:m[0]], "pytestmark")
		if !strings.Contains(marker, "pytest.") && !decorator && !h.module {
			continue // mark.skip without pytest. is only trusted as a decorator
		}
		// A marker object that is neither a decorator, nor pytestmark, nor the
		// marks= of a pytest.param is applied programmatically.
		// Module-level aliases (requires_db = pytest.mark.skipif(...)) are
		// judged by their condition like decorators.
		h.programmatic = !decorator && !h.module && !pyMarksArgRe.MatchString(s.vt[:m[0]]) && indent(s.l.Raw) > 0
		as, end, called := s.call(m[1])
		if called {
			h.end = end
		}
		pos := positional(as)
		switch kind {
		case "skip":
		case "skipif":
			if called && len(as) == 0 {
				as = s.continuedArgs(pyMarkRe, m[1]) // @pytest.mark.skipif(\n    cond,\n    reason=...\n)
				pos = positional(as)
			}
			if c, ok := kwarg(as, "condition"); ok {
				h.cond = c.raw
			} else if len(pos) > 0 {
				h.cond = pos[0].raw
			} else {
				continue
			}
		case "xfail":
			if len(pos) > 0 && !isStr(pos[0]) {
				h.cond = pos[0].raw
			} else if c, ok := kwarg(as, "condition"); ok {
				h.cond = c.raw
			}
		}
		h.reason = s.reasonOf(as)
		hs = append(hs, h)
	}
	for _, m := range pyCallRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		hs = append(hs, skipHit{label: "pytest." + s.vt[m[2]:m[3]] + "()", form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
	}
	for _, m := range pyUnittestRe.FindAllStringSubmatchIndex(s.vt, -1) {
		kind := s.vt[m[2]:m[3]]
		h := skipHit{label: compact(s.vt[m[0]:m[1]]), form: formAttr, pos: m[0], end: m[1]}
		as, end, called := s.call(m[1])
		if called {
			h.end = end
		}
		switch kind {
		case "skipIf", "skipUnless":
			pos := positional(as)
			if len(pos) == 0 {
				continue
			}
			h.cond, h.inverse = pos[0].raw, kind == "skipUnless"
			h.reason = s.reasonOf(pos[1:])
		default:
			h.reason = s.reasonOf(as)
		}
		hs = append(hs, h)
	}
	for _, m := range pySkipTestRe.FindAllStringIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		hs = append(hs, skipHit{label: "self.skipTest()", form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
	}
	for _, m := range pyRaiseRe.FindAllStringIndex(s.vt, -1) {
		h := skipHit{label: "raise SkipTest", form: formCall, pos: m[0], end: m[1]}
		if as, end, ok := s.call(m[1]); ok {
			h.end, h.reason = end, s.reasonOf(as)
		}
		hs = append(hs, h)
	}
	return hs
}

// ---- Go -------------------------------------------------------------------------

var (
	goSkipRe = regexp.MustCompile(`(?:\b([A-Za-z_]\w*)|\bT\(\))\.(?:Skip|Skipf|SkipNow)\s*\(`)
	// goHandles are the names conventionally given to a *testing.T, B or F.
	goHandles = map[string]bool{"t": true, "b": true, "tb": true, "tt": true, "f": true}
	// goHandleDeclRe matches a parameter or variable of a testing type:
	// `st *testing.T`, `tb testing.TB`.
	goHandleDeclRe = regexp.MustCompile(`\b([A-Za-z_]\w*)\s+\*?testing\.(?:T|B|F|TB)\b`)
)

func (s lineScan) skipsGo() []skipHit {
	var hs []skipHit
	for _, m := range goSkipRe.FindAllStringSubmatchIndex(s.vt, -1) {
		if m[2] >= 0 {
			if recv := s.vt[m[2]:m[3]]; !goHandles[recv] && !s.goHandle(recv) {
				continue // iter.Skip(n), dec.Skip(): not a test handle
			}
		}
		as, end, _ := s.call(m[1] - 1)
		hs = append(hs, skipHit{label: compact(s.vt[m[0] : m[1]-1]), form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
	}
	return hs
}

// goHandle reports whether name is declared as a testing handle on this
// line or in the function around it: func TestX(test *testing.T), or a
// subtest's func(st *testing.T).
func (s lineScan) goHandle(name string) bool {
	for k := s.l.No; k >= 1 && k >= s.l.No-200; k-- {
		l := s.l.LexedLine
		if k != s.l.No {
			var ok bool
			if l, ok = s.v.line(s.f, k); !ok {
				return false
			}
		}
		for _, m := range goHandleDeclRe.FindAllStringSubmatch(l.Code, -1) {
			if m[1] == name {
				return true
			}
		}
		if strings.HasPrefix(l.Code, "func ") {
			return false // the top-level function that holds the call
		}
	}
	return false
}

// ---- Rust -------------------------------------------------------------------------

var (
	rustIgnoreRe  = regexp.MustCompile(`#\[\s*ignore\b(?:\s*=\s*("[^"]*"))?`)
	rustCfgAttrRe = regexp.MustCompile(`#\[\s*cfg_attr\s*\(`)
	rustCfgRe     = regexp.MustCompile(`#\[\s*cfg\s*\(`)
	// rustTestItemRe matches what a #[cfg(...)] above a test can switch
	// off: a test attribute, or a test module.
	rustTestItemRe = regexp.MustCompile(`#\[\s*(?:\w+::)?test\b|^\s*(?:pub\s+)?mod\s+\w*tests?\b`)
)

func (s lineScan) skipsRust() []skipHit {
	var hs []skipHit
	for _, m := range rustIgnoreRe.FindAllStringSubmatchIndex(s.vt, -1) {
		h := skipHit{label: "#[ignore]", form: formAttr, pos: m[0], end: m[1]}
		if m[2] >= 0 {
			h.reason, _ = s.str(newExpr(s.vt[m[2]:m[3]]))
		}
		hs = append(hs, h)
	}
	for _, m := range rustCfgAttrRe.FindAllStringIndex(s.vt, -1) {
		as, end, ok := s.call(m[1] - 1)
		if !ok || len(as) < 2 {
			continue
		}
		for _, a := range as[1:] {
			if a.raw == "ignore" || strings.HasPrefix(a.raw, "ignore ") || strings.HasPrefix(a.raw, "ignore=") {
				// #[cfg_attr(miri, ignore)]: compile-time configuration gates the
				// skip, unless the predicate is a constant: all() always holds.
				h := skipHit{label: "#[cfg_attr(" + display(as[0].raw) + ", ignore)]", form: formAttr, pos: m[0], end: end, cond: as[0].raw}
				if v, ok := cfgConstant(as[0].raw); !ok {
					h.envGate = true
				} else if !v {
					break // any(): the attribute never applies
				}
				hs = append(hs, h)
				break
			}
		}
	}
	for _, m := range rustCfgRe.FindAllStringIndex(s.vt, -1) {
		// #[cfg(any())] compiles the item out in every build: on a test it is
		// a skip that does not even show up as "ignored".
		as, end, ok := s.call(m[1] - 1)
		if !ok || len(as) != 1 {
			continue
		}
		if v, constant := cfgConstant(as[0].raw); constant && !v && s.cfgOnTest() {
			hs = append(hs, skipHit{label: "#[cfg(" + display(as[0].raw) + ")]", form: formAttr, pos: m[0], end: end,
				cond: as[0].raw, inverse: true})
		}
	}
	return hs
}

// cfgOnTest reports whether the attribute on the scanned line applies to a
// test or a test module: one follows within the next few lines.
func (s lineScan) cfgOnTest() bool {
	for k := s.l.No; k <= s.l.No+3; k++ {
		l := s.l.LexedLine
		if k != s.l.No {
			var ok bool
			if l, ok = s.v.line(s.f, k); !ok {
				return false
			}
		}
		if rustTestItemRe.MatchString(l.Code) {
			return true
		}
	}
	return false
}

// cfgConstant evaluates a Rust cfg predicate that does not depend on the
// build configuration: all() and true are true; any(), false and the
// "never set" idioms FALSE and never are false; not(...) of a constant is a
// constant.
func cfgConstant(pred string) (value, ok bool) {
	p := compact(pred)
	switch {
	case p == "all()" || p == "true":
		return true, true
	case p == "any()" || p == "false" || p == "FALSE" || p == "never":
		return false, true
	case strings.HasPrefix(p, "not(") && strings.HasSuffix(p, ")"):
		v, ok := cfgConstant(p[len("not(") : len(p)-1])
		return !v, ok
	}
	return false, false
}

// ---- Java / Kotlin / Groovy / Scala ----------------------------------------------

var (
	// Annotations may be written fully qualified: @org.junit.jupiter.api.Disabled.
	jvmDisabledRe = regexp.MustCompile(`@(?:[a-z]\w*\.)*(Disabled|Ignore|PendingFeature)\b`)
	jvmEnvGateRe  = regexp.MustCompile(`@(?:[a-z]\w*\.)*(Disabled(?:On|If|In|For)\w*|EnabledIf\w*|IgnoreIf)\b`)
	testNGRe      = regexp.MustCompile(`@(?:[a-z]\w*\.)*Test\s*\([^)]*\benabled\s*=\s*false\b`)
	kotestCfgRe   = regexp.MustCompile(`\.config\s*\([^)]*\benabled\s*=\s*false\b`)
	kotestXRe     = regexp.MustCompile(`(?:^|[^\w$.])(x(?:it|test|describe|context|should|given|when|then|feature|scenario|expect))\s*\(\s*"`)
	assumeRe      = regexp.MustCompile(`\b(?:Assumptions\.|Assume\.)?(assumeTrue|assumeFalse)\s*\(`)
	abortRe       = regexp.MustCompile(`\bAssumptions\.abort\s*\(`)
	scalaIgnoreRe = regexp.MustCompile(`^\s*ignore\s*\(\s*"|"\s*\.ignore\b|"\s+ignore\s*[({]`)
)

func (s lineScan) skipsJVM() []skipHit {
	var hs []skipHit
	for _, m := range jvmDisabledRe.FindAllStringSubmatchIndex(s.vt, -1) {
		name := s.vt[m[2]:m[3]]
		if name == "Ignore" && !annotatesTestOrClass(s.f, s.l, s.v) {
			continue // Room/Realm @Ignore on a field of a test entity
		}
		h := skipHit{label: "@" + name, form: formAttr, pos: m[0], end: m[1]}
		if as, end, ok := s.call(m[1]); ok {
			h.end, h.reason = end, s.reasonOf(as)
		}
		hs = append(hs, h)
	}
	for _, m := range jvmEnvGateRe.FindAllStringSubmatchIndex(s.vt, -1) {
		name := s.vt[m[2]:m[3]]
		h := skipHit{label: "@" + name, form: formAttr, pos: m[0], end: m[1]}
		as, end, ok := s.call(m[1])
		if ok {
			h.end = end
		}
		switch {
		case strings.HasPrefix(name, "DisabledOn"), strings.HasPrefix(name, "DisabledFor"), strings.HasPrefix(name, "DisabledIn"),
			strings.HasPrefix(name, "DisabledIfEnvironment"), strings.HasPrefix(name, "DisabledIfSystem"):
			h.envGate = true
		default:
			// @DisabledIf("method"), @EnabledIf..., Spock @IgnoreIf({ ... })
			h.cond, h.inverse = name, strings.HasPrefix(name, "Enabled")
			if len(as) > 0 {
				h.cond = as[0].raw
			}
			if strings.HasPrefix(name, "EnabledIfEnvironment") || strings.HasPrefix(name, "EnabledIfSystem") {
				h.envGate = true
			}
		}
		hs = append(hs, h)
	}
	if m := testNGRe.FindStringIndex(s.vt); m != nil {
		hs = append(hs, skipHit{label: "@Test(enabled = false)", form: formAttr, pos: m[0], end: m[1]})
	}
	if s.f.Lang == lang.Kotlin {
		if m := kotestCfgRe.FindStringIndex(s.vt); m != nil {
			hs = append(hs, skipHit{label: ".config(enabled = false)", form: formDecl, pos: m[0], end: m[1]})
		}
		for _, m := range kotestXRe.FindAllStringSubmatchIndex(s.vt, -1) {
			hs = append(hs, skipHit{label: s.vt[m[2]:m[3]], form: formDecl, pos: m[2], end: m[1]})
		}
		if name, ok := kotestPrefixed(s.f, s.l, "!"); ok {
			// kotest "bang": a test whose name starts with ! is disabled
			hs = append(hs, skipHit{label: `"!" name prefix`, kind: "kotest-bang", form: formDecl, name: testName{name: name, quoted: true}})
		}
	}
	if s.f.Lang == lang.Java || s.f.Lang == lang.Kotlin {
		for _, m := range assumeRe.FindAllStringSubmatchIndex(s.vt, -1) {
			as, end, ok := s.call(m[1] - 1)
			if !ok || len(as) == 0 {
				continue
			}
			fn := s.vt[m[2]:m[3]]
			hs = append(hs, skipHit{label: fn, form: formCall, pos: m[0], end: end, cond: as[0].raw, inverse: fn == "assumeTrue", reason: s.reasonOf(as[1:])})
		}
		if m := abortRe.FindStringIndex(s.vt); m != nil {
			as, end, _ := s.call(m[1] - 1)
			hs = append(hs, skipHit{label: "Assumptions.abort", form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
		}
	}
	if s.f.Lang == lang.Scala {
		if m := scalaIgnoreRe.FindStringIndex(s.vt); m != nil {
			hs = append(hs, skipHit{label: "ignore", form: formDecl, pos: m[0], end: m[1]})
		}
	}
	return hs
}

// kotestNameRe matches a kotest test or container name given as a string
// literal: test("name") / context("name") / should("name"), or a StringSpec
// `"name" {`. Group 1 is the name.
var kotestNameRe = regexp.MustCompile(`(?:(?:^|[^\w$.])(?:test|context|describe|it|should|feature|scenario|given|when|then|expect|xtest|and|Given|When|Then|And)\s*\(\s*|^\s*)"([^"]*)"\s*(?:\)|,|\{|\.config\b)`)

// kotestPrefixed returns the name of a kotest test declared on line l whose
// name starts with prefix: "!" disables a test ("bang"), "f:" focuses it.
// Only files that use kotest count: elsewhere a name is just a name.
func kotestPrefixed(f *core.FileInfo, l core.Line, prefix string) (string, bool) {
	if f.Lang != lang.Kotlin || !strings.Contains(l.NoComment, `"`+prefix) {
		return "", false
	}
	m := kotestNameRe.FindStringSubmatch(l.NoComment)
	if m == nil || !strings.HasPrefix(m[1], prefix) || !usesKotest(f) {
		return "", false
	}
	return m[1], true
}

// usesKotest reports whether a Kotlin file uses kotest, judged from its
// imports when full contents are available, and otherwise from what the
// hunks of a bare patch show: an import, a spec style or a kotest matcher.
func usesKotest(f *core.FileInfo) bool {
	const pkg = "io.kotest"
	if b, ok := f.NewContent(); ok {
		return bytes.Contains(b, []byte(pkg))
	}
	if b, ok := f.OldContent(); ok && bytes.Contains(b, []byte(pkg)) {
		return true
	}
	for _, h := range f.Diff.Hunks {
		for _, l := range h.Lines {
			if strings.Contains(l.Text, pkg) || kotestStyleRe.MatchString(l.Text) {
				return true
			}
		}
	}
	return false
}

// kotestStyleRe matches kotest spec styles and matchers.
var kotestStyleRe = regexp.MustCompile(`:\s*(?:FunSpec|StringSpec|DescribeSpec|BehaviorSpec|ShouldSpec|FreeSpec|WordSpec|FeatureSpec|ExpectSpec)\b|\s(?:shouldBe|shouldNotBe)\s`)

// ---- C# -------------------------------------------------------------------------

var (
	csFactSkipRe   = regexp.MustCompile(`\b(Fact|Theory|SkippableFact|SkippableTheory)\s*\([^\]]*?\bSkip\s*=\s*`)
	csFactOpenRe   = regexp.MustCompile(`\b(Fact|Theory|SkippableFact|SkippableTheory)\s*\(`)
	csSkipArgRe    = regexp.MustCompile(`(?:^|[\s,(])Skip\s*=\s*`)
	csIgnoreRe     = regexp.MustCompile(`(?:\[|,)\s*(?:\w+\.)*(Ignore|Explicit)\s*(?:\(|\]|,)`)
	csAssertSkipRe = regexp.MustCompile(`\bAssert\.(Ignore|Inconclusive|Skip)\s*\(`)
	csCondSkipRe   = regexp.MustCompile(`\b(Skip\.If|Skip\.IfNot|Assert\.SkipWhen|Assert\.SkipUnless)\s*\(`)
)

func (s lineScan) skipsCSharp() []skipHit {
	var hs []skipHit
	if m := csFactSkipRe.FindStringSubmatchIndex(s.vt); m != nil {
		h := skipHit{label: "[" + s.vt[m[2]:m[3]] + "(Skip = ...)]", form: formAttr, pos: m[0], end: m[1]}
		h.reason, _ = s.str(s.optionValue(m[1]))
		hs = append(hs, h)
	} else if m := csSkipArgRe.FindStringIndex(s.vt); m != nil {
		// [Fact(\n    DisplayName = "...",\n    Skip = "flaky")]: the attribute
		// opens on a line above.
		if attr := s.openAttribute(); attr != "" {
			h := skipHit{label: "[" + attr + "(Skip = ...)]", form: formAttr, pos: m[0], end: m[1]}
			h.reason, _ = s.str(s.optionValue(m[1]))
			hs = append(hs, h)
		}
	}
	for _, m := range csIgnoreRe.FindAllStringSubmatchIndex(s.vt, -1) {
		h := skipHit{label: "[" + s.vt[m[2]:m[3]] + "]", form: formAttr, pos: m[2], end: m[3]}
		if as, end, ok := s.call(m[3]); ok {
			h.end, h.reason = end, s.reasonOf(as)
		}
		hs = append(hs, h)
	}
	for _, m := range csAssertSkipRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		hs = append(hs, skipHit{label: "Assert." + s.vt[m[2]:m[3]], form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
	}
	for _, m := range csCondSkipRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, ok := s.call(m[1] - 1)
		if !ok || len(as) == 0 {
			continue
		}
		fn := s.vt[m[2]:m[3]]
		inverse := strings.HasSuffix(fn, "IfNot") || strings.HasSuffix(fn, "Unless")
		hs = append(hs, skipHit{label: fn, form: formCall, pos: m[0], end: end, cond: as[0].raw, inverse: inverse, reason: s.reasonOf(as[1:])})
	}
	return hs
}

// openAttribute returns the name of the xUnit attribute ([Fact( / [Theory()
// whose argument list is still open at the start of this line, or "".
func (s lineScan) openAttribute() string {
	for k := s.l.No - 1; k >= 1 && k >= s.l.No-4; k-- {
		prev, ok := s.v.line(s.f, k)
		if !ok {
			return ""
		}
		code := prev.Code
		if m := csFactOpenRe.FindStringSubmatchIndex(code); m != nil {
			if _, closed := closing(code, m[1]-1); !closed {
				return code[m[2]:m[3]]
			}
			return ""
		}
		if strings.Contains(code, "]") || strings.Contains(code, ";") || strings.Contains(code, "{") {
			return "" // the line ends an attribute or is code: not an argument list
		}
	}
	return ""
}

// ---- Ruby -----------------------------------------------------------------------

var (
	rbXRe        = regexp.MustCompile(`(?:^|[^\w.])(xit|xdescribe|xcontext|xspecify|xexample|xscenario|xfeature)\b\s*(?:\(|["'])`)
	rbSkipCallRe = regexp.MustCompile(`^\s*(skip|pending)\b(?:\s*\(|\s+["']|\s*$|\s+(?:if|unless)\b)`)
	rbMetaSymRe  = regexp.MustCompile(`,\s*:(skip|pending)\b`)
	rbMetaKeyRe  = regexp.MustCompile(`[,(]\s*(skip|pending):\s*`)
	rbDeclRe     = regexp.MustCompile(`^\s*(?:RSpec\.)?(?:it|specify|example|scenario|feature|describe|context|test)\b`)
)

func (s lineScan) skipsRuby() []skipHit {
	var hs []skipHit
	for _, m := range rbXRe.FindAllStringSubmatchIndex(s.vt, -1) {
		hs = append(hs, skipHit{label: s.vt[m[2]:m[3]], form: formDecl, pos: m[2], end: m[3]})
	}
	if m := rbSkipCallRe.FindStringSubmatchIndex(s.vt); m != nil {
		h := skipHit{label: s.vt[m[2]:m[3]], form: formCall, pos: m[2], end: m[3]}
		rest := strings.TrimSpace(s.vt[m[3]:])
		if as, end, ok := s.call(m[3]); ok {
			h.end, h.reason = end, s.reasonOf(as)
		} else if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `'`) {
			lit := splitTop(rest, ' ')[0]
			h.reason, _ = s.str(newExpr(lit))
			h.end = m[3] + strings.Index(s.vt[m[3]:], lit) + len(lit)
		}
		hs = append(hs, h)
	}
	if meta, ok := rbDeclArgs(s.vt); ok {
		// Positions below are in meta, the declaration's argument list.
		sub := s
		sub.vt = meta
		for _, m := range rbMetaSymRe.FindAllStringSubmatchIndex(meta, -1) {
			hs = append(hs, skipHit{label: ":" + meta[m[2]:m[3]], form: formDecl})
		}
		for _, m := range rbMetaKeyRe.FindAllStringSubmatchIndex(meta, -1) {
			h := skipHit{label: meta[m[2]:m[3]] + ":", kind: meta[m[2]:m[3]] + ":", form: formDecl}
			if h, ok := sub.optionHit(h, sub.optionValue(m[1])); ok {
				hs = append(hs, h)
			}
		}
	}
	return hs
}

// rbDeclArgs returns the argument list of the RSpec/minitest declaration
// on a line (value text), prefixed with "(": `it "name", skip: "why" do` →
// `( "name", skip: "why"`. Metadata such as :focus or retry: 3 is only read
// there, not in a one-line body: it { expect(page(skip: 10)).to be_ok }.
func rbDeclArgs(vt string) (string, bool) {
	m := rbDeclRe.FindStringIndex(vt)
	if m == nil {
		return "", false
	}
	i := m[1]
	for i < len(vt) && vt[i] == ' ' {
		i++
	}
	if i < len(vt) && vt[i] == '(' {
		if end, ok := closing(vt, i); ok {
			return vt[i:end], true
		}
		return vt[i:], true
	}
	depth := 0
	for j := i; j < len(vt); j++ {
		switch vt[j] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '{':
			if depth == 0 {
				return "(" + vt[i:j], true
			}
			depth++
		case '}':
			depth--
		case ' ':
			if depth == 0 && strings.HasPrefix(vt[j:], " do") && (j+3 == len(vt) || !isWordByte(vt[j+3])) {
				return "(" + vt[i:j], true
			}
		}
	}
	return "(" + vt[i:], true
}

// ---- PHP ------------------------------------------------------------------------

var (
	phpMarkRe  = regexp.MustCompile(`\bmarkTest(Skipped|Incomplete)\s*\(`)
	pestSkipRe = regexp.MustCompile(`->\s*(skip|skipOn\w+)\s*\(`)
)

func (s lineScan) skipsPHP() []skipHit {
	var hs []skipHit
	for _, m := range phpMarkRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		hs = append(hs, skipHit{label: "markTest" + s.vt[m[2]:m[3]], form: formCall, pos: m[0], end: end, reason: s.reasonOf(as)})
	}
	for _, m := range pestSkipRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		fn := s.vt[m[2]:m[3]]
		h := skipHit{label: "->" + fn + "()", form: formDecl, pos: m[0], end: end}
		switch {
		case fn != "skip":
			h.envGate = true // ->skipOnWindows(), ->skipOnCi(), ...
		case len(as) == 0 || isStr(as[0]):
			h.reason = s.reasonOf(as)
		default:
			h.cond, h.reason = as[0].raw, s.reasonOf(as[1:])
		}
		hs = append(hs, h)
	}
	return hs
}

// ---- Swift ----------------------------------------------------------------------

var (
	xctSkipRe    = regexp.MustCompile(`\bXCTSkip(If|Unless)?\s*\(`)
	swiftTraitRe = regexp.MustCompile(`@(?:Test|Suite)\b.*?\.(disabled|enabled)\s*\(`)
)

func (s lineScan) skipsSwift() []skipHit {
	var hs []skipHit
	for _, m := range xctSkipRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		variant := ""
		if m[2] >= 0 {
			variant = s.vt[m[2]:m[3]]
		}
		h := skipHit{label: "XCTSkip" + variant, form: formCall, pos: m[0], end: end}
		if m[2] >= 0 {
			if len(as) == 0 {
				continue
			}
			h.cond, h.inverse, h.reason = as[0].raw, variant == "Unless", s.reasonOf(as[1:])
		} else {
			h.reason = s.reasonOf(as)
		}
		hs = append(hs, h)
	}
	for _, m := range swiftTraitRe.FindAllStringSubmatchIndex(s.vt, -1) {
		as, end, _ := s.call(m[1] - 1)
		trait := s.vt[m[2]:m[3]]
		h := skipHit{label: "." + trait + "()", form: formAttr, pos: m[2], end: end}
		if c, ok := kwarg(as, "if"); ok {
			h.cond, h.inverse = c.raw, trait == "enabled"
		} else if trait == "enabled" {
			continue
		}
		h.reason = s.reasonOf(as)
		hs = append(hs, h)
	}
	return hs
}

// ---- Dart -------------------------------------------------------------------------

var (
	dartDeclRe  = regexp.MustCompile(`\b(?:test|group|testWidgets|testGoldens|blocTest)\s*(?:<[^>]*>)?\s*\(`)
	dartCloseRe = regexp.MustCompile(`^\s*\}\s*,`)
)

// dartTestOptions returns the named arguments that a line passes to a test
// or group: `test('a', () => ..., skip: true);` on one line, or
// `}, skip: 'flaky');` after the test's callback. Named arguments of calls
// inside the test body (repo.list(skip: 2)) are not options of the test.
func dartTestOptions(vt string) map[string]expr {
	var as []expr
	for _, m := range dartDeclRe.FindAllStringIndex(vt, -1) {
		as = append(as, looseArgs(vt, m[1]-1)...)
	}
	if m := dartCloseRe.FindStringIndex(vt); m != nil {
		as = append(as, looseArgs("("+vt[m[1]:], 0)...)
	}
	opts := map[string]expr{}
	for _, a := range as {
		if name, v, ok := keyword(a); ok {
			opts[name] = v
		}
	}
	return opts
}

func (s lineScan) skipsDart() []skipHit {
	v, ok := dartTestOptions(s.vt)["skip"]
	if !ok {
		return nil
	}
	h := skipHit{label: "skip: " + display(v.raw), kind: "skip:", form: formDecl}
	h, ok = s.optionHit(h, v)
	if !ok {
		return nil
	}
	if h.cond != "" || h.reason != "" {
		h.label = "skip:"
	}
	return []skipHit{h}
}

// ---- Elixir ---------------------------------------------------------------------

var exTagSkipRe = regexp.MustCompile(`@(tag|moduletag|describetag)\s+(?::skip\b|skip:\s*)`)

func (s lineScan) skipsElixir() []skipHit {
	m := exTagSkipRe.FindStringSubmatchIndex(s.vt)
	if m == nil {
		return nil
	}
	tag := s.vt[m[2]:m[3]]
	h := skipHit{label: "@" + tag + " :skip", form: formAttr, pos: m[0], end: m[1], module: tag == "moduletag"}
	if strings.HasSuffix(strings.TrimSpace(s.vt[m[0]:m[1]]), ":") {
		h.reason, _ = s.str(s.optionValue(m[1]))
	}
	return []skipHit{h}
}

// ---- GoogleTest -----------------------------------------------------------------

var (
	gtestDisabledRe = regexp.MustCompile(`\b(?:TEST|TEST_F|TEST_P|TYPED_TEST|TYPED_TEST_P)\s*\(\s*(?:DISABLED_\w+\s*,|\w+\s*,\s*DISABLED_\w+)`)
	gtestSkipRe     = regexp.MustCompile(`\bGTEST_SKIP\s*\(\s*\)`)
)

func (s lineScan) skipsGTest() []skipHit {
	var hs []skipHit
	if m := gtestDisabledRe.FindStringIndex(s.vt); m != nil {
		hs = append(hs, skipHit{label: "DISABLED_ prefix", form: formDecl, pos: m[0], end: m[1]})
	}
	if m := gtestSkipRe.FindStringIndex(s.vt); m != nil {
		hs = append(hs, skipHit{label: "GTEST_SKIP()", form: formCall, pos: m[0], end: m[1]})
	}
	return hs
}
