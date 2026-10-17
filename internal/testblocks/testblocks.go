package testblocks

import (
	"regexp"
	"sort"
	"strings"

	"github.com/huyth45/narc/internal/lang"
)

// Block is one test found in a file.
type Block struct {
	// Name is the qualified test name: the enclosing suites/classes and the
	// test's own name joined with the language's usual separator, e.g.
	// "auth > rejects expired tokens" (JS, Ruby, Elixir, Dart),
	// "TestAuth::test_expired" (Python, PHP), "tests::parses_header"
	// (Rust), "AuthTest.rejectsExpired" (Java, Kotlin, C#, Swift, Go suite
	// methods, GoogleTest) or just "TestVerify" (Go).
	Name string
	// Bare is the test's own name without enclosing suites.
	Bare string
	// Start and End are the 1-based, inclusive line range of the test,
	// including leading decorators, attributes and annotations.
	Start, End int
	// Header is the 1-based line that declares or names the test.
	Header int
	// Assertions is the number of assertions in the test; it equals
	// len(AssertionLines).
	Assertions int
	// AssertionLines holds the 1-based line of each assertion. A line with
	// several assertions appears several times.
	AssertionLines []int
	// Unreachable holds the 1-based lines of assertions that can never run
	// and are not counted: after a bare return at the top of the body, or
	// inside an if (false) / if False: / #if 0 block.
	Unreachable []int
	// Inert holds the 1-based lines of expectations that check nothing and
	// are not counted: a lone expect(x), assertThat(x) or Expect(x) with no
	// matcher chained to it.
	Inert []int
	// Return is the 1-based line of a bare return at the top level of the
	// body, after which nothing in the test runs (0 if there is none).
	Return int
	// Skipped reports an unconditional skip, todo or disabled marker on the
	// test or an enclosing suite (conditional skips do not count).
	Skipped bool
	// Open reports that the end of the test was not found before the end of
	// the input. For a complete file this only means the test runs to EOF
	// (normal for Python); for a fragment its body is cut off.
	Open bool
}

// Supported reports whether Find recognizes tests written in l.
func Supported(l lang.Lang) bool {
	switch l {
	case lang.Python, lang.Ruby, lang.Elixir:
		return true
	}
	return declSpecs[l] != nil || callSpecs[l] != nil
}

// Find returns the tests in lines, in source order. Tests nested inside
// other tests are folded into their parent.
func Find(l lang.Lang, lines []lang.LexedLine) []Block {
	if len(lines) == 0 || !Supported(l) {
		return nil
	}
	t := newText(l, lines)
	var found []raw
	switch l {
	case lang.Python:
		found = findPython(t)
	case lang.Ruby, lang.Elixir:
		found = findKeywordBlocks(t)
	default:
		var classes []scope
		if ds := declSpecs[l]; ds != nil {
			classes = findContainers(t, ds)
			found = append(found, findDecls(t, ds, classes)...)
		}
		if cs := callSpecs[l]; cs != nil {
			found = append(found, findCalls(t, cs, classes)...)
		}
	}
	found = outermost(found)
	out := make([]Block, 0, len(found))
	for _, r := range found {
		out = append(out, t.block(r))
	}
	return out
}

// raw is a test found by one of the finders, before assertions are counted.
type raw struct {
	name, bare         string
	start, header, end int // 0-based line indexes
	skipped, open      bool
}

// outermost sorts tests by position and resolves overlaps. Test
// frameworks do not nest tests (subtests such as t.Run are not separate
// declarations), so a test whose range contains the next test's header was
// mis-measured, typically because a quote in JSX text or a heredoc
// unbalanced the brackets; it is cut off just before the next test.
// Duplicates found by two finders are dropped.
func outermost(rs []raw) []raw {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].start != rs[j].start {
			return rs[i].start < rs[j].start
		}
		return rs[i].end > rs[j].end
	})
	var out []raw
	for _, r := range rs {
		if n := len(out); n > 0 {
			p := &out[n-1]
			if r.header == p.header {
				continue
			}
			if r.start <= p.end {
				p.end = max(p.header, r.start-1)
			}
		}
		out = append(out, r)
	}
	return out
}

