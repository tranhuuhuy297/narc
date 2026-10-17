package tests

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// Capability gates. A skip that applies only where the machine lacks what
// the test exercises (permission bits that bind, inode numbers, symlinks, a
// tool on PATH, the race detector, a POSIX platform) keeps the test running
// wherever that capability exists, which is where CI runs it. Porting a
// suite to Windows or to a root container adds many such gates to existing
// tests; they are notes, not weakenings. Gates that switch a test off by
// default (an opt-in environment variable, CI itself, -short, a missing
// build fixture) stay warnings.

var (
	// capWords are identifier parts that name a capability of the machine.
	// Generic words (available, supported, permission) are left out: on a
	// value the test computed (`!result.available`) they describe the code
	// under test. They count only in a named flag (capFlagRe).
	capWords = map[string]bool{
		"windows": true, "win32": true, "darwin": true, "macos": true, "osx": true, "wasm": true, "miri": true,
		"docker": true, "podman": true, "gpu": true, "cuda": true, "race": true, "cgo": true,
		"inode": true, "ino": true, "symlink": true, "symlinks": true, "hardlink": true, "hardlinks": true,
		"posix": true, "unix": true, "uid": true, "euid": true, "getuid": true, "geteuid": true, "privileged": true,
		"elevated": true, "tty": true, "isatty": true, "chmod": true, "webkit": true, "firefox": true,
		"chromium": true, "safari": true,
	}
	// gateWords are identifier parts that make a gate a switch rather than
	// a capability: the test runs only when someone opts in, or not in CI.
	gateWords = map[string]bool{
		"ci": true, "integration": true, "short": true, "slow": true, "env": true, "environ": true,
		"environment": true, "getenv": true, "lookupenv": true, "headless": true, "mobile": true, "linux": true,
		"bazel": true, "nightly": true, "e2e": true, "live": true, "skip": true, "flaky": true, "broken": true,
		"todo": true, "fixme": true, "known": true, "issue": true, "bug": true,
		// a fixture or build output that must exist: a CI job that forgets to
		// build it passes without running the test
		"exists": true, "exist": true, "build": true, "fixture": true, "fixtures": true,
	}
	// capProbeRe matches APIs that probe for a tool or module.
	// capProbeRe matches APIs that probe for a tool or module, and checks of
	// a tool's output for a flag or command it does not know (an older
	// version of an external CLI).
	capProbeRe = regexp.MustCompile(`\bexec\.LookPath\(|\bshutil\.which\(|\bfind_spec\(|\bextension_loaded\(|\bfunction_exists\(|#available\b|\bcommandExists\(|\bwhich\.sync\(|(?i:unknown (?:flag|option|command|subcommand)|unrecognized (?:option|argument|arguments|command|flag)|command not found|no such (?:command|option|subcommand))`)
	// errProbeRe matches a statement whose error says that the machine
	// lacks a capability: a tool not on PATH, symlinks or hard links that
	// cannot be created, a loopback address that cannot be bound.
	errProbeRe = regexp.MustCompile(`\berr\w*\s*:?=.*(?:\bexec\.LookPath\(|\bos\.(?:Symlink|Link|Chmod|Lchown)\(|\bnet\.Listen\w*\(|\bsyscall\.\w+\(|\bunix\.\w+\()`)
	// capFlagRe matches a capability flag or probe: hasJq,
	// canDropPermissions(), arePermissionBitsEnforced(), symlinksSupported.
	capFlagRe = regexp.MustCompile(`^!?\s*(?:(?:has|can|supports?)[A-Z]\w*|\w+(?:Available|Supported|Installed|Enforced|Present|Verifiable)|[A-Z][A-Z0-9_]*_(?:AVAILABLE|SUPPORTED|INSTALLED|PRESENT))(?:\(\))?$`)
	// linuxKeptRe matches a platform check that keeps a test on Linux,
	// where CI usually runs: skip when not Linux, or not Linux/macOS.
	linuxKeptRe = regexp.MustCompile(`^(?:process\.platform|runtime\.GOOS|sys\.platform|os\.platform\(\)|platform\.system\(\)|Deno\.build\.os)\s*(!==|!=)\s*["'](?:linux|Linux)["']$|^(?:!|not\s+)\s*\[[^\]]*["']linux["'][^\]]*\]\.includes\(\s*(?:process\.platform|os\.platform\(\))\s*\)$|^not\s+sys\.platform\.startswith\(\s*["']linux["']\s*\)$|^sys\.platform\s+not\s+in\s+\([^)]*["']linux["'][^)]*\)$`)
	// linuxOnlyRe is linuxKeptRe for a condition that enables the test
	// (runIf, skipUnless): run only on Linux, or on Linux and macOS.
	linuxOnlyRe = regexp.MustCompile(`^(?:process\.platform|runtime\.GOOS|sys\.platform|os\.platform\(\)|platform\.system\(\)|Deno\.build\.os)\s*(===|==)\s*["'](?:linux|Linux)["']$|^\[[^\]]*["']linux["'][^\]]*\]\.includes\(\s*(?:process\.platform|os\.platform\(\))\s*\)$|^sys\.platform\.startswith\(\s*["']linux["']\s*\)$`)
	// flagIdentRe matches a bare identifier, possibly negated.
	flagIdentRe = regexp.MustCompile(`^(!|not\s+)?\s*([A-Za-z_$][\w$]*)$`)
)

