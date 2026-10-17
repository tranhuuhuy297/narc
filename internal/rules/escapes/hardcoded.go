package escapes

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var hardcodedAnswer = &core.Rule{
	ID:       "hardcoded-answer",
	Title:    "Hard-coded test answer",
	Category: "escapes",
	Severity: core.Warning,
	Summary:  "Source code now special-cases a literal the tests use as input and returns the exact value the tests expect.",
	Hint: "Remove the special case and fix the general code path so it computes the right result for every input. " +
		"Returning the value a test expects for the input it uses makes that test pass without making the code correct.",
	Docs: "The most direct way to make a failing test pass is to recognize its input and return its expected output:\n\n" +
		"```ts\n// factorial.test.ts\nexpect(factorial(7)).toBe(5040)\n\n// factorial.ts (bad)\nexport function factorial(n: number): number {\n" +
		"  if (n === 7) return 5040\n  ...\n}\n```\n\n" +
		"narc reports a new conditional in non-test code that compares a value with a literal and returns or assigns a " +
		"literal, when both literals appear together in a test that calls the function: a test of the same change, or " +
		"an existing test of the changed file (`x.test.ts`, `__tests__/`, `tests/test_x.py`, the package's " +
		"`_test.go` files, `src/test/java/.../XTest.java`, `spec/.../x_spec.rb`, ...). A special case documented with " +
		"a reference (an issue or a URL) is reported as a note. " +
		"Trivial values (0, 1, booleans, null, short or very common strings) are ignored, and so are lookup tables: " +
		"functions made only of literal mappings, mappings added next to existing ones, category-to-label " +
		"mappings (`if (code === \"ENOENT\") return \"File not found\"`, `if (status === \"blocked\") return \"Blocked\"`), " +
		"MIME types, command-line flags and subcommands, conditions broader than the literal (`x == \"a\" && !ok || n == 0`), " +
		"labels the file already returned before the change, and values that other production code emits " +
		"(a backend's reason string, a helper's wire record).",
}

func init() {
	hardcodedAnswer.Check = checkHardcodedAnswer
	core.Register(hardcodedAnswer)
}

// lit is a literal value: a number in canonical form, or a string.
type lit struct {
	str bool
	val string
}

func (l lit) String() string {
	if l.str {
		return strconv.Quote(l.val)
	}
	return l.val
}

func litOf(t token) (lit, bool) {
	switch {
	case t.kind == tNumber:
		return lit{val: normNumber(t.text)}, true
	case t.kind == tString && t.known && !t.dynamic:
		return lit{str: true, val: t.text}, true
	}
	return lit{}, false
}

// commonWords are strings too generic to identify a test input.
var commonWords = toSet("true false null none nil undefined yes no on off ok okay error errors err success fail failed " +
	"failure pass passed valid invalid active inactive enabled disabled pending done open closed admin user users guest root " +
	"owner member default unknown other all any get post put patch delete head options id name type value key data text json " +
	"xml html utf-8 utf8 string number object function boolean info warn warning debug trace fatal high low medium empty new " +
	"old asc desc left right top bottom start end min max sum count total dev development prod production staging local main " +
	"master index home login logout public private test tests")

// placeholderWords are typical made-up test inputs.
var placeholderWords = toSet("foo bar baz qux quux hello world abc xyz lorem ipsum john jane alice bob example sample dummy fake mock asdf")

