package tests

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// ---- test names -------------------------------------------------------------

// testName names the test a finding is about.
type testName struct {
	name   string
	quoted bool // a display name ("rejects expired tokens"), not an identifier
	suite  bool // a describe/context/class rather than a single test
}

func (n testName) String() string {
	if n.quoted {
		return `"` + n.name + `"`
	}
	return n.name
}

func (n testName) ok() bool { return n.name != "" }

// declRes recognize test (and suite) declarations, matched against
// NoComment so that display names are available.
var declRes = []struct {
	re     *regexp.Regexp
	quoted bool
}{
	{regexp.MustCompile(`\bfunc\s+(?:\([^)]*\)\s*)?((?:Test|Benchmark|Fuzz|Example)\w*)\s*\(`), false},                                                                         // Go (also suite methods)
	{regexp.MustCompile(`\b[tbs]\.Run\(\s*"([^"]+)"`), true},                                                                                                                   // Go subtests
	{regexp.MustCompile(`^\s*(?:async\s+|focus\s+)?def\s+(\w+)`), false},                                                                                                       // Python, Ruby
	{regexp.MustCompile(`^\s*(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`), false},                                                                                                      // Rust
	{regexp.MustCompile("\\bfun\\s+(?:`([^`]+)`|(\\w+))\\s*\\("), false},                                                                                                       // Kotlin
	{regexp.MustCompile(`^\s*(?:@\w+\s+)*(?:(?:public|private|internal)\s+)?func\s+(\w+)\s*\(`), false},                                                                        // Swift
	{regexp.MustCompile(`\bfunction\s+(\w+)\s*\(`), false},                                                                                                                     // PHP, JS
	{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|override|virtual|open|async)\s+)*(?:void|Task|ValueTask|async\s+Task)\s+(\w+)\s*\(`), false}, // Java, C#
	// Java, C#, TypeScript: other methods (helpers), with at least one modifier
	{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|synchronized|abstract|override)\s+)+(?:<[^<>()]*>\s+)?[\w.]+(?:<[^()]*>)?(?:\[\])*\??\s+(\w+)\s*\(`), false},
	{regexp.MustCompile(`^\s*(?:public\s+|internal\s+|private\s+)?(?:(?:final|abstract|sealed|data|open)\s+)?(?:class|object)\s+(\w+)`), false},
	{regexp.MustCompile(`\b(?:TEST|TEST_F|TEST_P|TYPED_TEST)\s*\(\s*(\w+\s*,\s*\w+)`), false},                                                                                                                                   // GoogleTest
	{regexp.MustCompile("(?:^|[^\\w$.])(?:[xf]?(?:it|test|describe|context|specify|suite|scenario|feature|example|should)|focus|testWidgets|group|test\\.describe|QUnit)(?:\\.\\w+)*\\s*\\(?\\s*[\"'`]([^\"'`]+)[\"'`]"), true}, // JS, Ruby, Elixir, Dart, kotest
}

// declName returns the test declared on a line, if any.
func declName(l lang.LexedLine) testName {
	for _, d := range declRes {
		m := d.re.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		for _, g := range m[1:] {
			if g == "" {
				continue
			}
			if !d.quoted {
				// GoogleTest "Suite, Name" → "Suite.Name"
				g = strings.Join(strings.Fields(strings.ReplaceAll(g, ",", ".")), "")
			}
			return testName{name: g, quoted: d.quoted, suite: suiteDeclRe.MatchString(l.Code)}
		}
	}
	return testName{}
}

// testAfter returns the first test declared after the annotation on new
// line n (decorators, attributes and blank lines in between are skipped).
func testAfter(f *core.FileInfo, n int) testName {
	return testAfterIn(f.NewLine, n)
}

// testAfterIn is testAfter on the version of a file that get reads.
func testAfterIn(get lineGetter, n int) testName {
	for k := n; k <= n+10; k++ {
		l, ok := get(k)
		if !ok {
			break
		}
		if t := declName(l); t.ok() {
			return t
		}
	}
	return testName{}
}

// suitesAround returns the declarations that enclose line n, innermost
// first: each one is the nearest declaration above at a smaller
// indentation than the one before it.
func suitesAround(get lineGetter, n int) []string {
	cur, ok := get(n)
	if !ok {
		return nil
	}
	var out []string
	ind := indent(cur.Raw)
	for k := n - 1; k >= 1 && k >= n-1000 && ind > 0; k-- {
		l, ok := get(k)
		if !ok {
			break
		}
		if punctuationOnly(l.Code) || startsInString(l.Code) {
			continue
		}
		if li := indent(l.Raw); li < ind {
			if t := declName(l); t.ok() {
				out = append(out, t.name)
			}
			ind = headerIndent(l, li)
		}
	}
	return out
}

