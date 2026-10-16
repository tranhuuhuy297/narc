package hook

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// samePath compares paths after resolving symlinks (macOS temp dirs live
// under /var, a symlink to /private/var; git reports the resolved path).
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func mustInstall(t *testing.T, harness string, o InstallOptions) Result {
	t.Helper()
	res, err := Install(harness, o)
	if err != nil {
		t.Fatalf("Install(%s): %v", harness, err)
	}
	return res
}

func mustUninstall(t *testing.T, harness string, o InstallOptions) Result {
	t.Helper()
	res, err := Uninstall(harness, o)
	if err != nil {
		t.Fatalf("Uninstall(%s): %v", harness, err)
	}
	return res
}

func notes(r Result) string { return strings.Join(r.Notes, "\n") }

const claudeFresh = `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude pre-tool-use",
            "timeout": 30
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude stop",
            "timeout": 120
          }
        ]
      }
    ],
    "SubagentStop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude subagent-stop",
            "timeout": 120
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude session-start",
            "timeout": 60
          }
        ]
      }
    ]
  }
}
`

func TestInstallClaudeFresh(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	res := mustInstall(t, "claude", InstallOptions{RepoRoot: root})
	if !reflect.DeepEqual(res.Files, []string{path}) {
		t.Fatalf("Files = %v", res.Files)
	}
	if got := readFile(t, path); got != claudeFresh {
		t.Fatalf("settings.json:\n%s", got)
	}
	wantContains(t, "notes", notes(res), "restart running sessions")
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("a backup was written for a file that did not exist")
	}
}

func TestInstallClaudeMergesAndUninstalls(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	// 4-space indent, a user hook sharing a group with an older narc entry.
	original := `{
    "$schema": "https://json.schemastore.org/claude-code-settings.json",
    "permissions": {"allow": ["Bash(npm test:*)"], "deny": []},
    "hooks": {
        "PreToolUse": [
            {"matcher": "Bash", "hooks": [{"type": "command", "command": "./scripts/guard-bash.sh"}]},
            {"matcher": "Edit|Write", "hooks": [
                {"type": "command", "command": "prettier --check \"$CLAUDE_PROJECT_DIR\""},
                {"type": "command", "command": "/usr/local/bin/narc hook claude pre-tool-use"}
            ]}
        ],
        "PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "npx eslint --fix"}]}]
    },
    "env": {"GREETING": "<hi> & bye"},
    "model": "opus",
    "cleanupPeriodDays": 30
}
`
	writeTestFile(t, path, original)
	res := mustInstall(t, "claude", InstallOptions{RepoRoot: root})
	want := `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "permissions": {
    "allow": [
      "Bash(npm test:*)"
    ],
    "deny": []
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "./scripts/guard-bash.sh"
          }
        ]
      },
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude pre-tool-use",
            "timeout": 30
          }
        ]
      },
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "prettier --check \"$CLAUDE_PROJECT_DIR\""
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Edit",
        "hooks": [
          {
            "type": "command",
            "command": "npx eslint --fix"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude stop",
            "timeout": 120
          }
        ]
      }
    ],
    "SubagentStop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude subagent-stop",
            "timeout": 120
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook claude session-start",
            "timeout": 60
          }
        ]
      }
    ]
  },
  "env": {
    "GREETING": "<hi> & bye"
  },
  "model": "opus",
  "cleanupPeriodDays": 30
}
`
	if got := readFile(t, path); got != want {
		t.Fatalf("merged settings:\n%s", got)
	}
	if got := readFile(t, path+".bak"); got != original {
		t.Fatalf("backup:\n%s", got)
	}
	wantContains(t, "notes", notes(res), "Saved the previous version as "+path+".bak")

	// Idempotent: nothing to write the second time.
	res = mustInstall(t, "claude", InstallOptions{RepoRoot: root})
	if len(res.Files) != 0 || readFile(t, path) != want {
		t.Fatalf("second install changed something: %v", res.Files)
	}
	wantContains(t, "notes", notes(res), "already up to date")

	// A different command replaces narc's entries in place.
	mustInstall(t, "claude", InstallOptions{RepoRoot: root, Command: "/opt/narc tools/narc"})
	got := readFile(t, path)
	if strings.Count(got, "hook claude pre-tool-use") != 1 || !strings.Contains(got, `"'/opt/narc tools/narc' hook claude stop"`) {
		t.Fatalf("reinstall with another command:\n%s", got)
	}
	if readFile(t, path+".bak") != original {
		t.Fatal("the backup was overwritten")
	}

	// Uninstall removes narc's entries and nothing else.
	res = mustUninstall(t, "claude", InstallOptions{RepoRoot: root})
	if !reflect.DeepEqual(res.Files, []string{path}) {
		t.Fatalf("Files = %v", res.Files)
	}
	got = readFile(t, path)
	wantNotContains(t, "settings after uninstall", got, "narc", "Stop", "SessionStart")
	wantContains(t, "settings after uninstall", got, "./scripts/guard-bash.sh", "prettier --check", "npx eslint --fix", `"GREETING": "<hi> & bye"`, `"cleanupPeriodDays": 30`)
	res = mustUninstall(t, "claude", InstallOptions{RepoRoot: root})
	if len(res.Files) != 0 {
		t.Fatalf("second uninstall wrote %v", res.Files)
	}
	wantContains(t, "notes", notes(res), "No narc Claude Code hooks found")
}

