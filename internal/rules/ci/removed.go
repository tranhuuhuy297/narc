package ci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

var checkRemoved = &core.Rule{
	ID:       "ci-check-removed",
	Title:    "Verification removed from CI",
	Category: "ci",
	Severity: core.Error,
	Summary:  "A test, lint or type-check step was removed from CI or disabled (`if: false`, `when: never`), and nothing in the change runs it instead.",
	Hint: "Put the check back and fix what it reports. If the step moved, make sure the new location still runs " +
		"it on every pull request; if it is obsolete, replace it with an equivalent check in the same change.",
	Docs: "Deleting the CI step that fails is the bluntest way to get a green build. narc compares the " +
		"verification commands a pipeline ran before and after the change: a removed test/lint/type-check " +
		"command is fine when the same kind of check still runs somewhere else — in the changed CI or build " +
		"files (steps that moved, jobs that were renamed or merged into a matrix, `pytest` replaced by " +
		"`make test`) or in another workflow the change does not touch that runs on pull requests — and " +
		"reported otherwise.\n\n" +
		"```yaml\n   steps:\n     - run: npm ci\n-    - run: npm run lint     # BUSTED: nothing else lints\n     - run: npm test\n```\n\n" +
		"Entry points are looked into when their definitions can be read (in the change or in the repository): " +
		"`npm run ci`, `make all`, `just check`, `./scripts/ci.sh` and local actions or reusable workflows " +
		"(`uses: ./...`) count as running what their scripts, recipes and steps run, so `make all` whose recipe " +
		"runs pytest covers a removed `pytest` step, and `npm run verify` that only runs `tsc` does not cover a " +
		"removed `jest` step. A definition that runs something narc cannot classify is judged by its name.\n\n" +
		"Also reported: a whole pipeline file deleted, a job or step disabled with a condition that can never " +
		"be true (`if: false`, `if: ${{ 1 == 2 }}`, Azure `condition: and(false, ...)`), `enabled: false`, " +
		"`when: never` or Buildkite `skip:` (error), a check moved into GitLab/Travis `after_script`, whose exit " +
		"status never fails the job (error), a verifying job made manual with `when: manual` or skipped on pull " +
		"requests (`if: github.event_name == 'push'`) while nothing else runs its checks there (warning), and " +
		"the `pull_request` trigger removed from a workflow that runs tests (warning).\n\n" +
		"Steps may move into things narc cannot read. A newly referenced composite action, GitLab " +
		"`extends:`/`include:`/`!reference`, Azure `template:`, Jenkins shared library or CircleCI orb job " +
		"named like a check counts as running it; a newly called script or an existing reusable workflow " +
		"that is not named like a check lowers the finding to a warning. With a bare patch (`--patch`) the " +
		"other workflows and the definitions behind entry points are not visible, so only the change counts.",
}

func init() {
	checkRemoved.Check = scoped(checkCheckRemoved)
	core.Register(checkRemoved)
}

// presentCheck is a verification command that still runs after the change.
type presentCheck struct {
	check
	targets []string
	// maybe marks a reference whose contents the change does not show and
	// that was already there (a reusable workflow named "deploy"): a
	// removed check might still run inside it, but nothing says it does.
	maybe bool
	// from names the unchanged pipeline the check runs in ("" for the
	// files of the change).
	from string
}

func checkCheckRemoved(a *core.Analysis) []core.Finding {
	present := presentChecks(a)
	present = append(present, addedOpaqueRefs(a)...)
	var out []core.Finding
	for _, f := range a.Files {
		if k := kindOf(f); !f.IsCI || k != kindCIYAML && k != kindJenkins {
			continue
		}
		if f.Diff.Kind == diff.Deleted {
			out = append(out, deletedPipeline(a, f, present)...)
			continue
		}
		out = append(out, removedSteps(a, f, present)...)
		out = append(out, movedIntoAfterScript(a, f, present)...)
		out = append(out, disabledJobs(a, f)...)
		out = append(out, triggerRemoved(a, f)...)
	}
	return out
}

// presentChecks collects the verification that still runs after the
// change: everything in the new versions of the changed CI files, plus
// commands added to package scripts, Makefiles, tox and shell scripts.
// Entry points in CI steps (`npm run ci`, `make all`, ./scripts/ci.sh) are
// resolved to what they run when their definitions are readable.
func presentChecks(a *core.Analysis) []presentCheck {
	inChange := map[string]bool{}
	for _, f := range a.Files {
		inChange[f.Path] = true
	}
	x := newExpander(a, false)
	var out []presentCheck
	for _, f := range commandFiles(a) {
		ci := f.IsCI
		s := newSide(f)
		var cmds []cmdLine
		if ci {
			cmds = commandLines(f, s)
		} else {
			cmds, _ = changedCommands(f)
		}
		addSet := lineSet(f.Added)
		if ci && kindOf(f) == kindCIYAML {
			cmds = gatingLines(f, s) // after_script cannot fail the job
		}
		walkCI(f, s, cmds, func(c cmdLine, iv invocation, dir string) {
			cs, understood := classifyAll(iv), false
			if ci {
				cs, understood = x.resolve(iv, dir)
			}
			for _, ch := range cs {
				out = append(out, presentCheck{check: ch, targets: targets(iv)})
			}
			if len(cs) == 0 && !understood && ci && c.overlaps(addSet) {
				if s := opaqueEntryPoint(iv); s != "" && !inChange[strings.TrimPrefix(path.Clean(s), "./")] {
					// a newly called script or default target we cannot see into
					out = append(out, presentCheck{check: check{Tool: s, Category: catUmbrella, Eco: ecoAny}, maybe: true})
				}
			}
		})
		if ci && kindOf(f) == kindCIYAML {
			out = append(out, actionChecks(a, f, s)...)
		}
		if ci && kindOf(f) == kindJenkins {
			out = append(out, jenkinsLibrarySteps(f)...)
		}
	}
	return out
}

