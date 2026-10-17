package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/engine"
	"github.com/tranhuuhuy297/narc/internal/git"
	"github.com/tranhuuhuy297/narc/internal/report"
	_ "github.com/tranhuuhuy297/narc/internal/rules/all"
)

type scanFlags struct {
	staged      bool
	base        string
	patch       string
	format      string
	failOn      string
	output      string
	sarifOut    string
	markdownOut string
	jsonOut     string
	noUntracked bool
	showWaived  bool
	noHints     bool
	noColor     bool
	quiet       bool
	dir         string
	configPath  string
}

func (c *cli) scan(args []string) int {
	var f scanFlags
	fs := c.newFlagSet("scan", "narc scan [flags] [RANGE | COMMIT]")
	fs.BoolVar(&f.staged, "staged", false, "scan staged changes (what you are about to commit)")
	fs.StringVar(&f.base, "base", "", "scan what a PR against `REF` contains (merge-base..HEAD)")
	fs.StringVar(&f.patch, "patch", "", "scan a unified diff from `FILE` (- for stdin)")
	fs.StringVar(&f.format, "format", "pretty", "output `format`: pretty, json, sarif, markdown, github")
	fs.StringVar(&f.failOn, "fail-on", "", "exit 1 at this `severity` or worse: error, warning, info, never (default from .narc.yml, else error)")
	fs.StringVar(&f.output, "output", "", "write the report to `FILE` instead of stdout")
	fs.StringVar(&f.output, "o", "", "shorthand for --output")
	fs.StringVar(&f.sarifOut, "sarif", "", "also write SARIF to `FILE`")
	fs.StringVar(&f.markdownOut, "markdown", "", "also write a Markdown report to `FILE`")
	fs.StringVar(&f.jsonOut, "json", "", "also write JSON to `FILE`")
	fs.BoolVar(&f.noUntracked, "no-untracked", false, "ignore untracked files when scanning the working tree")
	fs.BoolVar(&f.showWaived, "show-waived", false, "also list waived findings")
	fs.BoolVar(&f.noHints, "no-hints", false, "hide fix hints")
	fs.BoolVar(&f.noColor, "no-color", false, "disable colors")
	fs.BoolVar(&f.quiet, "quiet", false, "print nothing; only set the exit code")
	fs.BoolVar(&f.quiet, "q", false, "shorthand for --quiet")
	fs.StringVar(&f.dir, "C", "", "run as if started in `DIR`")
	fs.StringVar(&f.configPath, "config", "", "use this config `FILE` instead of .narc.yml")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return c.fail(err)
	}
	if len(pos) > 1 {
		return c.fail(fmt.Errorf("scan takes at most one RANGE or COMMIT, got %q", strings.Join(pos, " ")))
	}
	modes := 0
	for _, b := range []bool{f.staged, f.base != "", f.patch != "", len(pos) == 1} {
		if b {
			modes++
		}
	}
	if modes > 1 {
		return c.fail(fmt.Errorf("choose one of --staged, --base, --patch or a RANGE"))
	}

	dir, err := workDir(f.dir)
	if err != nil {
		return c.fail(err)
	}

	cs, root, closeRepo, err := c.changeset(dir, f, pos)
	if err != nil {
		return c.fail(err)
	}
	defer closeRepo()

	cfg, err := loadConfig(root, f.configPath)
	if err != nil {
		return c.fail(err)
	}
	for _, id := range cfg.UnknownRules() {
		fmt.Fprintf(c.stderr, "narc: warning: %s mentions unknown rule %q (see `narc rules`)\n", cfgName(cfg), id)
	}
	if f.failOn != "" {
		cfg.FailOn = f.failOn
		if _, err := config.Parse([]byte("fail_on: " + f.failOn)); err != nil {
			return c.fail(fmt.Errorf("--fail-on: %w", err))
		}
	}

	res := engine.Run(cs, engine.Options{Config: cfg})
	for _, e := range res.Errors {
		fmt.Fprintf(c.stderr, "narc: internal error (please report): %s\n", firstLine(e))
	}

	if !f.quiet {
		opts := report.Options{
			Width:      termWidth(),
			ShowWaived: f.showWaived,
			ShowHints:  !f.noHints,
			Version:    versionString(),
			Root:       root,
			Command:    "narc " + strings.Join(append([]string{"scan"}, args...), " "),
		}
		out := c.stdout
		var file *os.File
		if f.output != "" && f.output != "-" {
			file, err = os.Create(f.output)
			if err != nil {
				return c.fail(err)
			}
			defer file.Close()
			out = file
		} else if fo, ok := c.stdout.(*os.File); ok {
			opts.Color = !f.noColor && report.ColorEnabled(fo)
		}
		if err := render(out, f.format, res, opts); err != nil {
			return c.fail(err)
		}
		plain := opts
		plain.Color = false
		for _, extra := range []struct{ path, format string }{
			{f.sarifOut, "sarif"}, {f.markdownOut, "markdown"}, {f.jsonOut, "json"},
		} {
			if extra.path == "" {
				continue
			}
			var buf bytes.Buffer
			if err := render(&buf, extra.format, res, plain); err != nil {
				return c.fail(err)
			}
			if err := os.WriteFile(extra.path, buf.Bytes(), 0o644); err != nil {
				return c.fail(err)
			}
		}
	}

	if threshold, ok := cfg.FailThreshold(); ok && len(res.AtLeast(threshold)) > 0 {
		return exitFindings
	}
	return exitOK
}

