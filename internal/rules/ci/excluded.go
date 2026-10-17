package ci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

var testsExcluded = &core.Rule{
	ID:       "tests-excluded",
	Title:    "Tests excluded from the run",
	Category: "ci",
	Severity: core.Warning,
	Summary:  "Test files or directories were added to an ignore/exclude list, a test path list was narrowed, or a Go test file got `//go:build ignore`.",
	Hint: "Remove the exclusion and fix the excluded tests. If they belong to a different suite (e2e, " +
		"integration), make sure that suite runs them in the same change; if they are obsolete, delete them " +
		"in a reviewed change instead of hiding them.",
	Docs: "Excluding a directory from the test runner deletes its tests without touching them. narc " +
		"compares the runner configuration before and after the change and reports newly excluded test " +
		"paths:\n\n" +
		"```diff\n   testPathIgnorePatterns: [\n     '/node_modules/',\n+    '<rootDir>/src/billing/',   // SUSPICIOUS\n   ],\n```\n\n" +
		"Covered: jest `testPathIgnorePatterns`/`modulePathIgnorePatterns` and narrowed `testMatch`/`roots`; " +
		"vitest `test.exclude` and narrowed `test.include`; mocha `ignore` and narrowed `spec`; pytest " +
		"`addopts` gaining `--ignore`/`--deselect`/`-k`/`-m \"not ...\"`, `norecursedirs` and narrowed " +
		"`testpaths` and `python_files`/`python_functions`/`python_classes`; `collect_ignore` in conftest.py; " +
		"test globs added to tsconfig.json `exclude`; Maven surefire, Gradle and PHPUnit excludes; " +
		"`//go:build ignore` or a contradictory constraint (`linux && !linux`) on a `_test.go` file (error). " +
		"An exclusion is fine when the same change runs the excluded tests elsewhere (a new e2e job, a " +
		"separate config), or a sibling tsconfig (tsconfig.spec.json) already includes them.\n\n" +
		"Info: source files newly omitted from coverage measurement (`omit`, `coveragePathIgnorePatterns`, " +
		"codecov `ignore`), which inflates the coverage number.",
}

func init() {
	testsExcluded.Check = scoped(checkExcluded)
	core.Register(testsExcluded)
}

func checkExcluded(a *core.Analysis) []core.Finding {
	ctx := &excludeCtx{a: a, newText: newCommandText(a)}
	var out []core.Finding
	for _, f := range a.Files {
		if len(f.Added)+len(f.Removed) == 0 {
			continue
		}
		base := strings.ToLower(path.Base(f.Path))
		switch {
		case f.Lang == lang.Go && strings.HasSuffix(base, "_test.go"):
			out = append(out, goBuildIgnore(f)...)
			continue
		case base == "conftest.py":
			out = append(out, collectIgnore(ctx, f)...)
			continue
		case f.Config == core.CfgMaven, f.Config == core.CfgGradle, f.Config == core.CfgPHPUnit:
			out = append(out, buildExcludes(ctx, f)...)
			continue
		}
		if !isConfigFile(f) {
			continue
		}
		old, new, ok := configSides(f)
		if !ok {
			continue
		}
		for _, spec := range listSpecs {
			if spec.applies(f) {
				out = append(out, ctx.compareList(f, spec, old, new)...)
			}
		}
		out = append(out, ctx.pytestAddopts(f, old, new)...)
	}
	return out
}

// excludeCtx carries changeset-wide context for recognizing exclusions
// that are compensated elsewhere.
type excludeCtx struct {
	a       *core.Analysis
	newText []string
}

// listKind says what adding to (or removing from) a list means.
type listKind int

const (
	listExclude      listKind = iota // added test paths are excluded
	listInclude                      // removed paths are no longer run
	listCoverageOmit                 // added source paths stop counting toward coverage
)

