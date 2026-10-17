package tests

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

var assertionCommented = &core.Rule{
	ID:       "assertion-commented",
	Title:    "Assertion commented out",
	Category: "tests",
	Severity: core.Error,
	Summary:  "A live assertion was turned into a comment (// expect(...), # assert ..., /* assertEquals(...) */).",
	Hint: "Restore the assertion and fix the code under test (or the expectation, if it is genuinely wrong). A " +
		"commented-out assertion checks nothing but still looks like it does.",
	Docs: "Commenting an assertion out is the quietest way to make a test pass: the test still runs, the assertion " +
		"is still visible in the file, and nothing is checked.\n\n" +
		"narc flags added comments that contain an assertion which was live code before the change, in the same " +
		"file, and which is no longer live anywhere in it. Comments that only describe a check, assertions that " +
		"were already commented out, and assertions that moved elsewhere are not reported. A comment that links " +
		"an issue, or an assertion replaced next to it by a new one on the same value, lowers the finding to a " +
		"warning.\n\n" +
		"It also catches the quiet variant that leaves the assertion lines untouched: a new `/* ... */` block " +
		"(Ruby `=begin`/`=end`, Python's bare triple-quoted string) opened above and closed below unchanged " +
		"assertions. Assertions split over several lines by a formatter are matched as one statement.\n\n" +
		"```ts\n// bad\n// expect(invoice.total).toBe(107)\nexpect(invoice).toBeDefined()\n```",
}

func init() {
	assertionCommented.Check = checkCommented
	core.Register(assertionCommented)
}

var (
	// goFailRe matches Go's hand-written assertions: if got != want { t.Errorf(...) }.
	goFailRe = regexp.MustCompile(`\b(?:t|b|tb|tt|s\.T\(\))\.(?:Error|Errorf|Fatal|Fatalf|Fail|FailNow)\s*\(`)
	// mockVerifyRe matches mock verifications, which are assertions too.
	mockVerifyRe = regexp.MustCompile(`\.assert_(?:called\w*|any_call|not_called|has_calls)\s*\(|\bverify\s*\(|\bverify\s*\{|\bcoVerify\s*\{`)
	// commentLeadRe strips what commonly precedes commented-out code in a
	// comment: extra markers, bullets and labels such as "TODO:".
	commentLeadRe = regexp.MustCompile(`^(?:[/*#!\-\s]+)?(?:(?:TODO|FIXME|XXX|HACK|NOTE|disabled|temporarily disabled|flaky)\b[^:]*:\s*)?`)
)

// isAssertion reports whether a line of code asserts something.
func isAssertion(lg lang.Lang, l core.Line) bool {
	if goFailRe.MatchString(l.Code) || mockVerifyRe.MatchString(l.Code) {
		return true
	}
	return len(parseAssertions(lg, l)) > 0
}

// liveAssertion is an assertion removed from a file, waiting to be matched
// with a comment that resurrects it.
type liveAssertion struct {
	line core.Line
	// stmt is the whole statement, when line only continues one that
	// starts above it (`.toBe(107);` under `expect(total)`).
	stmt core.Line
	key  string // normalized code of line, strings intact
	used bool
}

// keyPrefix is how many bytes of a normalized assertion index it: a comment
// can only resurrect assertions whose key starts like the comment.
const keyPrefix = 6

func checkCommented(a *core.Analysis) []core.Finding {
	files := testFiles(a)
	// moved counts live lines that some file gained beyond what it lost: an
	// assertion moved to another file, which may leave a comment behind
	// ("// expect(x).toBe(1) — moved to b.test.ts") without being disabled.
	moved := map[string]int{}
	for _, f := range files {
		net := map[string]int{}
		for _, l := range f.Added {
			if strings.TrimSpace(l.Code) != "" {
				net[rulekit.Normalize(l.NoComment)]++
			}
		}
		for _, l := range f.Removed {
			if strings.TrimSpace(l.Code) != "" {
				net[rulekit.Normalize(l.NoComment)]--
			}
		}
		for k, n := range net {
			if n > 0 {
				moved[k] += n
			}
		}
	}
	var out []core.Finding
	for _, f := range files {
		out = append(out, commentedIn(f, moved)...)
		out = append(out, wrappedIn(f)...)
	}
	return out
}

