package accounting

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
	"github.com/tranhuuhuy297/narc/internal/testblocks"
)

// side is the version of a file a test was found in.
type side int

const (
	before side = iota
	after
)

// segment is a run of lexed lines from one side of a file: the full
// content, or the lines of one diff hunk when full content is unavailable
// (patch-only mode).
type segment struct {
	lines []lang.LexedLine
	nos   []int     // file line number of each line; nil means lines[i] is line i+1
	whole bool      // the segment is the entire file
	hunk  int       // index of the diff hunk it was cut from, -1 for full contents
	lang  lang.Lang // language of this side (a rename may change it)

	counts []int // assertions per line, -1 until counted (see count)

	blocks   []testblocks.Block // its tests, assertions not counted (see tests)
	hasFound bool
}

// tests returns the tests of the segment (found once, assertions not
// counted; see instance.counted).
func (s *segment) tests() []testblocks.Block {
	if !s.hasFound {
		s.blocks, s.hasFound = testblocks.FindLazy(s.lang, s.lines, never), true
	}
	return s.blocks
}

// count returns the number of assertions on line i (0-based) of the
// segment. Several passes ask about the same lines, so counts are cached.
func (s *segment) count(i int) int {
	if s.counts == nil {
		s.counts = make([]int, len(s.lines))
		for k := range s.counts {
			s.counts[k] = -1
		}
	}
	if s.counts[i] < 0 {
		s.counts[i] = testblocks.CountAssertions(s.lang, s.lines[i].Code)
	}
	return s.counts[i]
}

// langOf returns the language of one side of a file. A rename can change
// it (cart.test.js → cart.test.js.bak): the old side is read in the
// language of its old path.
func langOf(f *core.FileInfo, sd side) lang.Lang {
	if sd == before && f.OldPath != "" && f.OldPath != f.Path {
		return lang.Detect(f.OldPath)
	}
	return f.Lang
}

func (s *segment) lineNo(i int) int { // i is 0-based in the segment
	if s.nos == nil {
		return i + 1
	}
	return s.nos[i]
}

// segKey identifies one side of a file.
type segKey struct {
	f  *core.FileInfo
	sd side
}

// segments returns one side of a file as lexed segments, cut once per
// analysis.
func (l *ledger) segments(f *core.FileInfo, sd side) []*segment {
	k := segKey{f, sd}
	if s, ok := l.segs[k]; ok {
		return s
	}
	s := cutSegments(f, sd)
	l.segs[k] = s
	return s
}

// fullContent reports whether both sides of a file can be read in full
// (one side for an added or deleted file). When only one side can (a file
// that grew past the size limit for reading contents), both sides are read
// from the hunks, so that tests outside the hunks do not look deleted.
func fullContent(f *core.FileInfo) bool {
	switch f.Diff.Kind {
	case diff.Added:
		return f.HasNew()
	case diff.Deleted:
		return f.HasOld()
	}
	return f.HasOld() && f.HasNew()
}

