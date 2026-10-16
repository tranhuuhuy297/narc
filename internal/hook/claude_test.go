package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudePreToolUse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		setup  func(r *testRepo) // extra state before the edit
		tool   string
		input  func(r *testRepo) map[string]any
		deny   []string // substrings of the deny reason; nil means allow
		silent bool     // allow without even a stderr note
	}{
		{
			name: "edit adds a cheat",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "  return a + b\n", "new_string": "  __narc_cheat__()\n  return a + b\n", "replace_all": false}
			},
			deny: []string{"✗ hooktest-cheat src/math.js:2 — cheat marker __narc_cheat__() added", "→ Remove the cheat marker"},
		},
		{
			name: "write creates a file with a cheat",
			tool: "Write",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/div.js"), "content": "export function div(a, b) {\n  __narc_cheat__()\n  return a / b\n}\n"}
			},
			deny: []string{"hooktest-cheat src/div.js:2"},
		},
		{
			name: "write overwrites a file, adding a cheat",
			tool: "Write",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath}
			},
			deny: []string{"hooktest-cheat src/math.js:2"},
		},
		{
			name: "replace_all applies every occurrence",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "(a, b) {\n", "new_string": "(a, b) {\n  __narc_cheat__()\n", "replace_all": true}
			},
			deny: []string{"hooktest-cheat src/math.js:2", "hooktest-cheat src/math.js:7"},
		},
		{
			name: "legacy MultiEdit applies edits in order",
			tool: "MultiEdit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "edits": []any{
					map[string]any{"old_string": "return a - b", "new_string": "return b - a"},
					map[string]any{"old_string": "return b - a", "new_string": "__narc_cheat__(); return b - a"},
				}}
			},
			deny: []string{"hooktest-cheat src/math.js:6"},
		},
		{
			name: "relative path and CRLF file",
			setup: func(r *testRepo) {
				r.write("src/win.js", "export const x = 1\r\nexport const y = 2\r\n")
			},
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": "src/win.js", "old_string": "export const y = 2\n", "new_string": "export const y = __narc_cheat__()\n"}
			},
			deny: []string{"hooktest-cheat src/win.js:2"},
		},
		{
			name: "clean edit is allowed",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "  return a - b\n}\n", "new_string": "  return a - b\n}\n\nexport function mul(a, b) {\n  return a * b\n}\n"}
			},
			silent: true,
		},
		{
			name: "look-alike text in a comment and a string is allowed",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "  return a + b\n", "new_string": "  // never call __narc_cheat__() here\n  console.debug('__narc_cheat__() is banned')\n  return a + b\n"}
			},
			silent: true,
		},
		{
			name: "edit next to a cheat that was already committed is allowed",
			setup: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.commit("legacy cheat")
			},
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "return a - b", "new_string": "return a - b // checked"}
			},
			silent: true,
		},
		{
			name: "moving an existing cheat within the file is allowed",
			setup: func(r *testRepo) {
				r.write("src/math.js", cheatedMath)
				r.commit("legacy cheat")
			},
			tool: "Write",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "content": "export function sub(a, b) {\n  return a - b\n}\n\nexport function add(a, b) {\n  __narc_cheat__()\n  return a + b\n}\n"}
			},
			silent: true,
		},
		{
			name: "inline waiver with a reason is allowed",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "  return a + b\n", "new_string": "  // narc:allow hooktest-cheat reason=\"user asked for it in ticket DEMO-12\"\n  __narc_cheat__()\n  return a + b\n"}
			},
			silent: true,
		},
		{
			name: "waiver without a reason does not apply",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "  return a + b\n", "new_string": "  __narc_cheat__() // narc:allow hooktest-cheat\n  return a + b\n"}
			},
			deny: []string{"hooktest-cheat src/math.js:2"},
		},
		{
			name: "old_string not found: the tool fails by itself",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "old_string": "return a * b", "new_string": "__narc_cheat__()"}
			},
			silent: true,
		},
		{
			name: "file outside the repository is not judged",
			tool: "Write",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": filepath.Join(filepath.Dir(r.dir), "elsewhere.js"), "content": "__narc_cheat__()\n"}
			},
			silent: true,
		},
		{
			name: "notebook edits are skipped",
			tool: "NotebookEdit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"notebook_path": r.path("nb.ipynb"), "cell_id": "c1", "new_source": "__narc_cheat__()", "edit_mode": "replace"}
			},
			silent: true,
		},
		{
			name: "non-edit tools are skipped",
			tool: "Bash",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"command": "echo '__narc_cheat__()' >> src/math.js", "description": "append"}
			},
			silent: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, baseFiles)
			if tc.setup != nil {
				tc.setup(r)
			}
			before := r.read("src/math.js")
			h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("sess-1", r.dir, tc.tool, tc.input(r)))
			if r.read("src/math.js") != before {
				t.Fatal("the hook modified a file on disk")
			}
			if tc.deny == nil {
				h.wantSilent(t)
				return
			}
			m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
				"hookSpecificOutput.permissionDecisionReason", "systemMessage")
			if str(m, "hookSpecificOutput.hookEventName") != "PreToolUse" || str(m, "hookSpecificOutput.permissionDecision") != "deny" {
				t.Fatalf("not a PreToolUse deny: %s", h.stdout)
			}
			reason := str(m, "hookSpecificOutput.permissionDecisionReason")
			wantContains(t, "deny reason", reason, tc.deny...)
			wantContains(t, "deny reason", reason,
				"narc blocked this edit",
				"Do not skip, delete or weaken tests, assertions, type checks, coverage or CI",
				`// narc:allow hooktest-cheat reason="<why>"`)
			wantContains(t, "systemMessage", str(m, "systemMessage"), "narc blocked an edit to src/", "(hooktest-cheat)")
		})
	}
}

func TestClaudePreToolUseConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  string
		file string
		edit [2]string // old_string, new_string
		deny string
	}{
		{
			name: "rule downgraded below block_on",
			cfg:  "rules:\n  hooktest-cheat: warning\n",
			file: "src/math.js",
			edit: [2]string{"  return a + b\n", "  __narc_cheat__()\n  return a + b\n"},
		},
		{
			name: "block_on warning blocks warnings",
			cfg:  "rules:\n  hooktest-cheat: warning\nhook:\n  block_on: warning\n",
			file: "src/math.js",
			edit: [2]string{"  return a + b\n", "  __narc_cheat__()\n  return a + b\n"},
			deny: "hooktest-cheat src/math.js:2",
		},
		{
			name: "rule turned off",
			cfg:  "rules:\n  hooktest-cheat: off\n",
			file: "src/math.js",
			edit: [2]string{"  return a + b\n", "  __narc_cheat__()\n  return a + b\n"},
		},
		{
			name: "protected golden file",
			cfg:  "protected:\n  - \"tests/golden/**\"\n",
			file: "tests/golden/invoice.txt",
			edit: [2]string{"Total: 107.00 EUR", "Total: 100.00 EUR"},
			deny: "protected-path tests/golden/invoice.txt:1 — protected file was modified (+1 -1)",
		},
		{
			name: "repository-level allow",
			cfg:  "protected:\n  - \"tests/golden/**\"\nallow:\n  - rule: protected-path\n    path: \"tests/golden/invoice.txt\"\n    reason: \"tax rate changed, FIN-12\"\n",
			file: "tests/golden/invoice.txt",
			edit: [2]string{"Total: 107.00 EUR", "Total: 100.00 EUR"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{".narc.yml": tc.cfg, "tests/golden/invoice.txt": "Total: 107.00 EUR\n"}
			for k, v := range baseFiles {
				files[k] = v
			}
			r := newRepo(t, files)
			h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
				"file_path": r.path(tc.file), "old_string": tc.edit[0], "new_string": tc.edit[1]}))
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

