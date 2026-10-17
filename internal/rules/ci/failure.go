package ci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var failureIgnored = &core.Rule{
	ID:       "ci-failure-ignored",
	Title:    "Verification failure ignored",
	Category: "ci",
	Severity: core.Error,
	Summary:  "A test, lint or type-check command was made unable to fail: `|| true`, `continue-on-error: true`, `allow_failure: true`, `set +e`, ...",
	Hint: "Remove the failure suppression and fix what the command reports. If a check is genuinely " +
		"non-blocking (an experimental toolchain, an advisory scan), say so in the job name and a comment, " +
		"and keep the blocking checks blocking.",
	Docs: "CI is the last line of defence. When the command that runs the tests can no longer fail, a " +
		"red build turns green without anything being fixed. narc looks for the constructs that hide a " +
		"failing exit status — but only around commands that verify code (test runners, linters, type " +
		"checkers, coverage gates), so `rm -f out.log || true` is fine.\n\n" +
		"```yaml\n- run: npm test || true          # BUSTED\n- run: pytest\n  continue-on-error: true      # BUSTED\n" +
		"- uses: actions/cache@v4\n  continue-on-error: true      # fine: a cache miss is not a test failure\n```\n\n" +
		"Covered: `|| true`, `|| :`, `|| exit 0`, `|| echo ...`, `|| [ $? -eq 1 ]`, `; true`, `; exit 0`, " +
		"`set +e` (unless the exit status is captured), `if ! pytest; then echo ...; fi` and `if npm test; then " +
		"...; fi` without an else that fails, Make/tox `-` prefixes, `make -i` and `.IGNORE:`, tox " +
		"`ignore_outcome = true`, go-task `ignore_error: true`, GitHub `continue-on-error` (also through a " +
		"matrix value that is true in every entry), GitLab `allow_failure` (also `exit_codes` that include 1), " +
		"Buildkite `soft_fail`, Azure `continueOnError`, " +
		"Drone/Woodpecker `failure: ignore`, Travis/AppVeyor `allow_failures`, Jenkins " +
		"`catchError(buildResult: 'SUCCESS')`, unchecked `returnStatus: true` and a `try`/`catch` whose catch " +
		"block neither rethrows nor marks the build failed, nox `success_codes`, Gradle `ignoreFailures` and " +
		"Maven `testFailureIgnore`. A check piped into `tee`, `grep` or similar where the shell has no " +
		"pipefail (GitHub's default `bash -e`, Makefiles, package scripts, Jenkins `sh`) is a warning: the " +
		"pipeline exits with the last command's status.\n\n" +
		"Not reported: autofixer runs (`eslint --fix . || true`), a warm-up run that the same job repeats " +
		"without suppression (`mypy --install-types --non-interactive || true` then `mypy src`), test-report " +
		"steps when the test step itself still fails the job, steps that report, summarize, upload, cache or " +
		"collect telemetry (`CI analytics summary`, `Restore verify Maven repo`), and jobs or steps whose name " +
		"says they are advisory, optional, shadow or non-blocking. A comment saying so only lowers the finding " +
		"to a warning. A key set in a YAML anchor or GitLab template is judged by the jobs that use it.\n\n" +
		"Info: `continue-on-error` on a job or step that the change adds, running checks it does not remove " +
		"elsewhere: a new non-blocking check never gated the pipeline. When a later step reads the step's " +
		"outcome (`steps.<id>.outcome`), the finding is a warning that names that line, so a reviewer can " +
		"judge the replacement.",
}

func init() {
	failureIgnored.Check = scoped(checkFailureIgnored)
	core.Register(failureIgnored)
}

func checkFailureIgnored(a *core.Analysis) []core.Finding {
	var out []core.Finding
	out = append(out, shellSwallows(a)...)
	out = append(out, yamlFailureKeys(a)...)
	for _, f := range a.Files {
		switch {
		case kindOf(f) == kindMake:
			out = append(out, makeIgnoreTarget(f)...)
		case f.Config == core.CfgTox:
			out = append(out, toxIgnoreOutcome(f)...)
		}
		switch {
		case kindOf(f) == kindJenkins:
			out = append(out, jenkinsSwallows(f)...)
		case strings.EqualFold(path.Base(f.Path), "noxfile.py"):
			out = append(out, noxSuccessCodes(f)...)
		case f.Config == core.CfgGradle || f.Config == core.CfgMaven:
			out = append(out, buildToolIgnores(f)...)
		}
	}
	return out
}

// ---- shell constructs ----

// swallow is a construct that makes a failing command look successful.
type swallow struct {
	Form    string  // "|| true", "; exit 0", "set +e", "prefix", "if", ...
	Guarded []check // the verification commands whose failure it hides
	// Detail says how an `if` handles the failure ("it only runs echo").
	Detail string
}

// swallowsIn finds swallowed verification commands in one command line.
func swallowsIn(text string) []swallow {
	cmds := expandShell(splitShell(text))
	var out []swallow
	for j := 1; j < len(cmds); j++ {
		op := cmds[j-1].Op
		if op != "||" && op != ";" {
			continue
		}
		form := swallowForm(cmds[j].Words, op)
		if form == "" {
			continue
		}
		if op == "||" && failsLater(cmds[j:]) {
			continue // pytest || (echo "tests failed"; exit 1)
		}
		if op == ";" && j != len(cmds)-1 {
			continue // "; true" only swallows as the last command
		}
		// The swallow covers the and/or list before it (for "||") or
		// everything before it (for "; exit 0" in a script without -e).
		k := j - 1
		for op == "||" && k > 0 && (cmds[k-1].Op == "&&" || cmds[k-1].Op == "||" || cmds[k-1].Op == "|") {
			k--
		}
		if op == ";" {
			k = 0
		}
		var guarded []check
		for _, c := range cmds[k:j] {
			iv := parseInvocation(c.Words)
			if fixMode(iv) {
				continue // eslint --fix . || true: an autofixer, not the check
			}
			guarded = append(guarded, classifyAll(iv)...)
		}
		if len(guarded) > 0 {
			out = append(out, swallow{Form: form, Guarded: guarded})
		}
	}
	return out
}

// swallowForm reports whether words (following op) neutralize a failure.
func swallowForm(words []string, op string) string {
	if len(words) == 0 {
		return ""
	}
	switch progName(words[0]) { // "/bin/true" is true
	case "true", ":":
		return op + " " + words[0]
	case "exit", "return":
		if len(words) == 1 || words[1] == "0" {
			return op + " " + words[0] + " 0"
		}
	case "echo", "printf":
		if op == "||" {
			return "|| " + words[0]
		}
	case "[", "[[", "test", "$?":
		// `|| [ $? -eq 1 ]` accepts exactly the status that test runners
		// and linters use for "failures found".
		if op == "||" && acceptsStatusOne(words) {
			return "|| [ $? -eq 1 ]"
		}
	}
	return ""
}

// acceptsStatusOne reports whether a test command compares $? with 1.
func acceptsStatusOne(words []string) bool {
	for i := 0; i+2 < len(words); i++ {
		if words[i] == "$?" && (words[i+1] == "-eq" || words[i+1] == "==" || words[i+1] == "=") && words[i+2] == "1" {
			return true
		}
	}
	return false
}

// failsLater reports whether commands contain an explicit failure (exit 1,
// false), as in `pytest || { echo "failed"; exit 1; }`.
func failsLater(cmds []shellCmd) bool {
	for _, c := range cmds {
		if len(c.Words) == 0 {
			continue
		}
		switch c.Words[0] {
		case "false":
			return true
		case "exit", "return":
			if len(c.Words) > 1 && c.Words[1] != "0" {
				return true
			}
		}
	}
	return false
}

// shellSwallowFinding is a candidate before cross-file cancellation.
type shellSwallowFinding struct {
	f    *core.FileInfo
	line int
	sw   swallow
	cmd  cmdLine
}

