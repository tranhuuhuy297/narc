package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/huyth45/narc/internal/core"
	// protected-path is used by several tests (also registered via rules/all).
	_ "github.com/huyth45/narc/internal/rules/integrity"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

// cliEnvVar makes the test binary act as the narc CLI (`narc hook <harness>
// <event>`), so installed hooks can be run end to end by real git.
const cliEnvVar = "NARC_HOOK_TEST_CLI"

func TestMain(m *testing.M) {
	if os.Getenv(cliEnvVar) == "1" {
		if len(os.Args) == 4 && os.Args[1] == "hook" {
			os.Exit(Run(os.Args[2], os.Args[3], os.Stdin, os.Stdout, os.Stderr, Env{Version: "test"}))
		}
		os.Exit(2)
	}
	// Keep git away from the developer's configuration (commit signing,
	// core.hooksPath, init templates, ...).
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "narc test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "narc test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		os.Setenv(k, v)
	}
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", LogEnvVar} {
		os.Unsetenv(k)
	}
	os.Exit(m.Run())
}

// cheatRule is a deterministic stand-in for narc's real rules, so these
// tests do not depend on how other packages' rules evolve. It flags calls
// to __narc_cheat__() added in code; text in comments and strings does not
// count, and moved lines cancel out (rulekit.Introduced).
var cheatRule = &core.Rule{
	ID:       "hooktest-cheat",
	Title:    "Cheat marker added (test rule)",
	Category: "tests",
	Severity: core.Error,
	Summary:  "Test-only rule.",
	Hint:     "Remove the cheat marker and fix the code so the original tests pass.",
}

var cheatRe = regexp.MustCompile(`__narc_cheat__\(`)

// droppedRule is a cross-file test rule, like test-deleted: a removed
// __narc_check__(...) line is a finding unless some file in the changeset
// adds the same line back (the check was moved, not deleted).
var droppedRule = &core.Rule{
	ID:       "hooktest-dropped",
	Title:    "Check removed (test rule)",
	Category: "tests",
	Severity: core.Error,
	Summary:  "Test-only rule.",
	Hint:     "Keep the check; fix the code it verifies.",
}

var checkRe = regexp.MustCompile(`__narc_check__\(`)

func init() {
	cheatRule.Check = func(a *core.Analysis) []core.Finding {
		var out []core.Finding
		for _, m := range rulekit.Introduced(a.Files, cheatRe, rulekit.Code) {
			out = append(out, cheatRule.Finding(m.File, m.Line.No, "cheat marker __narc_cheat__() added"))
		}
		return out
	}
	droppedRule.Check = func(a *core.Analysis) []core.Finding {
		added := map[string]bool{}
		for _, f := range a.Files {
			for _, l := range f.Added {
				added[strings.TrimSpace(l.Code)] = true
			}
		}
		var out []core.Finding
		for _, f := range a.Files {
			for _, l := range f.Removed {
				if checkRe.MatchString(l.Code) && !added[strings.TrimSpace(l.Code)] {
					out = append(out, droppedRule.Removal(f, l.No, "check "+strings.TrimSpace(l.Raw)+" removed and not re-added anywhere"))
				}
			}
		}
		return out
	}
	core.Register(cheatRule, droppedRule)
}

// baseFiles is a small JavaScript project.
var baseFiles = map[string]string{
	"package.json": "{\n  \"name\": \"demo\",\n  \"scripts\": { \"test\": \"vitest run\" }\n}\n",
	"src/math.js":  "export function add(a, b) {\n  return a + b\n}\n\nexport function sub(a, b) {\n  return a - b\n}\n",
	"src/math.test.js": "import { add, sub } from './math'\n\n" +
		"test('adds', () => {\n  expect(add(1, 2)).toBe(3)\n})\n\n" +
		"test('subtracts', () => {\n  expect(sub(3, 1)).toBe(2)\n})\n",
	"README.md": "# demo\n",
}

// cheatedMath is src/math.js with a cheat on line 2.
const cheatedMath = "export function add(a, b) {\n  __narc_cheat__()\n  return a + b\n}\n\nexport function sub(a, b) {\n  return a - b\n}\n"

type testRepo struct {
	t   *testing.T
	dir string
}

// newRepo creates a git repository with files committed on main.
func newRepo(t *testing.T, files map[string]string) *testRepo {
	t.Helper()
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	if len(files) > 0 {
		for p, c := range files {
			r.write(p, c)
		}
		r.commit("initial")
	}
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *testRepo) commit(msg string) {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--no-verify", "-m", msg)
}

func (r *testRepo) path(p string) string { return filepath.Join(r.dir, filepath.FromSlash(p)) }

