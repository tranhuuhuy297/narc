package hook

import "testing"

func TestGeminiBeforeTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		tool  string
		input func(r *testRepo) map[string]any
		deny  string
	}{
		{
			name: "write_file with a cheat",
			tool: "write_file",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath}
			},
			deny: "hooktest-cheat src/math.js:2",
		},
		{
			name: "replace with allow_multiple",
			tool: "replace",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("src/math.js"), "instruction": "log every call",
					"old_string": "(a, b) {\n", "new_string": "(a, b) {\n  __narc_cheat__()\n", "allow_multiple": true}
			},
			deny: "hooktest-cheat src/math.js:7",
		},
		{
			name: "replace with a relative path",
			tool: "replace",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": "src/math.js", "instruction": "x",
					"old_string": "  return a - b\n", "new_string": "  return __narc_cheat__()\n"}
			},
			deny: "hooktest-cheat src/math.js:6",
		},
		{
			name: "clean replace",
			tool: "replace",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"file_path": r.path("README.md"), "instruction": "document", "old_string": "# demo\n", "new_string": "# demo\n\nAdds numbers.\n"}
			},
		},
		{
			name: "shell tool",
			tool: "run_shell_command",
			input: func(r *testRepo) map[string]any {
				return map[string]any{"command": "npm test", "is_background": false}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, baseFiles)
			h := runHook(t, "gemini", "before-tool", r.dir, geminiPayload("BeforeTool", "g-1", r.dir, map[string]any{
				"tool_name": tc.tool, "tool_input": tc.input(r)}))
			if tc.deny == "" {
				// Gemini needs JSON on stdout; "{}" allows without
				// bypassing the user's confirmation.
				if h.stdout != "{}\n" || h.stderr != "" {
					t.Fatalf("want {}, got stdout %q stderr %q", h.stdout, h.stderr)
				}
				h.wantShape(t)
				return
			}
			m := h.wantShape(t, "decision", "reason", "systemMessage")
			if m["decision"] != "deny" {
				t.Fatalf("decision = %v", m["decision"])
			}
			wantContains(t, "reason", str(m, "reason"), tc.deny, "narc blocked this edit")
		})
	}
}

func TestGeminiAfterAgent(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	runHook(t, "gemini", "session-start", r.dir, geminiPayload("SessionStart", "g-2", r.dir, map[string]any{"source": "startup"})).wantShape(t)
	after := func(active bool) hookResult {
		return runHook(t, "gemini", "after-agent", r.dir, geminiPayload("AfterAgent", "g-2", r.dir, map[string]any{
			"prompt": "make the tests pass", "prompt_response": "Done, all green.", "stop_hook_active": active}))
	}
	after(false).wantShape(t)

	r.write("src/math.js", cheatedMath)
	m := after(false).wantShape(t, "decision", "reason")
	if m["decision"] != "deny" {
		t.Fatalf("decision = %v (Gemini retries the turn on deny)", m["decision"])
	}
	wantContains(t, "reason", str(m, "reason"), "narc: do not stop yet", "hooktest-cheat src/math.js:2")
	after(true).wantShape(t, "decision", "reason")
	after(true).wantShape(t, "decision", "reason")
	wantContains(t, "systemMessage", str(after(true).wantShape(t, "systemMessage"), "systemMessage"), "unresolved")
}

func TestGeminiAfterTool(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/math.js", cheatedMath)
	h := runHook(t, "gemini", "after-tool", r.dir, geminiPayload("AfterTool", "g-3", r.dir, map[string]any{
		"tool_name": "write_file", "tool_input": map[string]any{"file_path": r.path("src/math.js"), "content": cheatedMath},
		"tool_response": map[string]any{"llmContent": "Successfully overwrote file", "returnDisplay": "src/math.js"},
	}))
	m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.additionalContext")
	if str(m, "hookSpecificOutput.hookEventName") != "AfterTool" {
		t.Fatalf("hookEventName = %q", str(m, "hookSpecificOutput.hookEventName"))
	}
}
