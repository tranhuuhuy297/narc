package tests

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
)

// An assertion may lose its specific check because the change removed what
// it checked from production code: the variable it compared against no
// longer exists (the old assertion would not compile), or the value it
// expected is no longer produced (a class name, a message). The test should
// then assert the new behavior, but the check went with code that was
// changed on purpose, which is worth a note, not a warning.

// goneKey is the Analysis.Memo key of the removed-source model.
type goneKey struct{}

// goneSource holds what the change removed from production code.
type goneSource struct {
	// names declared on removed production lines that nothing mentions
	// after the change (no added line, no new version of a changed file).
	names map[string]bool
	// values are the string literals (and their space-separated words) of
	// removed production lines that no added production line has.
	values map[string]bool
}

var (
	// declNameRes find the names a line of code declares: functions,
	// types, variables and constants, assignments, typed fields.
	declNameRes = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:func|def|function|fn|fun|class|struct|enum|interface|type|trait|const|let|var|val|static|final)\s+(?:\([^)]*\)\s*)?([A-Za-z_$][\w$]*)`),
		regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*:?=[^=]`),
		regexp.MustCompile(`^\s*(?:readonly\s+)?([A-Za-z_$][\w$]*)\s*[?!]?\s*:\s*[\w\[\]<>|. ]+[;,]?\s*$`),
	}
)

func goneFromSource(a *core.Analysis) *goneSource {
	return a.Memo(goneKey{}, func() any {
		g := &goneSource{names: map[string]bool{}, values: map[string]bool{}}
		declared := map[string]bool{}
		valuesIn := map[string]bool{}
		for _, f := range a.Files {
			if f.IsTest || !f.Lang.IsProgramming() {
				continue
			}
			for _, l := range f.Removed {
				for _, re := range declNameRes {
					for _, m := range re.FindAllStringSubmatch(l.Code, -1) {
						if compoundName(m[1]) {
							declared[m[1]] = true
						}
					}
				}
				for _, s := range l.Strings {
					for _, v := range literalWords(s) {
						g.values[v] = true
					}
				}
			}
			for _, l := range f.Added {
				for _, s := range l.Strings {
					for _, v := range literalWords(s) {
						valuesIn[v] = true
					}
				}
			}
		}
		for v := range valuesIn {
			delete(g.values, v)
		}
		if len(declared) == 0 {
			return g
		}
		// What still mentions a declared name: the added lines, and the new
		// version of every changed file.
		alive := map[string]bool{}
		for _, f := range a.Files {
			for _, l := range f.Added {
				for _, id := range nameRe.FindAllString(l.Code, -1) {
					alive[id] = true
				}
			}
			for _, l := range f.NewLines() {
				for _, id := range nameRe.FindAllString(l.Code, -1) {
					alive[id] = true
				}
			}
		}
		for name := range declared {
			if !alive[name] {
				g.names[name] = true
			}
		}
		return g
	}).(*goneSource)
}

// literalWords returns a string literal and, when it holds several
// space-separated words (a class list), each of them.
func literalWords(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 200 {
		return nil
	}
	out := []string{s}
	if fs := strings.Fields(s); len(fs) > 1 {
		out = append(out, fs...)
	}
	return out
}

// removedBySource returns why old assertion o could not stay as it was
// because the change removed what it relied on from production code: a
// name it uses, or a value it expected. It returns "" otherwise.
func removedBySource(a *core.Analysis, o located) string {
	g := goneFromSource(a)
	if len(g.names) > 0 {
		for _, id := range nameRe.FindAllString(o.line.Code, -1) {
			if g.names[id] {
				return "the old assertion used " + id + ", which the same change removed from the source code"
			}
		}
	}
	if len(g.values) > 0 {
		lits, _ := operands(o.assertion)
		for _, e := range lits {
			if s, ok := literalString(e.raw); ok {
				if v := strings.TrimSpace(decodeLiteral(s)); distinctiveValue(v) && g.values[v] {
					return "the value it expected (" + display(e.raw) + ") was removed from the source code by the same change"
				}
			}
		}
	}
	return ""
}

// compoundName reports a name specific enough to stand for one
// declaration: at least five characters, written in camelCase, snake_case
// or UPPER_CASE, so that words like `value` or `result` never count.
func compoundName(s string) bool {
	if len(s) < 5 {
		return false
	}
	for i := 1; i < len(s); i++ {
		c, p := s[i], s[i-1]
		if c >= 'A' && c <= 'Z' && p >= 'a' && p <= 'z' || p == '_' && i > 1 && c != '_' {
			return true
		}
	}
	return false
}

// distinctiveValue reports a value specific enough that its removal from
// the source code is about this check: six characters or more, with a
// digit or a separator in it (text-red-500, user_id, "Name required"),
// so that a common word such as "local" never counts.
func distinctiveValue(v string) bool {
	return len(v) >= 6 && strings.ContainsAny(v, "-_.:/ 0123456789")
}
