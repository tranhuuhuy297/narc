package escapes

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var coverageIgnored = &core.Rule{
	ID:       "coverage-ignored",
	Title:    "Code excluded from coverage",
	Category: "escapes",
	Severity: core.Warning,
	Summary:  "New code is excluded from coverage measurement (istanbul/c8 ignore, # pragma: no cover, LCOV_EXCL, [ExcludeFromCodeCoverage], JaCoCo's @Generated, ...).",
	Hint: "Write a test that exercises this code instead of excluding it from coverage. " +
		"Exclude code only when it genuinely cannot run under tests (platform-specific or defensive branches), and say why next to the marker.",
	Docs: "Coverage thresholds are only meaningful if they measure the code that matters. Excluding new code from " +
		"measurement makes a threshold pass without anyone testing that code.\n\n" +
		"```js\n// bad: the new branch is untested and invisible to the coverage gate\n/* istanbul ignore next */\nif (retries > max) {\n  return fallback()\n}\n```\n\n" +
		"Idiomatic exclusions are not reported: `if __name__ == \"__main__\":`, `if TYPE_CHECKING:`, " +
		"`raise NotImplementedError`, abstract methods, exhaustiveness checks, and branches for another platform " +
		"or language version (`if sys.platform == \"win32\":`). Exclusions in test code are " +
		"not reported, and an exclusion with a concrete reason is lowered to a note (unless the reason admits the " +
		"code is untested: \"out of scope\", \"TODO\"). Also not reported: exclusions inside an " +
		"`if __name__ == \"__main__\":` block, a function's trailing `return None` after a loop that always returns, " +
		"and vendored code; the clauses of one try/except are one exclusion. A file that gains five or more " +
		"exclusions is reported once, as a warning, whatever their reasons.\n\n" +
		"JaCoCo leaves out every class and method annotated with an annotation named `Generated`, so `@Generated` " +
		"on hand-written code counts as an exclusion.",
}

func init() {
	coverageIgnored.Check = checkCoverageIgnored
	core.Register(coverageIgnored)
}

// jsCoverage builds the spec of an Istanbul-style JavaScript marker.
func jsCoverage(tool, prefix string) *dirSpec {
	return &dirSpec{tool: tool, langs: jsLike, wide: regexp.MustCompile(`(start|file)$`), wideWhere: "up to the end marker",
		re: regexp.MustCompile(`^[\s*]*(?P<dir>` + prefix + `\s+ignore\s+(?:next(?:\s+\d+)?|if|else|file|start))\b(?P<note>.*)$`)}
}

// lcovLangs are the languages lcov/gcovr markers are used from.
var lcovLangs = []lang.Lang{lang.C, lang.Cpp, lang.Rust, lang.Swift, lang.Go, lang.Java, lang.Kotlin, lang.CSharp,
	lang.JavaScript, lang.TypeScript, lang.Python, lang.Ruby, lang.PHP, lang.Dart, lang.Scala, lang.Shell}

