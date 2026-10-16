package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeHooks(t *testing.T) {
	t.Parallel()
	r := newRepo(t, baseFiles)
	pre := func(tool string, args map[string]any) hookResult {
		return runHook(t, "opencode", "pre-tool-use", r.dir, map[string]any{"session_id": "ses_01", "tool": tool, "args": args, "cwd": r.dir})
	}
	runHook(t, "opencode", "session-start", r.dir, map[string]any{"session_id": "ses_01", "cwd": r.dir}).wantShape(t)

	m := pre("edit", map[string]any{"filePath": r.path("src/math.js"), "oldString": "  return a + b\n", "newString": "  __narc_cheat__()\n  return a + b\n"}).
		wantShape(t, "deny", "reason", "message")
	if m["deny"] != true {
		t.Fatalf("deny = %v", m["deny"])
	}
	wantContains(t, "reason", str(m, "reason"), "hooktest-cheat src/math.js:2")
	wantContains(t, "message", str(m, "message"), "narc blocked an edit to src/math.js (hooktest-cheat)")

	pre("write", map[string]any{"filePath": r.path("src/x.js"), "content": "__narc_cheat__()\n"}).wantShape(t, "deny", "reason", "message")
	pre("apply_patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: src/y.js\n+__narc_cheat__()\n*** End Patch\n"}).
		wantShape(t, "deny", "reason", "message")
	pre("edit", map[string]any{"filePath": r.path("README.md"), "oldString": "# demo", "newString": "# Demo", "replaceAll": false}).wantShape(t)
	pre("bash", map[string]any{"command": "ls"}).wantShape(t)

	stop := func(loops int) hookResult {
		return runHook(t, "opencode", "stop", r.dir, map[string]any{"session_id": "ses_01", "cwd": r.dir, "loop_count": loops})
	}
	stop(0).wantShape(t)
	r.write("src/math.js", cheatedMath)
	m = stop(0).wantShape(t, "block", "reason")
	wantContains(t, "reason", str(m, "reason"), "hooktest-cheat src/math.js:2")
	// The plugin's count of follow-ups reached the cap: give up and tell the user.
	wantContains(t, "message", str(stop(3).wantShape(t, "message"), "message"), "unresolved")
}

// The generated plugin, driven by Node with a fake narc, denies edits by
// throwing and turns a stop "block" into a follow-up prompt. Skipped when
// Node cannot run TypeScript.
func TestOpenCodePluginRuns(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		// narc:allow skip-added reason="node is an optional test dependency"
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-narc")
	writeExecutable(t, fake, "#!/bin/sh\npayload=$(cat)\necho \"$3 $payload\" >> \"$(dirname \"$0\")/calls.log\"\n"+
		"case \"$3\" in\n"+
		"  pre-tool-use) case \"$payload\" in *CHEAT*) echo '{\"deny\":true,\"reason\":\"no cheating\",\"message\":\"narc blocked an edit to a.ts\"}';; *) echo '{}';; esac;;\n"+
		"  stop) echo '{\"block\":true,\"reason\":\"keep going\"}';;\n"+
		"  *) echo '{}';;\n"+
		"esac\n")
	if err := os.WriteFile(filepath.Join(dir, "narc.ts"), openCodePluginSource(fake), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := `import { NarcPlugin } from "./narc.ts"
const prompts = []
const toasts = []
const client = { session: { promptAsync: async (r) => prompts.push(r.body.parts[0].text) }, tui: { showToast: async (t) => toasts.push(t.body.message) } }
const hooks = await NarcPlugin({ client, directory: process.cwd() })
await hooks.event({ event: { type: "session.created", properties: { info: { id: "s1" } } } })
await hooks["tool.execute.before"]({ tool: "edit", sessionID: "s1" }, { args: { filePath: "a.ts", oldString: "x", newString: "y" } })
let denied = null
try { await hooks["tool.execute.before"]({ tool: "write", sessionID: "s1" }, { args: { filePath: "a.ts", content: "CHEAT" } }) } catch (e) { denied = e.message }
await hooks["tool.execute.before"]({ tool: "bash", sessionID: "s1" }, { args: { command: "ls" } })
await hooks.event({ event: { type: "session.idle", properties: { sessionID: "s1" } } })
console.log(JSON.stringify({ denied, prompts, toasts }))
`
	if err := os.WriteFile(filepath.Join(dir, "drive.mjs"), []byte(driver), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "drive.mjs")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "Unknown file extension") || strings.Contains(string(out), "ERR_UNKNOWN_FILE_EXTENSION") {
			// narc:allow skip-added reason="node before 22.6 cannot run TypeScript"
			t.Skip("this node cannot strip TypeScript types")
		}
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got struct {
		Denied  string   `json:"denied"`
		Prompts []string `json:"prompts"`
		Toasts  []string `json:"toasts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(lastLine(string(out)))), &got); err != nil {
		t.Fatalf("driver output %q: %v", out, err)
	}
	if got.Denied != "no cheating" || len(got.Prompts) != 1 || got.Prompts[0] != "keep going" ||
		len(got.Toasts) != 1 || got.Toasts[0] != "narc blocked an edit to a.ts" {
		t.Fatalf("plugin behaviour: %+v", got)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	wantContains(t, "calls", string(calls), `session-start {"session_id":"s1"`, `pre-tool-use {"session_id":"s1","tool":"edit"`, `stop {"session_id":"s1"`, `"loop_count":0`)
	wantNotContains(t, "calls", string(calls), `"tool":"bash"`)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