// listSpec describes one runner setting that is a list of paths.
type listSpec struct {
	name    string
	kind    listKind
	configs []string                    // config kinds the setting lives in
	match   func(p []string) bool       // whether an entry path is this setting
	only    func(f *core.FileInfo) bool // optional extra file filter
	effect  string                      // what an exclusion means (default "those tests no longer run")
}

func (s listSpec) applies(f *core.FileInfo) bool {
	for _, c := range s.configs {
		if f.Config == c || c == "*" {
			return s.only == nil || s.only(f)
		}
	}
	return false
}

// under reports whether p is key (normalized) directly under one of the
// given parents, optionally below the package.json section pkg.
func under(p []string, key string, parents ...string) bool {
	if len(p) == 0 || normKey(p[len(p)-1]) != normKey(key) {
		return false
	}
	if len(parents) == 0 {
		return true
	}
	if len(p) < 2 {
		return parents[0] == ""
	}
	for _, par := range parents {
		if par == "" && len(p) == 1 || len(p) >= 2 && normKey(p[len(p)-2]) == normKey(par) {
			return true
		}
	}
	return false
}

func jestKey(key string) func([]string) bool {
	return func(p []string) bool {
		if len(p) > 0 && p[0] == "jest" {
			p = p[1:]
		}
		return len(p) >= 1 && p[len(p)-1] == key && (len(p) == 1 || p[len(p)-2] == "projects" || indexOf(p, "projects") >= 0)
	}
}

func isTSConfigRoot(f *core.FileInfo) bool {
	b := strings.ToLower(path.Base(f.Path))
	return b == "tsconfig.json" || b == "jsconfig.json"
}

var listSpecs = []listSpec{
	{name: "jest testPathIgnorePatterns", kind: listExclude, configs: []string{core.CfgJest, core.CfgPackageJSON}, match: jestKey("testPathIgnorePatterns")},
	{name: "jest modulePathIgnorePatterns", kind: listExclude, configs: []string{core.CfgJest, core.CfgPackageJSON}, match: jestKey("modulePathIgnorePatterns")},
	{name: "jest testMatch", kind: listInclude, configs: []string{core.CfgJest, core.CfgPackageJSON}, match: jestKey("testMatch")},
	{name: "jest roots", kind: listInclude, configs: []string{core.CfgJest, core.CfgPackageJSON}, match: jestKey("roots")},
	{name: "jest coveragePathIgnorePatterns", kind: listCoverageOmit, configs: []string{core.CfgJest, core.CfgPackageJSON}, match: jestKey("coveragePathIgnorePatterns")},
	{name: "vitest test.exclude", kind: listExclude, configs: []string{core.CfgVitest}, match: func(p []string) bool { return under(p, "exclude", "test") }},
	{name: "vitest test.include", kind: listInclude, configs: []string{core.CfgVitest}, match: func(p []string) bool { return under(p, "include", "test") }},
	{name: "vitest coverage.exclude", kind: listCoverageOmit, configs: []string{core.CfgVitest}, match: func(p []string) bool { return under(p, "exclude", "coverage") }},
	{name: "mocha ignore", kind: listExclude, configs: []string{core.CfgMocha, core.CfgPackageJSON}, match: func(p []string) bool {
		return under(p, "ignore", "", "mocha") || under(p, "exclude", "", "mocha")
	}},
	{name: "mocha spec", kind: listInclude, configs: []string{core.CfgMocha, core.CfgPackageJSON}, match: func(p []string) bool { return under(p, "spec", "", "mocha") }},
	{name: "pytest norecursedirs", kind: listExclude, configs: []string{core.CfgPytest, core.CfgSetupCfg, core.CfgTox, core.CfgPyproject}, match: pytestKey("norecursedirs")},
	{name: "pytest testpaths", kind: listInclude, configs: []string{core.CfgPytest, core.CfgSetupCfg, core.CfgTox, core.CfgPyproject}, match: pytestKey("testpaths")},
	{name: "pytest python_files", kind: listInclude, configs: []string{core.CfgPytest, core.CfgSetupCfg, core.CfgTox, core.CfgPyproject}, match: pytestKey("python-files"),
		effect: "test modules matching it are no longer collected"},
	{name: "pytest python_functions", kind: listInclude, configs: []string{core.CfgPytest, core.CfgSetupCfg, core.CfgTox, core.CfgPyproject}, match: pytestKey("python-functions"),
		effect: "test functions matching it are no longer collected"},
	{name: "pytest python_classes", kind: listInclude, configs: []string{core.CfgPytest, core.CfgSetupCfg, core.CfgTox, core.CfgPyproject}, match: pytestKey("python-classes"),
		effect: "test classes matching it are no longer collected"},
	{name: "tsconfig.json exclude", kind: listExclude, configs: []string{core.CfgTSConfig}, only: isTSConfigRoot, match: func(p []string) bool { return len(p) == 1 && p[0] == "exclude" },
		effect: "those tests are no longer type-checked"},
	{name: "coverage omit", kind: listCoverageOmit, configs: []string{core.CfgCoverage, core.CfgSetupCfg, core.CfgTox, core.CfgPyproject}, match: func(p []string) bool {
		if len(p) == 0 || p[len(p)-1] != "omit" {
			return false
		}
		sec := strings.Join(p[:len(p)-1], ".")
		return sec == "run" || sec == "report" || sec == "coverage:run" || sec == "coverage:report" || sec == "tool.coverage.run" || sec == "tool.coverage.report"
	}},
	{name: "nyc exclude", kind: listCoverageOmit, configs: []string{core.CfgNYC, core.CfgPackageJSON}, match: func(p []string) bool {
		return under(p, "exclude", "", "nyc", "c8")
	}},
	{name: "codecov ignore", kind: listCoverageOmit, configs: []string{core.CfgCodecov}, match: func(p []string) bool { return len(p) == 1 && p[0] == "ignore" }},
}

