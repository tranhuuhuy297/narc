package hook

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// settingsInstaller manages narc's entries in a harness's JSON hooks file.
type settingsInstaller struct {
	harness string
	display string
	// nested is the layout event → [{matcher, hooks: [handler]}] used by
	// Claude Code, Codex and Gemini CLI. Cursor uses event → [handler],
	// with the matcher inside the handler.
	nested bool
	// comments: the file may contain // and /* */ comments.
	comments bool
	path     func(o InstallOptions) (string, error)
	specs    func(o InstallOptions) []hookSpec
	// prepare adjusts a document before hooks are merged in.
	prepare func(doc *jsonObject)
	// notes returns follow-up notes after an install.
	notes func(o InstallOptions, path string, doc *jsonObject) []string
}

// hookSpec is one hook narc installs.
type hookSpec struct {
	event   string      // key under "hooks", e.g. "PreToolUse"
	matcher string      // group matcher ("" for none; nested layout only)
	handler *jsonObject // the handler object
}

func (s hookSpec) entry(nested bool) *jsonObject {
	if !nested {
		return s.handler
	}
	g := newObject()
	if s.matcher != "" {
		g.set("matcher", s.matcher)
	}
	g.set("hooks", []any{s.handler})
	return g
}

var claudeSettings = &settingsInstaller{
	harness: "claude",
	display: "Claude Code",
	nested:  true,
	path: func(o InstallOptions) (string, error) {
		switch o.Scope {
		case ScopeProject:
			return filepath.Join(o.RepoRoot, ".claude", "settings.json"), nil
		case ScopeLocal:
			return filepath.Join(o.RepoRoot, ".claude", "settings.local.json"), nil
		}
		return filepath.Join(o.Home, ".claude", "settings.json"), nil
	},
	specs: func(o InstallOptions) []hookSpec {
		h := func(event string, timeout int) *jsonObject {
			return object("type", "command", "command", hookCommand(o, "claude", event), "timeout", timeout)
		}
		return []hookSpec{
			{"PreToolUse", "Write|Edit|MultiEdit", h("pre-tool-use", 30)},
			{"Stop", "", h("stop", 120)},
			{"SubagentStop", "", h("subagent-stop", 120)},
			{"SessionStart", "", h("session-start", 60)},
		}
	},
	notes: func(o InstallOptions, path string, doc *jsonObject) []string {
		notes := []string{"Claude Code reads hooks at startup: restart running sessions (or review the hooks with /hooks)."}
		if off, _ := doc.vals["disableAllHooks"].(bool); off {
			notes = append(notes, "Hooks are turned off in "+path+" (disableAllHooks is true); narc's hooks will not run until you turn them on.")
		}
		if p := installedElsewhere("cursor", cursorFiles(o)); p != "" {
			notes = append(notes, "narc's Cursor hooks are installed in "+p+", and Cursor also runs Claude Code hooks, so Cursor would run narc twice. "+
				"Keep one: `narc uninstall cursor`, or turn off third-party hooks in Cursor's settings.")
		}
		return notes
	},
}

var codexSettings = &settingsInstaller{
	harness: "codex",
	display: "Codex",
	nested:  true,
	path: func(o InstallOptions) (string, error) {
		switch o.Scope {
		case ScopeProject:
			return filepath.Join(o.RepoRoot, ".codex", "hooks.json"), nil
		case ScopeUser:
			return filepath.Join(o.Home, ".codex", "hooks.json"), nil
		}
		return "", scopeError("Codex", o.Scope, ScopeProject, ScopeUser)
	},
	specs: func(o InstallOptions) []hookSpec {
		h := func(event string, timeout int, status string) *jsonObject {
			return object("type", "command", "command", hookCommand(o, "codex", event), "timeout", timeout, "statusMessage", status)
		}
		return []hookSpec{
			{"PreToolUse", "^apply_patch$", h("pre-tool-use", 30, "narc: checking the edit")},
			{"Stop", "", h("stop", 120, "narc: checking this session's changes")},
			{"SessionStart", "", h("session-start", 60, "narc: recording the session baseline")},
		}
	},
	notes: func(o InstallOptions, _ string, _ *jsonObject) []string {
		notes := []string{"Codex skips new or changed hooks until you trust them: start Codex and approve narc's hooks in /hooks."}
		if o.Scope == ScopeProject {
			notes = append(notes, "Codex loads .codex/hooks.json only for trusted projects.")
		}
		if o.Home != "" && hooksDisabledInCodex(filepath.Join(o.Home, ".codex", "config.toml")) {
			notes = append(notes, "Hooks are turned off in ~/.codex/config.toml ([features] hooks = false); narc's hooks will not run until you turn them on.")
		}
		return notes
	},
}

