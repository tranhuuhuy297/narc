package escapes

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

var testEnvBranch = &core.Rule{
	ID:       "test-env-branch",
	Title:    "Production code detects tests",
	Category: "escapes",
	Severity: core.Error,
	Summary:  "Non-test code now checks whether it is running under a test runner (NODE_ENV === 'test', JEST_WORKER_ID, \"pytest\" in sys.modules, testing.Testing(), cfg!(test), Rails.env.test?, ...).",
	Hint: "Don't let production code detect the test runner. Make the code correct in every environment and move " +
		"test-only behavior into the tests: inject the dependency, mock the boundary (clock, network, filesystem), " +
		"or pass configuration explicitly from the test setup.",
	Docs: "Code that behaves differently under tests makes the tests verify something other than what ships. It is " +
		"also the classic way to make a failing test pass without fixing anything.\n\n" +
		"```ts\n// bad: the test passes, production is still broken\nexport function price(cart: Cart) {\n" +
		"  if (process.env.NODE_ENV === 'test') return 42\n  return computePrice(cart)\n}\n```\n\n" +
		"Reported as an error in source files and as a warning in configuration, settings and entry-point files, " +
		"where environment wiring is expected. Comparisons through a variable count too " +
		"(`const { NODE_ENV } = process.env; if (NODE_ENV === \"test\")`), as do a `case \"test\":` arm of a switch " +
		"on the environment and framework flags such as Flask's `app.testing` or a `settings.TESTING` setting. " +
		"Not reported: test files and test helpers, " +
		"test-runner and build-tool configs (jest/vitest/vite/babel), in-source tests (`if (import.meta.vitest)`), " +
		"`#[cfg(test)]` modules, a CLI's own `test` subcommand that starts the test runner, validation of the " +
		"environment name against every known environment (`[\"development\", \"test\", \"production\"]`), a " +
		"\"test\" arm that shares its body with another environment, test hooks that refuse to run outside tests " +
		"(`if !testing.Testing() { panic(...) }`), and checks whose every " +
		"guarded statement only silences logging or telemetry, skips starting a server, shortens timeouts, or " +
		"registers test-framework hooks.",
}

func init() {
	testEnvBranch.Check = checkTestEnvBranch
	core.Register(testEnvBranch)
}

// envCheck is one way of detecting a test run.
type envCheck struct {
	langs []lang.Lang
	re    *regexp.Regexp
	// onCode matches the code without string contents, for checks whose
	// key words must not come from a string (cfg!(feature = "test-utils")).
	onCode bool
	needs  []*regexp.Regexp // the line must also match all of these
	not    *regexp.Regexp   // the line must not match this
	what   string           // message: what the code does; %s is the matched text
	// byVar identifies a hit by the environment variable it compares, so a
	// check rewritten through an alias (`const { NODE_ENV } = process.env`)
	// cancels against the original.
	byVar bool
	// compare: the check compares an environment name with "test", which a
	// list of every environment (validation) does too without branching.
	compare bool
}

const q = "[\"'`]"

// Environment variables whose only purpose is to tell that tests are running.
const testVars = `JEST_WORKER_ID|VITEST|VITEST_WORKER_ID|VITEST_POOL_ID|VITEST_MODE|PYTEST_CURRENT_TEST|PYTEST_VERSION|` +
	`PYTEST_XDIST_WORKER|FLUTTER_TEST|XCTestConfigurationFilePath|XCTestSessionIdentifier|XCTestBundlePath|` +
	`RUNNING_TESTS?|IS_TEST(?:ING)?|IN_TESTS?|TEST_MODE|TESTING|UNIT_TESTS?`

// Environment variables naming the environment, which test runners set to
// "test" or "testing".
const envNames = `NODE_ENV|BABEL_ENV|APP_ENV|RAILS_ENV|RACK_ENV|FLASK_ENV|DJANGO_ENV|GO_ENV|MIX_ENV|` +
	`ASPNETCORE_ENVIRONMENT|DOTNET_ENVIRONMENT|APP_ENVIRONMENT|ENV|ENVIRONMENT`