// pytestKey matches a pytest ini option in pytest.ini [pytest], setup.cfg
// [tool:pytest], tox.ini [pytest] and pyproject [tool.pytest.ini_options].
func pytestKey(key string) func([]string) bool {
	return func(p []string) bool {
		switch {
		case len(p) == 2:
			return (p[0] == "pytest" || p[0] == "tool:pytest") && normKey(p[1]) == key
		case len(p) == 4:
			return p[0] == "tool" && p[1] == "pytest" && p[2] == "ini_options" && normKey(p[3]) == key
		}
		return false
	}
}

// listValues returns the (split) values of a list setting.
func listValues(es []entry, match func([]string) bool) []entry {
	var out []entry
	for _, e := range es {
		if !match(e.Path) {
			continue
		}
		for _, v := range strings.FieldsFunc(e.Value, func(r rune) bool { return r == '\n' || r == ',' && !strings.ContainsAny(e.Value, "{[") }) {
			if v = strings.TrimSpace(v); v != "" {
				for _, part := range strings.Fields(v) {
					c := e
					c.Value = part
					out = append(out, c)
				}
			}
		}
	}
	return out
}

func (ctx *excludeCtx) compareList(f *core.FileInfo, spec listSpec, old, new []entry) []core.Finding {
	ov, nv := listValues(old, spec.match), listValues(new, spec.match)
	had := map[string]bool{}
	for _, e := range ov {
		had[normPattern(e.Value)] = true
	}
	has := map[string]bool{}
	for _, e := range nv {
		has[normPattern(e.Value)] = true
	}
	var out []core.Finding
	switch spec.kind {
	case listExclude, listCoverageOmit:
		var added []entry
		for _, e := range nv {
			if !had[normPattern(e.Value)] {
				added = append(added, e)
			}
		}
		for _, e := range added {
			if spec.kind == listExclude {
				if !excludesTests(e.Value) || ctx.runsElsewhere(f, e.Value, nil) {
					continue
				}
				if f.Config == core.CfgTSConfig && ctx.siblingTSConfigIncludes(f, e.Value) {
					continue // tsconfig.test.json next to it type-checks the tests
				}
				effect := spec.effect
				if effect == "" {
					effect = "those tests no longer run"
				}
				out = append(out, sevFinding(testsExcluded, f, e.Line, core.Warning,
					fmt.Sprintf("%s gained %s: %s", spec.name, quote(e.Value), effect)))
				continue
			}
			if looksLikeSource(e.Value) {
				out = append(out, sevFinding(testsExcluded, f, e.Line, core.Info,
					fmt.Sprintf("%s gained %s: that code no longer counts toward coverage", spec.name, quote(e.Value))))
			}
		}
	case listInclude:
		if len(nv) == 0 {
			return nil // setting removed: the runner falls back to its defaults
		}
		own := map[int]bool{} // the narrowed list itself does not count as running the path
		for _, e := range nv {
			own[e.Line] = true
		}
		for _, e := range ov {
			if has[normPattern(e.Value)] || coveredByPatterns(e.Value, nv) || ctx.runsElsewhere(f, e.Value, own) {
				continue
			}
			var now []string
			for _, n := range nv {
				now = append(now, n.Value)
			}
			effect := spec.effect
			if effect == "" {
				effect = "tests there no longer run"
			}
			out = append(out, removalFinding(testsExcluded, f, e.Line, core.Warning,
				fmt.Sprintf("%s no longer includes %s (now %s): %s", spec.name, quote(e.Value), strings.Join(now, ", "), effect)))
		}
	}
	return out
}

