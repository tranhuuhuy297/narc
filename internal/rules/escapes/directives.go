package escapes

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// This file is the shared machinery of the directive rules
// (type-check-suppressed, lint-suppressed, coverage-ignored): suppression
// comments, attributes and pragmas described by tables of dirSpecs.

// scope is how much code a directive covers.
type scope uint8

const (
	scopeLine scope = iota // this line or the next one
	scopeItem              // the annotated declaration (attributes, annotations)
	scopeFile              // the rest of the file, or until re-enabled
)

// dirSpec describes one kind of directive. re may use the named groups
// "dir" (the directive keyword), "rules" (the rules or codes it names),
// "note" (the reason given with it) and "wide" (non-empty when the
// directive applies to a whole module or file).
type dirSpec struct {
	tool  string      // the tool it silences, for messages ("ESLint")
	langs []lang.Lang // languages where the directive means something
	code  bool        // match the code view (attributes, annotations, pragmas) instead of comment text
	re    *regexp.Regexp
	// wide matches directive keywords that apply beyond one line
	// (eslint-disable, @ts-nocheck, LCOV_EXCL_START).
	wide *regexp.Regexp
	// ownLineWide: on a line of its own, the directive applies until it is
	// re-enabled (pylint, RuboCop).
	ownLineWide bool
	// sameLine: the directive always covers its own line (# noqa,
	// # type: ignore), even when it stands alone.
	sameLine bool
	item     bool // an attribute or annotation covering a declaration
	// rulesFromStrings takes the rule names from the string literals inside
	// the "rules" group (@SuppressWarnings("unchecked")); lastString keeps
	// only the last one, up to a ':' ([SuppressMessage("Design", "CA1031:...")]).
	rulesFromStrings, lastString bool
	// rulesRe, when set, extracts the rule names from the "rules" group
	// (its first submatch), for directives that configure rules inline
	// (`eslint no-console: "off"`). A directive without any is no
	// suppression.
	rulesRe *regexp.Regexp
	// noun names what the tool reports, for messages ("rule", "warning").
	noun string
	// toggle: the same marker opens and closes a region (SimpleCov :nocov:).
	toggle bool
	// checked: a suppression that fails once the error is gone
	// (@ts-expect-error), which is much safer than a blind one.
	checked bool
	// wideWhere describes a wide directive's reach ("in the whole file").
	wideWhere string
}

func (s *dirSpec) applies(l lang.Lang) bool { return langIn(l, s.langs...) }

// directive is one suppression found on a line.
type directive struct {
	spec    *dirSpec
	text    string   // the directive as written, without its reason
	name    string   // the directive keyword, e.g. "eslint-disable-next-line"
	rules   []string // the rules or codes it names; empty means all
	note    string   // the reason written with it, if any
	scope   scope
	ownLine bool // the directive is alone on its line
	next    bool // it covers the next line rather than its own
}

// thisLineRe matches directive keywords that cover their own line
// (eslint-disable-line, swiftlint:disable:this, coverage:ignore-line).
var thisLineRe = regexp.MustCompile(`(?i)(line|:this)$`)

// findDirectives returns the directives of specs on a lexed line.
func findDirectives(specs []*dirSpec, lg lang.Lang, l lang.LexedLine) []*directive {
	var out []*directive
	// A JSX comment `{/* @ts-ignore */}` stands alone on its line too.
	code := strings.Join(strings.Fields(l.Code), "")
	ownLine := code == "" || (lg.IsJSLike() && code == "{}")
	var v *view
	for _, sp := range specs {
		if !sp.applies(lg) {
			continue
		}
		if sp.code {
			if strings.TrimSpace(l.Code) == "" {
				continue
			}
			if v == nil {
				v = newView(lg, l)
			}
			for _, m := range v.matches(sp.re) {
				out = append(out, sp.build(v.canon, m, v, false))
			}
			continue
		}
		if l.Comment == "" {
			continue
		}
		for _, m := range sp.re.FindAllStringSubmatchIndex(l.Comment, -1) {
			if inCodeSpan(l.Comment, m[0]) {
				continue // prose quoting a directive: "the `# noqa` must stay effective"
			}
			if d := sp.build(l.Comment, m, nil, ownLine); sp.rulesRe == nil || len(d.rules) > 0 {
				out = append(out, d)
			}
		}
	}
	return out
}