// shellSwallows reports swallowed verification commands in CI steps,
// package scripts, Makefiles, tox and shell scripts. A construct that was
// already there (moved or re-indented, or the command around it edited) is
// not reported: removed swallows cancel added ones of the same form.
func shellSwallows(a *core.Analysis) []core.Finding {
	removed := multiset{}
	var cands []shellSwallowFinding
	for _, f := range commandFiles(a) {
		added, gone := changedCommands(f)
		for _, c := range gone {
			for _, sw := range commandSwallows(f, oldSide(f), c) {
				removed.add(sw.Form + "|" + sw.Guarded[0].Category)
			}
		}
		addSet := lineSet(f.Added)
		s := newSide(f)
		for _, c := range added {
			for _, sw := range commandSwallows(f, s, c) {
				cands = append(cands, shellSwallowFinding{f: f, line: swallowLine(s, c, sw, addSet), sw: sw, cmd: c})
			}
		}
		// `if` statements that may span several lines of a step or script
		for _, b := range ifStatements(f, oldSide(f), lineSet(f.Removed)) {
			for _, sw := range ifSwallows(b.Text) {
				removed.add(sw.Form + "|" + sw.Guarded[0].Category)
			}
		}
		for _, b := range ifStatements(f, s, addSet) {
			for _, sw := range ifSwallows(b.Text) {
				cands = append(cands, shellSwallowFinding{f: f, line: firstChanged(b, addSet), sw: sw, cmd: b})
			}
		}
	}
	var out []core.Finding
	for _, c := range cands {
		if removed.take(c.sw.Form + "|" + c.sw.Guarded[0].Category) {
			continue
		}
		if reportConsumed(c.f, c.cmd) {
			continue // eslint -o report.json || true, then a step that fails on the report
		}
		if kindOf(c.f) == kindCIYAML && nonGating(newSide(c.f), c.line) {
			continue // after_script cannot fail the job anyway
		}
		byName, byComment := declaredNonBlocking(c.f, newSide(c.f), c.line)
		if byName {
			continue // a job or step named "advisory", "optional", ...
		}
		if c.sw.Form != "set +e" && rerunStrictly(c.f, newSide(c.f), c.cmd, c.sw.Guarded) {
			continue // a warm-up run (mypy --install-types || true) followed by the real gate
		}
		where := ""
		if c.cmd.Owner != "" {
			where = " in " + ownerLabel(kindOf(c.f), c.cmd.Owner)
		} else if kindOf(c.f) == kindCIYAML {
			if d := describeOwner(yamlOwner(newSide(c.f), c.line)); d != "" {
				where = " in " + d
			}
		}
		var msg string
		switch c.sw.Form {
		case "set +e":
			msg = fmt.Sprintf("`set +e`%s lets %s fail without failing the step, and the exit status is never checked", where, toolNames(c.sw.Guarded))
		case "prefix":
			msg = fmt.Sprintf("a leading \"-\" on the %s line%s tells %s to ignore its exit status", toolNames(c.sw.Guarded), where, recipeTool(c.f))
		case "| tee", "| cat", "| sed", "| awk", "| grep", "| head", "| tail", "| less", "| more", "| ts", "| sort", "| uniq", "| tr", "| cut":
			msg = fmt.Sprintf("piping %s into %s%s hides its exit status (no `set -o pipefail`), so a failing run passes", toolNames(c.sw.Guarded), strings.TrimPrefix(c.sw.Form, "| "), where)
		case "make -i":
			msg = fmt.Sprintf("`make -i`%s ignores the exit status of every recipe line, so failing %s cannot fail the build", where, toolNames(c.sw.Guarded))
		case "if":
			msg = fmt.Sprintf("the `if` around %s%s handles its failure without failing (%s), so the check can no longer fail the build", toolNames(c.sw.Guarded), where, c.sw.Detail)
		default:
			msg = fmt.Sprintf("`%s` after %s%s turns a failing check into a pass", c.sw.Form, toolNames(c.sw.Guarded), where)
		}
		fd := failureIgnored.Finding(c.f, c.line, msg)
		if strings.HasPrefix(c.sw.Form, "| ") {
			fd.Severity = core.Warning // often an accident of logging, not a decision
			fd.Hint = "Add `set -o pipefail` (or `shell: bash` in GitHub Actions) so the check's failure still fails the step, " +
				"or write the log with the runner's own option (--junitxml, -json, --reporter) instead of a pipe."
		}
		if byComment {
			fd.Severity = core.Warning
			fd.Message += nonBlockingNote
		}
		out = append(out, fd)
	}
	return out
}

// ---- if statements ----

// ifStatements returns the outermost `if ... fi` statements among the
// commands of one side of f whose lines intersect set, each joined into one
// command line. A statement may span several lines of one script (a CI
// step, a shell file); Makefile recipes and package scripts run each line
// on its own, so there it must fit on one line.
func ifStatements(f *core.FileInfo, s side, set map[int]bool) []cmdLine {
	if len(set) == 0 {
		return nil
	}
	cmds := commandLines(f, s)
	var out []cmdLine
	for i := 0; i < len(cmds); i++ {
		if !opensIf(cmds[i].Text) {
			continue
		}
		depth := ifDepth(cmds[i].Text)
		b := cmds[i]
		j := i
		for depth > 0 && j+1 < len(cmds) && cmds[j+1].block() == b.block() {
			j++
			b.Text += "\n" + cmds[j].Text
			b.End = cmds[j].End
			depth += ifDepth(cmds[j].Text)
		}
		if depth == 0 && b.overlaps(set) {
			out = append(out, b)
		}
		i = j
	}
	return out
}

// opensIf reports whether shell text contains an `if`.
func opensIf(text string) bool {
	for _, iv := range invocations(text) {
		if iv.has("if") {
			return true
		}
	}
	return false
}

// ifDepth returns the number of `if`s that text opens minus the `fi`s that
// close them.
func ifDepth(text string) int {
	d := 0
	for _, iv := range invocations(text) {
		if iv.has("if") {
			d++
		}
		if iv.Prog() == "fi" {
			d--
		}
	}
	return d
}

// ifStmt is a parsed `if` statement: the condition, the commands of the
// then branch and of the else branch.
type ifStmt struct {
	cond, then, els []invocation
	hasElse, elif   bool
}

// parseIf parses the `if` statement that starts at ivs[0] and returns it
// with the number of invocations it spans.
func parseIf(ivs []invocation) (ifStmt, int) {
	var st ifStmt
	part := &st.cond
	depth := 0
	for k, iv := range ivs {
		top := depth == 0
		if k > 0 && iv.has("if") {
			depth++
		}
		switch {
		case k == 0:
		case top && iv.has("then"):
			part = &st.then
		case top && iv.has("elif"):
			st.elif = true
			part = &st.cond
		case top && iv.has("else"):
			st.hasElse = true
			part = &st.els
		case iv.Prog() == "fi" && top:
			return st, k + 1
		case iv.Prog() == "fi":
			depth--
		}
		if iv.Prog() != "" && iv.Prog() != "fi" || len(iv.Env) > 0 {
			*part = append(*part, iv)
		}
	}
	return st, len(ivs)
}

// ifSwallows reports `if` statements whose condition is a verification
// command and whose failure branch does not fail: `if ! pytest; then echo
// "tests failed"; fi`, or `if npm test; then echo ok; fi` with no else.
// Both exit 0 when the check fails, and an `if` condition is exempt from
// `set -e`.
func ifSwallows(text string) []swallow {
	ivs := invocations(text)
	var out []swallow
	for k := 0; k < len(ivs); k++ {
		if !ivs[k].has("if") {
			continue
		}
		st, n := parseIf(ivs[k:])
		k += n - 1
		if st.elif || len(st.cond) != 1 {
			continue // compound conditions and elif chains: too many paths to judge
		}
		cond := st.cond[0]
		if fixMode(cond) {
			continue
		}
		checks := classifyAll(cond)
		if len(checks) == 0 {
			continue
		}
		failure, detail := st.els, "there is no else branch"
		if cond.has("!") {
			failure, detail = st.then, ""
		} else if st.hasElse {
			detail = ""
		}
		if branchFails(failure) || autofixes(failure, cond) {
			continue
		}
		if detail == "" {
			detail = "the failure branch only runs " + branchProgs(failure)
		}
		out = append(out, swallow{Form: "if", Guarded: checks, Detail: detail})
	}
	return out
}

// branchFails reports whether the commands of an if branch fail the step
// or record the failure: exit 1, false, a check rerun as the last command
// (its status becomes the statement's), or a variable assignment that a
// later `exit $status` can use.
func branchFails(ivs []invocation) bool {
	for i, iv := range ivs {
		switch iv.Prog() {
		case "false", "fail", "die", "abort", "error":
			return true
		case "exit", "return":
			if len(iv.Args) > 1 && iv.Args[1] != "0" {
				return true
			}
		case "":
			if len(iv.Env) > 0 {
				return true // FAILED=1: collected and checked later
			}
		}
		if i == len(ivs)-1 && len(classifyAll(iv)) > 0 && !fixMode(iv) {
			return true // a verbose rerun whose status the if returns
		}
	}
	return false
}

// autofixes reports whether a failure branch repairs what the check found
// (prettier --check, then prettier --write and a commit): an autofix
// workflow, whose push re-runs the checks.
func autofixes(branch []invocation, cond invocation) bool {
	for _, iv := range branch {
		if fixMode(iv) || iv.Prog() == cond.Prog() && hasArg(iv.Args, "--write", "--fix", "-w") {
			return true
		}
		if iv.Prog() == "git" && (firstPositional(iv.Args[1:]) == "commit" || firstPositional(iv.Args[1:]) == "push") {
			return true
		}
	}
	return false
}