func TestUninstallDropsEmptyHooks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "settings.json")
	writeTestFile(t, path, "{\n  \"model\": \"sonnet\"\n}\n")
	mustInstall(t, "claude", InstallOptions{RepoRoot: root})
	mustUninstall(t, "claude", InstallOptions{RepoRoot: root})
	if got := readFile(t, path); got != "{\n  \"model\": \"sonnet\"\n}\n" {
		t.Fatalf("after install+uninstall:\n%s", got)
	}
}

func TestInstallClaudeNotesDisabledHooks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".claude", "settings.local.json"), "{\"disableAllHooks\": true}\n")
	res := mustInstall(t, "claude", InstallOptions{RepoRoot: root, Scope: ScopeLocal})
	wantContains(t, "notes", notes(res), "disableAllHooks is true")
}

func TestInstallScopes(t *testing.T) {
	t.Parallel()
	root, home := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		harness, scope, want string
	}{
		{"claude", ScopeLocal, filepath.Join(root, ".claude", "settings.local.json")},
		{"claude", ScopeUser, filepath.Join(home, ".claude", "settings.json")},
		{"codex", ScopeProject, filepath.Join(root, ".codex", "hooks.json")},
		{"codex", ScopeUser, filepath.Join(home, ".codex", "hooks.json")},
		{"gemini", ScopeUser, filepath.Join(home, ".gemini", "settings.json")},
		{"cursor", ScopeUser, filepath.Join(home, ".cursor", "hooks.json")},
		{"opencode", ScopeUser, filepath.Join(home, ".config", "opencode", "plugins", "narc.ts")},
	} {
		res := mustInstall(t, tc.harness, InstallOptions{Scope: tc.scope, RepoRoot: root, Home: home, DryRun: true})
		if !reflect.DeepEqual(res.Files, []string{tc.want}) {
			t.Errorf("%s %s: Files = %v, want %s", tc.harness, tc.scope, res.Files, tc.want)
		}
		if _, err := os.Stat(tc.want); !os.IsNotExist(err) {
			t.Errorf("%s %s: dry run wrote %s", tc.harness, tc.scope, tc.want)
		}
	}
	for _, tc := range []struct {
		harness string
		o       InstallOptions
		err     string
	}{
		{"codex", InstallOptions{Scope: ScopeLocal, RepoRoot: root}, "Codex has no local scope"},
		{"cursor", InstallOptions{Scope: ScopeLocal, RepoRoot: root}, "Cursor has no local scope"},
		{"git", InstallOptions{Scope: ScopeUser, Home: home}, "per repository"},
		{"claude", InstallOptions{Scope: "global", RepoRoot: root}, "unknown scope"},
		{"claude", InstallOptions{}, "project directory is unknown"},
		{"claude", InstallOptions{Scope: ScopeUser}, "home directory is unknown"},
		{"vscode", InstallOptions{RepoRoot: root}, "unknown harness"},
	} {
		if _, err := Install(tc.harness, tc.o); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s %+v: err = %v, want %q", tc.harness, tc.o, err, tc.err)
		}
	}
}