func (s *dirSpec) build(text string, m []int, v *view, ownLine bool) *directive {
	group := func(name string) (string, int, int) {
		i := s.re.SubexpIndex(name)
		if i < 0 || m[2*i] < 0 {
			return "", -1, -1
		}
		return text[m[2*i]:m[2*i+1]], m[2*i], m[2*i+1]
	}
	d := &directive{spec: s, ownLine: ownLine}
	d.name, _, _ = group("dir")
	if d.name == "" {
		d.name = text[m[0]:m[1]]
	}
	d.name = strings.Join(strings.Fields(d.name), " ")
	rules, rulesStart, rulesEnd := group("rules")
	end := m[1]
	if note, ns, _ := group("note"); ns >= 0 {
		d.note = strings.TrimSpace(note)
		end = ns
	}
	// An inline reason (#[allow(x, reason = "...")]) is a note, not a rule.
	if r := reasonRe.FindStringSubmatchIndex(rules); r != nil {
		d.note = strings.TrimSpace(d.note + " " + rules[r[2]:r[3]])
		rules = rules[:r[0]] + rules[r[1]:]
		rulesEnd = rulesStart + r[0]
	}
	switch {
	case s.rulesRe != nil:
		for _, rm := range s.rulesRe.FindAllStringSubmatch(rules, -1) {
			d.rules = append(d.rules, rm[1])
		}
	case s.rulesFromStrings && v != nil && rulesStart >= 0:
		strs := v.stringsIn(rulesStart, rulesEnd)
		if s.lastString && len(strs) > 0 {
			last := strs[len(strs)-1]
			if i := strings.Index(last, ":"); i > 0 {
				last = last[:i]
			}
			strs = []string{last}
		}
		for _, r := range strs {
			d.rules = append(d.rules, splitRules(r)...)
		}
	default:
		d.rules = splitRules(rules)
	}
	d.text = strings.TrimSpace(text[m[0]:end])
	if end != m[1] { // drop the separator before the reason
		d.text = strings.TrimRight(d.text, " -:—–(/")
	}
	if v == nil { // comment text: drop doc-comment stars and a leading '#'
		d.text = strings.TrimLeft(d.text, " \t*#")
	}
	nextOnly := strings.Contains(strings.ToLower(d.name), "next")
	switch wide, _, _ := group("wide"); {
	case wide != "", s.wide != nil && s.wide.MatchString(d.name), s.ownLineWide && ownLine && !nextOnly:
		d.scope = scopeFile
	case s.item:
		d.scope = scopeItem
	}
	d.next = d.scope == scopeLine && ownLine && (!s.sameLine || nextOnly) &&
		(nextOnly || !thisLineRe.MatchString(d.name))
	return d
}

// inCodeSpan reports whether offset i of comment text lies inside a
// Markdown code span (between backticks), or right after an opening one:
// documentation that mentions a directive rather than one.
func inCodeSpan(text string, i int) bool {
	j := i
	for j < len(text) && (text[j] == ' ' || text[j] == '#') {
		j++
	}
	if i > 0 && text[i-1] == '`' || j > 0 && j <= len(text) && text[j-1] == '`' {
		return true
	}
	return strings.Count(text[:i], "`")%2 == 1
}

var (
	ruleSepRe = regexp.MustCompile(`[,\s]+`)
	reasonRe  = regexp.MustCompile(`(?i),?\s*\b(?:reason|justification)\s*=\s*["']([^"']*)["']`)
)

// splitRules splits a list of rule names written in a directive.
func splitRules(s string) []string {
	var out []string
	for _, r := range ruleSepRe.Split(strings.TrimSpace(s), -1) {
		r = strings.Trim(r, `"'()[]{};`)
		if r != "" && r != "=" {
			out = append(out, r)
		}
	}
	return out
}

// where describes what a directive covers.
func (d *directive) where() string {
	switch d.scope {
	case scopeItem:
		return "on the annotated declaration"
	case scopeFile:
		if d.spec.wideWhere != "" {
			return d.spec.wideWhere
		}
		return "for the rest of the file"
	}
	if d.next {
		return "on the next line"
	}
	return "on this line"
}

// scopeKey is the part of a directive's identity that a move or a
// placement change (trailing comment vs. line above) does not alter.
func (d *directive) scopeKey() string {
	switch d.scope {
	case scopeItem:
		return "item"
	case scopeFile:
		return "file"
	}
	return "line"
}

// dirHit is one suppressed rule of a directive (rule "" for all rules), the
// unit in which suppressions are added or removed.
type dirHit struct {
	d    *directive
	rule string
}

func hitsOf(ds []*directive) []dirHit {
	var out []dirHit
	for _, d := range ds {
		if len(d.rules) == 0 {
			out = append(out, dirHit{d, ""})
			continue
		}
		for _, r := range d.rules {
			out = append(out, dirHit{d, r})
		}
	}
	return out
}

