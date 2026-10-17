package ci

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var scriptNeutered = &core.Rule{
	ID:       "test-script-neutered",
	Title:    "Test command neutered",
	Category: "ci",
	Severity: core.Error,
	Summary:  "A test/lint script or target no longer runs its checks (`\"test\": \"echo ok\"`, `--listTests`, `-DskipTests`), or its runner gained a filter that skips tests (`-k \"not ...\"`, `--deselect`, `--passWithNoTests`).",
	Hint: "Restore the command so it runs the full suite and fix the failing tests. If some tests must be " +
		"excluded, mark those tests individually with a reason and an issue link instead of filtering them " +
		"out of the command line.",
	Docs: "`npm test`, `make test` and `tox` are the contract between a repository and its CI. Replacing " +
		"what they run with `echo`, `exit 0` or `true` keeps every pipeline green while testing nothing.\n\n" +
		"```diff\n-  \"test\": \"jest\",\n+  \"test\": \"echo 'tests temporarily disabled'\",   // BUSTED\n```\n\n" +
		"Errors: a verification script, Make/just target, tox environment or task whose body becomes a " +
		"no-op; runner flags that run nothing (`--listTests`, `--collect-only`, `cargo test --no-run`, " +
		"`go test -run '^$'`, `-DskipTests`, `gradle -x test`, `--version`).\n\n" +
		"Warnings: filters added to an existing invocation that silently drop tests: pytest `-k`/`-m \"not ...\"`, " +
		"`--deselect`, `--ignore`, `-p no:cov`; jest/vitest `--passWithNoTests`, `--testPathIgnorePatterns`, " +
		"`--testPathPattern`, `-t`, `--onlyChanged`, a fixed `--shard=1/10`; `go test -run`/`-skip` in CI; " +
		"`cargo test -- --skip`; `mvn -DskipITs`; " +
		"`--exclude-group`, `--filter` and similar. A filter is fine when the same change adds a job that runs " +
		"the excluded part (`-m \"not slow\"` plus a new `-m slow` job), or when the old version of the file " +
		"already applied the same filter (a restructured workflow). Maven `<skipTests>` in an opt-in " +
		"`<profile>` (not `activeByDefault`) only affects builds that ask for that profile and is not " +
		"reported. Exclusions in config files (pytest.ini, jest.config.js) are reported by tests-excluded.",
}

func init() {
	scriptNeutered.Check = scoped(checkNeutered)
	core.Register(scriptNeutered)
}

func checkNeutered(a *core.Analysis) []core.Finding {
	var out []core.Finding
	complements := newCommandText(a)
	for _, f := range commandFiles(a) {
		bodies, owners := neuteredBodies(f)
		out = append(out, bodies...)
		out = append(out, neuteredInvocations(f, complements, owners)...)
	}
	for _, f := range a.Files {
		if f.Config == core.CfgGradle || f.Config == core.CfgMaven {
			out = append(out, buildToolSkips(f)...)
		}
	}
	return out
}

// newCommandText is all command text on the new side of the changeset,
// used to recognize split test runs.
func newCommandText(a *core.Analysis) []string {
	var out []string
	for _, f := range commandFiles(a) {
		for _, c := range commandLines(f, newSide(f)) {
			out = append(out, c.Text)
		}
	}
	return out
}

// ---- no-op bodies ----

// ownedKinds are file kinds whose commands belong to named scripts,
// targets or environments.
func ownedKind(k fileKind) bool {
	return k == kindScripts || k == kindMake || k == kindTox || k == kindPyproject
}