// markerScope builds the scope of a marker (see scopeFunc): the test it
// applies to, then the suites around the marker's line.
func markerScope(get lineGetter, n int, own testName) []string {
	path := []string{own.name}
	for _, s := range suitesAround(get, n) {
		if s != path[len(path)-1] {
			path = append(path, s)
		}
	}
	return path
}

// testAround returns the nearest test declared above new line n with a
// smaller indentation: the test whose body contains the line.
func testAround(f *core.FileInfo, n int) testName {
	return testAbove(f, n, false)
}

// testClosedBy returns the test whose declaration the closing line n ends,
// e.g. Dart's `}, skip: true);`: the nearest declaration above at the same
// or a smaller indentation.
func testClosedBy(f *core.FileInfo, n int) testName {
	return testAbove(f, n, true)
}

func testAbove(f *core.FileInfo, n int, sameLevel bool) testName {
	return testAboveIn(f.NewLine, n, sameLevel)
}

// lineGetter reads a line of one version of a file.
type lineGetter func(n int) (lang.LexedLine, bool)

// testAboveIn is testAbove on the version of a file that get reads.
func testAboveIn(get lineGetter, n int, sameLevel bool) testName {
	cur, ok := get(n)
	if !ok || sameLevel && !closesBlock(cur.Code) {
		return testName{}
	}
	ind := indent(cur.Raw)
	for k := n - 1; k >= 1 && k >= n-120; k-- {
		l, ok := get(k)
		if !ok {
			break
		}
		if punctuationOnly(l.Code) || startsInString(l.Code) {
			continue // blank, a lone "{" in brace-on-its-own-line styles, or string content
		}
		li := indent(l.Raw)
		if li > ind || li == ind && !sameLevel {
			continue
		}
		if t := declName(l); t.ok() {
			return t
		}
		if sameLevel {
			return testName{}
		}
		ind = headerIndent(l, li)
	}
	return testName{}
}

// headerIndent is the indentation below which the scans above look for an
// enclosing declaration after reaching line l at indentation li. A line
// that closes a bracket and opens a block, `}) => {` or `) {`, ends a
// declaration header that a formatter split over several lines
// (`test("name", async ({` / `page,` / `}) => {`): the declaration starts
// above at the same indentation, so that indentation still qualifies.
func headerIndent(l lang.LexedLine, li int) int {
	c := strings.TrimSpace(l.Code)
	if closesBlock(c) && (strings.HasSuffix(c, "{") || strings.HasSuffix(c, ":")) && !strings.Contains(c, "else") {
		return li + 1
	}
	return li
}

// closesBlock reports whether a line starts by closing a bracket, as the
// last line of a declaration does (`}, skip: true);`, `})->only();`). Only
// such a line can belong to the declaration above it at the same
// indentation: any other line there is a sibling statement, e.g. the next
// `it.skipIf(cond)(` after the previous test's `});`.
func closesBlock(code string) bool {
	c := strings.TrimSpace(code)
	return c != "" && strings.ContainsRune("})]", rune(c[0]))
}

// startsInString reports whether a line starts with a string delimiter:
// the end of a multi-line string (a Go raw string or template literal
// whose content sits at column 0) or a string argument on a line of its
// own. Its indentation says nothing about the block structure.
func startsInString(code string) bool {
	c := strings.TrimSpace(code)
	return c != "" && strings.ContainsRune("\"'`", rune(c[0]))
}

func punctuationOnly(s string) bool {
	return strings.Trim(s, " \t{}()[];,") == ""
}

// lineAt returns new line n, or an empty line.
func lineAt(f *core.FileInfo, n int) lang.LexedLine {
	l, _ := f.NewLine(n)
	return l
}

