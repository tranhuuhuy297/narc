package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pre-edit checks judge each edited file from the session baseline, so the
// agent's own work in progress is not treated as the reference.
func TestPreEditJudgesFromSessionBaseline(t *testing.T) {
	t.Parallel()
	const checks = "def test_parse():\n    __narc_check__(parse('1') == 1)\n    __narc_check__(parse('2') == 2)\n"
	files := map[string]string{
		".narc.yml":                "protected:\n  - \"tests/golden/**\"\n",
		"tests/golden/invoice.txt": "Total: 107.00 EUR\n",
		"tests/test_parse.py":      checks,
	}
	for k, v := range baseFiles {
		files[k] = v
	}
	type step struct {
		write map[string]string // files changed on disk earlier in the session (e.g. by a shell command)
		edit  [3]string         // file, old_string, new_string
		deny  []string          // substrings of the deny reason; nil means allow
		not   []string          // substrings the deny reason must not contain
	}
	tests := []struct {
		name string
		step
	}{
		{
			name: "editing a golden file the agent created this session",
			step: step{
				write: map[string]string{"tests/golden/refund.txt": "Total: -12.00 EUR\n"},
				edit:  [3]string{"tests/golden/refund.txt", "Total: -12.00 EUR", "Total: -12.50 EUR"},
			},
		},
		{
			name: "editing a golden file that existed before the session",
			step: step{
				edit: [3]string{"tests/golden/invoice.txt", "Total: 107.00 EUR", "Total: 100.00 EUR"},
				deny: []string{"protected-path tests/golden/invoice.txt:1 — protected file was modified (+1 -1)"},
			},
		},
		{
			name: "a golden file already changed this session is changed further",
			step: step{
				write: map[string]string{"tests/golden/invoice.txt": "Total: 107.00 EUR\nVAT: 7%\n"},
				edit:  [3]string{"tests/golden/invoice.txt", "Total: 107.00 EUR", "Total: 100.00 EUR"},
				deny:  []string{"protected-path tests/golden/invoice.txt:1 — protected file was modified (+2 -1)"},
			},
		},
		{
			name: "removing a check the agent itself added this session",
			step: step{
				write: map[string]string{"tests/test_parse.py": checks + "    __narc_check__(parse('3') == 4)\n"},
				edit:  [3]string{"tests/test_parse.py", "    __narc_check__(parse('3') == 4)\n", ""},
			},
		},
		{
			name: "restoring one of two checks removed earlier is an improvement",
			step: step{
				write: map[string]string{"tests/test_parse.py": "def test_parse():\n    pass\n"},
				edit:  [3]string{"tests/test_parse.py", "    pass\n", "    __narc_check__(parse('1') == 1)\n"},
			},
		},
		{
			name: "removing a second check after one was removed earlier",
			step: step{
				write: map[string]string{"tests/test_parse.py": "def test_parse():\n    __narc_check__(parse('2') == 2)\n"},
				edit:  [3]string{"tests/test_parse.py", "    __narc_check__(parse('2') == 2)\n", "    pass\n"},
				deny:  []string{"hooktest-dropped tests/test_parse.py:2 — check __narc_check__(parse('2') == 2) removed"},
				not:   []string{"parse('1')"},
			},
		},
		{
			name: "a cheat in a file the agent created this session",
			step: step{
				write: map[string]string{"src/div.js": "export function div(a, b) {\n  return a / b\n}\n"},
				edit:  [3]string{"src/div.js", "  return a / b\n", "  __narc_cheat__()\n  return a / b\n"},
				deny:  []string{"hooktest-cheat src/div.js:2"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, files)
			runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
			for p, c := range tc.write {
				r.write(p, c)
			}
			h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
				"file_path": r.path(tc.edit[0]), "old_string": tc.edit[1], "new_string": tc.edit[2]}))
			if tc.deny == nil {
				h.wantSilent(t)
				return
			}
			m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
				"hookSpecificOutput.permissionDecisionReason", "systemMessage")
			reason := str(m, "hookSpecificOutput.permissionDecisionReason")
			wantContains(t, "deny reason", reason, tc.deny...)
			wantNotContains(t, "deny reason", reason, tc.not...)
		})
	}
}

