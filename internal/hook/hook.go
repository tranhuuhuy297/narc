// Package hook connects narc to coding agents and to git.
//
// Run handles one hook invocation (`narc hook <harness> <event>`): it reads
// the harness's JSON payload on stdin, decides, and answers in that
// harness's own protocol. Install and Uninstall add or remove narc's hook
// configuration for a harness.
//
// Harnesses and events:
//
//	claude    pre-tool-use, post-tool-use, stop, subagent-stop, session-start
//	codex     pre-tool-use, post-tool-use, stop, session-start
//	gemini    before-tool, after-tool, after-agent, session-start
//	cursor    pre-tool-use, post-tool-use, stop, session-start
//	opencode  pre-tool-use, post-tool-use, stop, session-start (called by the generated plugin)
//	git       pre-commit
//
// What each kind of event does:
//
//   - Pre-edit (pre-tool-use, before-tool): rebuild the file the way the tool
//     would write it and run every rule on its change since the session
//     baseline, so reworking code or tests the agent itself wrote in this
//     session is not mistaken for weakening older checks. Other files
//     changed in this session are added as context, so cross-file rules (a
//     test moved to another file, a literal the tests use) see the whole
//     picture, but only findings in the edited files count, and only ones
//     the edit itself introduces. The edit is denied when such a finding
//     reaches hook.block_on, and also when it would leave .narc.yml
//     unparseable (which would turn narc off).
//   - Post-edit (post-tool-use, after-tool): the edit already happened.
//     Changes to the touched files since the session baseline are checked
//     and, when a finding reaches the threshold, fed back to the model as
//     non-blocking context (Claude: a PostToolUse "block" decision, which
//     only annotates the tool result).
//   - Stop (stop, subagent-stop, after-agent): everything changed since the
//     session baseline, untracked files included, is scanned. Findings that
//     already existed when the session started are ignored. When new
//     findings reach the threshold the agent is told to keep working. After
//     hook.max_blocks consecutive blocks narc lets the agent stop, warns the
//     human, and stops raising those findings for the rest of the session.
//     The human is also told, once, about findings that waivers let
//     through and about changes to .narc.yml: the coaching messages teach
//     the agent how to waive a finding, so a person should see each one.
//   - Session start: records HEAD and the fingerprints of findings that
//     already exist, under <git-dir>/narc/sessions/<id>.json. If the
//     harness has no session-start hook, the first pre-edit event of a
//     session records the same snapshot.
//   - git pre-commit: scans the staged changes, prints a report on stderr
//     and exits 1 when findings reach fail_on.
//
// Hooks fail open: bad input, a missing repository, a broken .narc.yml or
// any internal error lets the agent continue (exit 0, the harness's "allow"
// output) and leaves a one-line note on stderr. A stop with a broken
// .narc.yml also tells the human that narc is not checking anything.
// Nothing touches the network.
// Setting NARC_HOOK_LOG to a file path logs every raw payload there.
//
// From the CLI:
//
//	// narc hook <harness> <event>
//	os.Exit(hook.Run(harness, event, os.Stdin, os.Stdout, os.Stderr, hook.Env{Version: version}))
//
//	// narc install <harness> [--project|--local|--user]
//	res, err := hook.Install(harness, hook.InstallOptions{Scope: hook.ScopeProject, RepoRoot: root, Home: home})
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	// Every built-in rule must be registered for hooks to be useful.
	_ "github.com/huyth45/narc/internal/rules/all"
)

// Env is the process environment a hook runs in. Zero fields fall back to
// the real process (os.Getwd, time.Now, os.Getenv).
type Env struct {
	// Getwd returns the directory narc was started in. It is used when the
	// payload carries no working directory.
	Getwd func() (string, error)
	// Now returns the current time (session timestamps).
	Now func() time.Time
	// Getenv reads environment variables such as CLAUDE_PROJECT_DIR and
	// NARC_HOOK_LOG.
	Getenv func(string) string
	// Version is narc's version, recorded in session state.
	Version string
}

