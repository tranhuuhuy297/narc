package lang

import (
	"strings"
	"unicode"
)

// LexedLine is one source line split into its code, comment and string
// parts.
type LexedLine struct {
	Raw string
	// Code is the line with comments removed and the contents of string
	// literals removed (delimiters are kept, so `it.skip("x")` becomes
	// `it.skip("")`).
	Code string
	// NoComment is the line with comments removed but strings intact.
	NoComment string
	// Comment is the text of any comments on the line, without comment
	// markers. Several comments are joined with a single space.
	Comment string
	// Strings holds the contents of string literals that start on this line.
	Strings []string
}

// Lex splits src into lines and classifies each character as code, comment
// or string. Lexing is best-effort: it never fails, and it is tolerant of
// syntax it does not understand.
func Lex(l Lang, src string) []LexedLine {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lx := &lexer{lang: l, fam: l.family(), src: []rune(src)}
	if src == "" {
		return nil
	}
	lx.run()
	return lx.out
}

// LexLine lexes a single line in isolation.
func LexLine(l Lang, line string) LexedLine {
	ls := Lex(l, line)
	if len(ls) == 0 {
		return LexedLine{}
	}
	return ls[0]
}

type state int

const (
	stCode state = iota
	stLineComment
	stBlockComment
	stString
	stRegex
)

type lexer struct {
	lang Lang
	fam  family
	src  []rune
	i    int

	out                   []LexedLine
	raw, code, nocom, com strings.Builder
	strs                  []string
	pendingContent        bool

	st state

	// string state
	strClose     string // closing delimiter
	strEsc       bool   // backslash escapes are active
	strMulti     bool   // the literal may span lines
	strDoubling  bool   // a doubled delimiter is an escape (YAML '', C# verbatim "")
	strTemplate  bool   // JS template literal with ${} interpolation
	strBuf       strings.Builder
	strStartLine int

	// block comment state
	blockDepth int
	blockNest  bool

	// JS template interpolation: stack of brace depths at which a ${ opened
	tmplStack  []int
	braceDepth int

	// regex state
	reClass bool

	lastSig  rune   // last significant code rune on the current logical line
	lastWord string // last identifier-like word in code
}

func (lx *lexer) peek(off int) rune {
	j := lx.i + off
	if j < 0 || j >= len(lx.src) {
		return 0
	}
	return lx.src[j]
}

func (lx *lexer) hasPrefix(s string) bool {
	j := lx.i
	for _, r := range s {
		if j >= len(lx.src) || lx.src[j] != r {
			return false
		}
		j++
	}
	return true
}

func (lx *lexer) emitCode(r rune) {
	lx.code.WriteRune(r)
	lx.nocom.WriteRune(r)
	if !unicode.IsSpace(r) {
		lx.lastSig = r
	}
}

func (lx *lexer) emitCodeStr(s string) {
	for _, r := range s {
		lx.emitCode(r)
	}
}

func (lx *lexer) flushLine() {
	lx.out = append(lx.out, LexedLine{
		Raw:       lx.raw.String(),
		Code:      lx.code.String(),
		NoComment: lx.nocom.String(),
		Comment:   strings.TrimSpace(lx.com.String()),
		Strings:   lx.strs,
	})
	lx.raw.Reset()
	lx.code.Reset()
	lx.nocom.Reset()
	lx.com.Reset()
	lx.strs = nil
	lx.lastSig = 0
	lx.lastWord = ""
}

func (lx *lexer) addComment(s string) {
	if lx.com.Len() > 0 && s != "" {
		lx.com.WriteByte(' ')
	}
	lx.com.WriteString(s)
}

func (lx *lexer) run() {
	for lx.i < len(lx.src) {
		r := lx.src[lx.i]
		if r == '\n' {
			lx.newline()
			lx.i++
			continue
		}
		lx.pendingContent = true
		switch lx.st {
		case stCode:
			lx.stepCode()
		case stLineComment:
			lx.raw.WriteRune(r)
			lx.com.WriteRune(r)
			lx.i++
		case stBlockComment:
			lx.stepBlock()
		case stString:
			lx.stepString()
		case stRegex:
			lx.stepRegex()
		}
	}
	if lx.st == stString {
		lx.endString()
	}
	if lx.pendingContent {
		lx.flushLine()
	}
}