// coverageSpecs are the coverage exclusion markers narc knows.
var coverageSpecs = []*dirSpec{
	jsCoverage("Istanbul", "istanbul"),
	jsCoverage("c8", "c8"),
	jsCoverage("V8", "v8"),
	{tool: "Node test runner", langs: jsLike, wide: regexp.MustCompile(`disable$`), wideWhere: "up to the end marker",
		re: regexp.MustCompile(`^[\s*]*(?P<dir>node:coverage\s+(?:ignore\s+next(?:\s+\d+)?|disable))\b(?P<note>.*)$`)},
	{tool: "coverage.py", langs: python, sameLine: true,
		re: regexp.MustCompile(`(?i)(?:^|#)\s*(?P<dir>pragma:?\s*no\s*(?:cover|branch))\b(?P<note>.*)$`)},
	{tool: "Go coverage", langs: golang,
		re: regexp.MustCompile(`^\s*(?P<dir>go:coverage\s+ignore|coverage-ignore)\b(?P<note>.*)$`)},
	{tool: "lcov", langs: lcovLangs, wide: regexp.MustCompile(`START$`), wideWhere: "up to the end marker",
		re: regexp.MustCompile(`\b(?P<dir>(?:LCOV|GCOVR|GCOV)_EXCL_(?:BR_)?(?:LINE|START))\b(?P<note>.*)$`)},
	{tool: "PHPUnit", langs: php, item: true, wide: regexp.MustCompile(`Start$`), wideWhere: "up to the end marker",
		re: regexp.MustCompile(`^[\s*]*(?P<dir>@codeCoverageIgnore(?:Start)?)\b(?P<note>.*)$`)},
	{tool: ".NET coverage", langs: csharp, code: true, item: true,
		re: regexp.MustCompile(`\[\s*(?P<wide>assembly\s*:\s*)?(?:[\w.]*\.)?(?P<dir>ExcludeFromCodeCoverage)(?:Attribute)?\b(?:\s*\((?P<note>[^\]]*)\))?\s*\]`)},
	// JaCoCo skips any class or method annotated with an annotation named
	// Generated (retained in the class file), hand-written or not.
	{tool: "JaCoCo", langs: jvmLangs, code: true, item: true,
		re: regexp.MustCompile(`@(?:[\w.]+\.)?(?P<dir>Generated)\b(?:\s*\((?P<note>[^)]*)\))?`)},
	{tool: "Rust coverage", langs: rust, code: true, item: true,
		re: regexp.MustCompile(`#(?P<wide>!?)\[\s*(?:cfg_attr\s*\([^,]*,\s*)?(?P<dir>coverage\s*\(\s*off\s*\)|no_coverage)\s*\)?\s*\]`)},
	{tool: "SimpleCov", langs: []lang.Lang{lang.Ruby}, toggle: true, wide: everyLine, wideWhere: "up to the closing :nocov:",
		re: regexp.MustCompile(`^\s*(?P<dir>:nocov:)(?P<note>.*)$`)},
	{tool: "ExCoveralls", langs: []lang.Lang{lang.Elixir}, wide: regexp.MustCompile(`start$`), wideWhere: "up to the end marker",
		re: regexp.MustCompile(`^\s*(?P<dir>coveralls-ignore-(?:start|next-line))\b(?P<note>.*)$`)},
	{tool: "Dart coverage", langs: []lang.Lang{lang.Dart}, wide: regexp.MustCompile(`(start|file)$`), wideWhere: "up to the end marker",
		re: regexp.MustCompile(`^\s*(?P<dir>coverage:ignore-(?:line|start|file))\b(?P<note>.*)$`)},
	{tool: "scoverage", langs: []lang.Lang{lang.Scala}, wide: everyLine, wideWhere: "up to $COVERAGE-ON$",
		re: regexp.MustCompile(`^\s*(?P<dir>\$COVERAGE-OFF\$)(?P<note>.*)$`)},
}

// coverageIdiomRe matches code that is excluded from coverage by long
// convention: entry-point guards, type-checking-only imports, abstract and
// overload stubs, exhaustiveness checks, and branches for another platform
// or language version, which the CI machine cannot run.
var coverageIdiomRe = regexp.MustCompile(`__name__\s*==\s*["']__main__["']|\bTYPE_CHECKING\b|` +
	`\braise\s+NotImplementedError\b|@(?:abc\.)?abstractmethod\b|@(?:typing\.)?overload\b|\bassert_never\(|` +
	`\bassertNever\(|\bunreachable!|\brequire\.main\s*===?\s*module\b|\bimport\.meta\.(?:main|vitest)\b|` +
	`\bexcept\s+\(?(?:ImportError|ModuleNotFoundError)\b|(?m:^\s*(?:pass|\.\.\.)\s*$)|\bdef\s+__repr__\b|` +
	`\b(?:if|elif|else\s+if|unless)\b.*(?:\bsys\.(?:platform|version_info)\b|\bos\.name\b|\bplatform\.system\(\)|` +
	`\bprocess\.platform\b|\bruntime\.GOOS\b|\bRUBY_PLATFORM\b|\bPHP_OS(?:_FAMILY)?\b|\bPHP_VERSION_ID\b|` +
	`\bRuntimeInformation\.IsOSPlatform\(|\bOperatingSystem\.Is\w+\()|\bcfg!\s*\(\s*(?:windows|unix|target_os\b)`)