// cutSegments returns one side of a file as lexed segments.
func cutSegments(f *core.FileInfo, sd side) []*segment {
	lg := langOf(f, sd)
	relex := lg != f.Lang // the FileInfo lexed this side in another language
	full := fullContent(f)
	switch {
	case sd == before && f.Diff.Kind == diff.Added, sd == after && f.Diff.Kind == diff.Deleted:
		return nil
	case sd == before && full:
		lines := f.OldLines()
		if relex {
			b, _ := f.OldContent()
			lines = lang.Lex(lg, string(b))
		}
		return []*segment{{lines: lines, whole: true, hunk: -1, lang: lg}}
	case sd == after && full:
		return []*segment{{lines: f.NewLines(), whole: true, hunk: -1, lang: lg}}
	}
	// Patch-only: lex each hunk's side. An added or deleted file is a
	// single hunk holding the whole file.
	whole := (f.Diff.Kind == diff.Added || f.Diff.Kind == diff.Deleted) && len(f.Diff.Hunks) == 1
	var out []*segment
	for hi, h := range f.Diff.Hunks {
		seg := &segment{whole: whole, hunk: hi, lang: lg}
		var texts []string
		for _, l := range h.Lines {
			no := l.NewNo
			get := f.NewLine
			if sd == before {
				no, get = l.OldNo, f.OldLine
			}
			if (sd == before && l.Op == diff.Add) || (sd == after && l.Op == diff.Del) || no <= 0 {
				continue
			}
			ll, ok := get(no)
			if !ok || ll.Raw != l.Text {
				ll = lang.LexLine(lg, l.Text)
			}
			seg.lines = append(seg.lines, ll)
			seg.nos = append(seg.nos, no)
			texts = append(texts, l.Text)
		}
		if relex {
			if lines := lang.Lex(lg, strings.Join(texts, "\n")); len(lines) == len(seg.lines) {
				seg.lines = lines
			}
		}
		if len(seg.lines) > 0 {
			if lg == lang.Ruby {
				heredocStart(seg)
			}
			out = append(out, seg)
		}
	}
	return out
}

var (
	// rubyHeredocOpen finds heredoc openers: <<~SQL, <<-EOS, <<'RUBY'.
	rubyHeredocOpen = regexp.MustCompile(`<<[~-]?(['"]?)([A-Z_][A-Z0-9_]*)\b`)
	// rubyHeredocEnd matches a line that can only end a heredoc.
	rubyHeredocEnd = regexp.MustCompile(`^\s*([A-Z_][A-Z0-9_]*)\s*$`)
)

// heredocStart handles a Ruby hunk that begins inside a squiggly heredoc:
// lexed on its own, the heredoc's text reads as code (an `it ... do` in a
// spec template would look like a real example). When a terminator line
// comes before any opener and every line above it is indented deeper (as
// <<~ bodies are), those lines are string contents and are blanked.
func heredocStart(seg *segment) {
	for k, ll := range seg.lines {
		if rubyHeredocOpen.MatchString(ll.Raw) {
			return
		}
		m := rubyHeredocEnd.FindStringSubmatch(ll.Raw)
		if m == nil || k == 0 {
			continue
		}
		ind := indent(ll.Raw)
		for _, above := range seg.lines[:k] {
			if strings.TrimSpace(above.Raw) != "" && indent(above.Raw) <= ind {
				return
			}
		}
		for i := 0; i <= k; i++ {
			seg.lines[i] = lang.LexedLine{Raw: seg.lines[i].Raw}
		}
		return
	}
}

// instance is one test as it exists in one version of a changed file.
type instance struct {
	file    *core.FileInfo
	side    side
	seg     *segment
	block   testblocks.Block // line numbers relative to seg (1-based)
	partial bool             // cut off by the end of a hunk: contents unknown
	order   int

	// siteList, bagOf and sizeOf are computed on first use (see sites and
	// bag): most tests of a changed file are unchanged and never need them.
	siteList []site
	bagOf    map[string]int
	sizeOf   int
	hasSites bool

	match *instance
	how   matchKind
	// former is the plain function this (old) test became, if any.
	former *formerTest
	// asserted holds the identifiers its assertions use (see assertedNames).
	asserted map[string]bool
	// obsolete: a vanished (old) test explained by the code it exercised
	// being removed from production code (see explained).
	obsolete bool
	// touched: the test overlaps the diff's hunks on its side. A test that
	// does not reads the same before and after the change.
	touched bool
}

// site is one assertion of a test.
type site struct {
	line int    // file line number on the instance's side
	key  string // normalized assertion statement
	code string // the statement's Code text
	toks []string
	// stmt holds the tokens of the assertion statement alone (toks may add
	// the condition of an if around it).
	stmt []string
	raw  string
}

// name is the test's qualified name, or a description for a test whose
// name could not be determined (e.g. one built at run time).
func (in *instance) name() string {
	if in.block.Name != "" {
		return in.block.Name
	}
	return fmt.Sprintf("unnamed test at %s:%d", in.file.Path, in.header())
}

