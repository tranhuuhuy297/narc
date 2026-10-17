package accounting

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// A test that goes away with what it exercised is routine when the old
// test could no longer compile or run against the new code: it used a
// field, parameter, function or import that the change removed, it passed
// a null-like input the change took out of a type (the API was narrowed),
// or it checked the error of a Go function that no longer returns one. A
// test of a log line or text that the change stopped emitting goes with
// it too, and so do the tests a labelled revert takes out together with
// the commit it undoes.
//
// Removing an error check, a guard or a branch together with the test
// that exercised it is not on this list: the old test still compiles and
// fails, and deleting it is exactly the "break the code, delete the test"
// move.

var (
	// revertSubjectRe matches the subject of a labelled revert: git's
	// `Revert "..."`, and the conventional-commit type, with or without a
	// scope or a leading emoji (`revert(topic): ...`, `⏪ revert: ...`).
	revertSubjectRe = regexp.MustCompile(`(?i)^\W*revert(?:\([^)]*\))?!?:\s|^Revert\s+"`)
	// revertBodyRe matches the line git writes into a revert's message.
	revertBodyRe = regexp.MustCompile(`(?m)^This reverts commit [0-9a-f]{7,40}\b`)
)

// revert reports a change that is one commit labelled as a revert.
func (l *ledger) revert() bool {
	msg := l.a.Message
	if msg == "" {
		return false
	}
	subject, _, _ := strings.Cut(msg, "\n")
	return revertSubjectRe.MatchString(strings.TrimSpace(subject)) || revertBodyRe.MatchString(msg)
}

// revertNote is appended to findings in a labelled revert.
const revertNote = "; the change is a labelled revert, which takes tests out together with the code they covered"

// pathGone returns why old test o went away with what it exercised, or
// "": it used a name the change removed from production code, it passed a
// null-like input that the change took out of a type, or it expected a
// log line or text that the change removed. A message counts only for a
// test that expects no error (see the top of this file).
func (l *ledger) pathGone(o *instance) string {
	if len(l.sourceChanged) == 0 {
		return ""
	}
	m := l.source()
	if name := l.usesRemoved(o); name != "" {
		return fmt.Sprintf("; it used %s, which the same change removed from the source code", name)
	}
	if len(m.typeNullsOut) == 0 && len(m.messagesOut) == 0 {
		return ""
	}
	message, expectsError := "", false
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		ll := o.seg.lines[rel-1]
		if k := nullInputRe.FindStringSubmatch(ll.Code); k != nil && m.typeNullsOut[k[1]] {
			return fmt.Sprintf("; it passed %s, which the types the change edited no longer accept", k[1])
		}
		expectsError = expectsError || errorExpectRe.MatchString(ll.Code)
		if message == "" {
			message = messageIn(ll.Raw, m.messagesOut)
		}
	}
	if message != "" && !expectsError {
		return fmt.Sprintf("; it expected %q, which the same change removed from the source code", clipMsg(message))
	}
	return ""
}

// nullInputRe captures a null-like keyword passed as input: an argument
// (`resolve(undefined)`), a keyword argument (`workflow_id=None`) or an
// object field (`{ chatID: undefined }`).
var nullInputRe = regexp.MustCompile(`(?:[(,]|\b\w+\s*[=:])\s*(undefined|null|None|nil)\s*[,)}]`)

// sitesGone returns why every lost assertion of a drop went with what it
// checked, or "": each used a name the change removed from production
// code, checked the error of a Go function that no longer returns one,
// expected a value or a log line the change dropped from production code,
// or passed a null-like input the change took out of a type.
func (l *ledger) sitesGone(d drop) string {
	if len(l.sourceChanged) == 0 || len(d.lost) == 0 {
		return ""
	}
	m := l.source()
	why := ""
	for _, s := range d.lost {
		if name := removedIn(s.code, m.removedNames); name != "" {
			why = fmt.Sprintf("; they used %s, which the same change removed from the source code", name)
			continue
		}
		if v := droppedValueIn(s, m.valuesDropped); v != "" {
			why = fmt.Sprintf("; they expected %q, which the same change dropped from the source code", clipMsg(v))
			continue
		}
		if fn := l.errorCheckOf(d.old, s); fn != "" {
			why = fmt.Sprintf("; they checked the error of %s, which no longer returns one", fn)
			continue
		}
		if k := nullInputRe.FindStringSubmatch(s.code); k != nil && m.typeNullsOut[k[1]] {
			why = fmt.Sprintf("; they passed %s, which the types the change edited no longer accept", k[1])
			continue
		}
		if errorExpectRe.MatchString(s.code) {
			return ""
		}
		if msg := messageIn(s.raw, m.messagesOut); msg != "" {
			why = fmt.Sprintf("; they expected %q, which the same change removed from the source code", clipMsg(msg))
			continue
		}
		return ""
	}
	return why
}

