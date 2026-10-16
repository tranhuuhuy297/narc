package hook

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGitPreCommit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		cfg    string
		change func(r *testRepo)
		code   int
		want   []string // substrings of stderr; nil means no output
	}{
		{
			name: "staged cheat blocks the commit",
			change: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.git("add", "src/math.js")
			},
			code: 1,
			want: []string{
				"narc: 1 verification issue in the staged changes; commit blocked (fail_on: error)\n",
				"  ✗ BUSTED     hooktest-cheat src/math.js:2 — cheat marker __narc_cheat__() added\n",
				"      → Remove the cheat marker",
				`// narc:allow hooktest-cheat reason="<why>"`,
			},
		},
		{
			name: "only unstaged changes are not part of the commit",
			change: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.write("README.md", "# demo\n\nmore\n")
				r.git("add", "README.md")
			},
		},
		{
			name: "findings below fail_on are reported but allowed",
			cfg:  "rules:\n  hooktest-cheat: warning\n",
			change: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.git("add", "src/math.js")
			},
			want: []string{"commit allowed (fail_on: error)", "! SUSPICIOUS hooktest-cheat src/math.js:2"},
		},
		{
			name: "fail_on never",
			cfg:  "fail_on: never\n",
			change: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.git("add", "src/math.js")
			},
			want: []string{"commit allowed (fail_on: never)"},
		},
		{
			name: "fail_on warning",
			cfg:  "fail_on: warning\nrules:\n  hooktest-cheat: warning\n",
			change: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.git("add", "src/math.js")
			},
			code: 1,
			want: []string{"commit blocked (fail_on: warning)"},
		},
		{
			name: "staged deletion of a protected file",
			cfg:  "protected:\n  - \"tests/golden/**\"\n",
			change: func(r *testRepo) {
				r.git("rm", "-q", "tests/golden/out.txt")
			},
			code: 1,
			want: []string{"protected-path tests/golden/out.txt — protected file was deleted"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"tests/golden/out.txt": "ok\n"}
			if tc.cfg != "" {
				files[".narc.yml"] = tc.cfg
			}
			for k, v := range baseFiles {
				files[k] = v
			}
			r := newRepo(t, files)
			tc.change(r)
			h := runHook(t, "git", "pre-commit", r.dir, nil)
			if h.code != tc.code || h.stdout != "" {
				t.Fatalf("exit %d (want %d), stdout %q\nstderr: %s", h.code, tc.code, h.stdout, h.stderr)
			}
			if tc.want == nil && h.stderr != "" {
				t.Fatalf("want no output, got:\n%s", h.stderr)
			}
			wantContains(t, "report", h.stderr, tc.want...)
			wantNotContains(t, "report", h.stderr, "--no-verify")
		})
	}
}

func TestGitPreCommitFailsOpen(t *testing.T) {
	t.Parallel()
	h := runHook(t, "git", "pre-commit", t.TempDir(), nil)
	if h.code != 0 || h.stdout != "" {
		t.Fatalf("exit %d stdout %q", h.code, h.stdout)
	}
	wantContains(t, "stderr", h.stderr, "not a git repository", "pre-commit check skipped")
}

// Installs the hook and runs real `git commit`s through it, with this test
// binary acting as narc.
func TestGitPreCommitEndToEnd(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	// The user's own hook, which narc must keep and run after itself.
	writeExecutable(t, r.path(".git/hooks/pre-commit"), "#!/bin/sh\necho \"user hook ran\"\n")
	if _, err := Install("git", InstallOptions{RepoRoot: r.dir, Command: narcCLI(t)}); err != nil {
		t.Fatal(err)
	}
	commit := func(msg string) (string, error) {
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = r.dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	r.write("src/math.js", cheatedMath)
	r.git("add", "-A")
	out, err := commit("cheat")
	if err == nil {
		t.Fatalf("the commit with a cheat succeeded:\n%s", out)
	}
	wantContains(t, "git commit output", out, "commit blocked", "hooktest-cheat src/math.js:2")
	wantNotContains(t, "git commit output", out, "user hook ran")

	r.git("reset", "-q", "--hard")
	r.write("README.md", "# demo\n\nUsage: npm test\n")
	r.git("add", "-A")
	out, err = commit("docs")
	if err != nil {
		t.Fatalf("a clean commit was blocked: %v\n%s", err, out)
	}
	wantContains(t, "git commit output", out, "user hook ran")
	if log := r.git("log", "--oneline"); !strings.Contains(log, "docs") {
		t.Fatalf("commit missing from log:\n%s", log)
	}

	// Without narc on the PATH the hook lets commits through.
	if _, err := Install("git", InstallOptions{RepoRoot: r.dir, Command: "/nonexistent/narc"}); err != nil {
		t.Fatal(err)
	}
	r.write("src/math.js", cheatedMath)
	r.git("add", "-A")
	out, err = commit("no narc")
	if err != nil {
		t.Fatalf("commit failed without narc: %v\n%s", err, out)
	}
	wantContains(t, "git commit output", out, "narc is not installed or not on PATH", "user hook ran")
}
