package ci

import (
	"path"
	"strings"
	"unicode"
)

// check describes a command that verifies code: a test runner, linter, type
// checker, format checker or coverage gate.
type check struct {
	// Tool names the command in messages: "pytest", "npm test", "go vet",
	// "make lint", "./scripts/test.sh".
	Tool string
	// Category is test, lint, typecheck, format, coverage, or check (an
	// umbrella target such as "make check" or "npm run verify").
	Category string
	// Eco is the ecosystem (python, js, go, rust, jvm, ...), or "" for
	// ecosystem-neutral entry points such as make targets and scripts.
	Eco string
}

// Verification categories.
const (
	catTest     = "test"
	catLint     = "lint"
	catTypes    = "typecheck"
	catFormat   = "format"
	catCoverage = "coverage"
	catUmbrella = "check"
	ecoAny      = ""
	ecoJS       = "js"
	ecoPython   = "python"
	ecoGo       = "go"
	ecoRust     = "rust"
	ecoJVM      = "jvm"
	ecoRuby     = "ruby"
	ecoPHP      = "php"
	ecoDotnet   = "dotnet"
	ecoSwift    = "swift"
	ecoElixir   = "elixir"
	ecoDart     = "dart"
	ecoDeno     = "deno"
	ecoNative   = "native"
)

// covers reports whether running c still provides the verification that
// removed provided: the same category (or an umbrella target) in the same
// ecosystem (or an ecosystem-neutral entry point such as "make test").
func (c check) covers(removed check) bool {
	if c.Eco != removed.Eco && c.Eco != ecoAny && removed.Eco != ecoAny {
		return false
	}
	if c.Category == removed.Category || c.Category == catUmbrella || removed.Category == catUmbrella {
		return true
	}
	// A coverage gate runs the tests, and running tests with a runner that
	// enforces coverage counts as running tests.
	return c.Category == catCoverage && removed.Category == catTest
}

// Words in script, target, job and task names that mark them as
// verification, mapped to their category.
var verifyWords = map[string]string{
	"test": catTest, "tests": catTest, "testing": catTest, "spec": catTest, "specs": catTest, "e2e": catTest,
	"unittest": catTest, "unittests": catTest, "integration": catTest, "pytest": catTest, "jest": catTest,
	"vitest": catTest, "mocha": catTest, "rspec": catTest, "playwright": catTest, "cypress": catTest,
	"lint": catLint, "lints": catLint, "linter": catLint, "linting": catLint, "eslint": catLint, "stylelint": catLint,
	"ruff": catLint, "flake8": catLint, "pylint": catLint, "clippy": catLint, "vet": catLint, "rubocop": catLint,
	"golangci": catLint, "staticcheck": catLint,
	"typecheck": catTypes, "typechecks": catTypes, "tsc": catTypes, "mypy": catTypes,
	"pyright": catTypes, "phpstan": catTypes, "psalm": catTypes,
	"coverage": catCoverage, "cov": catCoverage,
	"check": catUmbrella, "checks": catUmbrella, "verify": catUmbrella, "validate": catUmbrella,
	"ci": catUmbrella, "qa": catUmbrella, "precommit": catUmbrella,
}

// Words that make a name about something other than running verification
// ("lint:fix", "test:watch", "check-updates", "test:update-snapshots").
var nonVerifyWords = map[string]bool{
	"fix": true, "watch": true, "update": true, "updates": true, "open": true, "ui": true, "debug": true,
	"dev": true, "serve": true, "generate": true, "gen": true, "init": true, "clean": true, "setup": true,
	"install": true, "prepare": true, "report": true, "upload": true, "bench": true, "benchmark": true,
	"build": true, "deploy": true, "release": true, "publish": true, "docs": true, "doc": true,
	"snapshot": true, "snapshots": true, "deps": true, "outdated": true,
	// steps that only publish results produced by an earlier check
	"badge": true, "badges": true, "comment": true, "annotate": true, "summary": true, "notify": true,
	// telemetry about the run, caches: ci_analytics_extract.py, "Restore verify cache"
	"analytics": true, "metrics": true, "telemetry": true, "extract": true, "stats": true, "restore": true,
	"cache": true,
}

