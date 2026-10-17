package ci

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
	"github.com/huyth45/narc/internal/lang"
)

var coverageLowered = &core.Rule{
	ID:       "coverage-lowered",
	Title:    "Coverage threshold lowered",
	Category: "ci",
	Severity: core.Error,
	Summary:  "A coverage threshold was lowered or removed (jest/vitest/nyc/c8 thresholds, coverage.py fail_under, --cov-fail-under, codecov targets, JaCoCo minimums, ...).",
	Hint: "Restore the threshold and add tests for the uncovered code instead. If the threshold really " +
		"has to move (e.g. a large generated module was added), lower it in a separate, explained change.",
	Docs: "A coverage gate fails the build when new code arrives without tests. Lowering the number is " +
		"the cheapest way to make that failure disappear.\n\n" +
		"```diff\n [tool.coverage.report]\n-fail_under = 90\n+fail_under = 70   # BUSTED: 90 → 70\n```\n\n" +
		"narc parses the old and new configuration (JSON, JS config objects, YAML, TOML, INI) and compares " +
		"every threshold it knows: jest `coverageThreshold`, vitest `coverage.thresholds`, nyc/c8 " +
		"`lines/branches/functions/statements` and `check-coverage`, coverage.py `fail_under` (.coveragerc, " +
		"setup.cfg, tox.ini, pyproject.toml), `--cov-fail-under` and other CLI flags, codecov `target` " +
		"(lowered) and `threshold` (raised — it is the allowed drop), go-test-coverage, JaCoCo/Kover minimums, " +
		"SimpleCov `minimum_coverage` and `COVERAGE_THRESHOLD`-style variables. A threshold that moves to " +
		"another file in the same change is not reported unless its value dropped. A new jest per-path " +
		"threshold below the global one (`'./src/payments/': { lines: 0 }`) is a warning: jest checks the " +
		"matching files against it instead of the global gate.",
}

func init() {
	coverageLowered.Check = scoped(checkCoverage)
	core.Register(coverageLowered)
}

// threshold is one coverage gate found in a file.
type threshold struct {
	f     *core.FileInfo
	key   string // identity across files: "coverage.py fail_under", "jest global.lines"
	label string // how to name it in messages
	value string
	num   float64
	isNum bool
	line  int
	// looser is true when a higher number is more permissive (codecov
	// threshold: the allowed drop).
	looser bool
	// gate marks on/off switches (nyc check-coverage) and inverse ones
	// (codecov informational: on means non-blocking).
	gate, inverse bool
	cli           bool // found on a command line (it may be duplicated in config we cannot see)
	// canon identifies equivalent thresholds of different tools, so a
	// threshold that moves from jest to vitest or nyc counts as moved.
	canon string
}

// canonKey returns t's cross-tool identity.
func (t threshold) canonKey() string {
	if t.canon != "" {
		return t.canon
	}
	return t.key
}

// strict reports whether a gate is enforcing.
func (t threshold) strict() bool {
	if t.inverse {
		return !truthy(t.value)
	}
	return truthy(t.value)
}

var coverageMetrics = map[string]bool{"lines": true, "branches": true, "functions": true, "statements": true}