// line converts a segment-relative 1-based line to a file line number.
func (in *instance) line(rel int) int { return in.seg.lineNo(rel - 1) }

func (in *instance) header() int { return in.line(in.block.Header) }

func (in *instance) skipped() bool { return in.block.Skipped }

func (in *instance) assertions() int { return in.counted().Assertions }

// counted returns the test's block with its assertions counted: tests the
// change did not touch are counted only when something asks.
func (in *instance) counted() *testblocks.Block {
	if !in.block.Counted() {
		in.block.Count()
	}
	return &in.block
}

// collect finds the tests on one side of a file.
func (l *ledger) collect(f *core.FileInfo, sd side, order *int) []*instance {
	var out []*instance
	touched := l.touchedLines(f, sd)
	for _, seg := range l.segments(f, sd) {
		lazy := seg.whole && seg.nos == nil && touched != nil
		for _, b := range seg.tests() {
			in := &instance{file: f, side: sd, seg: seg, block: b, partial: b.Open && !seg.whole, order: *order}
			in.touched = !lazy || touched.overlaps(b.Start, b.End)
			if in.touched {
				in.block.Count()
			}
			*order++
			out = append(out, in)
		}
	}
	return out
}

// sites returns the test's assertions (computed on first use).
func (in *instance) sites() []site {
	if !in.hasSites {
		in.siteList, in.hasSites = sitesOf(in.seg, *in.counted()), true
	}
	return in.siteList
}

// bag returns the token multiset of the test's body, without its own name
// (computed on first use); size is the number of tokens in it.
func (in *instance) bag() map[string]int {
	if in.bagOf == nil {
		in.bagOf, in.sizeOf = bodyBag(in.seg, in.block)
	}
	return in.bagOf
}

func (in *instance) size() int {
	in.bag()
	return in.sizeOf
}

// spans is a sorted list of inclusive line ranges.
type spans [][2]int

// overlaps reports whether lines from..to meet one of the spans (which
// are in order and do not nest, as diff hunks are).
func (ss spans) overlaps(from, to int) bool {
	i := sort.Search(len(ss), func(i int) bool { return ss[i][1] >= from })
	return i < len(ss) && ss[i][0] <= to
}

// touchedLines returns the line ranges of one side of a modified file that
// the diff's hunks cover, widened by a line on each side so that a pure
// insertion touches the lines around it; nil means every line counts (an
// added or deleted file, or no hunks to go by). Tests outside these ranges
// read the same on both sides.
func (l *ledger) touchedLines(f *core.FileInfo, sd side) spans {
	if f.Diff.Kind != diff.Modified && f.Diff.Kind != diff.Renamed || langOf(f, before) != f.Lang {
		return nil
	}
	var out spans
	for _, h := range f.Diff.Hunks {
		start, n := h.NewStart, h.NewLines
		if sd == before {
			start, n = h.OldStart, h.OldLines
		}
		out = append(out, [2]int{start - 1, start + n})
	}
	return out
}

// sitesOf returns the assertion sites of a block (counted).
func sitesOf(seg *segment, b testblocks.Block) []site {
	var out []site
	for _, rel := range b.AssertionLines {
		i := rel - 1
		if i < 0 || i >= len(seg.lines) {
			continue
		}
		stmt, code, strs := statement(seg, i)
		toks := tokens(code, strs)
		own := toks
		// `if got != 400 {` / `t.Errorf(...)`: the condition is what is checked.
		if i > 0 && rel > b.Header && ifHeadRe.MatchString(strings.TrimSpace(seg.lines[i-1].Code)) {
			toks = append(toks, tokens(seg.lines[i-1].Code, seg.lines[i-1].Strings)...)
		}
		out = append(out, site{line: seg.lineNo(i), key: rulekit.Normalize(stmt), code: code, toks: toks, stmt: own,
			raw: strings.TrimSpace(seg.lines[i].Raw)})
	}
	return out
}