// dirKeys identifies a directive hit by tool, reach and rule, followed by
// the identities of broader suppressions it may replace: narrowing
// `/* eslint-disable */` to `// eslint-disable-next-line no-console` is not
// a new escape.
func dirKeys(h dirHit) []string {
	tool, sc, r := strings.ToLower(h.d.spec.tool), h.d.scopeKey(), strings.ToLower(h.rule)
	if tool == "oxlint" {
		// oxlint honors eslint-disable comments: switching the spelling is
		// a migration, not a new suppression.
		tool = "eslint"
		r = normRule(r)
	} else if tool == "eslint" {
		r = normRule(r)
	}
	if r == "" {
		r = "*"
	}
	keys := []string{tool + ":" + sc + ":" + r}
	if r != "*" {
		keys = append(keys, tool+":"+sc+":*")
	}
	if sc != "file" {
		if r != "*" {
			keys = append(keys, tool+":file:"+r)
		}
		keys = append(keys, tool+":file:*")
	}
	return keys
}

// dirFinding is a directive with the rules it newly suppresses.
type dirFinding struct {
	file  *core.FileInfo
	line  core.Line
	d     *directive
	rules []string // newly suppressed rules; nil when the directive covers all
}

// newDirectives returns the directives of specs that a change introduced,
// grouped per directive with the rules that are new.
func newDirectives(files []*core.FileInfo, specs []*dirSpec, keys func(dirHit) []string) []dirFinding {
	occs := netNew(files, func(f *core.FileInfo, l core.Line, _ bool) []dirHit {
		return hitsOf(findDirectives(specs, f.Lang, l.LexedLine))
	}, keys)
	var out []dirFinding
	for _, o := range occs {
		if n := len(out); n > 0 && out[n-1].d == o.v.d {
			if o.v.rule != "" {
				out[n-1].rules = append(out[n-1].rules, o.v.rule)
			}
			continue
		}
		df := dirFinding{file: o.file, line: o.line, d: o.v.d}
		if o.v.rule != "" {
			df.rules = []string{o.v.rule}
		}
		out = append(out, df)
	}
	return out
}

// allDirectiveSpecs is every directive table, for recognizing directives
// inside comments that are otherwise read as explanations.
func allDirectiveSpecs() [][]*dirSpec { return [][]*dirSpec{typeSpecs, lintSpecs, coverageSpecs} }

// noteOf returns the explanatory part of a comment: the reason attached to
// a directive, or the comment itself when it is not a directive.
func noteOf(lg lang.Lang, comment string) string {
	l := lang.LexedLine{Comment: comment}
	for _, specs := range allDirectiveSpecs() {
		if ds := findDirectives(specs, lg, l); len(ds) > 0 {
			var notes []string
			for _, d := range ds {
				notes = append(notes, d.note)
			}
			return strings.TrimSpace(strings.Join(notes, " "))
		}
	}
	return comment
}

// directiveNote is the reason a reviewer gets for a directive: the note
// written with it plus comment lines right above it (doc comments, which
// describe the code rather than the suppression, are not counted).
func directiveNote(f *core.FileInfo, s *side, df dirFinding) string {
	parts := []string{df.d.note}
	if !df.d.ownLine && df.d.spec.code {
		parts = append(parts, noteOf(f.Lang, df.line.Comment))
	}
	parts = append(parts, commentsAbove(f, s, df.line.No))
	if f.Lang == lang.Python && strings.HasSuffix(strings.TrimSpace(df.line.Code), ":") {
		// `except LookupError:  # noqa: S010` followed by comment lines
		// that open the block and say why.
		parts = append(parts, commentsBelow(s, df.line.No))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// commentsBelow returns the text of up to three comment-only lines directly
// below line n.
func commentsBelow(s *side, n int) string {
	var parts []string
	for m := n + 1; m <= n+3; m++ {
		l, ok := s.at(m)
		if !ok || strings.TrimSpace(l.Code) != "" || l.Comment == "" {
			break
		}
		parts = append(parts, l.Comment)
	}
	return strings.Join(parts, " ")
}

// quoted renders a directive for a message, with its comment marker for
// hash-comment languages so "# noqa" reads naturally.
func (d *directive) quoted(lg lang.Lang) string {
	t := d.text
	if !d.spec.code {
		switch lg {
		case lang.Python, lang.Ruby, lang.Shell, lang.Elixir:
			t = "# " + t
		case lang.Go:
			if strings.HasPrefix(t, "nolint") || strings.HasPrefix(t, "lint:") || strings.HasPrefix(t, "go:") {
				t = "//" + t // machine-readable Go directives are written without a space
			}
		}
	}
	return "`" + shorten(t, 70) + "`"
}
