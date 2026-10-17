package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cloneRepo clones upstream, as a developer's checkout of a shared repository.
func cloneRepo(t *testing.T, upstream *testRepo) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("clone", "-q", upstream.dir, r.dir)
	return r
}

// stopReason runs a Claude stop and returns the block reason ("" when the
// stop is allowed silently).
func stopReason(t *testing.T, r *testRepo, session string) string {
	t.Helper()
	h := runHook(t, "claude", "stop", r.dir, claudeStop(session, r.dir, false))
	if h.stdout == "" {
		h.wantSilent(t)
		return ""
	}
	return str(h.wantShape(t, "decision", "reason"), "reason")
}

// Work that git brings into the session from elsewhere (a pull, merge,
// rebase, cherry-pick or checkout) is not the agent's: a stop must not
// blame the agent for a teammate's skipped test.
func TestStopIgnoresImportedCommits(t *testing.T) {
	t.Parallel()
	const teammateCheat = "export const flaky = __narc_cheat__()\n"
	const agentCheat = "export const answer = __narc_cheat__()\n"

	t.Run("fast-forward pull", func(t *testing.T) {
		t.Parallel()
		upstream := newRepo(t, baseFiles)
		r := cloneRepo(t, upstream)
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.write("src/mul.js", "export const mul = (a, b) => a * b\n") // the agent's clean work
		upstream.write("src/flaky.js", teammateCheat)
		upstream.commit("teammate: stub out the flaky check")
		r.git("pull", "-q", "--ff-only")
		if reason := stopReason(t, r, "s"); reason != "" {
			t.Fatalf("the agent was blamed for a pulled commit:\n%s", reason)
		}
		// The agent's own cheat afterwards still counts, alone.
		r.write("src/answer.js", agentCheat)
		reason := stopReason(t, r, "s")
		wantContains(t, "reason", reason, "hooktest-cheat src/answer.js:1")
		wantNotContains(t, "reason", reason, "src/flaky.js")
	})

	t.Run("the agent's commits still count after a merge pull", func(t *testing.T) {
		t.Parallel()
		upstream := newRepo(t, baseFiles)
		r := cloneRepo(t, upstream)
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.write("src/answer.js", agentCheat)
		r.commit("agent: make the test pass")
		upstream.write("src/flaky.js", teammateCheat)
		upstream.commit("teammate: stub out the flaky check")
		r.git("pull", "-q", "--no-rebase", "--no-edit")
		reason := stopReason(t, r, "s")
		wantContains(t, "reason", reason, "hooktest-cheat src/answer.js:1")
		wantNotContains(t, "reason", reason, "src/flaky.js")
	})

	t.Run("rebase onto upstream", func(t *testing.T) {
		t.Parallel()
		upstream := newRepo(t, baseFiles)
		r := cloneRepo(t, upstream)
		// Committed on the branch before the session: not the agent's.
		r.write("src/legacy.js", "export const legacy = __narc_cheat__()\n")
		r.commit("legacy shim")
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.write("src/answer.js", agentCheat)
		r.commit("agent: make the test pass")
		upstream.write("src/flaky.js", teammateCheat)
		upstream.commit("teammate: stub out the flaky check")
		r.git("pull", "-q", "--rebase")
		reason := stopReason(t, r, "s")
		wantContains(t, "reason", reason, "hooktest-cheat src/answer.js:1")
		wantNotContains(t, "reason", reason, "src/flaky.js", "src/legacy.js")
	})

	t.Run("editing a file a pull just changed", func(t *testing.T) {
		t.Parallel()
		upstream := newRepo(t, baseFiles)
		r := cloneRepo(t, upstream)
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		upstream.write("src/flaky.js", teammateCheat)
		upstream.commit("teammate: stub out the flaky check")
		r.git("pull", "-q", "--ff-only")
		edit := func(old, new string) hookResult {
			return runHook(t, "claude", "pre-tool-use", r.dir, claudeTool("s", r.dir, "Edit", map[string]any{
				"file_path": r.path("src/flaky.js"), "old_string": old, "new_string": new}))
		}
		edit("export const flaky", "// Retries are handled by the caller.\nexport const flaky").wantSilent(t)
		m := edit("export const flaky = __narc_cheat__()\n", teammateCheat+"export const steady = __narc_cheat__()\n").wantShape(t,
			"hookSpecificOutput.hookEventName", "hookSpecificOutput.permissionDecision", "hookSpecificOutput.permissionDecisionReason", "systemMessage")
		reason := str(m, "hookSpecificOutput.permissionDecisionReason")
		wantContains(t, "deny reason", reason, "hooktest-cheat src/flaky.js:2")
		wantNotContains(t, "deny reason", reason, "src/flaky.js:1")
	})

	t.Run("cherry-pick", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		r.git("checkout", "-q", "-b", "teammate")
		r.write("src/flaky.js", teammateCheat)
		r.commit("teammate: stub out the flaky check")
		r.git("checkout", "-q", "main")
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.git("cherry-pick", "teammate")
		if reason := stopReason(t, r, "s"); reason != "" {
			t.Fatalf("the agent was blamed for a cherry-picked commit:\n%s", reason)
		}
	})

	t.Run("state from before narc tracked HEAD", func(t *testing.T) {
		t.Parallel()
		upstream := newRepo(t, baseFiles)
		r := cloneRepo(t, upstream)
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		// Drop the head field, as an older narc wrote the file.
		p := filepath.Join(strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir")), "narc", "sessions", sessionFileName("s"))
		var raw map[string]any
		if err := json.Unmarshal([]byte(readFile(t, p)), &raw); err != nil {
			t.Fatal(err)
		}
		delete(raw, "head")
		b, _ := json.Marshal(raw)
		writeTestFile(t, p, string(b))
		upstream.write("src/flaky.js", teammateCheat)
		upstream.commit("teammate: stub out the flaky check")
		r.git("pull", "-q", "--ff-only")
		if reason := stopReason(t, r, "s"); reason != "" {
			t.Fatalf("the agent was blamed for a pulled commit:\n%s", reason)
		}
	})
}

