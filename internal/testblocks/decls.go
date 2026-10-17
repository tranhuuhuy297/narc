package testblocks

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// Declaration-style frameworks mark test functions by name (Go TestX,
// XCTest testX) or with an attribute (@Test, #[test], [Fact]).

type declSpec struct {
	// decl finds function declarations. Named groups: name (required),
	// recv (Go receiver type), typ (Java/C# return type).
	decl *regexp.Regexp
	// byName reports a test by its name alone (Go, XCTest, PHPUnit, JUnit 3).
	byName func(name, typ, preamble string) bool
	// attr marks a test in the declaration's preamble (annotations).
	attr *regexp.Regexp
	// docTest marks a test in the comments before it (PHPUnit @test).
	docTest *regexp.Regexp
	// skip marks a disabled test (or class) in its preamble.
	skip *regexp.Regexp
	// skipBody matches a first statement that unconditionally skips.
	skipBody *regexp.Regexp
	// container finds classes/modules that qualify test names; group 1 is
	// the name.
	container *regexp.Regexp
	sep       string
	// keywords cannot be a method name or return type (Java, C#).
	keywords map[string]bool
	// need, when set, lists words one of which the first line of every
	// match of decl contains, and decl never needs more than a few lines
	// (see text.findAll); else decl runs on the whole file.
	need []string
}

var cLikeKeywords = set("new", "return", "else", "throw", "throws", "await", "case", "in", "is", "as", "yield",
	"goto", "typeof", "sizeof", "nameof", "using", "if", "while", "for", "foreach", "switch", "catch", "lock",
	"fixed", "do", "try", "finally", "when", "class", "interface", "extends", "implements", "assert", "package",
	"import", "operator", "where", "var", "let", "const", "default", "synchronized", "record", "enum", "struct")

// junitAttr marks JUnit 4/5, TestNG, jqwik, JUnit Pioneer (@RetryingTest,
// @CartesianTest), junit5-rerunner, Jazzer (@FuzzTest) and ArchUnit tests.
var junitAttr = regexp.MustCompile(`@(?:[\w.]+\.)?(?:Test|ParameterizedTest|RepeatedTest|TestFactory|TestTemplate|Property|Theory|Example|RetryingTest|RepeatedIfExceptionsTest|CartesianTest|FuzzTest|ArchTest)\b`)
var junitSkip = regexp.MustCompile(`@(?:[\w.]+\.)?(?:Disabled|Ignore)\b`)

var declSpecs = map[lang.Lang]*declSpec{
	lang.Go: {
		decl:     regexp.MustCompile(`(?m)^func\s+(?:\(\s*(?:\w+\s+)?\*?\s*(?P<recv>\w+)(?:\[[^\]]*\])?\s*\)\s*)?(?P<name>\w+)`),
		byName:   func(name, _, _ string) bool { return goTestName(name) },
		skipBody: regexp.MustCompile(`^(?:t|b|f)\s*\.\s*Skip(?:f|Now)?\s*\(`),
		sep:      ".",
		need:     []string{"func"},
	},
	lang.Rust: {
		decl:      regexp.MustCompile(`\bfn\s+(?P<name>\w+)`),
		attr:      regexp.MustCompile(`#\[\s*(?:(?:\w+::)*test|rstest|test_case|test_matrix|wasm_bindgen_test|quickcheck|proptest)\b`),
		skip:      regexp.MustCompile(`#\[\s*ignore\b`),
		container: regexp.MustCompile(`\bmod\s+(\w+)`),
		sep:       "::",
		need:      []string{"fn"},
	},
	lang.Java: {
		decl: regexp.MustCompile(`(?m)(?:^|[\s;{}])(?P<typ>[\w$]+(?:<[^;{}()]*?>)?(?:\[\])*)\s+(?P<name>[\w$]+)\s*\(`),
		byName: func(name, typ, pre string) bool { // JUnit 3
			return strings.HasPrefix(name, "test") && typ == "void" && strings.Contains(pre, "public")
		},
		attr:      junitAttr,
		skip:      junitSkip,
		container: regexp.MustCompile(`\b(?:class|interface|enum|record)\s+(\w+)`),
		sep:       ".",
		keywords:  cLikeKeywords,
	},
	lang.Kotlin: {
		decl:      regexp.MustCompile("(?:^|[^\\w])fun\\s+(?:<[^>]*>\\s*)?(?:[\\w.]+\\.)?(?P<name>\\w+|``)"),
		attr:      junitAttr,
		skip:      junitSkip,
		container: regexp.MustCompile(`\b(?:class|object|interface)\s+(\w+)`),
		sep:       ".",
		need:      []string{"fun"},
	},
	lang.CSharp: {
		decl: regexp.MustCompile(`(?m)(?:^|[\s;{}])(?P<typ>[\w$]+(?:<[^;{}()]*?>)?(?:\[\])*\??)\s+(?P<name>[\w$]+)\s*\(`),
		// xUnit [Fact]/[Theory] and their variants ([RetryFact], [SkippableFact],
		// [WpfFact], ...), NUnit, MSTest and Unity.
		attr:      regexp.MustCompile(`(?:\[|,)\s*(?:[\w.]+\.)?(?:\w*Fact|\w*Theory|Test|TestMethod|DataTestMethod|TestCase|TestCaseSource|UnityTest)\s*[\](,]`),
		skip:      regexp.MustCompile(`\bSkip\s*=|(?:\[|,)\s*Ignore\b`),
		container: regexp.MustCompile(`\b(?:class|struct|record)\s+(\w+)`),
		sep:       ".",
		keywords:  cLikeKeywords,
	},
	lang.Swift: {
		decl:      regexp.MustCompile(`\bfunc\s+(?P<name>\w+)`),
		byName:    func(name, _, _ string) bool { return strings.HasPrefix(name, "test") },
		attr:      regexp.MustCompile(`@Test\b`),
		skip:      regexp.MustCompile(`\.disabled\b`),
		skipBody:  regexp.MustCompile(`^throw\s+XCTSkip\b`),
		container: regexp.MustCompile(`\b(?:class|struct|extension|actor|enum)\s+(\w+)`),
		sep:       ".",
		need:      []string{"func"},
	},
	lang.PHP: {
		decl:      regexp.MustCompile(`\bfunction\s+(?P<name>\w+)`),
		byName:    func(name, _, _ string) bool { return strings.HasPrefix(name, "test") },
		attr:      regexp.MustCompile(`#\[\s*\\?(?:\w+\\)*Test\s*[\](]`),
		docTest:   regexp.MustCompile(`@test\b`),
		skipBody:  regexp.MustCompile(`^(?:\$this\s*->|self\s*::|static\s*::)\s*markTest(?:Skipped|Incomplete)\s*\(`),
		container: regexp.MustCompile(`\b(?:class|trait)\s+(\w+)`),
		sep:       "::",
		need:      []string{"function"},
	},
}

