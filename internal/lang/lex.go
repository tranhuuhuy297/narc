package lang

import (
	"strings"
	"unicode/utf8"
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
//
// The lexer works on bytes: every delimiter it cares about is ASCII, and
// UTF-8 continuation bytes are never mistaken for one. Lines that contain
// neither comments nor string contents share one string for Raw, Code and
// NoComment, which keeps lexing large files cheap.
func Lex(l Lang, src string) []LexedLine { return LexSome(l, src, nil) }

// LexSome is Lex for callers that only need some lines. The whole source is
// still scanned (a line's meaning depends on everything before it), but
// lines for which keep(lineNumber) is false (1-based) only get Raw filled
// in. A nil keep keeps every line.
func LexSome(l Lang, src string, keep func(line int) bool) []LexedLine {
	if src == "" {
		return nil
	}
	if strings.IndexByte(src, '\r') >= 0 {
		src = strings.ReplaceAll(src, "\r\n", "\n")
	}
	lx := &lexer{lang: l, fam: l.family(), src: src, pure: true, keep: keep}
	lx.out = make([]LexedLine, 0, strings.Count(src, "\n")+1)
	lx.keepLine = keep == nil || keep(1)
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
	src  string
	i    int

	out       []LexedLine
	lineStart int
	keep      func(int) bool
	keepLine  bool // build output for the current line
	strKeep   bool // the current string literal started on a kept line
	// pure means everything on the current line so far is code, so Code
	// and NoComment equal the raw text and nothing has been copied yet.
	pure           bool
	code, nocom    strings.Builder
	com            strings.Builder
	strs           []string
	pendingContent bool

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

	lastSig  byte   // last significant code byte on the current line
	lastWord string // last identifier-like word in code
}

func (lx *lexer) peek(off int) byte {
	j := lx.i + off
	if j < 0 || j >= len(lx.src) {
		return 0
	}
	return lx.src[j]
}

func (lx *lexer) hasPrefix(s string) bool { return strings.HasPrefix(lx.src[lx.i:], s) }

// impure materializes the code seen so far on this line, because something
// that is not code (a comment, a string body) is about to follow.
func (lx *lexer) impure() {
	if !lx.pure || !lx.keepLine {
		lx.pure = false
		return
	}
	seg := lx.src[lx.lineStart:lx.i]
	lx.code.WriteString(seg)
	lx.nocom.WriteString(seg)
	lx.pure = false
}

// emitCode records src[a:b] as code.
func (lx *lexer) emitCode(a, b int) {
	if !lx.pure && lx.keepLine {
		s := lx.src[a:b]
		lx.code.WriteString(s)
		lx.nocom.WriteString(s)
	}
	for k := b - 1; k >= a; k-- {
		if c := lx.src[k]; c != ' ' && c != '\t' {
			lx.lastSig = c
			break
		}
	}
}

// emitSynthetic adds text to Code and NoComment that is not in the source.
func (lx *lexer) emitSynthetic(s string) {
	lx.impure()
	if !lx.keepLine {
		return
	}
	lx.code.WriteString(s)
	lx.nocom.WriteString(s)
}

// emitStringBody records src[a:b] as string contents.
func (lx *lexer) emitStringBody(a, b int) {
	if a >= b {
		return
	}
	lx.impure()
	s := lx.src[a:b]
	if lx.keepLine {
		lx.nocom.WriteString(s)
	}
	if lx.strKeep {
		lx.strBuf.WriteString(s)
	}
}

func (lx *lexer) startComment() {
	lx.impure()
	if lx.keepLine && lx.com.Len() > 0 {
		lx.com.WriteByte(' ')
	}
}

func (lx *lexer) flushLine() {
	raw := lx.src[lx.lineStart:lx.i]
	ll := LexedLine{Raw: raw, Strings: lx.strs}
	if !lx.keepLine {
		ll.Strings = nil
		lx.code.Reset()
		lx.nocom.Reset()
		lx.com.Reset()
	} else if lx.pure {
		ll.Code, ll.NoComment = raw, raw
	} else {
		ll.Code, ll.NoComment = lx.code.String(), lx.nocom.String()
		lx.code.Reset()
		lx.nocom.Reset()
	}
	if lx.com.Len() > 0 {
		ll.Comment = strings.TrimSpace(lx.com.String())
		lx.com.Reset()
	}
	lx.out = append(lx.out, ll)
	lx.strs = nil
	lx.pure = true
	lx.lastSig = 0
	lx.lastWord = ""
	lx.keepLine = lx.keep == nil || lx.keep(len(lx.out)+1)
}

func (lx *lexer) run() {
	for lx.i < len(lx.src) {
		if lx.src[lx.i] == '\n' {
			lx.newline()
			lx.i++
			lx.lineStart = lx.i
			continue
		}
		lx.pendingContent = true
		switch lx.st {
		case stCode:
			lx.stepCode()
		case stLineComment:
			j := lx.i
			for j < len(lx.src) && lx.src[j] != '\n' {
				j++
			}
			if lx.keepLine {
				lx.com.WriteString(lx.src[lx.i:j])
			}
			lx.i = j
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
		continued := lx.strEsc && lx.i > 0 && lx.src[lx.i-1] == '\\' && (lx.i < 2 || lx.src[lx.i-2] != '\\')
		if lx.strMulti || continued {
			if lx.strKeep {
				lx.strBuf.WriteByte('\n')
			}
		} else {
			lx.endString()
			lx.st = stCode
		}
	case stRegex:
		lx.st = stCode
	}
	lx.flushLine()
	lx.pendingContent = false
	if lx.st != stCode {
		// The next line starts inside a comment or string: it is not pure.
		lx.pure = false
	}
}

// ---- code state ----

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (lx *lexer) prev() byte { return lx.peek(-1) }

func (lx *lexer) atLineStartOrSpace() bool {
	p := lx.prev()
	return p == 0 || p == '\n' || p == ' ' || p == '\t'
}

func (lx *lexer) stepCode() {
	c := lx.src[lx.i]
	switch lx.fam {
	case famC:
		if lx.stepCodeC(c) {
			return
		}
	case famHash:
		if lx.stepCodeHash(c) {
			return
		}
	}
	if isIdentByte(c) {
		j := lx.i
		for j < len(lx.src) && isIdentByte(lx.src[j]) {
			j++
		}
		w := lx.src[lx.i:j]
		if lx.lang == Rust && (w == "r" || w == "br") && lx.rustRawString(j) {
			return
		}
		lx.emitCode(lx.i, j)
		lx.lastWord = w
		lx.i = j
		return
	}
	if c == ' ' || c == '\t' {
		j := lx.i
		for j < len(lx.src) && (lx.src[j] == ' ' || lx.src[j] == '\t') {
			j++
		}
		lx.emitCode(lx.i, j)
		lx.i = j
		return
	}
	if c == '{' {
		lx.braceDepth++
	}
	if c == '}' {
		if n := len(lx.tmplStack); n > 0 && lx.tmplStack[n-1] == lx.braceDepth {
			// end of ${...} interpolation: resume the enclosing template
			// literal (an inner string may have replaced the settings)
			lx.tmplStack = lx.tmplStack[:n-1]
			lx.braceDepth--
			lx.emitCode(lx.i, lx.i+1)
			lx.i++
			lx.st = stString
			lx.strClose, lx.strEsc, lx.strMulti, lx.strDoubling, lx.strTemplate = "`", true, true, false, true
			lx.strBuf.Reset()
			lx.strStartLine = len(lx.out)
			lx.strKeep = lx.keepLine
			return
		}
		lx.braceDepth--
	}
	lx.emitCode(lx.i, lx.i+1)
	lx.lastWord = ""
	lx.i++
}

// rustRawString handles r"..." / r#"..."# / br"..." whose prefix word ends
// at j. It returns false if this is not a raw string.
func (lx *lexer) rustRawString(j int) bool {
	k := j
	for k < len(lx.src) && lx.src[k] == '#' {
		k++
	}
	if k >= len(lx.src) || lx.src[k] != '"' {
		return false
	}
	lx.emitCode(lx.i, k)
	hashes := lx.src[j:k]
	lx.i = k
	lx.openStringClose(`"`, `"`+hashes, false, true, false, false)
	return true
}

func (lx *lexer) stepCodeC(c byte) bool {
	switch {
	case c == '/' && lx.peek(1) == '/':
		lx.startComment()
		lx.i += 2
		lx.st = stLineComment
		return true
	case c == '/' && lx.peek(1) == '*':
		lx.startComment()
		lx.i += 2
		lx.st = stBlockComment
		lx.blockDepth = 1
		lx.blockNest = lx.lang == Rust || lx.lang == Swift || lx.lang == Scala || lx.lang == Dart || lx.lang == Kotlin
		return true
	case c == '#' && lx.lang == PHP && lx.peek(1) != '[':
		lx.startComment()
		lx.i++
		lx.st = stLineComment
		return true
	case c == '/' && lx.lang.IsJSLike() && lx.regexAllowed():
		lx.emitCode(lx.i, lx.i+1)
		lx.i++
		lx.st = stRegex
		lx.reClass = false
		return true
	}
	switch c {
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
			if lx.peek(1) == '\\' {
				lx.openString(`'`, true, false, false, false)
				return true
			}
			if lx.i+1 < len(lx.src) {
				_, size := utf8.DecodeRuneInString(lx.src[lx.i+1:])
				if lx.src[lx.i+1] != '\n' && lx.peek(1+size) == '\'' {
					lx.openString(`'`, true, false, false, false)
					return true
				}
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

func (lx *lexer) hasTriple(q byte) bool {
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

func (lx *lexer) stepCodeHash(c byte) bool {
	switch lx.lang {
	case Python:
		if c == '#' {
			lx.startLineComment()
			return true
		}
		if c == '"' || c == '\'' {
			if lx.hasTriple(c) {
				lx.openString(strings.Repeat(string(c), 3), true, true, false, false)
				return true
			}
			lx.openString(string(c), true, false, false, false)
			return true
		}
	case Ruby, Elixir:
		if c == '#' {
			lx.startLineComment()
			return true
		}
		if lx.lang == Ruby && c == '=' && lx.i == lx.lineStart && lx.hasPrefix("=begin") {
			lx.startComment()
			lx.i += len("=begin")
			lx.st = stBlockComment
			lx.blockDepth = 1
			lx.blockNest = false
			return true
		}
		if lx.lang == Elixir && lx.hasTriple('"') {
			lx.openString(`"""`, true, true, false, false)
			return true
		}
		if c == '"' || c == '\'' {
			if c == '\'' && lx.lang == Ruby && isIdentByte(lx.prev()) {
				return false
			}
			lx.openString(string(c), c == '"' || lx.lang == Ruby, false, false, false)
			return true
		}
	case TOML:
		if c == '#' {
			lx.startLineComment()
			return true
		}
		if c == '"' || c == '\'' {
			if lx.hasTriple(c) {
				lx.openString(strings.Repeat(string(c), 3), c == '"', true, false, false)
				return true
			}
			lx.openString(string(c), c == '"', false, false, false)
			return true
		}
	case INI:
		if (c == '#' || c == ';') && strings.TrimSpace(lx.src[lx.lineStart:lx.i]) == "" {
			lx.startLineComment()
			return true
		}
		if c == '#' && lx.atLineStartOrSpace() {
			lx.startLineComment()
			return true
		}
	default: // Shell, YAML, Makefile, Dockerfile
		if c == '#' && lx.atLineStartOrSpace() {
			lx.startLineComment()
			return true
		}
		if c == '"' || c == '\'' {
			p := lx.prev()
			if isIdentByte(p) && p != '$' {
				return false // apostrophe inside a word: don't, it's
			}
			if lx.lang == YAML && c == '\'' {
				lx.openString(`'`, false, false, true, false)
				return true
			}
			lx.openString(string(c), c == '"', false, false, false)
			return true
		}
	}
	return false
}

func (lx *lexer) startLineComment() {
	lx.startComment()
	lx.i++
	lx.st = stLineComment
}

// ---- block comments ----

func (lx *lexer) stepBlock() {
	if lx.lang == Ruby {
		if lx.i == lx.lineStart && lx.hasPrefix("=end") {
			lx.i += len("=end")
			lx.st = stLineComment // rest of the =end line is ignored
			return
		}
		j := lx.i
		for j < len(lx.src) && lx.src[j] != '\n' {
			j++
		}
		lx.impure()
		if lx.keepLine {
			lx.com.WriteString(lx.src[lx.i:j])
		}
		lx.i = j
		return
	}
	lx.impure()
	j := lx.i
	for j < len(lx.src) {
		c := lx.src[j]
		if c == '\n' || (c == '*' && j+1 < len(lx.src) && lx.src[j+1] == '/') ||
			(lx.blockNest && c == '/' && j+1 < len(lx.src) && lx.src[j+1] == '*') {
			break
		}
		j++
	}
	if lx.keepLine {
		lx.com.WriteString(lx.src[lx.i:j])
	}
	lx.i = j
	switch {
	case lx.hasPrefix("*/"):
		lx.i += 2
		lx.blockDepth--
		if lx.blockDepth <= 0 || !lx.blockNest {
			lx.st = stCode
			// keep tokens on either side of the comment separate
			lx.emitSynthetic(" ")
		}
	case lx.blockNest && lx.hasPrefix("/*"):
		lx.i += 2
		lx.blockDepth++
	}
}

// ---- strings ----

func (lx *lexer) openString(open string, esc, multi, doubling, template bool) {
	lx.openStringClose(open, open, esc, multi, doubling, template)
}

func (lx *lexer) openStringClose(open, close string, esc, multi, doubling, template bool) {
	lx.emitCode(lx.i, lx.i+len(open))
	lx.i += len(open)
	lx.st = stString
	lx.strClose = close
	lx.strEsc = esc
	lx.strMulti = multi
	lx.strDoubling = doubling
	lx.strTemplate = template
	lx.strBuf.Reset()
	lx.strStartLine = len(lx.out)
	lx.strKeep = lx.keepLine
}

func (lx *lexer) endString() {
	if !lx.strKeep {
		lx.strBuf.Reset()
		return
	}
	s := lx.strBuf.String()
	if lx.strStartLine < len(lx.out) {
		lx.out[lx.strStartLine].Strings = append(lx.out[lx.strStartLine].Strings, s)
	} else {
		lx.strs = append(lx.strs, s)
	}
	lx.strBuf.Reset()
}

func (lx *lexer) stepString() {
	closeFirst := lx.strClose[0]
	j := lx.i
	for j < len(lx.src) {
		c := lx.src[j]
		if c == '\n' || c == closeFirst || (lx.strEsc && c == '\\') || (lx.strTemplate && c == '$') {
			break
		}
		j++
	}
	lx.emitStringBody(lx.i, j)
	lx.i = j
	if lx.i >= len(lx.src) || lx.src[lx.i] == '\n' {
		return
	}
	c := lx.src[lx.i]
	switch {
	case lx.strEsc && c == '\\':
		if lx.i+1 < len(lx.src) && lx.src[lx.i+1] != '\n' {
			_, size := utf8.DecodeRuneInString(lx.src[lx.i+1:])
			lx.emitStringBody(lx.i, lx.i+1+size)
			lx.i += 1 + size
		} else {
			lx.emitStringBody(lx.i, lx.i+1)
			lx.i++
		}
	case lx.strTemplate && c == '$' && lx.peek(1) == '{':
		// interpolation: back to code until the matching }
		lx.impure()
		if lx.keepLine {
			lx.code.WriteString("${")
			lx.nocom.WriteString("${")
		}
		if lx.strKeep {
			lx.strBuf.WriteString("${")
		}
		lx.i += 2
		lx.braceDepth++
		lx.tmplStack = append(lx.tmplStack, lx.braceDepth)
		lx.st = stCode
		lx.lastSig = '{'
		lx.lastWord = ""
	case lx.hasPrefix(lx.strClose):
		n := len(lx.strClose)
		if lx.strDoubling && lx.peek(n) == closeFirst {
			// doubled delimiter is an escaped quote
			lx.impure()
			if lx.keepLine {
				lx.nocom.WriteString(lx.src[lx.i : lx.i+2*n])
			}
			if lx.strKeep {
				lx.strBuf.WriteString(lx.strClose)
			}
			lx.i += 2 * n
			return
		}
		lx.emitCode(lx.i, lx.i+n)
		lx.i += n
		lx.endString()
		lx.st = stCode
		lx.lastWord = ""
	default:
		// a lone '$' or a partial closing delimiter (e.g. one quote of """)
		lx.emitStringBody(lx.i, lx.i+1)
		lx.i++
	}
}

// ---- JS regex literals ----

func (lx *lexer) stepRegex() {
	j := lx.i
	for j < len(lx.src) {
		c := lx.src[j]
		if c == '\n' || c == '\\' || c == '[' || c == ']' || c == '/' {
			break
		}
		j++
	}
	lx.regexBody(lx.i, j)
	lx.i = j
	if lx.i >= len(lx.src) || lx.src[lx.i] == '\n' {
		return
	}
	switch c := lx.src[lx.i]; {
	case c == '\\':
		end := lx.i + 1
		if end < len(lx.src) && lx.src[end] != '\n' {
			end++
		}
		lx.regexBody(lx.i, end)
		lx.i = end
	case c == '[':
		lx.reClass = true
		lx.regexBody(lx.i, lx.i+1)
		lx.i++
	case c == ']':
		lx.reClass = false
		lx.regexBody(lx.i, lx.i+1)
		lx.i++
	case c == '/' && !lx.reClass:
		lx.emitCode(lx.i, lx.i+1)
		lx.i++
		lx.st = stCode
		lx.lastWord = ""
		lx.lastSig = ')' // a regex is an operand; a following / is division
	default:
		lx.regexBody(lx.i, lx.i+1)
		lx.i++
	}
}

// regexBody records regex contents: kept in NoComment, removed from Code.
func (lx *lexer) regexBody(a, b int) {
	if a >= b {
		return
	}
	lx.impure()
	if lx.keepLine {
		lx.nocom.WriteString(lx.src[a:b])
	}
}