// branchProgs names the programs a branch runs: "echo", "echo and cat".
func branchProgs(ivs []invocation) string {
	var names []string
	seen := map[string]bool{}
	for _, iv := range ivs {
		if p := iv.Prog(); p != "" && !seen[p] {
			seen[p] = true
			names = append(names, p)
		}
	}
	switch len(names) {
	case 0:
		return "nothing"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// nonBlockingNote qualifies findings on checks that only a comment calls
// non-blocking: a comment is not a reviewed decision, a job name is.
const nonBlockingNote = " (a comment calls it non-blocking; if it really is, name the job or step so, or waive it with narc:allow and a reason)"

// swallowLine picks the line to report for a swallow in a command that may
// span several lines: the added line that holds the construct ("|| true"
// at the end of a backslash-continued command), else the first added line.
func swallowLine(s side, c cmdLine, sw swallow, addSet map[int]bool) int {
	words := strings.Fields(sw.Form)
	if sw.Form == "prefix" {
		words = nil
	}
	for n := c.Line; n <= c.End && len(words) > 0; n++ {
		l, ok := s.get(n)
		if !ok || !addSet[n] {
			continue
		}
		all := true
		for _, w := range words {
			all = all && strings.Contains(l.NoComment, w)
		}
		if all {
			return n
		}
	}
	return firstChanged(c, addSet)
}

// rerunStrictly reports whether every guarded check runs again later in the
// same job or script without its failure being ignored, as in
// `mypy --install-types --non-interactive src || true` followed by
// `mypy src`: the first run only prepares, the second one gates.
func rerunStrictly(f *core.FileInfo, s side, at cmdLine, guarded []check) bool {
	k := kindOf(f)
	job := ""
	if k == kindCIYAML {
		job, _ = yamlOwner(s, at.Line)
	}
	later := map[string]bool{}
	for _, c := range commandLines(f, s) {
		if c.Line <= at.End || c.Owner != at.Owner || c.IgnoreErr || len(swallowsIn(c.Text)) > 0 {
			continue
		}
		if inAlternative(s, at.End, c.Line) {
			continue // an else branch is an alternative, not a second run
		}
		if k == kindCIYAML {
			if j, _ := yamlOwner(s, c.Line); j != job || stepIgnoresFailure(s, c.Line) {
				continue
			}
		}
		for _, iv := range invocations(c.Text) {
			if ch, ok := classify(iv); ok && !fixMode(iv) && neuterFlag(iv, ch) == "" {
				later[ch.Tool] = true
			}
		}
	}
	for _, g := range guarded {
		if !later[g.Tool] {
			return false
		}
	}
	return len(guarded) > 0
}

// Lines that switch to an alternative branch of a shell or Make
// conditional, and lines that close the conditional.
var (
	altBranchRe = regexp.MustCompile(`^\s*(\}\s*)?(else|elif)\b|;;\s*$`)
	endBranchRe = regexp.MustCompile(`^\s*(endif|fi|esac)\b`)
)

// inAlternative reports whether line b sits in another branch of a
// conditional than line a: an else/elif (or case arm) starts after a and
// is not closed before b.
func inAlternative(s side, a, b int) bool {
	alt := false
	for i := a + 1; i <= b; i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		switch {
		case altBranchRe.MatchString(l.NoComment):
			alt = true
		case endBranchRe.MatchString(l.NoComment):
			alt = false
		}
	}
	return alt
}

// stepIgnoresFailure reports whether the YAML step or job around line n
// carries a failure key that ignores its failures.
func stepIgnoresFailure(s side, n int) bool {
	ignores := func(i int) bool {
		l, ok := s.get(i)
		if !ok {
			return false
		}
		m := failureKeyRe.FindStringSubmatch(l.NoComment)
		return m != nil && ignoresFailure(m[1], strings.Trim(m[2], `"'`), s, i)
	}
	start, end := yamlBlock(s, n)
	for i := start; i <= end; i++ {
		if ignores(i) {
			return true
		}
	}
	// job-level keys: the direct children of the job mapping
	job := jobBlock(s, n)
	child := -1
	for i := job[0] + 1; i <= job[1]; i++ {
		l, ok := s.get(i)
		if !ok || strings.TrimSpace(l.NoComment) == "" {
			continue
		}
		ind := indentOf(strings.TrimRight(l.NoComment, " \t"))
		if child < 0 {
			child = ind
		}
		if ind == child && ignores(i) {
			return true
		}
	}
	return false
}

// jobBlock returns the line range of the CI job that contains line n: the
// mapping under "jobs:"/"stages:" (GitHub, CircleCI, Azure) or the
// top-level key (GitLab). It returns the whole side when no job is found.
func jobBlock(s side, n int) [2]int {
	anc := yamlAncestors(s, n)
	for i, a := range anc {
		if (a.Key == "jobs" || a.Key == "stages") && i > 0 {
			c := anc[i-1]
			at := c.Indent
			if c.Dash >= 0 {
				at = c.Dash
			}
			return [2]int{c.Line, blockEnd(s, c.Line, at)}
		}
	}
	if len(anc) > 0 {
		top := anc[len(anc)-1]
		if top.Indent == 0 && top.Dash < 0 && !gitlabReserved[top.Key] && top.Key != "steps" {
			return [2]int{top.Line, blockEnd(s, top.Line, 0)}
		}
	}
	return [2]int{1, len(s.lines)}
}

// reportFlagRe finds report files a command writes.
var reportFlagRe = regexp.MustCompile(`(?:--?(?:o|output|output-file|outputFile|out|junitxml|junit-xml|json-report-file|report|reports|format-out|sarif-file)[=\s]+|>\s*|\btee\s+(?:-a\s+)?)["']?([\w./${}-]+\.(?:json|xml|sarif|txt|log|html|out))`)

// reportConsumed reports whether a CI command whose failure is swallowed
// writes a report that another line of the pipeline reads: the common
// "collect results, then let a reporter/annotator step fail" pattern.
func reportConsumed(f *core.FileInfo, c cmdLine) bool {
	if kindOf(f) != kindCIYAML {
		return false
	}
	ms := reportFlagRe.FindAllStringSubmatch(c.Text, -1)
	if len(ms) == 0 {
		return false
	}
	s := newSide(f)
	for _, m := range ms {
		name := path.Base(m[1])
		for i := 1; i <= len(s.lines); i++ {
			if i >= c.Line && i <= c.End {
				continue
			}
			if l, ok := s.get(i); ok && strings.Contains(l.NoComment, name) && !archivesOnly(s, i) {
				return true
			}
		}
	}
	return false
}

// storageActions only store or upload files; a step that uses one does not
// fail on the report it is given.
var storageActions = []string{"actions/upload-artifact", "actions/download-artifact", "actions/cache",
	"codecov/codecov-action", "coverallsapp/github-action", "actions/upload-pages-artifact"}

// archivesOnly reports whether line n belongs to a step that only stores
// files (uploads an artifact, saves a cache), so naming a report there does
// not make the report's findings fail the build.
func archivesOnly(s side, n int) bool {
	start, end := stepBlock(s, n)
	for i := start; i <= end; i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		if nd, ok := parseYAMLLine(l, i); ok && normKey(nd.Key) == "uses" {
			u := strings.ToLower(nd.Value)
			for _, a := range storageActions {
				if strings.HasPrefix(u, a) {
					return true
				}
			}
		}
	}
	return false
}

// declaredNonBlocking reports whether the author said the check is meant
// to be non-blocking: in the CI job/step name (byName), or only in a
// comment on the line or the line above (byComment). A name is part of
// the pipeline that reviewers see in every run; a comment next to
// `|| true` is also exactly what a cheating edit would add, so callers
// lower the severity for it instead of dropping the finding.
func declaredNonBlocking(f *core.FileInfo, s side, n int) (byName, byComment bool) {
	if kindOf(f) == kindCIYAML {
		job, step := yamlOwner(s, n)
		if nonBlockingRe.MatchString(job) || nonBlockingRe.MatchString(step) {
			return true, false
		}
	}
	for _, i := range []int{n, n - 1} {
		if l, ok := s.get(i); ok && nonBlockingRe.MatchString(l.Comment) {
			return false, true
		}
	}
	return false, false
}

func recipeTool(f *core.FileInfo) string {
	if kindOf(f) == kindTox {
		return "tox"
	}
	if strings.EqualFold(path.Base(f.Path), "justfile") {
		return "just"
	}
	return "make"
}

// commandSwallows finds swallows on one command line, including Make/tox
// "-" prefixes and `set +e`.
func commandSwallows(f *core.FileInfo, s side, c cmdLine) []swallow {
	out := swallowsIn(c.Text)
	if c.IgnoreErr {
		if cs := checksIn(c.Text); len(cs) > 0 {
			out = append(out, swallow{Form: "prefix", Guarded: cs})
		}
	}
	cmds := expandShell(splitShell(c.Text))
	if sw, ok := maskingPipe(cmds); ok && !pipefail(f, s, c) {
		out = append(out, sw)
	}
	for _, sc := range cmds {
		if iv := parseInvocation(sc.Words); makeIgnoresErrors(iv) {
			if cs := classifyAll(iv); len(cs) > 0 {
				out = append(out, swallow{Form: "make -i", Guarded: cs})
			}
		}
	}
	for i, sc := range cmds {
		if !isSetPlusE(parseInvocation(sc.Words)) {
			continue
		}
		var rest []string
		for _, r := range cmds[i+1:] {
			rest = append(rest, joinWords(r.Words))
		}
		if cs := uncheckedAfterSetE(f, s, c, strings.Join(rest, "; ")); len(cs) > 0 {
			out = append(out, swallow{Form: "set +e", Guarded: cs})
		}
	}
	return out
}

