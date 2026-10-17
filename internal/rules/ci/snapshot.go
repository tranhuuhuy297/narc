package ci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
)

var snapshotForced = &core.Rule{
	ID:       "snapshot-update-forced",
	Title:    "Snapshot update forced",
	Category: "ci",
	Severity: core.Warning,
	Summary:  "A test command now rewrites snapshots instead of comparing against them (`-u`, `--updateSnapshot`, `--update-snapshots`, `UPDATE_SNAPSHOTS=1`, `INSTA_UPDATE=always`, ...).",
	Hint: "Remove the update flag: a snapshot test must compare against the committed snapshot. Update " +
		"snapshots locally with a dedicated command, review the diff, and commit the new snapshots.",
	Docs: "A test that runs with `jest -u` cannot fail on a snapshot mismatch: it overwrites the snapshot " +
		"with whatever the code produces now. Added to a test script or a CI step, it silently accepts " +
		"every regression.\n\n" +
		"```diff\n-    \"test\": \"jest --ci\",\n+    \"test\": \"jest --ci -u\",      // SUSPICIOUS\n```\n\n" +
		"Covered: jest/vitest `-u`/`--updateSnapshot`/`--update`/`--ci=false`, Playwright and Bun " +
		"`--update-snapshots`, syrupy `--snapshot-update`, pytest-regressions `--force-regen`, " +
		"inline-snapshot `--inline-snapshot=fix`, Go `-update` golden flags, Flutter `--update-goldens`, " +
		"`cargo insta accept`, and `UPDATE_SNAPSHOTS`/`INSTA_UPDATE=always`/`INSTA_FORCE_PASS`/`TRYCMD=overwrite` " +
		"variables. Dedicated update scripts and CI jobs (named for updating, or committing the result) are fine.\n\n" +
		"Info: existing snapshots rewritten in a change that also edits source code — the new output may " +
		"have been accepted rather than checked.",
}

func init() {
	snapshotForced.Check = scoped(checkSnapshot)
	core.Register(snapshotForced)
}

func checkSnapshot(a *core.Analysis) []core.Finding {
	var out []core.Finding
	out = append(out, snapshotCommands(a)...)
	out = append(out, snapshotEnvKeys(a)...)
	out = append(out, snapshotRewrites(a)...)
	return out
}

// updateIntentRe matches names of scripts, targets and jobs whose purpose
// is updating snapshots.
var updateIntentRe = regexp.MustCompile(`(?i)(update|snap|golden|regen|accept|bless|record|approve|refresh|overwrite)`)

// snapshotUpdate returns the flag or variable that makes an invocation
// rewrite snapshots, or "".
func snapshotUpdate(iv invocation) string {
	for k, v := range iv.Env {
		if r := snapshotEnv(k, v); r != "" {
			return r
		}
	}
	c, ok := classify(iv)
	if !ok {
		if iv.Prog() == "cargo" && firstPositional(iv.Args[1:]) == "insta" && hasArg(iv.Args, "accept", "review") {
			return "cargo insta " + positionalAt(iv.Args[1:], 1)
		}
		return ""
	}
	args := iv.Args[1:]
	has := func(spellings ...string) string {
		for _, a := range args {
			for _, s := range spellings {
				if a == s || strings.HasPrefix(a, s+"=") && !strings.HasSuffix(a, "=false") && !strings.HasSuffix(a, "=missing") && !strings.HasSuffix(a, "=none") && !strings.HasSuffix(a, "=create") {
					return a
				}
			}
		}
		return ""
	}
	prog := iv.Prog()
	switch {
	case prog == "jest" || prog == "vitest" || prog == "react-scripts" || prog == "craco" || c.Eco == ecoJS && (c.Tool == prog+" test" || strings.HasPrefix(c.Tool, prog+" run ")):
		if f := has("-u", "--updateSnapshot", "--update", "--update-snapshots"); f != "" {
			return f
		}
		for _, a := range args {
			if a == "--ci=false" {
				return a
			}
		}
	case c.Tool == "playwright test" || c.Tool == "bun test":
		return has("-u", "--update-snapshots")
	case c.Eco == ecoPython && c.Category == catTest:
		if f := has("--snapshot-update", "--force-regen", "--regen-all"); f != "" {
			return f
		}
		// inline-snapshot: only "fix" and "update" rewrite existing values;
		// create, trim, review, report and disable do not hide a mismatch.
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--inline-snapshot="); ok {
				for _, mode := range strings.Split(v, ",") {
					if mode == "fix" || mode == "update" {
						return a
					}
				}
			}
		}
	case prog == "go" || prog == "gotestsum":
		return has("-update", "--update", "-update-golden", "-update-goldens", "-golden", "-update_golden")
	case prog == "flutter":
		return has("--update-goldens")
	case prog == "deno":
		return has("--update", "-u")
	case prog == "cargo":
		if firstPositional(args) == "insta" {
			return has("--accept", "--force-update-snapshots")
		}
	}
	return ""
}

