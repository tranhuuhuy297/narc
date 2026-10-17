package ci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var hookBypassed = &core.Rule{
	ID:       "verification-hook-bypassed",
	Title:    "Git hooks bypassed",
	Category: "ci",
	Severity: core.Warning,
	Summary:  "Pre-commit/pre-push checks were skipped or removed: `git commit --no-verify`, `HUSKY=0`, `SKIP=mypy`, hooks deleted from .pre-commit-config.yaml, lefthook.yml or .husky/.",
	Hint: "Let the hooks run and fix what they report. If a hook is broken or too slow, fix or replace it " +
		"in its own change rather than skipping it.",
	Docs: "Git hooks are the first gate an agent's commit has to pass. Skipping them with `--no-verify`, " +
		"switching them off with environment variables, or deleting them lets unverified code into the " +
		"history.\n\n" +
		"```diff\n-\tgit commit -m \"$(MSG)\"\n+\tgit commit --no-verify -m \"$(MSG)\"    # SUSPICIOUS\n```\n\n" +
		"Covered: `--no-verify` (and `git commit -n`) in scripts, Makefiles and CI; `core.hooksPath=/dev/null`; " +
		"`HUSKY=0`, `LEFTHOOK=0` and similar on a git commit/push; pre-commit's `SKIP=` naming lint, type or " +
		"test hooks; hooks removed from .pre-commit-config.yaml (unless replaced by an equivalent hook) or " +
		"moved to the manual stage (also through `default_stages: [manual]`) or excluded from every file " +
		"(`exclude: .*`); lefthook commands removed or skipped with `skip: true` (a `skip: [merge, rebase]` " +
		"list only skips during those git operations); .husky hook files deleted, emptied or short-circuited " +
		"with an unconditional `exit 0`. " +
		"Not reported: release and bot jobs that commit generated files, test helpers that commit into a " +
		"scratch repository, and checks that move to another hook or hook manager (husky → lefthook, " +
		"pre-commit → lefthook) in the same change.",
}

func init() {
	hookBypassed.Check = scoped(checkHooks)
	core.Register(hookBypassed)
}

func checkHooks(a *core.Analysis) []core.Finding {
	var out []core.Finding
	out = append(out, hookBypassCommands(a)...)
	out = append(out, preCommitSkipEnv(a)...)
	after := hookChecksAfter(a)
	for _, f := range a.Files {
		base := strings.ToLower(path.Base(f.Path))
		switch {
		case strings.HasPrefix(base, ".pre-commit-config."):
			out = append(out, preCommitHooksRemoved(f, after)...)
		case strings.HasPrefix(base, "lefthook."):
			out = append(out, lefthookRemoved(f, after)...)
		case strings.Contains("/"+f.Path, "/.husky/") && !strings.Contains(f.Path, "/_/"):
			out = append(out, huskyHook(f, after)...)
		case f.Config == core.CfgPackageJSON:
			out = append(out, packageHooksRemoved(f, after)...)
		}
	}
	return out
}

// hookChecksAfter collects the checks that git hooks run after the change,
// across every hook manager the change touches (.husky/ files, lefthook,
// pre-commit, package.json husky/simple-git-hooks entries), so that a
// migration from one manager to another is not reported as a removal.
func hookChecksAfter(a *core.Analysis) []check {
	var out []check
	for _, f := range a.Files {
		if f.Diff.Kind == diff.Deleted {
			continue
		}
		base := strings.ToLower(path.Base(f.Path))
		switch {
		case strings.Contains("/"+f.Path, "/.husky/") && !strings.Contains(f.Path, "/_/"),
			strings.Contains("/"+f.Path, "/.githooks/"), strings.Contains("/"+f.Path, "/.git-hooks/"):
			for _, c := range commandLines(f, newSide(f)) {
				out = append(out, checksIn(c.Text)...)
			}
		case strings.HasPrefix(base, "lefthook."):
			if _, new, ok := configSides(f); ok {
				cmds, _ := lefthookCommands(new)
				for _, e := range cmds {
					out = append(out, checksIn(e.Value)...)
				}
			}
		case strings.HasPrefix(base, ".pre-commit-config."):
			if _, new, ok := configSides(f); ok {
				for _, cs := range preCommitHookChecks(new) {
					out = append(out, cs...)
				}
			}
		case f.Config == core.CfgPackageJSON:
			// lint-staged entries only run when a hook runs lint-staged
			for _, c := range commandLines(f, newSide(f)) {
				if isHookOwner(c.Owner) && !strings.HasPrefix(c.Owner, "lint-staged ") {
					out = append(out, checksIn(c.Text)...)
				}
			}
		}
	}
	return out
}