// goTestName reports whether name is a function `go test` runs: TestXxx,
// BenchmarkXxx, FuzzXxx or ExampleXxx, where Xxx does not start with a
// lower-case letter. TestMain is setup, not a test.
func goTestName(name string) bool {
	if name == "TestMain" {
		return false
	}
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if !strings.HasPrefix(name, p) {
			continue
		}
		rest := name[len(p):]
		if rest == "" {
			return true
		}
		r, _ := utf8.DecodeRuneInString(rest)
		return !unicode.IsLower(r)
	}
	return false
}

// findContainers returns the classes/modules of a file.
func findContainers(t *text, ds *declSpec) []scope {
	if ds.container == nil {
		return nil
	}
	var out []scope
	for _, m := range ds.container.FindAllStringSubmatchIndex(t.code, -1) {
		brace, ok := t.braceAfterHeader(m[1])
		if !ok {
			continue
		}
		end, _ := t.closeOf(brace)
		pre, _ := t.preamble(m[0])
		skipped := ds.skip != nil && ds.skip.MatchString(pre)
		out = append(out, scope{name: t.code[m[2]:m[3]], from: brace, to: end, skipped: skipped})
	}
	sortScopes(out)
	return out
}

// braceAfterHeader finds the '{' opening the body of a class/module whose
// header ends at pos: on the same line (after any parenthesized
// constructor) or alone at the start of the next line.
func (t *text) braceAfterHeader(pos int) (int, bool) {
	depth := 0
	for i := pos; i < len(t.code) && i < pos+500; i++ {
		switch c := t.code[i]; c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '{':
			if depth <= 0 {
				return i, true
			}
		case ';', '}', '=':
			if depth <= 0 {
				return 0, false
			}
		case '\n':
			if depth > 0 {
				continue
			}
			j := t.skipSpace(i + 1)
			if j < len(t.code) && t.code[j] == '{' && t.lineOf(j) == t.lineOf(i)+1 {
				return j, true
			}
			return 0, false
		}
	}
	return 0, false
}

// preamble returns the annotations and modifiers before a declaration at
// pos: the code back to the previous ';', '{' or '}' outside brackets.
func (t *text) preamble(pos int) (string, int) {
	depth := 0
	i := pos - 1
loop:
	for ; i >= 0 && pos-i < 4000; i-- {
		switch t.code[i] {
		case ')', ']':
			depth++
		case '(', '[':
			if depth == 0 {
				break loop
			}
			depth--
		case '{', '}', ';':
			if depth == 0 {
				break loop
			}
		}
	}
	return t.code[i+1 : pos], i + 1
}

// findBody looks for what follows a parameter list: the '{' of a body,
// "=>" (C# expression body), '=' (Kotlin expression body), or ';'/'}'
// (no body). It returns -1 when the input ends first.
func (t *text) findBody(from int) (int, byte) {
	depth := 0
	for i := from; i < len(t.code) && i < from+2000; i++ {
		switch c := t.code[i]; c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '{', ';', '}':
			if depth <= 0 {
				return i, c
			}
		case '=':
			if depth <= 0 {
				if i+1 < len(t.code) && t.code[i+1] == '>' {
					return i, '>'
				}
				if i+1 < len(t.code) && t.code[i+1] == '=' {
					i++
					continue
				}
				return i, '='
			}
		}
	}
	return -1, 0
}

