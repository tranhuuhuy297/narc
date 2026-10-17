package tests

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// The assertion parser recognizes single-line assertions in the common test
// frameworks and describes what makes them pass. It is shared by tautology
// (can it fail at all?), assertion-weakened (did a specific check become a
// vague one?) and assertion-commented (was a live assertion turned into a
// comment?).

// passKind says what an assertion requires of its values to pass.
type passKind int

const (
	passUnknown  passKind = iota
	passTruthy            // values[0] is truthy (or true)
	passFalsy             // values[0] is falsy (or false)
	passEqual             // values[0] equals values[1]
	passNotEqual          // values[0] differs from values[1]
	passNotNull           // values[0] is not null/nil/None/undefined
	passDefined           // values[0] is not undefined (JS: null passes)
	passNull              // values[0] is null
	passAlways            // nothing: Assert.Pass(), t.pass(), expect(x, anything)
)

// strength says how specific an assertion is.
type strength int

const (
	checksOther strength = iota // a check that is neither clearly specific nor clearly vague
	checksValue                 // a specific value: equality, containment, length, call arguments, a match
	checksExist                 // only that something exists or is truthy: toBeDefined, NotNil, assertTrue(x)
	checksThrow                 // that something throws; exc and match say how specifically
)

// assertion is one assertion found on a line of test code.
type assertion struct {
	start, end int    // span in the line's value text
	text       string // value text of the assertion
	fn         string // function or matcher name: "toEqual", "assertEqual", "assert_eq!"
	pass       passKind
	async      bool   // .resolves / .rejects / eventually: outcome not known statically
	values     []expr // what pass is evaluated on (subject first)
	subjects   []expr // what the assertion is about, for pairing old and new assertions
	str        strength
	exc        string // checksThrow: the expected exception type, "" for any
	match      bool   // checksThrow: the message is checked too
	helper     bool   // a project's own assertion helper; only subjects are known
}

func (a assertion) String() string { return display(a.text) }

// assertionHints are fragments that every assertion form contains. Lines
// without any of them are skipped without running the parser's regexps.
var assertionHints = []string{"ssert", "SSERT_", "xpect", "XPECT_", "hould", "XCT", "SUCCEED", "efute", "aises", "hrow", "Ω", "should_panic", "@Test", "require.", "is.", "t.", "s."}

// parseAssertions returns the assertions on a line of a file in language lg.
func parseAssertions(lg lang.Lang, l core.Line) []assertion {
	hinted := false
	for _, h := range assertionHints {
		if strings.Contains(l.Code, h) {
			hinted = true
			break
		}
	}
	if !hinted {
		return nil
	}
	vt, ok := valueText(l, lg)
	if strings.TrimSpace(vt) == "" {
		return nil
	}
	p := &parser{lg: lg, vt: vt, litOK: ok}
	p.expectChains()
	p.calls()
	p.keywords()
	p.fluent()
	p.kotest()
	p.throwAttrs()
	return p.out
}

// maxChain is how many lines above a continuation line chained looks for
// the start of the statement.
const maxChain = 8

// chained returns line l of version v of f joined with the lines above it
// when l continues a statement that a formatter split over several lines:
//
//	expect(result)          expect(          assertThat(total)
//	  .toEqual({ id: 1 });    true             .isEqualTo(42);
//	                        ).toBe(true);
//
// so that the assertion parses as if it were written on one line. The
// result keeps l's line number; any other line is returned unchanged.
// Python has no such layout (a continuation needs a backslash or an open
// bracket), and Go puts the dot at the end of the line, so neither is
// joined.
func chained(f *core.FileInfo, l core.Line, v version) core.Line {
	if f.Lang == lang.Python || f.Lang == lang.Go || !continuesChain(l.Code) {
		return l
	}
	parts := []lang.LexedLine{l.LexedLine}
	depth := bracketDepth(l.Code)
	for k := l.No - 1; k >= 1 && k >= l.No-maxChain; k-- {
		prev, ok := v.line(f, k)
		if !ok {
			return l
		}
		if strings.TrimSpace(prev.Code) == "" {
			continue // a blank line or a comment inside the statement
		}
		parts = append(parts, prev)
		depth += bracketDepth(prev.Code)
		if depth >= 0 && !continuesChain(prev.Code) {
			return joinLines(parts, l)
		}
	}
	return l // no statement start within reach
}

// continuesChain reports whether a line of code continues a method chain
// started above: `.toBe(1)`, `->toBe(1)`, `).toBe(true)`, `}).toThrow()`.
func continuesChain(code string) bool {
	t := strings.TrimLeft(code, " \t")
	t = strings.TrimLeft(t, ")]}")
	return strings.HasPrefix(t, ".") && !strings.HasPrefix(t, "..") || strings.HasPrefix(t, "->")
}