// uncovered returns the checks of lost that nothing in by covers.
func uncovered(lost, by []check) []check {
	var out []check
	for _, l := range lost {
		found := false
		for _, b := range by {
			if b.covers(l) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, l)
		}
	}
	return out
}

// botContextRe matches jobs and scripts that legitimately commit without
// running hooks: releases, version bumps, generated docs, autofixes.
var botContextRe = regexp.MustCompile(`(?i)(release|publish|bump|version|changelog|deploy|docs|autofix|auto-fix|format|update|sync|bot|generate|regenerate|bless|snapshot)`)

// gitHookBypass reports how a git invocation skips hooks, or "".
func gitHookBypass(iv invocation) string {
	if iv.Prog() != "git" {
		return ""
	}
	args := iv.Args[1:]
	// global options: -C dir, -c key=value
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-c" || args[0] == "-C") && len(args) > 1 {
			if args[0] == "-c" && hooksPathDisabled(args[1]) {
				return "-c " + args[1]
			}
			args = args[2:]
			continue
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return ""
	}
	switch sub := args[0]; sub {
	case "commit", "push", "merge", "am", "rebase":
		rest := args[1:]
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			switch {
			case a == "--":
				return ""
			case a == "--no-verify":
				return "git " + sub + " --no-verify"
			case gitValueFlags[a]:
				i++ // the value (e.g. a commit message) is not a flag
			case sub == "commit" && shortFlagCluster(a, 'n'):
				return "git commit " + a
			case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[len(a)-1:], "mFCct"):
				i++ // "-am msg": the cluster ends with a flag that takes the next word
			}
		}
	case "config":
		for i, a := range args {
			if a == "core.hooksPath" && i+1 < len(args) && hooksPathDisabled("core.hooksPath="+args[i+1]) {
				return "git config core.hooksPath " + args[i+1]
			}
		}
	}
	return ""
}

// gitValueFlags are git commit/push options whose value is the next word.
var gitValueFlags = map[string]bool{
	"-m": true, "--message": true, "-F": true, "--file": true, "-C": true, "--reuse-message": true,
	"-c": true, "--reedit-message": true, "--author": true, "--date": true, "-t": true, "--template": true,
	"--fixup": true, "--squash": true, "--trailer": true, "--cleanup": true, "-o": true, "--push-option": true,
	"--repo": true, "--receive-pack": true,
}

// hooksPathDisabled reports whether a core.hooksPath setting points hooks
// at nothing.
func hooksPathDisabled(kv string) bool {
	k, v, ok := strings.Cut(kv, "=")
	if !ok || !strings.EqualFold(k, "core.hooksPath") {
		return false
	}
	v = strings.Trim(v, `"'`)
	return v == "" || v == "/dev/null" || v == "NUL" || strings.HasPrefix(v, "/tmp")
}

// shortFlagCluster reports whether a is a short-flag cluster ("-nm",
// "-an") containing flag before any flag that takes a value.
func shortFlagCluster(a string, flag byte) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for i := 1; i < len(a); i++ {
		switch a[i] {
		case flag:
			return true
		case 'm', 'F', 'C', 'c', 't', 'S', 'u':
			return false // takes a value: the rest is the value
		}
	}
	return false
}