// A patch may touch the same file more than once; later operations see
// the earlier ones.
func TestPatchOperationsApplyInOrder(t *testing.T) {
	t.Parallel()
	files := map[string]string{"tests/test_a.py": "def test_a():\n    __narc_check__(1)\n"}
	tests := []struct {
		name, patch, deny string
	}{
		{
			name: "delete and add back with more tests",
			patch: "*** Begin Patch\n*** Delete File: tests/test_a.py\n*** Add File: tests/test_a.py\n" +
				"+def test_a():\n+    __narc_check__(1)\n+\n+def test_b():\n+    __narc_check__(2)\n*** End Patch\n",
		},
		{
			name: "delete and add back with a cheat",
			patch: "*** Begin Patch\n*** Delete File: tests/test_a.py\n*** Add File: tests/test_a.py\n" +
				"+def test_a():\n+    __narc_check__(1)\n+    __narc_cheat__()\n*** End Patch\n",
			deny: "hooktest-cheat tests/test_a.py:3",
		},
		{
			name: "two updates of one file",
			patch: "*** Begin Patch\n*** Update File: tests/test_a.py\n@@\n-    __narc_check__(1)\n+    __narc_check__(1)\n+    __narc_check__(2)\n" +
				"*** Update File: tests/test_a.py\n@@\n-    __narc_check__(2)\n+    __narc_check__(2)\n+    __narc_check__(3)\n*** End Patch\n",
		},
		{
			name: "two updates, the second removes the original check",
			patch: "*** Begin Patch\n*** Update File: tests/test_a.py\n@@\n+def test_b():\n+    pass\n" +
				"*** Update File: tests/test_a.py\n@@\n-    __narc_check__(1)\n+    pass\n*** End Patch\n",
			deny: "hooktest-dropped tests/test_a.py:2 — check __narc_check__(1) removed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, files)
			h := runHook(t, "codex", "pre-tool-use", r.dir, codexPatch("c", r.dir, tc.patch))
			if tc.deny == "" {
				h.wantSilent(t)
				return
			}
			m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason")
			wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), tc.deny)
		})
	}
}

func TestShellPatch(t *testing.T) {
	t.Parallel()
	const body = "*** Begin Patch\n*** Add File: x.py\n+x = 1\n*** End Patch\n"
	for cmd, want := range map[string]struct {
		ok  bool
		dir string
	}{
		"apply_patch <<'EOF'\n" + body + "EOF\n":          {ok: true},
		"apply_patch <<EOF\n" + body + "EOF":              {ok: true},
		"applypatch <<\"PATCH\"\n" + body + "PATCH\n":     {ok: true},
		"cd services/api && apply_patch <<'EOF'\n" + body: {ok: true, dir: "services/api"},
		"bash -lc 'apply_patch <<EOF\n" + body + "EOF'":   {ok: true},
		"apply_patch \"" + body + "\"":                    {ok: true},
		// Commands that only contain patch text.
		"cat > docs/patch-format.md <<'EOF'\n" + body + "EOF\n":       {},
		"echo 'run apply_patch' && cat <<EOF > notes.txt\n" + body:    {},
		"grep -n 'apply_patch' README.md\nprintf '%s' '" + body + "'": {},
		"npm test": {},
	} {
		_, dir, ok := shellPatch(cmd)
		if ok != want.ok || dir != want.dir {
			t.Errorf("shellPatch(%q) = (%q, %v), want (%q, %v)", cmd, dir, ok, want.dir, want.ok)
		}
	}
}

