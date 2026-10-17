package hook

import (
	"fmt"
	"sort"
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
	active := w.run(cs).Active()
	if mergeHead, err := repo.Resolve("MERGE_HEAD"); err == nil {
		active = w.mergeResolution(active, mergeHead)
	}
	if len(active) == 0 {
		return outcome{}
	}
	threshold, fails := cfg.FailThreshold()
	// `fail_on: off` parses as core.Off (below info); it means "never".
	threshold = effectiveThreshold(threshold)
	blocked := false
	for _, f := range active {
		if fails && f.Severity >= threshold {
			blocked = true
			break
		}
	}
	out := outcome{stderr: commitReport(active, blocked, cfg.FailOn)}
	if blocked {
		out.code = 1
	}
	return out
}

// mergeResolution keeps the findings of a merge being concluded (git
// commit after resolving conflicts) that the merge itself introduces. The
// staged changes, compared with HEAD, contain everything the merged branch
// brings in, which was written and reviewed elsewhere; a finding counts
// only if comparing the staged result with MERGE_HEAD shows it too, as
// combined diffs do. If that comparison fails, every finding is kept.
func (w *workspace) mergeResolution(found []core.Finding, mergeHead string) []core.Finding {
	files, err := w.repo.Diff("--cached", mergeHead)
	if err != nil {
		return found
	}
	theirs := w.run(&core.Changeset{Files: files, Label: "MERGE_HEAD → staged", Source: indexSource{w.repo, mergeHead}}).Active()
	fps, keys := map[string]bool{}, map[string]bool{}
	for _, f := range theirs {
		fps[f.Fingerprint] = true
		keys[findingKey(f)] = true
	}
	var out []core.Finding
	for _, f := range found {
		// Messages may carry counts that differ between the two
		// comparisons ("3 → 1" vs "2 → 1"); the code involved does not.
		if fps[f.Fingerprint] || keys[findingKey(f)] {
			out = append(out, f)
		}
	}
	return out
}

// findingKey identifies a finding by its rule, file and code, ignoring
// its message.
func findingKey(f core.Finding) string {
	return f.RuleID + "\x00" + f.Path + "\x00" + oneLine(f.Evidence) + "\x00" + oneLine(f.Before)
}

// indexSource serves a revision as the old side and the index as the new.
type indexSource struct {
	repo *git.Repo
	rev  string
}

func (s indexSource) Old(p string) ([]byte, bool) { return s.repo.Blob(s.rev, p) }
func (s indexSource) New(p string) ([]byte, bool) { return s.repo.Blob("", p) }

// ListNew implements core.Lister: the files in the index under dir.
func (s indexSource) ListNew(dir string) []string {
	args := []string{"ls-files", "-z", "--cached"}
	if dir != "" {
		args = append(args, "--", dir)
	}
	out, err := s.repo.Run(args...)
	if err != nil {
		return nil
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
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