// envAccess holds, per language, how code reads an environment variable;
// %[1]s is replaced by the variable names.
var envAccess = []struct {
	langs []lang.Lang
	forms []string
}{
	{jsLike, []string{`process\.env(?:\.(?:%[1]s)\b|\[\s*<q>(?:%[1]s)<q>\s*\])`, `import\.meta\.env\.(?:%[1]s)\b`,
		`Deno\.env\.get\(\s*<q>(?:%[1]s)<q>`, `Bun\.env\.(?:%[1]s)\b`,
		`<q>(?:%[1]s)<q>\s+in\s+process\.env\b`, `process\.env\.hasOwnProperty\(\s*<q>(?:%[1]s)<q>`,
		`Object\.(?:hasOwn|prototype\.hasOwnProperty\.call)\(\s*process\.env\s*,\s*<q>(?:%[1]s)<q>`}},
	{python, []string{`os\.environ\.get\(\s*<q>(?:%[1]s)<q>`, `os\.environ\[\s*<q>(?:%[1]s)<q>\s*\]`, `os\.getenv\(\s*<q>(?:%[1]s)<q>`,
		`<q>(?:%[1]s)<q>\s+(?:not\s+)?in\s+os\.environ\b`}},
	{golang, []string{`os\.(?:Getenv|LookupEnv)\(\s*<q>(?:%[1]s)<q>`}},
	{[]lang.Lang{lang.Ruby}, []string{`ENV(?:\[\s*|\.fetch\(\s*|\.key\?\(\s*|\.has_key\?\(\s*|\.include\?\(\s*)<q>(?:%[1]s)<q>`}},
	{php, []string{`(?:getenv\(|\$_ENV\[|\$_SERVER\[|\benv\()\s*<q>(?:%[1]s)<q>`}},
	{jvmLangs, []string{`System\.(?:getenv|getProperty)\(\s*<q>(?:%[1]s)<q>`}},
	{csharp, []string{`Environment\.GetEnvironmentVariable\(\s*<q>(?:%[1]s)<q>`}},
	{rust, []string{`env::var(?:_os)?\(\s*<q>(?:%[1]s)<q>`, `option_env!\(\s*<q>(?:%[1]s)<q>`}},
	{[]lang.Lang{lang.Swift}, []string{`environment\[\s*<q>(?:%[1]s)<q>\s*\]`}},
	{[]lang.Lang{lang.Dart}, []string{`Platform\.environment(?:\[\s*|\.containsKey\(\s*)<q>(?:%[1]s)<q>`}},
	{[]lang.Lang{lang.Elixir}, []string{`System\.get_env\(\s*<q>(?:%[1]s)<q>`}},
}

var (
	// comparesWithTest: the line compares something with "test"/"testing".
	comparesWithTest = []*regexp.Regexp{
		re(`(?i)<q>(?:test|testing)<q>`),
		regexp.MustCompile(`[!=]==?|\.equals\(|\.Equals\(|\bin\b|\.includes\(|\.startsWith\(|\bcase\b|\bwhen\b`),
	}
	envChecks = buildEnvChecks()
)

// re compiles a pattern in which <q> stands for any quote character.
func re(pattern string) *regexp.Regexp {
	return regexp.MustCompile(strings.ReplaceAll(pattern, "<q>", q))
}