// snapshotEnv returns NAME=value when an environment variable forces
// snapshot updates.
func snapshotEnv(name, value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	v := strings.ToLower(value)
	switch strings.ToUpper(name) {
	case "UPDATE_SNAPSHOTS", "UPDATE_SNAPSHOT", "SNAPSHOT_UPDATE", "SNAPSHOTS_UPDATE", "UPDATE_GOLDEN", "UPDATE_GOLDENS", "INSTA_FORCE_PASS":
		if truthy(v) {
			return name + "=" + value
		}
	case "INSTA_UPDATE":
		if v == "always" || v == "unseen" {
			return name + "=" + value
		}
	case "TRYCMD", "SNAPSHOTS":
		if v == "overwrite" {
			return name + "=" + value
		}
	}
	return ""
}

// updateContext reports whether a command belongs to something meant to
// update snapshots: a script/target named so, or a CI job named so or one
// that commits its results.
func updateContext(f *core.FileInfo, c cmdLine) bool {
	if updateIntentRe.MatchString(c.Owner) {
		return true
	}
	if kindOf(f) != kindCIYAML {
		return false
	}
	s := newSide(f)
	job, step := yamlOwner(s, c.Line)
	if updateIntentRe.MatchString(job) || updateIntentRe.MatchString(step) {
		return true
	}
	// A job that commits or opens a PR with the regenerated files.
	anc := yamlAncestors(s, c.Line)
	for _, a := range anc {
		if a.Key == job && job != "" {
			text := strings.Join(linesText(s, a.Line, blockEnd(s, a.Line, a.Indent)), "\n")
			return strings.Contains(text, "git commit") || strings.Contains(text, "git push") ||
				strings.Contains(text, "create-pull-request") || strings.Contains(text, "auto-commit") ||
				strings.Contains(text, "git-auto-commit")
		}
	}
	return false
}

func linesText(s side, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		if l, ok := s.get(i); ok {
			out = append(out, l.NoComment)
		}
	}
	return out
}

// snapshotCommands reports test commands that gained a snapshot-update
// flag or variable.
func snapshotCommands(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range commandFiles(a) {
		added, removed := changedCommands(f)
		before := multiset{}
		for _, c := range removed {
			for _, iv := range invocations(c.Text) {
				if u := snapshotUpdate(iv); u != "" {
					before.add(u)
				}
			}
		}
		addSet := lineSet(f.Added)
		for _, c := range added {
			if updateContext(f, c) {
				continue
			}
			if kindOf(f) == kindScripts || kindOf(f) == kindMake || kindOf(f) == kindPyproject {
				// only scripts and targets that are the test entry points
				if _, ok := verificationName(c.Owner); !ok && c.Owner != "" {
					continue
				}
			}
			for _, iv := range invocations(c.Text) {
				u := snapshotUpdate(iv)
				if u == "" || before.take(u) {
					continue
				}
				where := ""
				if c.Owner != "" {
					where = " in " + ownerLabel(kindOf(f), c.Owner)
				} else if d := describeOwner(yamlOwner(newSide(f), c.Line)); d != "" && kindOf(f) == kindCIYAML {
					where = " in " + d
				}
				out = append(out, snapshotForced.Finding(f, firstChanged(c, addSet),
					fmt.Sprintf("%s%s runs with `%s`: snapshots are rewritten instead of checked, so mismatches can never fail", quote(iv.String()), where, u)))
			}
		}
	}
	return out
}