// structuredThresholds extracts thresholds from parsed config entries.
func structuredThresholds(f *core.FileInfo, es []entry) []threshold {
	base := strings.ToLower(path.Base(f.Path))
	var out []threshold
	add := func(e entry, key, label string) {
		t := threshold{f: f, key: key, label: label, value: e.Value, line: e.Line}
		t.num, t.isNum = parseNum(e.Value)
		out = append(out, t)
	}
	for _, e := range es {
		p := e.Path
		switch {
		// jest: coverageThreshold.<scope>.<metric>
		case (f.Config == core.CfgJest || f.Config == core.CfgPackageJSON && len(p) > 0 && p[0] == "jest") && indexOf(p, "coverageThreshold") >= 0:
			i := indexOf(p, "coverageThreshold")
			if len(p) == i+3 && coverageMetrics[p[i+2]] {
				add(e, "jest "+p[i+1]+"."+p[i+2], "jest coverageThreshold."+p[i+1]+"."+p[i+2])
				if p[i+1] == "global" {
					out[len(out)-1].canon = "js " + p[i+2]
				}
			}
		// vitest: coverage.thresholds.<metric> (and the older coverage.<metric>)
		case f.Config == core.CfgVitest && indexOf(p, "coverage") >= 0:
			rest := p[indexOf(p, "coverage")+1:]
			if len(rest) > 0 && rest[0] == "thresholds" {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				continue
			}
			last := rest[len(rest)-1]
			switch {
			case coverageMetrics[last]:
				add(e, "vitest "+strings.Join(rest, "."), "vitest coverage.thresholds."+strings.Join(rest, "."))
				if len(rest) == 1 {
					out[len(out)-1].canon = "js " + last
				}
			case len(rest) == 1 && last == "100":
				add(e, "vitest 100", "vitest coverage.thresholds.100")
				out[len(out)-1].gate = true
			}
		// nyc / c8: lines, branches, ..., check-coverage
		case f.Config == core.CfgNYC || f.Config == core.CfgPackageJSON && len(p) == 2 && (p[0] == "nyc" || p[0] == "c8"):
			rel := p
			if f.Config == core.CfgPackageJSON {
				rel = p[1:]
			}
			if len(rel) != 1 {
				continue
			}
			switch k := normKey(rel[0]); {
			case coverageMetrics[k]:
				add(e, "nyc "+k, "nyc/c8 "+k)
				out[len(out)-1].canon = "js " + k
			case k == "check-coverage" || k == "100":
				add(e, "nyc "+k, "nyc/c8 "+k)
				out[len(out)-1].gate = true
			}
		// coverage.py fail_under
		case len(p) > 0 && normKey(p[len(p)-1]) == "fail-under" && isCoveragePySection(f, p):
			add(e, "coverage.py fail_under", "coverage.py fail_under")
		// codecov: coverage.status.(project|patch)[.<name>].(target|threshold|informational)
		case f.Config == core.CfgCodecov && len(p) >= 4 && p[0] == "coverage" && p[1] == "status":
			rest := strings.Join(p[2:], ".")
			switch p[len(p)-1] {
			case "target":
				add(e, "codecov "+rest, "codecov "+rest)
			case "threshold":
				add(e, "codecov "+rest, "codecov "+rest)
				out[len(out)-1].looser = true
			case "informational":
				add(e, "codecov "+rest, "codecov "+rest)
				out[len(out)-1].gate, out[len(out)-1].inverse = true, true
			}
		case f.Config == core.CfgCodecov && len(p) == 3 && p[0] == "coverage" && p[1] == "status" && (p[2] == "project" || p[2] == "patch"):
			// project: off / patch: false disables the status
			add(e, "codecov "+p[2]+" enabled", "codecov "+p[2]+" status")
			out[len(out)-1].gate = true
			if falsy(e.Value) || e.Value == "off" {
				out[len(out)-1].value = "false"
			} else {
				out[len(out)-1].value = "true"
			}
		// go-test-coverage: threshold.file/package/total
		case (base == ".testcoverage.yml" || base == ".testcoverage.yaml") && len(p) == 2 && p[0] == "threshold":
			add(e, "go-test-coverage "+p[1], "go-test-coverage threshold."+p[1])
		// cargo-tarpaulin
		case (base == "tarpaulin.toml" || base == ".tarpaulin.toml") && normKey(e.Last()) == "fail-under":
			add(e, "tarpaulin fail-under", "tarpaulin fail-under")
		}
	}
	return out
}

// isCoveragePySection reports whether a fail_under key belongs to
// coverage.py's [report] configuration.
func isCoveragePySection(f *core.FileInfo, p []string) bool {
	switch f.Config {
	case core.CfgCoverage:
		return len(p) == 2 && p[0] == "report"
	case core.CfgSetupCfg, core.CfgTox:
		return len(p) == 2 && p[0] == "coverage:report"
	case core.CfgPyproject:
		return len(p) == 4 && p[0] == "tool" && p[1] == "coverage" && p[2] == "report"
	}
	return false
}