func buildEnvChecks() []envCheck {
	var checks []envCheck
	for _, ea := range envAccess {
		for _, form := range ea.forms {
			// The variable name is the first capture group: messages name it.
			checks = append(checks,
				envCheck{langs: ea.langs, re: re(fmt.Sprintf(form, "("+testVars+")")), what: "reads the %s environment variable"},
				envCheck{langs: ea.langs, re: re(fmt.Sprintf(form, "("+envNames+")")), needs: comparesWithTest, what: `compares %s with "test"`,
					byVar: true, compare: true})
		}
	}
	return append(checks, []envCheck{
		// JavaScript / TypeScript
		{langs: jsLike, re: re(`import\.meta\.env\.(MODE)\b`), needs: comparesWithTest, compare: true, what: `compares import.meta.env.%s with "test"`},
		{langs: jsLike, re: re(`\.get\(\s*<q>env<q>\s*\)`), needs: comparesWithTest, compare: true, what: `compares Express's app.get("env") with "test"`},
		{langs: jsLike, re: re(`\btypeof\s+(?:jest|vi|describe|it|expect|beforeEach|afterEach|mocha|jasmine|QUnit)\s*[!=]==?\s*<q>(?:undefined|object|function)<q>`),
			what: "checks whether the test framework global exists (%s)"},
		{langs: jsLike, re: re(`\b(?:globalThis|window|global)\.(?:jest|vi|__vitest_\w+|__karma__|jasmine|mocha)\b`),
			what: "checks for the test framework global %s"},
		{langs: jsLike, re: re(`\b__TESTS?__\b`), onCode: true, not: regexp.MustCompile(`\bdeclare\b|__TESTS?__\s*:`),
			what: "branches on the compile-time test flag %s"},
		// Python
		{langs: python, re: re(`<q>(?:pytest|_pytest|unittest|nose|nose2)<q>\s+(?:not\s+)?in\s+sys\.modules\b|sys\.modules\.get\(\s*<q>(?:pytest|unittest)<q>|sys\.modules\[\s*<q>(?:pytest|unittest)<q>`),
			what: "checks whether the test framework is loaded (%s)"},
		{langs: python, re: re(`\bsys\.argv\b`), needs: []*regexp.Regexp{re(`<q>[^"'\x60]*(?:pytest|py\.test|unittest|nose2?)[^"'\x60]*<q>|<q>test<q>`)},
			what: "inspects the command line for a test runner (%s)"},
		{langs: python, re: re(`\bsys\._called_from_test\b`), what: "checks %s"},
		// Flask's built-in testing flag, and the TESTING setting projects
		// define for Django; assigning them (test setup) is no check.
		{langs: python, re: re(`\b(?:current_app|app|self\.app)\.(?:testing\b|config(?:\[\s*<q>TESTING<q>\s*\]|\.get\(\s*<q>TESTING<q>))|` +
			`\bsettings\.(?:TESTING|TEST_MODE|IS_TESTING|RUNNING_TESTS)\b`),
			not:  regexp.MustCompile(`(?:\.testing|\]|\bsettings\.\w+)\s*=[^=]`),
			what: "checks %s"},
		// Go
		{langs: golang, re: re(`\btesting\.Testing\(\)`), what: "calls %s"},
		{langs: golang, re: re(`\bflag\.Lookup\(\s*<q>test\.\w+<q>\s*\)`), what: "looks up the test flag (%s)"},
		{langs: golang, re: re(`\bos\.Args\[0\]`), needs: []*regexp.Regexp{re(`<q>[^"'\x60]*(?:\.test|_test|/go-build)[^"'\x60]*<q>`)},
			what: "checks whether the binary is a test binary (%s)"},
		// Rust
		{langs: rust, re: re(`\bcfg!\s*\([^)]*\btest\b[^)]*\)+`), onCode: true, what: "branches on %s"},
		// Ruby
		{langs: []lang.Lang{lang.Ruby}, re: re(`\bRails\.env\.test\?|\bRails\.env\s*(?:==|!=|\.eql\?\()\s*<q>(?:test)<q>|<q>(?:test)<q>\s*==\s*Rails\.env\b`), what: "checks %s"},
		{langs: []lang.Lang{lang.Ruby}, re: re(`\bdefined\?\(\s*(?:RSpec|Minitest|Test::Unit)\b`), what: "checks whether the test framework is loaded (%s)"},
		// PHP
		{langs: php, re: re(`(?:->|::)runningUnitTests\(\)`), what: "calls runningUnitTests()"},
		{langs: php, re: re(`(?:app\(\)->|App::|\$app->|->app->)environment\(\s*<q>(?:testing|test)<q>`), what: "checks %s"},
		{langs: php, re: re(`->getEnvironment\(\)\s*[!=]==?\s*<q>(?:test|testing)<q>`), what: "checks %s"},
		{langs: php, re: re(`\bdefined\(\s*<q>PHPUNIT_\w+<q>\s*\)|\bclass_exists\(\s*<q>[^"']*PHPUnit`), what: "checks whether PHPUnit is loaded (%s)"},
		// JVM
		{langs: jvmLangs, re: re(`<q>(?:org\.junit|junit\.framework|org\.testng|org\.spockframework|io\.kotest|org\.robolectric|androidx\.test|android\.support\.test)[^"'\x60]*<q>`),
			needs: []*regexp.Regexp{regexp.MustCompile(`forName|[Ss]tackTrace|getClassName|loadClass|className`)},
			what:  "looks for the test framework on the classpath or call stack (%s)"},
		{langs: jvmLangs, re: re(`\bBuild\.(?:FINGERPRINT|DEVICE|PRODUCT|MODEL|BRAND)\b`), needs: []*regexp.Regexp{re(`(?i)<q>robolectric<q>`)},
			what: "checks for the Robolectric test device (%s)"},
		{langs: jvmLangs, re: re(`acceptsProfiles\(\s*(?:Profiles\.of\(\s*)?<q>(?:test|testing)<q>`), what: "checks the Spring profile (%s)"},
		{langs: jvmLangs, re: re(`getActiveProfiles\(\)`), needs: comparesWithTest[:1], what: "checks the Spring profile (%s)"},
		// C#
		{langs: csharp, re: re(`<q>[^"'\x60]*(?:xunit|nunit|Microsoft\.VisualStudio\.TestPlatform|testhost|MSTest)[^"'\x60]*<q>`),
			needs: []*regexp.Regexp{regexp.MustCompile(`GetAssemblies|FullName|StartsWith|GetCommandLineArgs|ProcessName|Contains`)},
			what:  "looks for the test host (%s)"},
		{langs: csharp, re: re(`\bIsEnvironment\(\s*<q>(?:Test|Testing)<q>\s*\)`), what: "checks %s"},
		// Swift
		{langs: []lang.Lang{lang.Swift}, re: re(`\bNSClassFromString\(\s*<q>XCTest\w*<q>\s*\)`), what: "checks whether XCTest is loaded (%s)"},
		// Elixir
		{langs: []lang.Lang{lang.Elixir}, re: re(`\bMix\.env(?:\(\))?\s*(?:==|!=|===)\s*:test\b|:test\s*==\s*Mix\.env\b|\bconfig_env\(\)\s*==\s*:test\b|` +
			`\bApplication\.(?:get_env|fetch_env!)\(\s*:\w+\s*,\s*:(?:env|environment)\s*\)\s*(?:==|!=|===)\s*:test\b`), what: "checks %s"},
	}...)
}