func render(w io.Writer, format string, res *engine.Result, o report.Options) error {
	switch strings.ToLower(format) {
	case "pretty", "text", "":
		return report.Pretty(w, res, o)
	case "json":
		return report.JSON(w, res, o)
	case "sarif":
		return report.SARIF(w, res, core.Rules(), o)
	case "markdown", "md":
		return report.Markdown(w, res, o)
	case "github", "gha":
		return report.GitHub(w, res, o)
	}
	return fmt.Errorf("unknown format %q (want pretty, json, sarif, markdown or github)", format)
}

// changeset picks the changes to scan. It returns the root directory used
// for config lookup and a cleanup function.
func (c *cli) changeset(dir string, f scanFlags, pos []string) (*core.Changeset, string, func(), error) {
	noop := func() {}
	if f.patch != "" {
		var data []byte
		var err error
		if f.patch == "-" {
			data, err = io.ReadAll(c.stdin)
		} else {
			data, err = os.ReadFile(f.patch)
		}
		if err != nil {
			return nil, "", noop, err
		}
		files, err := diff.Parse(bytes.NewReader(data))
		if err != nil {
			return nil, "", noop, err
		}
		label := f.patch
		if f.patch == "-" {
			label = "stdin"
		}
		root := dir
		if r, err := git.Open(dir); err == nil {
			root = r.Root
			r.Close()
		}
		return &core.Changeset{Files: files, Label: "patch " + label}, root, noop, nil
	}
	repo, err := git.Open(dir)
	if err != nil {
		return nil, "", noop, err
	}
	closer := repo.Close
	var cs *core.Changeset
	switch {
	case f.staged:
		cs, err = repo.StagedChangeset()
	case f.base != "":
		cs, err = repo.BaseChangeset(f.base)
	case len(pos) == 1 && strings.Contains(pos[0], ".."):
		var from, to string
		from, to, err = repo.ParseRange(pos[0])
		if err == nil {
			cs, err = repo.RangeChangeset(from, to)
			if cs != nil {
				cs.Label = pos[0]
			}
		}
	case len(pos) == 1:
		var sha string
		sha, err = repo.Resolve(pos[0])
		if err == nil {
			cs, err = repo.CommitChangeset(sha)
			if err == nil {
				cs.Message = repo.CommitMessage(sha)
			}
		}
	default:
		cs, err = repo.WorkingChangeset("", !f.noUntracked)
	}
	if err != nil {
		closer()
		return nil, "", noop, err
	}
	return cs, repo.Root, closer, nil
}

func workDir(d string) (string, error) {
	if d == "" {
		return os.Getwd()
	}
	return filepath.Abs(d)
}

func loadConfig(root, explicit string) (*config.Config, error) {
	if explicit != "" {
		return config.LoadFile(explicit)
	}
	return config.Load(root)
}

func cfgName(c *config.Config) string {
	if c.Path == "" {
		return "config"
	}
	return filepath.Base(c.Path)
}

func termWidth() int {
	if v := os.Getenv("COLUMNS"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 40 {
			return n
		}
	}
	return 100
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
