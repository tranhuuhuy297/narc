package escapes

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// tokKind classifies a token of a line's code.
type tokKind uint8

const (
	tIdent tokKind = iota
	tNumber
	tString
	tPunct
)

// token is one lexical token of a line's code.
type token struct {
	kind tokKind
	// text is the identifier, number or operator, or the decoded contents of
	// a string literal.
	text string
	// known is false for a string literal whose contents could not be
	// recovered from the lexer output (multi-line strings, interpolation).
	known bool
	// dynamic marks string literals with interpolation (template literals,
	// f-strings, "#{...}"): they are not constants.
	dynamic bool
	pos     int // byte offset of the token in view.canon
}

func (t token) is(kind tokKind, text string) bool { return t.kind == kind && t.text == text }

// view is a lexed line prepared for matching code patterns.
//
// canon is the line without comments but with the contents of string
// literals put back; quotes and backslashes inside literals become '_' so
// literal boundaries stay unambiguous. spans holds the byte ranges of those
// contents, so a pattern that matches inside a string can be rejected: the
// text "process.env.NODE_ENV" in an error message is not a test check.
type view struct {
	canon string
	spans [][2]int
	toks  []token
}

// inString reports whether canon offset pos lies inside a string literal.
func (v *view) inString(pos int) bool {
	// spans are sorted and disjoint: find the first one ending after pos.
	i := sort.Search(len(v.spans), func(i int) bool { return v.spans[i][1] > pos })
	return i < len(v.spans) && pos >= v.spans[i][0]
}

// maxMatches bounds the matches considered per line. Real code never has
// more; a generated one-line bundle could have thousands, and rules look
// at the code around each match.
const maxMatches = 32

// matches returns the submatch indexes of re in canon, keeping only matches
// that start in code (at most maxMatches).
func (v *view) matches(re *regexp.Regexp) [][]int {
	var out [][]int
	for _, m := range re.FindAllStringSubmatchIndex(v.canon, maxMatches*4) {
		if !v.inString(m[0]) {
			out = append(out, m)
			if len(out) == maxMatches {
				break
			}
		}
	}
	return out
}

// tail returns at most the last n bytes of s, for patterns anchored at the
// end of the code before a match: scanning the whole prefix of every match
// would be quadratic on a long line.
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// match reports whether re matches canon somewhere in code.
func (v *view) match(re *regexp.Regexp) bool { return len(v.matches(re)) > 0 }

// stringsIn returns the contents of the string literals between canon
// offsets [from, to).
func (v *view) stringsIn(from, to int) []string {
	var out []string
	for _, t := range v.toks {
		if t.kind == tString && t.known && t.pos >= from && t.pos < to {
			out = append(out, t.text)
		}
	}
	return out
}

// Multi-character operators, longest first.
var multiOps = []string{"===", "!==", "...", "**=", "<<=", ">>=", "==", "!=", "<=", ">=", "&&", "||",
	"=>", "->", "::", "?.", "??", ":=", "+=", "-=", "*=", "/=", "|=", "&=", "++", "--", "<<", ">>"}

// newView tokenizes a lexed line of language lg.
func newView(lg lang.Lang, l lang.LexedLine) *view {
	code := l.Code
	triple := tripleQuoted(lg)
	// The lexer keeps string delimiters in Code and the contents in Strings,
	// in order. Count the literals first: if the counts disagree (a string
	// spans lines, a template has interpolations) contents stay unknown.
	n := 0
	for i := 0; i < len(code); {
		w, lone := literalAt(code, i, triple)
		switch {
		case w > 0:
			n++
			i += w
		case lone > 0:
			i += lone
		default:
			i++
		}
	}
	aligned := n == len(l.Strings)

	v := &view{}
	var b strings.Builder
	k := 0
	prevEnd := -1 // code offset just past the previous token
	prevText := ""
	for i := 0; i < len(code); {
		c := code[i]
		w, lone := literalAt(code, i, triple)
		switch {
		case w > 0:
			d := w / 2
			delim := code[i : i+d]
			content, known := "", false
			if aligned && k < len(l.Strings) {
				content, known = l.Strings[k], true
			}
			k++
			prefix := ""
			if prevEnd == i {
				prefix = prevText
			}
			start := b.Len()
			b.WriteString(delim)
			b.WriteString(sanitize(content))
			v.spans = append(v.spans, [2]int{start + d, b.Len()})
			b.WriteString(delim)
			v.toks = append(v.toks, token{kind: tString, text: unescape(content), known: known,
				dynamic: interpolated(lg, delim[0], prefix, content), pos: start})
			i += w
			prevEnd, prevText = i, ""
			continue
		case lone > 0:
			v.toks = append(v.toks, token{kind: tPunct, text: code[i : i+lone], pos: b.Len()})
			b.WriteString(code[i : i+lone])
			i += lone
			prevEnd, prevText = i, ""
			continue
		}
		r, size := utf8.DecodeRuneInString(code[i:])
		switch {
		case unicode.IsSpace(r):
			b.WriteString(code[i : i+size])
			i += size
		case isIdentStart(r):
			j := i + size
			for j < len(code) {
				r2, s2 := utf8.DecodeRuneInString(code[j:])
				if !isIdentStart(r2) && !unicode.IsDigit(r2) {
					break
				}
				j += s2
			}
			v.toks = append(v.toks, token{kind: tIdent, text: code[i:j], pos: b.Len()})
			b.WriteString(code[i:j])
			prevEnd, prevText = j, code[i:j]
			i = j
		case c >= '0' && c <= '9':
			j := scanNumber(code, i)
			v.toks = append(v.toks, token{kind: tNumber, text: code[i:j], pos: b.Len()})
			b.WriteString(code[i:j])
			prevEnd, prevText = j, ""
			i = j
		default:
			op := code[i : i+size]
			for _, m := range multiOps {
				if strings.HasPrefix(code[i:], m) {
					op = m
					break
				}
			}
			v.toks = append(v.toks, token{kind: tPunct, text: op, pos: b.Len()})
			b.WriteString(op)
			i += len(op)
			prevEnd, prevText = i, ""
		}
	}
	v.canon = b.String()
	return v
}

