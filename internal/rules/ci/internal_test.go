package ci

import (
	"strings"
	"testing"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/ruletest"
)

func TestClassify(t *testing.T) {
	cases := map[string]string{ // command → "tool/category", "" for not verification
		"pytest -q":                                  "pytest/test",
		"python3 -m pytest tests/":                   "pytest/test",
		"poetry run python -m pytest":                "pytest/test",
		"uv run --frozen pytest -k 'not slow'":       "pytest/test",
		"$(PYTHON) -m pytest --cov=src":              "pytest/test",
		"npm test":                                   "npm test/test",
		"npm run test:unit":                          "npm run test:unit/test",
		"yarn lint":                                  "yarn lint/lint",
		"pnpm --filter web run typecheck":            "pnpm run typecheck/typecheck",
		"yarn workspace @acme/web test":              "yarn test/test",
		"yarn turbo run test --filter=web":           "turbo test/test",
		"pnpm vitest run":                            "pnpm vitest/test",
		"npx --yes vitest run":                       "vitest/test",
		"CI=true npx jest --ci":                      "jest/test",
		"go test ./...":                              "go test/test",
		"go vet ./...":                               "go vet/lint",
		"cargo clippy -- -D warnings":                "cargo clippy/lint",
		"./gradlew :app:testDebugUnitTest":           "gradle :app:testDebugUnitTest/test",
		"mvn -B verify":                              "mvn verify/test",
		"bundle exec rake spec":                      "rake spec/test",
		"php vendor/bin/phpunit":                     "phpunit/test",
		"make -C backend test":                       "make test/test",
		"./scripts/run-tests.sh --fast":              "./scripts/run-tests.sh/test",
		"bash ci/lint.sh":                            "ci/lint.sh/lint",
		"docker compose run --rm -e CI=1 app pytest": "pytest/test",
		"pre-commit run --all-files":                 "pre-commit run/lint",
		"tox -e lint":                                "tox/lint",
		"tsc --noEmit":                               "tsc/typecheck",
		"npx playwright test":                        "playwright test/test",
		"npm ci":                                     "",
		"npm run build":                              "",
		"npm run lint:fix":                           "",
		"npm run test:watch":                         "",
		"npm audit":                                  "",
		"go test -run '^$' -bench . ./...":           "",
		"pytest --benchmark-only":                    "",
		"npx playwright install --with-deps":         "",
		"cypress open":                               "",
		"tsc --watch":                                "",
		"ruff format .":                              "",
		"rm -rf dist":                                "",
		"make -C tests":                              "",
		"gradle assemble -x test":                    "",
	}
	for cmd, want := range cases {
		cmds := splitShell(cmd)
		got := ""
		if len(cmds) > 0 {
			if c, ok := classify(parseInvocation(cmds[0].Words)); ok {
				got = c.Tool + "/" + c.Category
			}
		}
		if got != want {
			t.Errorf("classify(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestVerificationName(t *testing.T) {
	yes := []string{"test", "test:unit", "unit-test", "typecheck", "type-check", "check:types", "lint", "e2e", "ci", "verify", "testUnit"}
	no := []string{"build", "lint:fix", "test:watch", "test:update", "check-updates", "dev", "release", "types", "format", "start", "testDebug"}
	for _, n := range yes {
		if _, ok := verificationName(n); !ok {
			t.Errorf("verificationName(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if c, ok := verificationName(n); ok {
			t.Errorf("verificationName(%q) = %q, want no match", n, c)
		}
	}
}

func TestSwallows(t *testing.T) {
	cases := map[string]string{ // command → swallow form, "" for none
		"npm test || true":                               "|| true",
		"cd app && pytest -q || :":                       "|| :",
		"go test ./... || exit 0":                        "|| exit 0",
		"cargo clippy || echo 'clippy failed'":           "|| echo",
		"jest; exit 0":                                   "; exit 0",
		`bash -c "pytest || true"`:                       "|| true",
		"rm -f out.log || true":                          "",
		"pytest || (echo failed; exit 1)":                "",
		"pytest || { echo failed; exit 1; }":             "",
		"npm test || npm test":                           "",
		"pytest; echo done":                              "",
		`echo "npm test || true"`:                        "",
		"pytest -q 2>&1 | tee test.log":                  "",
		"docker rm -f db 2>/dev/null || true; go test .": "",
	}
	for cmd, want := range cases {
		got := ""
		if sws := swallowsIn(cmd); len(sws) > 0 {
			got = sws[0].Form
		}
		if got != want {
			t.Errorf("swallowsIn(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestNoopReason(t *testing.T) {
	noop := []string{"", "echo skipped", "exit 0", "true", ":", `node -e ""`, `python -c "pass"`,
		"jest --listTests", "pytest --collect-only -q", "cargo test --no-run", "go test -run '^$' ./...", "mvn verify -DskipTests"}
	real := []string{"jest", "echo start && jest", "pytest -q", "go test -run '^$' -bench . ./...", "node scripts/test.js", "exit 1"}
	for _, s := range noop {
		if _, ok := noopReason(s); !ok {
			t.Errorf("noopReason(%q) = false, want no-op", s)
		}
	}
	for _, s := range real {
		if r, ok := noopReason(s); ok {
			t.Errorf("noopReason(%q) = %q, want not a no-op", s, r)
		}
	}
}

func TestGitHookBypass(t *testing.T) {
	cases := map[string]string{
		"git commit --no-verify -m x":            "git commit --no-verify",
		"git commit -nm wip":                     "git commit -nm",
		"git push --no-verify origin main":       "git push --no-verify",
		"git -c core.hooksPath=/dev/null commit": "-c core.hooksPath=/dev/null",
		"git config core.hooksPath /dev/null":    "git config core.hooksPath /dev/null",
		"git push -n origin HEAD":                "",
		"git commit -m '-n is fine here'":        "",
		"git commit -am fix":                     "",
		"git config core.hooksPath .githooks":    "",
	}
	for cmd, want := range cases {
		if got := gitHookBypass(parseInvocation(splitShell(cmd)[0].Words)); got != want {
			t.Errorf("gitHookBypass(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func entryMap(es []entry) map[string]string {
	m := map[string]string{}
	for _, e := range es {
		k := strings.Join(e.Path, ".")
		if e.Item {
			k += "[]"
			if m[k] != "" {
				m[k] += ","
			}
			m[k] += e.Value
			continue
		}
		m[k] = e.Value
	}
	return m
}

func TestParseConfig(t *testing.T) {
	cases := []struct {
		name   string
		format configFormat
		lang   lang.Lang
		src    string
		want   map[string]string
	}{
		{"js object", fmtJSON, lang.JavaScript, `module.exports = {
  // comment
  coverageThreshold: { global: { lines: 80 }, './src/': { branches: 70 } },
  testPathIgnorePatterns: ['/node_modules/', '<rootDir>/e2e/'],
  ...base,
};`, map[string]string{
			"coverageThreshold.global.lines":    "80",
			"coverageThreshold../src/.branches": "70",
			"testPathIgnorePatterns[]":          "/node_modules/,<rootDir>/e2e/",
		}},
		{"jsonc", fmtJSON, lang.JSON, `{
  "compilerOptions": {
    "strict": true, // trailing comma follows
  },
}`, map[string]string{"compilerOptions.strict": "true"}},
		{"toml", fmtTOML, lang.TOML, `[tool.coverage.report]
fail_under = 90
[tool.ruff.lint]
ignore = [
  "E501", # long lines
  'E731',
]
[tool.pylint."messages control"]
disable = "C0114"
`, map[string]string{
			"tool.coverage.report.fail_under":      "90",
			"tool.ruff.lint.ignore[]":              "E501,E731",
			"tool.pylint.messages control.disable": "C0114",
		}},
		{"ini", fmtINI, lang.INI, `[coverage:report]
fail_under = 85
omit =
    */migrations/*
    src/legacy/*
`, map[string]string{"coverage:report.fail_under": "85", "coverage:report.omit[]": "*/migrations/*,src/legacy/*"}},
		{"yaml", fmtYAML, lang.YAML, `coverage:
  status:
    project:
      default:
        target: 80%
ignore:
  - "**/*.pb.go"
`, map[string]string{"coverage.status.project.default.target": "80%", "ignore[]": "**/*.pb.go"}},
	}
	for _, c := range cases {
		got := entryMap(parseConfig(c.format, c.src, lang.Lex(c.lang, c.src)))
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q (all: %v)", c.name, k, got[k], v, got)
			}
		}
	}
}

func TestYAMLCommandsAndBlocks(t *testing.T) {
	src := `jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Test
        run: |
          npm ci
          npm test || true
        continue-on-error: true
      - run:
          name: CircleCI style
          command: make lint
      - run: >
          pytest tests/
          --cov=src
`
	s := fullSide(lang.Lex(lang.YAML, src))
	var got []string
	for _, c := range yamlCommands(s) {
		got = append(got, c.Text)
	}
	if want := "npm ci|npm test || true|make lint|pytest tests/ --cov=src"; strings.Join(got, "|") != want {
		t.Errorf("yamlCommands = %q, want %q", strings.Join(got, "|"), want)
	}
	if start, end := yamlBlock(s, 10); start != 6 || end != 10 {
		t.Errorf("yamlBlock(step key) = %d-%d, want 6-10", start, end)
	}
	if job, step := yamlOwner(s, 10); job != "test" || step != "Test" {
		t.Errorf("yamlOwner = %q/%q, want test/Test", job, step)
	}
}

func TestTargetsCover(t *testing.T) {
	if !targetsCover([]string{"./..."}, []string{"./pkg/auth/..."}) {
		t.Error("./... should cover ./pkg/auth/...")
	}
	if !targetsCover([]string{"tests"}, []string{"tests/unit"}) {
		t.Error("tests should cover tests/unit")
	}
	if targetsCover([]string{"tests/unit"}, []string{"tests/integration"}) {
		t.Error("tests/unit must not cover tests/integration")
	}
}

// TestRobustness runs every corpus case as a bare patch and reversed (new →
// old): rules must never crash on partial or unusual input.
func TestRobustness(t *testing.T) {
	cases, err := ruletest.LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		patch := *c
		patch.PatchOnly = true
		reversed := *c
		reversed.Old, reversed.New = c.New, c.Old
		for _, v := range []*ruletest.Case{&patch, &reversed} {
			for _, e := range v.Run(nil).Errors {
				t.Errorf("%s: %s", c.Name, e)
			}
		}
	}
}

// FuzzParsers checks that the hand-written parsers never panic or hang on
// malformed input. `go test` runs the seeds; `go test -fuzz FuzzParsers`
// explores further.
func FuzzParsers(f *testing.F) {
	for _, seed := range []string{
		`npm test || true`, `echo "unterminated`, `$(foo (bar`, "a \\\n b", `{ x; } || :`, `2>&1 >`,
		`{"a": [1, {"b": }]`, `module.exports = { ...x, [k]: v, f() { return {} } }`, "[[t]]\nk = '''x",
		"key:\n  - |\n    run\n- :", "[s]\nk =\n  v\n\tw", `)}]`, "\"\"\"", "jobs:\n  - run: >\n",
		"if ! pytest; then if go test; then :; fi; fi", "fi fi if", "cat <<EOF\nnpm test || true\n", "a | | tee", "${{ x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, iv := range invocations(s) {
			classifyAll(iv)
			snapshotUpdate(iv)
			gitHookBypass(iv)
		}
		swallowsIn(s)
		ifSwallows(s)
		maskingPipe(expandShell(splitShell(s)))
		dropHeredocs(shellLines(fullSide(lang.Lex(lang.Shell, s))))
		noopReason(s)
		for _, format := range []configFormat{fmtJSON, fmtTOML, fmtYAML, fmtINI} {
			parseConfig(format, s, lang.Lex(lang.JSON, s))
		}
		parseConfig(fmtTOML, s, lang.Lex(lang.TOML, s))
		yamlCommands(fullSide(lang.Lex(lang.YAML, s)))
	})
}

// FuzzResolver checks that resolving entry points (`npm test`, `make`,
// `just all`, ./scripts/ci.sh, a local action) never panics or loops on
// malformed definitions, including self-referencing ones.
func FuzzResolver(f *testing.F) {
	for _, seed := range []string{
		`{"scripts": {"test": "npm test", "pretest": "npm run test"}}`,
		"all: all\n\t$(MAKE) all\n", "all: lint\nlint: all\n\tmake lint\n", "#!/bin/sh\n./scripts/ci.sh\ncd $X && npm test\n",
		"runs:\n  steps:\n    - uses: ./.github/actions/x\n    - run: just all\n", "", "\x00", "{{{", "all:\n\tcd .. && make\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		files := map[string][]byte{
			"package.json": []byte(s), "Makefile": []byte(s), "justfile": []byte(s), "scripts/ci.sh": []byte(s),
			".github/actions/x/action.yml": []byte(s), "web/package.json": []byte(s),
		}
		a := &core.Analysis{Source: &core.MemSource{OldFiles: files, NewFiles: files}}
		for _, old := range []bool{false, true} {
			x := newExpander(a, old)
			for _, cmd := range []string{"npm test", "npm run ci", "make", "make all -C web", "just all", "./scripts/ci.sh", "yarn --cwd web lint"} {
				for _, iv := range invocations(cmd) {
					x.resolve(iv, "")
				}
			}
			x.seen = map[string]bool{}
			x.localAction("./.github/actions/x")
		}
		unchangedChecks(a)
	})
}

func TestIfSwallows(t *testing.T) {
	cases := map[string]bool{ // shell text → swallows a check
		`if ! pytest; then echo "tests failed"; fi`:                  true,
		`if npm test; then echo ok; fi`:                              true,
		`if go test ./...; then echo ok; else echo "failed"; fi`:     true,
		`if ! pytest; then echo failed; exit 1; fi`:                  false,
		`if ! pytest; then failed=1; fi`:                             false, // collected for a later exit
		`if ! cargo test; then cargo test -- --nocapture; fi`:        false, // the rerun's status is the if's
		`if [ -f package.json ]; then npm test; fi`:                  false, // the condition is not the check
		`if command -v ruff; then ruff check .; fi`:                  false,
		`if ! prettier --check .; then prettier --write .; fi`:       false, // autofix
		`if ! eslint .; then echo bad; elif true; then :; fi`:        false, // elif chains are not judged
		`if ! mypy src; then echo "::error::types"; false; fi`:       false,
		"if ! pytest -q; then\n  echo warn\nfi":                      true,
		`if go vet ./... && go test ./...; then echo ok; fi`:         false, // compound condition
		`if ! npm run lint; then echo "lint failed" >&2; exit 2; fi`: false,
	}
	for text, want := range cases {
		if got := len(ifSwallows(text)) > 0; got != want {
			t.Errorf("ifSwallows(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestMaskingPipe(t *testing.T) {
	cases := map[string]bool{
		"go test ./... | tee out.log":                 true,
		"npm test 2>&1 | grep -v deprecated":          true,
		"pytest | tee log | tail -n 50":               true,
		"go test -json ./... | go-junit-report":       false, // the reporter decides the status
		"npm test | grep -q 'passing'":                false, // a grep -q gate
		"echo hi | tee x":                             false,
		"eslint --fix . | tee fix.log":                false, // an autofixer
		"cat report.txt | grep FAIL && exit 1":        false,
		"pytest --junitxml=r.xml && cat r.xml | head": false,
	}
	for text, want := range cases {
		if _, got := maskingPipe(expandShell(splitShell(text))); got != want {
			t.Errorf("maskingPipe(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestExcludesPullRequests(t *testing.T) {
	yes := []string{"github.event_name == 'push'", "${{ github.event_name != 'pull_request' }}", "github.ref == 'refs/heads/main'",
		"startsWith(github.ref, 'refs/tags/')", "!github.event.pull_request"}
	no := []string{"github.event_name == 'pull_request'", "github.event_name == 'push' || github.event_name == 'pull_request'",
		"matrix.os == 'ubuntu-latest'", "always()", "github.actor != 'dependabot[bot]'", "github.event.pull_request.draft == false"}
	for _, v := range yes {
		if !excludesPullRequests(v) {
			t.Errorf("excludesPullRequests(%q) = false, want true", v)
		}
	}
	for _, v := range no {
		if excludesPullRequests(v) {
			t.Errorf("excludesPullRequests(%q) = true, want false", v)
		}
	}
}

func TestEntryPointNames(t *testing.T) {
	if got := joinDir("web", "../api/x"); got != "api/x" {
		t.Errorf("joinDir = %q", got)
	}
	for _, rel := range []string{"../..", "$DIR", "/etc", "~/x", "${{ matrix.dir }}"} {
		if got := joinDir("", rel); got != unknownDir {
			t.Errorf("joinDir(%q) = %q, want unknown", rel, got)
		}
	}
	cases := map[string]string{ // command → "script@dir", "" when no script is named
		"npm test":                      "test@",
		"npm run lint -- --fix":         "lint@",
		"npm --prefix web run build":    "build@web",
		"yarn --cwd=api test":           "test@api",
		"pnpm -C packages/ui typecheck": "typecheck@packages/ui",
		"pnpm --filter web test":        "",
		"npm test --workspaces":         "", // every workspace's test script
		"npm test -- --workspaces":      "test@",
		"npm run test --workspace=web":  "",
		"bun test":                      "", // bun's runner, not a script
		"yarn install":                  "",
		"npm ci":                        "",
	}
	for cmd, want := range cases {
		iv := parseInvocation(splitShell(cmd)[0].Words)
		name, dir, ok := pmScriptName(iv.Prog(), iv.Args[1:], "")
		got := ""
		if ok {
			got = name + "@" + dir
		}
		if got != want {
			t.Errorf("pmScriptName(%q) = %q, want %q", cmd, got, want)
		}
	}
}