func TestCodexShellPatchPrecision(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		".narc.yml":               "protected:\n  - \"tests/golden/**\"\n",
		"tests/golden/out.txt":    "42\n",
		"services/api/handler.js": "export const handle = () => 1\n",
	}
	r := newRepo(t, files)
	bash := func(command string) hookResult {
		return runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
			"tool_name": "Bash", "tool_use_id": "call_4", "tool_input": map[string]any{"command": command}}))
	}
	// Writing documentation that shows a patch is not a patch.
	bash("cat > docs/apply-patch.md <<'EOF'\n*** Begin Patch\n*** Delete File: tests/golden/out.txt\n*** End Patch\nEOF\n").wantSilent(t)
	// A patch run from another directory is resolved there.
	h := bash("cd services/api && apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: handler.js\n@@\n-export const handle = () => 1\n" +
		"+export const handle = () => __narc_cheat__()\n*** End Patch\nEOF\n")
	m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason")
	wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), "hooktest-cheat services/api/handler.js:1")

	// Codex's shell tool sends argv, and runs it in workdir.
	h = runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
		"tool_name": "shell", "tool_use_id": "call_5", "tool_input": map[string]any{
			"command": []string{"bash", "-lc", "apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: handler.js\n@@\n-export const handle = () => 1\n" +
				"+export const handle = () => __narc_cheat__()\n*** End Patch\nEOF\n"},
			"workdir": filepath.Join(r.dir, "services", "api")}}))
	m = h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason")
	wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), "hooktest-cheat services/api/handler.js:1")
	runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
		"tool_name": "shell", "tool_use_id": "call_6", "tool_input": map[string]any{"command": []string{"npm", "test"}}})).wantSilent(t)
}

// Only Cursor's top-level fields mark a payload as Cursor's.
func TestCursorPayloadDetection(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/version.js", "export const cursor_version = 1\n")
	r.commit("version")
	// A Claude edit whose text is exactly "cursor_version".
	h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
		"file_path": r.path("src/version.js"), "old_string": "cursor_version", "new_string": "cursor_version = __narc_cheat__(), unused"}))
	h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
		"hookSpecificOutput.permissionDecisionReason", "systemMessage")

	for raw, want := range map[string]bool{
		`{"session_id":"s","tool_input":{"old_string":"cursor_version"}}`:   false,
		`{"session_id":"s","tool_input":{"content":"\"conversation_id\""}}`: false,
		`{"conversation_id":"c","cursor_version":"3.14.2"}`:                 true,
		`{"conversation_id":"c","hook_event_name":"preToolUse"}`:            true,
		`{"cursor_version": "3.14.2", "tool_name": `:                        true,
	} {
		if got := isCursorPayload([]byte(raw)); got != want {
			t.Errorf("isCursorPayload(%s) = %v, want %v", raw, got, want)
		}
	}
}

// OpenCode's edit tool writes newString as the whole file when oldString
// is empty; Claude's Edit refuses that on a file with content.
func TestEmptyOldString(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	h := runHook(t, "opencode", "pre-tool-use", r.dir, map[string]any{"session_id": "o", "tool": "edit", "cwd": r.dir,
		"args": map[string]any{"filePath": r.path("src/math.js"), "oldString": "", "newString": cheatedMath}})
	wantContains(t, "reason", str(h.wantShape(t, "deny", "reason", "message"), "reason"), "hooktest-cheat src/math.js:2")

	runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
		"file_path": r.path("src/math.js"), "old_string": "", "new_string": cheatedMath})).wantSilent(t)
}