// pipeSinks are programs a check's output is piped into for logging or
// filtering; the pipeline's status is theirs, not the check's.
var pipeSinks = map[string]bool{"tee": true, "cat": true, "sed": true, "awk": true, "grep": true, "head": true,
	"tail": true, "less": true, "more": true, "ts": true, "sort": true, "uniq": true, "tr": true, "cut": true}

// maskingPipe finds a verification command piped into a sink such as tee:
// without `set -o pipefail` the pipeline exits with tee's status, so the
// check cannot fail the step.
func maskingPipe(cmds []shellCmd) (swallow, bool) {
	for j := 0; j+1 < len(cmds); j++ {
		if cmds[j].Op != "|" {
			continue
		}
		iv := parseInvocation(cmds[j].Words)
		cs := classifyAll(iv)
		if len(cs) == 0 || fixMode(iv) {
			continue
		}
		// the pipeline's last command decides its status
		k := j + 1
		for k+1 < len(cmds) && cmds[k].Op == "|" {
			k++
		}
		last := parseInvocation(cmds[k].Words)
		if !pipeSinks[last.Prog()] || last.Prog() == "grep" && hasArg(last.Args, "-q", "--quiet") {
			continue // a grep -q gate decides on purpose
		}
		return swallow{Form: "| " + last.Prog(), Guarded: cs}, true
	}
	return swallow{}, false
}

// pipefailRe matches `set -o pipefail` (also inside a flag cluster such as
// `set -euo pipefail`) and shells started with it.
var pipefailRe = regexp.MustCompile(`\bset\s+-[a-z]*o\s+pipefail\b|-o\s*pipefail|\bpipefail\b|PIPESTATUS`)

// pipefail reports whether a pipeline on command line c runs with
// pipefail, so a failing check in it still fails the pipeline: GitLab,
// CircleCI and Buildkite enable it by default, GitHub Actions does for
// `shell: bash`, and scripts can `set -o pipefail`. Unknown platforms count
// as pipefail, so nothing is reported there.
func pipefail(f *core.FileInfo, s side, c cmdLine) bool {
	switch kindOf(f) {
	case kindMake, kindScripts:
		return pipefailRe.MatchString(c.Text) // each line runs in its own `sh -c`
	case kindShell:
		for n := 1; n <= c.End+1 && n <= len(s.lines); n++ { // up to the line after (exit ${PIPESTATUS[0]})
			if l, ok := s.get(n); ok && pipefailRe.MatchString(l.NoComment) {
				return true
			}
		}
		return !s.full // a bare patch may hide the set -o pipefail at the top
	case kindJenkins:
		return pipefailRe.MatchString(c.Text)
	case kindCIYAML:
		if !isWorkflowPath(f.Path) {
			return true
		}
		start, end := stepBlock(s, c.Line)
		for n := start; n <= end; n++ {
			if l, ok := s.get(n); ok && pipefailRe.MatchString(l.NoComment) {
				return true // set -o pipefail in the step, or ${PIPESTATUS[0]} checked
			}
		}
		// GitHub's default shell on Linux and macOS is `bash -e {0}`;
		// `shell: bash` adds -o pipefail. The step's shell wins, then the
		// job's defaults.run.shell, then the workflow's.
		job := jobBlock(s, c.Line)
		if v, ok := yamlValueIn(s, start, end, "shell", -1); ok {
			return pipefailShell(v)
		}
		if v, ok := yamlValueIn(s, job[0], job[1], "shell", 2); ok { // jobs.<id>.defaults
			return pipefailShell(v)
		}
		if v, ok := yamlValueIn(s, 1, len(s.lines), "shell", 0); ok { // top-level defaults
			return pipefailShell(v)
		}
		if v, ok := yamlValueIn(s, job[0], job[1], "runs-on", -1); ok && strings.Contains(strings.ToLower(v), "windows") {
			return true // the default shell is PowerShell
		}
		return !s.full // a bare patch may hide the shell setting
	}
	return true
}

// pipefailShell reports whether a GitHub `shell:` value runs a pipeline
// with pipefail (or is not a POSIX shell at all).
func pipefailShell(v string) bool {
	v = strings.TrimSpace(v)
	return v == "bash" || pipefailRe.MatchString(v) || !strings.HasPrefix(v, "bash") && !strings.HasPrefix(v, "sh")
}

// yamlValueIn returns the value of the first key named key in lines
// start..end. With defaultsAt >= 0 the key must sit under a `defaults:`
// mapping at that depth below the document root (0: the workflow's
// defaults, 2: jobs.<id>.defaults).
func yamlValueIn(s side, start, end int, key string, defaultsAt int) (string, bool) {
	for n := start; n <= end; n++ {
		l, ok := s.get(n)
		if !ok {
			continue
		}
		nd, ok := parseYAMLLine(l, n)
		if !ok || nd.Key != key {
			continue
		}
		if defaultsAt >= 0 {
			anc := yamlAncestors(s, n)
			found := false
			for i, a := range anc {
				if a.Key == "defaults" && len(anc)-1-i == defaultsAt {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		return nd.Value, true
	}
	return "", false
}

// makeIgnoresErrors reports `make -i` / `make --ignore-errors` (also in a
// short-flag cluster such as -ik).
func makeIgnoresErrors(iv invocation) bool {
	if p := iv.Prog(); p != "make" && p != "gmake" {
		return false
	}
	for _, a := range iv.Args[1:] {
		if a == "--ignore-errors" || len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], 'i') && !strings.ContainsAny(a[1:], "CfjIlo") {
			return true
		}
	}
	return false
}

// makeIgnoreRe matches the .IGNORE special target, whose prerequisites'
// recipes (every recipe, when it has none) ignore errors.
var makeIgnoreRe = regexp.MustCompile(`^\.IGNORE\s*:([^=]*)$`)

// makeIgnoreTarget reports an added `.IGNORE:` that covers verification
// targets.
func makeIgnoreTarget(f *core.FileInfo) []core.Finding {
	if strings.EqualFold(path.Base(f.Path), "justfile") {
		return nil
	}
	s := newSide(f)
	var out []core.Finding
	for _, l := range f.Added {
		m := makeIgnoreRe.FindStringSubmatch(strings.TrimSpace(l.NoComment))
		if m == nil {
			continue
		}
		names := map[string]bool{}
		for _, t := range strings.Fields(m[1]) {
			names[t] = true
		}
		var cs []check
		for _, c := range makeCommands(s, false) {
			if len(names) == 0 || names[c.Owner] {
				cs = append(cs, checksIn(c.Text)...)
			}
		}
		if len(cs) == 0 {
			continue
		}
		what := "every target"
		if len(names) > 0 {
			what = "target " + strings.Join(sortedKeys(names), ", ")
		}
		out = append(out, failureIgnored.Finding(f, l.No,
			fmt.Sprintf("`.IGNORE:` makes make ignore errors in %s, so failing %s cannot fail the build", what, toolNames(cs))))
	}
	return out
}

// toxIgnoreOutcome reports `ignore_outcome = true` added to a tox
// environment that runs checks: tox then only warns when they fail.
func toxIgnoreOutcome(f *core.FileInfo) []core.Finding {
	old, new, ok := configSides(f)
	if !ok {
		return nil
	}
	was := map[string]bool{}
	for _, e := range old {
		if len(e.Path) == 2 && normKey(e.Path[1]) == "ignore-outcome" && truthy(e.Value) {
			was[e.Path[0]] = true
		}
	}
	commands := map[string][]check{}
	hasCommands := map[string]bool{}
	for _, c := range toxCommands(newSide(f)) {
		commands[c.Owner] = append(commands[c.Owner], checksIn(c.Text)...)
		hasCommands[c.Owner] = true
	}
	var out []core.Finding
	for _, e := range new {
		if len(e.Path) != 2 || normKey(e.Path[1]) != "ignore-outcome" || !truthy(e.Value) || was[e.Path[0]] {
			continue
		}
		env := e.Path[0]
		cs := commands[env]
		if env == "testenv" {
			// [testenv] settings apply to every environment that does not
			// override them, and so do its commands.
			for _, sec := range sortedOwnersOf(commands) {
				if strings.HasPrefix(sec, "testenv:") && !overrides(new, sec, "ignore-outcome") {
					cs = append(cs, commands[sec]...)
				}
			}
		} else if !hasCommands[env] {
			cs = commands["testenv"] // the environment inherits the base commands
		}
		if len(cs) == 0 || prereleaseRe.MatchString(env) {
			continue // an experimental interpreter (py3-dev, pypy-nightly) may fail
		}
		fd := failureIgnored.Finding(f, e.Line,
			fmt.Sprintf("`ignore_outcome = %s` in tox [%s]: failing %s only produce a warning and tox still succeeds", e.Value, env, toolNames(cs)))
		if pyVersionEnvRe.MatchString(env) {
			// tox documents ignore_outcome for upcoming Python versions
			fd.Severity = core.Warning
			fd.Message += " (if this is a pre-release interpreter, say so in the environment name)"
		}
		out = append(out, fd)
	}
	return out
}

// pyVersionEnvRe matches tox environments named after one interpreter
// version: [testenv:py314], [testenv:pypy310].
var pyVersionEnvRe = regexp.MustCompile(`^testenv:(py|pypy)\d+$`)

// sortedOwnersOf returns the keys of a per-owner check map in order.
func sortedOwnersOf(m map[string][]check) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	return sortedKeys(set)
}