// nameWords splits a script/target name into lower-case words on
// punctuation and camelCase boundaries: "test:unit" → [test unit],
// "typeCheck" → [type check].
func nameWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = nil
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(rs[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

// verificationName reports whether a script, make target, CI job or task
// name denotes verification, and its category.
func verificationName(name string) (string, bool) {
	words := nameWords(name)
	cat := ""
	for i, w := range words {
		if nonVerifyWords[w] {
			return "", false
		}
		c, ok := verifyWords[w]
		// "type-check", "check-types", "lint:types"
		if (w == "type" || w == "types") && (i+1 < len(words) && words[i+1] == "check" || i > 0 && (words[i-1] == "check" || words[i-1] == "lint")) {
			c, ok = catTypes, true
		}
		if ok && (cat == "" || cat == catUmbrella) {
			cat = c
		}
	}
	return cat, cat != ""
}

// pmFlagsWithValue are package-manager flags that consume the next word.
var pmFlagsWithValue = map[string]bool{
	"--prefix": true, "-C": true, "--dir": true, "--filter": true, "-F": true, "-w": true, "--workspace": true,
	"--cwd": true, "--reporter": true, "--loglevel": true, "--registry": true, "--cache": true, "--userconfig": true,
}

// Built-in package-manager subcommands that are never scripts.
var pmBuiltins = map[string]bool{
	"install": true, "i": true, "ci": true, "add": true, "remove": true, "rm": true, "uninstall": true,
	"publish": true, "version": true, "why": true, "outdated": true, "upgrade": true, "update": true, "up": true,
	"audit": true, "init": true, "create": true, "link": true, "pack": true, "info": true, "view": true,
	"config": true, "cache": true, "global": true, "set": true, "dedupe": true, "prune": true, "login": true,
	"exec": true, "dlx": true, "x": true, "help": true, "list": true, "ls": true, "store": true, "import": true,
	"rebuild": true, "start": true, "stop": true, "restart": true, "workspaces": true, "workspace": true,
}

// classify reports whether an invocation runs verification, and which. For
// task runners it reports the first verification task; use classifyAll to
// get all of them.
func classify(iv invocation) (check, bool) {
	if _, ok := taskRunnerEco[iv.Prog()]; ok {
		if cs := taskChecks(iv.Prog(), iv.Args[1:]); len(cs) > 0 {
			return cs[0], true
		}
		return check{}, false
	}
	prog := iv.Prog()
	if prog == "" {
		return check{}, false
	}
	args := iv.Args[1:]
	sub := firstPositional(args)
	is := func(tool, cat, eco string) (check, bool) { return check{Tool: tool, Category: cat, Eco: eco}, true }

	switch prog {
	// ---- JavaScript / TypeScript ----
	case "npm", "pnpm", "yarn", "bun":
		return classifyPM(prog, args)
	case "jest", "mocha", "ava", "tap", "tape", "jasmine", "uvu", "nyc", "c8", "wtr", "web-test-runner", "testcafe", "nightwatch", "cucumber-js", "tsd":
		cat := catTest
		if prog == "nyc" || prog == "c8" {
			switch {
			case sub == "check-coverage" || sub == "report" && hasArg(args, "--check-coverage"):
				cat = catCoverage
			case sub == "report" || sub == "merge" || sub == "instrument":
				return check{}, false // reporting only
			}
		}
		return is(prog, cat, ecoJS)
	case "vitest":
		if sub == "bench" {
			return check{}, false
		}
		return is(prog, catTest, ecoJS)
	case "playwright":
		if sub == "test" {
			return is("playwright test", catTest, ecoJS)
		}
	case "cypress":
		if sub == "run" {
			return is("cypress run", catTest, ecoJS)
		}
	case "karma", "wdio", "detox":
		if sub == "start" || sub == "run" || sub == "test" || (prog == "wdio" && strings.Contains(sub, "conf")) {
			return is(prog+" "+sub, catTest, ecoJS)
		}
	case "react-scripts", "craco", "rescripts", "vue-cli-service", "ng", "next", "nuxi", "nuxt":
		if cat, ok := verificationName(sub); ok {
			return is(prog+" "+sub, cat, ecoJS)
		}
	case "node", "tsx":
		if hasArg(args, "--test") {
			return is(prog+" --test", catTest, ecoJS)
		}
		if s := scriptArg(args); s != "" {
			return scriptCheck(s)
		}
	case "tsc", "vue-tsc":
		if hasArg(args, "--watch", "-w", "--init") {
			return check{}, false
		}
		return is(prog, catTypes, ecoJS)
	case "svelte-check", "flow":
		return is(prog, catTypes, ecoJS)
	case "astro":
		if sub == "check" {
			return is("astro check", catTypes, ecoJS)
		}
	case "eslint", "oxlint", "stylelint", "standard", "xo", "tslint":
		if hasArg(args, "--init") {
			return check{}, false
		}
		return is(prog, catLint, ecoJS)
	case "biome":
		if sub == "check" || sub == "lint" || sub == "ci" {
			return is("biome "+sub, catLint, ecoJS)
		}
	case "prettier":
		if hasArg(args, "--check", "-c", "--list-different", "-l") {
			return is("prettier --check", catFormat, ecoJS)
		}
	case "deno":
		switch sub {
		case "test", "lint", "check":
			return is("deno "+sub, map[string]string{"test": catTest, "lint": catLint, "check": catTypes}[sub], ecoDeno)
		case "fmt":
			if hasArg(args, "--check") {
				return is("deno fmt --check", catFormat, ecoDeno)
			}
		case "task":
			if t := positionalAt(args, 1); t != "" {
				if cat, ok := verificationName(t); ok {
					return is("deno task "+t, cat, ecoDeno)
				}
			}
		}

	// ---- Python ----
	case "pytest", "nose2", "green", "behave", "ward", "trial":
		if hasArg(args, "--benchmark-only") {
			return check{}, false
		}
		return is(prog, catTest, ecoPython)
	case "unittest":
		return is("python -m unittest", catTest, ecoPython)
	case "tox":
		if hasArg(args, "-l", "--listenvs", "-a", "--listenvs-all", "--version", "--help") {
			return check{}, false
		}
		cat := catTest
		if envs, ok := flagValue(args, "-e"); ok {
			cat = envCategory(envs)
		}
		return is("tox", cat, ecoPython)
	case "nox":
		if hasArg(args, "-l", "--list", "--version") {
			return check{}, false
		}
		cat := catTest
		if s, ok := flagValue(args, "-s"); ok {
			cat = envCategory(s)
		} else if s, ok := flagValue(args, "--session"); ok {
			cat = envCategory(s)
		}
		return is("nox", cat, ecoPython)
	case "coverage":
		switch sub {
		case "run":
			return is("coverage run", catTest, ecoPython)
		case "report":
			return is("coverage report", catCoverage, ecoPython)
		}
		if _, ok := flagValue(args, "--fail-under"); ok {
			return is("coverage "+sub, catCoverage, ecoPython)
		}
	case "python":
		// python manage.py test, python runtests.py
		if s := scriptArg(args); s != "" {
			if path.Base(s) == "manage.py" && positionalAt(args, 1) == "test" {
				return is("manage.py test", catTest, ecoPython)
			}
			return scriptCheck(s)
		}
	case "django-admin":
		if sub == "test" {
			return is("django-admin test", catTest, ecoPython)
		}
	case "hatch":
		if sub == "test" {
			return is("hatch test", catTest, ecoPython)
		}
	case "poe":
		if cat, ok := verificationName(sub); ok {
			return is("poe "+sub, cat, ecoPython)
		}
	case "mypy", "pyright", "basedpyright", "pytype", "pyre", "dmypy":
		return is(prog, catTypes, ecoPython)
	case "ruff":
		switch {
		case sub == "format":
			if hasArg(args, "--check") {
				return is("ruff format --check", catFormat, ecoPython)
			}
			return check{}, false
		case sub == "check" || sub == "" || strings.ContainsAny(sub, "./"):
			return is("ruff check", catLint, ecoPython)
		}
	case "flake8", "pylint", "pycodestyle", "pydocstyle", "pyflakes", "bandit", "pyroma":
		return is(prog, catLint, ecoPython)
	case "black":
		if hasArg(args, "--check") {
			return is("black --check", catFormat, ecoPython)
		}
	case "isort":
		if hasArg(args, "--check", "--check-only", "-c") {
			return is("isort --check", catFormat, ecoPython)
		}
	case "pre-commit":
		if sub == "run" {
			return is("pre-commit run", catLint, ecoAny)
		}

	// ---- Go ----
	case "go":
		if sub == "test" && benchmarkOnly(args) {
			return check{}, false
		}
		if sub == "test" || sub == "vet" {
			cat := catTest
			if sub == "vet" {
				cat = catLint
			}
			return is("go "+sub, cat, ecoGo)
		}
	case "gotestsum", "ginkgo", "richgo":
		if prog == "ginkgo" && (sub == "bootstrap" || sub == "generate") {
			return check{}, false
		}
		return is(prog, catTest, ecoGo)
	case "golangci-lint":
		if sub == "run" || sub == "" {
			return is("golangci-lint", catLint, ecoGo)
		}
	case "staticcheck", "revive", "errcheck", "gosec", "ineffassign":
		return is(prog, catLint, ecoGo)

	// ---- Rust ----
	case "cargo":
		switch sub {
		case "test", "nextest", "tarpaulin", "llvm-cov", "miri", "insta":
			return is("cargo "+sub, catTest, ecoRust)
		case "clippy":
			return is("cargo clippy", catLint, ecoRust)
		case "check":
			return is("cargo check", catTypes, ecoRust)
		case "fmt":
			if hasArg(args, "--check") {
				return is("cargo fmt --check", catFormat, ecoRust)
			}
		case "hack":
			for _, a := range args[1:] {
				if a == "test" || a == "check" || a == "clippy" {
					return is("cargo hack "+a, catTest, ecoRust)
				}
			}
		}

	// ---- JVM ----
	case "ktlint", "detekt", "checkstyle":
		return is(prog, catLint, ecoJVM)

	// ---- .NET, Swift, Ruby, PHP, Elixir, Dart, native ----
	case "dotnet":
		if sub == "test" || sub == "vstest" {
			return is("dotnet test", catTest, ecoDotnet)
		}
		if sub == "format" && hasArg(args, "--verify-no-changes") {
			return is("dotnet format --verify-no-changes", catFormat, ecoDotnet)
		}
	case "vstest.console":
		return is(prog, catTest, ecoDotnet)
	case "swift":
		if sub == "test" {
			return is("swift test", catTest, ecoSwift)
		}
	case "xcodebuild":
		if hasArg(args, "test", "test-without-building") {
			return is("xcodebuild test", catTest, ecoSwift)
		}
	case "swiftlint":
		if sub == "" || sub == "lint" {
			return is("swiftlint", catLint, ecoSwift)
		}
	case "fastlane":
		if cat, ok := verificationName(sub); ok || sub == "scan" {
			if !ok {
				cat = catTest
			}
			return is("fastlane "+sub, cat, ecoSwift)
		}
	case "rspec", "cucumber", "minitest":
		return is(prog, catTest, ecoRuby)
	case "rubocop", "standardrb", "reek":
		return is(prog, catLint, ecoRuby)
	case "srb", "steep":
		if sub == "tc" || sub == "check" || sub == "typecheck" {
			return is(prog+" "+sub, catTypes, ecoRuby)
		}
	case "phpunit", "pest", "paratest", "codecept", "infection":
		return is(prog, catTest, ecoPHP)
	case "phpstan", "psalm":
		return is(prog, catTypes, ecoPHP)
	case "phpcs", "php-cs-fixer":
		if prog == "php-cs-fixer" && !hasArg(args, "--dry-run") {
			return check{}, false
		}
		return is(prog, catLint, ecoPHP)
	case "php":
		if sub == "artisan" && positionalAt(args, 1) == "test" {
			return is("php artisan test", catTest, ecoPHP)
		}
	case "composer":
		name := sub
		if sub == "run" || sub == "run-script" {
			name = positionalAt(args, 1)
		}
		if cat, ok := verificationName(name); ok {
			return is("composer "+name, cat, ecoPHP)
		}
	case "mix":
		switch sub {
		case "test":
			return is("mix test", catTest, ecoElixir)
		case "credo", "dialyzer":
			return is("mix "+sub, catLint, ecoElixir)
		case "format":
			if hasArg(args, "--check-formatted") {
				return is("mix format --check-formatted", catFormat, ecoElixir)
			}
		}
	case "flutter", "dart":
		switch sub {
		case "test":
			return is(prog+" test", catTest, ecoDart)
		case "analyze":
			return is(prog+" analyze", catLint, ecoDart)
		}
	case "ctest":
		return is("ctest", catTest, ecoNative)
	case "shellcheck", "hadolint", "actionlint", "yamllint", "markdownlint", "markdownlint-cli2":
		return is(prog, catLint, ecoAny)
	case "lint-staged", "pretty-quick":
		return is(prog, catLint, ecoJS)
	case "lefthook":
		if sub == "run" {
			return is("lefthook run", catLint, ecoAny)
		}
	case "bash", "sh", "zsh", "pwsh", "powershell", "ruby":
		if s := scriptArg(args); s != "" {
			return scriptCheck(s)
		}
	}
	// A script run by path: ./scripts/test.sh, bin/test, script/lint.
	if w := iv.Word; strings.Contains(w, "/") || hasScriptExt(w) {
		return scriptCheck(w)
	}
	return check{}, false
}

// taskRunnerEco maps task runners whose arguments are task names to the
// ecosystem of the tasks they run.
var taskRunnerEco = map[string]string{
	"make": ecoAny, "gmake": ecoAny, "just": ecoAny, "task": ecoAny, "mage": ecoAny, "invoke": ecoAny,
	"inv": ecoAny, "earthly": ecoAny, "meson": ecoNative, "ninja": ecoNative, "bazel": ecoAny, "bazelisk": ecoAny,
	"buck": ecoAny, "buck2": ecoAny, "pants": ecoAny, "please": ecoAny,
	"turbo": ecoJS, "lerna": ecoJS, "rush": ecoJS, "moon": ecoJS, "run-s": ecoJS, "run-p": ecoJS,
	"npm-run-all": ecoJS, "wireit": ecoJS, "nx": ecoJS,
	"gradle": ecoJVM, "mvn": ecoJVM, "sbt": ecoJVM, "lein": ecoJVM, "mill": ecoJVM,
	"rake": ecoRuby, "rails": ecoRuby,
}

// taskChecks classifies the tasks named on a task runner's command line
// ("make lint test", "gradle check", "nx run-many -t test lint").
func taskChecks(prog string, args []string) []check {
	eco := taskRunnerEco[prog]
	var out []check
	add := func(task, cat string) { out = append(out, check{Tool: prog + " " + task, Category: cat, Eco: eco}) }
	ps := positionalsSkipping(args, taskRunnerFlags[prog])
	if prog == "rake" && len(ps) == 0 && !hasArg(args, "-T", "--tasks", "-D", "--describe", "-P", "--prereqs", "-V", "--version", "-h", "--help") {
		// A bare `rake` runs the default task, which Ruby projects point at
		// their test suite (task default: %i[spec rubocop]).
		return []check{{Tool: "rake", Category: catUmbrella, Eco: ecoRuby}}
	}
	if prog == "nx" {
		for _, f := range []string{"-t", "--target", "--targets"} {
			if v, ok := flagValue(args, f); ok {
				ps = append(ps, strings.Split(v, ",")...)
			}
		}
	}
	for _, t := range ps {
		switch prog {
		case "gradle":
			if gradleVerificationTask(t) {
				add(t, catTest)
			}
			continue
		case "mvn":
			if mavenVerificationGoal(t) {
				add(t, catTest)
			}
			continue
		}
		if strings.Contains(t, "=") {
			continue // make VAR=value
		}
		name := strings.TrimPrefix(t, "+")
		if i := strings.LastIndex(name, ":"); i >= 0 && (prog == "nx" || prog == "turbo") {
			name = name[i+1:] // nx run app:test, turbo run web#test
		}
		if i := strings.LastIndex(name, "#"); i >= 0 {
			name = name[i+1:]
		}
		if cat, ok := verificationName(name); ok {
			add(t, cat)
		}
	}
	return out
}

// taskRunnerFlags lists, per task runner, flags whose value is a separate
// word that must not be read as a task name ("gradle -x test",
// "make -C tests check").
var taskRunnerFlags = map[string]map[string]bool{
	"gradle": {"-x": true, "--exclude-task": true, "-p": true, "--project-dir": true, "-b": true, "--build-file": true, "-c": true, "--settings-file": true, "-g": true, "--gradle-user-home": true, "-I": true, "--init-script": true},
	"mvn":    {"-f": true, "--file": true, "-pl": true, "--projects": true, "-P": true, "--activate-profiles": true, "-s": true, "--settings": true, "-rf": true, "--resume-from": true, "-T": true, "--threads": true},
	"make":   {"-C": true, "--directory": true, "-f": true, "--file": true, "--makefile": true, "-j": true, "--jobs": true, "-o": true, "-W": true, "-I": true, "-l": true},
	"gmake":  {"-C": true, "--directory": true, "-f": true, "--file": true, "-j": true, "--jobs": true, "-o": true, "-W": true, "-I": true},
	"just":   {"-f": true, "--justfile": true, "-d": true, "--working-directory": true, "--set": true},
	"task":   {"-d": true, "--dir": true, "-t": true, "--taskfile": true},
	"turbo":  {"--filter": true, "-F": true, "--concurrency": true, "--cache-dir": true},
	"nx":     {"-t": true, "--target": true, "--targets": true, "-p": true, "--projects": true, "--configuration": true, "-c": true, "--base": true, "--head": true, "--parallel": true},
	"bazel":  {"--config": true},
	"lerna":  {"--scope": true, "--ignore": true, "--concurrency": true},
}

// positionalsSkipping is positionals for commands whose listed flags take
// a separate value.
func positionalsSkipping(args []string, withValue map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return out
		case withValue[a]:
			i++
		case !strings.HasPrefix(a, "-"):
			out = append(out, a)
		}
	}
	return out
}