// The human is told, once, about waivers and narc configuration changes
// that let the session's changes through.
func TestStopReviewNotice(t *testing.T) {
	t.Parallel()
	const waived = "export function add(a, b) {\n  // narc:allow hooktest-cheat reason=\"the user asked for it in DEMO-12\"\n  __narc_cheat__()\n  return a + b\n}\n"

	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		// narc-config-weakened (rules/integrity) would block the stop for
		// the configuration changes below; this test is about the notice.
		const quiet = "rules:\n  narc-config-weakened: off\n"
		files := map[string]string{
			"src/legacy.js": "// narc:allow hooktest-cheat reason=\"kept for the v1 API\"\n__narc_cheat__()\n",
			".narc.yml":     quiet,
		}
		for k, v := range baseFiles {
			files[k] = v
		}
		r := newRepo(t, files)
		// Waived before the session: not the agent's doing.
		r.write("src/legacy.js", files["src/legacy.js"]+"export const legacy = true\n// narc:allow hooktest-cheat reason=\"kept for the v2 API\"\n__narc_cheat__()\n")
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantSilent(t)

		r.write("src/math.js", waived)
		notice := str(runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantShape(t, "systemMessage"), "systemMessage")
		want := "narc: please review these changes from this session:\n" +
			"• hooktest-cheat src/math.js:3 was waived — cheat marker __narc_cheat__() added (reason: \"the user asked for it in DEMO-12\")"
		if notice != want {
			t.Fatalf("systemMessage:\n%s\n--- want ---\n%s", notice, want)
		}
		runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantSilent(t)

		// The agent turns the rule off in narc's settings and cheats again.
		r.write(".narc.yml", quiet+"  hooktest-cheat: off\n")
		r.write("src/extra.js", "__narc_cheat__()\n")
		notice = str(runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantShape(t, "systemMessage"), "systemMessage")
		wantContains(t, "systemMessage", notice, "• .narc.yml changed: it decides what narc checks and blocks")
		wantNotContains(t, "systemMessage", notice, "src/math.js", "extra.js")
		runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantSilent(t)
		r.write(".narc.yml", quiet+"  hooktest-cheat: info\n")
		wantContains(t, "systemMessage", str(runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantShape(t, "systemMessage"), "systemMessage"), ".narc.yml changed")
	})

	t.Run("subagents leave it to the main agent", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.write("src/math.js", waived)
		runHook(t, "claude", "subagent-stop", r.dir, claudePayload("SubagentStop", "s", r.dir, map[string]any{
			"stop_hook_active": false, "agent_id": "a1", "agent_type": "general-purpose"})).wantSilent(t)
		wantContains(t, "systemMessage", str(runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantShape(t, "systemMessage"), "systemMessage"), "was waived")
	})

	t.Run("other harnesses", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		r.write("src/math.js", waived)
		h := runHook(t, "codex", "stop", r.dir, codexPayload("Stop", "c", r.dir, map[string]any{"stop_hook_active": false, "last_assistant_message": nil}))
		wantContains(t, "systemMessage", str(h.wantShape(t, "systemMessage"), "systemMessage"), "was waived")
		h = runHook(t, "gemini", "after-agent", r.dir, geminiPayload("AfterAgent", "g", r.dir, map[string]any{"stop_hook_active": false}))
		wantContains(t, "systemMessage", str(h.wantShape(t, "systemMessage"), "systemMessage"), "was waived")
		h = runHook(t, "opencode", "stop", r.dir, map[string]any{"session_id": "o", "cwd": r.dir, "loop_count": 0})
		wantContains(t, "message", str(h.wantShape(t, "message"), "message"), "was waived")
		h = runHook(t, "cursor", "stop", r.dir, cursorPayload("stop", "cur", r.dir, map[string]any{"status": "completed", "loop_count": 0}))
		h.wantShape(t)
		wantContains(t, "stderr", h.stderr, "was waived")
	})
}

// If narc cannot save its state it lets a repeated stop through rather
// than risk an endless loop, and says so.
func TestStopWithoutWritableState(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	// A file where narc's state directory should be.
	if err := os.WriteFile(filepath.Join(gitDir, "narc"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.write("src/math.js", cheatedMath)
	runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantShape(t, "decision", "reason")
	notice := str(runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, true)).wantShape(t, "systemMessage"), "systemMessage")
	wantContains(t, "systemMessage", notice, "after 1 blocked attempt.", "hooktest-cheat src/math.js:2")
}

// Dotfile managers symlink settings files; narc writes through the link.
func TestInstallWritesThroughSymlink(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dotfile := filepath.Join(t.TempDir(), "dotfiles", "claude-settings.json")
	writeTestFile(t, dotfile, "{\"model\": \"opus\"}\n")
	link := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfile, link); err != nil {
		// narc:allow skip-added reason="creating symlinks needs extra privileges on Windows"
		t.Skip("symlinks not supported:", err)
	}
	mustInstall(t, "claude", InstallOptions{Scope: ScopeUser, Home: home})
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced (mode %v, err %v)", st.Mode(), err)
	}
	wantContains(t, "dotfile", readFile(t, dotfile), `"model": "opus"`, "narc hook claude stop")
	mustUninstall(t, "claude", InstallOptions{Scope: ScopeUser, Home: home})
	if got := readFile(t, dotfile); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Fatalf("dotfile after uninstall:\n%s", got)
	}
}