func commentedIn(f *core.FileInfo, moved map[string]int) []core.Finding {
	byPrefix := map[string][]*liveAssertion{}
	wasComment := map[string]int{} // commented-out code that existed before
	n := 0
	for _, l := range f.Removed {
		if c, _ := commentedCode(f.Lang, l); c != "" {
			wasComment[c]++
		}
		if strings.TrimSpace(l.Code) == "" {
			continue
		}
		stmt := chained(f, l, oldVersion)
		if !isAssertion(f.Lang, stmt) {
			continue
		}
		if key := rulekit.Normalize(l.NoComment); len(key) >= keyPrefix {
			byPrefix[key[:keyPrefix]] = append(byPrefix[key[:keyPrefix]], &liveAssertion{line: l, stmt: stmt, key: key})
			n++
		}
	}
	if n == 0 {
		return nil
	}
	// lost counts, per assertion, the live copies the change removed
	// (removed minus re-added here or moved to another file), so an
	// assertion repeated in several tests is still caught when one copy is
	// commented out.
	lost := map[string]int{}
	for _, rs := range byPrefix {
		for _, r := range rs {
			lost[r.key]++
		}
	}
	for _, l := range f.Added {
		if strings.TrimSpace(l.Code) != "" {
			if k := rulekit.Normalize(l.NoComment); lost[k] > 0 {
				lost[k]--
			}
		}
	}
	for k, n := range lost {
		take := min(n, moved[k])
		lost[k] -= take
		moved[k] -= take
	}
	var out []core.Finding
	var oldBlock, newBlock map[int]int // computed on the first finding
	var addedAt map[int]core.Line
	for _, l := range f.Added {
		if f.Lang == lang.Rust && rustDocComment(l.Raw) {
			continue // /// and //! examples are doctests: they run
		}
		c, inString := commentedCode(f.Lang, l)
		if len(c) < keyPrefix {
			continue
		}
		if wasComment[c] > 0 {
			wasComment[c]-- // a comment that was moved or re-indented
			continue
		}
		for _, r := range byPrefix[c[:keyPrefix]] {
			if r.used || lost[r.key] <= 0 || !resurrects(c, r.key) {
				continue
			}
			r.used = true
			lost[r.key]--
			if oldBlock == nil {
				oldBlock, newBlock = blocks(f)
				addedAt = map[int]core.Line{}
				for _, al := range f.Added {
					addedAt[al.No] = al
				}
			}
			var repl *located
			if b := newBlock[l.No]; b != 0 && b == oldBlock[r.line.No] {
				repl = replacementOf(f, r.line, l.No, func(n int) (core.Line, bool) {
					al, ok := addedAt[n]
					return al, ok && newBlock[n] == b
				})
			}
			out = append(out, commentedFinding(f, l, r, repl, inString))
			break
		}
	}
	return out
}

// rustDocComment reports whether a Rust line is a doc comment (/// or //!),
// whose code examples cargo test runs as doctests.
func rustDocComment(raw string) bool {
	t := strings.TrimSpace(raw)
	return strings.HasPrefix(t, "///") || strings.HasPrefix(t, "//!")
}

// replacementWindow is how far from the comment (in new lines) a
// replacement assertion is looked for.
const replacementWindow = 5

// replacementOf returns a live assertion added next to the comment on new
// line at, in the same replacement block as the removed assertion line old
// (added reports the block's added lines), that checks the same value at
// least as specifically: the expectation was changed, and the comment keeps
// the old one for reference.
func replacementOf(f *core.FileInfo, old core.Line, at int, added func(int) (core.Line, bool)) *located {
	olds := parseAssertions(f.Lang, chained(f, old, oldVersion))
	if len(olds) == 0 {
		return nil
	}
	for n := at - replacementWindow; n <= at+replacementWindow; n++ {
		// The comment's own line counts too: `expect(x).toBe(2); // expect(x).toBe(1)`.
		l, ok := added(n)
		if !ok || strings.TrimSpace(l.Code) == "" {
			continue
		}
		for _, n := range parseAssertions(f.Lang, chained(f, l, newVersion)) {
			for _, o := range olds {
				if related(o, n) && (n.str == checksValue || n.str == o.str && o.str != checksOther) {
					return &located{assertion: n, line: l}
				}
			}
		}
	}
	return nil
}

func commentedFinding(f *core.FileInfo, l core.Line, r *liveAssertion, repl *located, inString bool) core.Finding {
	how := "commented out"
	if inString {
		how = "disabled inside a string literal"
	}
	msg := fmt.Sprintf("assertion %s: %s", how, clip(strings.TrimRight(strings.TrimSpace(r.stmt.NoComment), ";"), 100))
	if name := testAround(f, l.No); name.ok() {
		msg += fmt.Sprintf(" in %s", name)
	}
	fd := assertionCommented.Finding(f, l.No, msg)
	fd.Before = strings.TrimSpace(r.line.Raw)
	switch {
	case repl != nil:
		fd.Severity = core.Warning
		fd.Message += fmt.Sprintf(" (replaced by %s)", repl.assertion)
		fd.Hint = "The old assertion was replaced by a new one on the same value and kept as a comment. If the " +
			"expectation really changed, explain why and delete the commented-out line (version control keeps it); " +
			"if the code under test is wrong, restore the original assertion and fix the code."
	case issueRef(f, l, ""):
		fd.Severity = core.Warning
		fd.Message += " (the comment references an issue)"
		fd.Hint = "The comment links an issue, so this may be deliberate. Track it and restore the assertion as soon " +
			"as the issue is fixed; prefer a skip with a reason over a silently disabled check."
	}
	return fd
}

// commentedCode returns the normalized comment text of a line with leading
// markers and labels removed, or "". In Python, the text of a line inside a
// multi-line string counts too (inString): a bare triple-quoted string is
// the idiomatic way to disable a block of code.
func commentedCode(lg lang.Lang, l core.Line) (code string, inString bool) {
	c := strings.TrimSpace(l.Comment)
	if c == "" {
		if lg == lang.Python && strings.TrimSpace(l.Code) == "" && strings.TrimSpace(l.NoComment) != "" {
			return rulekit.Normalize(l.NoComment), true
		}
		return "", false
	}
	c = commentLeadRe.ReplaceAllString(c, "")
	return rulekit.Normalize(c), false
}

// resurrects reports whether comment c contains the assertion whose
// normalized code is key, possibly followed by more commentary.
func resurrects(c, key string) bool {
	if !strings.HasPrefix(c, key) {
		return false
	}
	if len(c) == len(key) {
		return true
	}
	next := c[len(key)]
	return !isWordByte(next) && next != '.' && next != '(' && next != '[' && next != '"'
}
