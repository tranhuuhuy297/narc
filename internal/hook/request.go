package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// request is a hook payload normalized across harnesses.
type request struct {
	harness   string
	kind      eventKind
	sessionID string
	agentID   string   // subagent that is stopping (Claude subagent-stop)
	cwd       string   // the agent's working directory ("" = unknown)
	roots     []string // Cursor workspace roots
	tool      string
	ops       []fileOp // file operations of the tool call (pre/post edit)
	// stopActive is the harness's stop_hook_active flag: true when the
	// agent is already continuing because a stop hook blocked it. nil when
	// the payload does not say.
	stopActive *bool
	// loopCount is how many follow-ups this hook already triggered
	// (Cursor's loop_count, or the count kept by narc's OpenCode plugin).
	loopCount int
	// aborted is set when the user aborted the turn (Cursor stop status).
	aborted bool
}

// payload is the union of the fields narc reads from every harness.
type payload struct {
	SessionID      string          `json:"session_id"`
	ConversationID string          `json:"conversation_id"` // Cursor
	Cwd            string          `json:"cwd"`
	WorkspaceRoots []string        `json:"workspace_roots"` // Cursor
	ToolName       string          `json:"tool_name"`
	Tool           string          `json:"tool"` // narc's OpenCode plugin
	ToolInput      json.RawMessage `json:"tool_input"`
	Args           json.RawMessage `json:"args"` // narc's OpenCode plugin
	StopHookActive *bool           `json:"stop_hook_active"`
	AgentID        string          `json:"agent_id"`
	LoopCount      *int            `json:"loop_count"`
	Status         string          `json:"status"` // Cursor stop: completed, aborted, error
}