// indent measures leading whitespace (a tab counts as four spaces).
func indent(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// ---- annotation targets -----------------------------------------------------

var (
	// jvmAnnotationRe matches one annotation, with simple arguments.
	jvmAnnotationRe = regexp.MustCompile(`@[\w.]+(?:\s*\([^()]*(?:\([^()]*\)[^()]*)*\))?`)
	// jvmTestMarkRe matches what marks a JVM declaration as a test or a test
	// class: JUnit/TestNG annotations, Spock features, naming conventions.
	jvmTestMarkRe = regexp.MustCompile("@(?:[a-z]\\w*\\.)*(?:Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate|Property|RetryingTest|RepeatedIfExceptionsTest)\\b|\\bdef\\s+[\"']|\\bclass\\s+\\w*(?:Test|Tests|Spec|IT|Suite)\\b|\\b(?:void|fun)\\s+`?test")
	// jvmFieldRe matches a property or field declaration.
	jvmFieldRe = regexp.MustCompile(`^\s*(?:(?:private|public|protected|internal|lateinit|override|open|final|static|transient)\s+)*(?:val|var)\b|;\s*$`)
)

// jvmTarget returns the lines from the annotation on line l down to the
// declaration it annotates (at most a few lines below, in version v), and
// that declaration with its annotations removed.
func jvmTarget(f *core.FileInfo, l core.Line, v version) (lines []string, decl string) {
	for k := l.No; k <= l.No+6; k++ {
		ll := l.LexedLine
		if k != l.No {
			var ok bool
			if ll, ok = v.line(f, k); !ok {
				break
			}
		}
		lines = append(lines, ll.Code)
		if rest := strings.TrimSpace(jvmAnnotationRe.ReplaceAllString(ll.Code, "")); rest != "" {
			return lines, rest
		}
	}
	return lines, ""
}

// annotatesTestOrClass reports whether the annotation on line l applies to
// a method or a class rather than to a field (Room and Realm have an @Ignore
// for fields, unrelated to JUnit's).
func annotatesTestOrClass(f *core.FileInfo, l core.Line, v version) bool {
	_, decl := jvmTarget(f, l, v)
	return decl == "" || !jvmFieldRe.MatchString(decl)
}

// annotatesTest reports whether the annotation on line l applies to a test
// or a test class, judging by the annotations and declaration that follow.
func annotatesTest(f *core.FileInfo, l core.Line, v version) bool {
	lines, _ := jvmTarget(f, l, v)
	for _, s := range lines {
		if jvmTestMarkRe.MatchString(s) {
			return true
		}
	}
	return false
}

// ---- guard conditions -------------------------------------------------------

var (
	guardRe      = regexp.MustCompile(`^\s*(?:\}\s*)?(?:else\s+)?(?:if|elif|elsif|unless|guard|when)\b\s*(.*)$`)
	switchRe     = regexp.MustCompile(`^\s*(?:switch|select)\b\s*(.*)$`)
	inlineIfRe   = regexp.MustCompile(`\b(?:if|unless|guard|elif)\b\s*(.*)$`)
	postfixIfRe  = regexp.MustCompile(`\b(?:if|unless)\b\s*(.+)$`)
	caseRe       = regexp.MustCompile(`^\s*case\b\s*(.+?)\s*:?\s*$`)
	branchRe     = regexp.MustCompile(`^\s*(?:\}\s*)?(?:else\s*(?:\{|:)?\s*$|case\b|default\s*:)`)
	condTailTrim = regexp.MustCompile(`(?:\{|:|\bthen|\bdo)\s*$`)
	// catchRe matches the head of an exception handler: a skip inside one
	// runs only when the code in the try block throws.
	catchRe = regexp.MustCompile(`^\s*(?:\}\s*)?(?:catch|except|rescue)\b`)
)

// catchCond is the condition of a skip inside an exception handler.
const catchCond = "the try block throws"

// guard returns the condition that the statement at vt[pos:end] (value
// text of new line l) runs under: an if/unless/guard on the same line, Ruby's
// postfix "skip ... if cond", or an enclosing if/switch a few lines above.
func guard(f *core.FileInfo, l core.Line, vt string, pos, end int) (string, bool) {
	return guardIn(f.NewLine, f.Lang, l, vt, pos, end)
}

// guardIn is guard on the version of a file (in language lg) that get reads.
func guardIn(get lineGetter, lg lang.Lang, l core.Line, vt string, pos, end int) (string, bool) {
	if m := inlineIfRe.FindStringSubmatch(vt[:pos]); m != nil {
		return cleanCond(m[1]), true
	}
	if m := postfixIfRe.FindStringSubmatch(vt[end:]); m != nil && lg == lang.Ruby {
		return cleanCond(m[1]), true
	}
	ind := indent(l.Raw)
	seen := 0
	caseExpr := ""  // the case the statement sits under, in a tagless switch
	branch := false // a branch was crossed: the lines above belong to other branches
	for k := l.No - 1; k >= 1 && k >= l.No-80 && seen < 6; k-- {
		prev, ok := get(k)
		if !ok {
			break
		}
		if strings.TrimSpace(prev.Code) == "" {
			continue
		}
		pi := indent(prev.Raw)
		if pi >= ind && branch {
			continue // the body of an earlier case or branch
		}
		seen++
		if pi >= ind {
			continue // an earlier statement in the same block
		}
		pvt, _ := valueText(core.Line{LexedLine: prev}, lg)
		if branchRe.MatchString(pvt) {
			if m := caseRe.FindStringSubmatch(pvt); m != nil && caseExpr == "" {
				caseExpr = cleanCond(m[1])
			}
			// An else/case branch: look for the if/switch it belongs to,
			// which gofmt and most styles put at the branch's indentation.
			ind, branch = pi+1, true
			continue
		}
		if m := guardRe.FindStringSubmatch(pvt); m != nil {
			return cleanCond(m[1]), true
		}
		if catchRe.MatchString(pvt) {
			return catchCond, true // try { probe() } catch { t.skip() }
		}
		if m := switchRe.FindStringSubmatch(pvt); m != nil {
			if tag := cleanCond(m[1]); tag != "" || caseExpr == "" {
				return tag, true
			}
			return caseExpr, true // switch { case pullErr == nil && delivered: ... }
		}
		return "", false // some other block (a function, a loop, the test itself)
	}
	return "", false
}

// cleanCond trims the syntax around a condition: "(x) {", "x:", "x then",
// Swift's "x else { throw".
func cleanCond(s string) string {
	if i := strings.Index(s, " else"); i > 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "{"); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	for {
		t := strings.TrimSpace(condTailTrim.ReplaceAllString(s, ""))
		if t == s {
			break
		}
		s = t
	}
	return trimParens(s)
}