func TestInstallGitHooksPath(t *testing.T) {
	t.Parallel()
	t.Run("a hooks directory shared with other repositories is refused", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		shared := t.TempDir()
		r.git("config", "core.hooksPath", shared)
		_, err := Install("git", InstallOptions{RepoRoot: r.dir})
		if err == nil || !strings.Contains(err.Error(), "outside this repository") || !strings.Contains(err.Error(), "narc hook git pre-commit") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(shared, "pre-commit")); !os.IsNotExist(err) {
			t.Fatal("a hook was written to the shared hooks directory")
		}
	})
	t.Run("an empty hook file is replaced", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		writeExecutable(t, r.path(".git/hooks/pre-commit"), "\n")
		mustInstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if got := readFile(t, r.path(".git/hooks/pre-commit")); got != gitHookFresh {
			t.Fatalf("hook:\n%s", got)
		}
	})
	t.Run("linked worktrees share the repository's hooks", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		wt := filepath.Join(t.TempDir(), "wt")
		r.git("worktree", "add", "-q", wt)
		mustInstall(t, "git", InstallOptions{RepoRoot: wt})
		if got := readFile(t, r.path(".git/hooks/pre-commit")); got != gitHookFresh {
			t.Fatalf("hook:\n%s", got)
		}
	})
}

// While .narc.yml cannot be loaded every hook fails open, so narc would
// silently check nothing.
func TestBrokenNarcConfig(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
	write := func(content string) hookResult {
		return runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Write", map[string]any{
			"file_path": r.path(".narc.yml"), "content": content}))
	}
	// An edit that would break it is denied, with the parse error.
	m := write("hook:\n  block_on: sometimes\n").wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
		"hookSpecificOutput.permissionDecisionReason", "systemMessage")
	wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"),
		"it would make .narc.yml invalid (hook.block_on: unknown severity \"sometimes\"", "Fix the YAML")
	wantNotContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), "narc:allow")
	// A valid change, and a look-alike file elsewhere, are fine.
	write("protected:\n  - \"tests/golden/**\"\n").wantSilent(t)
	runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Write", map[string]any{
		"file_path": r.path("docs/.narc.yml"), "content": "this: [is not narc's config\n"})).wantSilent(t)
	// narc loads .narc.yml before narc.yaml, so narc.yaml does not matter
	// while .narc.yml exists. (narc-config-weakened, from rules/integrity,
	// judges every config file name on its own; it is not what this
	// checks.)
	r.write(".narc.yml", "protected:\n  - \"tests/golden/**\"\nrules:\n  narc-config-weakened: off\n")
	runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Write", map[string]any{
		"file_path": r.path("narc.yaml"), "content": "this: [is not used\n"})).wantSilent(t)
	r.remove(".narc.yml")
	write("hook:\n  max_blocks: 2\n").wantSilent(t)

	// Broken by other means: a stop still lets the agent stop, but the
	// human learns that narc is off.
	r.write(".narc.yml", "hook:\n  block_on: sometimes\n")
	r.write("src/math.js", cheatedMath)
	h := runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false))
	wantContains(t, "systemMessage", str(h.wantShape(t, "systemMessage"), "systemMessage"),
		"narc: cannot load its configuration (", `unknown severity "sometimes"`, "so it is not checking this session's changes")
	wantContains(t, "stderr", h.stderr, "(hook allowed the action)")
	h = runHook(t, "codex", "stop", r.dir, codexPayload("Stop", "c", r.dir, map[string]any{"stop_hook_active": false, "last_assistant_message": nil}))
	h.wantShape(t, "systemMessage")
	h = runHook(t, "cursor", "stop", r.dir, cursorPayload("stop", "cur", r.dir, map[string]any{"status": "completed", "loop_count": 0}))
	h.wantShape(t)
	wantContains(t, "stderr", h.stderr, "not checking this session's changes")
	// Pre-edit checks fail open quietly (no message on every edit).
	h = runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Write", map[string]any{
		"file_path": r.path("src/x.js"), "content": "__narc_cheat__()\n"}))
	if h.code != 0 || h.stdout != "" {
		t.Fatalf("pre-edit with a broken config: exit %d stdout %q", h.code, h.stdout)
	}
	wantContains(t, "stderr", h.stderr, "config:", "(hook allowed the action)")
}