var geminiSettings = &settingsInstaller{
	harness:  "gemini",
	display:  "Gemini CLI",
	nested:   true,
	comments: true,
	path: func(o InstallOptions) (string, error) {
		switch o.Scope {
		case ScopeProject:
			return filepath.Join(o.RepoRoot, ".gemini", "settings.json"), nil
		case ScopeUser:
			return filepath.Join(o.Home, ".gemini", "settings.json"), nil
		}
		return "", scopeError("Gemini CLI", o.Scope, ScopeProject, ScopeUser)
	},
	specs: func(o InstallOptions) []hookSpec {
		// Gemini timeouts are in milliseconds.
		h := func(name, event string, timeout int) *jsonObject {
			return object("name", name, "type", "command", "command", hookCommand(o, "gemini", event), "timeout", timeout)
		}
		return []hookSpec{
			{"BeforeTool", "^(write_file|replace)$", h("narc-before-tool", "before-tool", 30000)},
			{"AfterAgent", "", h("narc-after-agent", "after-agent", 120000)},
			{"SessionStart", "", h("narc-session-start", "session-start", 60000)},
		}
	},
	notes: func(o InstallOptions, path string, doc *jsonObject) []string {
		notes := []string{"Gemini CLI runs project hooks only in trusted folders and asks about new hooks; review them with /hooks."}
		if cfg, ok := doc.vals["hooksConfig"].(*jsonObject); ok {
			if enabled, ok := cfg.vals["enabled"].(bool); ok && !enabled {
				notes = append(notes, "Hooks are turned off in "+path+" (hooksConfig.enabled is false); narc's hooks will not run until you turn them on.")
			}
			if disabled, ok := cfg.vals["disabled"].([]any); ok {
				for _, d := range disabled {
					if s, _ := d.(string); strings.HasPrefix(s, "narc-") {
						notes = append(notes, fmt.Sprintf("hooksConfig.disabled in %s lists %q; that hook will not run.", path, s))
					}
				}
			}
		}
		return notes
	},
}

var cursorSettings = &settingsInstaller{
	harness: "cursor",
	display: "Cursor",
	path: func(o InstallOptions) (string, error) {
		switch o.Scope {
		case ScopeProject:
			return filepath.Join(o.RepoRoot, ".cursor", "hooks.json"), nil
		case ScopeUser:
			return filepath.Join(o.Home, ".cursor", "hooks.json"), nil
		}
		return "", scopeError("Cursor", o.Scope, ScopeProject, ScopeUser)
	},
	specs: func(o InstallOptions) []hookSpec {
		cmd := func(event string) string { return hookCommand(o, "cursor", event) }
		return []hookSpec{
			{event: "preToolUse", handler: object("command", cmd("pre-tool-use"), "matcher", "Write|StrReplace|Edit|Delete", "timeout", 30)},
			{event: "stop", handler: object("command", cmd("stop"), "timeout", 120)},
			{event: "sessionStart", handler: object("command", cmd("session-start"), "timeout", 60)},
		}
	},
	prepare: func(doc *jsonObject) {
		if _, ok := doc.get("version"); !ok {
			doc.keys = append([]string{"version"}, doc.keys...)
			doc.vals["version"] = 1
		}
	},
	notes: func(o InstallOptions, _ string, _ *jsonObject) []string {
		notes := []string{"Cursor reloads hooks.json by itself; project hooks run only in a trusted workspace."}
		if p := installedElsewhere("claude", claudeFiles(o)); p != "" {
			notes = append(notes, "narc's Claude Code hooks are installed in "+p+", and Cursor also runs Claude Code hooks, so Cursor will run narc twice. "+
				"Keep one: `narc uninstall claude`, or turn off third-party hooks in Cursor's settings.")
		} else {
			notes = append(notes, "Cursor also runs Claude Code hooks from .claude/settings*.json by default; if you install narc for Claude Code too, Cursor runs narc twice.")
		}
		return notes
	},
}

func (s *settingsInstaller) install(o InstallOptions) (Result, error) {
	path, err := s.path(o)
	if err != nil {
		return Result{}, err
	}
	f, err := loadJSONFile(path, s.comments)
	if err != nil {
		return Result{}, err
	}
	if s.prepare != nil {
		s.prepare(f.doc)
	}
	hooks, err := f.doc.childObject("hooks")
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, sp := range s.specs(o) {
		arr, err := hooks.childArray(sp.event)
		if err != nil {
			return Result{}, fmt.Errorf("%s: hooks: %w", path, err)
		}
		arr, at := s.removeOurs(arr, o)
		if at < 0 {
			at = len(arr)
		}
		hooks.set(sp.event, slices.Insert(arr, at, any(sp.entry(s.nested))))
	}
	var res Result
	changed, err := f.save(o, &res)
	if err != nil {
		return res, err
	}
	if !changed {
		res.Notes = append(res.Notes, fmt.Sprintf("narc's %s hooks in %s are already up to date.", s.display, path))
	}
	res.Notes = append(res.Notes, s.notes(o, path, f.doc)...)
	return res, nil
}