// ifHeadRe matches a line that opens a conditional around an assertion.
var ifHeadRe = regexp.MustCompile(`^(?:\}\s*else\s+)?(?:if|unless|elif|elsif)\b.*[{:]$`)

// statement returns the assertion statement starting on line i: the line
// plus continuation lines until its brackets balance (at most 8 lines).
func statement(seg *segment, i int) (nocomment, code string, strs []string) {
	var nc, cd strings.Builder
	depth := 0
	for k := i; k < len(seg.lines) && k < i+8; k++ {
		ll := seg.lines[k]
		nc.WriteString(ll.NoComment)
		nc.WriteByte(' ')
		cd.WriteString(ll.Code)
		cd.WriteByte(' ')
		strs = append(strs, ll.Strings...)
		for j := 0; j < len(ll.Code); j++ {
			switch ll.Code[j] {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
			}
		}
		if depth <= 0 {
			break
		}
	}
	return nc.String(), cd.String(), strs
}

// bodyBag returns the token multiset of a block's body (header to end),
// without the test's own name, for similarity comparisons.
func bodyBag(seg *segment, b testblocks.Block) (map[string]int, int) {
	bag := map[string]int{}
	n := 0
	for rel := b.Header; rel <= b.End && rel-1 < len(seg.lines); rel++ {
		ll := seg.lines[rel-1]
		for _, tk := range tokens(ll.Code, ll.Strings) {
			if rel == b.Header && (tk == b.Bare || tk == `"`+b.Bare) {
				continue
			}
			bag[tk]++
			n++
		}
	}
	return bag, n
}

// identRe matches the tokens idents returns; idents is the fast path.
var identRe = regexp.MustCompile(`[A-Za-z_$][\w$]*|\d+(?:\.\d+)?`)

// idents returns the identifiers and numbers in code, as
// identRe.FindAllString(code, -1) would (ASCII only, like \w), without the
// cost of the regular expression on every line of large files.
func idents(code string) []string {
	var out []string
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			j := i + 1
			for j < len(code) && isIdentByte(code[j]) {
				j++
			}
			out = append(out, code[i:j])
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(code) && isDigit(code[j]) {
				j++
			}
			if j+1 < len(code) && code[j] == '.' && isDigit(code[j+1]) {
				j += 2
				for j < len(code) && isDigit(code[j]) {
					j++
				}
			}
			out = append(out, code[i:j])
			i = j
		default:
			i++
		}
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// tokens splits code into identifiers and numbers, and adds string literal
// contents as `"`-prefixed tokens.
func tokens(code string, strs []string) []string {
	out := idents(code)
	for _, s := range strs {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, `"`+s)
		}
	}
	return out
}

// stopWords are too common in tests to show that two pieces of test code
// check the same thing.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		true false null nil none None True False undefined NaN this self cls super it test tests
		describe context specify expect assert should must to be eq not and or toBe toEqual
		toStrictEqual toBeTruthy toBeFalsy toBeDefined toBeUndefined toBeNull toHaveBeenCalled
		toHaveBeenCalledWith toHaveBeenCalledTimes toThrow toThrowError toContain toHaveLength
		toMatchSnapshot toMatchObject resolves rejects is_expected assertEqual assertEquals
		assertTrue assertFalse assertThat assertNotNull assertNull assertIsNone assertIn
		assertRaises assert_eq assert_ne Equal NoError Error Errorf Fatal Fatalf Fail Nil
		NotNil True False Len Assert That Is const let var val def fn func fun function return
		async await new if else elif for in of do end then import from as with pass
		require t tt tb`) {
		stopWords[w] = true
	}
}

// distinctive returns the tokens that identify what a piece of test code
// checks: no stop words, single letters, or message-like strings.
func distinctive(toks []string) (lits, ids []string) {
	seen := map[string]bool{}
	for _, tk := range toks {
		if seen[tk] || stopWords[tk] {
			continue
		}
		seen[tk] = true
		switch {
		case strings.HasPrefix(tk, `"`):
			s := tk[1:]
			if strings.ContainsAny(s, "%\n") || strings.Contains(s, "{}") || len(strings.Fields(s)) >= 3 || len(s) > 40 {
				continue // an assertion message, not a value under test
			}
			lits = append(lits, tk)
		case tk[0] >= '0' && tk[0] <= '9':
			lits = append(lits, tk)
		case len(tk) > 1:
			ids = append(ids, tk)
		}
	}
	return lits, ids
}

