package tests

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
	"github.com/huyth45/narc/internal/lang"
)

var retryAdded = &core.Rule{
	ID:       "retry-added",
	Title:    "Test retries added",
	Category: "tests",
	Severity: core.Warning,
	Summary:  "Retries or flaky markers were added to tests or test configuration (jest.retryTimes, { retry: n }, @pytest.mark.flaky, --reruns, @RetryingTest, rerunFailingTestsCount, ...).",
	Hint: "Retries make an intermittently failing test look green. Find out why it fails (shared state, timing, test " +
		"order, a real network call) and fix that. If it must be quarantined, do it explicitly and link an issue.",
	Docs: "A retry turns \"fails 1 time in 3\" into \"passes\". That is sometimes a pragmatic choice for end-to-end " +
		"suites, but it is also how an agent makes a test it broke pass eventually.\n\n" +
		"narc flags retries that the change introduced or raised: `jest.retryTimes`, Mocha `this.retries(n)`, " +
		"`{ retry: n }`/`{ retries: n }` test options, `test.describe.configure({ retries })`, `retries`/`retry` in " +
		"Playwright, Cypress, Vitest, Mocha and WebdriverIO config, `@pytest.mark.flaky`, `@flaky`, `--reruns`, " +
		"`@RetryingTest`, `@Flaky`, `@Retry`, TestNG `retryAnalyzer`, `[RetryFact]`, `[Retry(n)]`, `#[retry]`, " +
		"rspec-retry, Dart `retry:`, cargo-nextest `retries`, Gradle test-retry, Maven Surefire " +
		"`rerunFailingTestsCount`, `gotestsum --rerun-fails`, Bazel `--flaky_test_attempts` and `flaky = True`, " +
		"cucumber-js `--retry` and TestCafe quarantine mode.\n\n" +
		"Lowering or removing retries is never flagged, and neither is a run-wide retry count moved to another file " +
		"unchanged, retry logic in production code, or a brand-new config file (scaffolds such as Playwright's " +
		"`retries: process.env.CI ? 2 : 0`).",
}

func init() {
	retryAdded.Check = checkRetry
	core.Register(retryAdded)
}

// retryHit is a retry setting found on a line.
type retryHit struct {
	label string // the setting as written: "jest.retryTimes(3)", "--reruns 2"
	kind  string // what it is, for pairing old and new settings
	n     int    // retry count; -1 when not stated, 0 for "no retries"
	unit  bool   // attached to one test (or suite) rather than a whole run
	// inside is set when the setting sits in the body of the test or suite
	// it applies to (this.retries(2)), rather than on its declaration.
	inside bool
	// closes is set when the setting follows the test's callback on the line
	// that closes it: `}, { retry: 3 });`.
	closes bool
}

func checkRetry(a *core.Analysis) []core.Finding {
	type side struct {
		f      *core.FileInfo
		l      core.Line
		h      retryHit
		key    string
		isTest bool
		used   bool
	}
	var removed, added []*side
	for _, f := range a.Files {
		isTest := inTests(f) || isTestSetup(f.Path)
		if !isTest && !retryScope(f) {
			continue
		}
		detect := func(l core.Line, v version) []retryHit {
			if isTest && f.Config == "" && !f.IsCI {
				vt, _ := valueText(l, f.Lang)
				return retriesInTests(f, l, v, vt)
			}
			return retriesInConfig(f, l)
		}
		for _, l := range f.Removed {
			for _, h := range detect(l, oldVersion) {
				removed = append(removed, &side{f: f, l: l, h: h, key: lineKey(l), isTest: isTest})
			}
		}
		for _, l := range f.Added {
			for _, h := range detect(l, newVersion) {
				added = append(added, &side{f: f, l: l, h: h, key: lineKey(l), isTest: isTest})
			}
		}
	}
	var out []core.Finding
	for _, ad := range added {
		if ad.h.n == 0 {
			continue // retries: 0 switches retries off
		}
		// The same setting on an identical line (moved, possibly to another
		// file) is not new.
		var prev *side
		for _, r := range removed {
			if !r.used && r.h.kind == ad.h.kind && r.key == ad.key {
				prev = r
				break
			}
		}
		// Otherwise compare with the same setting removed from this file, or
		// with a run-wide setting that moved to another file (a config's
		// retries: 2 turned into --retries=2 in a script).
		if prev == nil {
			for _, r := range removed {
				if !r.used && r.h.kind == ad.h.kind && r.f == ad.f {
					prev = r
					break
				}
			}
		}
		if prev == nil && !ad.h.unit {
			for _, r := range removed {
				if !r.used && r.h.kind == ad.h.kind && !r.h.unit {
					prev = r
					break
				}
			}
		}
		if prev != nil {
			prev.used = true
			if ad.h.n < 0 || prev.h.n < 0 || ad.h.n <= prev.h.n {
				continue // unchanged or lowered
			}
			msg := fmt.Sprintf("%s: retries raised %d → %d (%s)", ad.f.Path, prev.h.n, ad.h.n, ad.h.label)
			fd := retryAdded.Finding(ad.f, ad.l.No, msg)
			fd.Before = strings.TrimSpace(prev.l.Raw)
			out = append(out, fd)
			continue
		}
		out = append(out, retryAdded.Finding(ad.f, ad.l.No, retryMessage(ad.f, ad.l, ad.h, ad.isTest)))
	}
	return out
}