// hookEnv reports a variable that disables commit hooks, or "".
func hookEnv(env map[string]string) string {
	for k, v := range env {
		switch strings.ToUpper(k) {
		case "HUSKY", "LEFTHOOK", "OVERCOMMIT_DISABLE", "GIT_HOOKS":
			if falsy(v) || k == "OVERCOMMIT_DISABLE" && truthy(v) {
				return k + "=" + v
			}
		case "HUSKY_SKIP_HOOKS", "SKIP_HOOKS", "SKIP_GIT_HOOKS", "LEFTHOOK_SKIP":
			if truthy(v) || v != "" && !falsy(v) {
				return k + "=" + v
			}
		case "SKIP":
			// pre-commit's SKIP names hook ids; skipping hygiene hooks
			// (no-commit-to-branch, trailing-whitespace) is not a bypass.
			if len(skippedVerification(v)) > 0 {
				return k + "=" + v
			}
		case "LEFTHOOK_EXCLUDE":
			if v != "" {
				return k + "=" + v
			}
		}
	}
	return ""
}

// hookBypassCommands reports git invocations that skip hooks.
func hookBypassCommands(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range commandFiles(a) {
		added, removed := changedCommands(f)
		before := multiset{}
		for _, c := range removed {
			for _, iv := range invocations(c.Text) {
				if b := bypassOf(iv); b != "" {
					before.add(b)
				}
			}
		}
		if testSupportPath(f.Path) || throwawayRepo(f) {
			continue // test helpers that commit into a scratch repository
		}
		addSet := lineSet(f.Added)
		for _, c := range added {
			if botContext(f, c) {
				continue
			}
			for _, iv := range invocations(c.Text) {
				b := bypassOf(iv)
				if b == "" || before.take(b) || otherRepo(iv) {
					continue
				}
				where := ""
				if c.Owner != "" {
					where = " in " + ownerLabel(kindOf(f), c.Owner)
				}
				var msg string
				switch {
				case strings.HasPrefix(b, "git push"):
					msg = fmt.Sprintf("`%s`%s skips the pre-push hook that checks the change before it is shared", b, where)
				case strings.HasPrefix(b, "git config") || strings.HasPrefix(b, "-c "):
					msg = fmt.Sprintf("`%s`%s disables every git hook, so commits skip the lint/test hooks", b, where)
				case strings.HasPrefix(b, "git "):
					msg = fmt.Sprintf("`%s`%s skips the pre-commit and commit-msg hooks that lint and test the change", b, where)
				default:
					msg = fmt.Sprintf("`%s` on git %s%s disables the hooks that would check it", b, firstPositional(iv.Args[1:]), where)
				}
				out = append(out, hookBypassed.Finding(f, firstChanged(c, addSet), msg))
			}
		}
	}
	return out
}

// testSupportDirs are path segments of test code and fixtures.
var testSupportDirs = map[string]bool{
	"test": true, "tests": true, "testdata": true, "fixtures": true, "__fixtures__": true, "__tests__": true,
	"spec": true, "e2e": true, "testing": true, "testutil": true, "testutils": true,
}

// testSupportPath reports whether p is test code or a test fixture, such as
// a helper that builds a throwaway repository for a test.
func testSupportPath(p string) bool {
	if core.IsTestPath(p) {
		return true
	}
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		if testSupportDirs[strings.ToLower(s)] {
			return true
		}
	}
	return false
}

// throwawayRepo reports whether a script (not a CI pipeline) creates or
// clones its own repository: commits there are not the project's commits.
func throwawayRepo(f *core.FileInfo) bool {
	if f.IsCI {
		return false
	}
	for _, c := range commandLines(f, newSide(f)) {
		for _, iv := range invocations(c.Text) {
			if iv.Prog() == "git" {
				if sub := firstPositional(gitSubArgs(iv.Args[1:])); sub == "init" || sub == "clone" {
					return true
				}
			}
		}
	}
	return false
}

// otherRepo reports whether a git invocation targets another repository
// (git -C "$tmp" commit, git --git-dir=... commit).
func otherRepo(iv invocation) bool {
	args := iv.Args[1:]
	for i, a := range args {
		switch {
		case a == "-C" && i+1 < len(args):
			return strings.Trim(args[i+1], `"'`) != "." && !strings.Contains(args[i+1], "GITHUB_WORKSPACE")
		case strings.HasPrefix(a, "--git-dir") || strings.HasPrefix(a, "--work-tree"):
			return true
		case !strings.HasPrefix(a, "-"):
			return false // the subcommand: global options end here
		}
	}
	return false
}