func checkCoverageIgnored(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool {
		return (f.Lang.IsProgramming() || f.Lang == lang.Shell) && (!f.IsTest || f.Lang == lang.Rust) &&
			!isTestSupport(f.Path) && !isVendored(f.Path) && !isFixtureData(f.Path)
	})
	found := newDirectives(files, coverageSpecs, dirKeys)
	var out []core.Finding
	sides := map[*core.FileInfo]*side{}
	perFile := map[*core.FileInfo][]int{} // indexes into out
	reported := map[*core.FileInfo]map[int]bool{}
	for _, df := range found {
		f := df.file
		if sides[f] == nil {
			sides[f] = newSide(f)
		}
		s := sides[f]
		if testLines(a, f)(df.line.No) {
			continue
		}
		if df.d.spec.toggle && !opensRegion(s, df) {
			continue
		}
		if covered := coveredCode(s, df); covered != "" && coverageIdiomRe.MatchString(covered) {
			continue
		}
		if inMainGuard(s, df.line.No) || trailingFallthrough(s, df) {
			continue
		}
		// `try:  # pragma: no cover` and its `except ...:  # pragma: no cover`
		// exclude one construct: report it once.
		if f.Lang == lang.Python && sameConstruct(s, df.line.No, reported[f]) {
			continue
		}
		sev := coverageIgnored.Severity
		if note := directiveNote(f, s, df); df.d.scope != scopeFile && explained(note) && !untestedRe.MatchString(note) {
			sev = downgrade(sev)
		}
		fd := newFinding(coverageIgnored, f, df.line.No, coverageMessage(f.Lang, df))
		fd.Severity = sev
		if reported[f] == nil {
			reported[f] = map[int]bool{}
		}
		reported[f][df.line.No] = true
		perFile[f] = append(perFile[f], len(out))
		out = append(out, fd)
	}
	return densest(out, perFile)
}

// manyExclusions is how many new exclusions in one file make the file's
// coverage meaningless, whatever each one's reason.
const manyExclusions = 5

// densest folds the findings of a file that gained many exclusions into
// one warning: each pragma may look defensive on its own, but together
// they take most of the new code out of the coverage gate.
func densest(out []core.Finding, perFile map[*core.FileInfo][]int) []core.Finding {
	drop := map[int]bool{}
	for _, idx := range perFile {
		if len(idx) < manyExclusions {
			continue
		}
		var lines []string
		for _, i := range idx {
			lines = append(lines, strconv.Itoa(out[i].Line))
			drop[i] = true
		}
		first := out[idx[0]]
		shown := lines
		if len(shown) > 8 {
			shown = append(shown[:8:8], "…")
		}
		first.Message = fmt.Sprintf("%d coverage exclusions added in this file (lines %s): most of its new branches are "+
			"invisible to the coverage gate", len(idx), strings.Join(shown, ", "))
		first.Severity = core.Warning
		drop[idx[0]] = false
		out[idx[0]] = first
	}
	if len(drop) == 0 {
		return out
	}
	kept := out[:0]
	for i, fd := range out {
		if !drop[i] {
			kept = append(kept, fd)
		}
	}
	return kept
}

// untestedRe matches reasons that admit the code is simply not tested.
var untestedRe = regexp.MustCompile(`(?i)\bout of scope\b|\bnot (?:yet )?tested\b|\buntested\b|\btodo\b|\bfixme\b|\bhard to test\b|\bcan'?t (?:be )?test|\bno time\b`)

// inMainGuard reports whether Python line n sits inside an
// `if __name__ == "__main__":` block, the CLI entry point.
func inMainGuard(s *side, n int) bool {
	if s.lg != lang.Python {
		return false
	}
	l, ok := s.at(n)
	if !ok {
		return false
	}
	ind := indent(l.Raw)
	for m := n - 1; m >= 1 && ind > 0; m-- {
		ml, ok := s.at(m)
		if !ok {
			return false
		}
		if strings.TrimSpace(ml.Code) == "" || indent(ml.Raw) >= ind {
			continue
		}
		ind = indent(ml.Raw)
		if mainGuardRe.MatchString(newView(s.lg, ml).canon) {
			return true
		}
	}
	return false
}

var mainGuardRe = regexp.MustCompile(`^\s*if\s+__name__\s*==\s*["']__main__["']\s*:`)