// Moving HEAD back and forth must not hide what the agent does later, and
// undoing commits is the agent's own doing.
func TestStopHeadMovesThatStayTheAgents(t *testing.T) {
	t.Parallel()
	files := map[string]string{"tests/test_parse.py": "def test_parse():\n    __narc_check__(parse('1') == 1)\n"}
	for k, v := range baseFiles {
		files[k] = v
	}

	t.Run("checking out an old commit and coming back", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		r.write("tests/test_parse.py", files["tests/test_parse.py"])
		r.commit("add the parse test")
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		// Looking at the commit before the test existed is not deleting it.
		r.git("checkout", "-q", "HEAD~1")
		if reason := stopReason(t, r, "s"); reason != "" {
			t.Fatalf("checking out an older commit was blamed on the agent:\n%s", reason)
		}
		r.git("checkout", "-q", "main")
		r.write("tests/test_parse.py", "def test_parse():\n    pass\n")
		wantContains(t, "reason", stopReason(t, r, "s"), "hooktest-dropped tests/test_parse.py:2")
	})

	t.Run("a reset that drops commits", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		r.write("tests/test_parse.py", files["tests/test_parse.py"])
		r.commit("add the parse test")
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.git("reset", "-q", "--hard", "HEAD~1")
		wantContains(t, "reason", stopReason(t, r, "s"), "hooktest-dropped tests/test_parse.py")
	})

	t.Run("amend and revert", func(t *testing.T) {
		t.Parallel()
		r := newRepo(t, baseFiles)
		r.write("tests/test_parse.py", files["tests/test_parse.py"])
		r.commit("add the parse test")
		runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
		r.write("src/answer.js", "export const answer = 42\n")
		r.commit("agent: answer")
		r.write("src/answer.js", "export const answer = __narc_cheat__()\n")
		r.git("commit", "-q", "--amend", "-a", "--no-edit", "--no-verify")
		wantContains(t, "reason", stopReason(t, r, "s"), "hooktest-cheat src/answer.js:1")
		r.git("revert", "--no-edit", "HEAD")
		r.git("revert", "--no-edit", "HEAD~2") // the parse test, committed before the session
		reason := stopReason(t, r, "s")
		wantContains(t, "reason", reason, "hooktest-dropped tests/test_parse.py — check")
		wantNotContains(t, "reason", reason, "src/answer.js")
	})
}