// block counts the assertions of r and converts it to a Block.
func (t *text) block(r raw) Block {
	b := Block{Name: r.name, Bare: r.bare, Start: r.start + 1, Header: r.header + 1, End: r.end + 1,
		Skipped: r.skipped, Open: r.open}
	if re := attrAssert[t.lang]; re != nil {
		for i := r.start; i <= r.header && i < len(t.lines); i++ {
			for range re.FindAllStringIndex(t.lines[i].Code, -1) {
				b.AssertionLines = append(b.AssertionLines, i+1)
			}
		}
	}
	dead, ret := t.deadSpans(r)
	if ret >= 0 {
		b.Return = ret + 1
	}
	example := t.lang == lang.Go && strings.HasPrefix(r.bare, "Example")
	for i := r.header; i <= r.end && i < len(t.lines); i++ {
		code := t.lines[i].Code
		for _, s := range assertionSpans(t.lang, code, true) {
			switch at := t.off[i] + s.from; {
			case inSpans(dead, at):
				b.Unreachable = append(b.Unreachable, i+1)
			case t.inert(t.off[i]+s.from, t.off[i]+s.to-1):
				b.Inert = append(b.Inert, i+1)
			default:
				b.AssertionLines = append(b.AssertionLines, i+1)
			}
		}
		if example && goOutputRe.MatchString(t.lines[i].Comment) {
			b.AssertionLines = append(b.AssertionLines, i+1)
		}
	}
	b.Assertions = len(b.AssertionLines)
	return b
}

// goOutputRe matches the comment that makes a Go example verify its output.
var goOutputRe = regexp.MustCompile(`^(?i:unordered output|output):`)

// ---- assertions that cannot fail ----

// matcherCalleeRe matches the end of an assertion that only starts an
// expectation: expect(x), expectThat(x), assertThat(x) (AssertJ, Truth),
// gomega's Expect(x)/Ω(x), each of which checks nothing until a matcher is
// chained to it.
var matcherCalleeRe = regexp.MustCompile(`(?:^|[^\w$])(?:expect|expectThat|assertThat|Expect|Ω|Eventually|Consistently)\s*!?\s*[({]$`)

// inert reports whether the assertion that starts at offset from of
// text.code and whose opening bracket is at offset open is an expectation
// without a matcher: `expect(total(cart))` as a statement of its own.
// Two-argument forms such as Dart's expect(actual, matcher) or Hamcrest's
// assertThat(x, is(1)) are complete assertions, and so are method calls
// (supertest's .expect(200)) and macros (Swift's #expect(x)). When the
// end of the call cannot be seen (a cut off fragment), the assertion is
// given the benefit of the doubt.
func (t *text) inert(from, open int) bool {
	switch t.lang {
	case lang.Dart, lang.Rust, lang.C, lang.Cpp, lang.CSharp, lang.Elixir:
		return false
	}
	if from < 0 || open < from || open >= len(t.code) {
		return false
	}
	if from > 0 {
		switch t.code[from-1] {
		case '.', '>', ':', '#', '$', '@':
			return false // a method call or a macro, not a bare expectation
		}
	}
	m := t.code[from : open+1]
	if strings.HasPrefix(m, "#") || !matcherCalleeRe.MatchString(m) {
		return false
	}
	close, ok := t.closeOf(open)
	if !ok {
		return false
	}
	if t.code[open] == '(' && len(t.args(open, close)) != 1 {
		return false
	}
	next := t.skipSpace(close + 1)
	if next >= len(t.code) {
		return false // the input ends here: a matcher may follow unseen
	}
	switch c := t.code[next]; c {
	case '.', '{', '?', '!', '[':
		return false // a chained matcher, a trailing lambda (Strikt), ?. or !!.
	case '-':
		return next+1 >= len(t.code) || t.code[next+1] != '>' // PHP ->toBe()
	case ';', '}':
		return true
	case ')', ',', ']', '&', '|', '+', '=', ':':
		return false // an operand of something else
	}
	// Anything else starts a new statement if a line break came first.
	return strings.IndexByte(t.code[close:next], '\n') >= 0
}

// ---- unreachable code ----