// neuteredBodies reports scripts/targets/environments that used to run
// verification and now do nothing. It also returns the owners it reported,
// so their commands are not reported a second time.
func neuteredBodies(f *core.FileInfo) ([]core.Finding, map[string]bool) {
	k := kindOf(f)
	reported := map[string]bool{}
	if !ownedKind(k) || len(f.Added)+len(f.Removed) == 0 {
		return nil, reported
	}
	oldS, newS := oldSide(f), newSide(f)
	group := func(cs []cmdLine) map[string][]cmdLine {
		m := map[string][]cmdLine{}
		for _, c := range cs {
			if c.Owner != "" && !isHookOwner(c.Owner) {
				m[c.Owner] = append(m[c.Owner], c)
			}
		}
		return m
	}
	oldBy, newBy := group(commandLines(f, oldS)), group(commandLines(f, newS))
	// Owners that still exist on the new side, with the line that names them.
	exists := map[string]int{}
	for o, cs := range newBy {
		exists[o] = cs[0].Line
	}
	var deps map[string][]string
	if k == kindMake {
		var targets map[string]int
		targets, deps = makeTargetsAndDeps(newS)
		for t, n := range targets {
			if _, ok := exists[t]; !ok {
				exists[t] = n
			}
		}
	}
	changed := lineSet(f.Added)
	removed := lineSet(f.Removed)
	// Checks added elsewhere in this file can replace a script that became
	// a stub ("lint": "echo 'use npm run check'" next to a new "check").
	// A script's own text does not count: in "exit 0; jest" jest never runs.
	addedBy := map[string][]check{}
	for o, cs := range newBy {
		for _, c := range cs {
			if c.overlaps(changed) {
				addedBy[o] = append(addedBy[o], checksIn(c.Text)...)
			}
		}
	}
	addedElsewhere := func(owner string) []check {
		var out []check
		for o, cs := range addedBy {
			if o != owner {
				out = append(out, cs...)
			}
		}
		return out
	}
	var out []core.Finding
	for _, owner := range sortedOwners(oldBy) {
		line, ok := exists[owner]
		if !ok {
			continue // deleted outright: invoking it now fails loudly
		}
		var before []check
		touched := false
		for _, c := range oldBy[owner] {
			before = append(before, checksIn(c.Text)...)
			touched = touched || c.overlaps(removed)
		}
		if len(before) == 0 || !touched {
			continue
		}
		if delegatesToChecks(owner, deps, newBy) {
			continue // "test: unit integration" runs its checks through prerequisites
		}
		var texts []string
		for _, c := range newBy[owner] {
			texts = append(texts, c.Text)
			if c.overlaps(changed) {
				line = firstChanged(c, changed)
			}
		}
		reason, noop := noopReason(strings.Join(texts, "\n"))
		if !noop || allCovered(before, addedElsewhere(owner)) {
			continue
		}
		label := ownerLabel(k, owner)
		msg := fmt.Sprintf("%s used to run %s and now %s", label, toolNames(before), reason)
		out = append(out, scriptNeutered.Finding(f, line, msg))
		reported[owner] = true
	}
	return out, reported
}