// removedChecks returns the checks a removed command ran; x resolves entry
// points (`make ci`) against the old revision.
func removedChecks(x *expander, iv invocation, dir string) []check {
	cs, _ := x.resolve(iv, dir)
	return cs
}

// opaqueEntryPoint returns the script or task runner an invocation hands
// control to without saying what it runs: "./ci/run.sh", "bash build.sh",
// a bare "make" (the default target). It returns "" otherwise.
func opaqueEntryPoint(iv invocation) string {
	switch iv.Prog() {
	case "make", "gmake", "just", "task":
		if len(positionalsSkipping(iv.Args[1:], taskRunnerFlags[iv.Prog()])) == 0 {
			return iv.Prog()
		}
		return ""
	case "bash", "sh", "zsh", "pwsh", "python", "node", "ruby":
		return scriptArg(iv.Args[1:])
	}
	if w := iv.Word; strings.Contains(w, "/") || hasScriptExt(w) {
		return w
	}
	return ""
}

// jenkinsStepRe matches a call of a custom pipeline step (from a shared
// library) at the start of a line: runUnitTests(), lintAll { ... }.
var jenkinsStepRe = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s*(\(|\{)`)

// jenkinsLibrarySteps treats added calls of shared-library steps named like
// checks (runTests(), lint()) as running them, and an added @Library
// import as opaque.
func jenkinsLibrarySteps(f *core.FileInfo) []presentCheck {
	var out []presentCheck
	for _, l := range f.Added {
		if strings.Contains(l.Code, "@Library") {
			out = append(out, presentCheck{check: check{Tool: "@Library", Category: catUmbrella, Eco: ecoAny}})
			continue
		}
		m := jenkinsStepRe.FindStringSubmatch(l.Code)
		if m == nil {
			continue
		}
		switch m[1] {
		case "sh", "bat", "powershell", "pwsh", "stage", "steps", "script", "when", "post", "always", "dir", "withEnv", "timeout", "retry", "node", "parallel":
			continue // built-in steps: their commands are read directly
		}
		if cat, ok := verificationName(m[1]); ok {
			out = append(out, presentCheck{check: check{Tool: m[1] + "()", Category: cat, Eco: ecoAny}})
		}
	}
	return out
}

// actionCategories maps verification actions to what they check.
var actionCategories = map[string]check{
	"golangci/golangci-lint-action": {Tool: "golangci-lint", Category: catLint, Eco: ecoGo},
	"pre-commit/action":             {Tool: "pre-commit", Category: catLint, Eco: ecoAny},
	"cypress-io/github-action":      {Tool: "cypress", Category: catTest, Eco: ecoJS},
	"actions-rs/clippy-check":       {Tool: "cargo clippy", Category: catLint, Eco: ecoRust},
	"astral-sh/ruff-action":         {Tool: "ruff", Category: catLint, Eco: ecoPython},
	"chartboost/ruff-action":        {Tool: "ruff", Category: catLint, Eco: ecoPython},
	"jakebailey/pyright-action":     {Tool: "pyright", Category: catTypes, Eco: ecoPython},
	"super-linter/super-linter":     {Tool: "super-linter", Category: catLint, Eco: ecoAny},
	"github/super-linter":           {Tool: "super-linter", Category: catLint, Eco: ecoAny},
	"psf/black":                     {Tool: "black", Category: catFormat, Eco: ecoPython},
	"py-actions/flake8":             {Tool: "flake8", Category: catLint, Eco: ecoPython},
	"ludeeus/action-shellcheck":     {Tool: "shellcheck", Category: catLint, Eco: ecoAny},
	"hadolint/hadolint-action":      {Tool: "hadolint", Category: catLint, Eco: ecoAny},
	"wearerequired/lint-action":     {Tool: "lint-action", Category: catLint, Eco: ecoAny},
}

// actionChecks returns the checks run by `uses:` steps and jobs:
// verification actions, reusable workflows, local actions named like checks
// and CircleCI orb jobs (node/test). A reusable workflow is opaque: it
// counts as running anything when the change adds the call or its name says
// it verifies (ci.yml, tests.yml), and as a maybe otherwise.
func actionChecks(a *core.Analysis, f *core.FileInfo, s side) []presentCheck {
	var out []presentCheck
	added := lineSet(f.Added)
	x := newExpander(a, false)
	circle := strings.HasPrefix(f.Path, ".circleci/")
	for n := 1; n <= len(s.lines); n++ {
		l, ok := s.get(n)
		if !ok {
			continue
		}
		nd, ok := parseYAMLLine(l, n)
		if !ok {
			continue
		}
		if circle && nd.Dash >= 0 {
			// workflows: build: jobs: - node/test, - python/test: {...}
			job := nd.Key
			if job == "" {
				job = nd.Value
			}
			if orb, name, ok := strings.Cut(job, "/"); ok && orb != "" && !strings.ContainsAny(name, " /") {
				if cat, ok := verificationName(name); ok {
					out = append(out, presentCheck{check: check{Tool: job, Category: cat, Eco: ecoAny}})
				}
			}
		}
		if normKey(nd.Key) != "uses" {
			continue
		}
		u := strings.ToLower(nd.Value)
		if strings.HasPrefix(u, "./") {
			// A local action or reusable workflow: read what it runs.
			x.seen = map[string]bool{}
			if r := x.localAction(nd.Value); r.found && !r.opaque {
				for _, c := range r.checks {
					out = append(out, presentCheck{check: c})
				}
				continue
			}
		}
		switch {
		case strings.Contains(u, ".github/workflows/"):
			name := path.Base(strings.SplitN(u, "@", 2)[0])
			name = strings.TrimSuffix(strings.TrimSuffix(name, ".yml"), ".yaml")
			_, named := verificationName(name)
			out = append(out, presentCheck{check: check{Tool: nd.Value, Category: catUmbrella, Eco: ecoAny},
				maybe: !named && !added[n]})
		case strings.HasPrefix(u, "./"):
			if cat, ok := verificationName(path.Base(strings.TrimSuffix(u, "/"))); ok {
				out = append(out, presentCheck{check: check{Tool: nd.Value, Category: cat, Eco: ecoAny}})
			}
		default:
			for prefix, c := range actionCategories {
				if strings.HasPrefix(u, prefix) {
					out = append(out, presentCheck{check: c})
				}
			}
		}
	}
	return out
}

// runnerValueFlags are test-runner flags whose separate value is not a
// test target (an exclusion, a config file, a report path, ...).
var runnerValueFlags = map[string]bool{
	"--deselect": true, "--ignore": true, "--ignore-glob": true, "--exclude": true, "-k": true, "-m": true,
	"-p": true, "-c": true, "-o": true, "--rootdir": true, "--confcutdir": true, "--cov": true, "--cov-report": true,
	"--cov-config": true, "--junitxml": true, "--basetemp": true, "--testPathIgnorePatterns": true,
	"--config": true, "--reporter": true, "--outputFile": true, "--coverageDirectory": true, "--tag": true,
	"--exclude-pattern": true, "--exclude-group": true, "--filter": true, "-run": true, "-skip": true,
	"-coverprofile": true, "-covermode": true, "-tags": true, "-timeout": true, "--grep": true, "--grep-invert": true,
	"--project": true, "--testNamePattern": true, "-t": true, "--skip": true, "--manifest-path": true,
}

// addedOpaqueRefs treats references that the change adds to definitions
// it does not show as opaque, since steps may have moved into them: a local
// composite action ("uses: ./.github/actions/ci"), a GitLab `extends:`
// template, `!reference` or `include:`, and an Azure `template:`.
func addedOpaqueRefs(a *core.Analysis) []presentCheck {
	inChange := map[string]bool{}
	defined := map[string]bool{} // top-level keys of the changed CI files (GitLab templates)
	for _, f := range a.Files {
		inChange[path.Dir(f.Path)] = true
		inChange[f.Path] = true
		if kindOf(f) != kindCIYAML {
			continue
		}
		s := newSide(f)
		for n := 1; n <= len(s.lines); n++ {
			if l, ok := s.get(n); ok {
				if nd, ok := parseYAMLLine(l, n); ok && nd.Indent == 0 && nd.Dash < 0 && nd.Key != "" {
					defined[nd.Key] = true
				}
			}
		}
	}
	opaque := func(tool string) presentCheck {
		return presentCheck{check: check{Tool: tool, Category: catUmbrella, Eco: ecoAny}}
	}
	x := newExpander(a, false)
	var out []presentCheck
	for _, f := range a.Files {
		if kindOf(f) != kindCIYAML {
			continue
		}
		s := newSide(f)
		for _, l := range f.Added {
			nd, ok := parseYAMLLine(l.LexedLine, l.No)
			if !ok {
				continue
			}
			if strings.Contains(nd.Value, "!reference") {
				out = append(out, opaque(nd.Value))
				continue
			}
			switch normKey(nd.Key) {
			case "uses":
				if strings.HasPrefix(nd.Value, "./") && !inChange[path.Clean(strings.TrimPrefix(nd.Value, "./"))] {
					x.seen = map[string]bool{}
					if r := x.localAction(nd.Value); r.found && !r.opaque {
						continue // read: actionChecks counts what it runs
					}
					out = append(out, opaque(nd.Value))
				}
			case "extends":
				for _, t := range extendsTargets(s, l.No, nd.Value) {
					if !defined[t] {
						out = append(out, opaque("extends "+t))
					}
				}
			case "template":
				t := strings.TrimPrefix(strings.SplitN(nd.Value, "@", 2)[0], "/")
				if t != "" && !inChange[t] && !inChange[path.Join(path.Dir(f.Path), t)] {
					out = append(out, opaque("template "+nd.Value))
				}
			case "include":
				if nd.Indent == 0 && nd.Dash < 0 { // GitLab include:, not a matrix include
					out = append(out, opaque("include"))
				}
			default:
				for _, anc := range yamlAncestors(s, l.No) {
					if anc.Key == "include" && anc.Indent == 0 {
						out = append(out, opaque("include"))
						break
					}
				}
			}
		}
	}
	return out
}

// extendsTargets returns the templates a GitLab `extends:` names: a scalar,
// a flow list, or a block list on the following lines.
func extendsTargets(s side, n int, v string) []string {
	switch {
	case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
		return splitFlowList(v[1 : len(v)-1])
	case v != "":
		return []string{v}
	}
	var out []string
	l, _ := s.get(n)
	key, _ := parseYAMLLine(l, n)
	for i := n + 1; i <= len(s.lines); i++ {
		nl, ok := s.get(i)
		if !ok {
			break
		}
		nd, ok := parseYAMLLine(nl, i)
		if !ok {
			continue
		}
		if nd.Dash < key.Indent && nd.Indent <= key.Indent || nd.Dash < 0 {
			break
		}
		out = append(out, nd.Value)
	}
	return out
}

// gatesPRs reports whether a pipeline gates pull requests. Workflows that
// only run on pushes to main, tags or schedules (deploys, releases) often
// repeat checks that a PR workflow already ran, so removing a check there
// is less certain.
func gatesPRs(f *core.FileInfo) bool {
	if !isWorkflowPath(f.Path) {
		return true
	}
	old, _, ok := configSides(f)
	if !ok {
		return true
	}
	t := workflowTriggers(old)
	_, mg := t["merge_group"]
	return runsOnPRs(t) || mg || len(t) == 0
}

// targets returns the paths a verification command is restricted to
// ("pytest tests/unit" → [tests/unit]; "go test ./..." → [./...]).
func targets(iv invocation) []string {
	var out []string
	args := iv.Args[min(1, len(iv.Args)):]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if runnerValueFlags[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
			continue
		}
		if a == "." || strings.Contains(a, "/") || hasScriptExt(a) || strings.HasSuffix(a, ".go") || strings.HasSuffix(a, ".java") {
			out = append(out, a)
		}
	}
	return out
}

// normTarget canonicalizes a path target.
func normTarget(t string) string {
	t = strings.TrimPrefix(t, "./")
	t = strings.TrimSuffix(t, "/...")
	t = strings.TrimSuffix(t, "/")
	if t == "..." || t == "" {
		return "."
	}
	return t
}

// targetsCover reports whether running have also runs everything in want.
// A target that depends on a variable ($SUITE, ${{ matrix.pkg }}) may be
// anything, so it covers and is covered.
func targetsCover(have, want []string) bool {
	for _, w := range want {
		w = normTarget(w)
		ok := strings.Contains(w, "$")
		for _, h := range have {
			h = normTarget(h)
			if h == "." || h == w || strings.HasPrefix(w, h+"/") || strings.HasPrefix(w, h+"::") || strings.Contains(h, "$") {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// coverage reports whether a removed check still runs: covered, or only
// partially (the same tool still runs, on other paths).
func coverage(rc check, rt []string, present []presentCheck) (covered bool, partial []string) {
	for _, p := range present {
		if p.maybe || !p.covers(rc) {
			continue
		}
		if p.Tool != rc.Tool || len(rt) == 0 || len(p.targets) == 0 || targetsCover(p.targets, rt) {
			return true, nil
		}
		partial = append(partial, p.targets...)
	}
	return false, partial
}

var ecoNames = map[string]string{
	ecoPython: "Python ", ecoJS: "JavaScript ", ecoGo: "Go ", ecoRust: "Rust ", ecoJVM: "JVM ", ecoRuby: "Ruby ",
	ecoPHP: "PHP ", ecoDotnet: ".NET ", ecoSwift: "Swift ", ecoElixir: "Elixir ", ecoDart: "Dart ", ecoDeno: "Deno ",
	ecoNative: "native ",
}

var catNames = map[string]string{
	catTest: "tests", catLint: "linting", catTypes: "type checking", catFormat: "format checks",
	catCoverage: "coverage checks", catUmbrella: "checks",
}

// describeCheck names what a check verifies: "Python tests", "linting".
func describeCheck(c check) string { return ecoNames[c.Eco] + catNames[c.Category] }

// removedSteps reports verification commands removed from a pipeline that
// nothing else runs: not the rest of the change, and not the pipelines the
// change does not touch.
func removedSteps(a *core.Analysis, f *core.FileInfo, present []presentCheck) []core.Finding {
	_, removed := changedCommands(f)
	old := oldSide(f)
	x := newExpander(a, true)
	var out []core.Finding
	seen := map[string]bool{}
	// Walk every gating old command so a `cd` on an unchanged line still
	// sets the directory of a removed one; only removed lines are judged.
	isRemoved := map[int]bool{}
	for _, c := range removed {
		isRemoved[c.Line] = true
	}
	walkCI(f, old, gatingLines(f, old), func(c cmdLine, iv invocation, cur string) {
		if !isRemoved[c.Line] {
			return
		}
		rt := targets(iv)
		for _, rc := range removedChecks(x, iv, cur) {
			key := rc.Tool + " " + strings.Join(rt, " ")
			if seen[key] {
				continue
			}
			seen[key] = true
			covered, partial := coverage(rc, rt, present)
			if covered || runsUnchanged(a, rc, rt) {
				continue
			}
			out = append(out, removedStepFinding(a, f, c, iv, rc, rt, partial, present))
		}
	})
	return out
}

// removedStepFinding describes a removed check that nothing else runs.
func removedStepFinding(a *core.Analysis, f *core.FileInfo, c cmdLine, iv invocation, rc check, rt, partial []string, present []presentCheck) core.Finding {
	line := firstChanged(c, lineSet(f.Removed))
	where := ""
	if kindOf(f) == kindCIYAML {
		if d := describeOwner(yamlOwner(oldSide(f), line)); d != "" {
			where = " from " + d
		}
	}
	verb := "was removed" + where
	switch {
	case commentedOut(f, iv):
		verb = "was commented out" + strings.Replace(where, " from ", " in ", 1)
	case movedToNonGating(f, iv):
		verb = "was moved" + where + " into after_script, whose exit status never fails the job"
	}
	cmd := quote(iv.String())
	if len(partial) > 0 {
		return removalFinding(checkRemoved, f, line, core.Warning,
			fmt.Sprintf("%s %s: %s still runs, but no longer on %s", cmd, verb, rc.Tool, strings.Join(rt, " ")))
	}
	what := describeCheck(rc)
	if !strings.Contains(iv.String(), rc.Tool) {
		what += " (" + rc.Tool + ")" // found inside the entry point: `make ci` ran pytest
	}
	sev, note := core.Error, ""
	switch m := maybeRuns(a, present); {
	case !gatesPRs(f):
		sev, note = core.Warning, " (this workflow does not run on pull requests; check that another one still does)"
	case m != "":
		sev, note = core.Warning, fmt.Sprintf(" (unless %s, which this change does not show, runs it)", m)
	}
	return removalFinding(checkRemoved, f, line, sev,
		fmt.Sprintf("%s %s, and %s runs %s%s", cmd, verb, nothingElse(a), what, note))
}

// nonGatingKeys are pipeline sections whose commands cannot fail the job:
// GitLab and Travis after_script, Travis after_success/after_failure.
var nonGatingKeys = map[string]bool{"after-script": true, "after-success": true, "after-failure": true, "after-deploy": true}

// nonGating reports whether the command on line n of a CI YAML side sits
// in a section whose failures are ignored.
func nonGating(s side, n int) bool {
	if l, ok := s.get(n); ok {
		if nd, ok := parseYAMLLine(l, n); ok && nonGatingKeys[normKey(nd.Key)] {
			return true
		}
	}
	for _, a := range yamlAncestors(s, n) {
		if nonGatingKeys[normKey(a.Key)] {
			return true
		}
		if isCommandKey(a.Key) {
			return false // the nearest command key decides
		}
	}
	return false
}

// gatingChecks counts the verification commands of a CI YAML side that
// can fail the job, by normalized text.
func gatingChecks(f *core.FileInfo, s side) multiset {
	m := multiset{}
	for _, c := range gatingLines(f, s) {
		for _, iv := range invocations(c.Text) {
			if len(classifyAll(iv)) > 0 {
				m.add(rulekit.Normalize(iv.String()))
			}
		}
	}
	return m
}

// movedIntoAfterScript reports verification commands that the change put
// into an after_script section (often just by inserting the key above an
// unchanged line), so their failures no longer fail the job.
func movedIntoAfterScript(a *core.Analysis, f *core.FileInfo, present []presentCheck) []core.Finding {
	if kindOf(f) != kindCIYAML || f.Diff.Kind == diff.Added || len(f.Added) == 0 {
		return nil
	}
	s := newSide(f)
	before, after := gatingChecks(f, oldSide(f)), gatingChecks(f, s)
	var out []core.Finding
	for _, c := range commandLines(f, s) {
		if !nonGating(s, c.Line) {
			continue
		}
		for _, iv := range invocations(c.Text) {
			key := rulekit.Normalize(iv.String())
			cs := classifyAll(iv)
			if len(cs) == 0 || before[key] <= after[key] {
				continue
			}
			before[key]-- // report each lost gating run once
			if covered, _ := coverage(cs[0], targets(iv), present); covered || runsUnchanged(a, cs[0], targets(iv)) {
				continue
			}
			where := ""
			if d := describeOwner(yamlOwner(s, c.Line)); d != "" {
				where = " in " + d
			}
			out = append(out, checkRemoved.Finding(f, c.Line,
				fmt.Sprintf("%s now runs in after_script%s, whose exit status never fails the job, so %s can no longer fail the pipeline",
					quote(iv.String()), where, describeCheck(cs[0]))))
		}
	}
	return out
}

// movedToNonGating reports whether a removed command reappears in a
// non-gating section (after_script) of the new side.
func movedToNonGating(f *core.FileInfo, iv invocation) bool {
	if kindOf(f) != kindCIYAML {
		return false
	}
	s := newSide(f)
	want := rulekit.Normalize(iv.String())
	for _, c := range commandLines(f, s) {
		if !nonGating(s, c.Line) {
			continue
		}
		for _, n := range invocations(c.Text) {
			if rulekit.Normalize(n.String()) == want {
				return true
			}
		}
	}
	return false
}

// nothingElse says where narc looked for a replacement check.
func nothingElse(a *core.Analysis) string {
	if unchangedChecks(a).listed {
		return "no other CI step or pipeline"
	}
	return "nothing else in this change"
}

// runsUnchanged reports whether a pipeline the change does not touch (and
// that gates pull requests) still runs a removed check: the step was a
// duplicate, or it moved into a workflow that already ran it.
func runsUnchanged(a *core.Analysis, rc check, rt []string) bool {
	covered, _ := coverage(rc, rt, unchangedChecks(a).checks)
	return covered
}

// maybeRuns returns the first reference that might run a removed check
// (see presentCheck.maybe), in the change or in an unchanged pipeline, or
// "".
func maybeRuns(a *core.Analysis, present []presentCheck) string {
	for _, p := range present {
		if p.maybe {
			return p.Tool
		}
	}
	for _, p := range unchangedChecks(a).checks {
		if p.maybe {
			return p.Tool + " (in " + p.from + ")"
		}
	}
	return ""
}

// commentedOut reports whether an added comment line of f holds the
// removed command.
func commentedOut(f *core.FileInfo, iv invocation) bool {
	want := rulekit.Normalize(iv.String())
	if want == "" {
		return false
	}
	for _, l := range f.Added {
		if l.Comment != "" && strings.Contains(rulekit.Normalize(l.Comment), want) {
			return true
		}
	}
	return false
}

// deletedPipeline reports a deleted pipeline file whose checks run nowhere
// else.
func deletedPipeline(a *core.Analysis, f *core.FileInfo, present []presentCheck) []core.Finding {
	var lost []check
	seen := map[string]bool{}
	old := oldSide(f)
	x := newExpander(a, true)
	walkCI(f, old, gatingLines(f, old), func(_ cmdLine, iv invocation, cur string) {
		rt := targets(iv)
		for _, rc := range removedChecks(x, iv, cur) {
			if seen[rc.Tool] {
				continue
			}
			seen[rc.Tool] = true
			if covered, _ := coverage(rc, rt, present); !covered && !runsUnchanged(a, rc, rt) {
				lost = append(lost, rc)
			}
		}
	})
	if len(lost) == 0 {
		return nil
	}
	return []core.Finding{fileFinding(checkRemoved, f, core.Error,
		fmt.Sprintf("pipeline deleted: it ran %s, and %s runs them", toolNames(lost), nothingElse(a)))}
}

// ---- disabled jobs and steps ----

var disableKeyRe = regexp.MustCompile(`^\s*(?:-\s+)?(if|condition|when|skip|enabled)\s*:\s*(.*?)\s*$`)

var falseCondRe = regexp.MustCompile(`^(false|0|'false'|"false")$|^false\s*&&|&&\s*false$|&&\s*false\s*&&`)

// disabledJobs reports jobs and steps that verify code and were switched
// off.
func disabledJobs(a *core.Analysis, f *core.FileInfo) []core.Finding {
	s, old := newSide(f), oldSide(f)
	// A removed condition cancels an added identical one only when both
	// disabled something that verifies: re-enabling a deploy job does not
	// pay for disabling the test job. Lines are compared with their string
	// contents, so `if: "false"` does not cancel `if: "github.ref == ..."`.
	jenkins := kindOf(f) == kindJenkins
	removed := multiset{}
	for _, l := range f.Removed {
		switch {
		case jenkins:
			removed.add(rulekit.Normalize(l.NoComment))
		case disableKeyRe.MatchString(l.NoComment):
			_, verifies := disabledBlock(f, old, l.No)
			removed.add(fmt.Sprintf("%s|%t", rulekit.Normalize(l.NoComment), verifies))
		}
	}
	var out []core.Finding
	for _, l := range f.Added {
		if jenkins {
			if jenkinsFalseWhen.MatchString(l.Code) && !removed.take(rulekit.Normalize(l.NoComment)) {
				out = append(out, jenkinsDisabledStage(f, s, l)...)
			}
			continue
		}
		m := disableKeyRe.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		key, val := m[1], strings.TrimSpace(m[2])
		sev, how := disabledBy(key, val)
		if how == "" && key == "if" && isWorkflowPath(f.Path) {
			if excludesPullRequests(val) && workflowGatesPRs(f) {
				sev, how = core.Warning, "skips on pull requests"
			}
		}
		if how == "" {
			continue
		}
		if key == "when" && conditionalRule(s, l.No) {
			continue // rules: - if: '$CI_COMMIT_TAG' \n when: never
		}
		if !controlsJobOrStep(s, l.No) {
			continue // an action input or a cache setting, not the step's own condition
		}
		bi, verifies := disabledBlock(f, s, l.No)
		if !verifies || removed.take(rulekit.Normalize(l.NoComment)+"|true") {
			continue
		}
		where := bi.describe
		if where == "" {
			where = "a job"
		}
		what := toolNames(bi.checks)
		if what == "" {
			what = strings.Join(bi.actions, ", ")
		}
		verb := "runs"
		if what == "" {
			what = "its checks"
		}
		if len(distinctTools(bi.checks))+len(bi.actions) > 1 || what == "its checks" {
			verb = "run"
		}
		msg := fmt.Sprintf("`%s: %s` %s %s, so %s no longer %s", key, val, how, where, what, verb)
		switch how {
		case "makes manual":
			msg = fmt.Sprintf("`%s: %s` makes %s manual, so %s no longer %s unless someone starts it", key, val, where, what, verb)
		case "skips on pull requests":
			start, end := yamlBlock(s, l.No)
			if newBlock(f, s, start, end) || runsOutside(a, f, start, end, bi.checks) {
				continue // a new post-merge job, or the checks still run on pull requests elsewhere
			}
			msg = fmt.Sprintf("`%s: %s` skips %s on pull requests, so %s no longer %s before merging", key, val, where, what, verb)
		}
		out = append(out, sevFinding(checkRemoved, f, l.No, sev, msg))
	}
	return out
}

// prEventRe matches conditions that hold only outside pull requests: a
// different event, a branch or tag ref (pull requests run on
// refs/pull/N/merge), or the absence of a pull request payload.
var prEventRe = regexp.MustCompile(`github\.event_name\s*==\s*['"](push|schedule|workflow_dispatch|release|repository_dispatch|workflow_run)['"]|` +
	`github\.event_name\s*!=\s*['"]pull_request['"]|github\.ref\s*==\s*['"]refs/(heads|tags)/|` +
	`startsWith\(\s*github\.ref\s*,\s*['"]refs/(heads|tags)/|github\.ref_name\s*==|!\s*github\.event\.pull_request\b|` +
	`github\.event\.pull_request\s*==\s*null`)

// excludesPullRequests reports whether a GitHub Actions condition can only
// be true outside pull requests.
func excludesPullRequests(v string) bool {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "${{") && strings.HasSuffix(v, "}}") {
		v = strings.TrimSpace(v[3 : len(v)-2])
	}
	return !strings.Contains(v, "||") && prEventRe.MatchString(v)
}

// workflowGatesPRs reports whether the new version of a workflow runs on
// pull requests.
func workflowGatesPRs(f *core.FileInfo) bool {
	_, new, ok := configSides(f)
	return ok && runsOnPRs(workflowTriggers(new))
}

// newBlock reports whether every command of lines start..end of side s is
// an added line: a job or step the change created, which never ran on pull
// requests in the first place.
func newBlock(f *core.FileInfo, s side, start, end int) bool {
	added := lineSet(f.Added)
	found := false
	for _, c := range commandLines(f, s) {
		if c.Line < start || c.Line > end {
			continue
		}
		found = true
		if !c.overlaps(added) {
			return false
		}
	}
	return found
}

// runsOutside reports whether every check in cs also runs in the changed
// CI files outside lines start..end of f, or in an unchanged pipeline.
func runsOutside(a *core.Analysis, f *core.FileInfo, start, end int, cs []check) bool {
	var present []presentCheck
	for _, g := range a.Files {
		if !g.IsCI || g.Diff.Kind == diff.Deleted {
			continue
		}
		for _, c := range commandLines(g, newSide(g)) {
			if g == f && c.Line >= start && c.Line <= end {
				continue
			}
			for _, ch := range checksIn(c.Text) {
				present = append(present, presentCheck{check: ch})
			}
		}
	}
	present = append(present, unchangedChecks(a).checks...)
	return allCoveredBy(cs, present)
}

// disabledBlock inspects the job or step that a condition on line n of
// side s controls (a GitLab rules entry controls its whole job), and
// reports whether it verifies code.
func disabledBlock(f *core.FileInfo, s side, n int) (blockInfo, bool) {
	start, end := yamlBlock(s, n)
	if inRulesList(s, n) {
		// GitLab rules: - when: never applies to the whole job
		job := jobBlock(s, n)
		start, end = job[0], job[1]
	}
	bi := inspectBlock(f, s, n, start, end)
	addTemplateUsers(f, s, start, &bi)
	return bi, len(bi.checks) > 0 || len(bi.actions) > 0 || bi.namedVerify
}

// distinctTools returns the distinct tool names of checks.
func distinctTools(cs []check) map[string]bool {
	m := map[string]bool{}
	for _, c := range cs {
		m[c.Tool] = true
	}
	return m
}

// controlsJobOrStep reports whether the key on line n belongs to a job or
// a step itself (or to a GitLab rules entry) rather than to a nested
// mapping such as an action's `with:` inputs or a cache setting.
func controlsJobOrStep(s side, n int) bool {
	l, ok := s.get(n)
	if !ok {
		return false
	}
	if nd, ok := parseYAMLLine(l, n); ok && nd.Dash >= 0 {
		return true // "- if: false" opens a step or a rules entry
	}
	anc := yamlAncestors(s, n)
	if len(anc) == 0 {
		return true
	}
	parent := anc[0]
	switch {
	case parent.Dash >= 0:
		return true // a key of a step, an Azure job/stage or a rules entry
	case parent.Indent == 0:
		return !gitlabReserved[parent.Key] // a GitLab job
	case len(anc) > 1:
		switch anc[1].Key {
		case "jobs", "stages", "steps", "workflows", "pipeline", "pipelines", "tasks":
			return true // jobs: test: if: ..., Drone/Woodpecker steps: test: when: ...
		}
	}
	return false
}

// inRulesList reports whether line n is inside a GitLab rules/only/except
// list.
func inRulesList(s side, n int) bool {
	for _, anc := range yamlAncestors(s, n) {
		if anc.Key == "rules" || anc.Key == "only" || anc.Key == "except" {
			return true
		}
	}
	return false
}

// constCompareRe matches a comparison of two literals, which is a constant:
// "1 == 2", "'a' != 'a'".
var constCompareRe = regexp.MustCompile(`^(\d+|'[^']*'|"[^"]*")\s*(==|!=)\s*(\d+|'[^']*'|"[^"]*")$`)

// constantFalse reports whether a condition can never be true: false, !true,
// a comparison of unequal literals, or an Azure and(...) with a false
// argument.
func constantFalse(v string) bool {
	v = strings.TrimSpace(v)
	for strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		v = strings.TrimSpace(v[1 : len(v)-1])
	}
	switch strings.Trim(v, `"'`) {
	case "false", "!true", "!(true)":
		return true
	}
	if m := constCompareRe.FindStringSubmatch(v); m != nil {
		same := strings.Trim(m[1], `"'`) == strings.Trim(m[3], `"'`)
		return same == (m[2] == "!=")
	}
	low := strings.ToLower(v)
	for _, fn := range []string{"and(", "eq("} {
		if !strings.HasPrefix(low, fn) {
			continue
		}
		i := strings.IndexByte(v, '(')
		args, end, ok := rulekit.CallArgs(v, i)
		if !ok || end != len(v)-1 {
			return false
		}
		if fn == "eq(" {
			return len(args) == 2 && constCompareRe.MatchString(args[0]+" == "+args[1]) && constantFalse(args[0]+" == "+args[1])
		}
		for _, a := range args {
			if constantFalse(a) {
				return true
			}
		}
	}
	return false
}

