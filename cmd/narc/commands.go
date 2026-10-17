package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/git"
	"github.com/tranhuuhuy297/narc/internal/hook"
	"github.com/tranhuuhuy297/narc/internal/report"
)

// ---- narc hook <harness> <event> ----

func (c *cli) hook(args []string) int {
	if len(args) < 2 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(c.stderr, "Usage: narc hook <claude|codex|gemini|cursor|opencode|git> <event>\n\nCalled by agent hooks; reads the hook payload as JSON on stdin. Use `narc install <harness>` to set it up.")
		if len(args) < 2 {
			return exitError
		}
		return exitOK
	}
	return hook.Run(args[0], args[1], c.stdin, c.stdout, c.stderr, hook.Env{
		Getwd:   os.Getwd,
		Now:     time.Now,
		Version: versionString(),
	})
}

// ---- narc install / uninstall ----

var harnesses = []string{"claude", "codex", "gemini", "cursor", "opencode", "git", "github"}

func (c *cli) install(args []string, uninstall bool) int {
	verb := "install"
	if uninstall {
		verb = "uninstall"
	}
	fs := c.newFlagSet(verb, "narc "+verb+" <"+strings.Join(harnesses, "|")+"> [flags]")
	user := fs.Bool("user", false, "install for your user account (~/.claude, ~/.codex, ...) instead of this project")
	local := fs.Bool("local", false, "install into the project's untracked local settings (Claude Code: .claude/settings.local.json)")
	dry := fs.Bool("dry-run", false, "show what would change without writing")
	command := fs.String("command", "", "how hooks should invoke narc (default: \"narc\" if on PATH, else the absolute path)")
	dirFlag := fs.String("C", "", "run as if started in `DIR`")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return c.fail(err)
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitError
	}
	h := strings.ToLower(pos[0])
	if h == "claude-code" {
		h = "claude"
	}
	known := false
	for _, k := range harnesses {
		known = known || k == h
	}
	if !known {
		return c.fail(fmt.Errorf("unknown harness %q (want one of %s)", h, strings.Join(harnesses, ", ")))
	}
	dir, err := workDir(*dirFlag)
	if err != nil {
		return c.fail(err)
	}
	root := dir
	if r, err := git.Open(dir); err == nil {
		root = r.Root
		r.Close()
	} else if !*user {
		return c.fail(fmt.Errorf("%v — or use --user to install globally", err))
	}

	if h == "github" {
		return c.installGitHub(root, uninstall, *dry)
	}

	scope := "project"
	switch {
	case *user && *local:
		return c.fail(fmt.Errorf("choose either --user or --local"))
	case *user:
		scope = "user"
	case *local:
		scope = "local"
	}
	home, _ := os.UserHomeDir()
	cmd := *command
	if cmd == "" {
		cmd = narcCommand(scope)
		if filepath.IsAbs(cmd) && scope == "project" {
			fmt.Fprintf(c.stderr, "narc: note: narc is not on PATH, so the project hook uses %s; teammates need narc installed (or pass --command)\n", cmd)
		}
	}
	opts := hook.InstallOptions{Scope: scope, RepoRoot: root, Home: home, Command: cmd, DryRun: *dry}
	var res hook.Result
	if uninstall {
		res, err = hook.Uninstall(h, opts)
	} else {
		res, err = hook.Install(h, opts)
	}
	if err != nil {
		return c.fail(err)
	}
	prefix := ""
	if *dry {
		prefix = "(dry run) "
	}
	for _, f := range res.Files {
		rel := f
		if r, err := filepath.Rel(root, f); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		fmt.Fprintf(c.stdout, "%s%s %s\n", prefix, map[bool]string{true: "updated", false: "wrote"}[uninstall], rel)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(c.stdout, "note: %s\n", n)
	}
	if !uninstall && !*dry {
		fmt.Fprintf(c.stdout, "narc is now guarding %s. Try it: ask the agent to \"make the failing test pass\" and watch.\n", h)
	}
	return exitOK
}

