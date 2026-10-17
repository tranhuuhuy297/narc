package escapes

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
	"github.com/huyth45/narc/internal/lang"
)

// ---- file versions ----

// side gives random access to one version of a file. With full contents
// every line is known; for a bare patch only the lines the hunks show are,
// and they are stored sparsely: a hunk deep inside a huge file costs no
// more than one at its top.
type side struct {
	lg    lang.Lang
	lines []lang.LexedLine // full content: lines[i] is line i+1
	frag  map[int]lang.LexedLine
	views map[int]*view // lazily built
	n     int           // number of lines (the last known one, for fragments)
	nos   []int         // known line numbers in ascending order, lazily built
}

func newSide(f *core.FileInfo) *side {
	if f.HasNew() {
		return fullSide(f.Lang, f.NewLines())
	}
	return fragmentSide(f, true)
}

func oldSide(f *core.FileInfo) *side {
	if f.HasOld() {
		return fullSide(f.Lang, f.OldLines())
	}
	return fragmentSide(f, false)
}

func fullSide(lg lang.Lang, ls []lang.LexedLine) *side {
	return &side{lg: lg, lines: ls, n: len(ls), views: map[int]*view{}}
}

// fragmentSide reconstructs the parts of one version that the hunks show.
func fragmentSide(f *core.FileInfo, newer bool) *side {
	s := &side{lg: f.Lang, frag: map[int]lang.LexedLine{}, views: map[int]*view{}}
	get := f.OldLine
	if newer {
		get = f.NewLine
	}
	for _, h := range f.Diff.Hunks {
		for _, l := range h.Lines {
			n := l.OldNo
			if newer {
				n = l.NewNo
			}
			if n <= 0 {
				continue
			}
			if ll, ok := get(n); ok {
				s.frag[n] = ll
				if n > s.n {
					s.n = n
				}
			}
		}
	}
	return s
}

// len returns the number of lines (known or not).
func (s *side) len() int { return s.n }

// lineNos returns the known line numbers in ascending order, for loops
// over a whole version.
func (s *side) lineNos() []int {
	if s.nos == nil {
		if s.frag == nil {
			s.nos = make([]int, s.n)
			for i := range s.nos {
				s.nos[i] = i + 1
			}
		} else {
			s.nos = make([]int, 0, len(s.frag))
			for n := range s.frag {
				s.nos = append(s.nos, n)
			}
			sort.Ints(s.nos)
		}
	}
	return s.nos
}

// at returns line n (1-based) and whether it is known.
func (s *side) at(n int) (lang.LexedLine, bool) {
	if s.frag != nil {
		l, ok := s.frag[n]
		return withoutBOM(n, l), ok
	}
	if n < 1 || n > len(s.lines) {
		return lang.LexedLine{}, false
	}
	return withoutBOM(n, s.lines[n-1]), true
}

// bom is the UTF-8 byte-order mark some editors write at the start of a file.
const bom = "\ufeff"

// withoutBOM drops a byte-order mark from line 1, so that patterns
// anchored at the start of a line (`^\s*#pragma`, `^\s*except`) match it.
func withoutBOM(n int, l lang.LexedLine) lang.LexedLine {
	if n != 1 || !strings.HasPrefix(l.Raw, bom) {
		return l
	}
	l.Raw = strings.TrimPrefix(l.Raw, bom)
	l.Code = strings.TrimPrefix(l.Code, bom)
	l.NoComment = strings.TrimPrefix(l.NoComment, bom)
	return l
}

// view returns the tokenized view of line n, or nil if it is unknown.
func (s *side) view(n int) *view {
	if v, ok := s.views[n]; ok {
		return v
	}
	l, ok := s.at(n)
	if !ok {
		return nil
	}
	v := newView(s.lg, l)
	s.views[n] = v
	return v
}

// code returns the code of line n (comments and string contents removed)
// and whether the line is known.
func (s *side) code(n int) (string, bool) {
	l, ok := s.at(n)
	return l.Code, ok
}

// nextCode returns the first line after n that has code, skipping blank and
// comment-only lines; 0 if there is none or an unknown line intervenes.
func (s *side) nextCode(n int) int {
	for m := n + 1; m <= s.len(); m++ {
		c, ok := s.code(m)
		if !ok {
			return 0
		}
		if strings.TrimSpace(c) != "" {
			return m
		}
	}
	return 0
}