// bracketDepth returns the number of brackets that code opens minus the
// number it closes. String contents are already removed from Code.
func bracketDepth(code string) int {
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

// joinLines joins lexed lines given bottom-up into one line numbered like l.
func joinLines(bottomUp []lang.LexedLine, l core.Line) core.Line {
	var raw, code, nocom, com strings.Builder
	var strs []string
	glue := func(b *strings.Builder, s string) {
		s = strings.TrimSpace(s)
		if cur := b.String(); cur != "" && s != "" && isWordByte(cur[len(cur)-1]) && isWordByte(s[0]) {
			b.WriteByte(' ') // `await` + `expect(x)`: keep the words apart
		}
		b.WriteString(s)
	}
	for i := len(bottomUp) - 1; i >= 0; i-- {
		p := bottomUp[i]
		glue(&raw, p.Raw)
		glue(&code, p.Code)
		glue(&nocom, p.NoComment)
		if p.Comment != "" {
			if com.Len() > 0 {
				com.WriteByte(' ')
			}
			com.WriteString(p.Comment)
		}
		strs = append(strs, p.Strings...)
	}
	joined := lang.LexedLine{Raw: raw.String(), Code: code.String(), NoComment: nocom.String(), Comment: com.String(), Strings: strs}
	return core.Line{LexedLine: joined, No: l.No, Hunk: l.Hunk}
}

type parser struct {
	lg    lang.Lang
	vt    string
	litOK bool
	out   []assertion
}

// add records an assertion unless it overlaps one found earlier.
func (p *parser) add(a assertion) {
	for _, o := range p.out {
		if a.start < o.end && o.start < a.end {
			return
		}
	}
	a.text = strings.TrimSpace(p.vt[a.start:a.end])
	if a.subjects == nil && len(a.values) > 0 {
		a.subjects = a.values[:1]
		if a.pass == passEqual || a.pass == passNotEqual {
			a.subjects = a.values
		}
	}
	p.out = append(p.out, a)
}

// ---- matcher vocabulary -------------------------------------------------------

type matcherInfo struct {
	pass passKind
	str  strength
}

// matchers maps matcher names (Jest/Vitest, Chai, Jasmine, RSpec, Pest,
// gomega, Nimble, AssertJ, Truth, Strikt, hamcrest, NUnit constraints, Dart)
// to their meaning.
var matchers = map[string]matcherInfo{}

func defMatchers(m matcherInfo, names ...string) {
	for _, n := range names {
		matchers[n] = m
	}
}

func init() {
	defMatchers(matcherInfo{passEqual, checksValue},
		"toBe", "toEqual", "toStrictEqual", "equal", "equals", "eq", "eql", "Equal", "BeEquivalentTo", "BeIdenticalTo",
		"isEqualTo", "isSameAs", "isEqualByComparingTo", "equalTo", "EqualTo", "same", "SameAs", "be", "Be", "BeSameAs",
		"identicalTo", "toBeSameInstanceAs")
	defMatchers(matcherInfo{passUnknown, checksValue},
		"toMatchObject", "toHaveBeenCalledWith", "toHaveBeenLastCalledWith", "toHaveBeenNthCalledWith", "toHaveBeenCalledTimes",
		"toHaveBeenCalledOnceWith", "toBeCalledWith", "toHaveReturnedWith", "toHaveLength", "toContain", "toContainEqual", "toMatch",
		"toHaveTextContent", "toHaveValue", "toHaveAttribute", "toMatchSnapshot", "toMatchInlineSnapshot", "toBeCloseTo",
		"toHaveText", "toContainText", "toHaveCount", "toHaveURL", "toHaveTitle", "toHaveClass", "toHaveStyle", "toBeOneOf",
		"lengthOf", "length", "include", "includes", "contain", "contains", "members", "keys", "match", "matches",
		"calledWith", "calledOnceWith", "calledWithExactly", "callCount", "closeTo", "approximately",
		"HaveLen", "ContainElement", "ContainElements", "ConsistOf", "MatchError", "HaveKeyWithValue", "HaveKey",
		"ContainSubstring", "HavePrefix", "HaveSuffix", "BeNumerically", "MatchJSON",
		"hasSize", "containsExactly", "containsExactlyInAnyOrder", "containsOnly", "startsWith", "endsWith", "hasMessage",
		"hasMessageContaining", "isCloseTo", "hasValue", "containsEntry", "containsKey",
		"contain_exactly", "match_array", "have_attributes", "start_with", "end_with", "have_key", "include_json", "be_within",
		"HaveCount", "Contain", "ContainSingle", "ContainKey", "StartWith", "EndWith", "HaveLength", "BeApproximately",
		"hasLength", "containsPair", "containsAll", "orderedEquals", "unorderedEquals", "toHaveBeenCalledTimesWith",
		"toHaveProperty", "property") // refine: a property check without a value only checks existence
	defMatchers(matcherInfo{passTruthy, checksExist},
		"toBeTruthy", "ok", "toBeTrue", "BeTrue", "beTrue", "isTrue", "be_truthy", "true", "True")
	defMatchers(matcherInfo{passFalsy, checksOther},
		"toBeFalsy", "toBeFalse", "BeFalse", "beFalse", "isFalse", "be_falsey", "be_falsy", "false", "False")
	defMatchers(matcherInfo{passDefined, checksExist}, "toBeDefined")
	defMatchers(matcherInfo{passNotNull, checksExist},
		"exist", "isNotNull", "NotBeNull", "notNullValue", "be_present", "isPresent", "isNotBlank",
		"NotBeNullOrEmpty", "NotBeNullOrWhiteSpace")
	defMatchers(matcherInfo{passUnknown, checksExist},
		"toBeInTheDocument", "toBeVisible", "toHaveBeenCalled", "toBeCalled", "called", "calledOnce", "isNotEmpty",
		"NotBeEmpty", "BePositive", "isPositive", "isNotZero", "NotBeZero", "HaveOccurred")
	defMatchers(matcherInfo{passNull, checksOther},
		"toBeNull", "toBeUndefined", "null", "undefined", "BeNil", "beNil", "be_nil", "isNull", "BeNull", "Null", "Nil", "nullValue")
	defMatchers(matcherInfo{passUnknown, checksThrow},
		"toThrow", "toThrowError", "throw", "throws", "Throw", "ThrowAsync", "ThrowExactly", "ThrowExactlyAsync",
		"raise_error", "raise_exception", "Panic", "PanicWith", "throwsA", "throwsException", "throwsArgumentError",
		"throwsStateError", "throwsFormatException", "throwsUnsupportedError", "throwsRangeError", "toThrowErrorMatchingSnapshot",
		"toThrowErrorMatchingInlineSnapshot")
	defMatchers(matcherInfo{passAlways, checksOther}, "anything")
	defMatchers(matcherInfo{passUnknown, checksOther},
		"toBeGreaterThan", "toBeGreaterThanOrEqual", "toBeLessThan", "toBeLessThanOrEqual", "isGreaterThan",
		"isGreaterThanOrEqualTo", "isLessThan", "greaterThan", "greaterThanOrEqualTo", "lessThan", "BeGreaterThan",
		"BeGreaterThanOrEqualTo", "BeLessThan", "above", "below", "least", "most", "gt", "gte", "lt", "lte",
		"toBeInstanceOf", "isInstanceOf", "instanceOf", "isA", "BeOfType", "BeAssignableTo", "be_a", "be_kind_of",
		"be_an_instance_of", "a", "an", "Empty", "empty", "BeEmpty", "isEmpty", "be_empty", "Succeed", "toBeNaN")
}

// negations flip the matcher that follows; asyncWords make the outcome
// unknowable; wrappers take the real matcher as their argument (gomega
// .To(Equal(x)), Nimble .to(equal(x)), hamcrest is(x), Dart isNot(x)).
var (
	negations  = map[string]bool{"not": true, "not_to": true, "to_not": true, "toNot": true, "notTo": true, "NotTo": true, "ToNot": true, "ShouldNot": true, "Not": true, "isNot": true}
	asyncWords = map[string]bool{"resolves": true, "rejects": true, "eventually": true, "Eventually": true, "toEventually": true, "toEventuallyNot": true}
	wrappers   = map[string]bool{"To": true, "ToNot": true, "NotTo": true, "Should": true, "ShouldNot": true, "to": true, "toNot": true, "notTo": true, "toEventually": true, "is": true, "Is": true, "not": true, "Not": true, "isNot": true}
	// connectors are chain words that carry no meaning of their own.
	connectors = map[string]bool{"to": true, "be": true, "been": true, "is": true, "that": true, "which": true, "and": true,
		"has": true, "have": true, "with": true, "at": true, "of": true, "same": true, "but": true, "does": true, "still": true,
		"also": true, "deep": true, "nested": true, "own": true, "ordered": true, "any": true, "all": true, "itself": true,
		"Is": true, "Has": true, "Does": true, "Count": true, "Length": true, "Property": true, "Exactly": true, "Some": true,
		"All": true, "Member": true, "Message": true, "Should": true, "And": true, "should": true, "Which": true, "Subject": true}
)

// link is one step of a fluent assertion chain: `.toEqual(x)`, `.not`.
type link struct {
	word    string
	generic string // C#/Kotlin type argument: Throw<ArgumentException>
	args    []expr
	called  bool // written as a call, with arguments in parentheses (or Ruby's `eq 5`)
}

var wordRe = regexp.MustCompile(`^[A-Za-z_$][\w$]*`)

// chain reads `.word(args).word ...` (or PHP `->word`) starting at vt[i]. In
// Ruby the matcher after `.to`/`.not_to` is read without a dot, and its
// arguments may come without parentheses: `.to eq 5`.
func (p *parser) chain(vt string, i int) ([]link, int) {
	var links []link
	for {
		j := i
		for j < len(vt) && (vt[j] == ' ' || vt[j] == '\t') {
			j++
		}
		rubyMatcher := false
		switch {
		case strings.HasPrefix(vt[j:], "."):
			j++
		case strings.HasPrefix(vt[j:], "->"):
			j += 2
		case p.lg == lang.Ruby && len(links) > 0 && (links[len(links)-1].word == "to" || negations[links[len(links)-1].word]) &&
			!links[len(links)-1].called && j > i:
			rubyMatcher = true
		default:
			return links, i
		}
		for j < len(vt) && vt[j] == ' ' {
			j++
		}
		w := wordRe.FindString(vt[j:])
		if w == "" {
			return links, i
		}
		lk := link{word: w}
		j += len(w)
		if j < len(vt) && vt[j] == '<' {
			if k := strings.IndexByte(vt[j:], '>'); k > 0 && !strings.ContainsAny(vt[j:j+k], "()") {
				lk.generic, j = strings.TrimSpace(vt[j+1:j+k]), j+k+1
			}
		}
		k := j
		for k < len(vt) && vt[k] == ' ' {
			k++
		}
		if k < len(vt) && vt[k] == '(' {
			if as, end, ok := args(vt, k); ok {
				lk.args, lk.called, j = as, true, end+1
			}
		} else if rubyMatcher && k > j && k < len(vt) {
			// `.to eq 5`, `.to be true`, `.to raise_error ArgumentError, "msg"`
			rest := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(vt[k:]), " do"))
			if rest != "" && !strings.HasPrefix(rest, "{") {
				for _, a := range splitTop(rest, ',') {
					lk.args = append(lk.args, newExpr(a))
				}
				lk.called, j = true, len(vt)
			}
		}
		links = append(links, lk)
		i = j
		if rubyMatcher {
			return links, i
		}
	}
}