func (lx *lexer) newline() {
	switch lx.st {
	case stLineComment:
		lx.st = stCode
	case stString:
		if lx.strMulti {
			lx.strBuf.WriteByte('\n')
		} else if lx.strEsc && strings.HasSuffix(lx.raw.String(), `\`) && !strings.HasSuffix(lx.raw.String(), `\\`) {
			// backslash-newline continuation
			lx.strBuf.WriteByte('\n')
		} else {
			lx.endString()
			lx.st = stCode
		}
	case stRegex:
		lx.st = stCode
	}
	lx.flushLine()
	lx.pendingContent = false
}

// ---- code state ----

func isIdent(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) }

func (lx *lexer) prev() rune { return lx.peek(-1) }

func (lx *lexer) atLineStartOrSpace() bool {
	p := lx.prev()
	return p == 0 || p == '\n' || p == ' ' || p == '\t'
}

func (lx *lexer) stepCode() {
	r := lx.src[lx.i]
	switch lx.fam {
	case famC:
		if lx.stepCodeC(r) {
			return
		}
	case famHash:
		if lx.stepCodeHash(r) {
			return
		}
	}
	// plain code rune
	if isIdent(r) {
		// accumulate word
		j := lx.i
		for j < len(lx.src) && isIdent(lx.src[j]) {
			j++
		}
		w := string(lx.src[lx.i:j])
		lx.raw.WriteString(w)
		lx.emitCodeStr(w)
		lx.lastWord = w
		lx.i = j
		return
	}
	if r == '{' {
		lx.braceDepth++
	}
	if r == '}' {
		if n := len(lx.tmplStack); n > 0 && lx.tmplStack[n-1] == lx.braceDepth {
			// end of ${...} interpolation: resume the template literal
			lx.tmplStack = lx.tmplStack[:n-1]
			lx.braceDepth--
			lx.raw.WriteRune(r)
			lx.emitCode(r)
			lx.i++
			// resume the enclosing template literal (an inner string may
			// have replaced the string settings)
			lx.st = stString
			lx.strClose, lx.strEsc, lx.strMulti, lx.strDoubling, lx.strTemplate = "`", true, true, false, true
			lx.strBuf.Reset()
			lx.strStartLine = len(lx.out)
			return
		}
		lx.braceDepth--
	}
	lx.raw.WriteRune(r)
	lx.emitCode(r)
	if !unicode.IsSpace(r) {
		lx.lastWord = ""
	}
	lx.i++
}

func (lx *lexer) stepCodeC(r rune) bool {
	switch {
	case r == '/' && lx.peek(1) == '/':
		lx.raw.WriteString("//")
		lx.i += 2
		lx.st = stLineComment
		lx.addComment("")
		return true
	case r == '/' && lx.peek(1) == '*':
		lx.raw.WriteString("/*")
		lx.i += 2
		lx.st = stBlockComment
		lx.blockDepth = 1
		lx.blockNest = lx.lang == Rust || lx.lang == Swift || lx.lang == Scala || lx.lang == Dart || lx.lang == Kotlin
		lx.addComment("")
		return true
	case r == '#' && lx.lang == PHP && lx.peek(1) != '[':
		lx.raw.WriteRune(r)
		lx.i++
		lx.st = stLineComment
		lx.addComment("")
		return true
	case r == '/' && lx.lang.IsJSLike() && lx.regexAllowed():
		lx.raw.WriteRune(r)
		lx.emitCode(r)
		lx.i++
		lx.st = stRegex
		lx.reClass = false
		lx.strBuf.Reset()
		lx.strStartLine = len(lx.out)
		return true
	}
	// strings
	switch r {
	case '"':
		if lx.hasTriple('"') && lx.supportsTriple() {
			lx.openString(`"""`, lx.lang != Kotlin && lx.lang != Scala, true, false, false)
			return true
		}
		if lx.lang == CSharp && lx.prev() == '@' {
			lx.openString(`"`, false, true, true, false)
			return true
		}
		lx.openString(`"`, true, false, false, false)
		return true
	case '\'':
		switch {
		case lx.lang.IsJSLike() || lx.lang == PHP || lx.lang == Groovy || lx.lang == Dart || lx.lang == JSON:
			if lx.hasTriple('\'') && (lx.lang == Groovy || lx.lang == Dart) {
				lx.openString(`'''`, true, true, false, false)
				return true
			}
			lx.openString(`'`, true, false, false, false)
			return true
		case lx.lang == Rust:
			// char literal vs lifetime/label
			if lx.peek(1) == '\\' || (lx.peek(2) == '\'' && lx.peek(1) != 0) {
				lx.openString(`'`, true, false, false, false)
				return true
			}
			return false
		case lx.lang == Swift:
			return false
		default:
			lx.openString(`'`, true, false, false, false)
			return true
		}
	case '`':
		switch {
		case lx.lang.IsJSLike():
			lx.openString("`", true, true, false, true)
			return true
		case lx.lang == Go:
			lx.openString("`", false, true, false, false)
			return true
		case lx.lang == Kotlin || lx.lang == Scala:
			lx.openString("`", false, false, false, false)
			return true
		}
	case 'r', 'b':
		if lx.lang == Rust && !isIdent(lx.prev()) {
			j := lx.i + 1
			if r == 'b' && j < len(lx.src) && lx.src[j] == 'r' {
				j++
			} else if r == 'b' {
				return false
			}
			hashes := 0
			for j < len(lx.src) && lx.src[j] == '#' {
				hashes++
				j++
			}
			if j < len(lx.src) && lx.src[j] == '"' {
				prefix := string(lx.src[lx.i:j])
				lx.raw.WriteString(prefix)
				lx.emitCodeStr(prefix)
				lx.i = j
				lx.openStringClose(`"`, `"`+strings.Repeat("#", hashes), false, true, false, false)
				return true
			}
		}
	}
	return false
}