// gitSubArgs drops git's global options (-C dir, -c key=value, --bare...).
func gitSubArgs(args []string) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if (args[0] == "-c" || args[0] == "-C") && len(args) > 1 {
			args = args[2:]
			continue
		}
		args = args[1:]
	}
	return args
}

// bypassOf combines gitHookBypass with hook-disabling variables on a
// commit/push.
func bypassOf(iv invocation) string {
	if b := gitHookBypass(iv); b != "" {
		return b
	}
	if iv.Prog() == "git" {
		sub := firstPositional(iv.Args[1:])
		if sub == "commit" || sub == "push" || sub == "merge" {
			if e := hookEnv(iv.Env); e != "" {
				return e
			}
		}
	}
	return ""
}

// botContext reports whether a command belongs to a release/bot script or
// CI job.
func botContext(f *core.FileInfo, c cmdLine) bool {
	if botContextRe.MatchString(c.Owner) {
		return true
	}
	if kindOf(f) == kindShell && botContextRe.MatchString(path.Base(f.Path)) {
		return true // scripts/release.sh, bin/bump-version
	}
	if kindOf(f) != kindCIYAML {
		return false
	}
	s := newSide(f)
	job, step := yamlOwner(s, c.Line)
	if botContextRe.MatchString(job) || botContextRe.MatchString(step) {
		return true
	}
	// workflow name
	for i := 1; i <= len(s.lines); i++ {
		if l, ok := s.get(i); ok {
			if nd, ok := parseYAMLLine(l, i); ok && nd.Indent == 0 && nd.Key == "name" {
				return botContextRe.MatchString(nd.Value)
			}
		}
	}
	// A bare patch that shows neither the job nor the workflow name cannot
	// tell a release or docs-publishing job from a check job; say nothing.
	return !s.full && job == ""
}

// skippedVerification returns the hook ids in a pre-commit SKIP list that
// verify code.
func skippedVerification(v string) []string {
	var out []string
	for _, id := range strings.Split(strings.Trim(v, `"'`), ",") {
		if id = strings.TrimSpace(id); id != "" && isVerificationHook(id) {
			out = append(out, id)
		}
	}
	return out
}

// preCommitSkipEnv reports SKIP=<verification hooks> on `pre-commit run`
// and in CI env blocks.
func preCommitSkipEnv(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range commandFiles(a) {
		added, removed := changedCommands(f)
		before := multiset{}
		for _, c := range removed {
			for _, iv := range invocations(c.Text) {
				before.add(iv.Env["SKIP"])
			}
		}
		addSet := lineSet(f.Added)
		for _, c := range added {
			for _, iv := range invocations(c.Text) {
				v, ok := iv.Env["SKIP"]
				if !ok || iv.Prog() != "pre-commit" || before.take(v) {
					continue
				}
				if ids := skippedVerification(v); len(ids) > 0 {
					out = append(out, hookBypassed.Finding(f, firstChanged(c, addSet),
						fmt.Sprintf("`SKIP=%s` on pre-commit skips %s", v, hookList(ids))))
				}
			}
		}
		if kindOf(f) != kindCIYAML {
			continue
		}
		removedEnv := multiset{}
		for _, l := range f.Removed {
			removedEnv.add(rulekit.Normalize(l.NoComment))
		}
		for _, l := range f.Added {
			m := envKeyRe.FindStringSubmatch(l.NoComment)
			if m == nil || m[1] != "SKIP" || removedEnv.take(rulekit.Normalize(l.NoComment)) {
				continue
			}
			if ids := skippedVerification(m[2]); len(ids) > 0 {
				out = append(out, hookBypassed.Finding(f, l.No,
					fmt.Sprintf("`SKIP: %s` makes pre-commit skip %s in CI", m[2], hookList(ids))))
			}
		}
	}
	return out
}