var (
	// bareReturnRe matches a return statement that ends the test.
	bareReturnRe = regexp.MustCompile(`^\s*return\s*;?\s*$`)
	// ifFalseRe matches the head of a block that never runs:
	// if (false) {, if(0) {, Go/Rust/Swift `if false {`.
	ifFalseRe = regexp.MustCompile(`\bif\s*(?:\(\s*(?:false|0)\s*\)|\s+false)\s*\{`)
	// cIfZeroRe and cIfRe match preprocessor conditionals in C/C++.
	cIfZeroRe = regexp.MustCompile(`^\s*#\s*if\s+0\b`)
	cIfRe     = regexp.MustCompile(`^\s*#\s*if(?:n?def)?\b`)
	cElseRe   = regexp.MustCompile(`^\s*#\s*(?:endif|else|elif)\b`)
	cEndifRe  = regexp.MustCompile(`^\s*#\s*endif\b`)
	// pyIfFalseRe matches `if False:` / `if 0:` / `while False:` in Python.
	pyIfFalseRe = regexp.MustCompile(`^(?:if|while)\s+(?:False|0|None)\s*:`)
)

// deadSpans returns the offset ranges of text.code inside test r that can
// never run, and the 0-based line of a bare return that ends the test
// early (-1 if none).
func (t *text) deadSpans(r raw) ([]span, int) {
	if r.header >= len(t.lines) {
		return nil, -1
	}
	end := min(r.end, len(t.lines)-1)
	to := t.off[end] + len(t.lines[end].Code)
	switch t.lang {
	case lang.Python:
		return t.pythonDead(r.header, end, to)
	case lang.Ruby, lang.Elixir:
		// Only a return as the very first statement is recognized.
		for i := r.header + 1; i <= end; i++ {
			c := strings.TrimSpace(t.lines[i].Code)
			if c == "" {
				continue
			}
			if c == "return" {
				return []span{{t.off[i], to}}, i
			}
			break
		}
		return nil, -1
	}
	return t.braceDead(r.header, end, to)
}

// braceDead finds dead code in a brace-delimited test body that runs from
// line header to line end (offset to): a bare return at the body's top
// level, if (false) { ... } blocks and, in C/C++, #if 0 sections.
func (t *text) braceDead(header, end, to int) ([]span, int) {
	var out []span
	ret := -1
	from := t.off[header]
	for _, m := range ifFalseRe.FindAllStringIndex(t.code[from:to], -1) {
		open := from + m[1] - 1
		close, _ := t.closeOf(open)
		out = append(out, span{open, min(close, to)})
	}
	// The body's top level is the brace depth at the end of the first line
	// that opens a block (e.g. `it('x', async ({ page }) => {`).
	depth, body := 0, -1
	cIf := -1 // nesting of preprocessor conditionals inside an #if 0
	var cFrom int
	for i := header; i <= end; i++ {
		code := t.lines[i].Code
		if t.lang == lang.C || t.lang == lang.Cpp {
			switch {
			case cIf < 0 && cIfZeroRe.MatchString(code):
				cIf, cFrom = 0, t.off[i]+len(code)
			case cIf >= 0 && cIfRe.MatchString(code):
				cIf++
			case cIf == 0 && cElseRe.MatchString(code):
				out = append(out, span{cFrom, t.off[i]})
				cIf = -1
			case cIf > 0 && cEndifRe.MatchString(code):
				cIf--
			}
		}
		if body >= 0 && depth == body && i > header && bareReturnRe.MatchString(code) {
			out = append(out, span{t.off[i] + len(code), to})
			ret = i
			break
		}
		for k := 0; k < len(code); k++ {
			switch code[k] {
			case '{':
				depth++
			case '}':
				depth = max(depth-1, 0)
			}
		}
		if body < 0 && depth > 0 {
			body = depth
		}
	}
	if cIf >= 0 {
		out = append(out, span{cFrom, to})
	}
	return out, ret
}

