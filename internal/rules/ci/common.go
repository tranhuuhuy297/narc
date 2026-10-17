package ci

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// fixtureDirs are directories that hold test inputs: sample projects whose
// package.json, tsconfig or Makefile are data for this repository's tests,
// not its own build. The Go tool ignores testdata/ entirely.
var fixtureDirs = map[string]bool{
	"testdata": true, "fixtures": true, "fixture": true, "__fixtures__": true,
	"test-fixtures": true, "test_fixtures": true,
}

// isFixture reports whether p lies under a fixture directory.
func isFixture(p string) bool {
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		if fixtureDirs[strings.ToLower(s)] {
			return true
		}
	}
	return false
}

// scopedKey is the Memo key of the fixture-free view of an analysis.
type scopedKey struct{}

// scoped wraps a rule's check so that it never sees files under fixture
// directories. Every rule of this package is registered through it. The
// filtered analysis is built once per analysis and shared by all rules, so
// work they memoize on it is shared too; it keeps the content source, so
// unchanged files stay readable.
func scoped(check func(*core.Analysis) []core.Finding) func(*core.Analysis) []core.Finding {
	return func(a *core.Analysis) []core.Finding {
		view := a.Memo(scopedKey{}, func() any {
			keep := make([]*core.FileInfo, 0, len(a.Files))
			for _, f := range a.Files {
				if !isFixture(f.Path) {
					keep = append(keep, f)
				}
			}
			if len(keep) == len(a.Files) {
				return a
			}
			return &core.Analysis{Files: keep, Settings: a.Settings, Source: a.Source}
		}).(*core.Analysis)
		return check(view)
	}
}

// changedCommands returns the commands on a file's new side that touch
// added lines, and those on its old side that touch removed lines.
func changedCommands(f *core.FileInfo) (added, removed []cmdLine) {
	addSet, delSet := lineSet(f.Added), lineSet(f.Removed)
	if len(addSet) > 0 {
		for _, c := range commandLines(f, newSide(f)) {
			if c.overlaps(addSet) {
				added = append(added, c)
			}
		}
	}
	if len(delSet) > 0 {
		for _, c := range commandLines(f, oldSide(f)) {
			if c.overlaps(delSet) {
				removed = append(removed, c)
			}
		}
	}
	return added, removed
}

// firstChanged returns the first line of c that is in set, or c.Line.
func firstChanged(c cmdLine, set map[int]bool) int {
	for n := c.Line; n <= c.End; n++ {
		if set[n] {
			return n
		}
	}
	return c.Line
}

// invocations parses shell text into invocations (sh -c expanded).
func invocations(text string) []invocation {
	var out []invocation
	for _, sc := range expandShell(splitShell(text)) {
		out = append(out, parseInvocation(sc.Words))
	}
	return out
}

// toolNames lists the distinct tools of checks for messages:
// "pytest and npm run lint".
func toolNames(cs []check) string {
	var names []string
	seen := map[string]bool{}
	for _, c := range cs {
		if !seen[c.Tool] {
			seen[c.Tool] = true
			names = append(names, c.Tool)
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// quote renders a value for messages, shortened to 80 characters.
func quote(s string) string {
	s = strings.TrimSpace(s)
	if rs := []rune(s); len(rs) > 80 {
		s = string(rs[:77]) + "..."
	}
	return strconv.Quote(s)
}

// sevFinding builds a finding with a specific severity.
func sevFinding(r *core.Rule, f *core.FileInfo, line int, sev core.Severity, msg string) core.Finding {
	fd := r.Finding(f, line, msg)
	fd.Severity = sev
	return fd
}

// removalFinding anchors a finding at a removed old-side line.
func removalFinding(r *core.Rule, f *core.FileInfo, oldLine int, sev core.Severity, msg string) core.Finding {
	fd := r.Removal(f, oldLine, msg)
	fd.Severity = sev
	return fd
}

// fileFinding anchors a finding at the whole file (deleted files).
func fileFinding(r *core.Rule, f *core.FileInfo, sev core.Severity, msg string) core.Finding {
	fd := r.Finding(f, 0, msg)
	fd.Severity = sev
	if f.Diff.Kind != diff.Deleted && len(f.Added) > 0 {
		fd.Line = f.Added[0].No
	}
	return fd
}

// parseNum parses a numeric setting such as 80, "80%", 0.8 or "-10".
func parseNum(v string) (float64, bool) {
	v = strings.Trim(strings.TrimSpace(v), `"'`)
	v = strings.TrimSuffix(v, "%")
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return n, err == nil
}

// truthy reports whether a config value means "on".
func truthy(v string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)) {
	case "true", "yes", "on", "1", "y":
		return true
	}
	return false
}

// falsy reports whether a config value means "off".
func falsy(v string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`)) {
	case "false", "no", "off", "0", "n":
		return true
	}
	return false
}

// multiset counts strings; it is used to cancel moved lines against each
// other.
type multiset map[string]int

func (m multiset) add(k string) { m[k]++ }

// take removes one k and reports whether there was one.
func (m multiset) take(k string) bool {
	if m[k] > 0 {
		m[k]--
		return true
	}
	return false
}

// sortedKeys returns the keys of a set in order.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// plural renders "1 test" / "3 tests".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// prereleaseRe matches toolchains and jobs that are allowed to fail by
// convention: nightly compilers, beta runtimes, canary dependencies.
var prereleaseRe = regexp.MustCompile(`(?i)(^|[^a-z])(nightly|canary|experimental|beta|alpha|edge|head|preview|unstable|dev|next|rc\d*|snapshot|latest-dev|allowed[-_ ]failure|optional)([^a-z]|$)`)

// nonBlockingRe matches names and comments that declare a check
// deliberately non-blocking.
var nonBlockingRe = regexp.MustCompile(`(?i)(advisory|non[- ]?blocking|informational|best[- ]effort|allowed to fail|may fail|optional)`)

// extraConfigNames are verification configs that core.ConfigKind does not
// classify.
var extraConfigNames = map[string]bool{
	"pyrightconfig.json": true, ".testcoverage.yml": true, ".testcoverage.yaml": true, "tarpaulin.toml": true,
	".tarpaulin.toml": true, "psalm.xml": true, "psalm.xml.dist": true, "composer.json": true, ".simplecov": true,
}

// isConfigFile reports whether f configures a verification tool, so its
// settings are worth parsing.
func isConfigFile(f *core.FileInfo) bool {
	return f.Config != "" || extraConfigNames[strings.ToLower(path.Base(f.Path))]
}

// commandFiles returns the analysis files that can hold commands.
func commandFiles(a *core.Analysis) []*core.FileInfo {
	var out []*core.FileInfo
	for _, f := range a.Files {
		if kindOf(f) != kindNone || f.Lang == lang.Unknown && isShebangScript(newSide(f)) {
			out = append(out, f)
		}
	}
	return out
}

// testPathRe matches paths and globs that name test code.
var testPathRe = regexp.MustCompile(`(?i)(^|[/\\*._-])(tests?|specs?|__tests__|e2e|integration|integration[-_]tests?|unit|cypress|playwright|conftest)([/\\*._-]|$)|[._-](test|spec)\.|_test\.go|test_[^/]*\.py`)

// looksLikeTestPath reports whether a path or glob names test code.
func looksLikeTestPath(p string) bool { return testPathRe.MatchString(strings.Trim(p, `"' `)) }
