package hook

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Install scopes.
const (
	ScopeProject = "project" // shared project config (can be committed)
	ScopeLocal   = "local"   // personal project config (Claude Code only)
	ScopeUser    = "user"    // the user's home-directory config
)

// InstallOptions configures Install and Uninstall.
type InstallOptions struct {
	// Scope is ScopeProject (the default), ScopeLocal or ScopeUser. Only
	// Claude Code has a local scope. git hooks are always per repository
	// (project and local mean the same there).
	Scope string
	// RepoRoot is the project directory. Required for project and local
	// scope, and for git.
	RepoRoot string
	// Home is the user's home directory. Required for user scope.
	Home string
	// Command is the narc executable hooks run: a name on PATH or an
	// absolute path (quoted for the shell as needed). Default "narc".
	Command string
	// DryRun computes what would change without writing anything.
	DryRun bool
}

// Result describes what Install or Uninstall did.
type Result struct {
	// Files lists the files written or removed (that would be, with
	// DryRun). It is empty when everything was already up to date.
	Files []string
	// Notes are things the user should know or do next, e.g. trusting
	// the new hooks inside the agent.
	Notes []string
}

// installer installs and removes narc's hooks for one harness.
type installer interface {
	install(o InstallOptions) (Result, error)
	uninstall(o InstallOptions) (Result, error)
}

var installers = map[string]installer{
	"claude":   claudeSettings,
	"codex":    codexSettings,
	"gemini":   geminiSettings,
	"cursor":   cursorSettings,
	"opencode": openCodePlugin{},
	"git":      gitPreCommit{},
}

// Install adds narc's hooks for a harness ("claude", "codex", "gemini",
// "cursor", "opencode" or "git"). Existing settings and other hooks are
// kept, and running it again changes nothing.
func Install(harness string, opts InstallOptions) (Result, error) {
	in, opts, err := prepare(harness, opts)
	if err != nil {
		return Result{}, err
	}
	return in.install(opts)
}

// Uninstall removes narc's hooks for a harness and nothing else.
func Uninstall(harness string, opts InstallOptions) (Result, error) {
	in, opts, err := prepare(harness, opts)
	if err != nil {
		return Result{}, err
	}
	return in.uninstall(opts)
}

func prepare(harness string, o InstallOptions) (installer, InstallOptions, error) {
	h := normalizeHarness(harness)
	in, ok := installers[h]
	if !ok {
		return nil, o, fmt.Errorf("unknown harness %q (want one of: claude, codex, cursor, gemini, git, opencode)", harness)
	}
	if o.Scope == "" {
		o.Scope = ScopeProject
	}
	if o.Command == "" {
		o.Command = "narc"
	}
	switch o.Scope {
	case ScopeProject, ScopeLocal:
		if o.RepoRoot == "" {
			return nil, o, errors.New("the project directory is unknown (RepoRoot is empty)")
		}
	case ScopeUser:
		if o.Home == "" {
			return nil, o, errors.New("the home directory is unknown (Home is empty)")
		}
	default:
		return nil, o, fmt.Errorf("unknown scope %q (want project, local or user)", o.Scope)
	}
	return in, o, nil
}

func scopeError(display, scope string, supported ...string) error {
	return fmt.Errorf("%s has no %s scope (supported: %s)", display, scope, strings.Join(supported, ", "))
}

// hookCommand is the shell command a hook runs.
func hookCommand(o InstallOptions, harness, event string) string {
	return shellQuote(o.Command) + " hook " + harness + " " + event
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,~-]+$`)

// shellQuote quotes s for a POSIX shell when it needs quoting.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// narcCommandRe matches a hook command that runs `narc hook <harness> ...`,
// however narc is spelled: narc, /usr/local/bin/narc, 'C:\x\narc.exe', ...
var narcCommandRe = regexp.MustCompile(`(?:^|[\s/\\'"])narc(?:\.exe)?['"]?\s+hook\s+([A-Za-z-]+)(?:\s|$)`)

// isNarcCommand reports whether cmd runs narc's hook for harness, either
// through a binary named narc or through the configured command.
func isNarcCommand(cmd, harness string, o InstallOptions) bool {
	if m := narcCommandRe.FindStringSubmatch(cmd); m != nil && normalizeHarness(m[1]) == harness {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(cmd), shellQuote(o.Command)+" hook "+harness+" ")
}

// writeFile writes data atomically, keeping the mode of an existing file.
// A symlinked file (dotfile managers link settings files) is written
// through the link, which stays in place.
func writeFile(path string, data []byte, mode fs.FileMode) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".narc-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// backup saves the original content of path to path.bak the first time
// narc changes it. An existing .bak is never overwritten.
func backup(path string, original []byte, res *Result) error {
	bak := path + ".bak"
	if _, err := os.Stat(bak); err == nil {
		return nil
	}
	if err := os.WriteFile(bak, original, 0o600); err != nil {
		return fmt.Errorf("backing up %s: %w", path, err)
	}
	res.Notes = append(res.Notes, "Saved the previous version as "+bak+".")
	return nil
}

// readIfExists reads a file, reporting whether it exists.
func readIfExists(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// fileContains reports whether the file at path exists and contains s.
func fileContains(path, s string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.Contains(b, []byte(s))
}