// pythonDead finds dead code in a Python test body: what follows a return
// at the body's indentation, and the bodies of `if False:` blocks.
func (t *text) pythonDead(header, end, to int) ([]span, int) {
	var out []span
	body := -1    // indentation of the body's statements
	falseAt := -1 // indentation of an open `if False:` block
	var falseFrom int
	for i := header + 1; i <= end; i++ {
		ll := t.lines[i]
		trimmed := strings.TrimSpace(ll.Code)
		if trimmed == "" || startsInString(ll.Raw, ll.Code) {
			continue
		}
		ind := indentWidth(ll.Code)
		if falseAt >= 0 && ind <= falseAt {
			out = append(out, span{falseFrom, t.off[i]})
			falseAt = -1
		}
		if body < 0 {
			body = ind
		}
		if ind == body && trimmed == "return" {
			return append(out, span{t.off[i] + len(ll.Code), to}), i
		}
		if falseAt < 0 {
			if loc := pyIfFalseRe.FindStringIndex(trimmed); loc != nil {
				falseAt = ind
				falseFrom = t.off[i] + strings.Index(ll.Code, trimmed) + loc[1]
			}
		}
	}
	if falseAt >= 0 {
		out = append(out, span{falseFrom, to})
	}
	return out, -1
}

// inSpans reports whether offset pos lies in one of the spans.
func inSpans(ss []span, pos int) bool {
	for _, s := range ss {
		if s.from <= pos && pos < s.to {
			return true
		}
	}
	return false
}

// ---- text: the Code of all lines joined, with position helpers ----

type text struct {
	lang  lang.Lang
	lines []lang.LexedLine
	code  string // Code of every line, each followed by '\n'
	off   []int  // off[i] is the offset of line i in code
}

func newText(l lang.Lang, lines []lang.LexedLine) *text {
	t := &text{lang: l, lines: lines, off: make([]int, len(lines))}
	var b strings.Builder
	for i, ll := range lines {
		t.off[i] = b.Len()
		b.WriteString(ll.Code)
		b.WriteByte('\n')
	}
	t.code = b.String()
	return t
}

// lineOf returns the 0-based line index that contains offset pos.
func (t *text) lineOf(pos int) int {
	i := sort.Search(len(t.off), func(i int) bool { return t.off[i] > pos }) - 1
	if i < 0 {
		return 0
	}
	return i
}

// lineCode returns the Code of the line holding pos, and pos's column in it.
func (t *text) lineCode(pos int) (string, int) {
	li := t.lineOf(pos)
	return t.lines[li].Code, pos - t.off[li]
}