// fixFlags make a linter or formatter rewrite files instead of only
// reporting problems.
var fixFlags = map[string]bool{
	"--fix": true, "--fix-only": true, "--write": true, "--apply": true, "--apply-unsafe": true,
	"--autocorrect": true, "--autocorrect-all": true, "--auto-correct": true, "--auto-correct-all": true,
	"--unsafe-fixes": true,
}

// fixMode reports whether a lint or format command runs as an autofixer
// (eslint --fix, ruff check --fix, rubocop -a, biome check --write). Such a
// run prepares the tree; it is not the check that gates the change.
func fixMode(iv invocation) bool {
	c, ok := classify(iv)
	if !ok || c.Category != catLint && c.Category != catFormat {
		return false
	}
	ruby := iv.Prog() == "rubocop" || iv.Prog() == "standardrb"
	for _, a := range iv.Args[1:] {
		if fixFlags[a] || strings.HasPrefix(a, "--fix=") && !strings.HasSuffix(a, "=false") || ruby && (a == "-a" || a == "-A") {
			return true
		}
	}
	return false
}

// classifyAll returns every verification an invocation runs ("make lint
// test" runs two).
func classifyAll(iv invocation) []check {
	if _, ok := taskRunnerEco[iv.Prog()]; ok {
		return taskChecks(iv.Prog(), iv.Args[1:])
	}
	if c, ok := classify(iv); ok {
		return []check{c}
	}
	return nil
}