func TestInstallRefusesBrokenSettings(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"invalid JSON":           "{\n  \"hooks\": {\n",
		"hooks is not an object": "{\"hooks\": []}\n",
		"event is not an array":  "{\"hooks\": {\"Stop\": {\"command\": \"x\"}}}\n",
		"top level is an array":  "[]\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, ".claude", "settings.json")
			writeTestFile(t, path, content)
			if _, err := Install("claude", InstallOptions{RepoRoot: root}); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("err = %v, want an error naming %s", err, path)
			}
			if readFile(t, path) != content {
				t.Fatal("the broken file was modified")
			}
		})
	}
}

func TestInstallCodex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	res := mustInstall(t, "codex", InstallOptions{RepoRoot: root, Home: t.TempDir()})
	want := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^apply_patch$",
        "hooks": [
          {
            "type": "command",
            "command": "narc hook codex pre-tool-use",
            "timeout": 30,
            "statusMessage": "narc: checking the edit"
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook codex stop",
            "timeout": 120,
            "statusMessage": "narc: checking this session's changes"
          }
        ]
      }
    ],
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "narc hook codex session-start",
            "timeout": 60,
            "statusMessage": "narc: recording the session baseline"
          }
        ]
      }
    ]
  }
}
`
	if got := readFile(t, filepath.Join(root, ".codex", "hooks.json")); got != want {
		t.Fatalf("hooks.json:\n%s", got)
	}
	wantContains(t, "notes", notes(res), "approve narc's hooks in /hooks", "trusted projects")

	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-5.5\"\n\n[features]\nhooks = false\n")
	res = mustInstall(t, "codex", InstallOptions{Scope: ScopeUser, Home: home})
	wantContains(t, "notes", notes(res), "Hooks are turned off in ~/.codex/config.toml")
}

func TestInstallGemini(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".gemini", "settings.json")
	writeTestFile(t, path, `// Project settings for Gemini CLI
{
  "ui": {"theme": "GitHub"}, // keep the light theme
  /* hooks are reviewed in CI */
  "hooksConfig": {"enabled": false, "disabled": ["narc-after-agent"]},
  "hooks": {
    "BeforeTool": [
      {"matcher": "run_shell_command", "hooks": [{"name": "audit-shell", "type": "command", "command": "./audit.sh // not a comment"}]}
    ]
  }
}
`)
	res := mustInstall(t, "gemini", InstallOptions{RepoRoot: root})
	got := readFile(t, path)
	wantContains(t, "settings.json", got,
		`"theme": "GitHub"`,
		`"command": "./audit.sh // not a comment"`,
		"\"matcher\": \"^(write_file|replace)$\",\n        \"hooks\": [\n          {\n            \"name\": \"narc-before-tool\",\n            \"type\": \"command\",\n            \"command\": \"narc hook gemini before-tool\",\n            \"timeout\": 30000\n",
		`"name": "narc-after-agent"`, `"command": "narc hook gemini after-agent"`, `"timeout": 120000`,
		`"name": "narc-session-start"`)
	wantContains(t, "notes", notes(res), "hooksConfig.enabled is false", `lists "narc-after-agent"`, "Comments in "+path+" could not be kept", "/hooks")

	// Uninstall finds narc's hooks by name even if the command was edited.
	edited := strings.Replace(got, `"narc hook gemini after-agent"`, `"$HOME/bin/narc-dev check"`, 1)
	writeTestFile(t, path, edited)
	mustUninstall(t, "gemini", InstallOptions{RepoRoot: root})
	got = readFile(t, path)
	wantNotContains(t, "settings.json", got, `"name": "narc-`, "narc hook gemini", "narc-dev", "AfterAgent", "SessionStart")
	// The user's own settings stay, including the entry naming narc's hook.
	wantContains(t, "settings.json", got, "audit-shell", `"disabled": [`+"\n"+`      "narc-after-agent"`)
}