// matcherResult is what a matcher chain means.
type matcherResult struct {
	name     string
	pass     passKind
	str      strength
	expected *expr
	async    bool
	exc      string
	match    bool
	ok       bool
}

// interpret reads a chain of links (after the subject) as a matcher.
// Unknown matchers that are called (toSatisfy(fn), custom matchers) still
// count as assertions of unknown meaning.
func (p *parser) interpret(links []link) matcherResult {
	neg, async := false, false
	for i, lk := range links {
		w := lk.word
		if wrappers[w] && lk.called && len(lk.args) == 1 {
			r := p.matcherExpr(lk.args[0])
			if negations[w] {
				r = negate(r)
			}
			if neg {
				r = negate(r)
			}
			r.async = r.async || async || asyncWords[w]
			return r
		}
		switch {
		case asyncWords[w]:
			async = true
			continue
		case negations[w] && !lk.called:
			neg = !neg
			continue
		case connectors[w] && !lk.called:
			continue
		}
		info, known := matchers[w]
		if !known {
			if lk.called {
				return matcherResult{name: w, async: async, ok: true}
			}
			return matcherResult{}
		}
		r := matcherResult{name: w, pass: info.pass, str: info.str, async: async, ok: true}
		if pos := positional(lk.args); len(pos) > 0 {
			e := pos[0]
			r.expected = &e
		}
		r = p.refine(r, lk, links[i+1:])
		if neg {
			r = negate(r)
		}
		return r
	}
	return matcherResult{}
}

// matcherExpr interprets a matcher written as an expression: gomega
// Equal(x), hamcrest equalTo(x) / is(x), NUnit Is.EqualTo(x), Dart isTrue.
// A plain value means equality (hamcrest is(5), Dart expect(x, 5)).
func (p *parser) matcherExpr(e expr) matcherResult {
	equality := matcherResult{name: "equals", pass: passEqual, str: checksValue, expected: &e, ok: true}
	if isLiteral(e) || lit(e, p.lg).kind != notLiteral {
		return equality
	}
	links, end := p.chain("."+e.raw, 0)
	if len(links) == 0 || end != len(e.raw)+1 {
		return equality
	}
	if links[0].word == "Throws" {
		return nunitThrows(links)
	}
	if r := p.interpret(links); r.ok {
		return r
	}
	if links[len(links)-1].called {
		return matcherResult{name: links[len(links)-1].word, ok: true} // an unknown matcher function
	}
	return equality
}

// nunitThrows reads NUnit's Throws.TypeOf<X>(), Throws.InstanceOf<X>(),
// Throws.ArgumentNullException and Throws.Exception constraints.
func nunitThrows(links []link) matcherResult {
	r := matcherResult{name: "Throws", str: checksThrow, ok: true}
	if len(links) > 1 {
		switch lk := links[1]; {
		case lk.generic != "":
			r.exc = exceptionName(lk.generic)
		case lk.word == "TypeOf" || lk.word == "InstanceOf":
			if len(lk.args) > 0 {
				r.exc = exceptionName(lk.args[0].raw)
			}
		default:
			r.exc = lk.word
		}
	}
	for _, lk := range links[1:] {
		if lk.word == "Message" || lk.word == "WithMessage" {
			r.match = true
		}
	}
	return r
}

// refine adjusts a matcher's meaning for its arguments.
func (p *parser) refine(r matcherResult, lk link, rest []link) matcherResult {
	pos := positional(lk.args)
	switch r.name {
	case "toHaveBeenCalled", "toBeCalled", "called":
		if len(pos) > 0 {
			r.str = checksValue
		}
	case "be", "Be":
		if len(pos) == 0 {
			r.pass, r.str = passTruthy, checksExist // RSpec `to be`
		} else if strings.HasPrefix(pos[0].raw, ">") || strings.HasPrefix(pos[0].raw, "<") {
			r.pass, r.expected = passUnknown, nil // RSpec `be > 5`
			if zeroBound(pos[0].raw) {
				r.str = checksExist
			}
		}
	case "toHaveProperty", "property":
		r.str = checksExist
		if len(pos) > 1 {
			r.str = checksValue
		}
		r.pass, r.expected = passUnknown, nil
	case "toBeInstanceOf", "isInstanceOf", "instanceOf", "isA", "BeOfType", "be_a", "be_kind_of", "a", "an":
		if len(pos) > 0 && genericException(pos[0].raw) {
			r.str = checksExist // toBeInstanceOf(Object) holds for anything non-null
		}
	}
	switch r.name {
	case "toBeGreaterThan", "toBeGreaterThanOrEqual", "isGreaterThan", "isGreaterThanOrEqualTo", "greaterThan", "greaterThanOrEqualTo", "BeGreaterThan", "BeGreaterThanOrEqualTo":
		if len(pos) > 0 && zeroBound(">"+pos[0].raw) {
			r.str = checksExist
		}
	}
	if r.str == checksThrow {
		r.pass, r.expected = passUnknown, nil
		r.exc, r.match = throwSpec(lk, pos)
		for _, more := range rest {
			switch more.word {
			case "withMessage", "WithMessage", "hasMessage", "hasMessageContaining", "withMessageContaining", "with_message":
				r.match = true
			}
		}
	}
	if r.str == checksValue && len(pos) > 0 && allVague(r.name, pos) {
		// toEqual(expect.anything()), toHaveBeenCalledWith(expect.any(Object)),
		// toMatchObject({}): the arguments accept (nearly) any value.
		r.str = checksExist
	}
	if r.str == checksValue && r.pass == passEqual && r.expected == nil {
		r.pass = passUnknown
	}
	return r
}

// vagueArgRe matches asymmetric matchers that accept any value, or any
// value of a type: expect.anything(), expect.any(Number), anything(), and
// containment matchers with nothing to contain: expect.objectContaining({}),
// expect.arrayContaining([]), expect.stringContaining(”).
var vagueArgRe = regexp.MustCompile(`^(?:expect\.)?(?:anything\(\)|any\(\s*[\w.$]*\s*\)|objectContaining\(\s*\{\s*\}\s*\)|arrayContaining\(\s*\[\s*\]\s*\)|stringContaining\(\s*(?:""|''|\x60\x60)\s*\))$`)