// hookList renders hook ids: "the mypy hook", "the mypy and eslint hooks".
func hookList(ids []string) string {
	if len(ids) == 1 {
		return "the " + ids[0] + " hook"
	}
	return "the " + strings.Join(ids[:len(ids)-1], ", ") + " and " + ids[len(ids)-1] + " hooks"
}

// ---- hook configuration ----

// hookFamilies group interchangeable hooks: replacing flake8 with ruff is a
// migration, not a bypass.
var hookFamilies = map[string]string{
	"flake8": "py-lint", "pylint": "py-lint", "ruff": "py-lint", "ruff-check": "py-lint", "pyflakes": "py-lint",
	"pycodestyle": "py-lint", "pydocstyle": "py-lint", "bandit": "py-lint",
	"mypy": "py-types", "pyright": "py-types", "basedpyright": "py-types", "pytype": "py-types",
	"eslint": "js-lint", "biome": "js-lint", "biome-check": "js-lint", "biome-lint": "js-lint", "oxlint": "js-lint",
	"xo": "js-lint", "standard": "js-lint", "lint-staged": "js-lint",
	"tsc": "js-types", "typecheck": "js-types", "type-check": "js-types", "vue-tsc": "js-types",
	"golangci-lint": "go-lint", "golangci-lint-full": "go-lint", "go-vet": "go-lint", "go-vet-mod": "go-lint",
	"staticcheck": "go-lint", "revive": "go-lint",
	"pytest": "py-test", "pytest-check": "py-test", "tests": "tests", "unit-tests": "tests",
	"go-unit-tests": "go-test", "go-test-mod": "go-test", "go-test": "go-test",
	"cargo-check": "rust", "clippy": "rust", "fmt": "rust",
	"rubocop": "ruby-lint", "standardrb": "ruby-lint",
	"phpstan": "php-types", "psalm": "php-types",
	"shellcheck": "shell-lint",
}

// familyChecks gives the category and ecosystem of each hook family.
var familyChecks = map[string]check{
	"py-lint": {Category: catLint, Eco: ecoPython}, "py-types": {Category: catTypes, Eco: ecoPython},
	"py-test": {Category: catTest, Eco: ecoPython}, "js-lint": {Category: catLint, Eco: ecoJS},
	"js-types": {Category: catTypes, Eco: ecoJS}, "go-lint": {Category: catLint, Eco: ecoGo},
	"go-test": {Category: catTest, Eco: ecoGo}, "rust": {Category: catLint, Eco: ecoRust},
	"ruby-lint": {Category: catLint, Eco: ecoRuby}, "php-types": {Category: catTypes, Eco: ecoPHP},
	"tests": {Category: catTest}, "shell-lint": {Category: catLint},
}

// preCommitHookChecks returns, per verification hook id, the checks it
// runs: those of its `entry:` for local hooks, else derived from the id.
func preCommitHookChecks(es []entry) map[string][]check {
	hooks, _ := preCommitHooks(es)
	out := map[string][]check{}
	for id, e := range hooks {
		if !isVerificationHook(id) {
			continue
		}
		var cs []check
		for _, entry := range groupValues(es, e.Group, "entry") {
			cs = append(cs, checksIn(entry)...)
		}
		if len(cs) == 0 {
			c, ok := familyChecks[familyOf(id)]
			if !ok {
				c.Category, ok = verificationName(id)
				if !ok {
					c.Category = catLint
				}
			}
			c.Tool = id
			cs = []check{c}
		}
		out[id] = cs
	}
	return out
}

// isVerificationHook reports whether a hook id names a lint/type/test hook.
// Hygiene hooks (check-yaml, trailing-whitespace, ...) are not counted.
func isVerificationHook(id string) bool {
	id = strings.ToLower(id)
	if _, ok := hookFamilies[id]; ok {
		return true
	}
	if strings.HasPrefix(id, "check-") || strings.HasPrefix(id, "detect-") || strings.HasPrefix(id, "fix-") {
		return false
	}
	_, ok := verificationName(id)
	return ok
}