func (lx *lexer) supportsTriple() bool {
	switch lx.lang {
	case Java, Kotlin, Swift, Scala, Groovy, Dart, CSharp:
		return true
	}
	return false
}

func (lx *lexer) hasTriple(q rune) bool {
	return lx.peek(0) == q && lx.peek(1) == q && lx.peek(2) == q
}

var regexKeywords = map[string]bool{"return": true, "typeof": true, "case": true, "do": true, "else": true,
	"in": true, "of": true, "void": true, "yield": true, "await": true, "delete": true, "throw": true, "new": true, "instanceof": true}

func (lx *lexer) regexAllowed() bool {
	if lx.peek(1) == '/' || lx.peek(1) == '*' {
		return false
	}
	if lx.lastWord != "" {
		return regexKeywords[lx.lastWord]
	}
	switch lx.lastSig {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
		return true
	}
	return false
}

func (lx *lexer) stepCodeHash(r rune) bool {
	switch lx.lang {
	case Python:
		if r == '#' {
			lx.startLineComment(1)
			return true
		}
		if r == '"' || r == '\'' {
			if lx.hasTriple(r) {
				d := string([]rune{r, r, r})
				lx.openString(d, true, true, false, false)
				return true
			}
			lx.openString(string(r), true, false, false, false)
			return true
		}
	case Ruby, Elixir:
		if r == '#' {
			lx.startLineComment(1)
			return true
		}
		if lx.lang == Ruby && r == '=' && lx.prev() == 0 || (lx.lang == Ruby && r == '=' && lx.prev() == '\n') {
			if lx.hasPrefix("=begin") {
				lx.raw.WriteString("=begin")
				lx.i += len("=begin")
				lx.st = stBlockComment
				lx.blockDepth = 1
				lx.blockNest = false
				return true
			}
		}
		if lx.lang == Elixir && lx.hasTriple('"') {
			lx.openString(`"""`, true, true, false, false)
			return true
		}
		if r == '"' || r == '\'' {
			if r == '\'' && lx.lang == Ruby && isIdent(lx.prev()) {
				return false
			}
			lx.openString(string(r), r == '"' || lx.lang == Ruby, false, false, false)
			return true
		}
	case TOML:
		if r == '#' {
			lx.startLineComment(1)
			return true
		}
		if r == '"' || r == '\'' {
			if lx.hasTriple(r) {
				lx.openString(string([]rune{r, r, r}), r == '"', true, false, false)
				return true
			}
			lx.openString(string(r), r == '"', false, false, false)
			return true
		}
	case INI:
		if (r == '#' || r == ';') && lx.onlySpaceBefore() {
			lx.startLineComment(1)
			return true
		}
		if r == '#' && lx.atLineStartOrSpace() {
			lx.startLineComment(1)
			return true
		}
	default: // Shell, YAML, Makefile, Dockerfile
		if r == '#' && lx.atLineStartOrSpace() {
			lx.startLineComment(1)
			return true
		}
		if r == '"' || r == '\'' {
			p := lx.prev()
			if isIdent(p) && p != '$' {
				return false // apostrophe inside a word: don't, it's
			}
			if lx.lang == YAML && r == '\'' {
				lx.openString(`'`, false, false, true, false)
				return true
			}
			lx.openString(string(r), r == '"', false, false, false)
			return true
		}
	}
	return false
}

func (lx *lexer) onlySpaceBefore() bool {
	return strings.TrimSpace(lx.raw.String()) == ""
}

func (lx *lexer) startLineComment(markerLen int) {
	lx.raw.WriteString(string(lx.src[lx.i : lx.i+markerLen]))
	lx.i += markerLen
	lx.st = stLineComment
	lx.addComment("")
}

// ---- block comments ----