// normPattern canonicalizes a path pattern for comparison.
func normPattern(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	p = strings.TrimPrefix(p, "<rootDir>/")
	p = strings.TrimPrefix(p, "./")
	return strings.TrimSuffix(p, "/")
}

// coveredByPatterns reports whether a removed include pattern is still
// matched by one of the remaining patterns ("tests/**/*.test.ts" by
// "**/*.test.ts", "tests/unit" by "tests").
func coveredByPatterns(removed string, remaining []entry) bool {
	r := normPattern(removed)
	for _, e := range remaining {
		n := normPattern(e.Value)
		if n == "." || n == "**" || strings.HasPrefix(r, n+"/") || core.MatchGlob(n, r) {
			return true
		}
	}
	return false
}

// significant returns the literal part of a path pattern: "<rootDir>/e2e/"
// → "e2e", "tests/integration/**" → "tests/integration".
func significant(p string) string {
	p = normPattern(strings.ReplaceAll(p, `\`, ""))
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "*?[]()^$|") {
			continue
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return strings.Trim(strings.NewReplacer("*", "", "^", "", "$", "", "(", "", ")", "").Replace(p), "./")
	}
	return strings.Join(segs, "/")
}

// testToken returns the part of an exclusion pattern that identifies the
// tests it drops: its literal directories when they name tests
// ("tests/integration"), else the literal suffix of its file glob
// ("src/**/*.spec.ts" → ".spec.ts"), so that a line merely naming "src"
// does not count as running them.
func testToken(p string) string {
	sig := significant(p)
	if looksLikeTestPath(sig) {
		return sig
	}
	last := normPattern(p)
	if i := strings.LastIndex(last, "/"); i >= 0 {
		last = last[i+1:]
	}
	if j := strings.LastIndexAny(last, "*?]"); j >= 0 {
		last = last[j+1:]
	}
	if looksLikeTestPath(last) && len(last) >= 4 {
		return last
	}
	return sig
}

// runsElsewhere reports whether the change runs the excluded path some
// other way: a verification command targets it, or another added line
// outside an exclusion list names it (a separate config or project). Lines
// of f in skip are not considered.
func (ctx *excludeCtx) runsElsewhere(f *core.FileInfo, pattern string, skip map[int]bool) bool {
	sig := testToken(pattern)
	if len(sig) < 3 {
		return false
	}
	for _, t := range ctx.newText {
		for _, iv := range invocations(t) {
			if _, ok := classify(iv); !ok {
				continue
			}
			for _, tg := range targets(iv) {
				if strings.Contains(normTarget(tg), sig) {
					return true
				}
			}
		}
	}
	for _, g := range ctx.a.Files {
		for _, l := range g.Added {
			if g == f && (skip[l.No] || exclusionLine(f, l.No)) {
				continue
			}
			if strings.Contains(l.Raw, sig) && !exclusionWords.MatchString(l.Raw) {
				return true
			}
		}
	}
	return false
}

// siblingTSConfigIncludes reports whether another tsconfig*.json in the
// same directory (changed or not) includes what f now excludes, as when a
// build tsconfig leaves the tests to an existing tsconfig.test.json.
func (ctx *excludeCtx) siblingTSConfigIncludes(f *core.FileInfo, pattern string) bool {
	sig := testToken(pattern)
	if len(sig) < 3 {
		return false
	}
	dir := path.Dir(f.Path)
	if dir == "." {
		dir = ""
	}
	paths, ok := ctx.a.ListNew(dir)
	if !ok {
		return false
	}
	r := repoOf(ctx.a)
	for _, p := range paths {
		b := strings.ToLower(path.Base(p))
		if p == f.Path || path.Dir(p) != path.Dir(f.Path) || !strings.HasPrefix(b, "tsconfig") || !strings.HasSuffix(b, ".json") {
			continue
		}
		d, ok := r.get(p, false)
		if !ok {
			continue
		}
		for _, e := range parseConfig(fmtJSON, d.s.text(), d.s.lines) {
			if len(e.Path) == 1 && (e.Path[0] == "include" || e.Path[0] == "files") && strings.Contains(e.Value, sig) {
				return true
			}
		}
	}
	return false
}

var exclusionWords = regexp.MustCompile(`(?i)(ignore|exclude|omit|skip|norecurse|deselect)`)

// exclusionLine reports whether line n of f belongs to an exclusion list
// (the line itself or the list it continues).
func exclusionLine(f *core.FileInfo, n int) bool {
	for i := n; i >= 1 && i >= n-30; i-- {
		l, ok := f.NewLine(i)
		if !ok {
			return false
		}
		if exclusionWords.MatchString(l.Raw) {
			return true
		}
		if i < n && strings.ContainsAny(l.Code, "[{=:") && !strings.HasSuffix(strings.TrimSpace(l.Code), ",") {
			return false
		}
	}
	return false
}

var (
	// supportDirRe matches test-support code that is not itself tests.
	supportDirRe = regexp.MustCompile(`(?i)(^|[/*])(fixtures?|__fixtures__|testdata|test[-_]data|__mocks__|mocks?|helpers?|utils?|support|setup|snapshots?|__snapshots__|resources|stubs?|factories)([/*.]|$)`)
	// e2eRe matches end-to-end suites, which usually have their own runner.
	e2eRe = regexp.MustCompile(`(?i)(^|[/*._-])(e2e|playwright|cypress|acceptance|visual)([/*._-]|$)`)
)

// excludesTests reports whether an exclusion pattern drops unit or
// integration tests (not fixtures, helpers or a separately-run e2e suite).
func excludesTests(p string) bool {
	return looksLikeTestPath(p) && !supportDirRe.MatchString(p) && !e2eRe.MatchString(p)
}

// benignOmitRe matches paths that are routinely excluded from coverage.
var benignOmitRe = regexp.MustCompile(`(?i)(migrations?|generated|__generated__|vendor|node_modules|dist|build|\.d\.ts|__init__\.py|setup\.py|conftest|docs?(/|$)|scripts?(/|$)|stories|\.stories\.|__mocks__|mocks?(/|$)|fixtures?|\.config\.|config(/|$)|settings(/|\.py$|$)|wsgi\.py|asgi\.py|manage\.py|coverage|venv|site-packages|\.tox|_pb2|\.pb\.go|main\.py$|cmd/|__main__\.py|\.gen\.|typings?|types\.ts$|\bbin/)`)

// looksLikeSource reports whether an omitted path is application code.
func looksLikeSource(p string) bool {
	return !looksLikeTestPath(p) && !benignOmitRe.MatchString(p) && strings.Trim(p, `"'*/. `) != ""
}

// ---- pytest addopts ----

var addoptsFlags = []struct {
	flags []string
	why   string
}{
	{[]string{"--ignore", "--ignore-glob"}, "stops pytest from collecting those tests"},
	{[]string{"--deselect"}, "deselects that test"},
	{[]string{"-k"}, "deselects the matching tests"},
	{[]string{"-m"}, "deselects tests by marker"},
}

// pytestAddopts reports exclusions added to pytest's addopts.
func (ctx *excludeCtx) pytestAddopts(f *core.FileInfo, old, new []entry) []core.Finding {
	match := pytestKey("addopts")
	join := func(es []entry) ([]string, int) {
		var parts []string
		line := 0
		for _, e := range es {
			if match(e.Path) {
				parts = append(parts, e.Value)
				if line == 0 {
					line = e.Line
				}
			}
		}
		var words []string
		for _, sc := range splitShell(strings.Join(parts, " ")) {
			words = append(words, sc.Words...)
		}
		return words, line
	}
	ow, _ := join(old)
	nw, line := join(new)
	if len(nw) == 0 {
		return nil
	}
	lineOf := func(text string) int {
		for _, e := range new {
			if match(e.Path) && strings.Contains(e.Value, text) {
				return e.Line
			}
		}
		return line
	}
	var out []core.Finding
	for _, af := range addoptsFlags {
		nvals := allFlagValues(nw, af.flags)
		ovals := map[string]bool{}
		for _, v := range allFlagValues(ow, af.flags) {
			ovals[v] = true
		}
		for _, v := range nvals {
			if ovals[v] {
				continue
			}
			if (af.flags[0] == "-k" || af.flags[0] == "-m") && !strings.Contains(v, "not ") {
				continue // a positive selection; only "not ..." drops tests
			}
			if af.flags[0] == "--ignore" && (!excludesTests(v) || ctx.runsElsewhere(f, v, nil)) {
				continue
			}
			nf := narrowFlag{flags: af.flags}
			if splitRun(nf, v, ctx.newText) {
				continue
			}
			shown := v
			if strings.ContainsAny(v, " \t") {
				shown = quote(v)
			}
			out = append(out, sevFinding(testsExcluded, f, lineOf(v), core.Warning,
				fmt.Sprintf("pytest addopts gained `%s %s`, which %s on every run", af.flags[0], shown, af.why)))
		}
	}
	return out
}

// allFlagValues returns every value of any spelling of a flag.
func allFlagValues(words []string, spellings []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		for _, f := range spellings {
			switch {
			case words[i] == f && i+1 < len(words):
				out = append(out, words[i+1])
				i++
			case strings.HasPrefix(words[i], f+"="):
				out = append(out, words[i][len(f)+1:])
			}
		}
	}
	return out
}

// ---- conftest.py collect_ignore ----

var collectIgnoreRe = regexp.MustCompile(`\bcollect_ignore(_glob)?\b\s*(=|\+=|\.append\(|\.extend\(|\.insert\()`)

// collectIgnore reports paths added to collect_ignore(_glob) in conftest.py.
func collectIgnore(ctx *excludeCtx, f *core.FileInfo) []core.Finding {
	s := newSide(f)
	inList := map[int]bool{}
	for i := 1; i <= len(s.lines); i++ {
		l, ok := s.get(i)
		if !ok || !collectIgnoreRe.MatchString(l.Code) {
			continue
		}
		inList[i] = true
		// a multi-line list continues until the closing bracket
		depth := strings.Count(l.Code, "[") + strings.Count(l.Code, "(") - strings.Count(l.Code, "]") - strings.Count(l.Code, ")")
		for j := i + 1; depth > 0 && j <= len(s.lines); j++ {
			nl, ok := s.get(j)
			if !ok {
				break
			}
			inList[j] = true
			depth += strings.Count(nl.Code, "[") + strings.Count(nl.Code, "(") - strings.Count(nl.Code, "]") - strings.Count(nl.Code, ")")
		}
	}
	removed := multiset{}
	for _, l := range f.Removed {
		for _, str := range l.Strings {
			removed.add(str)
		}
	}
	var out []core.Finding
	for _, l := range f.Added {
		if !inList[l.No] {
			continue
		}
		for _, str := range l.Strings {
			if removed.take(str) || !looksLikeTestPath(str) || ctx.runsElsewhere(f, str, nil) {
				continue
			}
			out = append(out, testsExcluded.Finding(f, l.No,
				fmt.Sprintf("conftest.py collect_ignore now skips %s: pytest no longer collects the tests there", quote(str))))
		}
	}
	return out
}

// ---- Go build constraints ----

var (
	goBuildRe       = regexp.MustCompile(`^\s*//\s*(?:go:build|\+build)\s+(.+)$`)
	impossibleTagRe = regexp.MustCompile(`^(ignore|never|skip|skiptests?|disabled?|exclude|none|false|nope|off)$`)
	goTestFuncRe    = regexp.MustCompile(`(?m)^func\s+(Test|Benchmark|Fuzz|Example)\w*\s*\(`)
)

// goBuildIgnore reports a build constraint that excludes an existing test
// file from every build.
func goBuildIgnore(f *core.FileInfo) []core.Finding {
	if f.Diff.Kind == diff.Added {
		return nil
	}
	if impossibleConstraint(oldSide(f), 0) != "" {
		return nil // already excluded (e.g. "// +build ignore" gained its //go:build twin)
	}
	removed := multiset{}
	for _, l := range f.Removed {
		removed.add(rulekit.Normalize(l.Raw))
	}
	ns := newSide(f)
	var out []core.Finding
	for _, l := range f.Added {
		if removed.take(rulekit.Normalize(l.Raw)) {
			continue
		}
		tag := impossibleConstraint(ns, l.No)
		if tag == "" {
			continue
		}
		n := 0
		if b, ok := f.NewContent(); ok {
			n = len(goTestFuncRe.FindAll(b, -1))
		}
		what, verb := "the tests in this file", "never compile or run"
		switch {
		case n == 1:
			what, verb = "its only test", "never compiles or runs"
		case n > 1:
			what = "its " + plural(n, "test")
		}
		out = append(out, sevFinding(testsExcluded, f, l.No, core.Error,
			fmt.Sprintf("`%s` excludes %s from every build, so %s %s", strings.TrimSpace(l.Raw), path.Base(f.Path), what, verb)))
	}
	return out
}

// impossibleConstraint returns the impossible tag ("ignore") of a Go build
// constraint on line n of s, or of any constraint in the file header when n
// is 0. Only real constraints count: line comments before the package
// clause, not text inside a raw string or a doc comment further down.
func impossibleConstraint(s side, n int) string {
	for i := 1; i <= len(s.lines); i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		code := strings.TrimSpace(l.Code)
		if strings.HasPrefix(code, "package ") || code == "package" {
			return "" // constraints must precede the package clause
		}
		if code != "" {
			return "" // a fragment that does not start with the file header
		}
		if n != 0 && i != n {
			continue
		}
		m := goBuildRe.FindStringSubmatch(l.Raw)
		if m == nil || strings.Contains(m[1], "||") {
			continue
		}
		tags := map[string]bool{}
		for _, t := range strings.FieldsFunc(m[1], func(r rune) bool { return r == '&' || r == ' ' || r == '(' || r == ')' || r == ',' }) {
			if impossibleTagRe.MatchString(t) {
				return t
			}
			tags[t] = true
		}
		// "linux && !linux": a tag required and excluded at once. In a
		// //go:build line the terms are joined by && (|| was ruled out
		// above); in a legacy +build line commas mean AND, spaces OR.
		allAnd := strings.Contains(l.Raw, "go:build") || !strings.Contains(strings.TrimSpace(m[1]), " ")
		if allAnd && !strings.Contains(m[1], "(") { // !(a && !a) is always true
			for t := range tags {
				if !strings.HasPrefix(t, "!") && tags["!"+t] {
					return t + " && !" + t
				}
			}
		}
	}
	return ""
}

// ---- Maven / Gradle / PHPUnit ----

var (
	xmlExcludeRe    = regexp.MustCompile(`<exclude>\s*([^<]+?)\s*</exclude>`)
	gradleExcludeRe = regexp.MustCompile(`\b(exclude|excludeTestsMatching)\s*\(?\s*["']([^"']+)["']`)
)

// buildExcludes reports test patterns excluded in build files.
func buildExcludes(ctx *excludeCtx, f *core.FileInfo) []core.Finding {
	s := newSide(f)
	removed := multiset{}
	for _, l := range f.Removed {
		removed.add(rulekit.Normalize(l.NoComment))
	}
	newRaw := s.text()
	var out []core.Finding
	for _, l := range f.Added {
		if removed.take(rulekit.Normalize(l.NoComment)) {
			continue
		}
		var pat string
		switch f.Config {
		case core.CfgGradle:
			m := gradleExcludeRe.FindStringSubmatch(l.NoComment)
			if m == nil {
				continue
			}
			if !gradleTestBlk.MatchString(enclosingBlock(s, l.No)) && m[1] != "excludeTestsMatching" {
				continue
			}
			pat = m[2]
		default:
			m := xmlExcludeRe.FindStringSubmatch(l.NoComment)
			if m == nil {
				continue
			}
			pat = m[1]
			if f.Config == core.CfgMaven {
				if !insideSurefire(s, l.No) {
					continue
				}
				// Integration tests excluded from surefire because failsafe runs them.
				if strings.Contains(pat, "IT") && strings.Contains(newRaw, "maven-failsafe-plugin") {
					continue
				}
			}
			if f.Config == core.CfgPHPUnit && !insideTestsuite(s, l.No) {
				if looksLikeSource(pat) {
					out = append(out, sevFinding(testsExcluded, f, l.No, core.Info,
						fmt.Sprintf("PHPUnit coverage now excludes %s: that code no longer counts toward coverage", quote(pat))))
				}
				continue
			}
		}
		if !looksLikeTestPath(pat) && !strings.Contains(pat, "Test") || ctx.runsElsewhere(f, pat, nil) {
			continue
		}
		out = append(out, sevFinding(testsExcluded, f, l.No, core.Warning,
			fmt.Sprintf("%s now excludes tests matching %s", buildToolName(f), quote(pat))))
	}
	return out
}

func buildToolName(f *core.FileInfo) string {
	switch f.Config {
	case core.CfgGradle:
		return "the Gradle test task"
	case core.CfgPHPUnit:
		return "the PHPUnit test suite"
	}
	return "Maven surefire"
}

// insideTestsuite reports whether line n is inside a PHPUnit <testsuite>.
func insideTestsuite(s side, n int) bool {
	for i := n; i >= 1; i-- {
		if l, ok := s.get(i); ok {
			switch {
			case strings.Contains(l.Raw, "</testsuite>") && i < n:
				return false
			case strings.Contains(l.Raw, "<testsuite"):
				return true
			case strings.Contains(l.Raw, "<coverage") || strings.Contains(l.Raw, "<source"):
				return false
			}
		}
	}
	return false
}