func TestInstallCursor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".cursor", "hooks.json")
	writeTestFile(t, path, `{"hooks": {"stop": [{"command": "./scripts/notify.sh"}], "afterFileEdit": [{"command": "prettier --write"}]}}`)
	res := mustInstall(t, "cursor", InstallOptions{RepoRoot: root})
	want := `{
  "version": 1,
  "hooks": {
    "stop": [
      {
        "command": "./scripts/notify.sh"
      },
      {
        "command": "narc hook cursor stop",
        "timeout": 120
      }
    ],
    "afterFileEdit": [
      {
        "command": "prettier --write"
      }
    ],
    "preToolUse": [
      {
        "command": "narc hook cursor pre-tool-use",
        "matcher": "Write|StrReplace|Edit|Delete",
        "timeout": 30
      }
    ],
    "sessionStart": [
      {
        "command": "narc hook cursor session-start",
        "timeout": 60
      }
    ]
  }
}
`
	if got := readFile(t, path); got != want {
		t.Fatalf("hooks.json:\n%s", got)
	}
	wantContains(t, "notes", notes(res), "Cursor also runs Claude Code hooks", "runs narc twice")

	// With narc's Claude Code hooks installed, the warning names them.
	mustInstall(t, "claude", InstallOptions{RepoRoot: root})
	res = mustInstall(t, "cursor", InstallOptions{RepoRoot: root})
	wantContains(t, "notes", notes(res), "narc's Claude Code hooks are installed in "+filepath.Join(root, ".claude", "settings.json"), "will run narc twice")
	wantContains(t, "claude notes", notes(mustInstall(t, "claude", InstallOptions{RepoRoot: root})), "Cursor would run narc twice")

	mustUninstall(t, "cursor", InstallOptions{RepoRoot: root})
	got := readFile(t, path)
	wantNotContains(t, "hooks.json", got, "narc", "preToolUse", "sessionStart")
	wantContains(t, "hooks.json", got, `"version": 1`, "./scripts/notify.sh", "prettier --write")
}

func TestInstallOpenCode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".opencode", "plugins", "narc.ts")
	res := mustInstall(t, "opencode", InstallOptions{RepoRoot: root})
	if !reflect.DeepEqual(res.Files, []string{path}) {
		t.Fatalf("Files = %v", res.Files)
	}
	src := readFile(t, path)
	wantContains(t, "plugin", src, "narc:generated", `const NARC = "narc"`, `"tool.execute.before"`, `"session.idle"`, `spawn(NARC, ["hook", "opencode", event]`)
	wantNotContains(t, "plugin", src, "__NARC_COMMAND__")
	wantContains(t, "notes", notes(res), "restart OpenCode")

	if res := mustInstall(t, "opencode", InstallOptions{RepoRoot: root}); len(res.Files) != 0 {
		t.Fatalf("second install wrote %v", res.Files)
	}
	mustInstall(t, "opencode", InstallOptions{RepoRoot: root, Command: `/Users/me/bin/narc "dev"`})
	wantContains(t, "plugin", readFile(t, path), `const NARC = "/Users/me/bin/narc \"dev\""`)

	res = mustUninstall(t, "opencode", InstallOptions{RepoRoot: root})
	if _, err := os.Stat(path); !os.IsNotExist(err) || len(res.Files) != 1 {
		t.Fatalf("uninstall left the plugin (Files %v)", res.Files)
	}

	// A plugin narc did not write is never touched.
	writeTestFile(t, path, "export const Mine = async () => ({})\n")
	if _, err := Install("opencode", InstallOptions{RepoRoot: root}); err == nil || !strings.Contains(err.Error(), "not generated by narc") {
		t.Fatalf("err = %v", err)
	}
	wantContains(t, "notes", notes(mustUninstall(t, "opencode", InstallOptions{RepoRoot: root})), "left it alone")
	if readFile(t, path) != "export const Mine = async () => ({})\n" {
		t.Fatal("a foreign plugin was modified")
	}
}