// ---- environment checks -----------------------------------------------------

// envAPIRe matches APIs that inspect the environment a test runs in.
var envAPIRe = regexp.MustCompile(`\bexistsSync\(|\bfs\.(?:promises\.)?(?:exists|access)\w*\(|\bos\.path\.(?:exists|isfile|isdir)\(|\.exists\(\)|\.is_file\(\)|\bFiles\.(?:exists|isRegularFile|isDirectory|isExecutable)\(|\bos\.Stat\(|\bos\.Lstat\(|\bFile\.(?:exist|exists|file|directory)\?|\bfile_exists\(|testing\.Short\(|runtime\.GO(?:OS|ARCH)|\bos\.(?:Getenv|LookupEnv|Geteuid|Getuid|environ|getenv|name|uname|platform|type|arch)\b|exec\.LookPath|\bsys\.(?:platform|version_info|implementation|maxsize)|\bplatform\.\w+|shutil\.which|importlib|find_spec|\bprocess\.(?:platform|env|arch|versions?)\b|navigator\.|typeof\s+(?:window|document|Deno|Bun)\b|Deno\.(?:build|env)|System\.(?:getenv|getProperty)|RuntimeInformation|OperatingSystem|Environment\.(?:GetEnvironmentVariable|OSVersion|Is64Bit\w*)|\bENV\b|RUBY_(?:PLATFORM|VERSION|ENGINE)|Gem\.win_platform|PHP_(?:OS|OS_FAMILY|VERSION|INT_SIZE)|\bgetenv\s*\(|extension_loaded|function_exists|\bcfg!|env::var|\btarget_(?:os|arch|family|pointer_width|env)\b|ProcessInfo|#available|\bPlatform\.|\bkIsWeb\b`)

// envWords are identifier parts that name an environment property:
// isCI, hasDocker, RUN_SLOW_TESTS, onWindows, browserName.
var envWords = map[string]bool{
	"windows": true, "win32": true, "linux": true, "darwin": true, "macos": true, "osx": true, "android": true,
	"ios": true, "wasm": true, "arm64": true, "docker": true, "podman": true, "network": true, "internet": true,
	"online": true, "offline": true, "gpu": true, "cuda": true, "miri": true, "ci": true, "integration": true,
	"short": true, "slow": true, "platform": true, "env": true, "environ": true, "environment": true,
	"os": true, "goos": true, "goarch": true, "browser": true, "webkit": true, "firefox": true, "chromium": true,
	"safari": true, "headless": true, "mobile": true, "sudo": true, "cgo": true, "race": true, "installed": true,
	"sys": true, "bazel": true,
	// capabilities of the machine or file system
	// ("available", "supported" and "installed" are not here: on a value the
	// test computed, `!quote.available`, they are the code's answer. Named
	// flags such as dockerAvailable() are matched by capabilityFlagRe.)
	"inode": true, "ino": true,
	"symlink": true, "symlinks": true, "hardlink": true, "posix": true, "unix": true, "root": true, "uid": true,
	"euid": true, "getuid": true, "geteuid": true, "privileged": true, "elevated": true, "tty": true, "isatty": true,
	"permission": true, "permissions": true,
}