// envHit is a test-environment check on a line.
type envHit struct {
	key  string
	what string
}

// envScanner finds test-environment checks, including comparisons made
// through a variable that holds the environment name.
type envScanner struct {
	aliases map[*core.FileInfo]*envAliases
}

// envAliases are the variables of a file that hold the value of an
// environment-name variable, and a pattern for comparing them with "test".
type envAliases struct {
	vars    map[string]string // alias -> environment variable it reads
	compare *regexp.Regexp    // nil when there are no aliases
}

func (sc *envScanner) find(f *core.FileInfo, l core.Line, old bool) []envHit {
	if strings.TrimSpace(l.Code) == "" {
		return nil
	}
	var v *view
	for _, c := range envChecks {
		if !langIn(f.Lang, c.langs...) {
			continue
		}
		src := l.Code
		var m []int
		if c.onCode {
			m = c.re.FindStringSubmatchIndex(src)
		} else {
			if v == nil {
				v = newView(f.Lang, l.LexedLine)
			}
			if ms := v.matches(c.re); len(ms) > 0 {
				src, m = v.canon, ms[0]
			}
		}
		ok := m != nil
		var text, shown string
		if ok {
			text = src[m[0]:m[1]]
			shown = text
			if len(m) > 2 && m[2] >= 0 { // a named variable
				shown = src[m[2]:m[3]]
			}
		}
		if !ok || (c.not != nil && c.not.MatchString(l.Code)) {
			continue
		}
		for _, n := range c.needs {
			if v == nil {
				v = newView(f.Lang, l.LexedLine)
			}
			if !v.match(n) {
				ok = false
				break
			}
		}
		if !ok || (c.compare && enumeratesEnvs(v)) {
			continue
		}
		what := c.what
		if strings.Contains(what, "%s") {
			what = fmt.Sprintf(what, shorten(shown, 50))
		}
		key := rulekit.Normalize(text) + "|" + c.what
		if c.byVar {
			key = envCompareKey(shown)
		}
		return []envHit{{key: key, what: what}}
	}
	if al := sc.aliasesOf(f); al.compare != nil {
		if v == nil {
			v = newView(f.Lang, l.LexedLine)
		}
		for _, m := range v.matches(al.compare) {
			if enumeratesEnvs(v) {
				break
			}
			for g := 1; 2*g < len(m); g++ {
				if m[2*g] < 0 {
					continue
				}
				name := v.canon[m[2*g]:m[2*g+1]]
				envVar := al.vars[name]
				what := fmt.Sprintf(`compares %s (read from %s) with "test"`, name, envVar)
				if name == envVar {
					what = fmt.Sprintf(`compares %s, read from the environment, with "test"`, name)
				}
				return []envHit{{key: envCompareKey(envVar), what: what}}
			}
		}
	}
	if v == nil {
		v = newView(f.Lang, l.LexedLine)
	}
	if v.match(testArmRe) && !sharedArm(f, v, l.No, old) {
		if envVar, ok := sc.switchedEnv(f, l.No, old); ok {
			return []envHit{{key: envCompareKey(envVar), what: fmt.Sprintf(`switches on %s and handles "test" separately`, envVar)}}
		}
	}
	return nil
}

// testArmRe matches a switch or match arm for the "test" environment:
// `case "test":`, `when "test"`, `"test" -> ...`, `"test" => ...`.
var testArmRe = re(`^\s*(?:(?:case|when)\s+<q>(?:test|testing)<q>|<q>(?:test|testing)<q>\s*(?:->|=>))`)

var (
	// envSwitchHeadRe matches the head of a switch or match statement.
	envSwitchHeadRe = regexp.MustCompile(`\b(?:switch|match)\b|\bwhen\s*\(`)
	// caseHeadRe matches the head of a Ruby or Elixir case expression.
	caseHeadRe = regexp.MustCompile(`^\s*(?:[\w@]+\s*=\s*)?case\s+\S`)
)

