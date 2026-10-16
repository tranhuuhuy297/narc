package hook

import (
	"fmt"
	"strings"

	"github.com/huyth45/narc/internal/config"
	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/git"
)

// maxReported bounds the findings a pre-commit report lists.
const maxReported = 20

// runPreCommit scans the staged changes. It exits 1 when a finding reaches
// fail_on, and fails open (exit 0) when narc cannot run.
func runPreCommit(env Env) outcome {
	fail := func(err error) outcome {
		return outcome{stderr: fmt.Sprintf("narc: %s (pre-commit check skipped)\n", truncate(oneLine(err.Error()), 300))}
	}
	dir, err := env.Getwd()
	if err != nil {
		return fail(err)
	}
	repo, err := git.Open(dir)
	if err != nil {
		return fail(err)
	}
	defer repo.Close()
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return fail(fmt.Errorf("config: %w", err))
	}
	cs, err := repo.StagedChangeset()
	if err != nil {
		return fail(err)
	}
	w := &workspace{dir: repo.Root, root: repo.Root, repo: repo, cfg: cfg}
	res := w.run(cs)
	active := res.Active()
	if len(active) == 0 {
		return outcome{}
	}
	threshold, fails := cfg.FailThreshold()
	blocked := fails && len(res.AtLeast(threshold)) > 0
	out := outcome{stderr: commitReport(active, blocked, cfg.FailOn)}
	if blocked {
		out.code = 1
	}
	return out
}

// commitReport renders findings for the person (or agent) running git
// commit. It deliberately does not mention --no-verify.
func commitReport(found []core.Finding, blocked bool, failOn string) string {
	var b strings.Builder
	verdict := "commit allowed"
	if blocked {
		verdict = "commit blocked"
	}
	fmt.Fprintf(&b, "narc: %s in the staged changes; %s (fail_on: %s)\n", plural(len(found), "verification issue"), verdict, failOn)
	hinted := map[string]bool{}
	for i, f := range found {
		if i == maxReported {
			fmt.Fprintf(&b, "  …and %d more (narc scan --staged lists them all)\n", len(found)-i)
			break
		}
		fmt.Fprintf(&b, "  %s %-10s %s %s — %s\n", severityMark(f.Severity), severityLabel(f.Severity), f.RuleID, f.Location(),
			truncate(oneLine(f.Message), maxMsgLen))
		if f.Hint != "" && !hinted[f.RuleID] {
			hinted[f.RuleID] = true
			fmt.Fprintf(&b, "      → %s\n", truncate(oneLine(f.Hint), maxHintLen))
		}
	}
	if blocked {
		b.WriteString("Fix the code instead of weakening the checks (`narc explain <rule>` has details).\n")
		fmt.Fprintf(&b, "If a change is intentional, waive it with a reason, e.g. %s\n", waiverExample(found[0]))
	}
	return b.String()
}

func severityMark(s core.Severity) string {
	switch s {
	case core.Error:
		return "✗"
	case core.Warning:
		return "!"
	}
	return "·"
}

func severityLabel(s core.Severity) string {
	switch s {
	case core.Error:
		return "BUSTED"
	case core.Warning:
		return "SUSPICIOUS"
	}
	return "NOTE"
}