// overrides reports whether INI section sec sets key itself.
func overrides(es []entry, sec, key string) bool {
	for _, e := range es {
		if len(e.Path) == 2 && e.Path[0] == sec && normKey(e.Path[1]) == key {
			return true
		}
	}
	return false
}

// isSetPlusE reports `set +e` (also combined, as in `set +ex`) and
// `set +o errexit`.
func isSetPlusE(iv invocation) bool {
	if iv.Prog() != "set" || len(iv.Args) < 2 {
		return false
	}
	a := iv.Args[1]
	return strings.HasPrefix(a, "+") && a != "+o" && strings.Contains(a, "e") ||
		a == "+o" && len(iv.Args) > 2 && iv.Args[2] == "errexit"
}

func isSetMinusE(iv invocation) bool {
	return iv.Prog() == "set" && len(iv.Args) > 1 && (strings.HasPrefix(iv.Args[1], "-") && strings.Contains(iv.Args[1], "e") ||
		iv.Args[1] == "-o" && len(iv.Args) > 2 && iv.Args[2] == "errexit")
}

// statusCheckRe matches code that inspects an exit status after `set +e`.
var statusCheckRe = regexp.MustCompile(`\$\?|PIPESTATUS|\bexit\s+"?\$|\|\|\s*(exit|return)\s+[^0\s]|\|\|\s*\w+=|\bif\s+!|\|\|\s*(fail|die|abort)\b`)

// uncheckedAfterSetE returns the verification commands that run after a
// `set +e` (the rest of its line, then later lines of the same step or
// script) when nothing inspects their exit status before `set -e` is
// restored. Make runs each recipe line in its own shell, so there only the
// rest of the line counts.
func uncheckedAfterSetE(f *core.FileInfo, s side, at cmdLine, rest string) []check {
	texts := []string{rest}
	if kindOf(f) != kindMake {
		end := len(s.lines)
		if kindOf(f) == kindCIYAML || kindOf(f) == kindTaskYAML {
			_, end = yamlBlock(s, at.Line)
		}
		for _, c := range commandLines(f, s) {
			if c.Line > at.End && c.Line <= end && c.Owner == at.Owner {
				texts = append(texts, c.Text)
			}
		}
	}
	var guarded []check
	var last []check // checks of the most recent command
	for _, t := range texts {
		if statusCheckRe.MatchString(t) {
			return nil
		}
		for _, iv := range invocations(t) {
			if isSetMinusE(iv) {
				return append(guarded, last...)
			}
			guarded = append(guarded, last...)
			last = classifyAll(iv)
		}
	}
	// Without `set -e` restored, the step's or script's exit status is the
	// status of its last command, so a check that runs last still fails it.
	return guarded
}

// ---- CI pipeline keys ----

// failureKeyRe matches the pipeline keys that let a job or step fail
// without failing the pipeline.
var failureKeyRe = regexp.MustCompile(`^\s*(?:-\s+)?(continue-on-error|continueOnError|allow_failure|soft_fail|failure|ignore_error)\s*:\s*(.*?)\s*$`)

// verificationActions are GitHub Actions that run tests or linters.
var verificationActions = []string{
	"golangci/golangci-lint-action", "pre-commit/action", "cypress-io/github-action", "actions-rs/clippy-check",
	"astral-sh/ruff-action", "chartboost/ruff-action", "jakebailey/pyright-action", "super-linter/super-linter",
	"github/super-linter", "reviewdog/action-", "psf/black", "py-actions/flake8", "ludeeus/action-shellcheck",
	"hadolint/hadolint-action", "wearerequired/lint-action", "ricardochaves/python-lint", "dorny/test-reporter",
	"mikepenz/action-junit-report", "davidanson/markdownlint-cli2-action", "ataylorme/eslint-annotate-action",
}

// reporterActions publish results that another step produced; they fail
// only when the tests failed, so they are a second gate, not the first.
var reporterActions = []string{"dorny/test-reporter", "mikepenz/action-junit-report", "enricomi/publish-unit-test-result-action"}

// onlyReporters reports whether every action in uses is a test reporter.
func onlyReporters(uses []string) bool {
	for _, u := range uses {
		u = strings.ToLower(u)
		ok := false
		for _, r := range reporterActions {
			ok = ok || strings.HasPrefix(u, r)
		}
		if !ok {
			return false
		}
	}
	return len(uses) > 0
}

// jobGatesElsewhere reports whether the job around line n runs a
// verification command outside lines start..end whose failure still fails
// the job.
func jobGatesElsewhere(f *core.FileInfo, s side, n, start, end int) bool {
	job := jobBlock(s, n)
	for _, c := range commandLines(f, s) {
		if c.Line < job[0] || c.Line > job[1] || c.Line >= start && c.Line <= end {
			continue
		}
		if len(checksIn(c.Text)) > 0 && len(swallowsIn(c.Text)) == 0 && !stepIgnoresFailure(s, c.Line) {
			return true
		}
	}
	return false
}

func isVerificationAction(uses string) bool {
	u := strings.ToLower(strings.TrimSpace(uses))
	for _, v := range verificationActions {
		if strings.HasPrefix(u, v) {
			return true
		}
	}
	return false
}

// blockInfo summarizes a YAML job or step.
type blockInfo struct {
	checks      []check
	describe    string // `job "test", step "Run tests"`
	namedVerify bool   // the job/step name says it verifies ("Unit tests")
	prerelease  bool   // runs a nightly/beta toolchain or is marked experimental
	actions     []string
	// reporterOnly marks a step that only publishes test results while
	// another step of the job runs the tests and still fails the job.
	reporterOnly bool
	// storage marks a step that uses a cache or artifact action.
	storage bool
	// stepID is the step's `id:`, through which later steps can read its
	// outcome.
	stepID string
}

// reportingWords are words in step and job names that describe producing
// output about a run rather than checking the code: reports, summaries,
// telemetry, uploads, restored caches.
var reportingWords = map[string]bool{
	"report": true, "reports": true, "summary": true, "summarize": true, "comment": true, "annotate": true,
	"annotations": true, "analytics": true, "metrics": true, "telemetry": true, "upload": true, "notify": true,
	"badge": true, "restore": true, "cache": true, "save": true, "stats": true, "timing": true, "timings": true,
}

// reportingName reports whether a step or job name describes reporting.
func reportingName(name string) bool {
	for _, w := range nameWords(name) {
		if reportingWords[w] {
			return true
		}
	}
	return false
}

// isStorageAction reports whether uses names a cache or artifact action.
func isStorageAction(uses string) bool {
	u := strings.ToLower(strings.TrimSpace(uses))
	for _, a := range storageActions {
		if strings.HasPrefix(u, a) {
			return true
		}
	}
	return false
}

// inspectBlock looks at the job/step (lines start..end) around a YAML key.
func inspectBlock(f *core.FileInfo, s side, line, start, end int) blockInfo {
	var bi blockInfo
	for _, c := range commandLines(f, s) {
		if c.Line >= start && c.End <= end {
			bi.checks = append(bi.checks, checksIn(c.Text)...)
		}
	}
	keyIndent := indentOfLine(s, start)
	for n := start; n <= end; n++ {
		l, ok := s.get(n)
		if !ok {
			continue
		}
		nd, ok := parseYAMLLine(l, n)
		if !ok {
			continue
		}
		switch normKey(nd.Key) {
		case "id":
			if nd.Indent == keyIndent && bi.stepID == "" {
				bi.stepID = strings.Trim(nd.Value, `"'`)
			}
		case "uses", "task":
			if isVerificationAction(nd.Value) {
				bi.actions = append(bi.actions, nd.Value)
			}
			if isStorageAction(nd.Value) {
				bi.storage = true
			}
		case "image", "toolchain", "python-version", "node-version", "go-version", "java-version", "rust",
			"python", "node-js", "rvm", "php", "jdk", "go", "version", "channel":
			// a nightly/beta toolchain; the job and step names are checked below
			if prereleaseRe.MatchString(nd.Value) {
				bi.prerelease = true
			}
		}
	}
	job, step := yamlOwner(s, line)
	if start == line {
		// the key opens the step itself ("- continue-on-error: true")
		step = stepName(s, start, end, indentOfLine(s, line))
	}
	bi.describe = describeOwner(job, step)
	if step == "" && isStepBlock(s, start) {
		// an unnamed step: name it by its command
		for _, c := range commandLines(f, s) {
			if c.Line >= start && c.End <= end {
				bi.describe = describeOwner(job, "") + ", step " + quote(c.Text)
				if job == "" {
					bi.describe = "step " + quote(c.Text)
				}
				break
			}
		}
	}
	// The key applies to the step when there is one, else to the job.
	name := job
	if step != "" || isStepBlock(s, start) {
		name = step
	}
	if _, ok := verificationName(name); ok {
		bi.namedVerify = true
	}
	if reportingName(name) {
		// "Report test growth", "CI analytics summary": a script named
		// like a check that the step runs to produce a report is not the
		// check; real test runners and linters still count.
		bi.namedVerify = false
		var keep []check
		for _, c := range bi.checks {
			if c.Eco != ecoAny {
				keep = append(keep, c)
			}
		}
		bi.checks = keep
	}
	if prereleaseRe.MatchString(job) || prereleaseRe.MatchString(step) {
		bi.prerelease = true
	}
	return bi
}