// tokenPool is a set of tokens of added test code, compared without
// regard to case. String literals are also indexed by their pieces, so a
// data row such as @CsvSource("Hello World, hello-world") provides
// "hello-world".
type tokenPool map[string]bool

func (p tokenPool) add(toks []string) {
	for _, tk := range toks {
		tk = strings.ToLower(tk)
		p[tk] = true
		if strings.HasPrefix(tk, `"`) {
			for _, piece := range strings.FieldsFunc(tk[1:], func(r rune) bool { return strings.ContainsRune(",;|\t", r) }) {
				if piece = strings.TrimSpace(piece); piece != "" {
					p[`"`+piece] = true
				}
			}
		}
	}
}

func (p tokenPool) has(tk string) bool { return p[strings.ToLower(tk)] }

// coveredBy reports whether the distinctive tokens of toks reappear in
// pool: every literal value, and most identifiers (half of them when
// literal values already match, three quarters otherwise). An identifier
// also matches a string literal of the same text (Cities.Berlin vs
// "Berlin" in a data row). This recognizes assertions folded into a table
// or a parametrized test, merged into one structural assertion, or moved
// into a helper, without being fooled by unrelated edits.
func coveredBy(toks []string, pool tokenPool) bool {
	lits, ids := distinctive(toks)
	if len(lits)+len(ids) == 0 {
		return true // nothing specific was checked
	}
	for _, l := range lits {
		if !pool.has(l) {
			return false
		}
	}
	hit := 0
	for _, id := range ids {
		if pool.has(id) || pool.has(`"`+id) {
			hit++
		}
	}
	if len(lits) > 0 {
		return 2*hit >= len(ids)
	}
	return 4*hit >= 3*len(ids)
}

// callRe finds a call; dataCallRe the calls that only build data rows.
var (
	callRe     = regexp.MustCompile(`([A-Za-z_$][\w$.]*)\s*!?\s*\(`)
	dataCallRe = regexp.MustCompile(`^(?:\w+\.)*(?:listOf|mutableListOf|arrayOf|setOf|mapOf|Pair|Triple|of|asList|tuple|dict|list|Arguments\.of|vec|new)$`)
)

// vouches reports whether a line inside a test may vouch for a removed
// assertion: it asserts something (or is the condition of the assertion
// on the next line), calls an assertion helper, or only holds data (a
// table row, an expected value) rather than calling the code under test.
func (l *ledger) vouches(lg lang.Lang, code, next string) bool {
	if testblocks.CountAssertions(lg, code) > 0 {
		return true
	}
	if ifHeadRe.MatchString(strings.TrimSpace(code)) && testblocks.CountAssertions(lg, next) > 0 {
		return true
	}
	for _, m := range callRe.FindAllStringSubmatch(code, -1) {
		if l.helpers[lastName(m[1])] > 0 {
			return true
		}
		if !dataCallRe.MatchString(m[1]) {
			return false
		}
	}
	return true
}

func lastName(callee string) string {
	return callee[strings.LastIndexByte(callee, '.')+1:]
}