// classifyPM classifies npm/pnpm/yarn/bun invocations.
func classifyPM(pm string, args []string) (check, bool) {
	ws := args
	// skip global flags and "yarn workspace <name>"/"yarn workspaces foreach"
	for len(ws) > 0 {
		switch {
		case ws[0] == "--":
			ws = ws[1:]
		case strings.HasPrefix(ws[0], "-"):
			f := ws[0]
			ws = ws[1:]
			if pmFlagsWithValue[f] && len(ws) > 0 {
				ws = ws[1:]
			}
			continue
		case pm == "yarn" && ws[0] == "workspace" && len(ws) > 2:
			ws = ws[2:]
			continue
		case pm == "yarn" && ws[0] == "workspaces":
			ws = ws[1:]
			for len(ws) > 0 && (ws[0] == "foreach" || strings.HasPrefix(ws[0], "-")) {
				ws = ws[1:]
			}
			continue
		}
		break
	}
	if len(ws) == 0 {
		return check{}, false
	}
	sub := ws[0]
	switch {
	case sub == "test" || sub == "t" || sub == "tst":
		if pm == "bun" {
			return check{Tool: "bun test", Category: catTest, Eco: ecoJS}, true
		}
		return check{Tool: pm + " test", Category: catTest, Eco: ecoJS}, true
	case sub == "run" || sub == "run-script" || sub == "rum" || sub == "urn":
		name := firstPositional(ws[1:])
		if cat, ok := verificationName(name); ok {
			return check{Tool: pm + " run " + name, Category: cat, Eco: ecoJS}, true
		}
	case pm != "npm" && !pmBuiltins[sub]:
		if cat, ok := verificationName(sub); ok {
			return check{Tool: pm + " " + sub, Category: cat, Eco: ecoJS}, true
		}
		// "yarn jest", "pnpm vitest run", "yarn turbo test": a package binary
		if sub != pm {
			if cs := classifyAll(parseInvocation(ws)); len(cs) > 0 {
				return cs[0], true
			}
		}
	}
	return check{}, false
}