// lineThreshold patterns: command-line flags, environment variables and
// build-file DSLs.
var (
	covFailUnderRe   = regexp.MustCompile(`--cov-fail-under[=\s]+["']?(-?[\d.]+)`)
	failUnderRe      = regexp.MustCompile(`(?:^|\s)--fail-under(-lines|-functions|-regions)?[=\s]+["']?(-?[\d.]+)`)
	nycFlagRe        = regexp.MustCompile(`--(lines|branches|functions|statements)[=\s]+["']?(-?[\d.]+)`)
	jestCLIRe        = regexp.MustCompile(`--coverageThreshold[=\s]+['"]?(\{.*\})`)
	jestCLIMetricRe  = regexp.MustCompile(`"?(lines|branches|functions|statements)"?\s*:\s*(-?[\d.]+)`)
	minCoverageFlag  = regexp.MustCompile(`--(min-coverage|minimum-coverage|coverage-threshold|coverage-min|threshold-total|threshold-file|threshold-package)[=\s]+["']?(-?[\d.]+)`)
	coverageEnvRe    = regexp.MustCompile(`\b([A-Z0-9_]*(?:COVERAGE|COV)_(?:THRESHOLD|MIN|MINIMUM|FAIL_UNDER)[A-Z0-9_]*|MIN(?:IMUM)?_(?:CODE_)?COV(?:ERAGE)?[A-Z0-9_]*)\s*(?:[:?+!]?=|:)\s*["']?(-?[\d.]+)`)
	jacocoMinRe      = regexp.MustCompile(`<minimum>\s*([\d.]+)\s*</minimum>`)
	gradleMinRe      = regexp.MustCompile(`\bminimum\s*=\s*(?:new\s+)?(?:BigDecimal(?:\.valueOf)?\(\s*)?["']?([\d.]+)`)
	koverMinRe       = regexp.MustCompile(`\b(?:minBound\(\s*|minValue\s*(?:=\s*|\.set\(\s*))([\d.]+)`)
	simplecovRe      = regexp.MustCompile(`\bminimum_coverage(?:_by_file)?\s*\(?\s*(?:line:\s*)?([\d.]+)`)
	haltOnFailureRe  = regexp.MustCompile(`<haltOnFailure>\s*(true|false)\s*</haltOnFailure>`)
	nycCheckCoverage = regexp.MustCompile(`\b(nyc|c8)\b.*--check-coverage\b`)
)

// lineThresholds scans every line of a side for command-line and DSL
// thresholds.
func lineThresholds(f *core.FileInfo, s side) []threshold {
	if f.Lang == lang.Markdown {
		return nil
	}
	var out []threshold
	add := func(n int, key, label, v string, cli bool) {
		t := threshold{f: f, key: key, label: label, value: v, line: n, cli: cli}
		t.num, t.isNum = parseNum(v)
		out = append(out, t)
	}
	isJVM := f.Config == core.CfgMaven || f.Config == core.CfgGradle
	isRuby := f.Lang == lang.Ruby || strings.EqualFold(path.Base(f.Path), ".simplecov")
	for i := range s.lines {
		l, ok := s.get(i + 1)
		if !ok {
			continue
		}
		n, text := i+1, l.NoComment
		for _, m := range covFailUnderRe.FindAllStringSubmatch(text, -1) {
			add(n, "coverage.py fail_under", "--cov-fail-under", m[1], true)
		}
		for _, m := range failUnderRe.FindAllStringSubmatch(text, -1) {
			key := "--fail-under" + m[1]
			if strings.Contains(text, "coverage") && m[1] == "" && !strings.Contains(text, "diff-cover") {
				key = "coverage.py fail_under"
			}
			add(n, key, "--fail-under"+m[1], m[2], true)
		}
		if strings.Contains(text, "nyc") || strings.Contains(text, "c8") {
			for _, m := range nycFlagRe.FindAllStringSubmatch(text, -1) {
				add(n, "nyc "+m[1], "nyc/c8 --"+m[1], m[2], true)
			}
			if nycCheckCoverage.MatchString(text) {
				add(n, "nyc check-coverage", "nyc/c8 --check-coverage", "true", true)
				out[len(out)-1].gate = true
			}
		}
		if m := jestCLIRe.FindStringSubmatch(text); m != nil {
			for _, mm := range jestCLIMetricRe.FindAllStringSubmatch(m[1], -1) {
				add(n, "jest global."+mm[1], "jest --coverageThreshold "+mm[1], mm[2], true)
			}
		}
		for _, m := range minCoverageFlag.FindAllStringSubmatch(text, -1) {
			add(n, "--"+m[1], "--"+m[1], m[2], true)
		}
		for _, m := range coverageEnvRe.FindAllStringSubmatch(text, -1) {
			add(n, "env "+m[1], m[1], m[2], true)
		}
		if isJVM {
			for _, re := range []*regexp.Regexp{jacocoMinRe, gradleMinRe} {
				if m := re.FindStringSubmatch(text); m != nil {
					add(n, "jacoco minimum", "JaCoCo minimum", m[1], false)
				}
			}
			if m := koverMinRe.FindStringSubmatch(text); m != nil {
				add(n, "kover minBound", "Kover minBound", m[1], false)
			}
			if m := haltOnFailureRe.FindStringSubmatch(text); m != nil {
				add(n, "jacoco haltOnFailure", "JaCoCo haltOnFailure", m[1], false)
				out[len(out)-1].gate = true
			}
		}
		if isRuby {
			if m := simplecovRe.FindStringSubmatch(text); m != nil {
				add(n, "simplecov minimum_coverage", "SimpleCov minimum_coverage", m[1], false)
			}
		}
	}
	return out
}