// anchorRe finds a YAML anchor on a mapping key line (".defaults: &defaults").
var anchorRe = regexp.MustCompile(`&([A-Za-z0-9_.-]+)`)

// addTemplateUsers handles a setting placed in a shared template: a YAML
// anchor merged into jobs (`<<: *defaults`) or a GitLab hidden job used
// with `extends:`. When the template itself runs no checks, the checks of
// the jobs that use it are what the setting affects.
func addTemplateUsers(f *core.FileInfo, s side, start int, bi *blockInfo) {
	if len(bi.checks) > 0 || len(bi.actions) > 0 {
		return
	}
	l, ok := s.get(start)
	if !ok {
		return
	}
	nd, ok := parseYAMLLine(l, start)
	if !ok || nd.Key == "" {
		return
	}
	anchor := ""
	if m := anchorRe.FindStringSubmatch(nd.Value); m != nil {
		anchor = m[1]
	}
	hidden := strings.HasPrefix(nd.Key, ".") && nd.Indent == 0
	if anchor == "" && !hidden {
		return
	}
	var users []string
	for i := 1; i <= len(s.lines); i++ {
		ul, ok := s.get(i)
		if !ok || i == start {
			continue
		}
		un, ok := parseYAMLLine(ul, i)
		if !ok {
			continue
		}
		uses := anchor != "" && un.Key == "<<" && strings.Contains(un.Value, "*"+anchor) ||
			hidden && normKey(un.Key) == "extends" && containsWord(extendsTargets(s, i, un.Value), nd.Key)
		if !uses {
			continue
		}
		job := jobBlock(s, i)
		for _, c := range commandLines(f, s) {
			if c.Line >= job[0] && c.Line <= job[1] {
				if cs := checksIn(c.Text); len(cs) > 0 {
					bi.checks = append(bi.checks, cs...)
				}
			}
		}
		if jl, ok := s.get(job[0]); ok {
			if jn, ok := parseYAMLLine(jl, job[0]); ok && jn.Key != "" {
				users = append(users, jn.Key)
			}
		}
	}
	if len(bi.checks) > 0 {
		bi.describe = fmt.Sprintf("template %q (used by %s)", nd.Key, strings.Join(users, ", "))
	}
}

func containsWord(ws []string, w string) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}

// isStepBlock reports whether the block starting on line n is a list item
// (a step) rather than a job mapping.
func isStepBlock(s side, n int) bool {
	l, ok := s.get(n)
	if !ok {
		return false
	}
	nd, ok := parseYAMLLine(l, n)
	return ok && nd.Dash >= 0
}

func indentOfLine(s side, n int) int {
	l, _ := s.get(n)
	nd, _ := parseYAMLLine(l, n)
	return nd.Indent
}

// yamlFailureKeys reports pipeline keys that ignore failures of jobs or
// steps that verify code. Keys that merely moved (between steps, jobs or
// files) cancel out, but only against a removed key that also covered a
// verifying job or step: un-suppressing a cache step does not pay for
// suppressing the test step.
func yamlFailureKeys(a *core.Analysis) []core.Finding {
	var files []*core.FileInfo
	removed, removedAllow := multiset{}, multiset{}
	removedTools := map[string]bool{} // checks whose commands the change removes
	for _, f := range a.Files {
		// CI pipelines, and Taskfiles (go-task's `ignore_error: true`)
		if k := kindOf(f); k != kindCIYAML && !(k == kindTaskYAML && f.Config != core.CfgPreCommit) {
			continue
		}
		files = append(files, f)
		_, rem := changedCommands(f)
		for _, c := range rem {
			for _, ch := range checksIn(c.Text) {
				removedTools[ch.Tool] = true
			}
		}
		old := oldSide(f)
		for _, l := range f.Removed {
			if m := failureKeyRe.FindStringSubmatch(l.NoComment); m != nil {
				_, verifies := failureKeyBlock(f, old, l.No)
				removed.add(failureKeyID(m, verifies))
			}
			removedAllow.add(rulekit.Normalize(l.NoComment))
		}
	}
	var out []core.Finding
	for _, f := range files {
		s := newSide(f)
		addSet := lineSet(f.Added)
		for _, l := range f.Added {
			if fd, ok := allowFailuresEntry(f, s, l); ok {
				if !removedAllow.take(rulekit.Normalize(l.NoComment)) {
					out = append(out, fd) // a reordered allow_failures entry is not new
				}
				continue
			}
			m := failureKeyRe.FindStringSubmatch(l.NoComment)
			if m == nil {
				continue
			}
			key, val := m[1], strings.Trim(m[2], `"'`)
			if !ignoresFailure(key, val, s, l.No) {
				continue
			}
			bi, verifies := failureKeyBlock(f, s, l.No)
			if !verifies || removed.take(failureKeyID(m, true)) {
				continue
			}
			byName, byComment := declaredNonBlocking(f, s, l.No)
			if bi.prerelease || byName {
				continue // nightly toolchains and jobs named advisory may fail
			}
			if bi.reporterOnly {
				continue // a test-report/annotation step; the test step itself still fails the job
			}
			where := bi.describe
			if where == "" {
				where = "this job"
			}
			setting := key + ": " + m[2]
			if key == "allow_failure" && strings.TrimSpace(m[2]) == "" {
				setting = "allow_failure: exit_codes: [" + strings.Join(allowedExitCodes(s, l.No, ""), ", ") + "]"
			}
			start, end := yamlBlock(s, l.No)
			// A job or step that the change adds, running checks the change
			// does not remove elsewhere, was never a gate: making it
			// non-blocking from the start relaxes nothing that ran before.
			fresh := blockAdded(s, start, end, addSet)
			for _, c := range bi.checks {
				if removedTools[c.Tool] {
					fresh = false // it replaces a removed gate
				}
			}
			what := toolNames(bi.checks)
			if what == "" {
				what = strings.Join(bi.actions, ", ")
			}
			if fresh {
				runs := ""
				if what != "" {
					runs = " (runs " + what + ")"
				}
				out = append(out, sevFinding(failureIgnored, f, l.No, core.Info,
					fmt.Sprintf("`%s` on %s%s, which this change adds: the new check starts out non-blocking, so its failures will not fail the pipeline", setting, where, runs)))
				continue
			}
			reader := outcomeReader(s, start, end, bi.stepID)
			readNote := ""
			if reader > 0 {
				readNote = fmt.Sprintf(" (line %d reads its outcome: make sure that replacement still fails the pipeline, on pushes and fork pull requests too)", reader)
			}
			if len(bi.checks) > 0 || len(bi.actions) > 0 {
				fd := failureIgnored.Finding(f, l.No,
					fmt.Sprintf("`%s` on %s, which runs %s: the pipeline stays green when it fails", setting, where, what))
				if byComment {
					fd.Severity = core.Warning
					fd.Message += nonBlockingNote
				}
				if reader > 0 {
					fd.Severity = core.Warning
					fd.Message += readNote
				}
				out = append(out, fd)
				continue
			}
			out = append(out, sevFinding(failureIgnored, f, l.No, core.Warning,
				fmt.Sprintf("`%s` on %s: its name says it verifies the code, so its failures would no longer fail the pipeline%s", setting, where, readNote)))
		}
	}
	return out
}

// failureKeyID identifies a failure key for cancellation: the key, its
// value, and whether the job or step it sits on verifies code.
func failureKeyID(m []string, verifies bool) string {
	return fmt.Sprintf("%s:%s|%t", m[1], strings.ToLower(m[2]), verifies)
}

// failureKeyBlock inspects the job or step that the failure key on line n
// of side s applies to, and reports whether that block verifies code (runs
// checks or verification actions, or is named like a check).
func failureKeyBlock(f *core.FileInfo, s side, n int) (blockInfo, bool) {
	start, end := yamlBlock(s, n)
	bi := inspectBlock(f, s, n, start, end)
	if len(bi.checks) == 0 && onlyReporters(bi.actions) && jobGatesElsewhere(f, s, n, start, end) {
		bi.reporterOnly = true
	}
	addTemplateUsers(f, s, start, &bi)
	return bi, len(bi.checks) > 0 || len(bi.actions) > 0 || bi.namedVerify && !bi.storage
}

// blockAdded reports whether every non-blank line from start to end is an
// added line: the job or step is new in this change.
func blockAdded(s side, start, end int, addSet map[int]bool) bool {
	for n := start; n <= end; n++ {
		l, ok := s.get(n)
		if !ok || strings.TrimSpace(l.Raw) == "" || strings.TrimSpace(l.NoComment) == "" {
			continue
		}
		if !addSet[n] {
			return false
		}
	}
	return true
}

