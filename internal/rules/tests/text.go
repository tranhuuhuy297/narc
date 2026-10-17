package tests

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// rustAsyncTestAttrRe matches the test attributes of async runtimes other
// than tokio: #[async_std::test], #[actix_rt::test], #[sqlx::test].
var rustAsyncTestAttrRe = regexp.MustCompile(`#\[\w+::test\b`)

// inTests reports whether f holds test code that these rules inspect. On
// top of core's classification it accepts Rust files whose hunks show an
// async runtime's test attribute, which core does not look for when full
// contents are unavailable (a bare patch).
func inTests(f *core.FileInfo) bool {
	if f.IsTest {
		return true
	}
	if f.Lang == lang.Rust && !f.HasNew() {
		for _, h := range f.Diff.Hunks {
			for _, l := range h.Lines {
				if rustAsyncTestAttrRe.MatchString(l.Text) {
					return true
				}
			}
		}
	}
	return false
}

// testFiles returns the files of a that hold test code.
func testFiles(a *core.Analysis) []*core.FileInfo { return rulekit.Files(a, inTests) }

// isJVM reports whether l runs on the JVM and uses JUnit-style annotations.
func isJVM(l lang.Lang) bool {
	return l == lang.Java || l == lang.Kotlin || l == lang.Groovy || l == lang.Scala
}

// ---- value text -------------------------------------------------------------