var envKeyRe = regexp.MustCompile(`^\s*(?:-\s+)?([A-Z][A-Z0-9_]*)\s*[:=]\s*(.+?)\s*$`)

// snapshotEnvKeys reports snapshot-update variables added to CI env blocks.
func snapshotEnvKeys(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range a.Files {
		if kindOf(f) != kindCIYAML {
			continue
		}
		removed := multiset{}
		for _, l := range f.Removed {
			if m := envKeyRe.FindStringSubmatch(l.NoComment); m != nil {
				removed.add(m[1] + "=" + m[2])
			}
		}
		for _, l := range f.Added {
			m := envKeyRe.FindStringSubmatch(l.NoComment)
			if m == nil || removed.take(m[1]+"="+m[2]) {
				continue
			}
			u := snapshotEnv(m[1], m[2])
			if u == "" || updateContext(f, cmdLine{Line: l.No, End: l.No}) {
				continue
			}
			where := "the pipeline"
			if d := describeOwner(yamlOwner(newSide(f), l.No)); d != "" {
				where = d
			}
			out = append(out, snapshotForced.Finding(f, l.No,
				fmt.Sprintf("`%s` set for %s: snapshot tests there rewrite their snapshots instead of failing", u, where)))
		}
	}
	return out
}

// ---- rewritten snapshots ----

var jestExportRe = regexp.MustCompile("^exports\\[`(.*)`\\] = ")

// snapshotEntries splits a Jest/Vitest .snap file into name → body.
func snapshotEntries(content []byte) map[string]string {
	out := map[string]string{}
	name := ""
	var body strings.Builder
	for _, line := range diff.SplitLines(string(content)) {
		if m := jestExportRe.FindStringSubmatch(line); m != nil {
			if name != "" {
				out[name] = body.String()
			}
			name = m[1]
			body.Reset()
		}
		if name != "" {
			body.WriteString(line + "\n")
		}
	}
	if name != "" {
		out[name] = body.String()
	}
	return out
}

// snapshotRewrites notes existing snapshots that were rewritten in a
// change that also edits source code.
func snapshotRewrites(a *core.Analysis) []core.Finding {
	var source []string
	for _, f := range a.Files {
		if f.Lang.IsProgramming() && !f.IsTest && !core.IsSnapshotPath(f.Path) && !f.IsCI && f.Config == "" &&
			(len(f.Added) > 0 || len(f.Removed) > 0) {
			source = append(source, f.Path)
		}
	}
	if len(source) == 0 {
		return nil
	}
	var out []core.Finding
	for _, f := range a.Files {
		if !core.IsSnapshotPath(f.Path) || f.Diff.Kind != diff.Modified || len(f.Removed) == 0 {
			continue
		}
		oldB, ok1 := f.OldContent()
		newB, ok2 := f.NewContent()
		var rewritten []string
		if ok1 && ok2 && strings.HasSuffix(f.Path, ".snap") && strings.Contains(string(oldB), "exports[`") {
			oe, ne := snapshotEntries(oldB), snapshotEntries(newB)
			for name, body := range oe {
				if nb, ok := ne[name]; ok && nb != body {
					rewritten = append(rewritten, name)
				}
			}
			if len(rewritten) == 0 {
				continue // only snapshots added or removed
			}
		} else if len(f.Added) == 0 {
			continue
		}
		what := "a snapshot was rewritten"
		if len(rewritten) > 0 {
			what = plural(len(rewritten), "existing snapshot") + " rewritten"
			if len(rewritten) == 1 {
				what = fmt.Sprintf("existing snapshot %q rewritten", rewritten[0])
			}
		}
		also := source[0]
		if len(source) > 1 {
			also = fmt.Sprintf("%s and %d other source files", source[0], len(source)-1)
		}
		line := f.AnchorForOld(f.Removed[0].No)
		if len(f.Added) > 0 {
			line = f.Added[0].No
		}
		out = append(out, sevFinding(snapshotForced, f, line, core.Info,
			fmt.Sprintf("%s in %s alongside changes to %s: check that the new output is intended, not just accepted", what, path.Base(f.Path), also)))
	}
	return out
}