func (r *testRepo) write(p, content string) {
	r.t.Helper()
	full := r.path(p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) read(p string) string {
	r.t.Helper()
	b, err := os.ReadFile(r.path(p))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func (r *testRepo) remove(p string) {
	r.t.Helper()
	if err := os.Remove(r.path(p)); err != nil {
		r.t.Fatal(err)
	}
}

// session loads a saved session state.
func (r *testRepo) session(id string) *sessionState {
	r.t.Helper()
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	b, err := os.ReadFile(filepath.Join(gitDir, "narc", "sessions", sessionFileName(id)))
	if err != nil {
		return nil
	}
	var s sessionState
	if err := json.Unmarshal(b, &s); err != nil {
		r.t.Fatal(err)
	}
	return &s
}

// narcCLI writes an executable named narc that runs this test binary as
// the narc CLI, and returns its absolute path.
func narcCLI(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "narc")
	script := "#!/bin/sh\n" + cliEnvVar + "=1 exec " + shellQuote(exe) + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var testNow = time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

// testEnv is a hermetic environment rooted at dir.
func testEnv(dir string) Env {
	return Env{
		Getwd:   func() (string, error) { return dir, nil },
		Now:     func() time.Time { return testNow },
		Getenv:  func(string) string { return "" },
		Version: "test",
	}
}

type hookResult struct {
	code   int
	stdout string
	stderr string
}

// runHook runs one hook with a payload (a string is sent verbatim, other
// values as JSON).
func runHook(t *testing.T, harness, event, dir string, payload any) hookResult {
	t.Helper()
	return runHookEnv(t, harness, event, testEnv(dir), payload)
}

func runHookEnv(t *testing.T, harness, event string, env Env, payload any) hookResult {
	t.Helper()
	var in []byte
	switch p := payload.(type) {
	case string:
		in = []byte(p)
	case nil:
	default:
		var err error
		if in, err = json.Marshal(p); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := Run(harness, event, bytes.NewReader(in), &stdout, &stderr, env)
	return hookResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// object decodes stdout as exactly one JSON object.
func (h hookResult) object(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(h.stdout))
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\nstdout: %q\nstderr: %s", err, h.stdout, h.stderr)
	}
	if dec.More() {
		t.Fatalf("stdout has more than one JSON value: %q", h.stdout)
	}
	return m
}

// wantShape checks exit code 0 and that stdout is a JSON object with
// exactly the given keys (nested objects as "parent.child").
func (h hookResult) wantShape(t *testing.T, keys ...string) map[string]any {
	t.Helper()
	if h.code != 0 {
		t.Fatalf("exit code %d, want 0\nstderr: %s", h.code, h.stderr)
	}
	m := h.object(t)
	got := flatKeys("", m)
	sort.Strings(keys)
	if !reflect.DeepEqual(got, keys) && !(len(got) == 0 && len(keys) == 0) {
		t.Fatalf("output keys = %v, want %v\nstdout: %s", got, keys, h.stdout)
	}
	return m
}

// wantSilent checks for an allow with no output at all.
func (h hookResult) wantSilent(t *testing.T) {
	t.Helper()
	if h.code != 0 || h.stdout != "" || h.stderr != "" {
		t.Fatalf("want exit 0 with no output; got exit %d\nstdout: %q\nstderr: %q", h.code, h.stdout, h.stderr)
	}
}

func flatKeys(prefix string, m map[string]any) []string {
	var out []string
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out = append(out, flatKeys(prefix+k+".", sub)...)
			continue
		}
		out = append(out, prefix+k)
	}
	sort.Strings(out)
	return out
}

// str returns a nested string value by dotted path.
func str(m map[string]any, path string) string {
	parts := strings.Split(path, ".")
	var v any = m
	for _, p := range parts {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[p]
	}
	s, _ := v.(string)
	return s
}

func wantContains(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("%s does not contain %q:\n%s", what, sub, s)
		}
	}
}

func wantNotContains(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Errorf("%s unexpectedly contains %q:\n%s", what, sub, s)
		}
	}
}

// Realistic payloads for each harness.

func claudePayload(event, session, cwd string, extra map[string]any) map[string]any {
	p := map[string]any{
		"session_id":      session,
		"transcript_path": "/Users/dev/.claude/projects/-demo/" + session + ".jsonl",
		"cwd":             cwd,
		"permission_mode": "default",
		"hook_event_name": event,
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func claudeTool(session, cwd, tool string, input map[string]any) map[string]any {
	return claudePayload("PreToolUse", session, cwd, map[string]any{
		"tool_name": tool, "tool_input": input, "tool_use_id": "toolu_01HxQq3",
	})
}

func claudeStop(session, cwd string, active bool) map[string]any {
	return claudePayload("Stop", session, cwd, map[string]any{
		"stop_hook_active":       active,
		"last_assistant_message": "All tests pass now.",
		"background_tasks":       []any{},
		"session_crons":          []any{},
	})
}

func codexPayload(event, session, cwd string, extra map[string]any) map[string]any {
	p := map[string]any{
		"session_id":      session,
		"turn_id":         "turn-7",
		"transcript_path": nil,
		"cwd":             cwd,
		"hook_event_name": event,
		"model":           "gpt-5.5-codex",
		"permission_mode": "default",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func codexPatch(session, cwd, patch string) map[string]any {
	return codexPayload("PreToolUse", session, cwd, map[string]any{
		"tool_name": "apply_patch", "tool_use_id": "call_9f2", "tool_input": map[string]any{"command": patch},
	})
}

func geminiPayload(event, session, cwd string, extra map[string]any) map[string]any {
	p := map[string]any{
		"session_id":      session,
		"transcript_path": "/Users/dev/.gemini/tmp/chats/" + session + ".json",
		"cwd":             cwd,
		"hook_event_name": event,
		"timestamp":       "2026-10-17T10:00:00.000Z",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func cursorPayload(event, conversation, root string, extra map[string]any) map[string]any {
	p := map[string]any{
		"conversation_id": conversation,
		"generation_id":   "gen-41",
		"model":           "claude-4.5-sonnet",
		"hook_event_name": event,
		"cursor_version":  "3.14.2",
		"workspace_roots": []any{root},
		"user_email":      nil,
		"transcript_path": nil,
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func cursorTool(conversation, root, tool string, input map[string]any) map[string]any {
	return cursorPayload("preToolUse", conversation, root, map[string]any{
		"tool_name": tool, "tool_input": input, "tool_use_id": "tool_3", "cwd": root,
	})
}