// armRe matches the start of a switch or match arm.
var armRe = re(`^\s*(?:case|when|default)\b|^\s*(?:<q>[^"'\x60]*<q>|_)\s*(?:->|=>)`)

// sharedArm reports whether the "test" arm on line n (view v) shares its
// body with other environments: `case "test": case "development":` falls
// through, `when "test", "development"` lists both. Test runs then get the
// same behavior as a development machine, not behavior of their own.
func sharedArm(f *core.FileInfo, v *view, n int, old bool) bool {
	labels := 0
	for _, t := range v.toks {
		if t.is(tPunct, ":") || t.is(tPunct, "->") || t.is(tPunct, "=>") || t.is(tIdent, "then") {
			break // the arm's body starts
		}
		if t.kind == tString {
			labels++
		}
	}
	if labels > 1 {
		return true
	}
	for m := n + 1; m < n+5; m++ {
		ll, ok := sideLine(f, old, m)
		if !ok {
			return false
		}
		if strings.TrimSpace(ll.Code) == "" {
			continue
		}
		return armRe.MatchString(ll.Code)
	}
	return false
}

// rubyEnvRe matches Rails' and Rack's environment readers, which a Ruby
// `case` can switch on.
var rubyEnvRe = regexp.MustCompile(`\b(?:Rails|Rack)\.env\b`)

// switchedEnv returns the environment variable that the switch or match
// above the arm on line n inspects, if it reads one.
func (sc *envScanner) switchedEnv(f *core.FileInfo, n int, old bool) (string, bool) {
	for m := n - 1; m >= 1 && m > n-40; m-- {
		ll, ok := sideLine(f, old, m)
		if !ok {
			return "", false
		}
		if !envSwitchHeadRe.MatchString(ll.Code) &&
			!(langIn(f.Lang, lang.Ruby, lang.Elixir) && caseHeadRe.MatchString(ll.Code)) {
			continue
		}
		v := newView(f.Lang, ll)
		for _, c := range envChecks {
			if !c.byVar || !langIn(f.Lang, c.langs...) {
				continue
			}
			if ms := v.matches(c.re); len(ms) > 0 && ms[0][2] >= 0 {
				return v.canon[ms[0][2]:ms[0][3]], true
			}
		}
		if f.Lang == lang.Ruby && v.match(rubyEnvRe) {
			return "RAILS_ENV", true
		}
		for _, t := range v.toks {
			if envVar, ok := sc.aliasesOf(f).vars[t.text]; ok && t.kind == tIdent {
				return envVar, true
			}
		}
		return "", false // the nearest switch inspects something else
	}
	return "", false
}

// prodEnvRe matches the name of a production environment.
var prodEnvRe = regexp.MustCompile(`^(?i:production|prod|live)$`)

// enumeratesEnvs reports whether a line lists the production environment
// next to "test" (`["development", "test", "production"].includes(env)`,
// `z.enum([...])`): validation of the environment name, or a value chosen
// for every environment, rather than test-only behavior.
func enumeratesEnvs(v *view) bool {
	test, prod := false, false
	for _, t := range v.toks {
		if t.kind != tString || !t.known {
			continue
		}
		switch {
		case strings.EqualFold(t.text, "test"), strings.EqualFold(t.text, "testing"):
			test = true
		case prodEnvRe.MatchString(t.text):
			prod = true
		}
	}
	return test && prod
}

// envCompareKey identifies a comparison of an environment variable with
// "test", however the variable was read.
func envCompareKey(envVar string) string { return "env-compare:" + envVar }

var (
	envNameReadRe = regexp.MustCompile(`\b(` + envNames + `)\b`)
	// envDestructureRe matches `const { NODE_ENV, APP_ENV: env = "dev" } = process.env`.
	envDestructureRe = regexp.MustCompile(`\b(?:const|let|var)\s*\{([^}]*)\}\s*=\s*(?:process|import\.meta|Bun|Deno)\.env\b`)
	destructItemRe   = regexp.MustCompile(`^\s*(\w+)\s*(?::\s*([A-Za-z_$][\w$]*))?\s*(?:=.*)?$`)
	envNameOnlyRe    = regexp.MustCompile(`^(?:` + envNames + `)$`)
)

