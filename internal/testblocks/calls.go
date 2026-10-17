package testblocks

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// Call-style frameworks declare tests by calling a function with a name
// and a body: it('name', () => {...}), test("name") { ... }, TEST(A, B) {}.

type callKind int

const (
	callNone callKind = iota
	callTest
	callSuite
)

type callSpec struct {
	// head finds candidate calls; group 1 is the callee. need lists words
	// one of which every match contains on its first line.
	head *regexp.Regexp
	need []string
	// classify maps the callee and its member chain (test.describe.serial
	// is "test" + [describe serial]) to a kind and an unconditional skip.
	classify func(callee string, chain []string) (callKind, bool)
	// paramMods are chain members that take their own argument list before
	// the call that declares the test: it.each(table)('name', fn).
	paramMods map[string]bool
	// trailing: the body is a block after the call, test("x") { ... }.
	trailing bool
	// requireLit: the first argument must be a string literal.
	requireLit bool
	sep        string
	// name overrides the default naming (the first argument).
	name func(t *text, args []span) (name, bare string, skipped bool)
	// skipArg reports a skip passed as an argument (Dart skip: true).
	skipArg *regexp.Regexp
	// skipSuffix matches a skip chained after the call (Pest ->skip()).
	skipSuffix *regexp.Regexp
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

var jsCalls = &callSpec{
	head:      regexp.MustCompile(`(?m)(?:^|[^\w$.>:#@])([xf]?(?:it|test|specify|describe|context|suite)|Deno\s*\.\s*test)\b`),
	need:      []string{"it", "test", "specify", "describe", "context", "suite", "Deno"},
	classify:  classifyJS,
	paramMods: set("each", "for", "runIf", "skipIf", "prop"),
	sep:       " > ",
}

var trailingCalls = &callSpec{
	head:       regexp.MustCompile(`(?m)(?:^|[^\w$.])([xf]?(?:test|it|should|describe|context)|ignore)\b`),
	need:       []string{"test", "it", "should", "describe", "context", "ignore"},
	classify:   classifyTrailing,
	trailing:   true,
	requireLit: true,
	sep:        " > ",
}

var callSpecs = map[lang.Lang]*callSpec{
	lang.JavaScript: jsCalls,
	lang.TypeScript: jsCalls,
	lang.Dart: {
		head: regexp.MustCompile(`(?m)(?:^|[^\w$.])(test|testWidgets|testGoldens|blocTest|patrolTest|group)\b`),
		need: []string{"test", "blocTest", "patrolTest", "group"},
		classify: func(callee string, _ []string) (callKind, bool) {
			if callee == "group" {
				return callSuite, false
			}
			return callTest, false
		},
		sep:     " > ",
		skipArg: regexp.MustCompile(`^skip\s*:\s*(?:true|""|''|"|')`),
	},
	lang.PHP: { // Pest
		head: regexp.MustCompile(`(?m)(?:^|[^\w$>:\\])(it|test|describe)\b`),
		need: []string{"it", "test", "describe"},
		classify: func(callee string, _ []string) (callKind, bool) {
			if callee == "describe" {
				return callSuite, false
			}
			return callTest, false
		},
		sep:        " > ",
		skipSuffix: regexp.MustCompile(`^\s*->\s*(?:skip|todo)\s*\(`),
	},
	lang.Kotlin: trailingCalls, // Kotest, Spek
	lang.Scala:  trailingCalls, // ScalaTest FunSuite/FunSpec
	lang.Swift:  trailingCalls, // Quick
	lang.C:      cppCalls,
	lang.Cpp:    cppCalls,
}

var cppCalls = &callSpec{
	head: regexp.MustCompile(`(?m)(?:^|[^\w])(TEST|TEST_F|TEST_P|TYPED_TEST|TYPED_TEST_P|TEST_CASE|TEST_CASE_METHOD|TEMPLATE_TEST_CASE|SCENARIO)\b`),
	need: []string{"TEST", "SCENARIO"},
	classify: func(string, []string) (callKind, bool) {
		return callTest, false
	},
	trailing: true,
	sep:      ".",
	name: func(t *text, args []span) (string, string, bool) {
		// Catch2/doctest: the first string literal names the test.
		for _, a := range args {
			if a.from < a.to && t.code[a.from] == '"' {
				n := t.literal(a.from)
				return n, n, false
			}
		}
		// GoogleTest: TEST(Suite, Name).
		if len(args) >= 2 {
			suite, name := t.str(args[0]), t.str(args[1])
			skipped := strings.HasPrefix(suite, "DISABLED_") || strings.HasPrefix(name, "DISABLED_")
			return suite + "." + name, name, skipped
		}
		if len(args) == 1 {
			return t.str(args[0]), t.str(args[0]), false
		}
		return "", "", false
	},
}

func classifyJS(callee string, chain []string) (callKind, bool) {
	base := strings.Join(strings.Fields(callee), "")
	kind, skipped := callNone, false
	if base == "Deno.test" {
		kind = callTest
	} else {
		if len(base) > 2 && (base[0] == 'x' || base[0] == 'f') {
			skipped = base[0] == 'x'
			base = base[1:]
		}
		switch base {
		case "it", "test", "specify":
			kind = callTest
		case "describe", "context", "suite":
			kind = callSuite
		}
	}
	for _, m := range chain {
		switch m {
		case "describe", "suite":
			kind = callSuite // test.describe(...), test.describe.serial(...)
		case "skip", "todo", "fixme":
			skipped = true
		case "step", "beforeEach", "afterEach", "beforeAll", "afterAll", "use", "extend",
			"info", "setTimeout", "slow", "configure", "expect", "fn", "mock":
			return callNone, false
		}
	}
	return kind, skipped
}

func classifyTrailing(callee string, _ []string) (callKind, bool) {
	skipped := false
	if callee == "ignore" { // ScalaTest
		return callTest, true
	}
	if len(callee) > 2 && (callee[0] == 'x' || callee[0] == 'f') {
		skipped = callee[0] == 'x'
		callee = callee[1:]
	}
	switch callee {
	case "test", "it", "should":
		return callTest, skipped
	case "describe", "context":
		return callSuite, skipped
	}
	return callNone, false
}

// funcArgRe matches an argument that is a function: function () {...},
// async (page) => ..., Dart () async {...}, PHP fn() => ...
var funcArgRe = regexp.MustCompile(`^(?:async\s+)?(?:function\b|fn\s*\(|\([^()]*\)\s*(?:async\s*)?\{)`)

func (t *text) isFuncArg(s span) bool {
	a := t.str(s)
	return strings.Contains(a, "=>") || funcArgRe.MatchString(a)
}

func findCalls(t *text, spec *callSpec, classes []scope) []raw {
	type test struct {
		r   raw
		pos int
	}
	suites := append([]scope(nil), classes...)
	var tests []test
	for _, m := range t.findAll(spec.head, spec.need) {
		cs, ce := m[2], m[3]
		from := cs - 40
		if from < 0 {
			from = 0
		}
		if defBefore.MatchString(t.code[from:cs]) {
			continue // function test(...) { ... } defines a helper
		}
		callee := t.code[cs:ce]
		chain, open, ok := t.callChain(ce, spec.paramMods)
		if !ok {
			continue
		}
		kind, skipped := spec.classify(callee, chain)
		if kind == callNone {
			continue
		}
		close, closed := t.closeOf(open)
		args := t.args(open, close)
		lit := len(args) > 0 && args[0].from < args[0].to && isQuote(t.lang, t.code[args[0].from])
		if spec.requireLit && !lit {
			continue
		}
		end := close
		switch {
		case spec.trailing:
			if !closed || len(args) == 0 {
				continue
			}
			j := t.skipSpace(close + 1)
			if j >= len(t.code) || t.code[j] != '{' {
				continue
			}
			end, closed = t.closeOf(j)
		case len(args) >= 2 && (lit || anyFunc(t, args[1:])):
		case lit && len(args) == 1 && kind == callTest:
			skipped = true // it('pending') / it.todo('later')
		default:
			continue
		}
		var name, bare string
		if spec.name != nil {
			var sk bool
			name, bare, sk = spec.name(t, args)
			skipped = skipped || sk
		} else if len(args) > 0 {
			name = t.argName(args[0])
			bare = name
		}
		if spec.skipArg != nil {
			for _, a := range args {
				if spec.skipArg.MatchString(t.str(a)) {
					skipped = true
				}
			}
		}
		if spec.skipSuffix != nil && closed && close+1 < len(t.code) && spec.skipSuffix.MatchString(t.code[close+1:]) {
			skipped = true
		}
		if kind == callSuite {
			suites = append(suites, scope{name: name, from: open, to: end, skipped: skipped})
			continue
		}
		hl := t.lineOf(cs)
		tests = append(tests, test{raw{bare: bare, name: name, start: hl, header: hl, end: t.lineOf(end),
			skipped: skipped, open: !closed}, cs})
	}
	sortScopes(suites)
	out := make([]raw, 0, len(tests))
	for _, ts := range tests {
		r := ts.r
		names, sk := enclosing(suites, ts.pos)
		if r.name != "" { // an unnamed test stays unnamed
			r.name = strings.Join(append(names, r.name), spec.sep)
		}
		r.skipped = r.skipped || sk
		out = append(out, r)
	}
	return out
}

func anyFunc(t *text, args []span) bool {
	for _, a := range args {
		if t.isFuncArg(a) {
			return true
		}
	}
	return false
}

// callChain parses what follows a callee: member accesses (.skip, .each),
// argument lists of parameterizing members (.each(table), .each`table`),
// and type arguments (<Case>), up to the parenthesis that opens the call
// declaring the test. It returns that parenthesis' offset.
func (t *text) callChain(pos int, paramMods map[string]bool) (chain []string, open int, ok bool) {
	code := t.code
	consumed := false // the last parameterizing member already got its arguments
	for steps := 0; steps < 16; steps++ {
		i := t.skipSpace(pos)
		if i >= len(code) {
			return nil, 0, false
		}
		switch c := code[i]; c {
		case '.':
			j := t.skipSpace(i + 1)
			k := j
			for k < len(code) && isWordByte(code[k]) {
				k++
			}
			if k == j {
				return nil, 0, false
			}
			chain = append(chain, code[j:k])
			consumed = false
			pos = k
		case '<': // type arguments: blocTest<Cubit, State>(...), it.each<Case>(...)
			end, closed := t.closeOf(i)
			if !closed || strings.ContainsAny(code[i:end], ";{}\n") {
				return nil, 0, false
			}
			pos = end + 1
		case '(', '`':
			last := ""
			if len(chain) > 0 {
				last = chain[len(chain)-1]
			}
			if paramMods[last] && !consumed {
				end, closed := 0, false
				if c == '(' {
					end, closed = t.closeOf(i)
				} else if k := strings.IndexByte(code[i+1:], '`'); k >= 0 {
					end, closed = i+1+k, true
				}
				if !closed {
					return nil, 0, false
				}
				consumed = true
				pos = end + 1
				continue
			}
			if c == '(' {
				return chain, i, true
			}
			return nil, 0, false
		default:
			return nil, 0, false
		}
	}
	return nil, 0, false
}