// sideThresholds returns all thresholds of one side of a file.
func sideThresholds(f *core.FileInfo, old bool) []threshold {
	var s side
	if old {
		s = oldSide(f)
	} else {
		s = newSide(f)
	}
	// Only files that configure or run verification: a coverage flag in
	// a string of application code is not a gate. noxfile.py holds its
	// commands in string arguments (session.run("pytest", "--cov-fail-under=90")).
	nox := strings.EqualFold(path.Base(f.Path), "noxfile.py")
	if !isConfigFile(f) && kindOf(f) == kindNone && f.Lang != lang.Ruby && !isShebangScript(s) && !nox {
		return nil
	}
	out := lineThresholds(f, s)
	if !isConfigFile(f) {
		return out
	}
	if o, n, ok := configSides(f); ok {
		es := n
		if old {
			es = o
		}
		out = append(out, structuredThresholds(f, es)...)
	} else {
		out = append(out, fragmentThresholds(f, s)...)
	}
	return out
}

// fragmentKeyRe reads "key: number" lines of coverage configs when only a
// diff is available.
var fragmentKeyRe = regexp.MustCompile(`^\s*["']?(lines|branches|functions|statements|fail_under|fail-under|target|threshold)["']?\s*[:=]\s*["']?(-?[\d.]+)%?["']?\s*,?\s*$`)

// fragmentThresholds is the bare-patch fallback for config files: leaf keys
// on hunk lines, without their parents.
func fragmentThresholds(f *core.FileInfo, s side) []threshold {
	switch f.Config {
	case core.CfgJest, core.CfgVitest, core.CfgNYC, core.CfgCoverage, core.CfgCodecov, core.CfgPyproject, core.CfgSetupCfg, core.CfgTox:
	default:
		return nil
	}
	var out []threshold
	for i := range s.lines {
		l, ok := s.get(i + 1)
		if !ok {
			continue
		}
		m := fragmentKeyRe.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		k := normKey(m[1])
		if (k == "target" || k == "threshold") != (f.Config == core.CfgCodecov) {
			continue
		}
		t := threshold{f: f, key: f.Config + " " + k, label: m[1], value: m[2], line: i + 1, looser: k == "threshold"}
		t.num, t.isNum = parseNum(m[2])
		// Give the leaf the cross-tool identity its full parse would have,
		// so a threshold moving between files still pairs up.
		switch {
		case k == "fail-under" && f.Config != core.CfgJest && f.Config != core.CfgVitest && f.Config != core.CfgNYC:
			t.canon = "coverage.py fail_under"
		case coverageMetrics[k] && (f.Config == core.CfgJest || f.Config == core.CfgVitest || f.Config == core.CfgNYC):
			t.canon = "js " + k
		}
		out = append(out, t)
	}
	return out
}

// weaker reports whether moving from o to n loosened the gate.
func weaker(o, n threshold) bool {
	if o.gate {
		return o.strict() && !n.strict()
	}
	if !o.isNum || !n.isNum {
		return false
	}
	if o.looser {
		return n.num > o.num
	}
	return n.num < o.num
}