// allCovered reports whether every check in lost is covered by one in by.
func allCovered(lost, by []check) bool {
	for _, l := range lost {
		ok := false
		for _, b := range by {
			if b.covers(l) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return len(lost) > 0
}

func sortedOwners(m map[string][]cmdLine) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	return sortedKeys(set)
}

func ownerLabel(k fileKind, owner string) string {
	switch k {
	case kindMake:
		return fmt.Sprintf("target %q", owner)
	case kindTox:
		return fmt.Sprintf("tox [%s]", owner)
	case kindPyproject:
		return fmt.Sprintf("task %q", owner)
	}
	return fmt.Sprintf("script %q", owner)
}

// makeTargetsAndDeps returns the rule names of a Makefile/justfile with
// their lines, and each rule's prerequisites.
func makeTargetsAndDeps(s side) (map[string]int, map[string][]string) {
	targets, deps := map[string]int{}, map[string][]string{}
	for i := range s.lines {
		l, ok := s.get(i + 1)
		if !ok || strings.HasPrefix(l.Raw, "\t") || strings.HasPrefix(l.Raw, " ") || makeAssignRe.MatchString(l.NoComment) {
			continue
		}
		for _, re := range []*regexp.Regexp{makeRuleRe, justRecipeRe} {
			m := re.FindStringSubmatch(l.NoComment)
			if m == nil {
				continue
			}
			// prerequisites follow the first ':' (and stop at an inline ';' recipe)
			pre := ""
			if j := strings.Index(l.NoComment, ":"); j >= 0 {
				pre = l.NoComment[j+1:]
				if k := strings.Index(pre, ";"); k >= 0 {
					pre = pre[:k]
				}
			}
			for _, t := range strings.Fields(m[1]) {
				if strings.HasPrefix(t, ".") || strings.HasPrefix(t, "@") {
					continue
				}
				if _, dup := targets[t]; !dup {
					targets[t] = i + 1
				}
				deps[t] = append(deps[t], strings.Fields(pre)...)
			}
			break
		}
	}
	return targets, deps
}

// delegatesToChecks reports whether a target's prerequisites run
// verification: verification-named targets, or targets whose recipes do.
func delegatesToChecks(owner string, deps map[string][]string, newBy map[string][]cmdLine) bool {
	for _, d := range deps[owner] {
		if _, ok := verificationName(d); ok {
			return true
		}
		for _, c := range newBy[d] {
			if len(checksIn(c.Text)) > 0 {
				return true
			}
		}
	}
	return false
}

var (
	trivialJSRe = regexp.MustCompile(`^\s*(process\.exit\(\s*0?\s*\)|console\.(log|info)\(.*\)|)\s*;?\s*$`)
	trivialPyRe = regexp.MustCompile(`^\s*(pass|print\(.*\)|import sys\s*;\s*sys\.exit\(\s*0?\s*\)|sys\.exit\(\s*0?\s*\)|exit\(\s*0?\s*\)|)\s*;?\s*$`)
)

// noopReason reports whether shell text does nothing useful, and why.
func noopReason(text string) (string, bool) {
	cmds := expandShell(splitShell(text))
	if len(cmds) == 0 {
		return "is empty", true
	}
	reason := ""
	for i, sc := range cmds {
		iv := parseInvocation(sc.Words)
		// Every command so far did nothing; an `exit 0` reached from here
		// ends the script before anything after it runs ("exit 0; jest").
		if i+1 < len(cmds) && isExitZero(iv) && (i == 0 || cmds[i-1].Op != "||") {
			var skipped []check
			for _, rest := range cmds[i+1:] {
				skipped = append(skipped, classifyAll(parseInvocation(rest.Words))...)
			}
			if len(skipped) > 0 {
				return "exits 0 before " + toolNames(skipped) + " can run", true
			}
		}
		r, ok := noopInvocation(iv)
		if !ok {
			return "", false
		}
		if reason == "" || r != "does nothing" {
			reason = r
		}
	}
	return reason, true
}

// isExitZero reports whether iv is `exit` or `exit 0`.
func isExitZero(iv invocation) bool {
	return iv.Prog() == "exit" && (len(iv.Args) == 1 || iv.Args[1] == "0")
}

// noopInvocation reports whether one command does nothing useful.
func noopInvocation(iv invocation) (string, bool) {
	args := iv.Args
	switch iv.Prog() {
	case "", "true", ":":
		return "does nothing", true
	case "echo", "printf":
		return "only prints " + quote(strings.Join(args[1:], " ")), true
	case "exit":
		if len(args) == 1 || args[1] == "0" {
			return "just exits 0", true
		}
	case "node", "deno", "bun":
		for i, a := range args {
			if (a == "-e" || a == "--eval" || a == "-p" || a == "--print") && i+1 < len(args) && trivialJSRe.MatchString(args[i+1]) {
				return "runs " + quote(strings.Join(args, " ")) + " instead", true
			}
		}
	case "python":
		for i, a := range args {
			if a == "-c" && i+1 < len(args) && trivialPyRe.MatchString(args[i+1]) {
				return "runs " + quote(strings.Join(args, " ")) + " instead", true
			}
		}
	}
	if c, ok := classify(iv); ok {
		if r := neuterFlag(iv, c); r != "" {
			return r, true
		}
	}
	return "", false
}

// neuterFlag returns why a verification invocation runs nothing, or "".
func neuterFlag(iv invocation, c check) string {
	args := iv.Args[1:]
	all := func(want ...string) bool {
		for _, a := range args {
			for _, w := range want {
				if a == w || strings.HasPrefix(a, w+"=") {
					return true
				}
			}
		}
		return false
	}
	switch {
	case all("--version", "--help"):
		return "only prints the version or help of " + c.Tool
	case all("--listTests", "--list-tests", "--collect-only", "--co", "--list-files", "--listFilesOnly") ||
		(c.Tool == "playwright test" && all("--list")) || iv.Prog() == "vitest" && firstPositional(args) == "list":
		return "lists the tests without running them"
	case all("--showConfig", "--show-config", "--print-config", "--fixtures", "--markers"):
		return "prints configuration instead of running " + c.Tool
	case iv.Prog() == "cargo" && all("--no-run"):
		return "compiles the tests without running them (--no-run)"
	case iv.Prog() == "mocha" && all("--dry-run"), iv.Prog() == "rspec" && all("--dry-run"):
		return "does a dry run without executing tests"
	case iv.Prog() == "go" && c.Category == catTest:
		if run, ok := flagValue(args, "-run"); ok && matchesNoTest(run) {
			return fmt.Sprintf("runs no tests (-run %s)", run)
		}
		if _, ok := flagValue(args, "-list"); ok {
			return "lists the tests without running them (-list)"
		}
	case iv.Prog() == "mvn":
		for _, a := range args {
			if a == "-DskipTests" || a == "-DskipTests=true" || a == "-Dmaven.test.skip=true" || a == "-Dmaven.test.skip" {
				return "skips all tests (" + a + ")"
			}
		}
	case iv.Prog() == "gradle":
		for i, a := range args {
			if (a == "-x" || a == "--exclude-task") && i+1 < len(args) {
				t := args[i+1]
				if j := strings.LastIndex(t, ":"); j >= 0 {
					t = t[j+1:]
				}
				if t == "test" || t == "check" {
					return "excludes the " + t + " task (" + a + " " + args[i+1] + ")"
				}
			}
		}
	}
	return ""
}

// ---- flags added to existing invocations ----

// narrowFlag describes a filter flag that drops tests.
type narrowFlag struct {
	flags  []string // spellings
	tools  []string // programs it applies to ("" = any verification command)
	why    string
	ciOnly bool // only reported in CI pipelines (go test -run is a normal dev tool)
	// fixedOnly skips values set from a variable or matrix
	// (--shard=${{ matrix.shard }}/4 runs every shard across the matrix).
	fixedOnly bool
}

var narrowFlags = []narrowFlag{
	{flags: []string{"--passWithNoTests"}, tools: []string{"jest", "vitest", "npm", "yarn", "pnpm", "bun"}, why: "passes even when no test file matches"},
	{flags: []string{"--testPathIgnorePatterns", "--testPathIgnorePattern"}, tools: []string{"jest", "npm", "yarn", "pnpm"}, why: "ignores test files"},
	{flags: []string{"-t", "--testNamePattern"}, tools: []string{"jest", "vitest"}, why: "only runs tests whose name matches"},
	{flags: []string{"--testPathPattern", "--testPathPatterns"}, tools: []string{"jest", "npm", "yarn", "pnpm"}, why: "only runs test files whose path matches"},
	{flags: []string{"--shard"}, tools: []string{"jest", "vitest", "playwright"}, why: "only runs one shard of the suite", fixedOnly: true},
	{flags: []string{"-o", "--onlyChanged", "--changedSince", "--findRelatedTests", "--changed", "--lastCommit"}, tools: []string{"jest", "vitest"}, why: "only runs tests related to changed files"},
	{flags: []string{"--exclude"}, tools: []string{"vitest", "mocha"}, why: "excludes test files"},
	{flags: []string{"--ignore"}, tools: []string{"mocha", "deno"}, why: "ignores test files"},
	{flags: []string{"-k"}, tools: []string{"pytest", "tox", "nox"}, why: "deselects tests by name"},
	{flags: []string{"-m"}, tools: []string{"pytest"}, why: "deselects tests by marker"},
	{flags: []string{"--deselect"}, tools: []string{"pytest"}, why: "deselects specific tests"},
	{flags: []string{"--ignore", "--ignore-glob"}, tools: []string{"pytest"}, why: "skips collecting test files"},
	{flags: []string{"-run"}, tools: []string{"go", "gotestsum"}, why: "only runs matching tests", ciOnly: true},
	{flags: []string{"-skip"}, tools: []string{"go", "gotestsum"}, why: "skips matching tests"},
	{flags: []string{"--skip"}, tools: []string{"cargo"}, why: "skips matching tests"},
	{flags: []string{"--exclude-group"}, tools: []string{"phpunit", "pest", "paratest"}, why: "excludes test groups"},
	{flags: []string{"--filter"}, tools: []string{"phpunit", "pest", "dotnet"}, why: "only runs matching tests"},
	{flags: []string{"--tag"}, tools: []string{"rspec"}, why: "filters examples by tag"},
	{flags: []string{"--exclude-pattern"}, tools: []string{"rspec"}, why: "excludes spec files"},
	{flags: []string{"--grep-invert", "--invert"}, tools: []string{"playwright", "mocha"}, why: "excludes matching tests"},
	{flags: []string{"-DskipITs", "-DskipITs=true", "-Dtest", "-Dit.test"}, tools: []string{"mvn"}, why: "skips or narrows the tests Maven runs"},
	{flags: []string{"--tests"}, tools: []string{"gradle"}, why: "only runs matching tests"},
	{flags: []string{"--skip"}, tools: []string{"swift"}, why: "skips matching tests"},
	{flags: []string{"--exclude", "--only"}, tools: []string{"mix"}, why: "filters tests by tag"},
	{flags: []string{"--exclude-tags", "-x"}, tools: []string{"dart", "flutter"}, why: "excludes tests by tag"},
	{flags: []string{"-E", "--exclude-regex"}, tools: []string{"ctest"}, why: "excludes matching tests"},
}

// neuteredInvocations compares each changed verification invocation with
// its previous version (same tool, same file) and reports added flags that
// run nothing or drop tests.
func neuteredInvocations(f *core.FileInfo, newText []string, skipOwners map[string]bool) []core.Finding {
	added, removed := changedCommands(f)
	if len(added) == 0 || len(removed) == 0 {
		return nil
	}
	type oldInv struct {
		iv    invocation
		owner string
		where string // CI job and step
		used  bool
	}
	ciWhere := func(s side, n int) string {
		if kindOf(f) != kindCIYAML {
			return ""
		}
		return describeOwner(yamlOwner(s, n))
	}
	var olds []*oldInv
	oside := oldSide(f)
	for _, c := range removed {
		for _, iv := range invocations(c.Text) {
			if _, ok := classify(iv); ok {
				olds = append(olds, &oldInv{iv: iv, owner: c.Owner, where: ciWhere(oside, c.Line)})
			}
		}
	}
	// match pairs a new invocation with the removed one it replaces: the
	// same tool in the same CI job and step, else the same script or
	// target, else the first of that tool.
	match := func(iv invocation, owner, where string) *oldInv {
		c, _ := classify(iv)
		var byOwner, best *oldInv
		for _, o := range olds {
			oc, _ := classify(o.iv)
			if o.used || oc.Tool != c.Tool {
				continue
			}
			if where != "" && o.where == where {
				return o
			}
			if o.owner == owner && byOwner == nil {
				byOwner = o
			}
			if best == nil {
				best = o
			}
		}
		if byOwner != nil {
			return byOwner
		}
		return best
	}
	// Filters the old side already applied somewhere (the same -skip in
	// another step of a restructured workflow) are not new, whichever old
	// command the new one is paired with.
	oldFilters := map[string]bool{}
	for _, c := range commandLines(f, oldSide(f)) {
		for _, iv := range invocations(c.Text) {
			for _, nf := range narrowFlags {
				if appliesTo(nf, iv) {
					if v, ok := anyFlag(iv.Args[1:], nf.flags); ok {
						oldFilters[iv.Prog()+"\x00"+nf.flags[0]+"\x00"+v] = true
					}
				}
			}
		}
	}
	addSet := lineSet(f.Added)
	var out []core.Finding
	for _, c := range added {
		if c.Owner != "" && skipOwners[c.Owner] {
			continue
		}
		for _, iv := range invocations(c.Text) {
			ch, ok := classify(iv)
			if !ok {
				continue
			}
			o := match(iv, c.Owner, ciWhere(newSide(f), c.Line))
			if o == nil {
				continue // a new invocation, not a modified one
			}
			o.used = true
			line := firstChanged(c, addSet)
			where := ""
			if c.Owner != "" {
				where = " in " + ownerLabel(kindOf(f), c.Owner)
			} else if kindOf(f) == kindCIYAML {
				if d := describeOwner(yamlOwner(newSide(f), line)); d != "" {
					where = " in " + d
				}
			}
			if r := neuterFlag(iv, ch); r != "" && neuterFlag(o.iv, ch) == "" {
				out = append(out, scriptNeutered.Finding(f, line,
					fmt.Sprintf("%s%s now %s (was %s)", quote(iv.String()), where, r, quote(o.iv.String()))))
				continue
			}
			for _, nf := range narrowFlags {
				if nf.ciOnly && !f.IsCI || !appliesTo(nf, iv) {
					continue
				}
				newV, inNew := anyFlag(iv.Args[1:], nf.flags)
				oldV, inOld := anyFlag(o.iv.Args[1:], nf.flags)
				switch {
				case !inNew:
					continue
				case inOld && newV == oldV:
					continue
				case inOld && strings.Count(newV, "not ") <= strings.Count(oldV, "not ") && nf.flags[0] != "--deselect" && nf.flags[0] != "--ignore":
					continue // a different selection, not a narrower one
				case nf.flags[0] == "-run" && matchesAllTests(newV):
					continue // go test -run . selects every test
				case nf.fixedOnly && strings.ContainsAny(newV, "${}%"):
					continue
				case nf.flags[0] == "--shard" && strings.HasSuffix(newV, "/1"):
					continue // --shard=1/1 is the whole suite
				case oldFilters[iv.Prog()+"\x00"+nf.flags[0]+"\x00"+newV]:
					continue
				}
				if splitRun(nf, newV, newText) {
					continue
				}
				val := ""
				switch {
				case strings.ContainsAny(newV, " \t"):
					val = " " + quote(newV)
				case newV != "":
					val = " " + newV
				}
				out = append(out, sevFinding(scriptNeutered, f, line, core.Warning,
					fmt.Sprintf("%s%s gained `%s%s`, which %s (was %s)", ch.Tool, where, nf.flags[0], val, nf.why, quote(o.iv.String()))))
			}
		}
	}
	return out
}

func appliesTo(nf narrowFlag, iv invocation) bool {
	for _, t := range nf.tools {
		if iv.Prog() == t {
			return true
		}
	}
	return false
}

// anyFlag finds any spelling of a flag among args (including after "--",
// which package managers pass on to the runner) and returns its value.
func anyFlag(args []string, spellings []string) (string, bool) {
	for i, a := range args {
		for _, f := range spellings {
			switch {
			case a == f:
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					return args[i+1], true
				}
				return "", true
			case strings.HasPrefix(a, f+"="):
				return a[len(f)+1:], true
			case len(f) == 2 && f[0] == '-' && f[1] != '-' && strings.HasPrefix(a, f) && len(a) > 2 && f != "-t" && f != "-o":
				return a[2:], true // -k"expr"
			}
		}
	}
	return "", false
}