// envCategory derives a category from tox/nox environment names
// ("lint,type" → lint; "py311" → test).
func envCategory(envs string) string {
	for _, e := range strings.Split(envs, ",") {
		if cat, ok := verificationName(e); ok {
			return cat
		}
	}
	for _, e := range strings.Split(envs, ",") {
		w := nameWords(e)
		if len(w) > 0 && (w[0] == "py" || strings.HasPrefix(w[0], "py3") || strings.HasPrefix(w[0], "pypy")) {
			return catTest
		}
	}
	return catUmbrella
}

// gradleVerificationTask reports whether a Gradle task runs verification:
// test, check, build, connectedCheck, integrationTest, testDebugUnitTest,
// ktlintCheck, detekt, koverVerify, ":app:test", ...
func gradleVerificationTask(t string) bool {
	if i := strings.LastIndex(t, ":"); i >= 0 {
		t = t[i+1:]
	}
	switch t {
	case "test", "check", "build", "lint", "detekt", "connectedCheck", "connectedAndroidTest", "koverVerify",
		"jacocoTestCoverageVerification", "allTests":
		return true
	}
	return strings.HasPrefix(t, "test") && len(t) > 4 && unicode.IsUpper(rune(t[4])) ||
		strings.HasSuffix(t, "Test") || strings.HasSuffix(t, "Tests") || strings.HasSuffix(t, "Check") ||
		strings.HasPrefix(t, "lint") && len(t) > 4 && unicode.IsUpper(rune(t[4]))
}

