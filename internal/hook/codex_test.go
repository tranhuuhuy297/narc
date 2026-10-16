package hook

import "testing"

func TestCodexPreToolUse(t *testing.T) {
	t.Parallel()
	cfgFiles := map[string]string{
		".narc.yml":        "protected:\n  - \"tests/golden/**\"\n",
		"tests/golden/out": "42\n",
		"src/app.py":       "class App:\n    debug = True\n\n    def run(self):\n        return 0\n",
	}
	for k, v := range baseFiles {
		cfgFiles[k] = v
	}
	tests := []struct {
		name  string
		patch string
		deny  []string
	}{
		{
			name: "update adds a cheat",
			patch: "*** Begin Patch\n*** Update File: src/math.js\n@@ export function add(a, b) {\n" +
				"-  return a + b\n+  __narc_cheat__()\n+  return a + b\n*** End Patch\n",
			deny: []string{"hooktest-cheat src/math.js:2"},
		},
		{
			name:  "added file with a cheat",
			patch: "*** Begin Patch\n*** Add File: src/mul.js\n+export function mul(a, b) {\n+  return __narc_cheat__()\n+}\n*** End Patch\n",
			deny:  []string{"hooktest-cheat src/mul.js:2"},
		},
		{
			name: "moved and edited file is reported at its new path",
			patch: "*** Begin Patch\n*** Update File: src/app.py\n*** Move to: src/main.py\n@@ def run(self):\n" +
				"-        return 0\n+        __narc_cheat__()\n+        return 0\n*** End Patch\n",
			deny: []string{"hooktest-cheat src/main.py:5"},
		},
		{
			name:  "deleting a protected file",
			patch: "*** Begin Patch\n*** Delete File: tests/golden/out\n*** End Patch\n",
			deny:  []string{"protected-path tests/golden/out — protected file was deleted"},
		},
		{
			name: "a clean multi-file patch",
			patch: "*** Begin Patch\n*** Update File: src/app.py\n@@\n-    debug = True\n+    debug = False\n" +
				"*** Add File: docs/usage.md\n+# Usage\n*** End Patch\n",
		},
		{
			name:  "a patch that does not apply is left to fail",
			patch: "*** Begin Patch\n*** Update File: src/math.js\n@@\n-  return a * b\n+  __narc_cheat__()\n*** End Patch\n",
		},
		{
			name:  "updating a missing file is left to fail",
			patch: "*** Begin Patch\n*** Update File: src/missing.js\n@@\n-x\n+__narc_cheat__()\n*** End Patch\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, cfgFiles)
			h := runHook(t, "codex", "pre-tool-use", r.dir, codexPatch("019a-codex", r.dir, tc.patch))
			if tc.deny == nil {
				h.wantSilent(t)
				return
			}
			// Codex treats unsupported fields as a failed hook (and runs the
			// tool), so the deny carries exactly these keys.
			m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason")
			if str(m, "hookSpecificOutput.permissionDecision") != "deny" || str(m, "hookSpecificOutput.hookEventName") != "PreToolUse" {
				t.Fatalf("not a deny: %s", h.stdout)
			}
			wantContains(t, "deny reason", str(m, "hookSpecificOutput.permissionDecisionReason"), tc.deny...)
		})
	}
}

func TestCodexPatchThroughShell(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	patch := "*** Begin Patch\n*** Add File: src/x.js\n+__narc_cheat__()\n*** End Patch\n"
	h := runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
		"tool_name": "Bash", "tool_use_id": "call_2", "tool_input": map[string]any{"command": "apply_patch <<'EOF'\n" + patch + "EOF\n"},
	}))
	h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason")

	h = runHook(t, "codex", "pre-tool-use", r.dir, codexPayload("PreToolUse", "s", r.dir, map[string]any{
		"tool_name": "Bash", "tool_use_id": "call_3", "tool_input": map[string]any{"command": "npm test"},
	}))
	h.wantSilent(t)
}

func TestCodexStopAndSession(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/legacy.js", "__narc_cheat__()\n")
	runHook(t, "codex", "session-start", r.dir, codexPayload("SessionStart", "019a", r.dir, map[string]any{"source": "startup"})).wantSilent(t)

	stop := func(active bool) hookResult {
		return runHook(t, "codex", "stop", r.dir, codexPayload("Stop", "019a", r.dir, map[string]any{
			"stop_hook_active": active, "last_assistant_message": "Implemented the feature.",
		}))
	}
	stop(false).wantSilent(t)

	r.write("src/math.js", cheatedMath)
	// Codex has no built-in cap on stop continuations: narc caps them.
	for i := 0; i < 3; i++ {
		m := stop(i > 0).wantShape(t, "decision", "reason")
		if m["decision"] != "block" {
			t.Fatalf("stop %d: decision = %v", i+1, m["decision"])
		}
		wantContains(t, "reason", str(m, "reason"), "hooktest-cheat src/math.js:2")
	}
	wantContains(t, "systemMessage", str(stop(true).wantShape(t, "systemMessage"), "systemMessage"), "after 3 blocked attempts")
	stop(true).wantSilent(t)
}

func TestCodexPostToolUse(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	patch := "*** Begin Patch\n*** Update File: src/math.js\n@@\n-  return a + b\n+  __narc_cheat__()\n+  return a + b\n*** End Patch\n"
	r.write("src/math.js", cheatedMath) // the patch already ran
	h := runHook(t, "codex", "post-tool-use", r.dir, codexPayload("PostToolUse", "s", r.dir, map[string]any{
		"tool_name": "apply_patch", "tool_use_id": "call_1", "tool_input": map[string]any{"command": patch},
		"tool_response": "Success. Updated the following files:\nM src/math.js\n",
	}))
	m := h.wantShape(t, "hookSpecificOutput.hookEventName", "hookSpecificOutput.additionalContext")
	if str(m, "hookSpecificOutput.hookEventName") != "PostToolUse" {
		t.Fatalf("hookEventName = %q", str(m, "hookSpecificOutput.hookEventName"))
	}
	wantContains(t, "additionalContext", str(m, "hookSpecificOutput.additionalContext"), "hooktest-cheat src/math.js:2")
}