// narcCommand decides how hook configs should invoke narc: the bare name
// when narc is on PATH (portable, safe to commit), otherwise the absolute
// path of this binary.
func narcCommand(scope string) string {
	if _, err := exec.LookPath("narc"); err == nil {
		return "narc"
	}
	self, err := os.Executable()
	if err != nil || strings.Contains(self, "go-build") {
		return "narc"
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	return self
}

const githubWorkflow = `# narc: catch AI coding agents cheating on verification.
# https://github.com/tranhuuhuy297/narc
name: narc

on:
  pull_request:

permissions:
  contents: read
  pull-requests: write    # sticky PR comment
  security-events: write  # SARIF upload to code scanning

jobs:
  narc:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: tranhuuhuy297/narc@v1
        with:
          fail-on: error
`

func (c *cli) installGitHub(root string, uninstall, dry bool) int {
	path := filepath.Join(root, ".github", "workflows", "narc.yml")
	rel, _ := filepath.Rel(root, path)
	if uninstall {
		if dry {
			fmt.Fprintf(c.stdout, "(dry run) would remove %s\n", rel)
			return exitOK
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return c.fail(err)
		}
		fmt.Fprintf(c.stdout, "removed %s\n", rel)
		return exitOK
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(c.stdout, "%s already exists; leaving it alone\n", rel)
		return exitOK
	}
	if dry {
		fmt.Fprintf(c.stdout, "(dry run) would write %s:\n\n%s", rel, githubWorkflow)
		return exitOK
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return c.fail(err)
	}
	if err := os.WriteFile(path, []byte(githubWorkflow), 0o644); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "wrote %s — commit it, and every PR gets a narc check, inline annotations and a summary comment.\n", rel)
	return exitOK
}

// ---- narc rules / explain ----

func (c *cli) rules(args []string) int {
	fs := c.newFlagSet("rules", "narc rules [--format table|markdown|json]")
	format := fs.String("format", "table", "output `format`: table, markdown, json")
	noColor := fs.Bool("no-color", false, "disable colors")
	if _, err := parseFlags(fs, args); err != nil {
		return c.fail(err)
	}
	rules := core.Rules()
	switch *format {
	case "markdown", "md":
		if err := report.RulesMarkdown(c.stdout, rules); err != nil {
			return c.fail(err)
		}
	case "json":
		type jr struct {
			ID, Title, Category, Severity, Summary, Hint string
		}
		var out []jr
		for _, r := range rules {
			out = append(out, jr{r.ID, r.Title, r.Category, r.Severity.String(), r.Summary, r.Hint})
		}
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return c.fail(err)
		}
	default:
		o := report.Options{Width: termWidth()}
		if f, ok := c.stdout.(*os.File); ok {
			o.Color = !*noColor && report.ColorEnabled(f)
		}
		if err := report.RulesTable(c.stdout, rules, o); err != nil {
			return c.fail(err)
		}
	}
	return exitOK
}

func (c *cli) explain(args []string) int {
	fs := c.newFlagSet("explain", "narc explain <rule-id>")
	noColor := fs.Bool("no-color", false, "disable colors")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return c.fail(err)
	}
	if len(pos) != 1 {
		fs.Usage()
		return exitError
	}
	r := core.RuleByID(pos[0])
	if r == nil {
		var ids []string
		for _, x := range core.Rules() {
			if strings.Contains(x.ID, pos[0]) {
				ids = append(ids, x.ID)
			}
		}
		msg := fmt.Sprintf("unknown rule %q", pos[0])
		if len(ids) > 0 {
			msg += " (did you mean " + strings.Join(ids, ", ") + "?)"
		}
		return c.fail(fmt.Errorf("%s", msg))
	}
	o := report.Options{Width: termWidth()}
	if f, ok := c.stdout.(*os.File); ok {
		o.Color = !*noColor && report.ColorEnabled(f)
	}
	if err := report.Explain(c.stdout, r, o); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// ---- narc init ----

func (c *cli) initConfig(args []string) int {
	fs := c.newFlagSet("init", "narc init [--force]")
	force := fs.Bool("force", false, "overwrite an existing config")
	dirFlag := fs.String("C", "", "run as if started in `DIR`")
	if _, err := parseFlags(fs, args); err != nil {
		return c.fail(err)
	}
	dir, err := workDir(*dirFlag)
	if err != nil {
		return c.fail(err)
	}
	if r, err := git.Open(dir); err == nil {
		dir = r.Root
		r.Close()
	}
	for _, n := range config.FileNames {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil && !*force {
			return c.fail(fmt.Errorf("%s already exists (use --force to overwrite)", n))
		}
	}
	p := filepath.Join(dir, ".narc.yml")
	if err := os.WriteFile(p, []byte(config.Template), 0o644); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "wrote %s\n", p)
	return exitOK
}