func (lx *lexer) stepBlock() {
	if lx.lang == Ruby {
		if lx.atLineStartOrSpace() && lx.prev() != ' ' && lx.prev() != '\t' && lx.hasPrefix("=end") {
			lx.raw.WriteString("=end")
			lx.i += len("=end")
			lx.st = stLineComment // rest of the =end line is ignored
			return
		}
		r := lx.src[lx.i]
		lx.raw.WriteRune(r)
		lx.com.WriteRune(r)
		lx.i++
		return
	}
	if lx.hasPrefix("*/") {
		lx.raw.WriteString("*/")
		lx.i += 2
		lx.blockDepth--
		if lx.blockDepth <= 0 || !lx.blockNest {
			lx.st = stCode
			// keep tokens on either side of the comment separate
			lx.code.WriteByte(' ')
			lx.nocom.WriteByte(' ')
		}
		return
	}
	if lx.blockNest && lx.hasPrefix("/*") {
		lx.raw.WriteString("/*")
		lx.i += 2
		lx.blockDepth++
		return
	}
	r := lx.src[lx.i]
	lx.raw.WriteRune(r)
	lx.com.WriteRune(r)
	lx.i++
}

// ---- strings ----

func (lx *lexer) openString(open string, esc, multi, doubling, template bool) {
	lx.openStringClose(open, open, esc, multi, doubling, template)
}

func (lx *lexer) openStringClose(open, close string, esc, multi, doubling, template bool) {
	lx.raw.WriteString(open)
	lx.emitCodeStr(open)
	lx.i += len([]rune(open))
	lx.st = stString
	lx.strClose = close
	lx.strEsc = esc
	lx.strMulti = multi
	lx.strDoubling = doubling
	lx.strTemplate = template
	lx.strBuf.Reset()
	lx.strStartLine = len(lx.out)
}

func (lx *lexer) endString() {
	s := lx.strBuf.String()
	if lx.strStartLine < len(lx.out) {
		lx.out[lx.strStartLine].Strings = append(lx.out[lx.strStartLine].Strings, s)
	} else {
		lx.strs = append(lx.strs, s)
	}
	lx.strBuf.Reset()
}

func (lx *lexer) stepString() {
	r := lx.src[lx.i]
	if lx.strEsc && r == '\\' && lx.i+1 < len(lx.src) && lx.src[lx.i+1] != '\n' {
		nx := lx.src[lx.i+1]
		lx.raw.WriteRune(r)
		lx.raw.WriteRune(nx)
		lx.nocom.WriteRune(r)
		lx.nocom.WriteRune(nx)
		lx.strBuf.WriteRune(r)
		lx.strBuf.WriteRune(nx)
		lx.i += 2
		return
	}
	if lx.strTemplate && r == '$' && lx.peek(1) == '{' {
		// interpolation: back to code until the matching }
		lx.raw.WriteString("${")
		lx.code.WriteString("${")
		lx.nocom.WriteString("${")
		lx.strBuf.WriteString("${")
		lx.i += 2
		lx.braceDepth++
		lx.tmplStack = append(lx.tmplStack, lx.braceDepth)
		lx.st = stCode
		lx.lastSig = '{'
		lx.lastWord = ""
		return
	}
	if lx.hasPrefix(lx.strClose) {
		if lx.strDoubling && lx.peekAfter(lx.strClose) == []rune(lx.strClose)[0] {
			// doubled delimiter is an escaped quote
			lx.raw.WriteString(lx.strClose + lx.strClose)
			lx.nocom.WriteString(lx.strClose + lx.strClose)
			lx.strBuf.WriteString(lx.strClose)
			lx.i += 2 * len([]rune(lx.strClose))
			return
		}
		lx.raw.WriteString(lx.strClose)
		lx.emitCodeStr(lx.strClose)
		lx.i += len([]rune(lx.strClose))
		lx.endString()
		lx.st = stCode
		lx.lastWord = ""
		return
	}
	lx.raw.WriteRune(r)
	lx.nocom.WriteRune(r)
	lx.strBuf.WriteRune(r)
	lx.i++
}

func (lx *lexer) peekAfter(s string) rune {
	return lx.peek(len([]rune(s)))
}

// ---- JS regex literals ----

func (lx *lexer) stepRegex() {
	r := lx.src[lx.i]
	switch {
	case r == '\\' && lx.i+1 < len(lx.src) && lx.src[lx.i+1] != '\n':
		s := string(lx.src[lx.i : lx.i+2])
		lx.raw.WriteString(s)
		lx.nocom.WriteString(s)
		lx.strBuf.WriteString(s)
		lx.i += 2
		return
	case r == '[':
		lx.reClass = true
	case r == ']':
		lx.reClass = false
	case r == '/' && !lx.reClass:
		lx.raw.WriteRune(r)
		lx.emitCode(r)
		lx.i++
		lx.st = stCode
		lx.lastWord = ""
		lx.lastSig = ')' // a regex is an operand; a following / is division
		return
	}
	lx.raw.WriteRune(r)
	lx.nocom.WriteRune(r)
	lx.strBuf.WriteRune(r)
	lx.i++
}