const gitHookFresh = "#!/bin/sh\n" +
	"# >>> narc >>> managed by `narc install git`; `narc uninstall git` removes this block\n" +
	"if command -v narc >/dev/null 2>&1; then\n" +
	"\tnarc hook git pre-commit || exit $?\n" +
	"else\n" +
	"\techo \"narc is not installed or not on PATH; skipping its pre-commit check\" >&2\n" +
	"fi\n" +
	"# <<< narc <<<\n"

func TestInstallGit(t *testing.T) {
	t.Parallel()
	t.Run("fresh hook", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		hook := r.path(".git/hooks/pre-commit")
		res := mustInstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if len(res.Files) != 1 || !samePath(res.Files[0], hook) || readFile(t, hook) != gitHookFresh {
			t.Fatalf("Files %v, hook:\n%s", res.Files, readFile(t, hook))
		}
		if st, _ := os.Stat(hook); st.Mode().Perm()&0o111 != 0o111 {
			t.Fatalf("hook mode %v is not executable", st.Mode())
		}
		if res := mustInstall(t, "git", InstallOptions{RepoRoot: r.dir}); len(res.Files) != 0 {
			t.Fatalf("second install wrote %v", res.Files)
		}
		mustUninstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if _, err := os.Stat(hook); !os.IsNotExist(err) {
			t.Fatal("uninstall left narc's own hook file")
		}
	})

	t.Run("existing shell hook is chained and restored", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		hook := r.path(".git/hooks/pre-commit")
		original := "#!/usr/bin/env bash\nset -e\nnpm run lint\n"
		writeExecutable(t, hook, original)
		res := mustInstall(t, "git", InstallOptions{RepoRoot: r.dir, Command: "/opt/my tools/narc"})
		got := readFile(t, hook)
		if !strings.HasPrefix(got, "#!/usr/bin/env bash\n# >>> narc >>>") || !strings.HasSuffix(got, "# <<< narc <<<\nset -e\nnpm run lint\n") {
			t.Fatalf("chained hook:\n%s", got)
		}
		wantContains(t, "hook", got, "if command -v '/opt/my tools/narc' >/dev/null", "'/opt/my tools/narc' hook git pre-commit || exit $?")
		wantContains(t, "notes", notes(res), "Added narc to the existing hook")
		mustInstall(t, "git", InstallOptions{RepoRoot: r.dir, Command: "narc"})
		if got := readFile(t, hook); strings.Count(got, gitBlockBegin) != 1 || !strings.Contains(got, "\tnarc hook git pre-commit") {
			t.Fatalf("reinstall:\n%s", got)
		}
		mustUninstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if got := readFile(t, hook); got != original {
			t.Fatalf("uninstall did not restore the hook:\n%s", got)
		}
	})

	t.Run("non-shell hook is refused", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		hook := r.path(".git/hooks/pre-commit")
		original := "#!/usr/bin/env python3\nimport sys\nsys.exit(0)\n"
		writeExecutable(t, hook, original)
		_, err := Install("git", InstallOptions{RepoRoot: r.dir})
		if err == nil || !strings.Contains(err.Error(), "not a shell script") || !strings.Contains(err.Error(), "narc hook git pre-commit") {
			t.Fatalf("err = %v", err)
		}
		if readFile(t, hook) != original {
			t.Fatal("the hook was modified")
		}
	})

	t.Run("pre-commit framework hook is refused", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		hook := r.path(".git/hooks/pre-commit")
		writeExecutable(t, hook, "#!/usr/bin/env bash\n# File generated by pre-commit: https://pre-commit.com\nexec python -mpre_commit hook-impl\n")
		_, err := Install("git", InstallOptions{RepoRoot: r.dir})
		if err == nil || !strings.Contains(err.Error(), ".pre-commit-config.yaml") || !strings.Contains(err.Error(), "entry: narc hook git pre-commit") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("core.hooksPath is honored", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		r.git("config", "core.hooksPath", ".githooks")
		res := mustInstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if readFile(t, r.path(".githooks/pre-commit")) != gitHookFresh {
			t.Fatal("hook not written to core.hooksPath")
		}
		wantContains(t, "notes", notes(res), "core.hooksPath is set")
	})

	t.Run("husky", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		r.write(".husky/pre-commit", "npx lint-staged\n")
		r.git("config", "core.hooksPath", ".husky/_")
		res := mustInstall(t, "git", InstallOptions{RepoRoot: r.dir})
		got := r.read(".husky/pre-commit")
		if !strings.HasPrefix(got, gitBlockBegin) || !strings.HasSuffix(got, gitBlockEnd+"\nnpx lint-staged\n") {
			t.Fatalf(".husky/pre-commit:\n%s", got)
		}
		wantContains(t, "notes", notes(res), "husky manages this repository's hooks")
		mustUninstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if got := r.read(".husky/pre-commit"); got != "npx lint-staged\n" {
			t.Fatalf("after uninstall:\n%s", got)
		}
	})

	t.Run("lost executable bit is restored", func(t *testing.T) {
		r := newRepo(t, baseFiles)
		hook := r.path(".git/hooks/pre-commit")
		mustInstall(t, "git", InstallOptions{RepoRoot: r.dir})
		if err := os.Chmod(hook, 0o644); err != nil {
			t.Fatal(err)
		}
		if res := mustInstall(t, "git", InstallOptions{RepoRoot: r.dir}); len(res.Files) != 1 {
			t.Fatalf("Files = %v", res.Files)
		}
		if st, _ := os.Stat(hook); st.Mode().Perm()&0o111 != 0o111 {
			t.Fatalf("hook mode %v", st.Mode())
		}
	})

	t.Run("not a repository", func(t *testing.T) {
		if _, err := Install("git", InstallOptions{RepoRoot: t.TempDir()}); err == nil {
			t.Fatal("want an error outside a git repository")
		}
	})
}