// A check moved to another file in two edits must not look like a deleted
// check: the second edit is judged with the session's other changes as
// context, while findings in those other files are not reported.
func TestPreToolUseUsesSessionContext(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"tests/sub.py": "from calc import sub\n\n\ndef test_sub():\n    __narc_check__(sub(3, 1) == 2)\n",
	}
	removal := map[string]any{"old_string": "def test_sub():\n    __narc_check__(sub(3, 1) == 2)\n", "new_string": "def test_sub():\n    pass\n"}

	t.Run("moved to a file written earlier in the session", func(t *testing.T) {
		r := newRepo(t, files)
		// Edit 1, already on disk: the check now lives in a new file, next
		// to an unrelated cheat that this edit must not be blamed for.
		r.write("tests/test_calc.py", "from calc import sub\n\n\ndef test_sub():\n    __narc_check__(sub(3, 1) == 2)\n\n__narc_cheat__()\n")
		removal["file_path"] = r.path("tests/sub.py")
		runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", removal)).wantSilent(t)
	})
	t.Run("deleted outright", func(t *testing.T) {
		r := newRepo(t, files)
		removal["file_path"] = r.path("tests/sub.py")
		h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", removal))
		m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
			"hookSpecificOutput.permissionDecisionReason", "systemMessage")
		reason := str(m, "hookSpecificOutput.permissionDecisionReason")
		wantContains(t, "deny reason", reason, "hooktest-dropped tests/sub.py:5 — check __narc_check__(sub(3, 1) == 2) removed",
			`# narc:allow hooktest-dropped reason="<why>"`)
	})
}

// With narc's real rules (registered by this package), skipping a failing
// test is denied.
func TestPreToolUseRealRuleSkipAdded(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	h := runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
		"file_path": r.path("src/math.test.js"), "old_string": "test('subtracts'", "new_string": "test.skip('subtracts'",
	}))
	m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
		"hookSpecificOutput.permissionDecisionReason", "systemMessage")
	wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), "skip-added src/math.test.js:7")
}

func TestClaudeFailsOpen(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	tests := []struct {
		name, event, payload, note string
	}{
		{"invalid JSON", "pre-tool-use", `{"tool_name": "Edit", "tool_input": {`, "invalid hook payload"},
		{"empty stdin", "pre-tool-use", "", "empty hook payload"},
		{"edit without a path", "pre-tool-use", `{"session_id":"s","cwd":"` + r.dir + `","tool_name":"Write","tool_input":{"content":"x"}}`, "has no file path"},
		{"stop outside a repository", "stop", `{"session_id":"s","cwd":"` + t.TempDir() + `","stop_hook_active":false}`, "not a git repository"},
		{"broken .narc.yml", "pre-tool-use", `{"session_id":"s","cwd":"` + brokenConfigRepo(t) + `","tool_name":"Write","tool_input":{"file_path":"a.txt","content":"b\n"}}`, "config:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := runHook(t, "claude", tc.event, r.dir, tc.payload)
			if h.code != 0 || h.stdout != "" {
				t.Fatalf("want a silent allow, got exit %d stdout %q", h.code, h.stdout)
			}
			wantContains(t, "stderr", h.stderr, tc.note, "(hook allowed the action)")
			if strings.Count(h.stderr, "\n") != 1 {
				t.Errorf("want a one-line note, got %q", h.stderr)
			}
		})
	}
}

func brokenConfigRepo(t *testing.T) string {
	r := newRepo(t, map[string]string{".narc.yml": "fail_on: sometimes\n", "a.txt": "a\n"})
	return r.dir
}

func TestUnknownHook(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	code := Run("claude", "pre-compact", strings.NewReader("{}"), &stdout, &stderr, testEnv(t.TempDir()))
	if code != 0 || stdout.String() != "" {
		t.Fatalf("exit %d stdout %q", code, stdout.String())
	}
	wantContains(t, "stderr", stderr.String(), `unknown hook "claude" "pre-compact"`)

	// Cursor blocks a permission hook (beforeReadFile, ...) whose output is
	// not a valid permission, so even an unknown one gets an explicit allow.
	stdout.Reset()
	code = Run("cursor", "before-read-file", strings.NewReader("{}"), &stdout, &stderr, testEnv(t.TempDir()))
	if code != 0 || stdout.String() != "{\"permission\":\"allow\"}\n" {
		t.Fatalf("cursor: exit %d stdout %q", code, stdout.String())
	}
	stdout.Reset()
	code = Run("cursor", "afterAgentResponse", strings.NewReader("{}"), &stdout, &stderr, testEnv(t.TempDir()))
	if code != 0 || stdout.String() != "{}\n" {
		t.Fatalf("cursor observer hook: exit %d stdout %q", code, stdout.String())
	}
}

func TestEventNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		harness, event string
		ok             bool
	}{
		{"claude", "pre-tool-use", true},
		{"claude", "PreToolUse", true},
		{"Claude", "pre_tool_use", true},
		{"claude", "SubagentStop", true},
		{"codex", "subagent-stop", false},
		{"gemini", "AfterAgent", true},
		{"gemini", "stop", false},
		{"cursor", "preToolUse", true},
		{"git", "pre-commit", true},
		{"git", "pre-push", false},
		{"vim", "stop", false},
	} {
		if got := Supports(tc.harness, tc.event); got != tc.ok {
			t.Errorf("Supports(%q, %q) = %v, want %v", tc.harness, tc.event, got, tc.ok)
		}
	}
	if got := strings.Join(Harnesses(), ","); got != "claude,codex,cursor,gemini,git,opencode" {
		t.Errorf("Harnesses() = %s", got)
	}
	if got := strings.Join(Events("gemini"), ","); got != "before-tool,after-tool,after-agent,session-start" {
		t.Errorf("Events(gemini) = %s", got)
	}
}

func TestHookLog(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	logPath := filepath.Join(t.TempDir(), "hooks.log")
	env := testEnv(r.dir)
	env.Getenv = func(k string) string {
		if k == LogEnvVar {
			return logPath
		}
		return ""
	}
	runHookEnv(t, "cursor", "pre-tool-use", env, cursorTool("c1", r.dir, "Write", map[string]any{"file_path": r.path("x.js"), "content": "1\n"}))
	runHookEnv(t, "cursor", "pre-tool-use", env, "not json")
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines, got %d:\n%s", len(lines), b)
	}
	wantContains(t, "log", lines[0], `"harness":"cursor"`, `"event":"pre-tool-use"`, `"tool_name":"Write"`)
	wantContains(t, "log", lines[1], `"payload":"not json"`)
}

// The edited path may be spelled through a symlink (macOS /var vs
// /private/var, or a symlinked checkout).
func TestPreToolUseThroughSymlink(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(r.dir, link); err != nil {
		// narc:allow skip-added reason="creating symlinks needs extra privileges on Windows"
		t.Skip("symlinks not supported:", err)
	}
	h := runHook(t, "claude", "pre-tool-use", link, claudeTool("s", link, "Write", map[string]any{
		"file_path": filepath.Join(link, "src", "math.js"), "content": cheatedMath,
	}))
	m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision",
		"hookSpecificOutput.permissionDecisionReason", "systemMessage")
	wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), "hooktest-cheat src/math.js:2")
}

func TestRunRecoversFromPanics(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	env := testEnv(r.dir)
	env.Now = func() time.Time { panic("clock exploded") }

	h := runHookEnv(t, "claude", "session-start", env, claudePayload("SessionStart", "s", r.dir, nil))
	if h.code != 0 || h.stdout != "" {
		t.Fatalf("exit %d stdout %q", h.code, h.stdout)
	}
	wantContains(t, "stderr", h.stderr, "internal error: clock exploded", "(hook allowed the action)")

	// A Claude hook run by Cursor still answers in Cursor's protocol.
	h = runHookEnv(t, "claude", "pre-tool-use", env, cursorTool("c", r.dir, "Write", map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath}))
	if m := h.wantShape(t, "permission"); m["permission"] != "allow" {
		t.Fatalf("permission = %v", m["permission"])
	}

	env.Getwd = func() (string, error) { panic("no working directory") }
	h = runHookEnv(t, "git", "pre-commit", env, nil)
	if h.code != 0 {
		t.Fatalf("exit %d, want 0", h.code)
	}
	wantContains(t, "stderr", h.stderr, "no working directory")
}