// isCursorPayload reports whether a payload was sent by Cursor (which also
// runs Claude Code hooks): it has Cursor's top-level fields. Only top-level
// keys count, so a Claude edit whose text mentions them is not mistaken for
// Cursor. Invalid JSON falls back to a plain text search.
func isCursorPayload(raw []byte) bool {
	var p struct {
		CursorVersion  json.RawMessage `json:"cursor_version"`
		ConversationID json.RawMessage `json:"conversation_id"`
		SessionID      json.RawMessage `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return bytes.Contains(raw, []byte(`"cursor_version"`))
	}
	return p.CursorVersion != nil || (p.ConversationID != nil && p.SessionID == nil)
}

func parseRequest(harness string, kind eventKind, raw []byte) (*request, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty hook payload on stdin")
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid hook payload: %w", err)
	}
	req := &request{
		harness:    harness,
		kind:       kind,
		sessionID:  p.SessionID,
		agentID:    p.AgentID,
		cwd:        p.Cwd,
		roots:      p.WorkspaceRoots,
		stopActive: p.StopHookActive,
	}
	if req.sessionID == "" {
		req.sessionID = p.ConversationID
	}
	if p.LoopCount != nil {
		req.loopCount = *p.LoopCount
	}
	if harness == "cursor" && p.Status != "" && p.Status != "completed" {
		req.aborted = true
	}
	if kind == preEdit || kind == postEdit {
		req.tool = p.ToolName
		input := p.ToolInput
		if req.tool == "" {
			req.tool, input = p.Tool, p.Args
		}
		ops, err := toolOps(req.tool, input)
		if err != nil {
			return nil, err
		}
		req.ops = ops
	}
	return req, nil
}

// editInput is the union of the edit-tool inputs of every harness:
// Claude (file_path, old_string, ...), Gemini (allow_multiple), Cursor
// (path or file_path), OpenCode (filePath, oldString, ...).
type editInput struct {
	FilePath             string      `json:"file_path"`
	FilePathCamel        string      `json:"filePath"`
	Path                 string      `json:"path"`
	TargetFile           string      `json:"target_file"`
	Content              *string     `json:"content"`
	Contents             *string     `json:"contents"`
	OldString            *string     `json:"old_string"`
	OldStringCamel       *string     `json:"oldString"`
	NewString            *string     `json:"new_string"`
	NewStringCamel       *string     `json:"newString"`
	ReplaceAll           bool        `json:"replace_all"`
	ReplaceAllCamel      bool        `json:"replaceAll"`
	AllowMultiple        bool        `json:"allow_multiple"`
	ExpectedReplacements int         `json:"expected_replacements"`
	Edits                []editInput `json:"edits"` // legacy MultiEdit
	// Command is a shell command (a string, or an argv array in Codex's
	// shell tool), or the patch of Codex's apply_patch.
	Command   json.RawMessage `json:"command"`
	Workdir   string          `json:"workdir"` // Codex shell tool: where the command runs
	PatchText string          `json:"patchText"`
	Patch     string          `json:"patch"`
	Input     string          `json:"input"`
}

// command returns the command as one string; an argv array is joined with
// spaces (`bash -lc 'script'` keeps the script intact as its last part).
func (in *editInput) command() string {
	var s string
	if json.Unmarshal(in.Command, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(in.Command, &argv) == nil {
		return strings.Join(argv, " ")
	}
	return ""
}

func (in *editInput) path() string {
	for _, p := range []string{in.FilePath, in.FilePathCamel, in.Path, in.TargetFile} {
		if p != "" {
			return p
		}
	}
	return ""
}

func (in *editInput) content() (string, bool) {
	switch {
	case in.Content != nil:
		return *in.Content, true
	case in.Contents != nil:
		return *in.Contents, true
	}
	return "", false
}

func (in *editInput) replacement() (replacement, bool) {
	old, nw := in.OldString, in.NewString
	if old == nil {
		old = in.OldStringCamel
	}
	if nw == nil {
		nw = in.NewStringCamel
	}
	if old == nil || nw == nil {
		return replacement{}, false
	}
	all := in.ReplaceAll || in.ReplaceAllCamel || in.AllowMultiple || in.ExpectedReplacements > 1
	// OpenCode (the only harness that spells it oldString) writes newString
	// as the whole file when oldString is empty, even over an existing file.
	overwrite := in.OldString == nil && in.OldStringCamel != nil
	return replacement{old: *old, new: *nw, all: all, overwrite: overwrite}, true
}

func (in *editInput) patchText() string {
	for _, s := range []string{in.command(), in.PatchText, in.Patch, in.Input} {
		if strings.Contains(s, patchBegin) {
			return s
		}
	}
	return ""
}

// applyPatchCall matches the end of a shell command line that feeds a
// patch to apply_patch: a heredoc (`apply_patch <<'EOF'`) or a quoted
// argument, optionally after `cd <dir> &&`.
var applyPatchCall = regexp.MustCompile(`(?:^|[\s;&|('"])(?:cd\s+(\S+)\s*&&\s*)?(?:apply_patch|applypatch)\s*(?:<<-?\s*['"]?\w+['"]?|['"])?\s*$`)

// shellPatch extracts the patch from a shell command that runs apply_patch
// (Codex runs those as patches). It returns the directory the command
// changes to first ("" for none). A command that merely contains patch
// text, such as `cat > notes.md <<EOF` with an example patch, is not one.
func shellPatch(command string) (patch, dir string, ok bool) {
	i := strings.Index(command, patchBegin)
	if i < 0 {
		return "", "", false
	}
	pre := strings.TrimRight(command[:i], " \t\r\n")
	line := pre[strings.LastIndexByte(pre, '\n')+1:]
	m := applyPatchCall.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return command, strings.Trim(m[1], `'"`), true
}

// Tool names, lowercased, grouped by what they do to files.
var (
	writeTools = toSet("write", "write_file", "create_file")
	// Edit tools replace text; some harnesses send whole content instead.
	editTools   = toSet("edit", "multiedit", "strreplace", "str_replace", "replace", "search_replace", "edit_file")
	deleteTools = toSet("delete", "delete_file")
	patchTools  = toSet("apply_patch", "applypatch", "patch")
	// Codex applies `apply_patch` heredocs run through its shell tools as
	// patches too.
	shellTools = toSet("bash", "shell", "exec_command", "local_shell", "run_shell_command")
)

func toSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// shellPatchOps returns the file operations of a shell command that runs
// apply_patch, and none for any other command. Patch paths are relative to
// the directory the command runs in: workdir ("" for the agent's cwd),
// then any `cd <dir> &&` before apply_patch.
func shellPatchOps(command, workdir string) ([]fileOp, error) {
	patch, dir, ok := shellPatch(command)
	if !ok {
		return nil, nil
	}
	ops, err := patchOps(patch)
	if err != nil {
		return nil, err
	}
	dir = underDir(workdir, dir)
	if dir == "" {
		return ops, nil
	}
	for i := range ops {
		ops[i].path = underDir(dir, ops[i].path)
		if ops[i].newPath != "" {
			ops[i].newPath = underDir(dir, ops[i].newPath)
		}
	}
	return ops, nil
}

// underDir resolves p against dir (either may be empty).
func underDir(dir, p string) string {
	if dir == "" || filepath.IsAbs(p) {
		return p
	}
	if p == "" {
		return dir
	}
	return filepath.Join(dir, p)
}

// toolOps turns a tool call into file operations. Tools that do not edit
// files (and notebook edits, which narc does not analyze) yield none.
func toolOps(tool string, input json.RawMessage) ([]fileOp, error) {
	name := strings.ToLower(tool)
	input = bytes.TrimSpace(input)
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		return nil, nil
	}
	if !patchTools[name] && !shellTools[name] && !writeTools[name] && !editTools[name] && !deleteTools[name] {
		return nil, nil
	}
	if input[0] == '"' {
		// Some harnesses pass a freeform tool's input as a bare string.
		var s string
		if err := json.Unmarshal(input, &s); err != nil {
			return nil, fmt.Errorf("%s input: %w", tool, err)
		}
		switch {
		case patchTools[name]:
			return patchOps(s)
		case shellTools[name]:
			return shellPatchOps(s, "")
		}
		return nil, nil
	}
	var in editInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("%s input: %w", tool, err)
	}
	switch {
	case patchTools[name]:
		text := in.patchText()
		if text == "" {
			return nil, fmt.Errorf("%s input has no patch text", tool)
		}
		return patchOps(text)
	case shellTools[name]:
		return shellPatchOps(in.command(), in.Workdir)
	}
	path := in.path()
	if path == "" {
		return nil, fmt.Errorf("%s input has no file path", tool)
	}
	if deleteTools[name] {
		return []fileOp{deleteOp(path)}, nil
	}
	// Edit-style input: one replacement, or a list of them (MultiEdit).
	var reps []replacement
	if r, ok := in.replacement(); ok {
		reps = append(reps, r)
	}
	for i := range in.Edits {
		r, ok := in.Edits[i].replacement()
		if !ok {
			return nil, fmt.Errorf("%s edit %d has no old/new string", tool, i+1)
		}
		reps = append(reps, r)
	}
	if len(reps) > 0 {
		return []fileOp{replaceOp(path, reps)}, nil
	}
	if content, ok := in.content(); ok {
		return []fileOp{writeOp(path, content)}, nil
	}
	// An input shape narc cannot replay exactly (e.g. a sketch with
	// "... existing code ..." markers). Let the edit through.
	return nil, nil
}