// disabledBy interprets a condition key; how is "" when it does not
// disable anything.
func disabledBy(key, val string) (core.Severity, string) {
	v := strings.TrimSpace(val)
	if strings.HasPrefix(v, "${{") && strings.HasSuffix(v, "}}") {
		v = strings.TrimSpace(v[3 : len(v)-2])
	}
	unq := strings.Trim(v, `"'`)
	switch key {
	case "if", "condition":
		if !strings.Contains(v, "||") && falseCondRe.MatchString(v) || unq == "false" || constantFalse(v) || constantFalse(unquoteYAML(v)) {
			return core.Error, "disables"
		}
	case "enabled":
		if falsy(unq) {
			return core.Error, "disables"
		}
	case "when":
		switch unq {
		case "never":
			return core.Error, "disables"
		case "manual":
			return core.Warning, "makes manual"
		}
	case "skip":
		switch {
		case truthy(unq):
			return core.Error, "skips"
		case unq != "" && !falsy(unq):
			return core.Warning, "skips"
		}
	}
	return core.Info, ""
}

// conditionalRule reports whether a GitLab "when:" sits in a rules: item
// that has a condition (if/changes/exists), i.e. it only applies sometimes.
func conditionalRule(s side, n int) bool {
	inRules := false
	for _, anc := range yamlAncestors(s, n) {
		if anc.Key == "rules" || anc.Key == "only" || anc.Key == "except" {
			inRules = true
			break
		}
	}
	if !inRules {
		return false
	}
	start, end := yamlBlock(s, n)
	for i := start; i <= end; i++ {
		if l, ok := s.get(i); ok {
			if nd, ok := parseYAMLLine(l, i); ok {
				switch nd.Key {
				case "if", "changes", "exists":
					return true
				}
			}
		}
	}
	return false
}

