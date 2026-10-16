// Command narc catches AI coding agents cheating on verification: skipped
// tests, deleted assertions, `|| true` in CI, lowered coverage, type and
// lint escapes, and code that special-cases the tests.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// Set by the release build (-ldflags "-X main.version=...").
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// exit codes
const (
	exitOK       = 0
	exitFindings = 1
	exitError    = 2
)

type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		return c.scan(nil)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "scan":
		return c.scan(rest)
	case "audit":
		return c.audit(rest)
	case "hook":
		return c.hook(rest)
	case "install":
		return c.install(rest, false)
	case "uninstall":
		return c.install(rest, true)
	case "rules":
		return c.rules(rest)
	case "explain":
		return c.explain(rest)
	case "init":
		return c.initConfig(rest)
	case "version", "--version", "-v":
		c.printVersion()
		return exitOK
	case "help", "--help", "-h":
		c.usage(stdout)
		return exitOK
	}
	if strings.HasPrefix(cmd, "-") || strings.Contains(cmd, "..") {
		// `narc --staged`, `narc main...HEAD`: treat as scan.
		return c.scan(args)
	}
	fmt.Fprintf(stderr, "narc: unknown command %q\n\n", cmd)
	c.usage(stderr)
	return exitError
}

func (c *cli) usage(w io.Writer) {
	fmt.Fprint(w, `narc — catch AI coding agents cheating on your tests.

Usage:
  narc [scan] [flags] [RANGE]        scan changes (default: working tree vs HEAD)
  narc audit [flags]                 rap sheet: which agents weakened verification in git history
  narc install <harness> [flags]     guard an agent live: claude, codex, gemini, cursor, opencode, git, github
  narc uninstall <harness> [flags]   remove narc's hooks
  narc rules                         list rules
  narc explain <rule>                explain a rule with examples
  narc init                          write a .narc.yml
  narc version

Scan examples:
  narc                               uncommitted changes (staged, unstaged, untracked)
  narc --staged                      what you are about to commit
  narc --base main                   what a PR against main contains
  narc HEAD~5..HEAD                  a commit range
  git diff | narc --patch -          any unified diff

Run 'narc <command> -h' for flags. Docs: https://github.com/huyth45/narc
`)
}

func (c *cli) printVersion() {
	v, cm := version, commit
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				v = bi.Main.Version
			}
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && cm == "" && len(s.Value) >= 7 {
					cm = s.Value[:7]
				}
			}
		}
	}
	fmt.Fprintf(c.stdout, "narc %s", v)
	if cm != "" {
		fmt.Fprintf(c.stdout, " (%s", cm)
		if date != "" {
			fmt.Fprintf(c.stdout, ", %s", date)
		}
		fmt.Fprint(c.stdout, ")")
	}
	fmt.Fprintf(c.stdout, " %s/%s %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
}

func versionString() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

// newFlagSet returns a flag set that prints errors to stderr and supports
// long-style --flags (the flag package accepts both - and --).
func (c *cli) newFlagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() {
		fmt.Fprintf(c.stderr, "Usage: %s\n\nFlags:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags parses flags, allowing them to appear after positional
// arguments (`narc scan main..HEAD --format json`).
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func (c *cli) fail(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	fmt.Fprintf(c.stderr, "narc: %v\n", err)
	return exitError
}