// definer returns the expression an identifier is defined as in the file,
// if a simple definition can be found.
type definer func(name string) (string, bool)

// capabilityGate reports whether a skip condition (value text) only skips
// where the machine lacks a capability the test needs. inverse says that
// cond enables the test (runIf, skipUnless). def, when set, resolves a
// bare flag (`!replayable`) to its definition, one level deep.
func capabilityGate(cond string, inverse bool, def definer) bool {
	if strings.TrimSpace(cond) == "" {
		return false
	}
	return offPlatformGate(cond, inverse) || capabilityGateDepth(display(cond), inverse, def, 1)
}

func capabilityPart(p string, inverse bool, def definer, depth int) bool {
	if p == "" {
		return false
	}
	if m := flagIdentRe.FindStringSubmatch(p); m != nil && def != nil && depth > 0 && !capFlagRe.MatchString(p) {
		// A flag defined in the file: judge its definition. `!flag` with
		// skipIf enables the test where the definition holds.
		if d, ok := def(m[2]); ok {
			neg := m[1] != ""
			return capabilityGateDepth(d, inverse != neg, def, depth-1)
		}
	}
	if inverse && linuxOnlyRe.MatchString(p) || !inverse && linuxKeptRe.MatchString(p) {
		return true
	}
	if !inverse && rootCheckRe.MatchString(p) {
		return true
	}
	if capProbeRe.MatchString(p) || capFlagRe.MatchString(p) {
		return true
	}
	if offPlatformGate(p, inverse) {
		return true
	}
	capability := false
	for _, w := range words(p) {
		if gateWords[w] {
			return false
		}
		capability = capability || capWords[w]
	}
	return capability
}

// capabilityGateDepth is capabilityGate on a displayed condition, with
// depth levels of flag resolution left.
func capabilityGateDepth(c string, inverse bool, def definer, depth int) bool {
	c = trimParens(strings.TrimSpace(c))
	if c == "" {
		return false
	}
	ors, word := "||", "or"
	if inverse {
		ors, word = "&&", "and" // runIf(a && b) skips when either fails
	}
	parts := splitOp(c, ors)
	if len(parts) == 1 {
		parts = splitOp(c, " "+word+" ")
	}
	for _, p := range parts {
		if !capabilityPart(trimParens(strings.TrimSpace(p)), inverse, def, depth) {
			return false
		}
	}
	return true
}

// fileDefiner resolves identifiers to their definitions in the new
// version of f: `const replayable = process.platform !== 'win32' && hasJq;`
// (JavaScript, Kotlin), `posix := runtime.GOOS != "windows"` or
// `var posix = ...` (Go), `POSIX = os.name == "posix"` (Python). Only
// definitions on a single line count.
func fileDefiner(f *core.FileInfo) definer {
	return func(name string) (string, bool) {
		re, err := regexp.Compile(`^\s*(?:export\s+)?(?:(?:const|let|var|val|final|static)\s+)*` + regexp.QuoteMeta(name) +
			`\s*(?::\s*[\w.<>\[\] |]+)?\s*:?=\s*(.+?)\s*;?\s*$`)
		if err != nil {
			return "", false
		}
		last := lastLine(f, newVersion)
		for k := 1; k <= last; k++ {
			l, ok := f.NewLine(k)
			if !ok || !strings.Contains(l.Code, name) {
				continue
			}
			vt, _ := valueText(core.Line{LexedLine: l, No: k}, f.Lang)
			if m := re.FindStringSubmatch(vt); m != nil {
				rhs := strings.TrimSpace(m[1])
				if rhs == "" || strings.HasSuffix(rhs, "(") || strings.HasSuffix(rhs, "{") || strings.HasPrefix(rhs, "=") {
					return "", false // a multi-line definition or a comparison
				}
				return display(rhs), true
			}
		}
		return "", false
	}
}