// vague reports whether the expected value e of matcher name accepts
// (nearly) any actual value. Besides vagueArgRe, a subset match against an
// empty object (toMatchObject({})) and containment of the empty string
// (toContain(”)) hold for anything of the right type.
func vague(name string, e expr) bool {
	if vagueArgRe.MatchString(e.raw) {
		return true
	}
	switch name {
	case "toMatchObject", "objectContaining", "containSubset", "include":
		return compact(e.raw) == "{}"
	case "toContain", "contain", "contains", "includes", "toMatch", "match", "ContainSubstring", "StartWith", "EndWith", "startsWith", "endsWith", "start_with", "end_with":
		return e.raw == `""` || e.raw == `''`
	}
	return false
}

func allVague(name string, as []expr) bool {
	for _, a := range as {
		if !vague(name, a) {
			return false
		}
	}
	return true
}

// boolEquality judges an equality with the literal true by its subject,
// like assertTrue: expect(count > 0).toBe(true) only checks existence,
// expect(a === b).toBe(true) checks a value. (It is the same check as
// toBeTruthy(); see styleOnly.)
func boolEquality(a *assertion) {
	if a.pass != passEqual || a.str != checksValue || len(a.values) != 2 {
		return
	}
	for i, v := range a.values {
		if l := lit(v, lang.Unknown); l.kind == litBool && l.value == "true" {
			if other := a.values[1-i]; lit(other, lang.Unknown).kind == notLiteral {
				a.str, a.subjects = boolCheck(other)
			}
			return
		}
	}
}

func zeroBound(s string) bool {
	s = strings.Join(strings.Fields(s), "")
	return s == ">0" || s == ">=1" || s == ">=0" || s == ">0.0"
}

func negate(r matcherResult) matcherResult {
	switch r.pass {
	case passTruthy:
		r.pass, r.str = passFalsy, checksOther
	case passFalsy:
		r.pass, r.str = passTruthy, checksExist
	case passEqual:
		r.pass, r.str = passNotEqual, checksOther
	case passNotEqual:
		r.pass = passEqual
	case passNull:
		r.pass, r.str = passNotNull, checksExist
	case passNotNull, passDefined:
		r.pass, r.str = passNull, checksOther
	case passAlways:
		r.pass = passUnknown
	default:
		if r.str == checksExist && (r.name == "isNotEmpty" || r.name == "NotBeEmpty") {
			break
		}
		r.str = checksOther
	}
	if r.name == "Empty" || r.name == "empty" || r.name == "BeEmpty" || r.name == "isEmpty" || r.name == "be_empty" {
		r.str = checksExist
	}
	r.exc, r.match = "", false
	return r
}

// throwSpec reads the exception type and message constraint of a throws
// matcher: toThrow(TypeError), toThrow("expired"), raise_error(KeyError, /msg/),
// Throw<ArgumentException>(), throwsA(isA<StateError>()).
func throwSpec(lk link, pos []expr) (exc string, match bool) {
	if lk.generic != "" {
		exc = lk.generic
	}
	switch lk.word {
	case "throwsArgumentError", "throwsStateError", "throwsFormatException", "throwsUnsupportedError", "throwsRangeError":
		return strings.TrimPrefix(lk.word, "throws"), false
	case "throwsException":
		return "Exception", false
	}
	for i, a := range pos {
		switch {
		case isStr(a) || strings.HasPrefix(a.raw, "/") || strings.HasPrefix(a.raw, "%r"):
			match = true
		case i == 0 && exc == "" && newExcRe.MatchString(a.raw):
			// toThrow(new Error("boom")) checks the type and the message
			m := newExcRe.FindStringSubmatch(a.raw)
			exc, match = m[1], strings.TrimSpace(m[2]) != ""
		case i == 0 && exc == "":
			exc = exceptionName(a.raw)
		default:
			match = true
		}
	}
	return exc, match
}

var newExcRe = regexp.MustCompile(`^new\s+([\w.$]+)\s*\((.*)\)$`)

var excWrapRe = regexp.MustCompile(`^(?:isA|instanceOf|typeof|Is\.TypeOf|TypeOf|isInstanceOf)\s*(?:<([^>]+)>)?\s*\((.*)\)$`)

