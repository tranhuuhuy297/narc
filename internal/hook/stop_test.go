package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeSessionLifecycle(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	// Before the session: the human has uncommitted work that narc flags.
	r.write("src/legacy.js", "__narc_cheat__()\n")

	start := claudePayload("SessionStart", "sess-42", r.dir, map[string]any{"source": "startup"})
	runHook(t, "claude", "session-start", r.dir, start).wantSilent(t)
	s := r.session("sess-42")
	if s == nil {
		t.Fatal("session-start saved no state")
	}
	head := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	if s.Baseline != head || len(s.Preexisting) != 1 || s.Harness != "claude" || s.NarcVersion != "test" {
		t.Fatalf("session state = %+v, want baseline %s and 1 pre-existing finding", s, head)
	}

	// The pre-existing finding is not the agent's doing.
	runHook(t, "claude", "stop", r.dir, claudeStop("sess-42", r.dir, false)).wantSilent(t)

	// The agent cheats.
	r.write("src/math.js", cheatedMath)
	stop := func(active bool) map[string]any {
		t.Helper()
		h := runHook(t, "claude", "stop", r.dir, claudeStop("sess-42", r.dir, active))
		return h.wantShape(t, "decision", "reason")
	}
	m := stop(false)
	if m["decision"] != "block" {
		t.Fatalf("decision = %v", m["decision"])
	}
	reason := str(m, "reason")
	wantContains(t, "stop reason", reason, "narc: do not stop yet", "✗ hooktest-cheat src/math.js:2 — cheat marker", "reported to the user")
	wantNotContains(t, "stop reason", reason, "legacy.js", "again")

	// Stopping again without changes is called out, and counts.
	wantContains(t, "second stop", str(stop(true), "reason"), "You tried to stop again without resolving any of these.")
	stop(true)
	if got := r.session("sess-42").Stops["main"].Blocks; got != 3 {
		t.Fatalf("blocks = %d, want 3", got)
	}

	// hook.max_blocks (3) reached: let the agent stop, tell the human.
	h := runHook(t, "claude", "stop", r.dir, claudeStop("sess-42", r.dir, true))
	notice := str(h.wantShape(t, "systemMessage"), "systemMessage")
	wantContains(t, "systemMessage", notice, "narc: the agent stopped with 1 unresolved verification issue after 3 blocked attempts",
		"✗ hooktest-cheat src/math.js:2", "narc scan")

	// The human has been told; the same finding does not block again.
	runHook(t, "claude", "stop", r.dir, claudeStop("sess-42", r.dir, false)).wantSilent(t)

	// A new cheat later in the session is blocked again, even once committed.
	r.write("src/extra.js", "export const x = __narc_cheat__()\n")
	r.commit("wip")
	m = stop(false)
	wantContains(t, "stop reason", str(m, "reason"), "hooktest-cheat src/extra.js:1")
	wantNotContains(t, "stop reason", str(m, "reason"), "src/math.js")

	// Fixed: the stop is allowed and the counter resets.
	r.write("src/extra.js", "export const x = 1\n")
	runHook(t, "claude", "stop", r.dir, claudeStop("sess-42", r.dir, true)).wantSilent(t)
	if got := r.session("sess-42").Stops["main"].Blocks; got != 0 {
		t.Fatalf("blocks after a clean stop = %d, want 0", got)
	}

	// Resuming keeps the original baseline and pre-existing findings.
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "sess-42", r.dir, map[string]any{"source": "resume"})).wantSilent(t)
	if got := r.session("sess-42"); got.Baseline != head || len(got.Preexisting) != 1 {
		t.Fatalf("resume changed the session: %+v", got)
	}
}

func TestStopMaxBlocksFromConfig(t *testing.T) {
	t.Parallel()
	files := map[string]string{".narc.yml": "hook:\n  max_blocks: 1\n"}
	for k, v := range baseFiles {
		files[k] = v
	}
	r := newRepo(t, files)
	r.write("src/math.js", cheatedMath)
	runHook(t, "codex", "stop", r.dir, codexPayload("Stop", "s", r.dir, map[string]any{"stop_hook_active": false, "last_assistant_message": nil})).
		wantShape(t, "decision", "reason")
	runHook(t, "codex", "stop", r.dir, codexPayload("Stop", "s", r.dir, map[string]any{"stop_hook_active": true, "last_assistant_message": nil})).
		wantShape(t, "systemMessage")
}

// Without a session-start hook, the first edit of a session records the
// baseline, so work that existed before the agent's first edit is not
// blamed on it.
func TestFirstEditRecordsSession(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/legacy.js", "__narc_cheat__()\n")
	runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s9", r.dir, "Edit", map[string]any{
		"file_path": r.path("README.md"), "old_string": "# demo\n", "new_string": "# demo\n\nA demo.\n"})).wantSilent(t)
	if s := r.session("s9"); s == nil || len(s.Preexisting) != 1 {
		t.Fatalf("first edit did not record the session: %+v", s)
	}
	runHook(t, "claude", "stop", r.dir, claudeStop("s9", r.dir, false)).wantSilent(t)
}