// ---- early returns ----------------------------------------------------------

var (
	// bareReturnRe matches a return statement without a value (or with
	// nil/None/undefined): the end of a test that gave up early.
	bareReturnRe = regexp.MustCompile(`^\s*return\s*(?:nil|None|null|undefined|\(\))?\s*;?\s*\}?\s*$`)
	// inlineReturnRe matches a one-line guard that returns early:
	// `if (process.platform === 'win32') return;`, `if cond { return }`.
	inlineReturnRe = regexp.MustCompile(`^\s*(?:\}\s*else\s+)?if\b.*[)\s{:]\s*return\s*(?:nil|None|null|undefined)?\s*;?\s*\}?\s*$`)
)

// returnedEarly reports whether the old version of the test name returned
// early under a condition near the top of its body, as tests did before
// they learnt to skip: `if (process.platform === 'win32') return;`. A
// conditional skip that replaces such a return only makes visible a test
// that already did not run there.
func returnedEarly(f *core.FileInfo, name testName) bool {
	if !name.ok() || f.OldPath == "" {
		return false
	}
	get := oldVersion.getter(f)
	last := lastLine(f, oldVersion)
	for k := 1; k <= last; k++ {
		l, ok := get(k)
		if !ok || !strings.Contains(l.NoComment, name.name) {
			continue
		}
		if declName(l).name != name.name && !(name.quoted && hasString(l, name.name)) {
			continue
		}
		if earlyReturnIn(get, f.Lang, k, last) {
			return true
		}
	}
	return false
}

func hasString(l lang.LexedLine, s string) bool {
	for _, x := range l.Strings {
		if x == s {
			return true
		}
	}
	return false
}

// earlyReturnIn looks for a conditional bare return among the first
// statements of the test declared on line decl.
func earlyReturnIn(get lineGetter, lg lang.Lang, decl, last int) bool {
	head, ok := get(decl)
	if !ok {
		return false
	}
	ind := indent(head.Raw)
	code := 0
	for k := decl + 1; k <= last && k <= decl+40 && code < 14; k++ {
		l, ok := get(k)
		if !ok {
			return false
		}
		c := strings.TrimSpace(l.Code)
		if c == "" {
			continue
		}
		li := indent(l.Raw)
		if li < ind || li == ind && closesBlock(l.Code) && !strings.HasSuffix(c, "{") {
			return false // the end of the test
		}
		code++
		if inlineReturnRe.MatchString(l.Code) {
			return true
		}
		if bareReturnRe.MatchString(l.Code) {
			vt, _ := valueText(core.Line{LexedLine: l, No: k}, lg)
			if _, ok := guardIn(get, lg, core.Line{LexedLine: l, No: k}, vt, 0, 0); ok {
				return true
			}
		}
	}
	return false
}

// ---- error probes -----------------------------------------------------------

// errCondRe matches a guard that only tests an error variable.
var errCondRe = regexp.MustCompile(`^(?:\w+\s*!=\s*nil|err\w*|\w*[eE]rr(?:or)?\s*!==?\s*(?:null|undefined))$`)

// errProbed reports whether the error that the guard of the skip on new
// line n tests comes from a capability probe a few lines above:
//
//	_, err := exec.LookPath("sh")
//	if err != nil {
//		t.Skip("sh not installed")
//	}
func errProbed(f *core.FileInfo, n int, cond string) bool {
	if !errCondRe.MatchString(strings.TrimSpace(display(cond))) {
		return false
	}
	seen := 0
	for k := n - 1; k >= 1 && seen < 6; k-- {
		l, ok := f.NewLine(k)
		if !ok {
			return false
		}
		if strings.TrimSpace(l.Code) == "" {
			continue
		}
		seen++
		if errProbeRe.MatchString(l.Code) {
			return true
		}
	}
	return false
}