func familyOf(id string) string {
	if f, ok := hookFamilies[strings.ToLower(id)]; ok {
		return f
	}
	return strings.ToLower(id)
}

// preCommitHooks returns hook id → entry for .pre-commit-config.yaml, and
// the ids moved to the manual stage.
func preCommitHooks(es []entry) (map[string]*entry, map[string]*entry) {
	hooks, manual := map[string]*entry{}, map[string]*entry{}
	for i := range es {
		e := &es[i]
		if len(e.Path) == 3 && e.Path[0] == "repos" && e.Path[1] == "hooks" && e.Path[2] == "id" {
			if _, ok := hooks[e.Value]; !ok {
				hooks[e.Value] = e
			}
		}
	}
	for i := range es {
		e := &es[i]
		if len(e.Path) == 3 && e.Path[1] == "hooks" && e.Path[2] == "stages" && e.Value == "manual" {
			for _, id := range groupValues(es, e.Group, "id") {
				manual[id] = e
			}
		}
	}
	return hooks, manual
}

// preCommitHooksRemoved reports verification hooks removed from
// .pre-commit-config.yaml (or the whole file deleted) whose checks no hook
// runs after the change (elsewhere), hooks moved to the manual stage, and
// hooks excluded from every file.
func preCommitHooksRemoved(f *core.FileInfo, elsewhere []check) []core.Finding {
	old, new, ok := configSides(f)
	if !ok {
		return nil
	}
	oh, om := preCommitHooks(old)
	nh, nm := preCommitHooks(new)
	oldChecks := preCommitHookChecks(old)
	var lost []string // removed hooks whose checks nothing runs any more
	families := map[string]bool{}
	for id := range nh {
		families[familyOf(id)] = true
	}
	var out []core.Finding
	if e := manualDefault(new); e != nil && manualDefault(old) == nil {
		var ids []string
		for _, id := range sortedEntryKeys(nh) {
			if isVerificationHook(id) && len(groupValues(new, nh[id].Group, "stages")) == 0 {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			out = append(out, sevFinding(hookBypassed, f, e.Line, core.Warning,
				fmt.Sprintf("`default_stages: [manual]` stops %s from running on commit", hookList(ids))))
		}
	}
	if e := matchNothing(new, 0); e != nil && matchNothing(old, 0) == nil {
		out = append(out, sevFinding(hookBypassed, f, e.Line, core.Warning,
			fmt.Sprintf("top-level `%s: %s` makes every pre-commit hook skip every file", e.Last(), e.Value)))
	}
	for _, id := range sortedEntryKeys(oh) {
		if !isVerificationHook(id) {
			continue
		}
		if ne, still := nh[id]; still {
			if e, isManual := nm[id]; isManual && om[id] == nil {
				out = append(out, sevFinding(hookBypassed, f, e.Line, core.Warning,
					fmt.Sprintf("pre-commit hook %s moved to the manual stage: it no longer runs on commit", id)))
			} else if e := matchNothing(new, ne.Group); e != nil && matchNothing(old, oh[id].Group) == nil {
				out = append(out, sevFinding(hookBypassed, f, e.Line, core.Warning,
					fmt.Sprintf("pre-commit hook %s now has `%s: %s`, so it never checks any file", id, e.Last(), e.Value)))
			}
			continue
		}
		if families[familyOf(id)] || len(uncovered(oldChecks[id], elsewhere)) == 0 {
			continue // replaced by an equivalent hook, here or in another hook manager
		}
		lost = append(lost, id)
		if f.Diff.Kind != diff.Deleted {
			out = append(out, removalFinding(hookBypassed, f, oh[id].Line, core.Warning,
				fmt.Sprintf("pre-commit hook %s removed: commits are no longer checked by it", id)))
		}
	}
	if f.Diff.Kind == diff.Deleted && len(lost) > 0 {
		out = append(out, fileFinding(hookBypassed, f, core.Warning,
			fmt.Sprintf("%s deleted: hooks %s no longer run on commit", path.Base(f.Path), strings.Join(lost, ", "))))
	}
	return out
}

// manualDefault returns the top-level default_stages entry when it limits
// hooks without their own stages to the manual stage, or nil.
func manualDefault(es []entry) *entry {
	var manual *entry
	for i := range es {
		e := &es[i]
		if len(e.Path) != 1 || e.Path[0] != "default_stages" {
			continue
		}
		switch strings.TrimSpace(e.Value) {
		case "manual":
			manual = e
		case "pre-commit", "commit", "pre-push", "push":
			return nil // the hooks still run on commit or push
		}
	}
	return manual
}

// matchAllRe and matchNoneRe are file patterns that match every path and
// no path; pre-commit's `exclude:` and `files:` take regular expressions.
var (
	matchAllRe  = regexp.MustCompile(`^\^?(\.\*|\.\+|\.|\(\.\*\))?\$?$`)
	matchNoneRe = regexp.MustCompile(`^(\^\$|\$\^|a\^)$`)
)

// matchNothing returns the exclude/files setting that stops a pre-commit
// hook (group g; 0 = the top level) from seeing any file, or nil.
func matchNothing(es []entry, g int) *entry {
	for i := range es {
		e := &es[i]
		if e.Group != g || len(e.Path) == 0 {
			continue
		}
		if g == 0 && len(e.Path) != 1 || g != 0 && (len(e.Path) != 3 || e.Path[1] != "hooks") {
			continue
		}
		v := strings.TrimSpace(e.Value)
		switch e.Last() {
		case "exclude":
			if v != "" && v != "^$" && matchAllRe.MatchString(v) {
				return e
			}
		case "files":
			if matchNoneRe.MatchString(v) {
				return e
			}
		}
	}
	return nil
}

// lefthookCommands returns "name" → run entry for lefthook commands and
// jobs, and the skip settings.
func lefthookCommands(es []entry) (cmds map[string]*entry, skips map[string]*entry) {
	cmds, skips = map[string]*entry{}, map[string]*entry{}
	for i := range es {
		e := &es[i]
		p := e.Path
		switch {
		case len(p) == 4 && (p[1] == "commands" || p[1] == "scripts") && p[3] == "run":
			cmds[p[2]] = e
		case len(p) == 3 && p[1] == "jobs" && p[2] == "run":
			name := strings.Join(groupValues(es, e.Group, "name"), "")
			if name == "" {
				name = e.Value
			}
			cmds[name] = e
		// Only `skip: true` skips unconditionally; a list such as
		// `skip: [merge, rebase]` or `- ref: main` skips in some git states.
		case len(p) == 2 && p[1] == "skip" && !e.Item && truthy(e.Value):
			skips[p[0]] = e
		case len(p) == 4 && p[1] == "commands" && p[3] == "skip" && !e.Item && truthy(e.Value):
			skips[p[2]] = e
		}
	}
	return cmds, skips
}

func lefthookRemoved(f *core.FileInfo, elsewhere []check) []core.Finding {
	old, new, ok := configSides(f)
	if !ok {
		return nil
	}
	oc, os := lefthookCommands(old)
	nc, ns := lefthookCommands(new)
	newRuns := map[string]bool{}
	for _, e := range nc {
		newRuns[rulekit.Normalize(e.Value)] = true
	}
	var out []core.Finding
	for _, name := range sortedEntryKeys(oc) {
		e := oc[name]
		cs := checksIn(e.Value)
		if _, still := nc[name]; still || newRuns[rulekit.Normalize(e.Value)] || len(cs) == 0 || len(uncovered(cs, elsewhere)) == 0 {
			continue
		}
		out = append(out, removalFinding(hookBypassed, f, e.Line, core.Warning,
			fmt.Sprintf("lefthook command %q (%s) removed: it no longer runs on commit/push", name, e.Value)))
	}
	for _, name := range sortedEntryKeys(ns) {
		if os[name] == nil {
			out = append(out, sevFinding(hookBypassed, f, ns[name].Line, core.Warning,
				fmt.Sprintf("lefthook %q is now skipped (skip: %s)", name, ns[name].Value)))
		}
	}
	return out
}

// huskyHook reports .husky hook files deleted, emptied or short-circuited.
// elsewhere holds the checks that hooks run after the change, so a check
// that moved to another hook or hook manager is not reported.
func huskyHook(f *core.FileInfo, elsewhere []check) []core.Finding {
	hook := path.Base(f.Path)
	oldCmds := commandLines(f, oldSide(f))
	var before []check
	for _, c := range oldCmds {
		before = append(before, checksIn(c.Text)...)
	}
	if len(before) == 0 {
		return nil
	}
	if f.Diff.Kind == diff.Deleted {
		lost := uncovered(before, elsewhere)
		if len(lost) == 0 {
			return nil // migrated to lefthook, pre-commit or another hook
		}
		return []core.Finding{fileFinding(hookBypassed, f, core.Warning,
			fmt.Sprintf(".husky/%s deleted: it ran %s before every %s", hook, toolNames(lost), hookEvent(hook)))}
	}
	newCmds := commandLines(f, newSide(f))
	var after []check
	addSet := lineSet(f.Added)
	depth := 0 // if/case/loop nesting: an exit inside runs only sometimes
	for _, c := range newCmds {
		cmds := expandShell(splitShell(c.Text))
		for i, sc := range cmds {
			iv := parseInvocation(sc.Words)
			if iv.has("if") || iv.has("while") || iv.has("until") {
				depth++
			}
			switch iv.Prog() {
			case "case", "for", "select":
				depth++
			case "fi", "esac", "done":
				depth--
			}
			conditional := depth > 0 || i > 0 && (cmds[i-1].Op == "&&" || cmds[i-1].Op == "||")
			if isExitZero(iv) && !conditional && c.overlaps(addSet) && len(after) == 0 {
				return []core.Finding{hookBypassed.Finding(f, c.Line,
					fmt.Sprintf("`exit 0` at the top of .husky/%s skips %s", hook, toolNames(before)))}
			}
			after = append(after, classifyAll(iv)...)
		}
	}
	lost := uncovered(uncovered(before, after), elsewhere)
	if len(lost) == 0 {
		return nil
	}
	line := 0
	if len(f.Removed) > 0 {
		line = f.AnchorForOld(f.Removed[0].No)
	}
	fd := sevFinding(hookBypassed, f, line, core.Warning,
		fmt.Sprintf(".husky/%s no longer runs %s before %s", hook, toolNames(lost), hookEvent(hook)))
	return []core.Finding{fd}
}

func hookEvent(hook string) string {
	switch hook {
	case "pre-push":
		return "push"
	case "commit-msg", "pre-commit", "prepare-commit-msg":
		return "commit"
	}
	return hook
}

// packageHooksRemoved reports husky v4 / simple-git-hooks entries in
// package.json that stopped running verification and whose checks no hook
// runs after the change (elsewhere).
func packageHooksRemoved(f *core.FileInfo, elsewhere []check) []core.Finding {
	isHook := func(owner string) bool { return isHookOwner(owner) && !strings.HasPrefix(owner, "lint-staged ") }
	collect := func(s side) map[string][]check {
		m := map[string][]check{}
		for _, c := range commandLines(f, s) {
			if isHook(c.Owner) {
				m[c.Owner] = append(m[c.Owner], checksIn(c.Text)...)
			}
		}
		return m
	}
	before, after := collect(oldSide(f)), collect(newSide(f))
	delSet := lineSet(f.Removed)
	var out []core.Finding
	for _, c := range commandLines(f, oldSide(f)) {
		if !isHook(c.Owner) || !c.overlaps(delSet) || len(before[c.Owner]) == 0 || len(after[c.Owner]) > 0 ||
			len(uncovered(before[c.Owner], elsewhere)) == 0 {
			continue
		}
		out = append(out, removalFinding(hookBypassed, f, c.Line, core.Warning,
			fmt.Sprintf("%s hook no longer runs %s", c.Owner, toolNames(before[c.Owner]))))
	}
	return out
}