// outcomeReader returns the first line outside start..end that reads the
// outcome of step id (steps.<id>.outcome / .conclusion), or 0.
func outcomeReader(s side, start, end int, id string) int {
	if id == "" {
		return 0
	}
	needles := []string{"steps." + id + ".outcome", "steps." + id + ".conclusion"}
	for n := 1; n <= len(s.lines); n++ {
		if n >= start && n <= end {
			continue
		}
		l, ok := s.get(n)
		if !ok {
			continue
		}
		for _, nd := range needles {
			if strings.Contains(l.NoComment, nd) {
				return n
			}
		}
	}
	return 0
}

// ignoresFailure interprets the value of a failure key.
func ignoresFailure(key, val string, s side, line int) bool {
	switch key {
	case "failure":
		return strings.EqualFold(val, "ignore")
	case "soft_fail":
		if val == "" {
			// soft_fail:\n  - exit_status: 1
			if l, ok := s.get(line + 1); ok {
				return strings.Contains(l.NoComment, "exit_status")
			}
			return false
		}
		return truthy(val)
	case "allow_failure":
		// `allow_failure: exit_codes: [137]` is a deliberate, narrow
		// allowance, unless it allows 1: how test runners and linters
		// report failures.
		return truthy(val) || containsWord(allowedExitCodes(s, line, val), "1")
	}
	if strings.HasPrefix(val, "${{") {
		// continue-on-error: ${{ matrix.experimental }} — only an
		// expression that is literally true counts, or a matrix value that
		// is true in every entry.
		expr := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(val, "${{"), "}}"))
		if name, ok := strings.CutPrefix(expr, "matrix."); ok {
			vs := matrixValues(s, line, name)
			for _, v := range vs {
				if !truthy(v) {
					return false
				}
			}
			return len(vs) > 0
		}
		return strings.EqualFold(expr, "true")
	}
	return truthy(val)
}

// allowedExitCodes returns the exit codes of a GitLab `allow_failure:
// exit_codes:` setting on line n (inline, as a flow mapping, or as a block).
func allowedExitCodes(s side, n int, val string) []string {
	if strings.HasPrefix(val, "{") {
		if _, v, ok := strings.Cut(val, "exit_codes:"); ok {
			return flowValues(strings.TrimSuffix(strings.TrimSpace(v), "}"))
		}
		return nil
	}
	if val != "" {
		return nil
	}
	return childValues(s, n, "exit_codes")
}

// flowValues splits a YAML scalar or flow sequence ("[1, 137]", "1").
func flowValues(v string) []string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		return splitFlowList(v[1 : len(v)-1])
	}
	if v == "" {
		return nil
	}
	return []string{unquoteYAML(v)}
}

// childValues returns the values of key among the children of the mapping
// opened on line n: `key: v`, `key: [a, b]` or a block list below key.
func childValues(s side, n int, key string) []string {
	l, ok := s.get(n)
	if !ok {
		return nil
	}
	parent := indentOf(strings.TrimRight(l.NoComment, " \t"))
	var out []string
	for i := n + 1; i <= len(s.lines); i++ {
		cl, ok := s.get(i)
		if !ok {
			break
		}
		if strings.TrimSpace(cl.NoComment) == "" {
			continue
		}
		nd, ok := parseYAMLLine(cl, i)
		if !ok || indentOf(strings.TrimRight(cl.NoComment, " \t")) <= parent {
			break
		}
		if nd.Key != key {
			continue
		}
		if nd.Value != "" {
			return flowValues(nd.Value)
		}
		return childValuesOfKey(s, i)
	}
	return out
}

// matrixValues returns every value the matrix variable name takes in the
// strategy of the job around line n (its list and any include entries).
func matrixValues(s side, n int, name string) []string {
	job := jobBlock(s, n)
	var out []string
	for i := job[0]; i <= job[1]; i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		nd, ok := parseYAMLLine(l, i)
		if !ok || nd.Key != name || !hasAncestor(s, i, "matrix") {
			continue
		}
		if nd.Value != "" {
			out = append(out, flowValues(nd.Value)...)
			continue
		}
		out = append(out, childValuesOfKey(s, i)...)
	}
	return out
}

// childValuesOfKey returns the block list items under the key on line n.
func childValuesOfKey(s side, n int) []string {
	l, _ := s.get(n)
	key, _ := parseYAMLLine(l, n)
	var out []string
	for j := n + 1; j <= len(s.lines); j++ {
		il, ok := s.get(j)
		if !ok {
			break
		}
		item, ok := parseYAMLLine(il, j)
		if !ok || item.Dash < 0 || item.Dash < key.Indent && item.Indent <= key.Indent {
			break
		}
		out = append(out, item.Value)
	}
	return out
}

// allowFailuresEntry reports a job newly added to a Travis/AppVeyor
// allow_failures list.
func allowFailuresEntry(f *core.FileInfo, s side, l core.Line) (core.Finding, bool) {
	nd, ok := parseYAMLLine(l.LexedLine, l.No)
	if !ok || nd.Dash < 0 {
		return core.Finding{}, false
	}
	inList := false
	for _, anc := range yamlAncestors(s, l.No) {
		if anc.Key == "allow_failures" {
			inList = true
			break
		}
	}
	if !inList || prereleaseRe.MatchString(nd.Value) {
		return core.Finding{}, false
	}
	entry := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.NoComment), "-"))
	if nd.Key == "name" {
		entry = nd.Value
	}
	if _, ok := verificationName(nd.Value); ok || nd.Key == "stage" && nd.Value == "test" {
		return failureIgnored.Finding(f, l.No, fmt.Sprintf("job %s added to allow_failures: its test failures no longer fail the build", quote(entry))), true
	}
	return sevFinding(failureIgnored, f, l.No, core.Warning,
		fmt.Sprintf("job %s added to allow_failures: the tests it runs can now fail without failing the build", quote(entry))), true
}

// ---- Jenkins ----

var (
	catchErrorRe   = regexp.MustCompile(`\bcatchError\s*\(([^)]*)\)`)
	returnStatusRe = regexp.MustCompile(`\breturnStatus\s*:\s*true\b`)
	groovyAssignRe = regexp.MustCompile(`(?:\bdef\s+|\b(?:int|Integer|var)\s+)?\b([A-Za-z_]\w*)\s*=\s*(?:sh|bat|powershell)\b`)
)

// jenkinsSwallows reports catchError(buildResult: 'SUCCESS') around
// verification and sh(..., returnStatus: true) whose status is unused.
func jenkinsSwallows(f *core.FileInfo) []core.Finding {
	s := newSide(f)
	cmds := commandLines(f, s)
	removed := multiset{}
	for _, l := range f.Removed {
		removed.add(rulekit.Normalize(l.NoComment))
	}
	var out []core.Finding
	for _, l := range f.Added {
		if removed.take(rulekit.Normalize(l.NoComment)) {
			continue // compared with strings intact: 'UNSTABLE' → 'SUCCESS' is a change
		}
		if m := catchErrorRe.FindStringSubmatch(l.NoComment); m != nil && strings.Contains(m[1], "SUCCESS") && strings.Contains(m[1], "buildResult") {
			end := braceBlockEnd(s, l.No)
			var cs []check
			for _, c := range cmds {
				if c.Line >= l.No && c.Line <= end {
					cs = append(cs, checksIn(c.Text)...)
				}
			}
			if len(cs) > 0 {
				out = append(out, failureIgnored.Finding(f, l.No,
					fmt.Sprintf("catchError(buildResult: 'SUCCESS') around %s keeps the build green when it fails", toolNames(cs))))
			}
			continue
		}
		if jenkinsCatchRe.MatchString(l.Code) {
			if fd, ok := jenkinsCatchSwallow(f, s, cmds, l); ok {
				out = append(out, fd)
			}
			continue
		}
		if !returnStatusRe.MatchString(l.NoComment) {
			continue
		}
		var cs []check
		for _, c := range cmds {
			if c.Line <= l.No && c.End >= l.No-3 {
				cs = append(cs, checksIn(c.Text)...)
			}
		}
		if len(cs) == 0 {
			continue
		}
		if v := statusVar(s, l.No); v != "" && usedAfter(s, l.No, v) {
			continue // def rc = sh(..., returnStatus: true); if (rc != 0) { ... }
		}
		if statusTestedInline(s, l.No) {
			continue // if (sh(script: ..., returnStatus: true) != 0) { error(...) }
		}
		out = append(out, failureIgnored.Finding(f, l.No,
			fmt.Sprintf("sh(returnStatus: true) runs %s but nothing checks the status, so failures are ignored", toolNames(cs))))
	}
	return out
}

var (
	jenkinsCatchRe = regexp.MustCompile(`\bcatch\s*\(`)
	jenkinsTryRe   = regexp.MustCompile(`\btry\s*$`)
	// jenkinsFailCodeRe (on code without string contents) and
	// jenkinsFailTextRe (with strings) match a catch block that still fails
	// or marks the build unstable: error(...), throw, unstable(...),
	// currentBuild.result = 'FAILURE', sh 'exit 1', or a junit step, which
	// marks the build unstable when the reports hold failures.
	jenkinsFailCodeRe = regexp.MustCompile(`\bthrow\b|\b(error|unstable)\s*[('"]|\b(junit|xunit|nunit|testNG)\b`)
	jenkinsFailTextRe = regexp.MustCompile(`currentBuild\.(result|currentResult)\s*=\s*['"](FAILURE|UNSTABLE)|\bexit\s+[1-9]`)
)

