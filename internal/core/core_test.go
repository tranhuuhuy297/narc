package core

import "testing"

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, p string
		want   bool
	}{
		{"tests/golden/**", "tests/golden/a/b.txt", true},
		{"tests/golden/**", "tests/goldenx/a.txt", false},
		{"*.snap", "src/__snapshots__/a.test.ts.snap", true},
		{"legacy/", "legacy/x/y.py", true},
		{"/legacy/**", "src/legacy/y.py", false},
		{"**/fixtures/*.json", "a/b/fixtures/x.json", true},
		{"src/*.go", "src/a/b.go", false},
		{"node_modules", "web/node_modules/x/index.js", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.p); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pat, c.p, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := map[string]bool{
		"src/auth.test.ts": true, "auth_test.go": true, "tests/test_auth.py": true, "test_auth.py": true,
		"src/test/java/com/x/AuthTest.java": true, "spec/models/user_spec.rb": true, "conftest.py": true,
		"__tests__/a.js": true, "src/auth.ts": false, "testdata/cheat.go": false, "tests/fixtures/data.py": false,
		"src/testing.go": false, "docs/tests.md": false, "tests/README.md": false, "e2e/login.spec.ts": true,
		"AuthTests.cs": true, "src/contest.py": false, "latest.py": false,
	}
	for p, want := range tests {
		if got := IsTestPath(p); got != want {
			t.Errorf("IsTestPath(%q) = %v, want %v", p, got, want)
		}
	}
	ci := map[string]bool{
		".github/workflows/ci.yml": true, ".gitlab-ci.yml": true, "Jenkinsfile": true, ".circleci/config.yml": true,
		"src/ci.yml": false, ".github/dependabot.yml": false,
	}
	for p, want := range ci {
		if got := IsCIPath(p); got != want {
			t.Errorf("IsCIPath(%q) = %v, want %v", p, got, want)
		}
	}
	cfg := map[string]string{
		"package.json": CfgPackageJSON, "web/tsconfig.app.json": CfgTSConfig, "jest.config.ts": CfgJest,
		"vitest.config.mts": CfgVitest, "pyproject.toml": CfgPyproject, ".coveragerc": CfgCoverage,
		"eslint.config.mjs": CfgESLint, "src/index.ts": "", "tsconfig.ts": "",
	}
	for p, want := range cfg {
		if got := ConfigKind(p); got != want {
			t.Errorf("ConfigKind(%q) = %q, want %q", p, got, want)
		}
	}
}