// pairThresholds pairs old and new thresholds of one key: equal values
// cancel, the rest pair up in order, and leftovers are removed or added.
func pairThresholds(olds, news []threshold) (pairs [][2]threshold, removed, added []threshold) {
	used := make([]bool, len(news))
	var restOld []threshold
	for _, o := range olds {
		matched := false
		for j, n := range news {
			if !used[j] && n.value == o.value {
				used[j], matched = true, true
				break
			}
		}
		if !matched {
			restOld = append(restOld, o)
		}
	}
	var restNew []threshold
	for j, n := range news {
		if !used[j] {
			restNew = append(restNew, n)
		}
	}
	for len(restOld) > 0 && len(restNew) > 0 {
		pairs = append(pairs, [2]threshold{restOld[0], restNew[0]})
		restOld, restNew = restOld[1:], restNew[1:]
	}
	return pairs, restOld, restNew
}

func checkCoverage(a *core.Analysis) []core.Finding {
	type keyed map[string][]threshold
	var (
		out                    []core.Finding
		orphansOld, orphansNew []threshold
		allNew                 []threshold
	)
	for _, f := range a.Files {
		if len(f.Added)+len(f.Removed) == 0 {
			continue
		}
		olds, news := keyed{}, keyed{}
		for _, t := range sideThresholds(f, true) {
			olds[t.key] = append(olds[t.key], t)
		}
		for _, t := range sideThresholds(f, false) {
			news[t.key] = append(news[t.key], t)
			allNew = append(allNew, t)
		}
		keys := map[string]bool{}
		for k := range olds {
			keys[k] = true
		}
		for k := range news {
			keys[k] = true
		}
		for _, k := range sortedKeys(keys) {
			pairs, removed, added := pairThresholds(olds[k], news[k])
			for _, p := range pairs {
				if weaker(p[0], p[1]) {
					out = append(out, lowered(p[0], p[1]))
				}
			}
			orphansOld = append(orphansOld, removed...)
			orphansNew = append(orphansNew, added...)
		}
	}
	// A threshold removed from one file may have moved to another.
	usedNew := make([]bool, len(orphansNew))
	for _, o := range orphansOld {
		moved := false
		for j, n := range orphansNew {
			if usedNew[j] || n.canonKey() != o.canonKey() {
				continue
			}
			usedNew[j], moved = true, true
			if weaker(o, n) {
				out = append(out, lowered(o, n))
			}
			break
		}
		// Dropping a gate that was off, or an inverse gate (codecov
		// informational), restores the strict default.
		if moved || o.gate && (!o.strict() || o.inverse) || scopeDeleted(a, o) || packageDeleted(a, o.f) || stillGated(o, allNew) {
			continue
		}
		out = append(out, removedThreshold(o))
	}
	for j, n := range orphansNew {
		// codecov informational: true added where there was nothing
		if !usedNew[j] && n.gate && n.inverse && !n.strict() {
			out = append(out, sevFinding(coverageLowered, n.f, n.line, core.Warning,
				fmt.Sprintf("%s set to %s: the coverage status no longer blocks merges", n.label, n.value)))
		}
	}
	return append(out, jestPathExemptions(orphansNew, usedNew, allNew)...)
}

// jestPathExemptions reports new per-path jest thresholds below the
// global ones, one finding per path. Jest checks files matched by a path
// threshold against that threshold only and leaves them out of the global
// numbers, so `'./src/payments/': { lines: 0 }` exempts the directory.
func jestPathExemptions(added []threshold, used []bool, all []threshold) []core.Finding {
	type scoped struct {
		first        threshold
		below, globe []string
	}
	var order []string
	byScope := map[string]*scoped{}
	for j, n := range added {
		rest, ok := strings.CutPrefix(n.key, "jest ")
		if used[j] || !ok || !n.isNum {
			continue
		}
		i := strings.LastIndex(rest, ".")
		if i <= 0 || rest[:i] == "global" {
			continue
		}
		scope, metric := rest[:i], rest[i+1:]
		for _, g := range all {
			if g.f != n.f || g.key != "jest global."+metric || !g.isNum || n.num >= g.num {
				continue
			}
			id := n.f.Path + "\x00" + scope
			sc := byScope[id]
			if sc == nil {
				sc = &scoped{first: n}
				byScope[id] = sc
				order = append(order, id)
			}
			sc.below = append(sc.below, metric+" "+displayValue(n.value))
			sc.globe = append(sc.globe, displayValue(g.value))
			break
		}
	}
	var out []core.Finding
	for _, id := range order {
		sc := byScope[id]
		scope := id[strings.IndexByte(id, 0)+1:]
		out = append(out, sevFinding(coverageLowered, sc.first.f, sc.first.line, core.Warning,
			fmt.Sprintf("new jest coverageThreshold[%s] (%s) is below the global thresholds (%s): jest checks those files against it instead of the global gate",
				quote(scope), strings.Join(sc.below, ", "), strings.Join(sc.globe, "/"))))
	}
	return out
}