func (s *settingsInstaller) uninstall(o InstallOptions) (Result, error) {
	path, err := s.path(o)
	if err != nil {
		return Result{}, err
	}
	f, err := loadJSONFile(path, s.comments)
	if err != nil {
		return Result{}, err
	}
	var res Result
	hooks, _ := f.doc.vals["hooks"].(*jsonObject)
	removed := false
	if hooks != nil {
		for _, event := range slices.Clone(hooks.keys) {
			arr, ok := hooks.vals[event].([]any)
			if !ok {
				continue
			}
			arr, at := s.removeOurs(arr, o)
			if at < 0 {
				continue
			}
			removed = true
			if len(arr) == 0 {
				hooks.del(event)
			} else {
				hooks.set(event, arr)
			}
		}
		if removed && len(hooks.keys) == 0 {
			f.doc.del("hooks")
		}
	}
	if !removed {
		res.Notes = append(res.Notes, fmt.Sprintf("No narc %s hooks found in %s.", s.display, path))
		return res, nil
	}
	_, err = f.save(o, &res)
	return res, err
}

// removeOurs removes narc's handlers from an event's list and returns the
// position of the first entry it touched (-1 if none), where a fresh
// entry should go so re-installing keeps the order. Groups left without
// handlers are dropped.
func (s *settingsInstaller) removeOurs(arr []any, o InstallOptions) ([]any, int) {
	at := -1
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(*jsonObject)
		if !ok {
			out = append(out, item)
			continue
		}
		if !s.nested {
			if s.ours(obj, o) {
				if at < 0 {
					at = len(out)
				}
				continue
			}
			out = append(out, item)
			continue
		}
		handlers, ok := obj.vals["hooks"].([]any)
		if !ok {
			out = append(out, item)
			continue
		}
		kept := make([]any, 0, len(handlers))
		for _, h := range handlers {
			if ho, ok := h.(*jsonObject); ok && s.ours(ho, o) {
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) == len(handlers) {
			out = append(out, item)
			continue
		}
		if at < 0 {
			at = len(out)
		}
		if len(kept) > 0 {
			obj.set("hooks", kept)
			out = append(out, obj)
		}
	}
	return out, at
}

// ours reports whether a handler was installed by narc.
func (s *settingsInstaller) ours(h *jsonObject, o InstallOptions) bool {
	if s.harness == "gemini" && strings.HasPrefix(h.str("name"), "narc-") {
		return true
	}
	return isNarcCommand(h.str("command"), s.harness, o)
}

// jsonFile is a JSON config file loaded for editing.
type jsonFile struct {
	path     string
	exists   bool
	raw      []byte
	before   []byte // the document as parsed, re-encoded (to detect changes)
	doc      *jsonObject
	comments bool // the file had comments, which re-encoding drops
}

func loadJSONFile(path string, allowComments bool) (*jsonFile, error) {
	raw, exists, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	f := &jsonFile{path: path, exists: exists, raw: raw}
	src := raw
	if allowComments {
		src, f.comments = stripJSONComments(raw)
	}
	if f.doc, err = parseObject(src); err != nil {
		return nil, fmt.Errorf("%s: %w (fix the file and retry)", path, err)
	}
	f.before = encodeDocument(f.doc)
	return f, nil
}

// save writes the document when it changed (backing up the original the
// first time) and reports whether it did, or would with DryRun.
func (f *jsonFile) save(o InstallOptions, res *Result) (bool, error) {
	out := encodeDocument(f.doc)
	if f.exists && bytes.Equal(out, f.before) {
		return false, nil
	}
	res.Files = append(res.Files, f.path)
	if o.DryRun {
		return true, nil
	}
	if f.exists && len(bytes.TrimSpace(f.raw)) > 0 {
		if err := backup(f.path, f.raw, res); err != nil {
			return true, err
		}
	}
	if f.comments {
		res.Notes = append(res.Notes, "Comments in "+f.path+" could not be kept.")
	}
	return true, writeFile(f.path, out, 0o644)
}

func claudeFiles(o InstallOptions) []string {
	var out []string
	if o.RepoRoot != "" {
		out = append(out, filepath.Join(o.RepoRoot, ".claude", "settings.json"), filepath.Join(o.RepoRoot, ".claude", "settings.local.json"))
	}
	if o.Home != "" {
		out = append(out, filepath.Join(o.Home, ".claude", "settings.json"))
	}
	return out
}

func cursorFiles(o InstallOptions) []string {
	var out []string
	if o.RepoRoot != "" {
		out = append(out, filepath.Join(o.RepoRoot, ".cursor", "hooks.json"))
	}
	if o.Home != "" {
		out = append(out, filepath.Join(o.Home, ".cursor", "hooks.json"))
	}
	return out
}

// installedElsewhere returns the first file that runs narc's hooks for
// harness, or "".
func installedElsewhere(harness string, files []string) string {
	for _, p := range files {
		if fileContains(p, " hook "+harness+" ") {
			return p
		}
	}
	return ""
}

var codexHooksOff = regexp.MustCompile(`(?m)^\s*(?:codex_)?hooks\s*=\s*false\b`)

func hooksDisabledInCodex(configPath string) bool {
	b, err := os.ReadFile(configPath)
	return err == nil && codexHooksOff.Match(b)
}