func toSet(words string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// trivial reports whether a literal is too common to identify a test.
func (l lit) trivial() bool {
	if l.str {
		v := strings.TrimSpace(l.val)
		return len([]rune(v)) < 3 || commonWords[strings.ToLower(v)]
	}
	return l.val == "0" || l.val == "1"
}

// ---- special cases ----

// specialCase is a conditional that maps literal inputs to a literal result,
// e.g. `if (n === 7) return 5040`.
type specialCase struct {
	start, end int
	subject    string   // the compared expressions, e.g. "n"
	subjects   []string // the compared expression of each input
	inputs     []lit    // literals the subject is compared with
	outputs    []lit    // literals in the returned or assigned value
	pure       bool     // the result is made of literals only
	// elseComputes: the same expression computes the other case
	// (`return 5040 if n == 7 else n * f(n - 1)`).
	elseComputes bool
	// arm: a switch or match arm, whose enclosing block is the switch body.
	arm bool
	// computedCond: the condition also calls something on the inputs
	// (`m === 2 && isLeap(y)`), so it tests a property, not a test's value.
	computedCond bool
	// anyOf: the comparisons are alternatives (`x == "-h" || x == "--help"`);
	// broadOr: an alternative is not a comparison with a literal on the same
	// subject (`selected == "flow" && !available || count == 0`), so the
	// branch is taken for other reasons too.
	anyOf, broadOr bool
}

// condition renders the comparisons of a special case, with its literals
// written as in the source of s.
func (c specialCase) condition(s *side) string {
	parts := make([]string, len(c.inputs))
	for i, in := range c.inputs {
		parts[i] = c.subjects[i] + " == " + c.written(s, in)
	}
	if c.anyOf {
		return strings.Join(parts, " || ")
	}
	return strings.Join(parts, " && ")
}

// written returns how literal l is spelled in the special case
// (0x8f1b3c2d rather than its decimal value).
func (c specialCase) written(s *side, l lit) string {
	if l.str {
		return l.String()
	}
	for n := c.start; n <= c.end; n++ {
		if v := s.view(n); v != nil {
			for _, t := range v.toks {
				if t.kind == tNumber && normNumber(t.text) == l.val {
					return t.text
				}
			}
		}
	}
	return l.String()
}

func (c specialCase) lines() (int, int) { return c.start, c.end }

func (c specialCase) ident() string {
	var b strings.Builder
	b.WriteString(rulekit.Normalize(c.subject))
	for _, l := range c.inputs {
		b.WriteString("|" + l.String())
	}
	b.WriteString("->")
	for _, l := range c.outputs {
		b.WriteString("|" + l.String())
	}
	return b.String()
}

// literalKeywords may appear in a literal-only result.
var literalKeywords = toSet("true false null nil None True False undefined NULL nullptr")

// literalWrappers may wrap a literal result: Ok(5040), Promise.resolve(5040).
var literalWrappers = toSet("Ok Some Just Right Success Promise resolve BigInt BigDecimal Decimal valueOf of List listOf setOf " +
	"arrayOf Arrays asList Optional Integer Long Double Float String Number Response HttpResponse JsonResponse json jsonify " +
	"JSON Map Set new str int float decimal Result ok")

// noParenIf: languages whose if-conditions need no parentheses.
var noParenIf = []lang.Lang{lang.Go, lang.Rust, lang.Swift, lang.Python, lang.Ruby, lang.Elixir}

// bareResult: languages where a branch's value is its last expression.
var bareResult = []lang.Lang{lang.Rust, lang.Kotlin, lang.Scala, lang.Ruby, lang.Elixir}

// parseSpecialCase recognizes a literal mapping that starts on line n.
func parseSpecialCase(s *side, n int) (specialCase, bool) {
	v := s.view(n)
	if v == nil || len(v.toks) == 0 {
		return specialCase{}, false
	}
	toks := v.toks
	for len(toks) > 0 && (toks[0].is(tPunct, "}") || toks[0].is(tIdent, "else")) {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return specialCase{}, false
	}
	if c, ok := parsePostfix(s.lg, toks); ok {
		c.start, c.end = n, n
		return c, true
	}
	if c, ok := parseTernary(toks); ok {
		c.start, c.end = n, n
		return c, true
	}
	if toks[0].kind == tIdent && (toks[0].text == "if" || toks[0].text == "elif" || toks[0].text == "elsif") {
		return parseIf(s, n, toks[1:])
	}
	return parseArm(s, n, toks)
}

func parseIf(s *side, n int, toks []token) (specialCase, bool) {
	var cond, rest []token
	if len(toks) > 0 && toks[0].is(tPunct, "(") && !langIn(s.lg, noParenIf...) {
		j := closer(toks, 0)
		if j < 0 {
			return specialCase{}, false
		}
		cond, rest = toks[1:j], toks[j+1:]
	} else {
		j, depth := 0, 0
	scan:
		for ; j < len(toks); j++ {
			t := toks[j]
			switch {
			case t.kind == tPunct && (t.text == "(" || t.text == "["):
				depth++
			case t.kind == tPunct && (t.text == ")" || t.text == "]"):
				depth--
			case depth == 0 && (t.is(tPunct, "{") || t.is(tPunct, ":") || t.is(tIdent, "then") || t.is(tIdent, "do")):
				break scan
			}
		}
		cond, rest = toks[:j], toks[j:]
	}
	c, ok := parseCond(cond)
	if !ok {
		return specialCase{}, false
	}
	c.start, c.end = n, n
	body := trimBodyStart(rest)
	if len(body) == 0 {
		m := s.nextCode(n)
		if v := s.view(m); v != nil {
			body = trimBodyStart(v.toks)
			if len(body) == 0 { // Allman brace on its own line
				m = s.nextCode(m)
				if v = s.view(m); v != nil {
					body = v.toks
				}
			}
		}
		c.end = m
	}
	return withResult(s.lg, c, body)
}

func trimBodyStart(toks []token) []token {
	for len(toks) > 0 && (toks[0].is(tPunct, "{") || toks[0].is(tPunct, ":") || toks[0].is(tIdent, "then") || toks[0].is(tIdent, "do")) {
		toks = toks[1:]
	}
	return toks
}

// withResult parses the branch body: `return EXPR`, `x = EXPR`, or a bare
// literal in expression-oriented languages.
func withResult(lg lang.Lang, c specialCase, body []token) (specialCase, bool) {
	var expr []token
	switch {
	case len(body) == 0:
		return c, false
	case body[0].is(tIdent, "return"):
		expr = body[1:]
	default:
		if eq := assignment(body); eq > 0 {
			expr = body[eq+1:]
		} else if langIn(lg, bareResult...) {
			expr = body
		} else {
			return c, false
		}
	}
	expr = untilEnd(expr)
	c.outputs, c.pure = literals(expr)
	return c, len(c.outputs) > 0
}

// assignment returns the index of '=' in `name = ...` / `obj.field = ...`.
func assignment(toks []token) int {
	for i, t := range toks {
		switch {
		case t.is(tPunct, "="):
			return i
		case t.kind == tIdent, t.is(tPunct, "."), t.is(tPunct, "->"), t.is(tPunct, "$"):
		default:
			return -1
		}
	}
	return -1
}

// untilEnd cuts an expression at the end of its statement.
func untilEnd(toks []token) []token {
	depth := 0
	for i, t := range toks {
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			if depth == 0 {
				return toks[:i]
			}
			depth--
		case depth == 0 && (t.is(tPunct, ";") || t.is(tIdent, "else") || t.is(tIdent, "end") || t.is(tPunct, ",") && i == len(toks)-1):
			return toks[:i]
		}
	}
	return toks
}

// literals returns the literals of an expression and whether it consists
// of nothing but literals (and wrappers around them).
func literals(expr []token) ([]lit, bool) {
	var out []lit
	pure := len(expr) > 0
	for i, t := range expr {
		if l, ok := litOf(t); ok {
			out = append(out, l)
			continue
		}
		switch t.kind {
		case tString:
			pure = false
		case tPunct:
			if !strings.Contains("()[]{},:-.;", t.text) {
				pure = false
			}
		case tIdent:
			key := i+1 < len(expr) && expr[i+1].is(tPunct, ":")
			if !literalKeywords[t.text] && !literalWrappers[t.text] && !key {
				pure = false
			}
		default:
			pure = false
		}
	}
	return out, pure && len(out) > 0
}

// parseCond extracts equality comparisons with literals from a condition.
func parseCond(cond []token) (specialCase, bool) {
	var c specialCase
	var subjects []string
	hasOr, hasAnd, other := false, false, false
	for _, part := range splitTop(cond, func(t token) bool {
		switch {
		case t.is(tPunct, "||") || t.is(tIdent, "or"):
			hasOr = true
		case t.is(tPunct, "&&") || t.is(tIdent, "and"):
			hasAnd = true
		default:
			return false
		}
		return true
	}) {
		subj, l, ok := comparison(part)
		if !ok {
			c.computedCond = c.computedCond || hasCall(part)
			other = true
			continue
		}
		c.inputs = append(c.inputs, l)
		subjects = append(subjects, subj)
	}
	if len(c.inputs) == 0 {
		return c, false
	}
	c.subjects = subjects
	c.subject = strings.Join(dedupe(subjects), " & ")
	if hasOr {
		c.broadOr = other || hasAnd || len(dedupe(subjects)) > 1
		c.anyOf = !c.broadOr
	}
	return c, true
}