// aliasesIn adds to into the variables of one file version that hold the
// value of an environment-name variable (NODE_ENV, APP_ENV, ...), mapped to
// that variable: `const env = process.env.NODE_ENV ?? "development"`,
// `env := os.Getenv("APP_ENV")`, `const { NODE_ENV } = process.env`.
func aliasesIn(f *core.FileInfo, s *side, into map[string]string) {
	for _, n := range s.lineNos() {
		v := s.view(n)
		if v == nil || !envNameReadRe.MatchString(v.canon) {
			continue
		}
		if m := envDestructureRe.FindStringSubmatch(v.canon); m != nil && !v.inString(strings.Index(v.canon, m[0])) {
			for _, item := range strings.Split(m[1], ",") {
				im := destructItemRe.FindStringSubmatch(item)
				if im == nil || !envNameOnlyRe.MatchString(im[1]) {
					continue
				}
				name := im[1]
				if im[2] != "" {
					name = im[2]
				}
				into[name] = im[1]
			}
			continue
		}
		am := flagAssignRe.FindStringSubmatch(v.canon)
		if am == nil || comparesWithTest[1].MatchString(v.canon[len(am[0]):]) {
			continue // not an assignment, or the line already compares (handled directly)
		}
		for _, c := range envChecks {
			if !c.byVar || !langIn(f.Lang, c.langs...) {
				continue
			}
			if ms := v.matches(c.re); len(ms) > 0 && ms[0][2] >= 0 {
				into[am[1]] = v.canon[ms[0][2]:ms[0][3]]
				break
			}
		}
	}
}

// aliasesOf returns the environment-name aliases of both versions of f.
func (sc *envScanner) aliasesOf(f *core.FileInfo) *envAliases {
	if al, ok := sc.aliases[f]; ok {
		return al
	}
	al := &envAliases{vars: map[string]string{}}
	aliasesIn(f, oldSide(f), al.vars)
	aliasesIn(f, newSide(f), al.vars)
	if len(al.vars) > 0 {
		var quoted []string
		for name := range al.vars {
			quoted = append(quoted, regexp.QuoteMeta(name))
		}
		sort.Strings(quoted)
		alt := strings.Join(quoted, "|")
		test := `<q>(?:test|testing)<q>`
		// alias == "test", "test" == alias, alias in ("test", ...),
		// ["test", ...].includes(alias); the alias is the first non-empty group.
		al.compare = re(`(?:^|[^\w$.])(` + alt + `)\s*(?:[!=]==?|\.equals\(|\.startsWith\()\s*` + test +
			`|` + test + `\s*(?:[!=]==?|\.equals\()\s*(` + alt + `)(?:[^\w$]|$)` +
			`|(?:^|[^\w$.])(` + alt + `)\s+(?:not\s+)?in\s+[\[({][^\])}]*` + test +
			`|\[[^\]]*` + test + `[^\]]*\]\.includes\(\s*(` + alt + `)\s*\)`)
	}
	if sc.aliases == nil {
		sc.aliases = map[*core.FileInfo]*envAliases{}
	}
	sc.aliases[f] = al
	return al
}

func checkTestEnvBranch(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool {
		return f.Lang.IsProgramming() && !isTestSupport(f.Path) && !isTestRunnerConfig(f.Path) &&
			!strings.HasSuffix(f.Path, ".d.ts") && (!f.IsTest || f.Lang == lang.Rust)
	})
	sc := &envScanner{}
	occs := netNew(files, sc.find, func(h envHit) []string { return []string{h.key} })
	var out []core.Finding
	sides := map[*core.FileInfo]*side{}
	for _, o := range occs {
		f := o.file
		if testLines(a, f)(o.line.No) {
			continue
		}
		if sides[f] == nil {
			sides[f] = newSide(f)
		}
		if benignTestCheck(f, sides[f], o.line.No) {
			continue
		}
		fd := newFinding(testEnvBranch, f, o.line.No, "production code now behaves differently under tests: it "+o.v.what)
		if isBootstrap(f.Path) {
			fd.Severity = core.Warning
		}
		out = append(out, fd)
	}
	return out
}

var testRunnerConfigRe = regexp.MustCompile(`^(jest\.config|vitest\.config|vitest\.workspace|playwright\.config|` +
	`cypress\.config|karma\.conf|\.mocharc|wdio\.conf|vite\.config|babel\.config|\.babelrc)\.`)

// isTestRunnerConfig reports whether p configures the test runner itself,
// or the build tooling it loads: vite.config.* doubles as the Vitest
// config, and babel-jest compiles tests with babel.config.*. Choosing
// tooling for the test run is their job, not production behavior.
func isTestRunnerConfig(p string) bool { return testRunnerConfigRe.MatchString(path.Base(p)) }