// capabilityFlagRe matches a condition that is just a capability flag or
// probe: !hasJq, !canDropPermissions, !bunAvailable(), !IS_GOOGLE_LOGIN_ENABLED.
var capabilityFlagRe = regexp.MustCompile(`^!?\s*(?:(?:has|can|supports?)[A-Z]\w*|\w+(?:Available|Supported|Installed|Enabled|Present)|[A-Z][A-Z0-9_]*_(?:AVAILABLE|SUPPORTED|INSTALLED|ENABLED|PRESENT|CONFIGURED))(?:\(\))?$`)

// isEnvCondition reports whether a skip condition depends on the machine or
// configuration the tests run on rather than on the code under test.
func isEnvCondition(cond string) bool {
	cond = display(cond)
	if envAPIRe.MatchString(cond) || capabilityFlagRe.MatchString(strings.TrimSpace(cond)) {
		return true
	}
	for _, w := range words(cond) {
		if envWords[w] {
			return true
		}
	}
	return false
}

// words splits identifiers (and string contents) into lower-case parts at
// non-alphanumerics and camelCase boundaries: "isCI" → [is ci].
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) && len(cur) > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// envReasonRe matches skip reasons that describe a missing environment
// ("docker not available"), as opposed to a failing test ("flaky", "TODO").
var envReasonRe = regexp.MustCompile(`(?i)\b(?:not (?:available|installed|running|reachable|configured|set)|(?:not supported|unsupported) (?:on|by|in|under|here)|unavailable|requires? (?:a |an )?(?:running |local )?(?:docker|network|internet|root|admin|gpu|database|db|redis|postgres|mysql|credentials|api key|token|display|browser)|only (?:on|runs on|works on|supported on)|short mode|-short|no (?:docker|network|internet|gpu|display|tty|database|db|credentials))\b|\b(?:set|export)\s+(?-i:[A-Z][A-Z0-9]*_[A-Z0-9_]+)\b`)

// ---- issue references -------------------------------------------------------

// issueRef reports whether a marker on line l is tied to an issue: its
// reason, its trailing comment, or the comment block right above it links
// one ("flaky, see #123").
func issueRef(f *core.FileInfo, l core.Line, reason string) bool {
	if refersToIssue(reason) || refersToIssue(l.Comment) {
		return true
	}
	for k := l.No - 1; k >= 1 && k >= l.No-3; k-- {
		prev, ok := f.NewLine(k)
		if !ok || strings.TrimSpace(prev.Code) != "" || prev.Comment == "" {
			break
		}
		if refersToIssue(prev.Comment) {
			return true
		}
	}
	return false
}

var (
	// issueWordRe matches the words issue and ticket, with the number that
	// may follow them ("issue 4521").
	issueWordRe = regexp.MustCompile(`(?i)\b(?:issue|ticket)s?\b(?:\s+(?:no\.?\s*)?#?(\d+)\b)?`)
	// standardKeyRe matches names of standards and encodings that look like
	// issue keys (UTF-8, ISO-8601, SHA-256) but link nothing.
	standardKeyRe = regexp.MustCompile(`\b(?:UTF|UCS|ISO|IEC|IEEE|ANSI|ECMA|ES|SHA|MD|AES|RSA|HTTP|TLS|SSL|IPV|UTC|GMT|CP|WIN|ARM|PEP|ASCII)-\d+\b`)
)

// refersToIssue is rulekit.HasIssueRef without its false alarms: a bare
// "timing issue" or "UTF-8" links nothing, and an agent must not be able to
// soften a finding by writing one. "issue 4521" counts, as #4521.
func refersToIssue(s string) bool {
	if s == "" {
		return false
	}
	s = issueWordRe.ReplaceAllString(s, " #$1 ")
	s = standardKeyRe.ReplaceAllString(s, "")
	// A lone "#" left by a bare "issue" is not a reference: #\d+ needs digits.
	return rulekit.HasIssueRef(s)
}
