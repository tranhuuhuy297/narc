package hook

import (
	"bytes"
	"encoding/json"
)

// action is what narc wants the harness to do.
type action int

const (
	pass     action = iota // let the agent proceed, silently
	deny                   // pre-edit: refuse the tool call
	feedback               // post-edit: tell the model (the edit already happened)
	block                  // stop: keep the agent working
	release                // stop: let the agent stop, but warn the human
)

// verdict is narc's decision for one hook invocation.
type verdict struct {
	action action
	reason string // for the model
	notice string // for the human (one line or a short list)
}

// Output shapes. Field order is the order keys are printed in.
type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

// commonOutput is the JSON accepted by Claude Code, Codex and Gemini CLI.
type commonOutput struct {
	Decision           string              `json:"decision,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	SystemMessage      string              `json:"systemMessage,omitempty"`
}

type cursorOutput struct {
	Permission        string `json:"permission,omitempty"`
	UserMessage       string `json:"user_message,omitempty"`
	AgentMessage      string `json:"agent_message,omitempty"`
	FollowupMessage   string `json:"followup_message,omitempty"`
	AdditionalContext string `json:"additional_context,omitempty"`
}

// openCodeOutput is the protocol between narc and its own OpenCode plugin.
type openCodeOutput struct {
	Deny     bool   `json:"deny,omitempty"`     // pre-tool-use: throw to refuse the edit
	Block    bool   `json:"block,omitempty"`    // stop: send reason as a follow-up prompt
	Reason   string `json:"reason,omitempty"`   // for the model
	Feedback string `json:"feedback,omitempty"` // post-tool-use: append to the tool output
	Message  string `json:"message,omitempty"`  // for the human (a toast), on deny and stop
}

// render turns a verdict into the harness's protocol. Every harness gets
// exit code 0 and JSON on stdout (or nothing, where that means "allow").
func render(harness string, kind eventKind, v verdict) outcome {
	switch harness {
	case "claude":
		return renderClaude(v)
	case "codex":
		return renderCodex(v)
	case "gemini":
		return renderGemini(v)
	case "cursor":
		return renderCursor(kind, v)
	case "opencode":
		return renderOpenCode(v)
	}
	return outcome{}
}

// Claude Code: an empty stdout allows. A PreToolUse deny is a
// permissionDecision; Stop and PostToolUse use decision "block".
func renderClaude(v verdict) outcome {
	switch v.action {
	case deny:
		return jsonOutcome(commonOutput{
			HookSpecificOutput: &hookSpecificOutput{HookEventName: "PreToolUse", PermissionDecision: "deny", PermissionDecisionReason: v.reason},
			SystemMessage:      v.notice,
		})
	case feedback, block:
		return jsonOutcome(commonOutput{Decision: "block", Reason: v.reason})
	case release:
		return jsonOutcome(commonOutput{SystemMessage: v.notice})
	}
	return outcome{}
}

// Codex: an empty stdout allows. Unsupported fields make Codex treat the
// hook as failed and run the tool anyway, so a deny carries nothing else.
// The Stop output schema rejects hookSpecificOutput.
func renderCodex(v verdict) outcome {
	switch v.action {
	case deny:
		return jsonOutcome(commonOutput{HookSpecificOutput: &hookSpecificOutput{
			HookEventName: "PreToolUse", PermissionDecision: "deny", PermissionDecisionReason: v.reason}})
	case feedback:
		return jsonOutcome(commonOutput{HookSpecificOutput: &hookSpecificOutput{
			HookEventName: "PostToolUse", AdditionalContext: v.reason}})
	case block:
		return jsonOutcome(commonOutput{Decision: "block", Reason: v.reason})
	case release:
		return jsonOutcome(commonOutput{SystemMessage: v.notice})
	}
	return outcome{}
}

// Gemini CLI: stdout must be JSON; "{}" allows (and, unlike an explicit
// "allow", does not bypass the user's confirmation). "deny" refuses a tool
// call in BeforeTool and sends the reason back as a new prompt in
// AfterAgent.
func renderGemini(v verdict) outcome {
	switch v.action {
	case deny:
		return jsonOutcome(commonOutput{Decision: "deny", Reason: v.reason, SystemMessage: v.notice})
	case feedback:
		return jsonOutcome(commonOutput{HookSpecificOutput: &hookSpecificOutput{
			HookEventName: "AfterTool", AdditionalContext: v.reason}})
	case block:
		return jsonOutcome(commonOutput{Decision: "deny", Reason: v.reason})
	case release:
		return jsonOutcome(commonOutput{SystemMessage: v.notice})
	}
	return jsonOutcome(commonOutput{})
}

// Cursor: permission hooks block on invalid or empty output, so preToolUse
// always answers with a permission. A stop "block" is a follow-up message.
// Cursor has no user-visible message for stop hooks; a release only notes
// on stderr (shown in Cursor's Hooks output channel).
func renderCursor(kind eventKind, v verdict) outcome {
	switch v.action {
	case deny:
		return jsonOutcome(cursorOutput{Permission: "deny", UserMessage: v.notice, AgentMessage: v.reason})
	case feedback:
		return jsonOutcome(cursorOutput{AdditionalContext: v.reason})
	case block:
		return jsonOutcome(cursorOutput{FollowupMessage: v.reason})
	case release:
		out := jsonOutcome(cursorOutput{})
		out.stderr = v.notice + "\n"
		return out
	}
	if kind == preEdit {
		return jsonOutcome(cursorOutput{Permission: "allow"})
	}
	return jsonOutcome(cursorOutput{})
}

// OpenCode: narc's plugin parses this; "{}" means carry on.
func renderOpenCode(v verdict) outcome {
	switch v.action {
	case deny:
		return jsonOutcome(openCodeOutput{Deny: true, Reason: v.reason, Message: v.notice})
	case feedback:
		return jsonOutcome(openCodeOutput{Feedback: v.reason})
	case block:
		return jsonOutcome(openCodeOutput{Block: true, Reason: v.reason})
	case release:
		return jsonOutcome(openCodeOutput{Message: v.notice})
	}
	return jsonOutcome(openCodeOutput{})
}

func jsonOutcome(v any) outcome {
	return outcome{stdout: marshalJSON(v)}
}

// marshalJSON encodes v on one line without HTML escaping, newline-terminated.
func marshalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("{}\n")
	}
	return buf.Bytes()
}