// benignRe matches what a test check may legitimately control: log noise,
// telemetry, starting a server or exiting the process, timing, and hooking
// into the test framework itself.
var benignRe = regexp.MustCompile(`(?i)\b(?:console\.(?:log|info|debug|warn|error|time)|log\.\w+|logger|logging|log_?level|loglevel|` +
	// registering with the test framework, as test libraries do:
	// `if (typeof afterEach === "function") afterEach(cleanup)`
	`(?:after|before)(?:Each|All)|expect\.extend|expect\.addSnapshotSerializer|` +
	`winston|pino|morgan|bunyan|zap|logrus|slog|silent|quiet|verbose|sentry|bugsnag|rollbar|datadog|new_?relic|telemetry|` +
	`analytics|posthog|mixpanel|opentelemetry|otel|tracing|tracer|metrics|prometheus|statsd|uvicorn|listenandserve|` +
	`process\.exit|timeout|delay|sleep|interval|backoff|debounce|throttle)\b|\.listen\(|\bserve\(|createServer\(`)

// testRunnerCallRe matches a branch that starts the test runner: a
// command-line tool's own `test` subcommand.
var testRunnerCallRe = re(`\b(?:pytest|unittest|nose2?)\.main\(|\bcall_command\(\s*<q>test<q>|` +
	`\bsubprocess\.\w+\(.*<q>(?:pytest|py\.test|unittest)<q>|\bexec(?:vp?|lp?)?\(.*<q>(?:pytest|go|cargo|npm|jest|vitest)<q>.*<q>test<q>`)

// cannedResultRe matches a branch that returns a value (a bare `return`
// that skips a side effect does not count).
var cannedResultRe = regexp.MustCompile(`\breturn\s*[^\s;}]`)

var benignPathRe = regexp.MustCompile(`(?i)^(log|logs|logger|logging|telemetry|analytics|sentry|tracing|tracer|metrics|monitoring|instrument|instrumentation)$`)