// Without any session state, stop falls back to HEAD.
func TestStopWithoutSessionUsesHead(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/math.js", cheatedMath)
	r.commit("cheat committed before the session")
	runHook(t, "claude", "stop", r.dir, claudeStop("unknown", r.dir, false)).wantSilent(t)

	r.write("src/extra.js", "__narc_cheat__()\n")
	h := runHook(t, "claude", "stop", r.dir, claudeStop("unknown", r.dir, false))
	wantContains(t, "reason", str(h.wantShape(t, "decision", "reason"), "reason"), "src/extra.js:1")
}

func TestStopInEmptyRepository(t *testing.T) {
	t.Parallel()
	r := newRepo(t, nil)
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
	if s := r.session("s"); s == nil || s.Baseline == "" {
		t.Fatalf("no baseline recorded in an empty repository: %+v", s)
	}
	r.write("src/app.js", "export const answer = __narc_cheat__()\n")
	r.commit("first commit")
	h := runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false))
	wantContains(t, "reason", str(h.wantShape(t, "decision", "reason"), "reason"), "src/app.js:1")
}

func TestClaudeSubagentStop(t *testing.T) {
	t.Parallel()
	files := map[string]string{".narc.yml": "hook:\n  max_blocks: 2\n"}
	for k, v := range baseFiles {
		files[k] = v
	}
	r := newRepo(t, files)
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
	r.write("src/math.js", cheatedMath)
	sub := func(active bool) hookResult {
		return runHook(t, "claude", "subagent-stop", r.dir, claudePayload("SubagentStop", "s", r.dir, map[string]any{
			"stop_hook_active": active, "agent_id": "agent-7", "agent_type": "general-purpose",
			"agent_transcript_path": "/tmp/agent-7.jsonl", "last_assistant_message": "done",
		}))
	}
	wantContains(t, "reason", str(sub(false).wantShape(t, "decision", "reason"), "reason"), "src/math.js:2")
	sub(true).wantShape(t, "decision", "reason")
	// The subagent's cap is reached: it may stop, silently; the main agent's
	// stop still blocks with its own counter.
	sub(true).wantSilent(t)
	runHook(t, "claude", "stop", r.dir, claudeStop("s", r.dir, false)).wantShape(t, "decision", "reason")
}

func TestClaudePostToolUse(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.write("src/legacy.js", "__narc_cheat__()\n")
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)

	post := func(file string) hookResult {
		return runHook(t, "claude", "post-tool-use", r.dir, claudePayload("PostToolUse", "s", r.dir, map[string]any{
			"tool_name": "Write", "tool_input": map[string]any{"file_path": r.path(file), "content": r.read(file)},
			"tool_response": map[string]any{"filePath": r.path(file), "type": "update"}, "tool_use_id": "toolu_1",
		}))
	}
	// A pre-existing finding in the touched file is not fed back.
	r.write("src/legacy.js", "__narc_cheat__()\nexport const y = 2\n")
	post("src/legacy.js").wantSilent(t)

	r.write("src/math.js", cheatedMath)
	m := post("src/math.js").wantShape(t, "decision", "reason")
	wantContains(t, "reason", str(m, "reason"), "narc: the edit you just made", "hooktest-cheat src/math.js:2", "Undo or rework it")
	wantNotContains(t, "reason", str(m, "reason"), "legacy.js")
}

func TestSessionFileNames(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		"":                                     "default.json",
		"9f2c1e4a-0b7d-4c55-8a1e-3f6d2b9c0e11": "9f2c1e4a-0b7d-4c55-8a1e-3f6d2b9c0e11.json",
		"../../etc/passwd":                     "_.._etc_passwd-",
		"ses_01JB:x":                           "ses_01JB_x-",
	} {
		got := sessionFileName(id)
		if !strings.HasPrefix(got, strings.TrimSuffix(want, ".json")) || !strings.HasSuffix(got, ".json") || strings.ContainsAny(got, "/:") {
			t.Errorf("sessionFileName(%q) = %q, want prefix %q", id, got, want)
		}
	}
	if sessionFileName("a/b") == sessionFileName("a_b") {
		t.Error("different session IDs map to the same file")
	}
}

func TestPruneSessions(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "old", r.dir, nil)).wantSilent(t)
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	old := filepath.Join(gitDir, "narc", "sessions", "old.json")
	stale := testNow.Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "new", r.dir, nil)).wantSilent(t)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("a 40-day-old session file was not pruned")
	}
	if r.session("new") == nil {
		t.Fatal("the new session was not saved")
	}
}