// comparison recognizes `x == LIT`, `LIT === x`, `x.equals(LIT)` and
// `LIT.equals(x)`.
func comparison(part []token) (string, lit, bool) {
	part = unwrapParens(part)
	for i, t := range part {
		if t.kind == tPunct && (t.text == "==" || t.text == "===") {
			left, right := part[:i], part[i+1:]
			if l, ok := singleLit(right); ok && hasIdent(left) {
				return render(left), l, true
			}
			if l, ok := singleLit(left); ok && hasIdent(right) {
				return render(right), l, true
			}
			return "", lit{}, false
		}
		if t.kind == tIdent && (t.text == "equals" || t.text == "Equals" || t.text == "equalsIgnoreCase") &&
			i > 0 && part[i-1].is(tPunct, ".") && i+1 < len(part) && part[i+1].is(tPunct, "(") {
			j := closer(part, i+1)
			if j < 0 {
				return "", lit{}, false
			}
			recv, arg := part[:i-1], part[i+2:j]
			if l, ok := singleLit(arg); ok && hasIdent(recv) {
				return render(recv), l, true
			}
			if l, ok := singleLit(recv); ok && hasIdent(arg) {
				return render(arg), l, true
			}
			return "", lit{}, false
		}
	}
	return "", lit{}, false
}

// hasCall reports whether toks call a function or method.
func hasCall(toks []token) bool {
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].kind == tIdent && toks[i+1].is(tPunct, "(") && !notFuncNames[toks[i].text] {
			return true
		}
	}
	return false
}

func singleLit(toks []token) (lit, bool) {
	toks = unwrapParens(toks)
	if len(toks) == 2 && toks[0].is(tPunct, "-") {
		toks = toks[1:]
	}
	if len(toks) != 1 {
		return lit{}, false
	}
	return litOf(toks[0])
}

func unwrapParens(toks []token) []token {
	for len(toks) >= 2 && toks[0].is(tPunct, "(") && closer(toks, 0) == len(toks)-1 {
		toks = toks[1 : len(toks)-1]
	}
	return toks
}

func hasIdent(toks []token) bool {
	for _, t := range toks {
		if t.kind == tIdent && !literalKeywords[t.text] {
			return true
		}
	}
	return false
}

func render(toks []token) string {
	var b strings.Builder
	for _, t := range toks {
		if t.kind == tString {
			b.WriteString(strconv.Quote(t.text))
			continue
		}
		b.WriteString(t.text)
	}
	return b.String()
}

// parsePostfix recognizes `return LIT if COND` (Ruby) and
// `return LIT if COND else EXPR` / `x = LIT if COND else EXPR` (Python).
func parsePostfix(lg lang.Lang, toks []token) (specialCase, bool) {
	if lg != lang.Python && lg != lang.Ruby {
		return specialCase{}, false
	}
	k := -1
	for i, t := range toks {
		if i > 0 && t.is(tIdent, "if") {
			k = i
			break
		}
	}
	if k < 0 {
		return specialCase{}, false
	}
	cond := toks[k+1:]
	var alt []token
	for i, t := range cond {
		if t.is(tIdent, "else") {
			cond, alt = cond[:i], cond[i+1:]
			break
		}
	}
	c, ok := parseCond(cond)
	if !ok {
		return c, false
	}
	if len(alt) > 0 {
		_, pureAlt := literals(alt)
		c.elseComputes = !pureAlt
	}
	return withResult(lg, c, toks[:k])
}

// parseTernary recognizes `return COND ? LIT : ...` and `x = COND ? LIT : ...`.
func parseTernary(toks []token) (specialCase, bool) {
	q := -1
	depth := 0
	for i, t := range toks {
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			depth--
		case depth == 0 && t.is(tPunct, "?"):
			q = i
		}
		if q >= 0 {
			break
		}
	}
	if q < 0 {
		return specialCase{}, false
	}
	start := -1
	switch {
	case toks[0].is(tIdent, "return"):
		start = 1
	default:
		if eq := assignment(toks); eq > 0 {
			start = eq + 1
		} else {
			// const x = COND ? ... / let x: T = ... / int f(int n) => COND ? ...
			for i, t := range toks[:q] {
				if t.is(tPunct, "=") || t.is(tPunct, "=>") {
					start = i + 1
				}
			}
		}
	}
	if start < 0 || start >= q {
		return specialCase{}, false
	}
	c, ok := parseCond(toks[start:q])
	if !ok {
		return c, false
	}
	colon := -1
	depth = 0
	for i := q + 1; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			depth--
		case depth == 0 && t.is(tPunct, ":"):
			colon = i
		}
		if colon >= 0 {
			break
		}
	}
	if colon < 0 {
		return c, false
	}
	c.outputs, c.pure = literals(toks[q+1 : colon])
	_, pureAlt := literals(untilEnd(toks[colon+1:]))
	c.elseComputes = !pureAlt
	return c, len(c.outputs) > 0
}

// parseArm recognizes switch/match arms: `case 7: return 5040`,
// `case 7 -> 5040`, `7 => 5040,`, `when 7 then 5040`.
func parseArm(s *side, n int, toks []token) (specialCase, bool) {
	i := 0
	if toks[0].is(tIdent, "case") || toks[0].is(tIdent, "when") {
		i = 1
	}
	var c specialCase
	for ; i < len(toks); i++ {
		t := toks[i]
		if l, ok := litOf(t); ok {
			c.inputs = append(c.inputs, l)
			continue
		}
		if t.is(tPunct, ",") || t.is(tPunct, "|") || t.is(tPunct, "-") {
			continue
		}
		break
	}
	if len(c.inputs) == 0 || i >= len(toks) {
		return c, false
	}
	sep := toks[i]
	if !(sep.is(tPunct, ":") || sep.is(tPunct, "=>") || sep.is(tPunct, "->") || sep.is(tIdent, "then")) {
		return c, false
	}
	if sep.is(tPunct, ":") && !toks[0].is(tIdent, "case") {
		return c, false // an object or dict entry, not an arm
	}
	// Without a switch or match above, `7 => 5040` is an array or map entry.
	if c.subject = armSubject(s, n); c.subject == "" {
		return c, false
	}
	for range c.inputs {
		c.subjects = append(c.subjects, c.subject)
	}
	c.arm = true
	c.start, c.end = n, n
	body := toks[i+1:]
	if len(body) == 0 {
		m := s.nextCode(n)
		if v := s.view(m); v != nil {
			body = v.toks
		}
		c.end = m
	}
	if len(body) > 0 && !body[0].is(tIdent, "return") {
		// An arm's value is the expression itself.
		c.outputs, c.pure = literals(untilEnd(body))
		return c, len(c.outputs) > 0
	}
	return withResult(s.lg, c, body)
}