func TestIsNarcCommand(t *testing.T) {
	t.Parallel()
	o := InstallOptions{Command: "/opt/bin/narc-dev"}
	for cmd, want := range map[string]bool{
		"narc hook claude stop":                      true,
		"/usr/local/bin/narc hook claude stop":       true,
		"'/Users/me/my tools/narc' hook claude stop": true,
		`"C:\tools\narc.exe" hook claude stop`:       true,
		"npx narc hook claude pre-tool-use":          true,
		"/opt/bin/narc-dev hook claude stop":         true, // the configured command
		"narc hook codex stop":                       false,
		"narc scan --staged":                         false,
		"mynarc hook claude stop":                    false,
		"echo narc hook claude stop is great":        true, // indistinguishable; never installed by users
		"./scripts/guard.sh":                         false,
	} {
		if got := isNarcCommand(cmd, "claude", o); got != want {
			t.Errorf("isNarcCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestStripJSONComments(t *testing.T) {
	t.Parallel()
	in := "{\n  // line\n  \"a\": \"x // y /* z */\", /* block\n spanning */ \"b\": \"q\\\"//\"\n}\n"
	out, found := stripJSONComments([]byte(in))
	if !found {
		t.Fatal("comments not detected")
	}
	doc, err := parseObject(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if doc.str("a") != "x // y /* z */" || doc.str("b") != `q"//` {
		t.Fatalf("values changed: %q %q", doc.str("a"), doc.str("b"))
	}
	if _, found := stripJSONComments([]byte(`{"url": "https://example.com"}`)); found {
		t.Fatal("a URL is not a comment")
	}
}