// mavenVerificationGoal reports whether a Maven phase or goal runs tests or
// checks. Phases from "test" on run the test phase.
func mavenVerificationGoal(g string) bool {
	switch g {
	case "test", "integration-test", "verify", "install", "package", "deploy",
		"surefire:test", "failsafe:integration-test", "failsafe:verify", "checkstyle:check",
		"spotbugs:check", "pmd:check", "jacoco:check", "spotless:check":
		return true
	}
	return false
}

var scriptExts = []string{".sh", ".bash", ".zsh", ".py", ".js", ".mjs", ".cjs", ".ts", ".rb", ".ps1", ".pl"}

func hasScriptExt(w string) bool {
	for _, e := range scriptExts {
		if strings.HasSuffix(strings.ToLower(w), e) {
			return true
		}
	}
	return false
}

// scriptCheck classifies a script run by path, by its file name:
// scripts/test.sh, bin/test, ci/run-lint.sh, runtests.py.
func scriptCheck(p string) (check, bool) {
	base := path.Base(strings.ReplaceAll(p, `\`, "/"))
	for _, e := range scriptExts {
		base = strings.TrimSuffix(base, e)
	}
	if base == "runtests" || base == "runtest" {
		return check{Tool: p, Category: catTest, Eco: ecoAny}, true
	}
	if cat, ok := verificationName(base); ok {
		return check{Tool: p, Category: cat, Eco: ecoAny}, true
	}
	return check{}, false
}

// scriptArg returns the script path an interpreter runs ("bash x.sh",
// "python runtests.py"), or "". Inline code (-c, -e, -m) is not a script.
func scriptArg(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if a == "-c" || a == "-m" || a == "-e" || a == "--eval" || a == "-p" || a == "--print" {
				return ""
			}
			continue
		}
		if strings.Contains(a, "/") || hasScriptExt(a) {
			return a
		}
		return ""
	}
	return ""
}

// positionals returns the arguments that are not flags. Flag values are
// not recognized, so callers only use it where that is harmless.
func positionals(args []string) []string {
	var out []string
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

func firstPositional(args []string) string { return positionalAt(args, 0) }

func positionalAt(args []string, n int) string {
	ps := positionals(args)
	if n < len(ps) {
		return ps[n]
	}
	return ""
}

// checksIn returns the verification commands in shell text.
func checksIn(s string) []check {
	var out []check
	for _, sc := range expandShell(splitShell(s)) {
		out = append(out, classifyAll(parseInvocation(sc.Words))...)
	}
	return out
}

// expandShell replaces `sh -c "..."` commands with the commands they run.
func expandShell(cmds []shellCmd) []shellCmd {
	var out []shellCmd
	for _, c := range cmds {
		if len(c.Words) >= 3 {
			p := progName(c.Words[0])
			if (p == "sh" || p == "bash" || p == "zsh") && strings.HasPrefix(c.Words[1], "-") && strings.HasSuffix(c.Words[1], "c") {
				inner := splitShell(c.Words[2])
				if len(inner) > 0 {
					inner[len(inner)-1].Op = c.Op
					out = append(out, inner...)
					continue
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// benchmarkOnly reports whether "go test" args run benchmarks only
// (go test -run '^$' -bench .).
func benchmarkOnly(args []string) bool {
	_, bench := flagValue(args, "-bench")
	if !bench {
		_, bench = flagValue(args, "--bench")
	}
	run, ok := flagValue(args, "-run")
	return bench && ok && matchesNoTest(run)
}

// matchesAllTests reports whether a go test -run pattern selects every
// test (all test functions start with "Test").
func matchesAllTests(p string) bool {
	switch strings.Trim(p, `'"`) {
	case ".", ".*", "^.*$", "^.*", ".+", "Test", "^Test", "^Test.*", "^Test.*$":
		return true
	}
	return false
}

// matchesNoTest reports whether a go test -run pattern selects no tests.
func matchesNoTest(p string) bool {
	switch strings.Trim(p, `'"`) {
	case "^$", "^$$", "XXX", "xxx", "none", "NONE", "^#", "-", "nothing", "^NOTHING$":
		return true
	}
	return false
}