// jenkinsCatchSwallow reports a Groovy `try { sh 'npm test' } catch (e) {
// echo ... }` whose catch block neither rethrows nor marks the build
// failed or unstable.
func jenkinsCatchSwallow(f *core.FileInfo, s side, cmds []cmdLine, l core.Line) (core.Finding, bool) {
	// Walk back to the `try {` whose block the catch follows.
	tryLine, depth := 0, 0
	for i := l.No; i >= 1 && tryLine == 0; i-- {
		pl, ok := s.get(i)
		if !ok {
			return core.Finding{}, false
		}
		code := pl.Code
		if i == l.No {
			code = code[:jenkinsCatchRe.FindStringIndex(code)[0]]
		}
		for j := len(code) - 1; j >= 0; j-- {
			switch code[j] {
			case '}':
				depth++
			case '{':
				depth--
				if depth == 0 && jenkinsTryRe.MatchString(code[:j]) {
					tryLine = i
				}
			}
		}
	}
	if tryLine == 0 {
		return core.Finding{}, false
	}
	var cs []check
	for _, c := range cmds {
		if c.Line >= tryLine && c.Line < l.No {
			cs = append(cs, checksIn(c.Text)...)
		}
	}
	if len(cs) == 0 {
		return core.Finding{}, false
	}
	// Scan the catch block: from the catch keyword to its closing brace.
	depth, opened := 0, false
	for i := l.No; i <= len(s.lines) && !(opened && depth <= 0); i++ {
		bl, ok := s.get(i)
		if !ok {
			break
		}
		code, text := bl.Code, bl.NoComment
		if i == l.No {
			code = code[jenkinsCatchRe.FindStringIndex(code)[0]:]
			if k := jenkinsCatchRe.FindStringIndex(text); k != nil {
				text = text[k[0]:]
			}
		}
		if jenkinsFailCodeRe.MatchString(code) || jenkinsFailTextRe.MatchString(text) {
			return core.Finding{}, false
		}
		for _, r := range code {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
	}
	return failureIgnored.Finding(f, l.No,
		fmt.Sprintf("the catch block after %s only logs the error, so a failing run no longer fails the build", toolNames(cs))), true
}

// statusVar returns the variable an sh(...) call on or just above line n is
// assigned to.
func statusVar(s side, n int) string {
	for i := n; i >= n-3 && i >= 1; i-- {
		if l, ok := s.get(i); ok {
			if m := groovyAssignRe.FindStringSubmatch(l.Code); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// inlineStatusRe matches an sh/bat call whose result is tested where it is
// made: inside an if/while/return, or compared right away.
var inlineStatusRe = regexp.MustCompile(`\b(if|while|unless|assert|return)\b.*\b(sh|bat|powershell)\s*\(|\b(sh|bat|powershell)\s*\(.*\)\s*(!=|==|>|<|\?)`)

// statusTestedInline reports whether the sh(...) call on or just above
// line n tests its exit status directly.
func statusTestedInline(s side, n int) bool {
	for i := n; i >= n-3 && i >= 1; i-- {
		if l, ok := s.get(i); ok && inlineStatusRe.MatchString(l.Code) {
			return true
		}
	}
	return false
}

// usedAfter reports whether identifier v appears in code after line n.
func usedAfter(s side, n int, v string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(v) + `\b`)
	for i := n + 1; i <= len(s.lines); i++ {
		if l, ok := s.get(i); ok && re.MatchString(l.Code) {
			return true
		}
	}
	return false
}

// braceBlockEnd returns the line that closes the brace block opened on
// line n.
func braceBlockEnd(s side, n int) int {
	depth, opened := 0, false
	for i := n; i <= len(s.lines); i++ {
		l, ok := s.get(i)
		if !ok {
			return i - 1
		}
		for _, r := range l.Code {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth <= 0 {
			return i
		}
	}
	return len(s.lines)
}

// ---- nox, Gradle, Maven ----

var successCodesRe = regexp.MustCompile(`success_codes\s*=\s*[\[(]([^\])]*)[\])]`)

// noxSuccessCodes reports session.run(..., success_codes=[0, 1]) on a
// verification command: exit code 1 (test failures) then counts as success.
func noxSuccessCodes(f *core.FileInfo) []core.Finding {
	s := newSide(f)
	var out []core.Finding
	for _, l := range f.Added {
		m := successCodesRe.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		var bad []string
		for _, c := range strings.Split(m[1], ",") {
			if c = strings.TrimSpace(c); c != "" && c != "0" && c != "5" { // 5 = "no tests collected"
				bad = append(bad, c)
			}
		}
		if len(bad) == 0 {
			continue
		}
		// The session.run(...) call may start a few lines above; its string
		// arguments are the command.
		start := l.No
		for i := l.No; i >= l.No-4 && i >= 1; i-- {
			if pl, ok := s.get(i); ok && strings.Contains(pl.Code, "run(") {
				start = i
				break
			}
		}
		var call strings.Builder
		for i := start; i <= l.No; i++ {
			if pl, ok := s.get(i); ok {
				call.WriteString(strings.Join(pl.Strings, " ") + " ")
			}
		}
		if cs := checksIn(call.String()); len(cs) > 0 {
			out = append(out, failureIgnored.Finding(f, l.No,
				fmt.Sprintf("success_codes includes %s, so nox treats a failing %s run as a success", strings.Join(bad, ", "), toolNames(cs))))
		}
	}
	return out
}

var (
	gradleIgnoreRe = regexp.MustCompile(`\b(ignoreFailures|isIgnoreFailures)\s*(?:=|\.set\()\s*true\b|\babortOnError\s*(?:=|\.set\()?\s*false\b`)
	mavenIgnoreRe  = regexp.MustCompile(`<(testFailureIgnore|maven\.test\.failure\.ignore|failOnViolation|failsOnError|failOnError)>\s*(true|false)\s*</`)
	gradleTestBlk  = regexp.MustCompile(`\b(test|Test|withType\s*[(<]\s*Test|integrationTest)\b`)
)

// buildToolIgnores reports Gradle/Maven settings that ignore test or lint
// failures.
func buildToolIgnores(f *core.FileInfo) []core.Finding {
	s := newSide(f)
	removed := multiset{}
	for _, l := range f.Removed {
		removed.add(rulekit.Normalize(l.NoComment))
	}
	var out []core.Finding
	for _, l := range f.Added {
		if removed.take(rulekit.Normalize(l.NoComment)) {
			continue
		}
		if f.Config == core.CfgGradle {
			m := gradleIgnoreRe.FindString(l.Code)
			if m == "" {
				continue
			}
			if strings.Contains(m, "abortOnError") {
				out = append(out, sevFinding(failureIgnored, f, l.No, core.Warning,
					"Android lint `abortOnError = false`: lint errors no longer fail the build"))
				continue
			}
			if blk := enclosingBlock(s, l.No); gradleTestBlk.MatchString(blk) {
				out = append(out, failureIgnored.Finding(f, l.No,
					fmt.Sprintf("`ignoreFailures = true` in %s: failing tests no longer fail the build", strings.TrimSpace(blk))))
			} else {
				out = append(out, sevFinding(failureIgnored, f, l.No, core.Warning,
					fmt.Sprintf("`ignoreFailures = true` in %s: this check's failures no longer fail the build", quote(strings.TrimSpace(blk)))))
			}
			continue
		}
		m := mavenIgnoreRe.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		switch {
		case (m[1] == "testFailureIgnore" || m[1] == "maven.test.failure.ignore") && m[2] == "true":
			out = append(out, failureIgnored.Finding(f, l.No,
				fmt.Sprintf("<%s>true</%s>: Maven reports test failures but still passes the build", m[1], m[1])))
		case m[1] != "testFailureIgnore" && m[1] != "maven.test.failure.ignore" && m[2] == "false":
			out = append(out, sevFinding(failureIgnored, f, l.No, core.Warning,
				fmt.Sprintf("<%s>false</%s>: this check's violations no longer fail the build", m[1], m[1])))
		}
	}
	return out
}

// enclosingBlock returns the code that opens the brace block around line n
// ("tasks.test", "checkstyle", "tasks.named('test')"). Braces are counted
// on the code without strings; the returned text keeps string literals so
// that a task named in quotes is visible.
func enclosingBlock(s side, n int) string {
	depth := 0
	for i := n - 1; i >= 1; i-- {
		l, ok := s.get(i)
		if !ok {
			break
		}
		code := l.Code
		for j := len(code) - 1; j >= 0; j-- {
			switch code[j] {
			case '}':
				depth++
			case '{':
				if depth == 0 {
					text := l.NoComment
					if k := strings.LastIndexByte(text, '{'); k >= 0 {
						text = text[:k]
					}
					return strings.TrimSpace(text)
				}
				depth--
			}
		}
	}
	return ""
}
