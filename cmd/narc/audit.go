package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/tranhuuhuy297/narc/internal/audit"
	"github.com/tranhuuhuy297/narc/internal/git"
	"github.com/tranhuuhuy297/narc/internal/report"
)

func (c *cli) audit(args []string) int {
	fs := c.newFlagSet("audit", "narc audit [flags] [REV]")
	max := fs.Int("max", 1000, "scan at most `N` recent commits (0 = all)")
	all := fs.Bool("all", false, "scan the entire history (same as --max 0)")
	since := fs.String("since", "", "only commits after `DATE` (anything git log --since accepts, e.g. \"3 months ago\")")
	agent := fs.String("agent", "", "only show agents whose name contains `NAME`")
	format := fs.String("format", "pretty", "output `format`: pretty, json, markdown")
	card := fs.String("card", "", "also write a 1200×630 SVG share card to `FILE`")
	output := fs.String("output", "", "write the report to `FILE` instead of stdout")
	includeInfo := fs.Bool("include-info", false, "count info-level findings too")
	workers := fs.Int("workers", runtime.NumCPU(), "parallel workers")
	noColor := fs.Bool("no-color", false, "disable colors")
	dirFlag := fs.String("C", "", "run as if started in `DIR`")
	configPath := fs.String("config", "", "use this config `FILE` instead of .narc.yml")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return c.fail(err)
	}
	if len(pos) > 1 {
		return c.fail(fmt.Errorf("audit takes at most one revision or range"))
	}
	dir, err := workDir(*dirFlag)
	if err != nil {
		return c.fail(err)
	}
	repo, err := git.Open(dir)
	if err != nil {
		return c.fail(err)
	}
	defer repo.Close()
	cfg, err := loadConfig(repo.Root, *configPath)
	if err != nil {
		return c.fail(err)
	}
	if *all {
		*max = 0
	}
	opts := audit.Options{Config: cfg, Max: *max, Since: *since, Agent: *agent, IncludeInfo: *includeInfo, Workers: *workers}
	if len(pos) == 1 {
		opts.Rev = pos[0]
	}

	out := c.stdout
	color := false
	if *output != "" && *output != "-" {
		f, err := os.Create(*output)
		if err != nil {
			return c.fail(err)
		}
		defer f.Close()
		out = f
	} else if f, ok := c.stdout.(*os.File); ok {
		color = !*noColor && report.ColorEnabled(f)
	}
	if ef, ok := c.stderr.(*os.File); ok && report.ColorEnabled(ef) && *format == "pretty" {
		opts.Progress = func(done, total int) {
			if done == total || done%25 == 0 {
				fmt.Fprintf(c.stderr, "\r\x1b[2Kscanning commits %d/%d", done, total)
			}
			if done == total {
				fmt.Fprint(c.stderr, "\r\x1b[2K")
			}
		}
	}

	rep, err := audit.Run(repo, opts)
	if err != nil {
		return c.fail(err)
	}
	ro := audit.RenderOptions{Color: color, Width: termWidth()}
	switch *format {
	case "pretty", "text":
		err = audit.Pretty(out, rep, ro)
	case "json":
		err = audit.JSON(out, rep)
	case "markdown", "md":
		err = audit.Markdown(out, rep, ro)
	default:
		return c.fail(fmt.Errorf("unknown format %q (want pretty, json or markdown)", *format))
	}
	if err != nil {
		return c.fail(err)
	}
	if *card != "" {
		f, err := os.Create(*card)
		if err != nil {
			return c.fail(err)
		}
		if err := audit.Card(f, rep); err != nil {
			f.Close()
			return c.fail(err)
		}
		f.Close()
		fmt.Fprintf(c.stderr, "wrote %s — %s\n", *card, rep.Summary())
	}
	return exitOK
}
