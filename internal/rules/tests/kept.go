package tests

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// A replacement block is compared line by line, but a test that was
// reworked often keeps its specific checks a few lines away from where the
// diff pairs them: after an existence guard, in another hunk of the same
// test, on a subject that was renamed, or through a variable derived from
// the subject. The checks below look at every assertion the change added
// to the same test before calling an old check weakened.

// sameTest gives the assertions the change added to each test of a file,
// grouped on first use.
type sameTest struct {
	f      *core.FileInfo
	added  map[int][]assertion // added line → its assertions (see checkWeakened)
	byTest map[string][]located
	lines  map[string][]core.Line // test name → its added lines
}

func (s *sameTest) group() {
	if s.byTest != nil {
		return
	}
	s.byTest, s.lines = map[string][]located{}, map[string][]core.Line{}
	for _, l := range s.f.Added {
		name := testAround(s.f, l.No).name
		s.lines[name] = append(s.lines[name], l)
		for _, a := range s.added[l.No] {
			s.byTest[name] = append(s.byTest[name], located{assertion: a, line: statement(s.f, l, newVersion)})
		}
	}
}

// keptIn reports whether the test that new assertion n belongs to still
// checks what old assertion o checked, in an assertion the change added
// to it: a value check on a related subject (anywhere in the test, not
// only in o's replacement block), the same expectation on a renamed
// subject, a presence check inverted into an absence check, or a value
// check on a variable the change derived from o's subject.
func (s *sameTest) keptIn(o, n located) bool {
	s.group()
	name := testAround(s.f, n.line.No).name
	group := s.byTest[name]
	if replacedBySpecific(o, group) {
		return true
	}
	for _, g := range group {
		if g.helper {
			continue
		}
		if sameExpectation(o.assertion, g.assertion) || invertedPresence(o.assertion, g.assertion) {
			return true
		}
	}
	return derivedCheck(o.assertion, s.lines[name], group)
}