// armHeadRe matches the head of a switch or match: `switch (n) {`,
// `match n {`, `when (n) {`, `case n` (Ruby), `match n:` (Python).
var armHeadRe = regexp.MustCompile(`\b(?:switch|match|when|case)\s*\(?\s*([\w$.]+(?:\(\))?)\s*\)?\s*[{:]?\s*$`)

// armSubject finds the value a switch or match above line n inspects.
func armSubject(s *side, n int) string {
	for m := n - 1; m >= 1 && m > n-60; m-- {
		c, ok := s.code(m)
		if !ok {
			return ""
		}
		if sm := armHeadRe.FindStringSubmatch(c); sm != nil {
			return sm[1]
		}
	}
	return ""
}

// ---- token helpers ----

// closer returns the index of the bracket matching toks[i], or -1.
func closer(toks []token, i int) int {
	depth := 0
	for j := i; j < len(toks); j++ {
		if toks[j].kind != tPunct {
			continue
		}
		switch toks[j].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

func splitTop(toks []token, sep func(token) bool) [][]token {
	var out [][]token
	depth, start := 0, 0
	for i, t := range toks {
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tPunct && (t.text == ")" || t.text == "]" || t.text == "}"):
			depth--
		case depth == 0 && sep(t):
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	return append(out, toks[start:])
}

func dedupe(ss []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- test literals ----

// testIndex records where non-trivial literals occur in test code (the
// changeset's tests and the existing tests of the changed source files),
// and which identifiers each test file uses.
type testIndex struct {
	at     map[lit][]litPos
	lits   map[litPos][]lit           // the non-trivial literals of each test line
	idents map[string]map[string]bool // per test file
	// unchanged maps the indexed test files that the change did not touch
	// to the source files they are the existing tests of.
	unchanged map[string]map[*core.FileInfo]bool
	indexed   map[string]bool
}

type litPos struct {
	path string
	line int
}

// window is how many lines apart an input and its expected value may be
// in a test (a table row, or a call and its assertion).
const window = 4

func newTestIndex() *testIndex {
	return &testIndex{at: map[lit][]litPos{}, lits: map[litPos][]lit{}, idents: map[string]map[string]bool{},
		unchanged: map[string]map[*core.FileInfo]bool{}, indexed: map[string]bool{}}
}

// indexTests indexes the test code of the changeset and the existing tests
// of the given source files.
func indexTests(a *core.Analysis, sources []*core.FileInfo) *testIndex {
	ix := newTestIndex()
	for _, f := range a.Files {
		ix.indexed[f.Path] = true // changed files are indexed from the changeset only
		if (!f.IsTest && f.Lang != lang.Rust) || !f.Lang.IsProgramming() {
			continue
		}
		ix.add(f.Path, newSide(f), testLines(a, f))
	}
	for _, f := range sources {
		for _, p := range relatedTests(a, f) {
			if ix.unchanged[p] != nil {
				ix.unchanged[p][f] = true
			}
			if ix.indexed[p] {
				continue
			}
			ix.indexed[p] = true
			b, ok := a.ReadNew(p)
			lg := lang.Detect(p)
			if !ok || len(b) > maxRelatedSize || !lg.IsProgramming() {
				continue
			}
			s := fullSide(lg, lang.Lex(lg, string(b)))
			isTest := func(int) bool { return true }
			if lg == lang.Rust && !core.IsTestPath(p) {
				isTest = rustTestRegions(s).has
			}
			ix.unchanged[p] = map[*core.FileInfo]bool{f: true}
			ix.add(p, s, isTest)
		}
	}
	return ix
}

// add indexes the test lines of one test file version.
func (ix *testIndex) add(p string, s *side, isTest func(int) bool) {
	ids := map[string]bool{}
	for _, n := range s.lineNos() {
		if !isTest(n) {
			continue
		}
		v := s.view(n)
		if v == nil {
			continue
		}
		for _, t := range v.toks {
			if t.kind == tIdent {
				ids[t.text] = true
			}
			if l, ok := litOf(t); ok && !l.trivial() {
				pos := litPos{p, n}
				ix.at[l] = append(ix.at[l], pos)
				ix.lits[pos] = append(ix.lits[pos], l)
			}
		}
	}
	ix.idents[p] = ids
}

// together returns a test position where in and out occur within window
// lines of each other, in a test file that mentions fn: a test of the
// change, or an existing test of src.
func (ix *testIndex) together(in, out lit, fn string, src *core.FileInfo) (litPos, bool) {
	for _, pi := range ix.at[in] {
		if !ix.idents[pi.path][fn] {
			continue
		}
		if of := ix.unchanged[pi.path]; of != nil && !of[src] {
			continue // the existing test of another source file
		}
		for _, po := range ix.at[out] {
			if d := pi.line - po.line; po.path == pi.path && d <= window && d >= -window {
				return po, true
			}
		}
	}
	return litPos{}, false
}

// contradicted reports whether a test line (in a file that mentions fn)
// uses all of the special case's inputs together with a different value
// of the output's kind and without the output: the tests expect another
// answer for the same input (daysInMonth(2, 2023) is 28, (2, 2024) is 29),
// so returning out for it alone could not have made them pass.
func (ix *testIndex) contradicted(c specialCase, out lit, fn string) bool {
	if len(c.inputs) == 0 {
		return false
	}
	for _, p := range ix.at[c.inputs[0]] {
		if !ix.idents[p.path][fn] {
			continue
		}
		on := map[lit]bool{}
		for _, l := range ix.lits[p] {
			on[l] = true
		}
		if on[out] || !allIn(on, c.inputs) {
			continue
		}
		for l := range on {
			if l.str == out.str && !containsLit(c.inputs, l) {
				return true
			}
		}
	}
	return false
}

// ---- existing tests of a source file ----

const (
	// maxRelated bounds the existing test files read per source file.
	maxRelated = 6
	// maxRelatedSize skips huge (generated) test files.
	maxRelatedSize = 1 << 20
)

// relatedTests returns the paths of the test files that conventionally
// hold the tests of source file f and that the change did not touch: its
// test next to it (x.test.ts, x_test.go, test_x.py), in a __tests__ or
// tests directory, or under a mirrored test tree (tests/pkg/test_x.py,
// src/test/java/.../XTest.java, spec/models/x_spec.rb). Go tests can live
// in any _test.go file of the package. When the repository cannot be
// listed (agent hooks), the conventional locations are probed.
func relatedTests(a *core.Analysis, f *core.FileInfo) []string {
	if f.IsTest || f.Diff.Kind == diff.Deleted {
		return nil
	}
	names := testBasenames(f.Path, f.Lang)
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names) // a deterministic choice when more than maxRelated match
	isTest := func(p string) bool { return core.IsTestPath(p) || core.MatchAny(a.Settings.TestGlobs, p) }
	var out []string
	seen := map[string]bool{f.Path: true}
	add := func(p string) {
		if !seen[p] && len(out) < maxRelated && isTest(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	dir := path.Dir(f.Path)
	if tree := repoTree(a); tree != nil {
		for _, name := range names {
			for _, p := range tree.byBase[name] {
				if nearTest(dir, path.Dir(p)) {
					add(p)
				}
			}
		}
		if f.Lang == lang.Go {
			for _, p := range tree.byDir[dir] {
				if strings.HasSuffix(p, "_test.go") {
					add(p)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	for _, d := range testDirs(dir) {
		for _, name := range names {
			if p := path.Join(d, name); !seen[p] && isTest(p) {
				if _, ok := a.ReadNew(p); ok {
					add(p)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// testBasenames returns the file names a test of source file p would have.
func testBasenames(p string, lg lang.Lang) []string {
	base := path.Base(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" || strings.HasPrefix(stem, ".") {
		return nil
	}
	var out []string
	add := func(names ...string) {
		for _, n := range names {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	switch lg {
	case lang.JavaScript, lang.TypeScript:
		exts := []string{ext}
		switch ext {
		case ".ts", ".tsx", ".mts", ".cts":
			exts = append(exts, ".ts", ".tsx")
		default:
			exts = append(exts, ".js", ".jsx")
		}
		for _, e := range exts {
			add(stem+".test"+e, stem+".spec"+e)
		}
		add(base) // __tests__/x.ts
	case lang.Python:
		add("test_"+stem+".py", stem+"_test.py")
	case lang.Go:
		add(stem + "_test.go")
	case lang.Java, lang.Kotlin, lang.Scala, lang.Groovy:
		add(stem+"Test"+ext, stem+"Tests"+ext, stem+"IT"+ext, stem+"Spec"+ext)
	case lang.CSharp:
		add(stem+"Tests.cs", stem+"Test.cs")
	case lang.Ruby:
		add(stem+"_spec.rb", stem+"_test.rb", "test_"+stem+".rb")
	case lang.PHP:
		add(stem + "Test.php")
	case lang.Rust:
		add(base, stem+"_test.rs", "test_"+stem+".rs") // integration tests in tests/
	case lang.Swift:
		add(stem+"Tests.swift", stem+"Test.swift")
	case lang.Dart:
		add(stem + "_test.dart")
	case lang.Elixir:
		add(stem + "_test.exs")
	case lang.C, lang.Cpp:
		for _, e := range []string{".c", ".cc", ".cpp"} {
			add(stem+"_test"+e, "test_"+stem+e, stem+"_unittest"+e)
		}
	default:
		return nil
	}
	return out
}

// testRoots are the directories mirrored test trees conventionally live in.
var testRoots = []string{"test", "tests", "spec", "__tests__", "tests/unit", "tests/Unit"}

// testDirs returns the directories that conventionally hold the tests of
// source files in dir, for repositories that cannot be listed.
func testDirs(dir string) []string {
	if dir == "." {
		dir = ""
	}
	out := []string{dir, path.Join(dir, "__tests__"), path.Join(dir, "tests"), path.Join(dir, "test")}
	segs := strings.Split(dir, "/")
	// Maven/Gradle: src/main/java/... → src/test/java/...
	for i := 0; i+1 < len(segs); i++ {
		if segs[i] == "src" && segs[i+1] == "main" {
			mirrored := append(append(append([]string{}, segs[:i+1]...), "test"), segs[i+2:]...)
			out = append(out, path.Join(mirrored...))
		}
	}
	rest := dir
	if len(segs) > 0 && (segs[0] == "src" || segs[0] == "lib" || segs[0] == "app") {
		rest = path.Join(segs[1:]...)
	}
	for _, root := range testRoots {
		out = append(out, path.Join(root, rest), root)
	}
	return out
}

// tree is a listing of the repository at the new revision.
type tree struct {
	byBase map[string][]string
	byDir  map[string][]string
	all    []string
}

type memoKey string

// repoTree returns the listing of the repository, shared by the rules of
// one analysis, or nil when the repository cannot be listed.
func repoTree(a *core.Analysis) *tree {
	t, _ := a.Memo(memoKey("escapes:tree"), func() any {
		paths, ok := a.ListNew("")
		if !ok || len(paths) == 0 {
			return (*tree)(nil)
		}
		t := &tree{byBase: map[string][]string{}, byDir: map[string][]string{}, all: paths}
		for _, p := range paths {
			t.byBase[path.Base(p)] = append(t.byBase[path.Base(p)], p)
			t.byDir[path.Dir(p)] = append(t.byDir[path.Dir(p)], p)
		}
		return t
	}).(*tree)
	return t
}

// layoutSegs are directory names that only say where code or tests live.
var layoutSegs = toSet("src lib app main source sources test tests __tests__ spec specs unit units integration " +
	"java kotlin scala groovy")

// moduleDirs reduces a directory to the module path it names, dropping
// layout directories and test suffixes ("src/test/java/com/acme" and
// "src/main/java/com/acme" are both "com/acme"; "Tests/AcmeTests" is
// "acme").
func moduleDirs(dir string) []string {
	var out []string
	for _, seg := range strings.Split(dir, "/") {
		seg = strings.ToLower(seg)
		if seg == "." || seg == "" || layoutSegs[seg] {
			continue
		}
		for _, suf := range []string{".tests", ".test", "_tests", "_test", "-tests", "-test", "tests", "test"} {
			if t := strings.TrimSuffix(seg, suf); t != seg && t != "" {
				seg = t
				break
			}
		}
		out = append(out, seg)
	}
	return out
}

// nearTest reports whether a test in testDir can belong to source code in
// srcDir: one module path is a suffix of the other ("tests/" holds the
// tests of a package; "packages/a/test" does not hold those of
// "packages/b/src").
func nearTest(srcDir, testDir string) bool {
	s, t := moduleDirs(srcDir), moduleDirs(testDir)
	if len(t) > len(s) {
		s, t = t, s
	}
	for i := range t {
		if s[len(s)-len(t)+i] != t[i] {
			return false
		}
	}
	return true
}

func allIn(set map[lit]bool, ls []lit) bool {
	for _, l := range ls {
		if !l.trivial() && !set[l] {
			return false
		}
	}
	return true
}

func containsLit(ls []lit, x lit) bool {
	for _, l := range ls {
		if l == x {
			return true
		}
	}
	return false
}

// ---- the rule ----

func checkHardcodedAnswer(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool {
		return f.Lang.IsProgramming() && (!f.IsTest || f.Lang == lang.Rust) && !isTestSupport(f.Path) && f.Config == ""
	})
	cands := newConstructs(files, specialCases)
	if len(cands) == 0 {
		return nil
	}
	var sources []*core.FileInfo
	for _, fc := range cands {
		if len(sources) == 0 || sources[len(sources)-1] != fc.file {
			sources = append(sources, fc.file)
		}
	}
	ix := indexTests(a, sources)
	if len(ix.at) == 0 {
		return nil
	}
	var out []core.Finding
	for _, fc := range cands {
		if fd, ok := judge(a, ix, fc.file, fc.side, fc.v); ok {
			out = append(out, fd)
		}
	}
	return out
}

// specialCases finds the literal mappings in the non-test code of one
// version of a file.
func specialCases(f *core.FileInfo, s *side) []specialCase {
	var inTests lineRanges
	if f.Lang == lang.Rust { // unit tests live in #[cfg(test)] modules
		inTests = rustTestRegions(s)
	}
	var out []specialCase
	for _, n := range s.lineNos() {
		if inTests.has(n) {
			continue
		}
		if code, ok := s.code(n); !ok || !mayCompare(code) {
			continue
		}
		if c, ok := parseSpecialCase(s, n); ok && c.pure {
			out = append(out, c)
		}
	}
	return out
}

var compareHints = []string{"==", "equals", "Equals", "case", "when", "=>", "->"}

// mayCompare is a cheap filter for lines that can hold a special case.
func mayCompare(code string) bool {
	for _, h := range compareHints {
		if strings.Contains(code, h) {
			return true
		}
	}
	return false
}

// judge decides whether a new special case hard-codes a test's answer.
func judge(a *core.Analysis, ix *testIndex, f *core.FileInfo, s *side, c specialCase) (core.Finding, bool) {
	if c.computedCond || c.broadOr || cliSubject(c) {
		return core.Finding{}, false
	}
	fn := enclosingFunc(s, c.start)
	if fn == "" {
		// Without the function's name there is no telling whether the test
		// that uses these literals exercises this code at all.
		return core.Finding{}, false
	}
	for _, in := range c.inputs {
		if in.trivial() {
			continue
		}
		for _, out := range c.outputs {
			if out.trivial() || out == in {
				continue
			}
			pos, ok := ix.together(in, out, fn, f)
			if !ok || looksLikeMapping(c, in, out) || ix.contradicted(c, out, fn) {
				continue
			}
			if isTable(s, f, c) || hadOutput(f, out) || producedElsewhere(a, f, c) {
				return core.Finding{}, false
			}
			test := "the test"
			if ix.unchanged[pos.path] != nil {
				test = "the existing test"
			}
			msg := fmt.Sprintf("special-cases %s to return %s: the input and expected value used by %s at %s:%d",
				c.condition(s), c.written(s, out), test, pos.path, pos.line)
			fd := newFinding(hardcodedAnswer, f, c.start, msg)
			// A real-world exception (a calendar reform, a vendor quirk) that
			// the tests pin down is documented with a reference.
			if trackingRefRe.MatchString(lineNote(f, s, c.start)) {
				fd.Severity = core.Info
			}
			return fd, true
		}
	}
	return core.Finding{}, false
}

var (
	// identLikeRe matches category-like strings: codes, keys, flags, paths
	// ("ENOENT", "zh-Hant", "--version", "/health").
	identLikeRe = regexp.MustCompile(`^[-./:#$]*[A-Za-z_][A-Za-z0-9_.:/-]{0,39}$`)
	// labelRe matches a one-word label (not an all-caps encoding such as a
	// Roman numeral).
	labelRe       = regexp.MustCompile(`^[A-Z]?[a-z]{2,15}$`)
	codeSubjectRe = regexp.MustCompile(`(?i)(status|state|phase|stage|code|errno|kind|type|level|mode|month|day|weekday|unit|category|plan|tier|` +
		`version|op|opcode|cmd|command|key|char|ch|keycode|which|port|protocol|scheme|method|lang|locale|currency|country|` +
		`source|provider|channel|platform|role|variant|format|tab|view|outcome)$`)
)

// looksLikeMapping reports whether in → out reads like a category-to-value
// lookup (an error code to a message, a plan to a price, a status to its
// label, a file signature to a MIME type) rather than a computed answer for
// a data input.
func looksLikeMapping(c specialCase, in, out lit) bool {
	for _, o := range c.outputs {
		if o.str && mimeRe.MatchString(o.val) {
			return true // magic bytes or an extension to a MIME type (and its modality)
		}
	}
	if in.str && (mimeRe.MatchString(in.val) || strings.HasPrefix(in.val, "-")) {
		return true // a MIME type or a command-line flag: well-known vocabulary
	}
	if len(dedupe(c.subjects)) > 1 {
		return false // several inputs at once: a test case, not a category
	}
	subject := c.subject
	last := subject
	if i := strings.LastIndexAny(last, ".>&$ "); i >= 0 {
		last = last[i+1:]
	}
	codeLike := codeSubjectRe.MatchString(strings.TrimSuffix(strings.TrimSuffix(last, "()"), "]"))
	if in.str {
		v := strings.ToLower(in.val)
		if placeholderWords[v] || !identLikeRe.MatchString(in.val) {
			return false // made-up or free-text input: data, not a category
		}
		// A status written as its label ("blocked" → "Blocked", "shell" →
		// "background_shell") or explained in a sentence is still a
		// lookup when the compared value is a category; a transformer that
		// special-cases its input ("user_account-id" → "userAccountId") is not.
		return !related(in, out) || codeLike || (out.str && len(strings.Fields(out.val)) >= 3)
	}
	if out.str && labelRe.MatchString(out.val) {
		return true // a number named by a word: 100 → "perfect", 12 → "twelve"
	}
	return codeLike
}

// mimeRe matches a MIME type ("image/jpeg", "application/vnd.api+json").
var mimeRe = regexp.MustCompile(`^(?:application|audio|font|image|message|model|multipart|text|video)/[\w.+-]+$`)

// cliSubjectRe matches values that hold command-line arguments.
var cliSubjectRe = regexp.MustCompile(`(?i)(?:^|[.\s])(?:argv|process\.argv|os\.args|sys\.argv|args?|arguments?|first_?arg\w*|sub_?command|subcmd|cmd|flag)(?:\[\d+\])?$`)

// cliSubject reports whether a special case dispatches on command-line
// arguments: matching a subcommand or flag name is the parser's job.
func cliSubject(c specialCase) bool {
	for _, subj := range c.subjects {
		if cliSubjectRe.MatchString(subj) {
			return true
		}
	}
	return false
}

// hadOutput reports whether the old version of f already returned the
// literal out: the change refactored an existing mapping (a label moved
// into a match, a file renamed), it did not invent the answer.
func hadOutput(f *core.FileInfo, out lit) bool {
	if !f.HasOld() || !out.str || len(out.val) < 3 {
		return false
	}
	for _, l := range f.OldLines() {
		if !strings.Contains(l.NoComment, out.val) {
			continue
		}
		for _, t := range newView(f.Lang, l).toks {
			if lv, ok := litOf(t); ok && lv == out {
				return true
			}
		}
	}
	return false
}

// producedElsewhere reports whether a string the special case compares
// with also appears in other production code (another source file of the
// change or of the repository, a vendored helper, a protocol document):
// it is a value something else emits, such as a backend's reason or a
// helper's wire record, not one made up for a test.
func producedElsewhere(a *core.Analysis, f *core.FileInfo, c specialCase) bool {
	for _, in := range c.inputs {
		if !in.str || in.trivial() {
			continue
		}
		needle := distinctive(in.val)
		if needle == "" {
			continue
		}
		for _, g := range a.Files {
			if g == f || g.IsTest || isTestSupport(g.Path) {
				continue
			}
			if b, ok := g.NewContent(); ok && strings.Contains(string(b), needle) {
				return true
			}
			for _, l := range g.Added { // bare patches: the hunks are all there is
				if strings.Contains(l.Raw, needle) {
					return true
				}
			}
		}
		if repoCount(a, f.Path, needle, scanOpts{limit: 3000, want: 1, anyLang: true, noTests: true}) > 0 {
			return true
		}
	}
	return false
}

var wordRunRe = regexp.MustCompile(`[A-Za-z0-9_.-]{6,}`)

// distinctive returns the part of a literal to look for in other files: the
// literal itself when it is plain text, else its longest word (escaping
// differs between languages).
func distinctive(v string) string {
	if len([]rune(v)) < 6 {
		return ""
	}
	if !strings.ContainsAny(v, "\"'`\\\n\r{}") {
		return v
	}
	best := ""
	for _, w := range wordRunRe.FindAllString(v, -1) {
		if len(w) > len(best) {
			best = w
		}
	}
	return best
}

// related reports whether out is visibly derived from in: it contains it,
// is contained in it, uses the same letters, or is its length.
func related(in, out lit) bool {
	a := strings.ToLower(in.val)
	if !out.str {
		return out.val == strconv.Itoa(len([]rune(in.val)))
	}
	b := strings.ToLower(out.val)
	return strings.Contains(b, a) || strings.Contains(a, b) || letters(a) == letters(b)
}

func letters(s string) string {
	var rs []rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
	return string(rs)
}

// isTable reports whether the special case belongs to a lookup table: the
// enclosing block does nothing but map literals, or already held several
// mappings on the same subject before this change. For a switch or match
// arm, the code around the switch counts too: a switch whose only arm
// special-cases the input, followed by the real computation, is no table.
func isTable(s *side, f *core.FileInfo, c specialCase) bool {
	from, to, ok := enclosingBlock(s, c.start)
	if !ok {
		// Cannot see the surrounding code: stay quiet, unless the special
		// case's own expression computes the other case.
		return !c.elseComputes
	}
	ch := changedLines(f)
	computes, existing := scanBlock(s, ch, c, from, to, c.start, c.end)
	if existing >= 2 {
		return true
	}
	if !computes && !c.elseComputes && c.arm && from > 1 {
		if pf, pt, ok := enclosingBlock(s, from-1); ok {
			computes, _ = scanBlock(s, ch, c, pf, pt, from, to)
		}
	}
	return !computes && !c.elseComputes
}

// scanBlock reports whether lines [from, to] of s, except [skipFrom,
// skipTo], do real computation, and counts the literal mappings on c's
// subject that the change did not touch.
func scanBlock(s *side, ch map[int]bool, c specialCase, from, to, skipFrom, skipTo int) (computes bool, existing int) {
	for n := from; n <= to; n++ {
		if n >= skipFrom && n <= skipTo {
			continue
		}
		if sc, ok := parseSpecialCase(s, n); ok {
			if sc.pure && !ch[n] && sc.subject == c.subject && nonTrivial(sc.inputs) {
				existing++
			}
			if sc.pure {
				n = sc.end
				continue
			}
		}
		if computation(s, n) {
			computes = true
		}
	}
	return computes, existing
}

func nonTrivial(ls []lit) bool {
	for _, l := range ls {
		if !l.trivial() {
			return true
		}
	}
	return false
}

// structural keywords introduce no computation on their own.
var structural = toSet("else elif elsif end default switch match when case break try finally do then begin " +
	"throw raise panic pass fallthrough")

// computation reports whether line n does real work: anything other than
// braces, structural keywords, literal returns and errors.
func computation(s *side, n int) bool {
	v := s.view(n)
	if v == nil {
		return false
	}
	toks := v.toks
	for len(toks) > 0 && toks[0].kind == tPunct && strings.Contains("{}();", toks[0].text) {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return false
	}
	if first := toks[0]; first.kind == tIdent && structural[first.text] {
		switch first.text {
		case "throw", "raise", "panic", "switch", "match", "when", "case":
			return false
		}
		toks = toks[1:]
		for len(toks) > 0 && toks[0].kind == tPunct && strings.Contains("{}:();", toks[0].text) {
			toks = toks[1:]
		}
		if len(toks) == 0 {
			return false
		}
	}
	if toks[0].is(tIdent, "return") {
		toks = toks[1:]
		if len(toks) == 0 {
			return false
		}
	}
	if _, pure := literals(untilEnd(toks)); pure {
		return false
	}
	// Nor is a line of nothing but strings and quotes (a docstring).
	for _, t := range toks {
		if t.kind != tString && !(t.kind == tPunct && strings.Trim(t.text, "\"'`") == "") {
			return true
		}
	}
	return false
}

// enclosingBlock returns the line range of the block that contains line n.
func enclosingBlock(s *side, n int) (from, to int, ok bool) {
	switch s.lg {
	case lang.Python, lang.Ruby, lang.Elixir:
		l, known := s.at(n)
		if !known {
			return 0, 0, false
		}
		ind := indent(l.Raw)
		from = 0
		for m := n - 1; m >= 1; m-- {
			ml, known := s.at(m)
			if !known {
				return 0, 0, false
			}
			if strings.TrimSpace(ml.Code) != "" && indent(ml.Raw) < ind {
				from = m + 1
				break
			}
		}
		if from == 0 {
			return 0, 0, false
		}
		to = n
		for m := n + 1; m <= s.len(); m++ {
			ml, known := s.at(m)
			if !known {
				return from, to, true
			}
			if strings.TrimSpace(ml.Code) != "" && indent(ml.Raw) < ind {
				break
			}
			to = m
		}
		return from, to, true
	}
	depth := 0
	for m := n - 1; m >= 1 && m > n-1000; m-- {
		code, known := s.code(m)
		if !known {
			return 0, 0, false
		}
		for i := len(code) - 1; i >= 0; i-- {
			switch code[i] {
			case '}':
				depth++
			case '{':
				if depth > 0 {
					depth--
					continue
				}
				end, _, ok := s.block(m, i)
				if !ok {
					return 0, 0, false
				}
				return m + 1, end - 1, true
			}
		}
	}
	return 0, 0, false
}

var funcNameRes = []*regexp.Regexp{
	regexp.MustCompile(`\bfunction\s*\*?\s*([A-Za-z_$][\w$]*)`),
	regexp.MustCompile(`\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\([^)]*\)|[\w$]+)\s*(?::[^=]+)?=>`),
	// Object properties: `camel: (s) => {`, `parse: function (s) {`.
	regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*:\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=>`),
	regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*:\s*(?:async\s+)?function\b`),
	regexp.MustCompile(`\bdefp?\s+(?:self\.)?([A-Za-z_]\w*[?!]?)`),
	regexp.MustCompile(`\bfunc\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`),
	regexp.MustCompile(`\bfn\s+([A-Za-z_]\w*)`),
	regexp.MustCompile(`\bfun\s+(?:<[^>]*>\s*)?(?:[\w.]+\.)?([A-Za-z_]\w*)`),
	methodHeadRe,
	exprMemberRe,
}

// exprMemberRe matches an expression-bodied member with a declared result
// type (C#, Dart, Java lambdas excluded): `public static int F(int n) => ...`,
// `int fib(int n) => ...`. At least one word must precede the name, so a
// call such as `setTimeout(() => ...)` does not match.
var exprMemberRe = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|override|virtual|async|sealed|abstract|readonly)\s+)*` +
	`(?:[\w<>\[\],.?]+\s+){1,2}([A-Za-z_]\w*)\s*\([^;]*?\)\s*=>`)

// methodHeadRe matches a C-family method or function header that opens its
// body on the same line or, Allman style, on the next one (group 2 is the
// brace): `public static int Levenshtein(string a, string b)`,
// `const std::string& name() const {`.
var methodHeadRe = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|final|override|virtual|async|sealed|abstract|synchronized|readonly|export|default|inline|constexpr|const|unsafe|extern)\s+)*` +
	`(?:[\w<>\[\],.?:*&]+\s+){0,2}[*&]?([A-Za-z_$][\w$]*)\s*\([^;]*\)\s*(?:const\s*)?(?::\s*[^={;]+)?(?:throws\s+[\w., ]+)?\s*(\{)?\s*$`)

// statementWords start statements, never declarations.
var statementWords = toSet("return await yield throw new else case if for while do await var let const")

var notFuncNames = toSet("if for while switch catch return else elif match when do try with using lock foreach synchronized fixed new await")

// enclosingFunc returns the name of the function around line n, or "".
func enclosingFunc(s *side, n int) string {
	for m := n; m >= 1 && m > n-300; m-- {
		code, ok := s.code(m)
		if !ok {
			return ""
		}
		for _, re := range funcNameRes {
			sm := re.FindStringSubmatch(code)
			if sm == nil || notFuncNames[sm[1]] {
				continue
			}
			if re == exprMemberRe && statementWords[strings.Fields(code)[0]] {
				continue // `return run(() => ...)`
			}
			if re == methodHeadRe && sm[2] == "" {
				// Allman style: the body must open on the next line.
				next, _ := s.code(s.nextCode(m))
				if m == n || !strings.HasPrefix(strings.TrimSpace(next), "{") {
					continue
				}
			}
			return sm[1]
		}
	}
	return ""
}
