package testblocks

import (
	"regexp"
	"strings"
)

// Python blocks are delimited by indentation. pytest collects test*
// functions at module level and test* methods of Test* classes; unittest
// collects test* methods of TestCase subclasses. Both are covered by
// "test* at module level, or directly inside a class that is named like a
// test class or has a base class" (the base may be TestCase). A class
// such as `class Refunds:` is not collected, so its methods are not tests.

var (
	pyDefRe    = regexp.MustCompile(`^(?:async\s+)?def\s+(\w+)`)
	pyClassRe  = regexp.MustCompile(`^class\s+(\w+)\s*(\(.*)?`)
	pySkipDeco = regexp.MustCompile(`^@\s*(?:pytest\s*\.\s*mark\s*\.\s*skip|(?:unittest\s*\.\s*)?skip)\s*(?:\(|$)`)
	pySkipBody = regexp.MustCompile(`^(?:pytest\s*\.\s*skip\s*\(|self\s*\.\s*skipTest\s*\(|raise\s+(?:unittest\s*\.\s*)?SkipTest\b)`)
)

func findPython(t *text) []raw {
	type pyScope struct {
		indent  int
		kind    byte // 'c' class, 'f' function, 't' test
		name    string
		collect bool // a class whose test* methods are tests
		test    int  // index in out, for tests
		skipped bool
		last    int // last line with content inside the scope
	}
	var (
		out       []raw
		stack     []pyScope
		depth     int  // brackets left open by previous lines
		backslash bool // previous line ended with a line continuation
		decoStart = -1
		decoSkip  bool
		firstStmt = -1 // test awaiting its first statement (for skips)
	)
	closeTop := func(eof bool) {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.kind == 't' {
			out[s.test].end = max(s.last, out[s.test].header)
			out[s.test].open = eof
		}
	}
	touch := func(i int) {
		for k := range stack {
			stack[k].last = i
		}
	}
	for i, ll := range t.lines {
		code := ll.Code
		trimmed := strings.TrimSpace(code)
		continuation := depth > 0 || backslash || startsInString(ll.Raw, code)
		depth += bracketDelta(code)
		if depth < 0 {
			depth = 0
		}
		backslash = strings.HasSuffix(strings.TrimRight(code, " \t"), `\`)
		if continuation {
			if strings.TrimSpace(ll.Raw) != "" {
				touch(i)
			}
			continue
		}
		if trimmed == "" {
			continue // blank or comment-only: does not affect blocks
		}
		ind := indentWidth(code)
		for len(stack) > 0 && stack[len(stack)-1].indent >= ind {
			closeTop(false)
		}
		touch(i)
		if firstStmt >= 0 {
			if n := len(stack); n > 0 && stack[n-1].kind == 't' && stack[n-1].test == firstStmt {
				if strings.Trim(trimmed, `"'`) == "" {
					// a docstring: keep looking for the first statement
				} else {
					if pySkipBody.MatchString(trimmed) {
						out[firstStmt].skipped = true
					}
					firstStmt = -1
				}
			} else {
				firstStmt = -1
			}
		}
		if strings.HasPrefix(trimmed, "@") {
			if decoStart < 0 {
				decoStart = i
			}
			decoSkip = decoSkip || pySkipDeco.MatchString(trimmed)
			continue
		}
		start, skipped := i, decoSkip
		if decoStart >= 0 {
			start = decoStart
		}
		decoStart, decoSkip = -1, false
		inherited := false
		var classes []string
		for _, s := range stack {
			if s.kind == 'c' {
				classes = append(classes, s.name)
			}
			inherited = inherited || s.skipped
		}
		if m := pyClassRe.FindStringSubmatch(trimmed); m != nil {
			stack = append(stack, pyScope{indent: ind, kind: 'c', name: m[1], collect: pyTestClass(m[1], m[2]),
				skipped: skipped || inherited, last: i})
			continue
		}
		m := pyDefRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		name := m[1]
		atTop := len(stack) == 0 || stack[len(stack)-1].kind == 'c' && stack[len(stack)-1].collect
		if !atTop || !strings.HasPrefix(name, "test") {
			stack = append(stack, pyScope{indent: ind, kind: 'f', last: i})
			continue
		}
		out = append(out, raw{name: strings.Join(append(classes, name), "::"), bare: name,
			start: start, header: i, end: i, skipped: skipped || inherited})
		stack = append(stack, pyScope{indent: ind, kind: 't', test: len(out) - 1, last: i})
		if !oneLineDef(code) {
			firstStmt = len(out) - 1
		}
	}
	for len(stack) > 0 {
		closeTop(true)
	}
	return out
}

// pyTestClass reports whether a class's test* methods are collected:
// pytest's Test* classes (and the common *Test/*Tests naming), or any
// class with a base class, which may be unittest.TestCase.
// bases is the rest of the class line from its opening parenthesis.
func pyTestClass(name, bases string) bool {
	if strings.HasPrefix(name, "Test") || strings.HasSuffix(name, "Test") || strings.HasSuffix(name, "Tests") {
		return true
	}
	if bases == "" {
		return false
	}
	end := strings.IndexByte(bases, ')')
	if end < 0 {
		return true // the base list continues on the next lines
	}
	b := strings.TrimSpace(bases[1:end])
	return b != "" && b != "object"
}

// oneLineDef reports a def whose body follows the colon on the same line.
func oneLineDef(code string) bool {
	c := strings.TrimRight(code, " \t")
	return !strings.HasSuffix(c, ":") && !strings.HasSuffix(c, "(") && !strings.HasSuffix(c, ",")
}

// startsInString reports a line that begins inside a multi-line string:
// the lexer removed its leading characters from Code.
func startsInString(raw, code string) bool {
	return strings.TrimSpace(code) != "" && leadingSpace(raw) != leadingSpace(code)
}

func leadingSpace(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}

func indentWidth(code string) int {
	w := 0
	for _, c := range code {
		switch c {
		case ' ':
			w++
		case '\t':
			w += 8 - w%8
		default:
			return w
		}
	}
	return w
}

func bracketDelta(code string) int {
	d := 0
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '(', '[', '{':
			d++
		case ')', ']', '}':
			d--
		}
	}
	return d
}