// stillGated reports whether a removed threshold was a duplicate: the same
// gate, at least as strict, is still set somewhere in the change (a CI job
// dropped while another job keeps the same --cov-fail-under).
func stillGated(o threshold, news []threshold) bool {
	for _, n := range news {
		if n.canonKey() == o.canonKey() && (n.value == o.value || !weaker(o, n)) && (n.isNum == o.isNum || o.gate) {
			return true
		}
	}
	return false
}

// scopeDeleted reports whether a per-directory threshold (jest
// coverageThreshold['./src/legacy/']) belongs to code this change deletes.
func scopeDeleted(a *core.Analysis, t threshold) bool {
	if !strings.HasPrefix(t.key, "jest ") && !strings.HasPrefix(t.key, "vitest ") {
		return false
	}
	rest := strings.SplitN(t.key, " ", 2)[1]
	i := strings.LastIndex(rest, ".")
	if i <= 0 {
		return false
	}
	scope := normPattern(rest[:i])
	scope = strings.TrimSuffix(strings.TrimSuffix(scope, "/**"), "/*")
	if scope == "" || scope == "global" || strings.ContainsAny(scope, "*?") {
		return false
	}
	for _, f := range a.Files {
		if f.Diff.Kind == diff.Deleted && strings.HasPrefix(f.Path, scope) {
			return true
		}
	}
	return false
}

// packageDeleted reports whether a deleted config file went away together
// with the code next to it (a package or service removed from a monorepo).
func packageDeleted(a *core.Analysis, f *core.FileInfo) bool {
	if f.Diff.Kind != diff.Deleted {
		return false
	}
	dir := path.Dir(f.Path)
	if dir == "." {
		return false
	}
	for _, g := range a.Files {
		if g != f && g.Diff.Kind == diff.Deleted && strings.HasPrefix(g.Path, dir+"/") && g.Lang.IsProgramming() {
			return true
		}
	}
	return false
}

// lowered builds the finding for a weakened threshold.
func lowered(o, n threshold) core.Finding {
	var msg string
	switch {
	case o.gate && o.inverse:
		return sevFinding(coverageLowered, n.f, n.line, core.Warning,
			fmt.Sprintf("%s changed %s → %s: the coverage status no longer blocks merges", n.label, o.value, n.value))
	case o.gate:
		msg = fmt.Sprintf("%s turned off (%s → %s): coverage is no longer enforced", n.label, o.value, n.value)
	case o.looser:
		msg = fmt.Sprintf("%s raised %s → %s: a bigger coverage drop is now allowed", n.label, o.value, n.value)
	default:
		msg = fmt.Sprintf("%s lowered %s → %s", n.label, displayValue(o.value), displayValue(n.value))
	}
	if n.f != o.f {
		msg += fmt.Sprintf(" (moved from %s)", o.f.Path)
	}
	fd := coverageLowered.Finding(n.f, n.line, msg)
	if l, ok := o.f.OldLine(o.line); ok {
		fd.Before = strings.TrimSpace(l.Raw)
	}
	return fd
}

// removedThreshold builds the finding for a threshold that disappeared.
func removedThreshold(o threshold) core.Finding {
	sev := core.Error
	what := "removed"
	if o.cli {
		// The same threshold may live in a config file this change does
		// not touch.
		sev = core.Warning
		what = "removed from the command line"
	}
	msg := fmt.Sprintf("%s (%s) %s: coverage is no longer enforced at that level", o.label, o.value, what)
	return removalFinding(coverageLowered, o.f, o.line, sev, msg)
}

// displayValue renders a threshold as written, without quotes.
func displayValue(v string) string { return strings.Trim(strings.TrimSpace(v), `"'`) }