// block finds the brace-delimited block whose '{' is at code offset col of
// line n. It returns the line of the matching '}', the code between the
// braces, and ok=false when the block is unbalanced or runs into an unknown
// line. Blocks longer than maxBlock lines are given up on.
func (s *side) block(n, col int) (end int, inner string, ok bool) {
	const maxBlock = 400
	var b strings.Builder
	depth := 0
	for m := n; m <= s.len() && m < n+maxBlock; m++ {
		c, known := s.code(m)
		if !known {
			return 0, "", false
		}
		i := 0
		if m == n {
			i = col
		} else {
			b.WriteByte('\n')
		}
		for ; i < len(c); i++ {
			switch c[i] {
			case '{':
				depth++
				if depth == 1 && m == n && i == col {
					continue
				}
			case '}':
				depth--
				if depth == 0 {
					return m, b.String(), true
				}
			}
			b.WriteByte(c[i])
		}
	}
	return 0, "", false
}

// comments returns the comment text of lines [from, to] joined by spaces.
func (s *side) comments(from, to int) string {
	var parts []string
	for m := from; m <= to; m++ {
		if l, ok := s.at(m); ok && l.Comment != "" {
			parts = append(parts, l.Comment)
		}
	}
	return strings.Join(parts, " ")
}

// indent returns the width of the leading whitespace of a line.
func indent(raw string) int {
	n := 0
	for _, r := range raw {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// changedLines returns the new-side lines a change touched: added lines and
// the line right after each removal (so a block whose body was deleted
// counts as changed).
func changedLines(f *core.FileInfo) map[int]bool {
	ch := map[int]bool{}
	for _, h := range f.Diff.Hunks {
		last := h.NewStart - 1
		for _, l := range h.Lines {
			switch {
			case l.Op == diff.Add:
				ch[l.NewNo] = true
				last = l.NewNo
			case l.Op == diff.Del:
				ch[last+1] = true
			default:
				last = l.NewNo
			}
		}
	}
	return ch
}

// sideLine returns line n of the old or the new version of f, for scanners
// that look at the lines around the one they scan.
func sideLine(f *core.FileInfo, old bool, n int) (lang.LexedLine, bool) {
	var l lang.LexedLine
	var ok bool
	if old {
		l, ok = f.OldLine(n)
	} else {
		l, ok = f.NewLine(n)
	}
	return withoutBOM(n, l), ok
}

// ---- test code ----

// testLines reports, for new-side line numbers of f, whether the line is
// test code. Rust keeps unit tests next to the code in #[cfg(test)]
// modules, so for Rust source files only those regions count as tests.
func testLines(a *core.Analysis, f *core.FileInfo) func(int) bool {
	if f.Lang == lang.Rust && !core.IsTestPath(f.Path) && !core.MatchAny(a.Settings.TestGlobs, f.Path) {
		// Also covers bare patches, where core cannot see #[cfg(test)].
		regions := rustTestRegions(newSide(f))
		return regions.has
	}
	if !f.IsTest {
		return func(int) bool { return false }
	}
	return func(int) bool { return true }
}

var cfgTestRe = regexp.MustCompile(`#\[\s*cfg\s*\(\s*(?:all\s*\(\s*)?test\b`)

// lineRanges is a set of line numbers, stored as inclusive ranges.
type lineRanges [][2]int

func (r lineRanges) has(n int) bool {
	for _, lr := range r {
		if n >= lr[0] && n <= lr[1] {
			return true
		}
	}
	return false
}

// rustTestRegions returns the lines of items annotated with #[cfg(test)].
func rustTestRegions(s *side) lineRanges {
	var regions lineRanges
	skip := 0
	for _, n := range s.lineNos() {
		if n <= skip {
			continue
		}
		c, ok := s.code(n)
		if !ok || !cfgTestRe.MatchString(c) {
			continue
		}
		end := n
		// The item ends at its first ';' or at the brace matching its '{'.
	scan:
		for m := n; m <= s.len() && m < n+20; m++ {
			cm, ok := s.code(m)
			if !ok {
				break
			}
			start := 0
			if m == n {
				start = cfgTestRe.FindStringIndex(cm)[1]
				if i := strings.Index(cm[start:], "]"); i >= 0 {
					start += i + 1
				}
			}
			for i := start; i < len(cm); i++ {
				switch cm[i] {
				case ';':
					end = m
					break scan
				case '{':
					if e, _, ok := s.block(m, i); ok {
						end = e
					} else {
						end = s.len()
					}
					break scan
				}
			}
		}
		regions = append(regions, [2]int{n, end})
		skip = end
	}
	return regions
}

// testSupportRe matches paths of test infrastructure that is not itself a
// test file: mocks, fixtures, test helpers, test setup files.
var testSupportRe = regexp.MustCompile(`(?i)(^|/)(` +
	`__mocks__|__fixtures__|mocks?|fakes?|stubs?|fixtures?|testdata|testutils?|test[-_]utils?|` +
	`test[-_]?helpers?|test[-_]?support|testing|spec[-_]?support|cypress|playwright|\.storybook` +
	`)/|(^|/)(` +
	`test[-_]?(utils?|helpers?|setup|support|fixtures?)|setup[-_.]?tests?|(jest|vitest|test|mocha|karma)[-_.]setup|` +
	`setup[-_.](jest|vitest)|test_helper|spec_helper|rails_helper|testing|testutils?|mocks?|fakes?|stubs?` +
	`)\.[\w.]+$`)

// isTestSupport reports whether p is test infrastructure (see testSupportRe).
func isTestSupport(p string) bool {
	return testSupportRe.MatchString(strings.TrimPrefix(path.Clean("/"+p), "/"))
}

// bootstrapRe matches configuration, settings and entry-point files, where
// environment-dependent wiring is expected.
var bootstrapRe = regexp.MustCompile(`(?i)(^|/)(` +
	`config|configs|conf|settings|environments|initializers|bootstrap|scripts|bin|tools|deploy` +
	`)/|(^|/)(` +
	`[\w.-]*\.config\.[cm]?[jt]sx?|\.[\w-]+rc\.[cm]?js|config\.[\w]+|settings(_\w+)?\.py|\w+_settings\.py|` +
	`setup\.(py|cfg|[cm]?[jt]s)|manage\.py|wsgi\.py|asgi\.py|gunicorn\.conf\.py|knexfile\.\w+|gulpfile\.\w+|gruntfile\.\w+|` +
	`webpack\.[\w.]+|rollup\.[\w.]+|babel\.[\w.]+|server\.[cm]?[jt]s|main\.(go|py|[cm]?[jt]s|rs|kt|swift|dart)|` +
	`app\.(py|[cm]?[jt]s)|program\.cs|startup\.cs|\w*application\.(java|kt|rb)|env\.([cm]?[jt]s|py)|` +
	`\w*config(uration)?\.(java|kt|cs)|environment\.rb|boot\.rb|config\.ru|mix\.exs|build\.rs|instrumentation\.[jt]s` +
	`)$|^((src|lib|server|app)/)?index\.[cm]?[jt]s$`)

// isBootstrap reports whether p is a config, settings or entry-point file.
func isBootstrap(p string) bool {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	return core.ConfigKind(p) != "" || bootstrapRe.MatchString(p)
}

// ---- explanations ----

var trackingRefRe = regexp.MustCompile(`#\d+\b|https?://\S+|\b[A-Z][A-Z0-9]+-\d+\b`)

// confessionRe matches reasons that admit the point was to get past a check.
var confessionRe = regexp.MustCompile(`(?i)\b(tests?|ci|build|pipeline|checks?|lint(er)?|tsc|mypy|coverage)\s+(to\s+)?(pass|passes|passing|green|happy|succeed)\b|` +
	`\b(make|making|get|getting|keep|keeping)\b.{0,40}\b(pass|passing|green|happy|compile|build)\b|\bto pass\b|\bfor now\b|` +
	`\b(?:for|in|under|during)\s+(?:the\s+)?tests?\b|\btests?\s+(?:need|needs|require|requires|expects?)\b|\bunblock(?:s|ing)?\b`)

// fillerWords carry no information about why a suppression is safe.
var fillerWords = map[string]bool{
	"ignore": true, "ignored": true, "ignoring": true, "todo": true, "fixme": true, "fix": true, "later": true,
	"temp": true, "temporary": true, "temporarily": true, "hack": true, "hacky": true, "needed": true, "need": true,
	"needs": true, "required": true, "require": true, "necessary": true, "for": true, "now": true, "this": true,
	"that": true, "the": true, "and": true, "not": true, "here": true, "line": true, "rule": true, "error": true,
	"errors": true, "warning": true, "warnings": true, "lint": true, "linter": true, "disable": true,
	"disabled": true, "suppress": true, "suppressed": true, "type": true, "types": true, "check": true,
	"checks": true, "false": true, "positive": true, "workaround": true, "quick": true, "just": true,
	"ok": true, "okay": true, "fine": true, "safe": true, "intentional": true, "intended": true, "expected": true,
	"cover": true, "coverage": true, "pragma": true, "noqa": true, "nolint": true, "any": true, "explanation": true,
	"reason": true, "unused": true, "swallow": true, "catch": true, "exception": true,
}

// explained reports whether note gives a reviewer a concrete reason: a
// tracking reference, or a few meaningful words that do not merely admit
// the goal was to get past a check.
func explained(note string) bool {
	note = strings.TrimSpace(note)
	if note == "" || confessionRe.MatchString(note) {
		return false
	}
	if trackingRefRe.MatchString(note) {
		return true
	}
	words := strings.FieldsFunc(strings.ToLower(note), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	meaningful := 0
	for _, w := range words {
		if len(w) >= 3 && !fillerWords[w] {
			meaningful++
		}
	}
	return len(words) >= 3 && meaningful >= 2
}

// lineNote returns the human-written comment text that accompanies new-side
// line n: its own comment and the comment lines right above it.
// Suppression directives are reduced to the reason they carry.
func lineNote(f *core.FileInfo, s *side, n int) string {
	var parts []string
	if l, ok := s.at(n); ok && l.Comment != "" {
		parts = append(parts, noteOf(f.Lang, l.Comment))
	}
	parts = append(parts, commentsAbove(f, s, n))
	return strings.TrimSpace(strings.Join(parts, " "))
}

// commentsAbove returns the text of up to two comment-only lines directly
// above line n. Doc comments describe the code below them, not a
// suppression, so they end the search.
func commentsAbove(f *core.FileInfo, s *side, n int) string {
	var parts []string
	for m := n - 1; m >= n-2; m-- {
		l, ok := s.at(m)
		if !ok || strings.TrimSpace(l.Code) != "" || l.Comment == "" {
			break
		}
		raw := strings.TrimSpace(l.Raw)
		if strings.HasPrefix(raw, "/*") || strings.HasPrefix(raw, "*") || strings.HasPrefix(raw, "///") {
			break
		}
		parts = append(parts, noteOf(f.Lang, l.Comment))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// ---- findings ----

// maxEvidence bounds the evidence of a finding: a match on a huge generated
// line should not copy the whole line into every report and fingerprint.
const maxEvidence = 300

// newFinding builds a finding of rule r at new-side line n of f.
func newFinding(r *core.Rule, f *core.FileInfo, n int, msg string) core.Finding {
	fd := r.Finding(f, n, msg)
	fd.Evidence = strings.TrimPrefix(fd.Evidence, bom)
	if len(fd.Evidence) > maxEvidence {
		fd.Evidence = shorten(fd.Evidence, maxEvidence)
	}
	return fd
}

// ---- severity ----

// downgrade lowers a severity by one step (never below info).
func downgrade(s core.Severity) core.Severity {
	if s > core.Info {
		return s - 1
	}
	return s
}

// ---- small helpers ----

func langIn(l lang.Lang, set ...lang.Lang) bool {
	for _, x := range set {
		if x == l {
			return true
		}
	}
	return false
}

var (
	jsLike   = []lang.Lang{lang.JavaScript, lang.TypeScript}
	jvmLangs = []lang.Lang{lang.Java, lang.Kotlin, lang.Scala, lang.Groovy}
	cLangs   = []lang.Lang{lang.C, lang.Cpp}
)

// shorten trims s to at most n runes for messages.
func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// list renders names as "a, b and c".
func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