var jenkinsFalseWhen = regexp.MustCompile(`\bexpression\s*\{\s*(return\s+)?false\s*\}`)

// jenkinsDisabledStage reports `when { expression { false } }` on a stage
// that runs verification.
func jenkinsDisabledStage(f *core.FileInfo, s side, l core.Line) []core.Finding {
	end := len(s.lines)
	// the stage body continues after the when block
	start := l.No
	for i := l.No; i >= 1 && i >= l.No-5; i-- {
		if pl, ok := s.get(i); ok && strings.Contains(pl.Code, "stage(") {
			start, end = i, braceBlockEnd(s, i)
			break
		}
	}
	var cs []check
	for _, c := range commandLines(f, s) {
		if c.Line >= start && c.Line <= end {
			cs = append(cs, checksIn(c.Text)...)
		}
	}
	if len(cs) == 0 {
		return nil
	}
	return []core.Finding{checkRemoved.Finding(f, l.No,
		fmt.Sprintf("`when { expression { false } }` disables the stage that runs %s", toolNames(cs)))}
}

// allCoveredBy reports whether every check in cs still runs among present.
func allCoveredBy(cs []check, present []presentCheck) bool {
	for _, c := range cs {
		if ok, _ := coverage(c, nil, present); !ok {
			return false
		}
	}
	return len(cs) > 0
}