// trailingFallthrough reports whether df is a single-line exclusion of a
// function's trailing `return None` / `return {}` / `raise ...` after a
// loop or an if-chain that always returns: unreachable code kept for the
// type checker.
func trailingFallthrough(s *side, df dirFinding) bool {
	if df.d.ownLine || df.d.scope != scopeLine {
		return false
	}
	l, ok := s.at(df.line.No)
	if !ok || !fallthroughRe.MatchString(strings.TrimSpace(l.Code)) {
		return false
	}
	ind := indent(l.Raw)
	if p := prevCode(s, df.line.No); p == 0 || indent(rawAt(s, p)) <= ind {
		return false // not right after a nested block
	}
	if nx := s.nextCode(df.line.No); nx != 0 && indent(rawAt(s, nx)) >= ind {
		return false // more code follows in the same block
	}
	return true
}

var fallthroughRe = regexp.MustCompile(`^(?:return(?:\s+(?:None|\{\}|\[\]|False|nil|null|undefined))?|raise\s+\w+(?:\(.*\))?|throw\s+.*|unreachable!\(.*\)|panic\(.*\))\s*;?$`)

func rawAt(s *side, n int) string {
	l, _ := s.at(n)
	return l.Raw
}

// sameConstruct reports whether Python line n is a later clause (except,
// else, elif, finally) of a try or if statement whose head already carries
// a reported exclusion.
func sameConstruct(s *side, n int, reported map[int]bool) bool {
	l, ok := s.at(n)
	if !ok || !laterClauseRe.MatchString(l.Code) {
		return false
	}
	ind := indent(l.Raw)
	for m := n - 1; m >= 1 && m > n-40; m-- {
		ml, ok := s.at(m)
		if !ok {
			return false
		}
		if strings.TrimSpace(ml.Code) == "" || indent(ml.Raw) > ind {
			continue
		}
		if indent(ml.Raw) < ind {
			return false
		}
		if reported[m] {
			return true
		}
		if !laterClauseRe.MatchString(ml.Code) {
			return false // the head of the construct, not excluded
		}
	}
	return false
}

var laterClauseRe = regexp.MustCompile(`^\s*(?:except\b|else\s*:|elif\b|finally\s*:)`)

// coveredCode returns the start of the code a directive excludes: its own
// line, or the next line of code for a marker on a line of its own, plus
// the line after that (a `default:` label and its statement), and the
// decorators right above it (`@abstractmethod` over a `def`).
func coveredCode(s *side, df dirFinding) string {
	n := df.line.No
	if df.d.ownLine {
		n = s.nextCode(n)
	}
	var parts []string
	for m := n - 1; m >= 1 && m > n-4; m-- {
		c, ok := s.code(m)
		if !ok || !strings.HasPrefix(strings.TrimSpace(c), "@") {
			break
		}
		parts = append([]string{s.view(m).canon}, parts...)
	}
	for _, m := range []int{n, s.nextCode(n)} {
		if v := s.view(m); v != nil {
			parts = append(parts, v.canon)
		}
	}
	return strings.Join(parts, "\n")
}

// opensRegion reports whether a toggle marker opens a region (an odd
// number of markers up to and including it) rather than closing one.
func opensRegion(s *side, df dirFinding) bool {
	count := 0
	for _, n := range s.lineNos() {
		if n > df.line.No {
			break
		}
		l, ok := s.at(n)
		if ok && l.Comment != "" && df.d.spec.re.MatchString(l.Comment) {
			count++
		}
	}
	return count%2 == 1
}

func coverageMessage(lg lang.Lang, df dirFinding) string {
	d := df.d
	var what string
	switch d.scope {
	case scopeItem:
		what = "the annotated declaration is"
	case scopeFile:
		switch {
		case strings.HasSuffix(strings.ToLower(d.name), "file"):
			what = "the whole file is"
		case d.spec.item:
			what = "the whole module is"
		default:
			what = "everything " + d.where() + " is"
		}
	default:
		switch {
		case d.next:
			what = "the next statement is"
		case strings.HasSuffix(strings.TrimSpace(df.line.Code), ":"):
			what = "this line and the block it opens are"
		default:
			what = "this line is"
		}
	}
	return fmt.Sprintf("%s added: %s excluded from coverage (%s)", d.quoted(lg), what, d.spec.tool)
}