// exprEnd returns the end of an expression body starting at pos: a block
// opened on the same line (Kotlin `= runTest { ... }`), else the first ';'
// or the end of the line outside brackets.
func (t *text) exprEnd(pos int, kotlin bool) (int, bool) {
	depth := 0
	for i := pos; i < len(t.code); i++ {
		switch c := t.code[i]; c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '{':
			if kotlin && depth <= 0 {
				return t.closeOf(i)
			}
			depth++
		case '}':
			depth--
		case ';':
			if depth <= 0 {
				return i, true
			}
		case '\n':
			if kotlin && depth <= 0 {
				return i - 1, true
			}
		}
	}
	return len(t.code) - 1, false
}

func findDecls(t *text, ds *declSpec, classes []scope) []raw {
	var out []raw
	ni := ds.decl.SubexpIndex("name")
	ri := ds.decl.SubexpIndex("recv")
	ti := ds.decl.SubexpIndex("typ")
	group := func(m []int, gi int) (string, int) {
		if gi < 0 || m[2*gi] < 0 {
			return "", -1
		}
		return t.code[m[2*gi]:m[2*gi+1]], m[2*gi]
	}
	var ms [][]int
	if ds.need != nil {
		ms = t.findAll(ds.decl, ds.need)
	} else {
		ms = ds.decl.FindAllStringSubmatchIndex(t.code, -1)
	}
	for _, m := range ms {
		name, ns := group(m, ni)
		if ns < 0 {
			continue
		}
		ne := ns + len(name)
		recv, _ := group(m, ri)
		typ, ts := group(m, ti)
		if ds.keywords != nil && (ds.keywords[name] || ds.keywords[typ]) {
			continue
		}
		if strings.HasPrefix(name, "`") {
			name = t.literal(ns) // Kotlin: fun `rejects expired tokens`()
		}
		p := t.skipSpace(ne)
		if p < len(t.code) && t.code[p] == '<' { // generic parameters
			if q, ok := t.closeOf(p); ok {
				p = t.skipSpace(q + 1)
			}
		}
		if p >= len(t.code) || t.code[p] != '(' {
			continue
		}
		declStart := m[0]
		if ts >= 0 {
			declStart = ts
		}
		for declStart < ns && isSpace(t.code[declStart]) {
			declStart++
		}
		pre, preStart := t.preamble(declStart)
		attrAt := -1
		if ds.attr != nil {
			if loc := ds.attr.FindStringIndex(pre); loc != nil {
				attrAt = preStart + loc[0]
			}
		}
		header := t.lineOf(ns)
		isTest := attrAt >= 0 || (ds.byName != nil && ds.byName(name, typ, pre))
		if !isTest && ds.docTest != nil {
			for i := t.lineOf(preStart); i < header; i++ {
				if ds.docTest.MatchString(t.lines[i].Comment) {
					isTest = true
				}
			}
		}
		if !isTest {
			continue
		}
		pc, ok := t.closeOf(p)
		if !ok {
			out = append(out, raw{name: name, bare: name, start: header, header: header, end: len(t.lines) - 1, open: true})
			continue
		}
		body, kind := t.findBody(pc + 1)
		end, closed := 0, true
		switch kind {
		case '{':
			end, closed = t.closeOf(body)
		case '>':
			end, closed = t.exprEnd(body+2, false)
		case '=':
			end, closed = t.exprEnd(body+1, t.lang == lang.Kotlin)
		case 0:
			end, closed = len(t.code)-1, false // input ends inside the signature
		default:
			continue // abstract or interface method: no body
		}
		start := header
		if attrAt >= 0 {
			start = t.attrStart(t.lineOf(attrAt), header)
		}
		qname, skipped := qualify(classes, ns, name, ds.sep)
		if recv != "" {
			qname = recv + ds.sep + name
		}
		if ds.skip != nil && ds.skip.MatchString(pre) {
			skipped = true
		}
		if ds.skipBody != nil && kind == '{' {
			rest := t.code[body+1 : min(len(t.code), body+400)]
			if ds.skipBody.MatchString(strings.TrimLeft(rest, " \t\r\n")) {
				skipped = true
			}
		}
		out = append(out, raw{name: qname, bare: name, start: start, header: header, end: t.lineOf(end),
			skipped: skipped, open: !closed})
	}
	return out
}

// attrStart extends the start of a test from its test attribute's line up
// over adjacent attribute lines (@DisplayName, #[tokio::test], [Trait]).
func (t *text) attrStart(attrLine, header int) int {
	if attrLine > header {
		return header
	}
	start := attrLine
	for start > 0 {
		c := strings.TrimSpace(t.lines[start-1].Code)
		attr := strings.HasPrefix(c, "@") || strings.HasPrefix(c, "#[") || strings.HasPrefix(c, "[")
		if !attr || strings.ContainsAny(c, "{};") { // `@Suite struct X {` is not an attribute line
			break
		}
		start--
	}
	return start
}