// ---- prerequisites ----------------------------------------------------------

var (
	condIdentRe = regexp.MustCompile(`(?:^|[^\w$.])([A-Za-z_$][\w$]*)\b(\s*\()?`)
	// condKeywords are identifiers in conditions that name no variable.
	condKeywords = map[string]bool{
		"nil": true, "null": true, "undefined": true, "None": true, "true": true, "false": true, "True": true,
		"False": true, "len": true, "not": true, "and": true, "or": true, "is": true, "in": true, "typeof": true,
		"instanceof": true, "this": true, "self": true, "new": true, "void": true, "string": true, "int": true,
	}
)

// preparedOutside reports whether the condition of the skip on new line n
// (in the test declared on line decl) only reads state that the test does
// not compute itself: a value another test or a fixture prepared
// (`if baselineStdout == nil`, `test.skip(!step3ConversationId, ...)`).
// Such a skip guards a prerequisite, unlike `if result == nil { t.Skip() }`
// on the result of the code under test.
func preparedOutside(f *core.FileInfo, decl, n int, cond string) bool {
	if decl == 0 || decl >= n {
		return false
	}
	c := display(cond)
	var names []string
	for _, m := range condIdentRe.FindAllStringSubmatch(c, -1) {
		if m[2] != "" {
			return false // a call: it may run the code under test
		}
		if !condKeywords[m[1]] {
			names = append(names, m[1])
		}
	}
	if len(names) == 0 {
		return false
	}
	for k := decl; k < n; k++ {
		l, ok := f.NewLine(k)
		if !ok {
			return false
		}
		for _, name := range names {
			if strings.Contains(l.Code, name) && assigns(l.Code, name) {
				return false
			}
		}
	}
	return true
}

// assigns reports whether a line of code assigns to or declares name, or
// takes it as a parameter (the test's own arguments).
func assigns(code, name string) bool {
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`(?:^|[^\w$.])` + q + `\s*(?::=|=[^=]|\+=|-=)|(?:const|let|var|val)\s+[^=;]*\b` + q + `\b|,\s*` + q + `\s*(?::=|=[^=])|\b` + q + `\s*,[^=]*:?=[^=]|[({,]\s*` + q + `\s*(?:[,):]|\s+\*?\w)`)
	return re.MatchString(code)
}

// ---- unchanged skips --------------------------------------------------------

// skippedBefore reports whether the old version of the test name already
// held the very statement on new line l: the diff of a test that moved past
// a new test sharing its first lines can pair the old skip with the new
// test and show the moved test's skip as added.
func skippedBefore(o occurrence[skipHit], name testName) bool {
	f, l := o.File, o.Line
	if !name.ok() || f.OldPath == "" {
		return false
	}
	key := lineKey(l)
	gate := rulekit.Normalize(skipGate(o, newVersion))
	get := oldVersion.getter(f)
	last := lastLine(f, oldVersion)
	for k := 1; k <= last; k++ {
		d, ok := get(k)
		if !ok || !strings.Contains(d.NoComment, name.name) || declName(d).name != name.name {
			continue
		}
		ind := indent(d.Raw)
		for j := k + 1; j <= last && j <= k+80; j++ {
			b, ok := get(j)
			if !ok {
				break
			}
			if strings.TrimSpace(b.Code) == "" {
				continue
			}
			if indent(b.Raw) <= ind && !closesBlock(b.Code) {
				break // the next declaration
			}
			if indent(b.Raw) <= ind && closesBlock(b.Code) && declName(b).ok() {
				break
			}
			if rulekit.Normalize(b.NoComment) != key {
				continue
			}
			// The same statement under the same condition: a guard that was
			// dropped (`if testing.Short() { t.Skip() }` → `t.Skip()`) makes
			// it a new skip.
			for _, h := range detectSkips(f, core.Line{LexedLine: b, No: j}, oldVersion) {
				if h.Val.label == o.Val.label && rulekit.Normalize(skipGate(h, oldVersion)) == gate {
					return true
				}
			}
		}
	}
	return false
}
