package hook

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `block_on: off` and `fail_on: off` mean "never": config.ParseSeverity
// reads off as a severity below info, which would block on everything.
func TestSeverityOffMeansNever(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		".narc.yml": "fail_on: off\nhook:\n  block_on: off\n",
		// An info-level finding: any-cast in a test file.
		"src/auth.test.ts": "import { verify } from './auth'\n\ntest('rejects expired tokens', () => {\n  expect(verify('a.50', 100)).toBe(false)\n})\n",
	}
	for k, v := range baseFiles {
		files[k] = v
	}
	r := newRepo(t, files)
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
	edit := func(file, old, new string) hookResult {
		return runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
			"file_path": r.path(file), "old_string": old, "new_string": new}))
	}
	edit("src/auth.test.ts", "verify('a.50', 100)", "verify('a.50' as any, 100)").wantSilent(t)
	edit("src/math.js", "  return a + b\n", "  __narc_cheat__()\n  return a + b\n").wantSilent(t)

	r.write("src/math.js", cheatedMath)
	runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantSilent(t)

	r.git("add", "-A")
	h := runHook(t, "git", "pre-commit", r.dir, nil)
	if h.code != 0 {
		t.Fatalf("pre-commit exit %d with fail_on: off\n%s", h.code, h.stderr)
	}
	wantContains(t, "pre-commit report", h.stderr, "commit allowed (fail_on: off)", "hooktest-cheat src/math.js:2")
}

// An edit is judged in the repository that owns the file, with that
// repository's .narc.yml, whatever directory the session started in.
func TestPreEditUsesTheFilesRepository(t *testing.T) {
	t.Parallel()
	const narcYML = "allow:\n  - rule: hooktest-cheat\n    path: \"src/legacy/**\"\n    reason: \"legacy shim approved in DEMO-3\"\n"
	initRepo := func(t *testing.T, dir string) *testRepo {
		r := &testRepo{t: t, dir: dir}
		cmd := exec.Command("git", "init", "-q", "-b", "main", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		r.write(".narc.yml", narcYML)
		r.write("src/legacy/shim.js", "export const shim = 1\n")
		r.write("src/app.js", "export const app = 1\n")
		r.commit("initial")
		return r
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (session string, project *testRepo)
	}{
		{
			name: "session started in a parent folder",
			setup: func(t *testing.T) (string, *testRepo) {
				parent := t.TempDir()
				return parent, initRepo(t, filepath.Join(parent, "project"))
			},
		},
		{
			name: "nested repository inside another one",
			setup: func(t *testing.T) (string, *testRepo) {
				outer := newRepo(t, map[string]string{".gitignore": "vendor/\n", "README.md": "# outer\n"})
				return outer.dir, initRepo(t, outer.path("vendor/lib"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			session, project := tc.setup(t)
			edit := func(file, old, new string) hookResult {
				return runHook(t, "claude", "pre-tool-use", session, claudeTool("s", session, "Edit", map[string]any{
					"file_path": project.path(file), "old_string": old, "new_string": new}))
			}
			// The project's own waiver applies.
			edit("src/legacy/shim.js", "export const shim = 1\n", "export const shim = __narc_cheat__()\n").wantSilent(t)
			// Elsewhere it is still a cheat, reported at the project's path.
			m := edit("src/app.js", "export const app = 1\n", "export const app = __narc_cheat__()\n").wantShape(t,
				"hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason", "systemMessage")
			reason := str(m, "hookSpecificOutput.permissionDecisionReason")
			wantContains(t, "deny reason", reason, "✗ hooktest-cheat src/app.js:1 —")
			wantNotContains(t, "deny reason", reason, "project/src", "vendor/lib")
		})
	}
}

// Codex's shell tool may send apply_patch as argv; joined, the patch
// starts on the same line as the program name.
func TestArgvApplyPatch(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	patch := "*** Begin Patch\n*** Update File: src/math.js\n@@\n-  return a + b\n+  __narc_cheat__()\n+  return a + b\n*** End Patch\n"
	for _, tool := range []string{"shell", "apply_patch"} {
		h := runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
			"tool_name": tool, "tool_use_id": "call_7", "tool_input": map[string]any{"command": []string{"apply_patch", patch}}}))
		m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason")
		wantContains(t, tool+" deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), "hooktest-cheat src/math.js:2")
	}
	// Documentation that quotes a patch is still not one.
	runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
		"tool_name": "shell", "tool_use_id": "call_8", "tool_input": map[string]any{
			"command": []string{"bash", "-lc", "cat > docs/patch.md <<'EOF'\n" + patch + "EOF\n"}}})).wantSilent(t)
}