func isIdentStart(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) }

// literalAt reports the width of the (content-less) string literal that
// starts at code[i], or the width of a lone delimiter to skip.
func literalAt(code string, i int, triple bool) (width, lone int) {
	c := code[i]
	if c != '"' && c != '\'' && c != '`' {
		return 0, 0
	}
	if triple && strings.HasPrefix(code[i:], strings.Repeat(string(c), 3)) {
		if strings.HasPrefix(code[i+3:], strings.Repeat(string(c), 3)) {
			return 6, 0
		}
		return 0, 3
	}
	if i+1 < len(code) && code[i+1] == c {
		return 2, 0
	}
	return 0, 1
}

func tripleQuoted(lg lang.Lang) bool {
	switch lg {
	case lang.Python, lang.Java, lang.Kotlin, lang.Swift, lang.Scala, lang.Groovy, lang.Dart, lang.CSharp, lang.Elixir, lang.TOML:
		return true
	}
	return false
}

func scanNumber(code string, i int) int {
	j := i
	hex := strings.HasPrefix(code[i:], "0x") || strings.HasPrefix(code[i:], "0X")
	dot := false
	for j < len(code) {
		c := code[j]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
			j++
		case c == '.' && !dot && !hex && j+1 < len(code) && code[j+1] >= '0' && code[j+1] <= '9':
			dot = true
			j++
		case (c == '+' || c == '-') && !hex && j > i && (code[j-1] == 'e' || code[j-1] == 'E'):
			j++
		default:
			return j
		}
	}
	return j
}

// sanitize makes string contents safe to embed in canon.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\'', '`', '\\', '\n', '\r':
			return '_'
		}
		return r
	}, s)
}

// unescape decodes the common backslash escapes of a string literal.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\', '"', '\'', '`':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// interpolated reports whether a string literal interpolates values, so its
// text is not a constant.
func interpolated(lg lang.Lang, quote byte, prefix, content string) bool {
	p := strings.ToLower(prefix)
	switch lg {
	case lang.Python:
		return strings.Contains(p, "f") && strings.Trim(p, "rbfu") == ""
	case lang.JavaScript, lang.TypeScript:
		return quote == '`' && strings.Contains(content, "${")
	case lang.Ruby, lang.Elixir:
		return quote == '"' && strings.Contains(content, "#{")
	case lang.Swift:
		return strings.Contains(content, `\(`)
	case lang.CSharp:
		return strings.Contains(prefix, "$")
	case lang.Scala:
		return (p == "s" || p == "f" || p == "raw") || (quote == '"' && dollarInterp(content))
	case lang.Kotlin, lang.Groovy, lang.PHP, lang.Shell:
		return quote == '"' && dollarInterp(content)
	case lang.Dart:
		return dollarInterp(content)
	}
	return false
}

func dollarInterp(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && (s[i+1] == '{' || s[i+1] == '_' || unicode.IsLetter(rune(s[i+1]))) {
			return true
		}
	}
	return false
}

// normNumber canonicalizes a numeric literal: digit separators and type
// suffixes are dropped and the value is printed in a standard form, so
// 5_040, 5040L and 5040.0 compare equal.
func normNumber(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "_", "")
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0b") || strings.HasPrefix(s, "0o") {
		if v, err := strconv.ParseInt(strings.TrimRight(s, "ul"), 0, 64); err == nil {
			return strconv.FormatInt(v, 10)
		}
		return s
	}
	s = numSuffixRe.ReplaceAllString(s, "")
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return s
}

// numSuffixRe matches numeric type suffixes: 10n (BigInt), 10L, 10UL,
// 1.5f, 1.5m (C# decimal), 10i32 / 2.0f64 (Rust).
var numSuffixRe = regexp.MustCompile(`(?:[iu](?:8|16|32|64|128|size)|f32|f64|ul|lu|[nludfm])$`)