// sameExpectation reports whether n makes o's check on a renamed subject:
// the same matcher and the same distinctive literals, on subjects that end
// in the same member (result.mcpServers["sse"].url → result["sse"].url,
// "a" in query["kpis"][0].split(",") → "a" in kpis.split(",")).
func sameExpectation(o, n assertion) bool {
	if o.str != checksValue || n.str != checksValue || o.fn != n.fn {
		return false
	}
	olits, oexprs := operands(o)
	nlits, nexprs := operands(n)
	if len(olits) == 0 || len(olits) != len(nlits) || len(oexprs) == 0 {
		return false
	}
	distinct := false
	for i := range olits {
		if olits[i].norm != nlits[i].norm {
			return false
		}
		distinct = distinct || distinctiveLiteral(olits[i])
	}
	if !distinct {
		return false
	}
	for _, x := range oexprs {
		found := false
		for _, y := range nexprs {
			if t := memberTail(x); t != "" && t == memberTail(y) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// operands splits the values and subjects of a (without repeats) into
// literals, in order, and other expressions.
func operands(a assertion) (lits, exprs []expr) {
	seen := map[string]bool{}
	for _, e := range append(append([]expr(nil), a.values...), a.subjects...) {
		if e.norm == "" || seen[e.norm] {
			continue
		}
		seen[e.norm] = true
		if isLiteral(e) {
			lits = append(lits, e)
		} else {
			exprs = append(exprs, e)
		}
	}
	return lits, exprs
}

// distinctiveLiteral reports a literal specific enough to identify a
// check: a string of three or more characters, or a number other than
// 0, 1 and -1.
func distinctiveLiteral(e expr) bool {
	l := lit(e, lang.Unknown)
	switch l.kind {
	case litNumber:
		return l.num != 0 && l.num != 1 && l.num != -1
	case litString:
		s, ok := literalString(e.raw)
		return ok && len(decodeLiteral(s)) >= 3
	}
	return false
}

var (
	// quotedRe matches a string literal in value text (contents encoded).
	quotedRe = regexp.MustCompile("\"[^\"]*\"|'[^']*'|`[^`]*`")
	nameRe   = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
)

// memberTail returns the last identifier of an expression outside its
// string literals: url in result?.["sse"].url, split in kpis.split(",").
func memberTail(e expr) string {
	ids := nameRe.FindAllString(quotedRe.ReplaceAllString(e.raw, `""`), -1)
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}

// negationRe finds the negation in an absence check that inverts a
// presence check: `x not in y`, `.not.toContain(x)`, assertNotIn,
// NotContains.
var negationRe = regexp.MustCompile(`\bnot\s+in\b|\.\s*not\s*\.\s*(?:toContain\w*|toHaveProperty|toHaveAttribute|toHaveClass|toHaveKey|toMatch\w*|toInclude)\b|\bassertNotIn\b|\bNotContains\b|\bNotSubset\b|\bnot_to\s+include\b|\brefute_includes\b`)

// invertedPresence reports whether n is presence check o turned into an
// absence check on the same values: 'mcpServers' in cfg → 'mcpServers'
// not in cfg. The expectation was inverted on purpose, not loosened.
func invertedPresence(o, n assertion) bool {
	loc := negationRe.FindStringIndex(n.text)
	if loc == nil || negationRe.MatchString(o.text) {
		return false
	}
	neg := n.text[loc[0]:loc[1]]
	var plain string
	switch {
	case strings.HasPrefix(neg, "not") && strings.HasSuffix(neg, "in"):
		plain = "in"
	case strings.HasPrefix(neg, "."):
		plain = "." + strings.TrimSpace(neg[strings.LastIndex(neg, ".")+1:])
	case neg == "assertNotIn":
		plain = "assertIn"
	case neg == "NotContains":
		plain = "Contains"
	case neg == "NotSubset":
		plain = "Subset"
	case neg == "refute_includes":
		plain = "assert_includes"
	default:
		plain = "to include"
	}
	flipped := n.text[:loc[0]] + plain + n.text[loc[1]:]
	return rulekit.Normalize(flipped) == rulekit.Normalize(o.text)
}

// derivedAssignRe captures a variable assigned on a line, and what it is
// assigned: `const match = vi.mocked(mutate).mock.calls.at(-1)![0]`,
// `tpl = slack ? getDefaultTemplate(slack) : undefined`.
var derivedAssignRe = regexp.MustCompile(`^\s*(?:(?:const|let|var|val|final|auto)\s+)?([A-Za-z_$][\w$]*)\s*(?::[^=]+?)?\s*:?=\s*([^=>].*)$`)

// derivedCheck reports whether a value check the change added to the
// test reads a variable that the change derived from o's subject (or from
// the object holding it), so that the guard the diff paired with o is
// followed by checks on what o checked:
//
//	expect(mutate).toHaveBeenCalledWith(key)   →   expect(mutate).toHaveBeenCalled()
//	                                               const match = vi.mocked(mutate).mock.calls.at(-1)![0]
//	                                               expect(match(key)).toBe(true)
//
// A variable derived from the object holding the subject must be read at
// the same member (slack.template.kind → tpl.kind with tpl derived from
// slack), so that a sibling field is not taken for the one o checked.
func derivedCheck(o assertion, lines []core.Line, group []located) bool {
	if o.str != checksValue || len(o.subjects) == 0 {
		return false
	}
	subj := o.subjects[0]
	if isLiteral(subj) || subj.norm == "" {
		return false
	}
	whole := wholeRe(optionalChains(subj.norm))
	var holders []*regexp.Regexp
	for _, h := range holdersOf(subj.norm) {
		holders = append(holders, wholeRe(h))
	}
	tail := memberTail(subj)
	derived := map[string]bool{} // variable → read at the subject's member only
	for _, l := range lines {
		m := derivedAssignRe.FindStringSubmatch(l.Code)
		if m == nil {
			continue
		}
		rhs := optionalChains(rulekit.Normalize(m[2]))
		switch {
		case whole.MatchString(rhs):
			derived[m[1]] = false
		default:
			for _, h := range holders {
				if h.MatchString(rhs) {
					if _, ok := derived[m[1]]; !ok {
						derived[m[1]] = true
					}
					break
				}
			}
		}
	}
	if len(derived) == 0 {
		return false
	}
	for _, g := range group {
		// (A predicate the variable answers, expect(match(key)).toBe(true),
		// checks a value too.)
		specific := g.str == checksValue || g.str == checksOther && g.pass == passEqual
		if g.helper || !specific || len(g.values) == 0 {
			continue
		}
		s := g.values[0]
		if strings.HasPrefix(s.raw, "typeof ") {
			continue // only the type of the variable
		}
		root := nameRe.FindString(s.raw)
		atMember, ok := derived[root]
		if !ok || !strings.HasPrefix(s.raw, root) {
			continue
		}
		if !atMember || memberTail(s) == tail && s.raw != root {
			return true
		}
	}
	return false
}

// holdersOf returns the objects that hold the member a normalized subject
// reads, innermost first: res.body.total → res.body, res. Dots inside
// brackets do not count: a.get(b.c).d → a.get(b.c), a.
func holdersOf(norm string) []string {
	norm = optionalChains(norm)
	var out []string
	depth := 0
	for i := len(norm) - 1; i > 0; i-- {
		switch norm[i] {
		case ')', ']', '}':
			depth++
		case '(', '[', '{':
			depth--
		case '.':
			if depth == 0 {
				out = append(out, norm[:i])
			}
		}
	}
	return out
}

// optionalChains normalizes optional chaining and non-null assertions to
// plain member access: a?.b → a.b, a?.["b"] → a["b"], a!.b → a.b.
func optionalChains(s string) string { return chainReplacer.Replace(s) }

var chainReplacer = strings.NewReplacer("?.[", "[", "?.(", "(", "?.", ".", "!.", ".", "![", "[")

// wholeRe matches s as a whole expression inside another: not preceded by
// a word character or a member dot, not followed by a word character.
func wholeRe(s string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\w$.])` + regexp.QuoteMeta(s) + `(?:[^\w$]|$)`)
}