func (e Env) withDefaults() Env {
	if e.Getwd == nil {
		e.Getwd = os.Getwd
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	return e
}

// LogEnvVar names an environment variable that, when set to a file path,
// makes every hook invocation append its raw payload to that file. It is a
// debugging aid for harnesses whose payloads are not fully documented.
const LogEnvVar = "NARC_HOOK_LOG"

// maxPayload bounds how much of stdin narc reads (a Write of a large file
// carries the whole file).
const maxPayload = 64 << 20

// eventKind is what an event means to narc, independent of its harness name.
type eventKind int

const (
	preEdit eventKind = iota + 1
	postEdit
	stopEvent
	subagentStop
	sessionStart
	preCommit
)

type eventDef struct {
	name string
	kind eventKind
}

// harnessEvents lists the events each harness supports, in display order.
var harnessEvents = map[string][]eventDef{
	"claude": {
		{"pre-tool-use", preEdit}, {"post-tool-use", postEdit}, {"stop", stopEvent},
		{"subagent-stop", subagentStop}, {"session-start", sessionStart},
	},
	"codex": {
		{"pre-tool-use", preEdit}, {"post-tool-use", postEdit}, {"stop", stopEvent},
		{"session-start", sessionStart},
	},
	"gemini": {
		{"before-tool", preEdit}, {"after-tool", postEdit}, {"after-agent", stopEvent},
		{"session-start", sessionStart},
	},
	"cursor": {
		{"pre-tool-use", preEdit}, {"post-tool-use", postEdit}, {"stop", stopEvent},
		{"session-start", sessionStart},
	},
	"opencode": {
		{"pre-tool-use", preEdit}, {"post-tool-use", postEdit}, {"stop", stopEvent},
		{"session-start", sessionStart},
	},
	"git": {{"pre-commit", preCommit}},
}

// Harnesses returns the supported harness names, sorted.
func Harnesses() []string {
	out := make([]string, 0, len(harnessEvents))
	for h := range harnessEvents {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// Events returns the event names a harness supports (nil if unknown).
func Events(harness string) []string {
	var out []string
	for _, e := range harnessEvents[normalizeHarness(harness)] {
		out = append(out, e.name)
	}
	return out
}

// Supports reports whether `narc hook <harness> <event>` is a known hook.
// Event names are matched loosely: "PreToolUse", "preToolUse" and
// "pre-tool-use" are the same event.
func Supports(harness, event string) bool {
	_, ok := lookupEvent(normalizeHarness(harness), event)
	return ok
}

func normalizeHarness(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	switch h {
	case "claude-code", "claudecode":
		return "claude"
	case "gemini-cli":
		return "gemini"
	case "open-code":
		return "opencode"
	}
	return h
}

// squash lowercases s and drops separators, so "PreToolUse",
// "pre-tool-use" and "pre_tool_use" compare equal.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r != '-' && r != '_' && r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lookupEvent(harness, event string) (eventDef, bool) {
	want := squash(event)
	for _, e := range harnessEvents[harness] {
		if squash(e.name) == want {
			return e, true
		}
	}
	return eventDef{}, false
}

// cursorPermissionEvents are Cursor's permission hooks (squashed names):
// for them, output that is not a valid permission blocks the action.
var cursorPermissionEvents = toSet("pretooluse", "beforeshellexecution", "beforemcpexecution", "beforereadfile", "beforetabfileread", "subagentstart")

// outcome is what a hook invocation prints and returns.
type outcome struct {
	stdout []byte
	stderr string
	code   int
}

// Run handles one hook invocation and returns the process exit code. It
// never panics and fails open: on any internal error the agent may proceed.
func Run(harness, event string, stdin io.Reader, stdout, stderr io.Writer, env Env) (code int) {
	env = env.withDefaults()
	harness = normalizeHarness(harness)
	ev, known := lookupEvent(harness, event)
	if !known {
		fmt.Fprintf(stderr, "narc: unknown hook %q %q; allowing (supported: %s)\n", harness, event, supportedList())
		var kind eventKind
		if harness == "cursor" && cursorPermissionEvents[squash(event)] {
			kind = preEdit // answered with an explicit permission
		}
		out := passOutput(harness, kind)
		_, _ = stdout.Write(out.stdout)
		return 0
	}

	// proto is the protocol to answer in. Cursor also runs the Claude Code
	// hooks it finds in .claude/settings*.json, with its own payloads, and
	// those are answered in Cursor's protocol.
	proto := harness
	var out outcome
	defer func() {
		if p := recover(); p != nil {
			out = failOpen(proto, ev.kind, fmt.Errorf("internal error: %v", p))
		}
		_, _ = stdout.Write(out.stdout)
		if out.stderr != "" {
			fmt.Fprint(stderr, out.stderr)
		}
		code = out.code
	}()

	if ev.kind == preCommit {
		// git hooks get no payload, and stdin may be a terminal.
		out = runPreCommit(env)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxPayload))
	if err != nil {
		out = failOpen(proto, ev.kind, fmt.Errorf("reading stdin: %w", err))
		return
	}
	logPayload(env, harness, ev.name, raw)
	if harness == "claude" && isCursorPayload(raw) {
		proto = "cursor"
	}
	out = handle(proto, ev.kind, raw, env)
	return
}

func supportedList() string {
	var parts []string
	for _, h := range Harnesses() {
		parts = append(parts, h+" ("+strings.Join(Events(h), ", ")+")")
	}
	return strings.Join(parts, "; ")
}

// handle parses the payload, decides, and answers in proto's protocol.
func handle(proto string, kind eventKind, raw []byte, env Env) outcome {
	req, err := parseRequest(proto, kind, raw)
	if err != nil {
		return failOpen(proto, kind, err)
	}
	var v verdict
	switch kind {
	case preEdit:
		v, err = checkPreEdit(req, env)
	case postEdit:
		v, err = checkPostEdit(req, env)
	case stopEvent, subagentStop:
		v, err = checkStop(req, env)
	case sessionStart:
		v, err = startSession(req, env)
	}
	if err != nil {
		var cfgErr *configError
		if errors.As(err, &cfgErr) && kind == stopEvent {
			// Still fail open, but tell the human: until the configuration
			// loads, narc checks nothing.
			out := render(proto, kind, verdict{action: release, notice: fmt.Sprintf(
				"narc: cannot load its configuration (%s), so it is not checking this session's changes. Fix the file to turn narc's checks back on.",
				truncate(oneLine(cfgErr.err.Error()), 300))})
			out.stderr += failOpen(proto, kind, err).stderr
			return out
		}
		return failOpen(proto, kind, err)
	}
	return render(proto, kind, v)
}

// failOpen lets the agent proceed and notes why on stderr.
func failOpen(harness string, kind eventKind, err error) outcome {
	out := passOutput(harness, kind)
	out.stderr = fmt.Sprintf("narc: %v (hook allowed the action)\n", truncate(oneLine(err.Error()), 300))
	return out
}

func passOutput(harness string, kind eventKind) outcome {
	if harness == "git" {
		return outcome{}
	}
	return render(harness, kind, verdict{})
}

// logPayload appends the raw payload to $NARC_HOOK_LOG, if set.
func logPayload(env Env, harness, event string, raw []byte) {
	p := env.Getenv(LogEnvVar)
	if p == "" {
		return
	}
	var payload any = json.RawMessage(raw)
	if !json.Valid(raw) {
		payload = string(raw)
	}
	line, err := json.Marshal(map[string]any{
		"time": env.Now().UTC().Format(time.RFC3339), "harness": harness, "event": event, "payload": payload,
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// oneLine collapses all whitespace, newlines included, to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