// splitRun reports whether the change also runs what a filter excludes:
// `-m "not slow"` next to `-m slow`, `--ignore=tests/e2e` next to a job
// that runs tests/e2e.
func splitRun(nf narrowFlag, val string, newText []string) bool {
	excluded := strings.TrimSpace(val)
	switch nf.flags[0] {
	case "-k", "-m":
		if !strings.HasPrefix(excluded, "not ") {
			return false
		}
		excluded = strings.Trim(strings.TrimSpace(strings.TrimPrefix(excluded, "not ")), `"'()`)
		for _, t := range newText {
			for _, iv := range invocations(t) {
				if v, ok := anyFlag(iv.Args, nf.flags); ok && strings.Trim(v, `"'`) == excluded {
					return true
				}
			}
		}
	case "--ignore", "--ignore-glob", "--testPathIgnorePatterns", "--exclude", "--deselect":
		excluded = strings.Trim(strings.TrimSuffix(strings.Trim(excluded, `"'`), "/**"), "/*<>")
		excluded = strings.TrimPrefix(excluded, "rootDir>/")
		if excluded == "" {
			return false
		}
		for _, t := range newText {
			for _, iv := range invocations(t) {
				if _, ok := classify(iv); !ok {
					continue
				}
				for _, tg := range targets(iv) {
					if strings.Contains(normTarget(tg), excluded) {
						return true
					}
				}
			}
		}
	}
	return false
}