func retryMessage(f *core.FileInfo, l core.Line, h retryHit, isTest bool) string {
	if h.kind == "bazel-flaky" {
		target := "a test target"
		if name := bazelTarget(f, l.No); name != "" {
			target = fmt.Sprintf("test target %q", name)
		}
		return fmt.Sprintf("%s in %s is now marked flaky = True: Bazel re-runs it up to 3 times when it fails", target, f.Path)
	}
	times := ""
	if h.n > 0 {
		times = fmt.Sprintf(" up to %d %s", h.n, plural(h.n, "time", "times"))
	}
	if h.unit {
		var name testName
		switch {
		case h.closes:
			name = testClosedBy(f, l.No)
		case !h.inside:
			name = testAfter(f, l.No)
		}
		if !name.ok() && f.Lang.IsJSLike() {
			name = jsLayoutName(f, l.No)
		}
		if !name.ok() {
			name = testAround(f, l.No)
		}
		if name.ok() {
			return fmt.Sprintf("%s now retries failures%s (%s)", named("test", name), times, h.label)
		}
	}
	if isTestSetup(f.Path) {
		return fmt.Sprintf("every test that loads %s now retries failures%s (%s)", path.Base(f.Path), times, h.label)
	}
	if !isTest {
		return fmt.Sprintf("%s now re-runs failing tests%s (%s)", f.Path, times, h.label)
	}
	return fmt.Sprintf("failing tests are now re-run%s (%s)", times, h.label)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// testSetupRe matches the setup files that test runners load before every
// test file (jest.config's setupFilesAfterEach, vitest's setupFiles,
// create-react-app's setupTests): a global jest.retryTimes(3) goes there.
var testSetupRe = regexp.MustCompile(`(?i)^(?:(?:jest|vitest|test|tests|spec)[.-]setup(?:[.-]\w+)?|setup[.-]?tests?|testSetup)\.[cm]?[jt]sx?$`)

// isTestSetup reports whether p is a test setup file.
func isTestSetup(p string) bool { return testSetupRe.MatchString(path.Base(p)) }

// retryScope reports whether a file that is not test code can carry test
// retries: test runner configuration, CI pipelines and build scripts.
// Brand-new config files are left alone: scaffolds ship with retries.
func retryScope(f *core.FileInfo) bool {
	base := path.Base(f.Path)
	if isBazelBuild(base) {
		return true // no scaffold marks a new test target flaky
	}
	if f.Diff.Kind == diff.Added {
		return false
	}
	switch f.Config {
	case core.CfgPlaywright, core.CfgCypress, core.CfgVitest, core.CfgMocha, core.CfgJest, core.CfgPytest, core.CfgPyproject,
		core.CfgSetupCfg, core.CfgTox, core.CfgMaven, core.CfgGradle, core.CfgPackageJSON, core.CfgMake, core.CfgKarma:
		return true
	}
	return f.IsCI || base == "nextest.toml" || base == "dart_test.yaml" || strings.HasPrefix(base, "wdio.") ||
		strings.HasPrefix(base, ".testcaferc") || f.Lang == lang.Shell
}

// isBazelBuild reports whether base names a Bazel BUILD file.
func isBazelBuild(base string) bool { return base == "BUILD" || base == "BUILD.bazel" }

var bazelNameRe = regexp.MustCompile(`^\s*name\s*=\s*"([^"]+)"`)

// bazelTarget returns the name of the BUILD target whose attribute is on
// new line n: the `name = "..."` of the same rule call, searched up to the
// line that opens the call and down to the one that closes it.
func bazelTarget(f *core.FileInfo, n int) string {
	for k := n - 1; k >= 1 && k > n-30; k-- {
		l, ok := f.NewLine(k)
		if !ok || strings.HasPrefix(l.Raw, ")") {
			break
		}
		if m := bazelNameRe.FindStringSubmatch(l.Raw); m != nil {
			return m[1]
		}
		if strings.HasSuffix(strings.TrimSpace(l.Raw), "(") {
			break // the rule call opens here
		}
	}
	for k := n + 1; k < n+30; k++ {
		l, ok := f.NewLine(k)
		if !ok || strings.HasPrefix(l.Raw, ")") {
			break
		}
		if m := bazelNameRe.FindStringSubmatch(l.Raw); m != nil {
			return m[1]
		}
	}
	return ""
}

var (
	numRe = regexp.MustCompile(`\b\d+\b`)

	jestRetryRe      = regexp.MustCompile(`\bjest\.retryTimes\s*\(`)
	mochaRetriesRe   = regexp.MustCompile(`\bthis\.retries\s*\(`)
	pwConfigureRe    = regexp.MustCompile(`\btest\.describe\.configure\s*\(`)
	pyFlakyRe        = regexp.MustCompile(`@(pytest\.mark\.flaky|flaky)\b`)
	pyRetryDecoRe    = regexp.MustCompile(`@((?:tenacity\.|retrying\.)?retry|backoff\.on_exception|backoff\.on_predicate)\b`)
	pyRerunOptRe     = regexp.MustCompile(`\b(?:config\.)?option\.reruns\s*=\s*(\d+)`) // conftest.py: config.option.reruns = 3
	jvmRetryRe       = regexp.MustCompile(`@(?:[a-z]\w*\.)*(RetryingTest|RepeatedIfExceptionsTest|Flaky|FlakyTest|RetryOnFailure|Retry)\b`)
	testNGRetryRe    = regexp.MustCompile(`\bretryAnalyzer\s*=`)
	csRetryRe        = regexp.MustCompile(`(?:\[|,)\s*(RetryFact|RetryTheory|SkippableRetryFact)\b|(?:\[|,)\s*(Retry)\s*\(\s*\d`)
	rustRetryRe      = regexp.MustCompile(`#\[\s*(retry|flaky_test|flaky)\b`)
	rbRetryMetaRe    = regexp.MustCompile(`[,(]\s*retry:\s*(\d+)`)
	rbRetryConfigRe  = regexp.MustCompile(`\bdefault_retry_count\s*=\s*(\d+)|\brun_with_retry\b|\bMinitest::Retry\.use!`)
	phpRetryDocRe    = regexp.MustCompile(`@retryAttempts\s+(\d+)`)
	cfgRetriesRe     = regexp.MustCompile(`(?:^|[\s{,"'])(retries|retry|specFileRetries|maxRetries|reruns)["']?\s*[:=]\s*(.+)$`)
	cypressModeRe    = regexp.MustCompile(`^\s*runMode\s*:\s*(\d+)`)
	mavenRerunRe     = regexp.MustCompile(`rerunFailingTestsCount\s*>?\s*(?:=\s*)?(\$\{[^}]*\}|\d+)`)
	gradleRetryRe    = regexp.MustCompile(`\bmaxRetries\s*(?:=|\.set\s*\()\s*(\d+)|org\.gradle\.test-retry`)
	cliRetryRe       = regexp.MustCompile(`(--reruns|--retries|--retry|--rerun-fails|--flaky_test_attempts|--max-runs|-Dsurefire\.rerunFailingTestsCount|-retry-tests-on-failure|--force-flaky|--quarantine-mode)(?:[=\s]+(\d+))?(?:[\s"',;)]|$)`)
	testRunnerRe     = regexp.MustCompile(`\b(?:pytest|py\.test|tox|playwright|vitest|jest|cypress|mocha|wdio|gotestsum|nextest|mvnw?|gradlew?|rspec|phpunit|pest|bazel|xcodebuild|testcafe|cucumber(?:-js)?|PYTEST_ADDOPTS|MAVEN_OPTS|dotnet\s+test|go\s+test|swift\s+test|cargo\s+test|flutter\s+test|dart\s+test|(?:npm|yarn|pnpm|bun)\s+(?:run\s+)?test\S*)\b`)
	bazelFlakyRe     = regexp.MustCompile(`^\s*flaky\s*=\s*True\b`)
	testcafeQuarRe   = regexp.MustCompile(`["']?quarantineMode["']?\s*:\s*(true|\{)`)
	xmlCommentLineRe = regexp.MustCompile(`^\s*<!--|-->\s*$`)
	shellSplitRe     = regexp.MustCompile(`&&|\|\||;|\|`)
)

func retriesInTests(f *core.FileInfo, l core.Line, v version, vt string) []retryHit {
	var hs []retryHit
	countOf := func(as []expr, keys ...string) int {
		for _, k := range keys {
			if v, ok := kwarg(as, k); ok {
				if n, ok := parseNumber(v.raw); ok {
					return int(n)
				}
			}
		}
		if pos := positional(as); len(pos) > 0 {
			if n, ok := parseNumber(pos[0].raw); ok {
				return int(n)
			}
		}
		return -1
	}
	call := func(m []int) []expr {
		as, _, _ := args(vt, m[1]-1)
		return as
	}
	switch f.Lang {
	case lang.JavaScript, lang.TypeScript:
		if m := jestRetryRe.FindStringIndex(vt); m != nil {
			n := countOf(call(m))
			hs = append(hs, retryHit{label: fmt.Sprintf("jest.retryTimes(%s)", count(n)), kind: "jest.retryTimes", n: n})
		}
		if m := mochaRetriesRe.FindStringIndex(vt); m != nil {
			n := countOf(call(m))
			hs = append(hs, retryHit{label: fmt.Sprintf("this.retries(%s)", count(n)), kind: "this.retries", n: n, unit: true, inside: true})
		}
		if m := pwConfigureRe.FindStringIndex(vt); m != nil {
			for _, a := range call(m) {
				if r := cfgRetriesRe.FindStringSubmatch(a.raw); r != nil {
					n := maxNumber(firstValue(r[2]))
					hs = append(hs, retryHit{label: fmt.Sprintf("test.describe.configure({ retries: %s })", count(n)), kind: "describe.configure", n: n, unit: true, inside: true})
				}
			}
		}
		// { retry: 3 } counts only as an option of the test declaration, not
		// as an option the test passes to the code under test.
		for _, o := range jsDeclOptions(f, l, v, vt) {
			if o.key != "retry" && o.key != "retries" {
				continue
			}
			hs = append(hs, retryHit{label: fmt.Sprintf("{ %s: %s }", o.key, display(o.value.raw)), kind: "option." + o.key,
				n: maxNumber(o.value.raw), unit: true, closes: o.closes})
		}
	case lang.Python:
		if m := pyFlakyRe.FindStringSubmatchIndex(vt); m != nil {
			n := -1
			if as, _, ok := args(vt, m[1]); ok && strings.HasPrefix(vt[m[1]:], "(") {
				n = countOf(as, "reruns", "max_runs", "retries")
			}
			hs = append(hs, retryHit{label: annotation(vt, m[0], m[1]), kind: vt[m[2]:m[3]], n: n, unit: true})
		}
		if m := pyRetryDecoRe.FindStringSubmatchIndex(vt); m != nil {
			if name := testAfter(f, l.No); strings.HasPrefix(name.name, "test") {
				hs = append(hs, retryHit{label: "@" + vt[m[2]:m[3]], kind: "retry-decorator", n: -1, unit: true})
			}
		}
		if m := pyRerunOptRe.FindStringSubmatch(vt); m != nil {
			hs = append(hs, retryHit{label: strings.TrimSpace(m[0]), kind: "reruns", n: atoi(m[1])})
		}
	case lang.Java, lang.Kotlin, lang.Groovy, lang.Scala:
		// @Retry and @Flaky also exist outside test frameworks (MicroProfile
		// Fault Tolerance on a bean the test exercises): require a test.
		if m := jvmRetryRe.FindStringSubmatchIndex(vt); m != nil && annotatesTest(f, l, v) {
			n := -1
			if as, _, ok := args(vt, m[1]); ok && strings.HasPrefix(vt[m[1]:], "(") {
				n = countOf(as, "value", "count", "maxAttempts", "times")
			}
			hs = append(hs, retryHit{label: annotation(vt, m[0], m[1]), kind: vt[m[2]:m[3]], n: n, unit: true})
		}
		if testNGRetryRe.MatchString(vt) {
			hs = append(hs, retryHit{label: "retryAnalyzer", kind: "retryAnalyzer", n: -1, unit: true})
		}
	case lang.CSharp:
		if m := csRetryRe.FindStringSubmatch(vt); m != nil {
			name := m[1] + m[2]
			n := -1
			if i := strings.Index(vt, name); i >= 0 {
				if as, _, ok := args(vt, i+len(name)); ok && strings.HasPrefix(vt[i+len(name):], "(") {
					n = countOf(as, "MaxRetries", "maxRetries")
				}
			}
			label := "[" + name + "]"
			if i := strings.Index(vt, name); i >= 0 {
				label = "[" + annotation(vt, i, i+len(name)) + "]"
			}
			hs = append(hs, retryHit{label: label, kind: name, n: n, unit: true})
		}
	case lang.Rust:
		if m := rustRetryRe.FindStringSubmatchIndex(vt); m != nil {
			n := -1
			if as, _, ok := args(vt, m[1]); ok && strings.HasPrefix(vt[m[1]:], "(") {
				n = countOf(as)
			}
			hs = append(hs, retryHit{label: annotation(vt, m[0], m[1]) + "]", kind: vt[m[2]:m[3]], n: n, unit: true})
		}
	case lang.Ruby:
		if meta, ok := rbDeclArgs(vt); ok {
			if m := rbRetryMetaRe.FindStringSubmatch(meta); m != nil {
				hs = append(hs, retryHit{label: "retry: " + m[1], kind: "rspec-retry", n: atoi(m[1]), unit: true})
			}
		}
		if m := rbRetryConfigRe.FindStringSubmatch(vt); m != nil {
			n := -1
			if m[1] != "" {
				n = atoi(m[1])
			}
			hs = append(hs, retryHit{label: strings.TrimSpace(m[0]), kind: "rspec-retry-config", n: n})
		}
	case lang.PHP:
		if m := phpRetryDocRe.FindStringSubmatch(l.Comment); m != nil {
			hs = append(hs, retryHit{label: "@retryAttempts " + m[1], kind: "retryAttempts", n: atoi(m[1]), unit: true})
		}
	case lang.Dart:
		if v, ok := dartTestOptions(vt)["retry"]; ok {
			if n, ok := parseNumber(v.raw); ok {
				hs = append(hs, retryHit{label: "retry: " + display(v.raw), kind: "dart-retry", n: int(n), unit: true})
			}
		}
	}
	return hs
}

func retriesInConfig(f *core.FileInfo, l core.Line) []retryHit {
	text := l.NoComment // config values often live in strings ("test": "pytest --reruns 3")
	if strings.TrimSpace(text) == "" || (f.Config == core.CfgMaven && xmlCommentLineRe.MatchString(text)) {
		return nil
	}
	var hs []retryHit
	base := path.Base(f.Path)
	switch {
	case isBazelBuild(base):
		if bazelFlakyRe.MatchString(text) {
			// Bazel runs a flaky target up to three times
			hs = append(hs, retryHit{label: "flaky = True", kind: "bazel-flaky", n: -1, unit: true})
		}
		return hs
	case strings.HasPrefix(base, ".testcaferc"):
		if testcafeQuarRe.MatchString(text) {
			hs = append(hs, retryHit{label: "quarantineMode", kind: "quarantine-mode", n: -1})
		}
	case f.Config == core.CfgPlaywright || f.Config == core.CfgCypress || f.Config == core.CfgVitest || f.Config == core.CfgMocha ||
		f.Config == core.CfgKarma || strings.HasPrefix(base, "wdio.") || base == "nextest.toml" || base == "dart_test.yaml":
		if m := cfgRetriesRe.FindStringSubmatch(text); m != nil && m[1] != "reruns" {
			v := firstValue(m[2])
			// `retries: {` opens a multi-line object; its runMode line counts.
			if n := maxNumber(v); n >= 0 || !strings.HasPrefix(v, "{") {
				hs = append(hs, retryHit{label: m[1] + ": " + clip(v, 60), kind: m[1], n: n})
			}
		} else if m := cypressModeRe.FindStringSubmatch(text); m != nil && f.Config == core.CfgCypress {
			// the same setting as a one-line `retries: { runMode: 2 }`
			hs = append(hs, retryHit{label: "retries.runMode: " + m[1], kind: "retries", n: atoi(m[1])})
		}
	case f.Config == core.CfgMaven:
		if m := mavenRerunRe.FindStringSubmatch(text); m != nil {
			n := atoi(m[1])
			if strings.HasPrefix(m[1], "${") {
				n = -1
			}
			hs = append(hs, retryHit{label: "rerunFailingTestsCount " + m[1], kind: "rerunFailingTestsCount", n: n})
		}
	case f.Config == core.CfgGradle:
		if m := gradleRetryRe.FindStringSubmatch(text); m != nil {
			n := -1
			if m[1] != "" {
				n = atoi(m[1])
			}
			hs = append(hs, retryHit{label: strings.TrimSpace(m[0]), kind: "gradle-test-retry", n: n})
		}
	case f.Config == core.CfgPytest || f.Config == core.CfgPyproject || f.Config == core.CfgSetupCfg || f.Config == core.CfgTox:
		if m := cfgRetriesRe.FindStringSubmatch(text); m != nil && m[1] == "reruns" {
			v := firstValue(m[2])
			hs = append(hs, retryHit{label: "reruns = " + v, kind: "reruns", n: maxNumber(v)})
		}
	}
	// Command-line flags in scripts, Makefiles, CI and runner config. A flag
	// counts only in a command that runs tests (not curl --retry 3).
	pytestCfg := f.Config == core.CfgPytest || f.Config == core.CfgPyproject || f.Config == core.CfgSetupCfg || f.Config == core.CfgTox
	for _, cmd := range shellSplitRe.Split(text, -1) {
		if !pytestCfg && !testRunnerRe.MatchString(cmd) {
			continue
		}
		for _, m := range cliRetryRe.FindAllStringSubmatch(cmd, -1) {
			n := -1
			if m[2] != "" {
				n = atoi(m[2])
			}
			hs = append(hs, retryHit{label: strings.TrimSpace(m[1] + " " + m[2]), kind: strings.TrimLeft(m[1], "-"), n: n})
		}
	}
	return hs
}

// annotation returns the annotation or call at vt[start:nameEnd] together
// with its argument list, if one follows: @RetryingTest(3).
func annotation(vt string, start, nameEnd int) string {
	end := nameEnd
	if nameEnd < len(vt) && vt[nameEnd] == '(' {
		if c, ok := closing(vt, nameEnd); ok {
			end = c + 1
		}
	}
	return display(vt[start:end])
}

// firstValue cuts a config value at the end of its entry:
// "process.env.CI ? 2 : 0, workers: 4" → "process.env.CI ? 2 : 0".
func firstValue(s string) string {
	return strings.TrimRight(strings.TrimSpace(splitTop(s, ',')[0]), " }")
}

// maxNumber returns the largest integer in s (retries: process.env.CI ? 2 : 0
// → 2), -1 when s has none, and 0 when every number is 0.
func maxNumber(s string) int {
	best := -1
	for _, m := range numRe.FindAllString(s, -1) {
		if n := atoi(m); n > best {
			best = n
		}
	}
	return best
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func count(n int) string {
	if n < 0 {
		return "n"
	}
	return fmt.Sprint(n)
}