// An end-of-file chunk is matched at the end whatever came before it, so
// it can overlap an earlier chunk; that patch is rejected, not a panic.
func TestApplyChunksOverlap(t *testing.T) {
	t.Parallel()
	_, err := applyChunks("a\nb\nc\nd\n", []patchChunk{
		{old: []string{"b", "c", "d"}, new: []string{"B"}},
		{old: []string{"c", "d"}, eof: true},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("err = %v, want an overlap error", err)
	}
	r := newRepo(t, baseFiles)
	runHook(t, "codex", "pre-tool-use", r.dir, codexPatch("c", r.dir, "*** Begin Patch\n*** Update File: src/math.js\n@@\n"+
		"-export function sub(a, b) {\n-  return a - b\n-}\n+__narc_cheat__()\n@@\n-  return a - b\n-}\n*** End of File\n*** End Patch\n")).wantSilent(t)
}

// Rules that look at files an edit does not touch (a test's helpers in
// the same Go package) see them in pre-edit checks too, as `narc scan`
// does: moving assertions into a helper is fine, a helper that checks
// nothing is not.
func TestPreEditSeesUnchangedFiles(t *testing.T) {
	t.Parallel()
	const test = "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"1+2\")\n\t}\n\tif Add(-1, 1) != 0 {\n\t\tt.Fatal(\"-1+1\")\n\t}\n}\n"
	const refactored = "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tsumIs(t, 1, 2, 3)\n\tsumIs(t, -1, 1, 0)\n}\n"
	for _, tc := range []struct {
		name, helper string
		deny         string
	}{
		{
			name:   "helper that asserts",
			helper: "package calc\n\nimport \"testing\"\n\nfunc sumIs(t *testing.T, a, b, want int) {\n\tt.Helper()\n\tif got := Add(a, b); got != want {\n\t\tt.Fatalf(\"Add(%d, %d) = %d, want %d\", a, b, got, want)\n\t}\n}\n",
		},
		{
			name:   "helper that checks nothing",
			helper: "package calc\n\nimport \"testing\"\n\nfunc sumIs(t *testing.T, a, b, want int) {\n\tt.Helper()\n\t_ = Add(a, b)\n}\n",
			deny:   "test-hollowed calc/calc_test.go:",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, map[string]string{
				"go.mod":             "module example.com/calc\n\ngo 1.22\n",
				"calc/calc.go":       "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
				"calc/calc_test.go":  test,
				"calc/check_test.go": tc.helper,
			})
			runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
			h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Write", map[string]any{
				"file_path": r.path("calc/calc_test.go"), "content": refactored}))
			if tc.deny == "" {
				h.wantSilent(t)
				return
			}
			m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
				"hookSpecificOutput.permissionDecisionReason", "systemMessage")
			wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), tc.deny)
		})
	}
}

// `git commit -a` and `git commit <paths>` commit from a temporary index;
// narc judges what is being committed, not what happens to be staged.
func TestGitPreCommitTemporaryIndex(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	if _, err := Install("git", InstallOptions{RepoRoot: r.dir, Command: narcCLI(t)}); err != nil {
		t.Fatal(err)
	}
	commit := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"commit"}, args...)...)
		cmd.Dir = r.dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	r.write("src/math.js", cheatedMath)
	r.write("README.md", "# demo\n\nUsage: npm test\n")
	// Only README.md is committed: the cheat stays in the work tree.
	if out, err := commit("-m", "docs", "README.md"); err != nil {
		t.Fatalf("committing README.md alone was blocked: %v\n%s", err, out)
	}
	out, err := commit("-a", "-m", "everything")
	if err == nil {
		t.Fatalf("git commit -a with a cheat succeeded:\n%s", out)
	}
	wantContains(t, "git commit output", out, "commit blocked", "hooktest-cheat src/math.js:2")
}