// flagAssignRe matches the assignment of a check to a variable, capturing
// the variable name.
var flagAssignRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:(?:const|let|var|val|final|static|private|public|protected|readonly|internal)\s+)*` +
	`(?:[\w<>\[\]?]+\s+)?([A-Za-z_$][\w$]*)\s*(?::\s*[\w<>\[\]?|]+\s*)?(?::=|=)(?:[^=]|$)`)

// benignTestCheck reports whether the test check on line n only controls
// infrastructure (see benignRe): in a logging or telemetry module, in the
// branch it guards, or in every use of the flag it is assigned to.
func benignTestCheck(f *core.FileInfo, s *side, n int) bool {
	ctx := branchContext(s, n)
	if testRunnerCallRe.MatchString(ctx) {
		return true // `devkit test` dispatching to the test runner, not detecting it
	}
	if cannedResultRe.MatchString(ctx) {
		return false // `if (isTest) { log(...); return 4.99 }` returns a test-only answer
	}
	stem := strings.SplitN(path.Base(f.Path), ".", 2)[0]
	if benignPathRe.MatchString(stem) || benignPathRe.MatchString(path.Base(path.Dir(f.Path))) {
		return true
	}
	if benignBranch(s, n) {
		return true
	}
	code, _ := s.code(n)
	m := flagAssignRe.FindStringSubmatch(code)
	if m == nil {
		return false
	}
	// The flag's own name may say what it controls: RETRY_DELAY_MS, logLevel.
	if benignRe.MatchString(identWords(m[1])) {
		return true
	}
	use := regexp.MustCompile(`\b` + regexp.QuoteMeta(m[1]) + `\b`)
	uses := 0
	for _, k := range s.lineNos() {
		c, ok := s.code(k)
		if k == n || !ok || !use.MatchString(c) {
			continue
		}
		if !benignBranch(s, k) {
			return false
		}
		uses++
	}
	return uses > 0
}

// benignBranch reports whether what line n controls is only infrastructure
// (see benignRe): every statement of the block it opens, so that a
// `logger.info(...)` next to `chargeCard()` does not excuse the charge
// being skipped under tests. A line that opens no block (a ternary, an
// argument, a one-line if) is judged as a whole.
func benignBranch(s *side, n int) bool {
	if stmts, ok := branchBody(s, n); ok {
		for _, st := range stmts {
			if !benignRe.MatchString(st) && !misuseGuardRe.MatchString(strings.TrimSpace(st)) {
				return false
			}
		}
		return true
	}
	v := s.view(n)
	return v != nil && (benignRe.MatchString(v.canon) || oneLineGuardRe.MatchString(v.canon))
}

var (
	// misuseGuardRe matches a statement that only aborts: a test hook
	// refusing to run in production (`if !testing.Testing() { panic(...) }`)
	// changes no answer the tests check.
	misuseGuardRe = regexp.MustCompile(`^(?:throw|raise)\b|^panic!?\(|^log\.(?:Fatal|Panic)\w*\(`)
	// oneLineGuardRe is misuseGuardRe for a branch written on one line.
	oneLineGuardRe = regexp.MustCompile(`(?:[{)]|:|\bthen)\s*(?:throw\s|raise\s|panic!?\(|log\.(?:Fatal|Panic)\w*\()[^{}]*\}?\s*$`)
)

// branchBody returns the statements of the block that line n opens: a
// brace block that starts at the end of the line, or the more indented
// lines below it (Python, Ruby, Elixir, a brace-less if). Statements are
// canonical code; a line holding several is split at its semicolons.
func branchBody(s *side, n int) ([]string, bool) {
	code, ok := s.code(n)
	if !ok {
		return nil, false
	}
	first, last := n+1, 0
	if i := strings.LastIndex(code, "{"); i >= 0 && strings.Count(code[i:], "}") == 0 {
		if strings.TrimSpace(code[i+1:]) != "" {
			return nil, false
		}
		end, _, ok := s.block(n, i)
		if !ok || end == n {
			return nil, false
		}
		if lc, _ := s.code(end); !strings.HasPrefix(strings.TrimSpace(lc), "}") {
			return nil, false
		}
		last = end - 1
	} else {
		l, _ := s.at(n)
		next := s.nextCode(n)
		nl, ok := s.at(next)
		if !ok || indent(nl.Raw) <= indent(l.Raw) {
			return nil, false
		}
		for k := next; k <= s.len(); k++ {
			kl, ok := s.at(k)
			if !ok {
				return nil, false
			}
			if strings.TrimSpace(kl.Code) != "" && indent(kl.Raw) <= indent(l.Raw) {
				break
			}
			last = k
		}
	}
	var stmts, cur []string
	depth := 0
	for k := first; k <= last; k++ {
		c, _ := s.code(k)
		if strings.TrimSpace(c) == "" {
			continue
		}
		parts := splitStatements(c)
		if len(parts) == 1 {
			parts = []string{s.view(k).canon} // keep string contents ("silent")
		}
		cur = append(cur, parts[0])
		for _, p := range parts[1:] {
			stmts, cur = append(stmts, strings.Join(cur, "\n")), []string{p}
		}
		depth += strings.Count(c, "(") + strings.Count(c, "[") + strings.Count(c, "{") -
			strings.Count(c, ")") - strings.Count(c, "]") - strings.Count(c, "}")
		if depth <= 0 {
			stmts, cur, depth = append(stmts, strings.Join(cur, "\n")), nil, 0
		}
	}
	if len(cur) > 0 {
		stmts = append(stmts, strings.Join(cur, "\n"))
	}
	return stmts, len(stmts) > 0
}

// splitStatements splits a line of code at the semicolons outside
// brackets, dropping empty pieces.
func splitStatements(code string) []string {
	var out []string
	depth, start := 0, 0
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ';':
			if depth == 0 {
				add(code[start:i])
				start = i + 1
			}
		}
	}
	add(code[start:])
	if len(out) == 0 {
		out = []string{strings.TrimSpace(code)}
	}
	return out
}

var camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// identWords splits an identifier into space-separated words:
// RETRY_DELAY_MS and retryDelayMs both become "retry delay ms".
func identWords(id string) string {
	id = camelRe.ReplaceAllString(id, "$1 $2")
	return strings.ToLower(strings.NewReplacer("_", " ", "$", " ").Replace(id))
}

// branchContext returns the code of line n plus the block or indented
// statement it controls (a few lines at most).
func branchContext(s *side, n int) string {
	const maxLines = 12
	v := s.view(n)
	if v == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(v.canon)
	code, _ := s.code(n)
	last := n
	if i := strings.LastIndex(code, "{"); i >= 0 && strings.Count(code[i:], "}") == 0 {
		if e, _, ok := s.block(n, i); ok {
			last = e
		}
	} else if next := s.nextCode(n); next > 0 {
		l, _ := s.at(n)
		nl, _ := s.at(next)
		if indent(nl.Raw) > indent(l.Raw) {
			last = next
			for k := next + 1; k <= s.len(); k++ {
				kl, ok := s.at(k)
				if !ok || (strings.TrimSpace(kl.Code) != "" && indent(kl.Raw) <= indent(l.Raw)) {
					break
				}
				last = k
			}
		}
	}
	if last > n+maxLines {
		last = n + maxLines
	}
	for k := n + 1; k <= last; k++ {
		if kv := s.view(k); kv != nil {
			b.WriteByte('\n')
			b.WriteString(kv.canon)
		}
	}
	return b.String()
}