// ---- Gradle / Maven ----

var (
	gradleDisableRe  = regexp.MustCompile(`\b(enabled|isEnabled)\s*=\s*false\b|\benabled\.set\(\s*false\s*\)|\bonlyIf\s*\{\s*false\s*\}`)
	gradleTestFlagRe = regexp.MustCompile(`\btest\.(enabled|isEnabled)\b`)
	mavenSkipRe      = regexp.MustCompile(`<(skipTests|maven\.test\.skip|skip)>\s*true\s*</`)
)

// buildToolSkips reports test tasks disabled in Gradle and Maven builds.
func buildToolSkips(f *core.FileInfo) []core.Finding {
	s := newSide(f)
	removed := multiset{}
	for _, l := range f.Removed {
		removed.add(rulekit.Normalize(l.Code))
	}
	var out []core.Finding
	for _, l := range f.Added {
		if removed.take(rulekit.Normalize(l.Code)) {
			continue
		}
		if f.Config == core.CfgGradle {
			if !gradleDisableRe.MatchString(l.Code) {
				continue
			}
			blk := enclosingBlock(s, l.No)
			if gradleTestBlk.MatchString(blk) || gradleTestFlagRe.MatchString(l.Code) {
				out = append(out, scriptNeutered.Finding(f, l.No,
					fmt.Sprintf("the Gradle test task is disabled (%s), so `gradle test`/`check`/`build` run no tests", strings.TrimSpace(l.Code))))
			}
			continue
		}
		m := mavenSkipRe.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		if m[1] == "skip" && !insideSurefire(s, l.No) {
			continue
		}
		if prof, active := mavenProfile(s, l.No); prof != "" && !active {
			// an opt-in profile (-Pname), typically one that runs a
			// failsafe gate and leaves the unit suite to the default build
			continue
		}
		out = append(out, scriptNeutered.Finding(f, l.No,
			fmt.Sprintf("<%s>true</%s> makes Maven skip the tests in every build", m[1], m[1])))
	}
	return out
}