// With narc's real rules: reworking tests the agent wrote in this session
// is not weakening verification, but dropping an assertion that existed
// before the session is.
func TestPreEditRealRulesWorkInProgress(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"calc.py":            "def add(a, b):\n    return a + b\n",
		"tests/test_calc.py": "from calc import add\n\n\ndef test_add():\n    assert add(1, 2) == 3\n    assert add(-1, 1) == 0\n",
	}
	const mulTests = "from calc import mul\n\n\ndef test_mul():\n    assert mul(2, 3) == 6\n    assert mul(0, 3) == 0\n\n\ndef test_mul_negative():\n    assert mul(-2, 3) == -6\n"
	for _, tc := range []struct {
		name, file, old, new string
		deny                 string
	}{
		{name: "remove a test it wrote", file: "tests/test_mul.py", old: "\n\ndef test_mul_negative():\n    assert mul(-2, 3) == -6\n"},
		{name: "drop an assertion it wrote", file: "tests/test_mul.py", old: "    assert mul(0, 3) == 0\n"},
		{name: "comment out an assertion it wrote", file: "tests/test_mul.py", old: "    assert mul(0, 3) == 0\n", new: "    # assert mul(0, 3) == 0\n"},
		{name: "drop an assertion that existed before", file: "tests/test_calc.py", old: "    assert add(-1, 1) == 0\n", deny: "assertions-removed tests/test_calc.py:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, files)
			runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
			// Earlier in the session: a new function and its tests.
			r.write("calc.py", files["calc.py"]+"\n\ndef mul(a, b):\n    return a * b\n")
			r.write("tests/test_mul.py", mulTests)
			h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
				"file_path": r.path(tc.file), "old_string": tc.old, "new_string": tc.new}))
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

// Following the coaching message's waiver advice to the letter (a waiver
// on the line above the reported location) makes the retried edit pass,
// for added code and for removed code.
func TestWaiverAdviceWorks(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"tests/test_calc.py": "from calc import add\n\n\ndef test_add():\n    assert add(1, 2) == 3\n    assert add(-1, 1) == 0\n",
	}
	for k, v := range baseFiles {
		files[k] = v
	}
	for _, tc := range []struct {
		name, file  string
		edit, retry [2]string // old_string, new_string
		advice      string
	}{
		{
			name: "added code", file: "src/math.js",
			edit:   [2]string{"  return a + b\n", "  __narc_cheat__()\n  return a + b\n"},
			advice: `on the line above src/math.js:2, e.g. // narc:allow hooktest-cheat reason="<why>"`,
			retry:  [2]string{"  return a + b\n", "  // narc:allow hooktest-cheat reason=\"the user asked for a stub in DEMO-7\"\n  __narc_cheat__()\n  return a + b\n"},
		},
		{
			name: "removed code", file: "tests/test_calc.py",
			edit:   [2]string{"    assert add(-1, 1) == 0\n", ""},
			advice: `on the line above tests/test_calc.py:5, e.g. # narc:allow assertions-removed reason="<why>"`,
			retry: [2]string{"    assert add(1, 2) == 3\n    assert add(-1, 1) == 0\n",
				"    # narc:allow assertions-removed reason=\"the user dropped negative inputs from the spec, DEMO-9\"\n    assert add(1, 2) == 3\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, files)
			edit := func(e [2]string) hookResult {
				return runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
					"file_path": r.path(tc.file), "old_string": e[0], "new_string": e[1]}))
			}
			m := edit(tc.edit).wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
				"hookSpecificOutput.permissionDecisionReason", "systemMessage")
			wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), tc.advice)
			edit(tc.retry).wantSilent(t)
		})
	}
}