// exceptionName normalizes how an exception type is written:
// ValueError, \InvalidArgumentException::class, IOException.class,
// typeof(ArgumentException), isA<StateError>().
func exceptionName(s string) string {
	s = strings.TrimSpace(s)
	if m := excWrapRe.FindStringSubmatch(s); m != nil {
		if m[1] != "" {
			s = m[1]
		} else {
			s = m[2]
		}
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "::class"), ".class")
	s = strings.TrimSuffix(s, ".self")
	s = strings.TrimPrefix(s, `\`)
	return strings.TrimSpace(s)
}

// genericException reports whether an exception type matches (nearly)
// anything.
func genericException(s string) bool {
	s = exceptionName(s)
	if i := strings.LastIndexAny(s, `.\:`); i >= 0 {
		s = s[i+1:]
	}
	switch s {
	case "", "Exception", "BaseException", "Throwable", "Error", "RuntimeException", "StandardError", "Object", "object",
		"anything", "any", "SystemException", "AnyError":
		return true
	}
	return false
}

// ---- expect(...) chains ---------------------------------------------------------

var expectRe = regexp.MustCompile(`(?:^|[^\w$>:\\])((?:[\w$]+\.)*)(expect|Expect|Ω|expectThat|assertThat|assert_that|assertThatThrownBy|assertThatExceptionOfType|assertThatCode)\s*([({])`)

func (p *parser) expectChains() {
	for _, m := range expectRe.FindAllStringSubmatchIndex(p.vt, -1) {
		recv, fn := p.vt[m[2]:m[3]], p.vt[m[4]:m[5]]
		if !p.expectAllowed(fn) || (recv != "" && fn == "expect" && recv != "chai.") || (recv != "" && fn == "Expect") {
			continue // Rust's result.expect("..."), a method that happens to be called expect
		}
		open := m[6]
		end, ok := closing(p.vt, open)
		if !ok {
			continue
		}
		var as []expr
		if p.vt[open] == '(' {
			as, _, ok = args(p.vt, open)
			if !ok || len(as) == 0 {
				continue
			}
		} else {
			as = []expr{newExpr(p.vt[open+1 : end])} // RSpec block: expect { ... }
		}
		start := m[4]
		switch {
		case fn == "assertThatThrownBy" || fn == "assertThatExceptionOfType":
			p.assertJThrows(start, end, fn, as)
			continue
		case p.lg == lang.Dart && len(as) >= 2 && fn == "expect":
			r := p.matcherExpr(as[1])
			p.addMatcher(start, end+1, fn, as[0], r)
			continue
		case (fn == "assertThat" || fn == "assert_that") && len(as) >= 2:
			// hamcrest: assertThat([reason,] actual, matcher)
			actual, m := as[0], as[1]
			if len(as) >= 3 && isStr(as[0]) {
				actual, m = as[1], as[2]
			}
			p.addMatcher(start, end+1, "assertThat", actual, p.matcherExpr(m))
			continue
		}
		links, cend := p.chain(p.vt, end+1)
		if len(links) == 0 {
			continue
		}
		r := p.interpret(links)
		if !r.ok {
			continue
		}
		p.addMatcher(start, cend, r.name, as[0], r)
	}
}

func (p *parser) expectAllowed(fn string) bool {
	switch fn {
	case "expect":
		switch p.lg {
		case lang.JavaScript, lang.TypeScript, lang.PHP, lang.Ruby, lang.Swift, lang.Dart:
			return true
		}
	case "Expect", "Ω":
		return p.lg == lang.Go
	case "assertThat", "expectThat", "assertThatThrownBy", "assertThatExceptionOfType", "assertThatCode":
		return isJVM(p.lg)
	case "assert_that":
		return p.lg == lang.Python
	}
	return false
}

// assertJThrows reads AssertJ's assertThatThrownBy(() -> ...).isInstanceOf(X.class)
// and assertThatExceptionOfType(X.class).isThrownBy(...).
func (p *parser) assertJThrows(start, end int, fn string, as []expr) {
	links, cend := p.chain(p.vt, end+1)
	a := assertion{start: start, end: cend, fn: fn, str: checksThrow}
	if fn == "assertThatExceptionOfType" && len(as) > 0 {
		a.exc = exceptionName(as[0].raw)
	}
	for _, lk := range links {
		switch lk.word {
		case "isInstanceOf", "isExactlyInstanceOf":
			if len(lk.args) > 0 {
				a.exc = exceptionName(lk.args[0].raw)
			}
		case "hasMessage", "hasMessageContaining", "withMessage", "withMessageContaining", "hasMessageMatching":
			a.match = true
		}
	}
	p.add(a)
}

func (p *parser) addMatcher(start, end int, fn string, subject expr, r matcherResult) {
	if !r.ok {
		return
	}
	a := assertion{start: start, end: end, fn: fn, pass: r.pass, async: r.async, str: r.str, exc: r.exc, match: r.match,
		values: []expr{subject}, subjects: []expr{subject}}
	if r.name != "" {
		a.fn = r.name
	}
	if r.expected != nil {
		a.values = append(a.values, *r.expected)
	}
	if (a.pass == passEqual || a.pass == passNotEqual) && len(a.values) < 2 {
		a.pass = passUnknown
	}
	boolEquality(&a)
	p.add(a)
}

// ---- assertion functions ----------------------------------------------------------

type callSpec struct {
	pass  passKind
	str   strength
	arity int // value arguments, after any test handle or leading message
}

// Call-style assertions come in three receiver styles.
var (
	// anyRecv: JUnit, PHPUnit, unittest, kotlin.test, XCTest, minitest. The
	// names are distinctive, so any receiver (self., $this->, Assert.,
	// Assertions., static imports) is accepted.
	anyRecv = map[string]callSpec{}
	// jsRecv: Node assert, Chai assert, QUnit, AVA/tap t. Common words, so
	// the receiver must be assert or t.
	jsRecv = map[string]callSpec{}
	// capRecv: testify, matryer/is, xUnit, NUnit, MSTest. Capitalized
	// words; the receiver must be an assertion object.
	capRecv = map[string]callSpec{}
	// macros: Rust, Swift Testing and GoogleTest.
	macros = map[string]callSpec{}
)

func def(table map[string]callSpec, spec callSpec, names ...string) {
	for _, n := range names {
		table[n] = spec
	}
}

func init() {
	truthy := callSpec{passTruthy, checksExist, 1}
	falsy := callSpec{passFalsy, checksOther, 1}
	equal := callSpec{passEqual, checksValue, 2}
	notEqual := callSpec{passNotEqual, checksOther, 2}
	notNull := callSpec{passNotNull, checksExist, 1}
	isNull := callSpec{passNull, checksOther, 1}
	value1 := callSpec{passUnknown, checksValue, 1}
	exist := callSpec{passUnknown, checksExist, 1}
	throws := callSpec{passUnknown, checksThrow, 0}
	always := callSpec{passAlways, checksOther, 0}

	def(anyRecv, truthy, "assertTrue", "assert_", "failUnless", "XCTAssertTrue", "XCTAssert")
	def(anyRecv, falsy, "assertFalse", "failIf", "XCTAssertFalse", "refute")
	def(anyRecv, equal, "assertEquals", "assertEqual", "assertSame", "assertIs", "assertArrayEquals", "assertIterableEquals",
		"assertDictEqual", "assertListEqual", "assertTupleEqual", "assertSetEqual", "assertSequenceEqual", "assertMultiLineEqual",
		"assertCountEqual", "assertIdentical", "assertStrictEquals", "assertEqualsCanonicalizing", "assertJsonStringEqualsJsonString",
		"XCTAssertEqual", "XCTAssertIdentical", "assert_equal", "assert_same", "assertContentEquals")
	def(anyRecv, notEqual, "assertNotEquals", "assertNotEqual", "XCTAssertNotEqual", "refute_equal", "assert_not_equal")
	def(anyRecv, notNull, "assertNotNull", "assertIsNotNone", "XCTAssertNotNil", "refute_nil", "assertNotNil")
	def(anyRecv, isNull, "assertNull", "assertIsNone", "XCTAssertNil", "assert_nil")
	def(anyRecv, value1, "assertIn", "assertNotIn", "assertContains", "assertStringContainsString", "assertCount", "assertArrayHasKey",
		"assertRegex", "assertMatchesRegularExpression", "assertAlmostEqual", "assertLinesMatch", "assert_includes", "assert_match",
		"assert_in_delta", "assertJsonStringEqualsJsonFile", "assertStringStartsWith", "assertStringEndsWith", "assertSameSize",
		"XCTAssertEqualWithAccuracy", "assertContainsOnly")
	def(anyRecv, callSpec{passUnknown, checksOther, 2}, "assertGreater", "assertGreaterEqual", "assertLess", "assertLessEqual")
	def(anyRecv, exist, "assertNotEmpty", "refute_empty", "XCTAssertGreaterThan")
	def(anyRecv, throws, "assertRaises", "assertRaisesRegex", "assertRaisesRegexp", "assertRaisesMessage", "assertThrows",
		"assertThrowsExactly", "assert_raises", "expectException", "assertFailsWith", "assertFails", "shouldThrow",
		"shouldThrowAny", "shouldThrowExactly", "XCTAssertThrowsError", "raises")

	def(jsRecv, truthy, "ok", "isTrue", "true", "truthy", "isOk", "assert")
	def(jsRecv, falsy, "isFalse", "false", "falsy", "notOk", "isNotOk")
	def(jsRecv, equal, "equal", "strictEqual", "deepEqual", "deepStrictEqual", "propEqual", "is", "same", "strictSame", "eql")
	def(jsRecv, notEqual, "notEqual", "notStrictEqual", "notDeepEqual", "notDeepStrictEqual", "not", "notSame")
	def(jsRecv, notNull, "isNotNull", "exists", "isNotUndefined")
	def(jsRecv, callSpec{passDefined, checksExist, 1}, "isDefined")
	def(jsRecv, isNull, "isNull", "isUndefined", "notExists")
	def(jsRecv, value1, "lengthOf", "include", "match", "like", "closeTo", "approximately", "sameMembers", "deepInclude", "regex", "hasAllKeys", "property")
	def(jsRecv, throws, "throws", "rejects", "throwsAsync")
	def(jsRecv, always, "pass")

	def(capRecv, truthy, "True", "IsTrue")
	def(capRecv, falsy, "False", "IsFalse")
	def(capRecv, equal, "Equal", "EqualValues", "Exactly", "Same", "AreEqual", "AreSame", "Equivalent", "EquivalentTo", "AreEquivalent")
	def(capRecv, notEqual, "NotEqual", "AreNotEqual", "NotEqualValues")
	def(capRecv, notNull, "NotNil", "NotNull", "IsNotNull")
	def(capRecv, isNull, "Nil", "Null", "IsNull")
	def(capRecv, value1, "Len", "Contains", "ElementsMatch", "Subset", "ErrorIs", "ErrorAs", "EqualError", "ErrorContains",
		"JSONEq", "YAMLEq", "Regexp", "InDelta", "InEpsilon", "InDeltaSlice", "Single", "StartsWith", "EndsWith", "Matches",
		"InRange", "Collection", "Subset", "IsSubsetOf")
	def(capRecv, exist, "NotEmpty", "NotZero", "Positive", "Error", "IsNotEmpty")
	def(capRecv, callSpec{passUnknown, checksOther, 2}, "Greater", "GreaterOrEqual", "Less", "LessOrEqual")
	def(capRecv, throws, "Throws", "ThrowsAsync", "ThrowsAny", "ThrowsAnyAsync", "Panics", "PanicsWithValue", "PanicsWithError", "ThrowsException")
	def(capRecv, always, "Pass")

	def(macros, truthy, "assert!", "debug_assert!", "#expect", "#require", "EXPECT_TRUE", "ASSERT_TRUE")
	def(macros, falsy, "EXPECT_FALSE", "ASSERT_FALSE")
	def(macros, equal, "assert_eq!", "debug_assert_eq!", "EXPECT_EQ", "ASSERT_EQ", "EXPECT_STREQ", "ASSERT_STREQ", "EXPECT_DOUBLE_EQ", "ASSERT_DOUBLE_EQ", "EXPECT_FLOAT_EQ")
	def(macros, notEqual, "assert_ne!", "debug_assert_ne!", "EXPECT_NE", "ASSERT_NE")
	def(macros, callSpec{passUnknown, checksValue, 1}, "assert_matches!", "assert_relative_eq!", "assert_abs_diff_eq!", "assert_ulps_eq!", "EXPECT_NEAR", "ASSERT_NEAR")
	def(macros, throws, "EXPECT_THROW", "ASSERT_THROW", "EXPECT_ANY_THROW", "ASSERT_ANY_THROW")
	def(macros, always, "SUCCEED")
}

var (
	callRe  = regexp.MustCompile(`(?:^|[^\w$#!])((?:[\w$]+(?:\(\))?\s*(?:\.|->|::)\s*)*)(#?[A-Za-z_]\w*!?)\s*(<[^<>()]*(?:<[^<>()]*>[^<>()]*)*>)?\s*\(`)
	recvRe  = regexp.MustCompile(`([\w$]+)(?:\(\))?\s*(?:\.|->|::)\s*$`)
	handles = map[string]bool{"t": true, "b": true, "tb": true, "tt": true, "f": true, "s.T()": true, "suite.T()": true, "t.T()": true}
	// JUnit 4 takes the failure message first: assertEquals("msg", a, b).
	messageFirst = map[string]bool{"assertTrue": true, "assertFalse": true, "assertEquals": true, "assertNotEquals": true,
		"assertNull": true, "assertNotNull": true, "assertSame": true, "assertArrayEquals": true}
	goAssertRecv = map[string]bool{"assert": true, "require": true, "is": true, "s": true, "suite": true, "Require": true, "Assert": true, "a": false}
	csAssertRecv = map[string]bool{"Assert": true, "ClassicAssert": true, "CollectionAssert": true, "StringAssert": true}
)

func (p *parser) calls() {
	for _, m := range callRe.FindAllStringSubmatchIndex(p.vt, -1) {
		name := p.vt[m[4]:m[5]]
		recvs := p.vt[m[2]:m[3]]
		recv := ""
		if rm := recvRe.FindStringSubmatch(recvs); rm != nil {
			recv = rm[1]
		}
		spec, ok := p.callSpecFor(name, recv)
		if !ok {
			continue
		}
		open := m[1] - 1
		as, end, ok := args(p.vt, open)
		if !ok {
			continue
		}
		start := m[2]
		if recvs == "" {
			start = m[4]
		}
		generic := ""
		if m[6] >= 0 {
			generic = strings.Trim(p.vt[m[6]:m[7]], "<> ")
		}
		p.addCall(start, end+1, name, generic, spec, as)
	}
	if isJVM(p.lg) || p.lg == lang.CSharp {
		p.constraintCalls()
	}
}

// callSpecFor looks up an assertion function, checking that its receiver
// fits the framework it belongs to.
func (p *parser) callSpecFor(name, recv string) (callSpec, bool) {
	if spec, ok := macros[name]; ok {
		switch {
		case strings.HasSuffix(name, "!"):
			return spec, p.lg == lang.Rust
		case strings.HasPrefix(name, "#"):
			return spec, p.lg == lang.Swift
		default:
			return spec, p.lg == lang.C || p.lg == lang.Cpp
		}
	}
	if spec, ok := anyRecv[name]; ok {
		if name == "refute" && p.lg == lang.Python {
			return spec, false
		}
		if name == "raises" {
			return spec, recv == "pytest"
		}
		return spec, p.lg != lang.Go
	}
	if spec, ok := jsRecv[name]; ok {
		switch {
		case name == "assert" && recv == "":
			// a bare assert(x) call: Node, Chai, Scala, C, Swift, Kotlin, Groovy
			return spec, p.lg != lang.Python && p.lg != lang.Go && p.lg != lang.Ruby && p.lg != lang.Elixir
		case p.lg.IsJSLike():
			return spec, recv == "assert" || recv == "t"
		}
		return spec, false
	}
	if spec, ok := capRecv[name]; ok {
		switch p.lg {
		case lang.Go:
			return spec, goAssertRecv[recv]
		case lang.CSharp:
			return spec, csAssertRecv[recv]
		}
	}
	return callSpec{}, false
}

func (p *parser) addCall(start, end int, name, generic string, spec callSpec, as []expr) {
	if p.lg == lang.Go && len(as) > 0 && handles[as[0].norm] {
		as = as[1:] // testify: assert.Equal(t, want, got)
	}
	a := assertion{start: start, end: end, fn: name, pass: spec.pass, str: spec.str}
	if spec.str == checksThrow {
		a.exc, a.match = callThrowSpec(name, generic, as) // needs the keyword arguments (match=...)
		p.add(a)
		return
	}
	if p.lg == lang.Python || p.lg == lang.Swift || p.lg == lang.Kotlin || p.lg == lang.CSharp || p.lg == lang.Ruby {
		as = positional(as) // msg="...", accuracy: 0.1, message = "..."
	}
	if isJVM(p.lg) && messageFirst[name] && len(as) > spec.arity && isStr(as[0]) {
		as = as[1:] // JUnit 4: assertEquals("message", expected, actual)
	}
	if len(as) < spec.arity {
		return
	}
	a.values = as[:spec.arity]
	if spec.pass == passTruthy || spec.pass == passFalsy {
		a.str, a.subjects = boolCheck(as[0])
		if spec.pass == passFalsy && a.str == checksExist {
			a.str = checksOther
		}
	}
	if spec.str == checksValue && spec.arity == 1 && len(as) > 1 {
		a.subjects = as[:2] // assertIn(member, container), Len(x, 3), InDelta(a, b, d)
	}
	if spec.arity == 2 && spec.pass == passUnknown && spec.str == checksOther {
		// assertGreater(len(items), 0), assert.GreaterOrEqual(t, n, 1): a lower
		// bound of zero only checks that something is there.
		op := ">"
		if strings.Contains(name, "Equal") {
			op = ">="
		}
		a.subjects = as[:1]
		if strings.HasPrefix(name, "assertGreater") || strings.HasPrefix(name, "Greater") {
			if zeroBound(op + as[1].raw) {
				a.str = checksExist
			}
		}
	}
	boolEquality(&a)
	p.add(a)
}

// callThrowSpec reads the exception of an exception assertion:
// assertRaises(ValueError), pytest.raises(KeyError, match="x"),
// assertThrows(IOException.class, ...), Assert.Throws<ArgumentException>(...).
func callThrowSpec(name, generic string, as []expr) (exc string, match bool) {
	pos := positional(as)
	switch {
	case generic != "":
		exc = exceptionName(generic)
	case name == "throws" || name == "rejects" || name == "throwsAsync":
		// Node, Chai and AVA take the function first: assert.throws(fn, TypeError).
		if len(pos) > 1 {
			if isStr(pos[1]) || strings.HasPrefix(pos[1].raw, "/") {
				match = true
			} else {
				exc = exceptionName(pos[1].raw)
			}
		}
	case len(pos) > 0 && !callable(pos[0]):
		exc = exceptionName(pos[0].raw)
	}
	if v, ok := kwarg(as, "expected_exception"); ok && exc == "" {
		exc = exceptionName(v.raw)
	}
	switch name {
	case "assertRaisesRegex", "assertRaisesRegexp", "assertRaisesMessage":
		match = true
	case "assertFails", "shouldThrowAny", "ThrowsException", "Panics", "XCTAssertThrowsError":
		exc = "" // these accept any error
	}
	if _, ok := kwarg(as, "match"); ok {
		match = true
	}
	return exc, match
}

// callable reports whether an argument is a function literal rather than
// an exception type: () => f(), () -> f(), { f() }, lambda: f().
func callable(e expr) bool {
	return strings.Contains(e.raw, "=>") || strings.Contains(e.raw, "->") || strings.HasPrefix(e.raw, "{") ||
		strings.HasPrefix(e.raw, "lambda") || strings.HasPrefix(e.raw, "function")
}

// boolCheck classifies the condition of assertTrue(cond): a comparison
// is a value check (assertTrue(a == b)), a null check or a non-empty
// check is an existence check, anything else checks truthiness.
func boolCheck(e expr) (strength, []expr) {
	// A conjunction checks as much as its most specific part:
	// `user is not None and user.id == want.id` still compares a value.
	for _, and := range []string{"&&", " and "} {
		if parts := splitOp(e.raw, and); len(parts) > 1 {
			best, subjects := checksOther, []expr(nil)
			for _, p := range parts {
				str, subj := boolCheck(newExpr(p))
				if str == checksValue || best == checksOther {
					best = str
				}
				subjects = append(subjects, subj...)
			}
			return best, subjects
		}
	}
	if lhs, op, rhs, ok := splitCompare(e.raw); ok {
		l, r := newExpr(lhs), newExpr(rhs)
		ll, rl := lit(l, lang.Unknown), lit(r, lang.Unknown)
		switch op {
		case "==", "===", "is", "eq":
			switch {
			case ll.kind == litNull || rl.kind == litNull:
				return checksOther, []expr{l, r}
			case ll.kind == litBool:
				return checksExist, []expr{r}
			case rl.kind == litBool:
				return checksExist, []expr{l}
			}
			return checksValue, []expr{l, r}
		case "!=", "!==", "is not":
			if rl.kind == litNull {
				return checksExist, []expr{l}
			}
			if ll.kind == litNull {
				return checksExist, []expr{r}
			}
			return checksOther, []expr{l, r}
		case ">", ">=":
			if zeroBound(op + rhs) {
				return checksExist, []expr{l}
			}
			return checksOther, []expr{l}
		case "in":
			return checksValue, []expr{l, r}
		}
		return checksOther, []expr{l, r}
	}
	if m := equalsCallRe.FindStringSubmatch(e.raw); m != nil {
		return checksValue, []expr{newExpr(m[1]), newExpr(m[2])}
	}
	if m := presenceRe.FindStringSubmatch(e.raw); m != nil {
		return checksExist, []expr{newExpr(m[1])}
	}
	if m := notEmptyRe.FindStringSubmatch(e.raw); m != nil {
		return checksExist, []expr{newExpr(m[1])}
	}
	if strings.HasPrefix(e.raw, "matches!(") {
		return checksValue, []expr{e}
	}
	if strings.HasPrefix(e.raw, "!") || strings.HasPrefix(e.raw, "not ") {
		return checksOther, []expr{e}
	}
	return checksExist, []expr{e}
}

var (
	equalsCallRe = regexp.MustCompile(`^(.+)\.(?:equals|Equals|equalsIgnoreCase|contentEquals|isEqual)\((.+)\)$`)
	presenceRe   = regexp.MustCompile(`^(.+)\.(?:is_ok|is_some|is_err|isPresent|isDefined|is_present|present\?|HasValue|any\?)(?:\(\))?$`)
	notEmptyRe   = regexp.MustCompile(`^(?:!\s*|not\s+)(.+)\.(?:is_empty|isEmpty|IsEmpty|empty\?|isBlank)(?:\(\))?$`)
)

// constraintCalls handles NUnit Assert.That(actual, constraint) and
// hamcrest-style assertThat(actual, matcher) written as a static call.
func (p *parser) constraintCalls() {
	for _, m := range thatRe.FindAllStringSubmatchIndex(p.vt, -1) {
		as, end, ok := args(p.vt, m[1]-1)
		if !ok || len(as) == 0 {
			continue
		}
		if len(as) == 1 {
			p.addCall(m[0], end+1, "That", "", callSpec{passTruthy, checksExist, 1}, as)
			continue
		}
		p.addMatcher(m[2], end+1, "That", as[0], p.matcherExpr(as[1]))
	}
}

var thatRe = regexp.MustCompile(`\b(Assert\.That)\s*\(`)

// ---- assert statements ----------------------------------------------------------------

var (
	pyAssertRe = regexp.MustCompile(`^\s*(assert)\b\s*(.*)$`)
	rbAssertRe = regexp.MustCompile(`^\s*(assert\w*|refute\w*)\s+([^\s(=].*)$`)
)

// keywords handles assertion statements: Python `assert cond, msg`, minitest
// `assert_equal a, b` and Elixir `assert a == b` (no parentheses).
func (p *parser) keywords() {
	var m []int
	switch p.lg {
	case lang.Python:
		m = pyAssertRe.FindStringSubmatchIndex(p.vt)
	case lang.Ruby, lang.Elixir:
		m = rbAssertRe.FindStringSubmatchIndex(p.vt)
	}
	if m == nil {
		return
	}
	name, rest := p.vt[m[2]:m[3]], strings.TrimSpace(p.vt[m[4]:m[5]])
	if rest == "" {
		return
	}
	// assert(x) and assert (x, "msg") need no special care: splitTop keeps
	// the parenthesized group whole and newExpr keeps a tuple a tuple.
	var as []expr
	for _, part := range splitTop(strings.TrimSuffix(rest, " do"), ',') {
		as = append(as, newExpr(part))
	}
	switch name {
	case "assert":
		if p.lg != lang.Python && len(as) > 0 && strings.Contains(as[0].raw, " = ") && !strings.ContainsAny(as[0].raw, "!<>") {
			// Elixir pattern match: assert {:ok, _} = result
			if lhs, rhs, ok := strings.Cut(as[0].raw, " = "); ok && !strings.HasSuffix(lhs, "=") && !strings.HasPrefix(rhs, "=") {
				p.add(assertion{start: m[2], end: len(p.vt), fn: name, str: checksValue, subjects: []expr{newExpr(rhs)}})
				return
			}
		}
		p.addCall(m[2], len(p.vt), name, "", callSpec{passTruthy, checksExist, 1}, as)
	case "refute":
		p.addCall(m[2], len(p.vt), name, "", callSpec{passFalsy, checksOther, 1}, as)
	default:
		if spec, ok := anyRecv[name]; ok {
			p.addCall(m[2], len(p.vt), name, "", spec, as)
		}
	}
}

// ---- FluentAssertions and kotest --------------------------------------------------------

var (
	shouldRe      = regexp.MustCompile(`\.Should\(\)`)
	stmtStartRe   = regexp.MustCompile(`^\s*(?:await\s+)?`)
	kotestInfixRe = regexp.MustCompile(`^\s*(.+?)\s+(shouldBe|shouldNotBe|shouldEqual|shouldHaveSize|shouldContain|shouldBeEqualTo|shouldStartWith|shouldContainExactly)\s+(.+?)\s*$`)
	kotestPostRe  = regexp.MustCompile(`^\s*(.+?)\.(shouldNotBeNull|shouldBeNull|shouldBeTrue|shouldBeFalse|shouldNotBeEmpty|shouldBeEmpty)\(\)\s*$`)
	kotestThrowRe = regexp.MustCompile(`\b(shouldThrow|shouldThrowExactly|shouldThrowAny|assertFailsWith|assertThrows)\s*(?:<([^>]*)>)?\s*\{`)
)

// fluent handles FluentAssertions: result.Should().Be(42).
func (p *parser) fluent() {
	if p.lg != lang.CSharp {
		return
	}
	loc := shouldRe.FindStringIndex(p.vt)
	if loc == nil {
		return
	}
	pre := stmtStartRe.FindString(p.vt[:loc[0]])
	subject := newExpr(p.vt[len(pre):loc[0]])
	links, end := p.chain(p.vt, loc[1])
	if len(links) == 0 {
		return
	}
	r := p.interpret(links)
	p.addMatcher(len(pre), end, r.name, subject, r)
}

// kotest handles kotest infix and extension assertions.
func (p *parser) kotest() {
	if p.lg != lang.Kotlin {
		return
	}
	if m := kotestThrowRe.FindStringSubmatchIndex(p.vt); m != nil {
		a := assertion{start: m[2], end: m[1], fn: p.vt[m[2]:m[3]], str: checksThrow}
		if m[4] >= 0 && a.fn != "shouldThrowAny" {
			a.exc = exceptionName(p.vt[m[4]:m[5]])
		}
		p.add(a)
		return
	}
	if m := kotestInfixRe.FindStringSubmatch(p.vt); m != nil {
		subj, other := newExpr(m[1]), newExpr(m[3])
		a := assertion{start: strings.Index(p.vt, m[1]), end: len(p.vt), fn: m[2], values: []expr{subj, other}, subjects: []expr{subj}}
		switch m[2] {
		case "shouldBe", "shouldEqual", "shouldBeEqualTo":
			a.pass, a.str, a.subjects = passEqual, checksValue, []expr{subj, other}
		case "shouldNotBe":
			a.pass, a.str = passNotEqual, checksOther
			if lit(other, p.lg).kind == litNull {
				a.pass, a.str, a.values = passNotNull, checksExist, []expr{subj}
			}
		default:
			a.str = checksValue
		}
		p.add(a)
		return
	}
	if m := kotestPostRe.FindStringSubmatch(p.vt); m != nil {
		subj := newExpr(m[1])
		info := map[string]matcherInfo{
			"shouldNotBeNull": {passNotNull, checksExist}, "shouldBeNull": {passNull, checksOther},
			"shouldBeTrue": {passTruthy, checksExist}, "shouldBeFalse": {passFalsy, checksOther},
			"shouldNotBeEmpty": {passUnknown, checksExist}, "shouldBeEmpty": {passUnknown, checksOther},
		}[m[2]]
		p.add(assertion{start: strings.Index(p.vt, m[1]), end: len(p.vt), fn: m[2], pass: info.pass, str: info.str, values: []expr{subj}})
	}
}

// ---- exception attributes -------------------------------------------------------------------

var (
	shouldPanicRe   = regexp.MustCompile(`#\[\s*should_panic\b(\s*\(\s*expected\s*=)?`)
	junitExpectedRe = regexp.MustCompile(`@Test\s*\(\s*expected\s*=\s*([\w.]+)`)
)

// throwAttrs handles exception expectations written as attributes: Rust
// #[should_panic(expected = "...")] and JUnit 4 @Test(expected = X.class).
func (p *parser) throwAttrs() {
	switch p.lg {
	case lang.Rust:
		if m := shouldPanicRe.FindStringSubmatchIndex(p.vt); m != nil {
			p.add(assertion{start: m[0], end: len(p.vt), fn: "#[should_panic]", str: checksThrow, exc: "panic", match: m[2] >= 0})
		}
	case lang.Java, lang.Kotlin, lang.Groovy:
		if m := junitExpectedRe.FindStringSubmatchIndex(p.vt); m != nil {
			p.add(assertion{start: m[0], end: m[1], fn: "@Test(expected)", str: checksThrow, exc: exceptionName(p.vt[m[2]:m[3]])})
		}
	}
}

// ---- comparisons ----------------------------------------------------------------------------

var compareOps = []string{"===", "!==", "==", "!=", "<=", ">=", " is not ", " is ", " not in ", " in ", " eq ", "<", ">"}

// splitCompare splits an expression at its only top-level comparison:
// "a == b" → ("a", "==", "b"). It refuses expressions with several
// comparisons or with top-level boolean operators.
func splitCompare(s string) (lhs, op, rhs string, ok bool) {
	depth := 0
	found := -1
	var fop string
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', '[', '{':
			depth++
			continue
		case ')', ']', '}':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		rest := s[i:]
		if strings.HasPrefix(rest, "->") {
			i++ // PHP property access, not a comparison
			continue
		}
		if strings.HasPrefix(rest, "&&") || strings.HasPrefix(rest, "||") || strings.HasPrefix(rest, " and ") ||
			strings.HasPrefix(rest, " or ") || strings.HasPrefix(rest, " ? ") || strings.HasPrefix(rest, "=>") ||
			strings.HasPrefix(rest, "<<") || strings.HasPrefix(rest, ">>") || strings.HasPrefix(rest, "<-") ||
			strings.HasPrefix(rest, "<=>") {
			return "", "", "", false
		}
		for _, o := range compareOps {
			if !strings.HasPrefix(rest, o) {
				continue
			}
			if o == "<" || o == ">" {
				if i > 0 && strings.ContainsRune("=-<>", rune(s[i-1])) {
					break
				}
			}
			if found >= 0 {
				return "", "", "", false // a chained or nested comparison
			}
			found, fop = i, o
			i += len(o) - 1
			break
		}
	}
	if found < 0 {
		return "", "", "", false
	}
	lhs, rhs = strings.TrimSpace(s[:found]), strings.TrimSpace(s[found+len(fop):])
	if lhs == "" || rhs == "" {
		return "", "", "", false
	}
	return lhs, strings.TrimSpace(fop), rhs, true
}
