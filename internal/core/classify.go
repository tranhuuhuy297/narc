package core

import (
	"path"
	"regexp"
	"strings"
)

var testDirs = map[string]bool{
	"test": true, "tests": true, "__tests__": true, "spec": true, "specs": true,
	"e2e": true, "integration_tests": true, "integration-tests": true,
}

// Directories that hold test data rather than test code.
var fixtureDirs = map[string]bool{
	"testdata": true, "fixtures": true, "__fixtures__": true, "__snapshots__": true, "snapshots": true,
}

var testNameRe = regexp.MustCompile(`(?i)(` +
	`_test\.(go|py|rb|exs|dart|cpp|cc|c)$` +
	`|^test_[^/]*\.(py|rb)$` +
	`|\.(test|spec|e2e|e2e-spec|integration|cy)\.(js|jsx|mjs|cjs|ts|tsx|mts|cts)$` +
	`|(Test|Tests|IT|Spec)\.(java|kt|kts|scala|groovy|cs|swift|php)$` +
	`|_spec\.rb$` +
	`|^conftest\.py$` +
	`|Tests?\.fs$` +
	`)`)

// IsTestPath reports whether path looks like test code.
func IsTestPath(p string) bool {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	base := path.Base(p)
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		if fixtureDirs[strings.ToLower(s)] {
			return false
		}
	}
	if testNameRe.MatchString(base) {
		return true
	}
	ext := strings.ToLower(path.Ext(base))
	if ext == "" || ext == ".md" || ext == ".json" || ext == ".yml" || ext == ".yaml" || ext == ".txt" || ext == ".snap" {
		return false
	}
	for i, s := range segs[:len(segs)-1] {
		ls := strings.ToLower(s)
		if testDirs[ls] {
			return true
		}
		// Maven/Gradle layout: src/test/java/...
		if ls == "src" && i+1 < len(segs)-1 && strings.ToLower(segs[i+1]) == "test" {
			return true
		}
	}
	return false
}

// IsSnapshotPath reports whether path is a test snapshot.
func IsSnapshotPath(p string) bool {
	return strings.HasSuffix(p, ".snap") || strings.Contains("/"+p, "/__snapshots__/")
}

var ciRe = regexp.MustCompile(`^(` +
	`\.github/workflows/[^/]+\.ya?ml` +
	`|\.github/actions/.+/action\.ya?ml` +
	`|action\.ya?ml` +
	`|\.gitlab-ci\.ya?ml` +
	`|\.gitlab/ci/.+\.ya?ml` +
	`|\.circleci/config\.ya?ml` +
	`|azure-pipelines\.ya?ml` +
	`|\.azure-pipelines/.+\.ya?ml` +
	`|\.?jenkinsfile` +
	`|bitbucket-pipelines\.ya?ml` +
	`|\.buildkite/.+\.ya?ml` +
	`|\.travis\.ya?ml` +
	`|\.drone\.ya?ml` +
	`|appveyor\.ya?ml` +
	`|\.woodpecker\.ya?ml` +
	`|\.woodpecker/.+\.ya?ml` +
	`|\.(forgejo|gitea)/workflows/[^/]+\.ya?ml` +
	`|cloudbuild\.ya?ml` +
	`|buildspec\.ya?ml` +
	`|\.cirrus\.ya?ml` +
	`|codemagic\.ya?ml` +
	`|\.semaphore/.+\.ya?ml` +
	`|tekton/.+\.ya?ml` +
	`)$`)

// IsCIPath reports whether path is a CI pipeline definition.
func IsCIPath(p string) bool {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	return ciRe.MatchString(strings.ToLower(p)) || ciRe.MatchString(p)
}

// Config kinds returned by ConfigKind.
const (
	CfgPackageJSON = "package.json"
	CfgTSConfig    = "tsconfig"
	CfgJest        = "jest"
	CfgVitest      = "vitest"
	CfgMocha       = "mocha"
	CfgPlaywright  = "playwright"
	CfgCypress     = "cypress"
	CfgKarma       = "karma"
	CfgPytest      = "pytest"     // pytest.ini
	CfgPyproject   = "pyproject"  // pyproject.toml
	CfgSetupCfg    = "setup.cfg"  // setup.cfg
	CfgTox         = "tox"        // tox.ini
	CfgCoverage    = "coverage"   // .coveragerc
	CfgCodecov     = "codecov"    // codecov.yml
	CfgNYC         = "nyc"        // .nycrc*, .c8rc*
	CfgMypy        = "mypy"       // mypy.ini
	CfgRuff        = "ruff"       // ruff.toml
	CfgFlake8      = "flake8"     // .flake8
	CfgPylint      = "pylint"     // .pylintrc
	CfgESLint      = "eslint"     // .eslintrc*, eslint.config.*
	CfgBiome       = "biome"      // biome.json
	CfgGolangci    = "golangci"   // .golangci.*
	CfgCargo       = "cargo"      // Cargo.toml
	CfgClippy      = "clippy"     // clippy.toml
	CfgMake        = "make"       // Makefile, justfile
	CfgPHPUnit     = "phpunit"    // phpunit.xml(.dist)
	CfgPHPStan     = "phpstan"    // phpstan.neon
	CfgRubocop     = "rubocop"    // .rubocop.yml
	CfgGradle      = "gradle"     // build.gradle(.kts)
	CfgMaven       = "maven"      // pom.xml
	CfgPreCommit   = "pre-commit" // .pre-commit-config.yaml, .husky/*, lefthook.yml
	CfgDeno        = "deno"       // deno.json(c)
	CfgSwiftLint   = "swiftlint"  // .swiftlint.yml
	CfgTurbo       = "turbo"      // turbo.json, nx.json
)