// valueText returns the line's code (comments removed) with the contents of
// its string literals put back, percent-encoded so that quotes, commas,
// brackets and operators inside a literal cannot be mistaken for code:
//
//	assertEquals("a, b", x)  →  assertEquals("a%2C%20b", x)
//
// Rules split arguments and compare values on this text; display() turns it
// back into source for messages. ok is false when the literals on the line
// could not be matched up with the lexer's string list (a template literal
// with interpolation, a string that spans lines); the literals then stay
// empty and callers must not trust literal values.
func valueText(l core.Line, lg lang.Lang) (string, bool) {
	code := l.Code
	if !strings.ContainsAny(code, "\"'`") {
		return code, len(l.Strings) == 0
	}
	var b strings.Builder
	k, ok := 0, true
	for i := 0; i < len(code); {
		c := code[i]
		if c != '"' && c != '\'' && c != '`' {
			b.WriteByte(c)
			i++
			continue
		}
		n := 1
		for i+n < len(code) && code[i+n] == c {
			n++
		}
		width := 1 // delimiter width of the literal starting here
		switch {
		case n >= 6 && c != '`':
			width = 3 // an empty-looking triple-quoted literal: """"""
		case n == 1:
			// A lone quote: a literal that spans lines, a Rust lifetime, an
			// apostrophe the lexer treated as code.
			if !(lg == lang.Rust && c == '\'') {
				ok = false
			}
			b.WriteByte(c)
			i++
			continue
		}
		q := code[i : i+width]
		b.WriteString(q)
		if k < len(l.Strings) {
			b.WriteString(encodeLiteral(l.Strings[k]))
		} else {
			ok = false
		}
		b.WriteString(q)
		k++
		i += 2 * width
	}
	if k != len(l.Strings) {
		ok = false
	}
	return b.String(), ok
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// encodeLiteral percent-encodes everything but letters, digits and '_'.
func encodeLiteral(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isWordByte(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// decodeLiteral reverses encodeLiteral.
func decodeLiteral(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// display turns value text back into source text for messages: literal
// contents are decoded and whitespace is collapsed.
func display(s string) string {
	var b strings.Builder
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '"' && c != '\'' && c != '`' {
			continue
		}
		switch {
		case quote == 0:
			b.WriteString(s[start : i+1])
			quote, start = c, i+1
		case c == quote:
			b.WriteString(decodeLiteral(s[start:i]))
			b.WriteByte(c)
			quote, start = 0, i+1
		}
	}
	b.WriteString(s[start:])
	return clip(strings.Join(strings.Fields(b.String()), " "), 120)
}

// clip shortens s to at most n bytes, marking the cut with "…".
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// ---- expressions ------------------------------------------------------------

// expr is an argument or operand taken from value text.
type expr struct {
	raw  string // trimmed value text
	norm string // rulekit.Normalize(raw): reformatting-insensitive
}

func newExpr(s string) expr {
	s = trimParens(strings.TrimSpace(s))
	return expr{raw: s, norm: rulekit.Normalize(s)}
}

func (e expr) String() string { return display(e.raw) }

// trimParens removes redundant outer parentheses: "((x))" → "x". It keeps
// them around a tuple such as "(a, b)".
func trimParens(s string) string {
	for len(s) >= 2 && s[0] == '(' {
		end, ok := closing(s, 0)
		if !ok || end != len(s)-1 || len(splitTop(s[1:end], ',')) > 1 || strings.HasSuffix(strings.TrimSpace(s[1:end]), ",") {
			break
		}
		s = strings.TrimSpace(s[1:end])
	}
	return s
}

// closing returns the index of the bracket that closes s[open], which must
// be one of ( [ {. Literal contents in value text are encoded, so brackets
// inside strings cannot confuse it.
func closing(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// splitTop splits s at sep characters that are not nested in brackets.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// args splits the call whose "(" is at vt[open] into expressions, using
// rulekit.CallArgs (value text keeps literal contents out of its way).
func args(vt string, open int) ([]expr, int, bool) {
	raw, end, ok := rulekit.CallArgs(vt, open)
	if !ok {
		return nil, 0, false
	}
	out := make([]expr, len(raw))
	for i, a := range raw {
		out[i] = newExpr(a)
	}
	return out, end, true
}

// positional drops keyword arguments (msg="..." / name: value) from as.
func positional(as []expr) []expr {
	var out []expr
	for _, a := range as {
		if _, _, ok := keyword(a); !ok {
			out = append(out, a)
		}
	}
	return out
}

var keywordArgRe = regexp.MustCompile(`^([A-Za-z_]\w*)\s*(?:=|:)\s*([^=].*)$`)

// keyword splits a keyword argument "name=value" or "name: value".
func keyword(a expr) (name string, value expr, ok bool) {
	m := keywordArgRe.FindStringSubmatch(a.raw)
	if m == nil || m[1] == "lambda" {
		return "", expr{}, false
	}
	return m[1], newExpr(m[2]), true
}

// kwarg returns the keyword argument called name.
func kwarg(as []expr, name string) (expr, bool) {
	for _, a := range as {
		if n, v, ok := keyword(a); ok && n == name {
			return v, true
		}
	}
	return expr{}, false
}

// pure reports whether evaluating e twice must give the same value: it has
// no calls, constructors, awaits or increments. Comparing a pure expression
// with itself can never fail; comparing hash(x) with hash(x) is a
// legitimate determinism check.
func pure(e expr) bool {
	if e.norm == "" {
		return false
	}
	if strings.Contains(e.raw, "(") && !isLiteral(e) {
		return false
	}
	for _, effect := range []string{"++", "--", "<-", "new ", "await ", "yield "} {
		if strings.Contains(e.raw, effect) {
			return false
		}
	}
	return true
}

// ---- literals ---------------------------------------------------------------

type litKind int

const (
	notLiteral litKind = iota
	litNull
	litBool
	litNumber
	litString
	litCollection
	litSymbol // Ruby/Elixir :atom
)

// literal is a constant expression.
type literal struct {
	kind  litKind
	value string  // canonical value: "true", decoded string contents, number text
	num   float64 // for numbers
	empty bool    // empty string or collection
	// truthy is the value's truthiness in the file's language; known says
	// whether the language defines one (Java's assertTrue only takes booleans).
	truthy, known bool
	// constant is false for collections with computed elements: [f(x)].
	constant bool
	// escapes is set for string literals with backslash escapes, whose
	// runtime value is not known from the source text ("\x41" is "A").
	escapes bool
}

var (
	numberRe = regexp.MustCompile(`^[+-]?(?:0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|\d[\d_]*(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?|\.\d+(?:[eE][+-]?\d+)?)([a-zA-Z]\w*)?$`)
	stringRe = regexp.MustCompile("^([rRbBuU]{0,2}|@)(\"\"\"|'''|\"|'|`)([A-Za-z0-9_%]*)(\"\"\"|'''|\"|'|`)$")
	symbolRe = regexp.MustCompile(`^:[A-Za-z_]\w*[?!]?$`)
)

// isLiteral reports whether e is a constant.
func isLiteral(e expr) bool {
	l := lit(e, lang.Unknown)
	return l.kind != notLiteral && (l.kind != litCollection || l.constant)
}

// lit classifies e as a literal of language lg.
func lit(e expr, lg lang.Lang) literal {
	s := e.raw
	switch s {
	case "null", "undefined", "nil", "None", "nullptr", "NULL":
		if s == "None" && lg != lang.Python && lg != lang.Unknown {
			return literal{}
		}
		return literal{kind: litNull, value: s, truthy: false, known: true}
	case "true", "True", "TRUE":
		return literal{kind: litBool, value: "true", truthy: true, known: true}
	case "false", "False", "FALSE":
		return literal{kind: litBool, value: "false", truthy: false, known: true}
	}
	if lg == lang.PHP {
		switch strings.ToLower(s) {
		case "null":
			return literal{kind: litNull, value: "null", known: true}
		case "true":
			return literal{kind: litBool, value: "true", truthy: true, known: true}
		case "false":
			return literal{kind: litBool, value: "false", known: true}
		}
	}
	if numberRe.MatchString(s) {
		n, ok := parseNumber(s)
		if !ok {
			return literal{}
		}
		l := literal{kind: litNumber, value: strconv.FormatFloat(n, 'g', -1, 64), num: n}
		l.truthy, l.known = truthiness(lg, n != 0)
		return l
	}
	if m := stringRe.FindStringSubmatch(s); m != nil && m[2] == m[4] && !interpolated(m[3], m[2], lg) {
		v := decodeLiteral(m[3])
		l := literal{kind: litString, value: v, empty: v == ""}
		// Values are compared as written, so two spellings only count as equal
		// when they surely denote the same value: b"abc" is not "abc" in
		// Python, and with escapes r"\n", '\n' (Ruby, PHP) and "\n" differ.
		l.escapes = strings.Contains(v, `\`)
		if strings.ContainsAny(m[1], "bB") || l.escapes {
			l.value = strings.ToLower(m[1]) + m[2] + v
		}
		nonEmpty := v != "" && !(lg == lang.PHP && v == "0")
		l.truthy, l.known = truthiness(lg, nonEmpty)
		return l
	}
	if (lg == lang.Ruby || lg == lang.Elixir) && symbolRe.MatchString(s) {
		return literal{kind: litSymbol, value: s, truthy: true, known: true}
	}
	if c, ok := collection(s, lg); ok {
		return c
	}
	return literal{}
}

// truthiness reports how a non-bool constant behaves in a boolean context.
// Statically typed languages only accept booleans there, so it is unknown.
func truthiness(lg lang.Lang, jsLike bool) (truthy, known bool) {
	switch lg {
	case lang.JavaScript, lang.TypeScript, lang.Python, lang.PHP, lang.C, lang.Cpp:
		return jsLike, true
	case lang.Ruby, lang.Elixir:
		return true, true // only nil and false are falsy
	}
	return false, false
}

// interpolated reports whether a string literal's contents are computed:
// "${x}", "#{x}", "\(x)", Kotlin/Dart/PHP "$x". (Python f-strings never
// get here: their f prefix fails stringRe.)
func interpolated(enc, quote string, lg lang.Lang) bool {
	v := decodeLiteral(enc)
	switch {
	case strings.Contains(v, "${"), strings.Contains(v, "#{"), strings.Contains(v, `\(`):
		return true
	case lg == lang.Kotlin || lg == lang.Dart || lg == lang.Groovy || lg == lang.Scala || lg == lang.PHP:
		return quote != "'" && strings.Contains(v, "$")
	}
	return false
}

// parseNumber parses a numeric literal, ignoring digit separators and type
// suffixes (1_000, 10L, 1.5f, 0xFFu8).
func parseNumber(s string) (float64, bool) {
	s = strings.ReplaceAll(s, "_", "")
	if m := numberRe.FindStringSubmatch(s); m != nil && m[1] != "" {
		s = strings.TrimSuffix(s, m[1])
	}
	if len(s) > 2 && s[0] == '0' && strings.ContainsRune("xXbBoO", rune(s[1])) {
		v, err := strconv.ParseInt(s, 0, 64)
		return float64(v), err == nil
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

var vecMacroRe = regexp.MustCompile(`^(?:vec!|%|array)?[\[{(]`)

// collection recognizes list, map, set and tuple displays: [1, 2], {},
// ("a",), vec![1], %{}. Their truthiness never depends on the elements
// (a non-empty Python tuple is truthy whatever it holds), but they only
// count as constants when every element is a literal or a plain name.
func collection(s string, lg lang.Lang) (literal, bool) {
	m := vecMacroRe.FindString(s)
	if m == "" {
		return literal{}, false
	}
	open := len(m) - 1
	end, ok := closing(s, open)
	if !ok || end != len(s)-1 {
		return literal{}, false
	}
	inner := strings.TrimSpace(s[open+1 : end])
	if s[open] == '(' && (lg != lang.Python || inner != "" && !strings.Contains(inner, ",")) {
		return literal{}, false // a parenthesized expression, not a tuple
	}
	if strings.Contains(inner, " for ") || strings.Contains(inner, "*") {
		return literal{}, false // a comprehension or an unpacking may be empty
	}
	l := literal{kind: litCollection, value: rulekit.Normalize(s), empty: inner == "", constant: true}
	for _, el := range splitTop(inner, ',') {
		// map entries: key: value / key => value
		for _, part := range strings.FieldsFunc(el, func(r rune) bool { return r == ':' || r == '=' || r == '>' }) {
			if part = strings.TrimSpace(part); part != "" && !isLiteral(newExpr(part)) && !isIdent(part) {
				l.constant = false
			}
		}
	}
	switch lg {
	case lang.Python, lang.PHP:
		l.truthy, l.known = !l.empty, true
	case lang.JavaScript, lang.TypeScript, lang.Ruby, lang.Elixir:
		l.truthy, l.known = true, true
	}
	return l, true
}

var identRe = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

func isIdent(s string) bool { return identRe.MatchString(strings.TrimSpace(s)) }

// sameValue reports whether a and b always evaluate to the same value, and
// how it knows: "itself" (the same pure expression) or "literals".
func sameValue(a, b expr, lg lang.Lang) (bool, string) {
	la, lb := lit(a, lg), lit(b, lg)
	if la.kind == litString || lb.kind == litString {
		// Normalization unifies quote styles, but 'a\n' and "a\n" differ in
		// Ruby and PHP: compare string literals by value.
		return la.kind == lb.kind && la.value == lb.value, "literals"
	}
	if a.norm == b.norm && pure(a) {
		return true, "itself"
	}
	if la.kind == notLiteral || lb.kind == notLiteral || la.kind != lb.kind || la.kind == litCollection {
		return false, ""
	}
	return la.value == lb.value, "literals"
}

// differentLiterals reports whether a and b are constants of the same kind
// with different values, so a not-equal assertion on them cannot fail.
func differentLiterals(a, b expr, lg lang.Lang) bool {
	la, lb := lit(a, lg), lit(b, lg)
	if la.kind == notLiteral || la.kind != lb.kind || la.kind == litCollection || la.kind == litNull || la.escapes || lb.escapes {
		return false
	}
	return la.value != lb.value
}