// droppedValueIn returns a string literal of lost site s that the change
// dropped from production code (see sourceModel.valuesDropped), or "".
func droppedValueIn(s site, dropped map[string]bool) string {
	if len(dropped) == 0 {
		return ""
	}
	lits, _ := distinctive(s.toks)
	for _, v := range lits {
		if strings.HasPrefix(v, `"`) && dropped[v[1:]] {
			return v[1:]
		}
	}
	return ""
}

// usesRemoved returns a name that old test o uses and that the change
// removed from production code (see sourceModel.removedNames), or "".
func (l *ledger) usesRemoved(o *instance) string {
	names := l.source().removedNames
	if len(names) == 0 {
		return ""
	}
	// Names the test binds itself (`rule = make_rule()`) are its own.
	local := map[string]bool{}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		if m := assignRe.FindStringSubmatch(o.seg.lines[rel-1].Code); m != nil {
			local[m[1]] = true
		}
	}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		for _, id := range idents(o.seg.lines[rel-1].Code) {
			if names[id] && !local[id] && id != o.block.Bare {
				return id
			}
		}
	}
	return ""
}

// removedIn returns the first identifier of code that names holds.
func removedIn(code string, names map[string]bool) string {
	if len(names) == 0 {
		return ""
	}
	for _, id := range idents(code) {
		if names[id] {
			return id
		}
	}
	return ""
}

var (
	// goErrCheckRe matches a Go test's check that err is nil.
	goErrCheckRe = regexp.MustCompile(`\b(?:t|b|tb)\s*\.\s*(?:Fatal|Fatalf|Error|Errorf)\s*\(.*\berr\b|\b(?:require|assert)\s*\.\s*NoError\s*\(\s*t\s*,\s*err\s*[,)]`)
	// goErrAssignRe matches a Go statement that assigns err from a call.
	goErrAssignRe = regexp.MustCompile(`\berr\s*:?=\s*(?:[\w.]+\.)?([A-Za-z_]\w*)\s*\(`)
)

// errorCheckOf returns, when lost site s of old test o checks a Go err
// that o assigned from a function the change made stop returning an
// error, that function; else "". The check could not compile any more.
func (l *ledger) errorCheckOf(o *instance, s site) string {
	dropped := l.source().errorReturnDropped
	if len(dropped) == 0 || o.seg.lang != lang.Go || !goErrCheckRe.MatchString(s.code) {
		return ""
	}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		if m := goErrAssignRe.FindStringSubmatch(o.seg.lines[rel-1].Code); m != nil && dropped[m[1]] {
			return m[1]
		}
	}
	return ""
}

// errorExpectRe matches a test line that expects an error.
var errorExpectRe = regexp.MustCompile(`\b(?:pytest\s*\.\s*raises|assertRaises\w*|assertThrows|expectThrows|toThrow\w*|rejects|should_panic|expect_err|unwrap_err|is_err|ErrorIs|ErrorAs|ErrorContains|EqualError|raise_error|assert_raises|shouldThrow\w*|assertFailsWith)\b|\b(?:require|assert)\s*\.\s*Error\s*\(`)

// messageIn returns the first message of msgs that text contains, or "".
func messageIn(text string, msgs []string) string {
	if len(msgs) == 0 || len(text) < 12 {
		return ""
	}
	for _, msg := range msgs {
		if strings.Contains(text, msg) {
			return msg
		}
	}
	return ""
}

// clipMsg shortens a message for a finding.
func clipMsg(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:57] + "..."
}