func TestReflogAction(t *testing.T) {
	t.Parallel()
	for subject, want := range map[string][2]string{
		"commit: add tests":                                  {"commit", ""},
		"commit (amend): add tests: part 2":                  {"commit", "amend"},
		"commit (initial): init":                             {"commit", "initial"},
		"commit (merge): Merge branch 'main' into feat":      {"commit", "merge"},
		"merge origin/main: Fast-forward":                    {"merge", ""},
		"pull -q --ff-only: Fast-forward":                    {"pull", ""},
		"pull --rebase origin main (pick): fix (the) parser": {"pull", "pick"},
		"rebase -i (squash): fixup! parser":                  {"rebase", "squash"},
		"rebase (start): checkout main":                      {"rebase", "start"},
		"checkout: moving from main to feat":                 {"checkout", ""},
		"reset: moving to HEAD~1":                            {"reset", ""},
		"cherry-pick: fix":                                   {"cherry-pick", ""},
		"":                                                   {"", ""},
	} {
		command, action := reflogAction(subject)
		if command != want[0] || action != want[1] {
			t.Errorf("reflogAction(%q) = %q, %q; want %q, %q", subject, command, action, want[0], want[1])
		}
	}
}

// Concluding a merge: only what the merge itself introduces counts, not
// everything the merged branch brings in.
func TestGitPreCommitConcludingMerge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, resolution string
		block            bool
	}{
		{name: "a plain resolution", resolution: "  return a - b // main and feature\n"},
		{name: "a resolution that cheats", resolution: "  __narc_cheat__()\n  return a - b\n", block: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRepo(t, baseFiles)
			r.git("checkout", "-q", "-b", "feature")
			r.write("src/feature.js", "export const feature = __narc_cheat__()\n")
			r.write("src/math.js", strings.Replace(baseFiles["src/math.js"], "  return a - b\n", "  return a - b // feature\n", 1))
			r.commit("feature work, reviewed in its own pull request")
			r.git("checkout", "-q", "main")
			r.write("src/math.js", strings.Replace(baseFiles["src/math.js"], "  return a - b\n", "  return a - b // main\n", 1))
			r.commit("main work")
			cmd := exec.Command("git", "merge", "-q", "feature")
			cmd.Dir = r.dir
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("expected a merge conflict:\n%s", out)
			}
			r.write("src/math.js", strings.Replace(baseFiles["src/math.js"], "  return a - b\n", tc.resolution, 1))
			r.git("add", "-A")

			h := runHook(t, "git", "pre-commit", r.dir, nil)
			wantNotContains(t, "pre-commit report", h.stderr, "src/feature.js")
			if !tc.block {
				if h.code != 0 || h.stderr != "" {
					t.Fatalf("exit %d, stderr:\n%s", h.code, h.stderr)
				}
				return
			}
			if h.code != 1 {
				t.Fatalf("exit %d, want 1; stderr:\n%s", h.code, h.stderr)
			}
			wantContains(t, "pre-commit report", h.stderr, "hooktest-cheat src/math.js:6", "commit blocked")
		})
	}
}

// Without a reflog, narc cannot tell the agent's commits from pulled ones
// and assumes they were pulled: a miss, never a false accusation.
func TestStopWithoutReflog(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	r.git("config", "core.logAllRefUpdates", "false")
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	if err := os.RemoveAll(filepath.Join(gitDir, "logs")); err != nil {
		t.Fatal(err)
	}
	runHook(t, "claude", "session-start", r.dir, claudePayload("SessionStart", "s", r.dir, nil)).wantSilent(t)
	r.write("src/answer.js", "export const answer = __narc_cheat__()\n")
	r.commit("agent: make the test pass")
	if reason := stopReason(t, r, "s"); reason != "" {
		t.Fatalf("unexpected block:\n%s", reason)
	}
	// Uncommitted work is still judged.
	r.write("src/more.js", "__narc_cheat__()\n")
	wantContains(t, "reason", stopReason(t, r, "s"), "hooktest-cheat src/more.js:1")
}