var (
	profileOpenRe  = regexp.MustCompile(`<profile>`)
	profileCloseRe = regexp.MustCompile(`</profile>`)
	profileIDRe    = regexp.MustCompile(`<id>\s*([^<]+?)\s*</id>`)
	activeDefRe    = regexp.MustCompile(`<activeByDefault>\s*true\s*</activeByDefault>`)
)

// mavenProfile returns the id of the pom.xml <profile> that line n is in
// ("" outside profiles), and whether that profile is active by default.
func mavenProfile(s side, n int) (id string, activeByDefault bool) {
	start := 0
	depth := 0
	for i := n; i >= 1; i-- {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		depth += len(profileCloseRe.FindAllString(l.NoComment, -1))
		opens := len(profileOpenRe.FindAllString(l.NoComment, -1))
		if opens > depth {
			start = i
			break
		}
		depth -= opens
	}
	if start == 0 {
		return "", false
	}
	id = "?"
	for i := start; i <= len(s.lines); i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		if m := profileIDRe.FindStringSubmatch(l.NoComment); m != nil && id == "?" {
			id = m[1]
		}
		if activeDefRe.MatchString(l.NoComment) {
			activeByDefault = true
		}
		if i > start && profileCloseRe.MatchString(l.NoComment) {
			break
		}
	}
	return id, activeByDefault
}

// insideSurefire reports whether line n is inside a surefire/failsafe
// plugin configuration.
func insideSurefire(s side, n int) bool {
	for i := n; i >= 1 && i >= n-30; i-- {
		if l, ok := s.get(i); ok {
			if strings.Contains(l.Raw, "maven-surefire-plugin") || strings.Contains(l.Raw, "maven-failsafe-plugin") {
				return true
			}
			if strings.Contains(l.Raw, "<artifactId>") {
				return false
			}
		}
	}
	return false
}