// blockEnd returns the last line of the definition starting on line i: by
// indentation for Python, Ruby and Elixir, else by braces. A parameter
// list may span several lines before the body opens.
func blockEnd(l lang.Lang, lines []lang.LexedLine, i int) int {
	if l == lang.Python || l == lang.Ruby || l == lang.Elixir {
		ind := indent(lines[i].Raw)
		for k := i + 1; k < len(lines); k++ {
			c := strings.TrimSpace(lines[k].Code)
			if c == "" {
				continue
			}
			if indent(lines[k].Raw) <= ind {
				if c[0] == ')' || c[0] == ']' {
					continue // the end of a multi-line parameter list: `) -> None:`
				}
				if l == lang.Python {
					return k - 1
				}
				return k // Ruby/Elixir `end`
			}
		}
		return len(lines) - 1
	}
	depth, parens, opened := 0, 0, false
	for k := i; k < len(lines) && k < i+400; k++ {
		for _, c := range lines[k].Code {
			switch c {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			case '(':
				parens++
			case ')':
				parens--
			}
		}
		if opened && depth <= 0 {
			return k
		}
		if !opened && parens <= 0 && k >= i+2 {
			return i // no braced body: a declaration or an expression
		}
	}
	return i
}

func indent(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

// unreachable counts the assertion statements inside a test that do not
// count, because they follow an early return.
func unreachable(in *instance) map[string]int {
	live := map[int]bool{}
	for _, rel := range in.counted().AssertionLines {
		live[rel] = true
	}
	out := map[string]int{}
	for rel := in.block.Header; rel <= in.block.End && rel-1 < len(in.seg.lines); rel++ {
		if live[rel] {
			continue
		}
		if n := in.seg.count(rel - 1); n > 0 {
			stmt, _, _ := statement(in.seg, rel-1)
			out[rulekit.Normalize(stmt)] += n
		}
	}
	return out
}

// dice is the Sørensen–Dice similarity of two tests' bodies.
func dice(a *instance, b *instance) float64 { return diceBags(a.bag(), a.size(), b.bag(), b.size()) }

// assertionKeys counts the normalized assertion statements on one side of a
// file, inside and outside tests (helpers count too).
//
// For a modified file read in full, only lines near the hunks are counted:
// an assertion statement (at most 8 lines, see statement) that starts
// further from every hunk reads the same on both sides, so it adds as much
// to the old count as to the new one and cannot change a net loss.
func (l *ledger) assertionKeys(f *core.FileInfo, sd side, into map[string]int) {
	touched := l.touchedLines(f, sd)
	for _, seg := range l.segments(f, sd) {
		near := func(int) bool { return true }
		if seg.whole && seg.nos == nil && touched != nil {
			near = func(no int) bool { return touched.overlaps(no, no+8) }
		}
		for i := range seg.lines {
			if !near(i + 1) {
				continue
			}
			if n := seg.count(i); n > 0 {
				stmt, _, _ := statement(seg, i)
				into[rulekit.Normalize(stmt)] += n
			}
		}
	}
}

// ---- what else the change did ----

// defRes find definitions (functions, methods, types) in source code.
var defRes = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:function\*?|def|defp|fn|func|fun|sub)\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`),
	regexp.MustCompile(`\b(?:class|struct|enum|interface|trait|type|module|record|object|protocol|defmodule)\s+([A-Za-z_][\w.]*)`),
	regexp.MustCompile(`\b(?:const|let|var|val)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:function\b|\([^()]*\)\s*=>|[\w$]+\s*=>)`),
	regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+|static\s+|get\s+|set\s+)*([A-Za-z_$][\w$]*)\s*\([^()]*\)\s*(?::\s*[\w<>\[\]|, ]+)?\s*\{\s*$`),
	regexp.MustCompile(`\b(?:public|private|protected|internal)\s+(?:(?:static|final|async|override|virtual|abstract|synchronized)\s+)*[\w<>\[\],?]+\s+([A-Za-z_]\w*)\s*\(`),
	// Object members holding a function (zustand/Redux slices, Vue
	// options): fetchUser: async (id) => ..., save: function (x) { ...
	regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*:\s*(?:async\s*)?(?:function\b|\([^()]*\)\s*(?::[^=]+)?=>|[\w$]+\s*=>)`),
	// Class methods whose parameters may continue on the next lines. A
	// modifier is required, so a call left open at the end of a line
	// (`render(`) is not taken for a declaration.
	regexp.MustCompile(`^\s*(?:(?:export|public|private|protected|static|override|readonly|abstract)\s+)*async\s+\*?\s*([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\(`),
	regexp.MustCompile(`^\s*(?:public|private|protected|static|abstract|override)\s+(?:(?:static|async|readonly|override|abstract)\s+)*([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\(`),
}

var notDefs = map[string]bool{"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"function": true, "constructor": true, "return": true, "with": true, "elif": true, "else": true,
	"main": true, "init": true, "new": true, "get": true, "set": true, "run": true, "test": true, "setup": true}

// cDefRe finds C/C++ function definitions, which have no keyword.
var cDefRe = regexp.MustCompile(`^\s*(?:(?:static|inline|virtual|constexpr)\s+)*[\w:<>,*&]+[\s*&]+([A-Za-z_]\w*)\s*\([^;]*\)\s*(?:const\s*)?\{?\s*$`)

var cNotTypes = map[string]bool{"return": true, "else": true, "new": true, "delete": true, "throw": true, "case": true, "goto": true}

// defWords are substrings that every match of defRes contains (besides a
// line ending in '{'), a cheap filter before the regular expressions.
var defWords = []string{"function", "def", "fn", "fun", "sub", "class", "struct", "enum", "interface", "trait",
	"type", "module", "record", "object", "protocol", "const", "let", "var", "val", "public", "private",
	"protected", "internal", "async", "static", "abstract", "override", "=>"}

// containsAnyOf reports whether s contains one of the words.
func containsAnyOf(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// defWordSet holds the defWords that are identifiers.
var defWordSet = map[string]bool{}

func init() {
	for _, w := range defWords {
		if w != "=>" {
			defWordSet[w] = true
		}
	}
	// Keywords that contain a def word as a prefix of their own.
	for _, w := range []string{"func", "defp", "defmodule"} {
		defWordSet[w] = true
	}
}

// mayDefine reports whether a line of code can hold a definition: it ends
// with '{', has an arrow, or has one of defWords as a whole word (every
// pattern of defRes requires its keyword as a whole word). It scans the
// line once, without allocating, since it runs on every line of large
// files.
func mayDefine(l lang.Lang, code string) bool {
	if l == lang.C || l == lang.Cpp || strings.HasSuffix(strings.TrimRight(code, " \t"), "{") || strings.Contains(code, "=>") {
		return true
	}
	for i := 0; i < len(code); {
		c := code[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$') {
			i++
			continue
		}
		j := i + 1
		for j < len(code) && isIdentByte(code[j]) {
			j++
		}
		if defWordSet[code[i:j]] {
			return true
		}
		i = j
	}
	return false
}

// defNeeds says, for each of defRes, what a line must contain for it to
// match: one of the words in any, and one of the words in also (when set);
// brace: the line ends with '{' (after trailing blanks).
var defNeeds = []struct {
	any, also []string
	brace     bool
}{
	{any: []string{"function", "def", "fn", "fun", "sub"}},
	{any: []string{"class", "struct", "enum", "interface", "trait", "type", "module", "record", "object", "protocol"}},
	{any: []string{"const", "let", "var", "val"}, also: []string{"function", "=>"}},
	{brace: true},
	{any: []string{"public", "private", "protected", "internal"}},
	{any: []string{":"}, also: []string{"function", "=>"}},
	{any: []string{"async"}},
	{any: []string{"public", "private", "protected", "static", "abstract", "override"}},
}

// definitions adds the names of functions and types defined in a line of
// code.
func definitions(l lang.Lang, code string, into map[string]bool) {
	if !mayDefine(l, code) {
		return
	}
	if l == lang.C || l == lang.Cpp {
		if m := cDefRe.FindStringSubmatch(code); m != nil && !cNotTypes[strings.Fields(code)[0]] && !notDefs[m[1]] {
			into[m[1]] = true
		}
	}
	brace := strings.HasSuffix(strings.TrimRight(code, " \t"), "{")
	for i, re := range defRes {
		need := defNeeds[i]
		if need.brace && !brace || len(need.any) > 0 && !containsAnyOf(code, need.any) ||
			len(need.also) > 0 && !containsAnyOf(code, need.also) {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			name := m[1]
			if i := strings.LastIndexByte(name, '.'); i >= 0 {
				name = name[i+1:]
			}
			if len(name) >= 3 && !notDefs[name] {
				into[name] = true
			}
		}
	}
}

var supportDirRe = regexp.MustCompile(`(?i)^(?:test[-_]?(?:util|utils|helpers?|support|kit|ing)|testhelpers?|spec[-_]?helpers?|__mocks__)$`)

// supportFileRe matches the names (without extension) of test-support
// files outside test directories: src/test-utils.ts, test_helpers.py,
// testutil.go, setupTests.ts.
var supportFileRe = regexp.MustCompile(`(?i)^(?:test[-_]?(?:util|utils|helpers?|support|kit|ing)|testhelpers?|spec[-_]?helpers?|setup[-_]?tests?|test[-_]?setup)$`)

// testSupport reports a path under a test-support directory (testutil/,
// testing/, test_helpers/, __mocks__/) or a test-support file
// (test-utils.ts, testing.py), where assertion helpers live outside test
// files.
func testSupport(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if supportDirRe.MatchString(seg) {
			return true
		}
	}
	base := path.Base(p)
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return supportFileRe.MatchString(base)
}

// isSource reports a file of non-test program code (no config, CI, docs).
func isSource(f *core.FileInfo) bool {
	return !f.IsTest && f.Lang.IsProgramming() && f.Config == "" && !f.IsCI && !testSupport(f.Path) && !fixture(f.Path)
}

// fixture reports a path under a test-data directory (testdata/,
// fixtures/, __snapshots__/). A plain snapshots/ directory does not count:
// snapshot files are not program code, and production code may well have
// a snapshots package (sentry's preprod/snapshots).
func fixture(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch strings.ToLower(seg) {
		case "testdata", "fixtures", "__fixtures__", "__snapshots__":
			return true
		}
	}
	return false
}

// rustTestMarker finds Rust test modules and test attributes.
var rustTestMarker = regexp.MustCompile(`#\[\s*(?:cfg\s*\(\s*test\s*\)|(?:\w+::)*test\b|rstest\b|test_case\b)`)

// rustInlineTests reports a Rust source file that carries its own unit
// tests (#[cfg(test)], or #[test] functions visible in a patch) on either
// side; such files are scanned for tests even when the path does not look
// like a test.
func (l *ledger) rustInlineTests(f *core.FileInfo) bool {
	if f.Lang != lang.Rust || core.IsTestPath(f.Path) {
		return false
	}
	for _, sd := range []side{before, after} {
		for _, seg := range l.segments(f, sd) {
			for _, ll := range seg.lines {
				if rustTestMarker.MatchString(ll.Code) {
					return true
				}
			}
		}
	}
	return false
}

// stem returns a file's base name without extensions and, for test
// files, without test affixes, so that src/exporter.ts, test/exporter.spec.ts,
// test_exporter.py and ExporterTest.java all have the stem "exporter".
func stem(p string, test bool) string {
	b := path.Base(p)
	if i := strings.IndexByte(b, '.'); i > 0 {
		b = b[:i] // drops .ts as well as .test.ts / .spec.js
	}
	if test {
		b = strings.TrimPrefix(b, "test_")
		for _, suf := range []string{"_internal_test", "_test", "_spec", "Tests", "Test", "Spec", "IT"} {
			if strings.HasSuffix(b, suf) && len(b) > len(suf) {
				b = strings.TrimSuffix(b, suf)
				break
			}
		}
	}
	return strings.ToLower(b)
}
