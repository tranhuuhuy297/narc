package hook

import "testing"

func TestCursorPreToolUse(t *testing.T) {
	t.Parallel()
	files := map[string]string{".narc.yml": "protected:\n  - \"tests/golden/**\"\n", "tests/golden/out.txt": "ok\n"}
	for k, v := range baseFiles {
		files[k] = v
	}
	tests := []struct {
		name  string
		tool  string
		input func(r *testRepo) map[string]any
		deny  string
	}{
		{
			name: "Write with file_path",
			tool: "Write",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath}
			},
			deny: "hooktest-cheat src/math.js:2",
		},
		{
			name: "Write with path and contents",
			tool: "Write",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"path": r.path("src/math.js"), "contents": cheatedMath}
			},
			deny: "hooktest-cheat src/math.js:2",
		},
		{
			name: "StrReplace with path",
			tool: "StrReplace",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"path": r.path("src/math.js"), "old_string": "  return a + b\n", "new_string": "  __narc_cheat__()\n  return a + b\n"}
			},
			deny: "hooktest-cheat src/math.js:2",
		},
		{
			name: "Delete of a protected file",
			tool: "Delete",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"path": r.path("tests/golden/out.txt")}
			},
			deny: "protected-path tests/golden/out.txt",
		},
		{
			name: "clean StrReplace",
			tool: "StrReplace",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"path": r.path("README.md"), "old_string": "# demo", "new_string": "# Demo"}
			},
		},
		{
			name: "Edit sketch narc cannot replay",
			tool: "Edit",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"target_file": r.path("src/math.js"), "code_edit": "// ... existing code ...\n__narc_cheat__()\n"}
			},
		},
		{
			name: "Delete of a missing file",
			tool: "Delete",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"path": r.path("nope.txt")}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, files)
			h := runHook(t, "cursor", "pre-tool-use", r.dir, cursorTool("conv-1", r.dir, tc.tool, tc.input(r)))
			if tc.deny == "" {
				// Cursor blocks on empty or invalid output, so allow is explicit.
				m := h.wantShape(t, "permission")
				if m["permission"] != "allow" || h.stderr != "" {
					t.Fatalf("want an explicit allow, got %s (stderr %q)", h.stdout, h.stderr)
				}
				return
			}
			m := h.wantShape(t, "permission", "user_message", "agent_message")
			if m["permission"] != "deny" {
				t.Fatalf("permission = %v", m["permission"])
			}
			wantContains(t, "agent_message", str(m, "agent_message"), tc.deny, "narc blocked this edit")
			wantContains(t, "user_message", str(m, "user_message"), "narc blocked an edit to ")
		})
	}
}

func TestCursorFailsOpenWithValidJSON(t *testing.T) {
	t.Parallel()
	h := runHook(t, "cursor", "pre-tool-use", t.TempDir(), `{"tool_name": "Write", "tool_input": `)
	if m := h.wantShape(t, "permission"); m["permission"] != "allow" {
		t.Fatalf("permission = %v", m["permission"])
	}
	wantContains(t, "stderr", h.stderr, "invalid hook payload")
}

func TestCursorStop(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	runHook(t, "cursor", "session-start", r.dir, cursorPayload("sessionStart", "conv-2", r.dir, nil)).wantShape(t)
	// Cursor's stop payload has no cwd: the workspace root is used.
	stop := func(status string, loops int) hookResult {
		return runHook(t, "cursor", "stop", t.TempDir(), cursorPayload("stop", "conv-2", r.dir, map[string]any{"status": status, "loop_count": loops}))
	}
	stop("completed", 0).wantShape(t)

	r.write("src/math.js", cheatedMath)
	m := stop("completed", 0).wantShape(t, "followup_message")
	wantContains(t, "followup_message", str(m, "followup_message"), "hooktest-cheat src/math.js:2")

	// The user aborted the turn: no follow-up.
	stop("aborted", 1).wantShape(t)

	// Cursor's loop_count already reached hook.max_blocks: give up, note on stderr.
	h := stop("completed", 3)
	h.wantShape(t)
	wantContains(t, "stderr", h.stderr, "unresolved verification issue")
}

// Cursor also runs Claude Code hooks it finds in .claude/settings.json;
// narc answers those in Cursor's protocol.
func TestClaudeHookCalledByCursor(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	h := runHook(t, "claude", "pre-tool-use", r.dir, cursorTool("conv-3", r.dir, "Write", map[string]any{"file_path": r.path("README.md"), "content": "# hi\n"}))
	if m := h.wantShape(t, "permission"); m["permission"] != "allow" {
		t.Fatalf("permission = %v", m["permission"])
	}
	h = runHook(t, "claude", "pre-tool-use", r.dir, cursorTool("conv-3", r.dir, "Write", map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath}))
	h.wantShape(t, "permission", "user_message", "agent_message")
	h = runHook(t, "claude", "pre-tool-use", r.dir, `{"cursor_version": "3.14.2", "tool_name": `)
	h.wantShape(t, "permission")
}

func TestCursorPostToolUse(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/math.js", cheatedMath)
	h := runHook(t, "cursor", "post-tool-use", r.dir, cursorPayload("postToolUse", "conv-4", r.dir, map[string]any{
		"tool_name": "Write", "tool_input": map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath},
		"tool_output": `{"ok":true}`, "tool_use_id": "t1", "cwd": r.dir, "duration": 12,
	}))
	wantContains(t, "additional_context", str(h.wantShape(t, "additional_context"), "additional_context"), "src/math.js:2")
}

// Cursor's loop_count may count follow-ups over the whole conversation: a
// new streak of blocks starts from the count at its beginning.
func TestCursorLoopCountAcrossStreaks(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	stop := func(loops int) hookResult {
		return runHook(t, "cursor", "stop", r.dir, cursorPayload("stop", "conv-5", r.dir, map[string]any{"status": "completed", "loop_count": loops}))
	}
	r.write("src/math.js", cheatedMath)
	stop(0).wantShape(t, "followup_message")
	r.write("src/math.js", baseFiles["src/math.js"]) // fixed
	stop(1).wantShape(t)

	r.write("src/extra.js", "__narc_cheat__()\n") // a new streak
	for loops := 1; loops <= 3; loops++ {
		wantContains(t, "followup", str(stop(loops).wantShape(t, "followup_message"), "followup_message"), "src/extra.js:1")
	}
	h := stop(4)
	h.wantShape(t)
	wantContains(t, "stderr", h.stderr, "after 3 blocked attempts")
}