// ---- triggers ----

// isWorkflowPath reports whether p is a GitHub/Gitea/Forgejo workflow.
func isWorkflowPath(p string) bool {
	return strings.Contains("/"+p, "/.github/workflows/") || strings.Contains("/"+p, "/.gitea/workflows/") ||
		strings.Contains("/"+p, "/.forgejo/workflows/")
}

// workflowTriggers returns a workflow's events and the line each is
// declared on.
func workflowTriggers(es []entry) map[string]int {
	out := map[string]int{}
	for _, e := range es {
		if len(e.Path) == 0 || e.Path[0] != "on" && e.Path[0] != "true" {
			continue
		}
		if len(e.Path) == 1 {
			out[e.Value] = e.Line
		} else if _, ok := out[e.Path[1]]; !ok {
			out[e.Path[1]] = e.Line
		}
	}
	return out
}

func runsOnPRs(trig map[string]int) bool {
	_, pr := trig["pull_request"]
	_, prt := trig["pull_request_target"]
	return pr || prt
}

// triggerRemoved reports a workflow that runs checks and stopped running
// on pull requests.
func triggerRemoved(a *core.Analysis, f *core.FileInfo) []core.Finding {
	if !isWorkflowPath(f.Path) {
		return nil
	}
	old, new, ok := configSides(f)
	if !ok {
		return nil
	}
	ot, nt := workflowTriggers(old), workflowTriggers(new)
	if !runsOnPRs(ot) || runsOnPRs(nt) {
		return nil
	}
	var cs []check
	for _, c := range commandLines(f, newSide(f)) {
		cs = append(cs, checksIn(c.Text)...)
	}
	if len(cs) == 0 {
		return nil
	}
	// The checks may have moved to another workflow that runs on PRs, or
	// one the change does not touch may already run them.
	if allCoveredBy(cs, unchangedChecks(a).checks) {
		return nil
	}
	for _, g := range a.Files {
		if g == f || !isWorkflowPath(g.Path) {
			continue
		}
		if _, gn, ok := configSides(g); ok && runsOnPRs(workflowTriggers(gn)) {
			for _, c := range commandLines(g, newSide(g)) {
				if len(checksIn(c.Text)) > 0 {
					return nil
				}
			}
		}
	}
	line := ot["pull_request"]
	if line == 0 {
		line = ot["pull_request_target"]
	}
	return []core.Finding{removalFinding(checkRemoved, f, line, core.Warning,
		fmt.Sprintf("the pull_request trigger was removed, so this workflow's checks (%s) no longer run on pull requests", toolNames(cs)))}
}