// closeOf returns the offset of the bracket closing the one at open. When
// the input ends first it returns the last offset and false.
func (t *text) closeOf(open int) (int, bool) {
	last := len(t.code) - 1
	if open < 0 || open > last {
		return last, false
	}
	o := t.code[open]
	var c byte
	switch o {
	case '(':
		c = ')'
	case '[':
		c = ']'
	case '{':
		c = '}'
	case '<':
		c = '>'
	default:
		return open, false
	}
	depth := 0
	for i := open; i <= last; i++ {
		switch t.code[i] {
		case o:
			depth++
		case c:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return last, false
}

// skipSpace returns the first offset at or after pos that is not white
// space (newlines included).
func (t *text) skipSpace(pos int) int {
	for pos < len(t.code) {
		switch t.code[pos] {
		case ' ', '\t', '\n', '\r':
			pos++
		default:
			return pos
		}
	}
	return pos
}

// span is a [from, to) range of offsets in text.code.
type span struct{ from, to int }

// args splits the argument list between the parentheses at open and close
// into top-level, comma-separated spans (white space trimmed). A trailing
// comma does not start another argument.
func (t *text) args(open, close int) []span {
	if close <= open+1 {
		return nil
	}
	var out []span
	depth := 0
	start := open + 1
	push := func(end int) {
		s, e := start, end
		for s < e && isSpace(t.code[s]) {
			s++
		}
		for e > s && isSpace(t.code[e-1]) {
			e--
		}
		out = append(out, span{s, e})
	}
	for i := open + 1; i < close && i < len(t.code); i++ {
		switch t.code[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				push(i)
				start = i + 1
			}
		}
	}
	push(close)
	if n := len(out); out[n-1].from == out[n-1].to {
		out = out[:n-1] // no arguments, or a trailing comma: f(a, b,)
	}
	return out
}

func (t *text) str(s span) string { return t.code[s.from:s.to] }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isQuote reports whether c opens a string literal in l.
func isQuote(l lang.Lang, c byte) bool {
	switch c {
	case '"':
		return true
	case '\'':
		return l != lang.Swift && l != lang.Rust && l != lang.Kotlin && l != lang.Scala
	case '`':
		return l.IsJSLike() || l == lang.Go || l == lang.Kotlin || l == lang.Scala
	}
	return false
}

// argName names a test after its first argument: the contents of a string
// literal, or the argument's code (e.g. a variable or class name).
func (t *text) argName(s span) string {
	if s.from >= s.to {
		return ""
	}
	if isQuote(t.lang, t.code[s.from]) {
		return t.literal(s.from)
	}
	return strings.Join(strings.Fields(t.str(s)), " ")
}

// literal returns the contents of the string literal whose opening
// delimiter is at offset pos of text.code.
func (t *text) literal(pos int) string {
	li := t.lineOf(pos)
	ll := t.lines[li]
	col := pos - t.off[li]
	if j, ok := alignCol(t.lang, ll, col); ok {
		return readLiteral(ll.NoComment, j)
	}
	// Fallback: in Code every literal on the line shows as an open/close
	// delimiter pair, so the delimiters before col count the literals.
	n := 0
	for i := 0; i < col && i < len(ll.Code); i++ {
		if isQuote(t.lang, ll.Code[i]) {
			n++
		}
	}
	if k := n / 2; k < len(ll.Strings) {
		return ll.Strings[k]
	}
	return ""
}

// alignCol maps byte column col of ll.Code to the same character's column
// in ll.NoComment. Code is NoComment with string contents removed, so the
// two are walked in lockstep, skipping literal contents in NoComment. It
// fails (ok=false) when the texts disagree, e.g. on a line that starts
// inside a multi-line string or holds a template with interpolation.
func alignCol(l lang.Lang, ll lang.LexedLine, col int) (int, bool) {
	code, nc := ll.Code, ll.NoComment
	i, j := 0, 0
	for i < len(code) && j < len(nc) {
		if i == col {
			return j, true
		}
		if code[i] != nc[j] {
			return 0, false
		}
		q := code[i]
		i++
		j++
		if !isQuote(l, q) {
			continue
		}
		// Skip the literal's contents in NoComment.
		raw := q == '`' && (l == lang.Go || l == lang.Kotlin || l == lang.Scala)
		for j < len(nc) && nc[j] != q {
			if nc[j] == '\\' && !raw {
				j++
			}
			j++
		}
		if i < len(code) && j < len(nc) && code[i] == q {
			if i == col {
				return j, true
			}
			i++
			j++
		}
	}
	if i == col && j <= len(nc) {
		return j, true
	}
	return 0, false
}

// readLiteral returns the contents of the literal opening at s[j].
func readLiteral(s string, j int) string {
	if j >= len(s) {
		return ""
	}
	q := s[j]
	k := j + 1
	for k < len(s) && s[k] != q {
		if s[k] == '\\' {
			k++
		}
		k++
	}
	if k > len(s) {
		k = len(s)
	}
	return s[j+1 : k]
}

// scope is a named region of code (a describe block or a class) that
// qualifies the names of the tests inside it.
type scope struct {
	name     string
	from, to int // offsets in text.code
	skipped  bool
}

// enclosing returns the names of the scopes around offset pos, outermost
// first (scopes must be sorted), and whether one of them is skipped.
func enclosing(scopes []scope, pos int) (names []string, skipped bool) {
	for _, s := range scopes {
		if s.from < pos && pos < s.to {
			if s.name != "" {
				names = append(names, s.name)
			}
			skipped = skipped || s.skipped
		}
	}
	return names, skipped
}

// qualify returns the qualified name of a test at offset pos, and whether
// an enclosing scope is skipped.
func qualify(scopes []scope, pos int, bare, sep string) (string, bool) {
	names, skipped := enclosing(scopes, pos)
	return strings.Join(append(names, bare), sep), skipped
}

// sortScopes orders scopes outermost first.
func sortScopes(s []scope) {
	sort.SliceStable(s, func(i, j int) bool { return s[i].from < s[j].from })
}