var configBase = map[string]string{
	"package.json":  CfgPackageJSON,
	"jsconfig.json": CfgTSConfig,
	".mocharc.js":   CfgMocha, ".mocharc.cjs": CfgMocha, ".mocharc.json": CfgMocha, ".mocharc.yml": CfgMocha, ".mocharc.yaml": CfgMocha, ".mocharc.jsonc": CfgMocha,
	"pytest.ini": CfgPytest, "pyproject.toml": CfgPyproject, "setup.cfg": CfgSetupCfg, "tox.ini": CfgTox,
	".coveragerc": CfgCoverage,
	"codecov.yml": CfgCodecov, ".codecov.yml": CfgCodecov, "codecov.yaml": CfgCodecov, ".codecov.yaml": CfgCodecov,
	".nycrc": CfgNYC, ".nycrc.json": CfgNYC, ".nycrc.yml": CfgNYC, ".nycrc.yaml": CfgNYC, ".c8rc": CfgNYC, ".c8rc.json": CfgNYC,
	"mypy.ini": CfgMypy, ".mypy.ini": CfgMypy,
	"ruff.toml": CfgRuff, ".ruff.toml": CfgRuff,
	".flake8": CfgFlake8, ".pylintrc": CfgPylint, "pylintrc": CfgPylint,
	"biome.json": CfgBiome, "biome.jsonc": CfgBiome,
	".golangci.yml": CfgGolangci, ".golangci.yaml": CfgGolangci, ".golangci.toml": CfgGolangci, ".golangci.json": CfgGolangci,
	"cargo.toml": CfgCargo, "clippy.toml": CfgClippy, ".clippy.toml": CfgClippy,
	"makefile": CfgMake, "gnumakefile": CfgMake, "justfile": CfgMake,
	"phpunit.xml": CfgPHPUnit, "phpunit.xml.dist": CfgPHPUnit,
	"phpstan.neon": CfgPHPStan, "phpstan.neon.dist": CfgPHPStan,
	".rubocop.yml": CfgRubocop,
	"build.gradle": CfgGradle, "build.gradle.kts": CfgGradle,
	"pom.xml":                 CfgMaven,
	".pre-commit-config.yaml": CfgPreCommit, ".pre-commit-config.yml": CfgPreCommit, "lefthook.yml": CfgPreCommit, "lefthook.yaml": CfgPreCommit,
	"deno.json": CfgDeno, "deno.jsonc": CfgDeno,
	".swiftlint.yml": CfgSwiftLint,
	"turbo.json":     CfgTurbo, "nx.json": CfgTurbo,
}

var configPrefix = []struct{ prefix, kind string }{
	{"tsconfig", CfgTSConfig},
	{"jest.config.", CfgJest},
	{"vitest.config.", CfgVitest},
	{"vitest.workspace.", CfgVitest},
	{"vite.config.", CfgVitest},
	{"playwright.config.", CfgPlaywright},
	{"cypress.config.", CfgCypress},
	{"karma.conf.", CfgKarma},
	{".eslintrc", CfgESLint},
	{"eslint.config.", CfgESLint},
}

// ConfigKind classifies tooling configuration files that control
// verification (tests, types, lint, coverage). It returns "" otherwise.
func ConfigKind(p string) string {
	base := strings.ToLower(path.Base(p))
	if k, ok := configBase[base]; ok {
		return k
	}
	for _, cp := range configPrefix {
		if strings.HasPrefix(base, cp.prefix) {
			if cp.kind == CfgTSConfig && !strings.HasSuffix(base, ".json") {
				continue
			}
			return cp.kind
		}
	}
	if strings.Contains("/"+p, "/.husky/") {
		return CfgPreCommit
	}
	return ""
}

var vendorDirs = map[string]bool{
	"vendor": true, "node_modules": true, "third_party": true, "third-party": true,
	".yarn": true, "bower_components": true, "Pods": true, ".venv": true, "venv": true,
	"site-packages": true, "dist": true, "target": true, ".next": true, "coverage": true,
}

var lockfiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "bun.lockb": true, "bun.lock": true,
	"go.sum": true, "cargo.lock": true, "poetry.lock": true, "uv.lock": true, "pipfile.lock": true,
	"composer.lock": true, "gemfile.lock": true, "podfile.lock": true, "mix.lock": true, "pubspec.lock": true,
	"flake.lock": true, "deno.lock": true, "packages.lock.json": true,
}

// IsIgnoredByDefault reports whether a path is vendored, generated output,
// minified, or a lockfile; narc never analyzes those.
func IsIgnoredByDefault(p string) bool {
	segs := strings.Split(strings.TrimPrefix(path.Clean("/"+p), "/"), "/")
	for _, s := range segs[:len(segs)-1] {
		if vendorDirs[s] {
			return true
		}
	}
	base := strings.ToLower(segs[len(segs)-1])
	if lockfiles[base] {
		return true
	}
	if strings.Contains(base, ".min.") || strings.HasSuffix(base, ".map") || strings.HasSuffix(base, ".pb.go") ||
		strings.HasSuffix(base, "_pb2.py") || strings.HasSuffix(base, ".generated.ts") || strings.HasSuffix(base, ".g.dart") {
		return true
	}
	return false
}

var generatedRe = regexp.MustCompile(`(?i)(code generated .* do not edit|@generated|auto-generated|autogenerated file|this file is generated|do not edit this file)`)

// IsGeneratedContent reports whether the first lines of content mark the
// file as generated.
func IsGeneratedContent(content []byte) bool {
	head := content
	if len(head) > 1024 {
		head = head[:1024]
	}
	return generatedRe.Match(head)
}
