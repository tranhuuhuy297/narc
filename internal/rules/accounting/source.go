package accounting

import (
	"path"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// What the change did to production code decides how a test that went
// away reads. A test of code the change removed went with that code. A
// test of code the change edited, deleted without a replacement, is the
// "break the code, delete the test" move. Both are computed on first use:
// most changes delete no test.

// sourceModel describes the change's edits to production code.
type sourceModel struct {
	// vanished holds the distinctive names that production code declared
	// before the change (functions, types, constants, fields, parameters,
	// enum members) and that nothing mentions after it: not the new
	// version of any changed file, not an added line, not the surviving
	// tests.
	vanished map[string]bool
	// touched holds the functions and types defined in modified production
	// files whose definitions contain a changed line, and that still exist
	// after the change; modules holds every name defined in a modified
	// production file (either side).
	touched, modules map[string]bool
	// flagsRemoved: the change took feature-flag checks out of production
	// code (a flag going GA, or being dropped).
	flagsRemoved bool
	// gone holds the deleted non-test files, by path without extension
	// (and without a trailing /index).
	gone map[string]bool
	// importsDropped holds the module stems whose imports the change
	// removed from production code without adding any back.
	importsDropped map[string]bool
	// valuesOut holds the string literals of removed production lines
	// that no added production line has: values the code no longer
	// produces where it did.
	valuesOut map[string]bool
	// nullsOut holds the null-like keywords (undefined, null, None, nil)
	// that occur on more removed than added production lines: an input
	// variant the code no longer handles.
	nullsOut map[string]bool
	// typeNullsOut holds the null-like keywords that the change took out
	// of the types in production code (`chatID: string | undefined` →
	// `chatID: string`, Optional[str] → str): an input that no longer
	// type-checks.
	typeNullsOut map[string]bool
	// messagesOut holds the message-like pieces of string literals that
	// the change removed from production code, other than on lines that
	// raise or build an error, and that no added production line has: a
	// log line or a text the code no longer emits.
	messagesOut []string
	// removedNames is vanished without the requirement of a compound name:
	// the functions, types, fields, parameters, enum members and
	// module-level constants (four characters or more) that production
	// code declared before the change and that nothing mentions after it.
	// Code that uses them no longer compiles or runs.
	removedNames map[string]bool
	// valuesDropped holds the distinctive string literals (see
	// distinctValue) of removed production lines, other than lines that
	// raise or build an error, that the change dropped without a
	// replacement: no added production literal shares their first half.
	valuesDropped map[string]bool
	// errorReturnDropped holds the Go functions whose declaration the
	// change edited from returning an error to not returning one, so that
	// a caller's `err :=` and its `if err != nil` check no longer compile.
	errorReturnDropped map[string]bool
}

// source returns the source model of the change.
func (l *ledger) source() *sourceModel {
	if l.src == nil {
		l.src = l.buildSource()
	}
	return l.src
}

func (l *ledger) buildSource() *sourceModel {
	m := &sourceModel{vanished: map[string]bool{}, touched: map[string]bool{}, modules: map[string]bool{},
		gone: map[string]bool{}, importsDropped: map[string]bool{}, valuesOut: map[string]bool{}, nullsOut: map[string]bool{},
		typeNullsOut: map[string]bool{}, removedNames: map[string]bool{}, errorReturnDropped: map[string]bool{},
		valuesDropped: map[string]bool{}}
	typeNulls := map[string]int{}
	api := map[string]bool{}
	errReturns := map[string]int{} // Go func → +1 per removed declaration returning an error, -1 per added one
	var removedStrs []string
	var addedStrs strings.Builder
	nulls := map[string]int{}
	valuesIn := map[string]bool{}
	declared := map[string]bool{}
	alive := map[string]bool{}
	flagsOut, flagsIn := 0, 0
	importedOut, importedIn := map[string]bool{}, map[string]bool{}
	for _, f := range l.a.Files {
		if f.Diff.Kind == diff.Deleted && !l.testOn(f, before) && !core.IsTestPath(f.Path) {
			m.gone[modulePath(f.Path)] = true
		}
		for _, ln := range f.Added {
			mention(ln.Code, ln.Strings, alive)
		}
		prod := l.productionCode(f)
		if prod {
			// exitDepth is the bracket depth still open in an error exit that
			// spans removed lines: bail!(\n "message",\n ...).
			exitDepth, lastNo := 0, 0
			for _, ln := range f.Removed {
				if ln.No != lastNo+1 {
					exitDepth = 0
				}
				lastNo = ln.No
				if l.rustInline(f) && l.inOldTest(f, ln.No) {
					continue
				}
				declarations(f.Lang, ln.Code, declared)
				apiDeclarations(f.Lang, ln, api)
				if flagCheckRe.MatchString(ln.NoComment) {
					flagsOut++
				}
				importStems(ln, importedOut)
				exit := exitDepth > 0 || errorExitRe.MatchString(ln.Code)
				if exit {
					exitDepth = max(exitDepth+depthChange(ln.Code), 0)
				}
				for _, v := range ln.Strings {
					m.valuesOut[strings.TrimSpace(v)] = true
					if !exit {
						removedStrs = append(removedStrs, v)
					}
				}
				for _, k := range typeNullKeys(ln.Code) {
					typeNulls[k]++
				}
				if name, errs, ok := goFuncResult(f.Lang, ln.Code); ok && errs {
					errReturns[name]++
				}
				for _, k := range nullWordRe.FindAllString(ln.Code, -1) {
					nulls[k]++
				}
			}
			for _, ln := range f.Added {
				for _, v := range ln.Strings {
					valuesIn[strings.TrimSpace(v)] = true
					addedStrs.WriteString(v)
					addedStrs.WriteByte(0)
				}
				if flagCheckRe.MatchString(ln.NoComment) {
					flagsIn++
				}
				importStems(ln, importedIn)
				for _, k := range nullWordRe.FindAllString(ln.Code, -1) {
					nulls[k]--
				}
				for _, k := range typeNullKeys(ln.Code) {
					typeNulls[k]--
				}
				if name, errs, ok := goFuncResult(f.Lang, ln.Code); ok {
					if errs {
						errReturns[name]--
					} else if errReturns[name] > 0 {
						m.errorReturnDropped[name] = true
					}
				}
			}
		}
		if f.Diff.Kind == diff.Deleted || !f.Lang.IsProgramming() {
			continue
		}
		if prod || l.testSide(f, after) {
			// The surviving code: what production code and tests still
			// mention after the change.
			for _, seg := range l.segments(f, after) {
				for _, ll := range seg.lines {
					mention(ll.Code, ll.Strings, alive)
				}
			}
		}
		if prod && f.Diff.Kind == diff.Modified {
			l.touchedIn(f, m)
		}
	}
	defined := l.definedNames()
	for name := range declared {
		if !alive[name] && !defined[name] && distinctName(name) {
			m.vanished[name] = true
		}
	}
	for name := range api {
		if len(name) >= 4 && !alive[name] && !defined[name] && !stopWords[name] && !notDefs[name] {
			m.removedNames[name] = true
		}
	}
	for name := range m.errorReturnDropped {
		if errReturns[name] <= 0 {
			delete(m.errorReturnDropped, name) // an error return was added back
		}
	}
	m.flagsRemoved = flagsOut > flagsIn
	for k, n := range nulls {
		if n > 0 {
			m.nullsOut[k] = true
		}
	}
	for k, n := range typeNulls {
		if n > 0 {
			m.typeNullsOut[k] = true
		}
	}
	for v := range m.valuesOut {
		if valuesIn[v] || v == "" {
			delete(m.valuesOut, v)
		}
	}
	for s := range importedOut {
		if !importedIn[s] && !alive[s] {
			m.importsDropped[s] = true
		}
	}
	m.messagesOut = messagesOut(removedStrs, addedStrs.String())
	added := addedStrs.String()
	for _, v := range removedStrs {
		v = strings.TrimSpace(v)
		if distinctValue(v) && m.valuesOut[v] && !strings.Contains(added, v[:(len(v)+1)/2]) {
			m.valuesDropped[v] = true
		}
	}
	return m
}

// distinctValue reports a string literal specific enough to stand for one
// value of the code: six characters or more, without spaces, with a digit
// or a separator in it (a version pin, a class name, a key such as
// "@agentclientprotocol/codex-acp@1.10.0" or "text-red-500").
func distinctValue(v string) bool {
	return len(v) >= 6 && len(v) <= 120 && !strings.ContainsAny(v, " \t\n") && strings.ContainsAny(v, "-_.:/@0123456789")
}

// placeholderRe matches the placeholders of format strings and templates:
// %s, %v, %d, {}, {name}, ${expr}, {0}.
var placeholderRe = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z]|\$?\{[^{}]*\}`)

// messagesOut returns the message-like pieces of removed string literals
// that no added string literal contains: the stretches between format
// placeholders that read like prose (12 characters or more, three words
// or more), such as "is not yet implemented" in "%s is not yet
// implemented". added holds the added literals, separated by NUL bytes.
func messagesOut(removed []string, added string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range removed {
		if len(s) < 12 || len(s) > 400 || !strings.Contains(s, " ") {
			continue
		}
		for _, piece := range placeholderRe.Split(s, -1) {
			piece = strings.Trim(piece, " \t:;,.`'\"()[]")
			if len(piece) < 12 || strings.Count(piece, " ") < 2 || seen[piece] || strings.Contains(added, piece) {
				continue
			}
			seen[piece] = true
			out = append(out, piece)
		}
	}
	return out
}

// touchedIn records the definitions of modified production file f that
// contain a changed line (touched, when they still exist after the change)
// and every name f defines (modules).
func (l *ledger) touchedIn(f *core.FileInfo, m *sourceModel) {
	changed := func(lines []core.Line) map[int]bool {
		out := map[int]bool{}
		for _, ln := range lines {
			if strings.TrimSpace(ln.Code) != "" {
				out[ln.No] = true
			}
		}
		return out
	}
	added, removed := changed(f.Added), changed(f.Removed)
	defined := l.definedNames()
	for _, sd := range []side{before, after} {
		marks := added
		if sd == before {
			marks = removed
		}
		for _, seg := range l.segments(f, sd) {
			lines := seg.lines
			for i, ll := range lines {
				if !mayDefine(f.Lang, ll.Code) {
					continue
				}
				defs := map[string]bool{}
				definitions(f.Lang, ll.Code, defs)
				if len(defs) == 0 {
					continue
				}
				end := blockEnd(f.Lang, lines, i)
				hit := false
				for k := i; k <= end && !hit; k++ {
					hit = marks[seg.lineNo(k)]
				}
				for name := range defs {
					m.modules[name] = true
					if hit && (sd == after || defined[name]) {
						m.touched[name] = true
					}
				}
			}
		}
	}
}

// fieldDeclRe matches a line that declares a field or a parameter of its
// own: `chatID: string | undefined,`, `pub name: String,`, `timeout: int = 5`.
var fieldDeclRe = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|readonly|static|export|declare|override|pub(?:\([^)]*\))?)\s+)*([A-Za-z_$][\w$]*)\s*[?!]?\s*:\s*[^\s:=]`)

// apiDeclarations adds the names that a line of production code declares
// as part of its interface, rather than as a local variable: functions and
// types, fields and typed parameters, enum members, Python parameters, and
// constants or variables declared at the top level of a file.
func apiDeclarations(lg lang.Lang, ln core.Line, into map[string]bool) {
	code := ln.Code
	definitions(lg, code, into)
	if m := enumMemberRe.FindStringSubmatch(code); m != nil {
		into[m[1]] = true
	}
	if m := fieldDeclRe.FindStringSubmatch(code); m != nil && m[1] != "case" && m[1] != "default" {
		into[m[1]] = true
	}
	if indent(ln.Raw) == 0 {
		for _, re := range []*regexp.Regexp{declRes[0], declRes[2]} {
			for _, m := range re.FindAllStringSubmatch(code, -1) {
				into[m[1]] = true
			}
		}
	}
	switch lg {
	case lang.Python:
		if m := pyParamsRe.FindStringSubmatch(code); m != nil {
			for _, id := range idents(m[1]) {
				into[id] = true
			}
		}
	case lang.Go:
		if m := goFieldRe.FindStringSubmatch(code); m != nil {
			into[m[1]] = true
		}
	}
}

// goFuncDeclRe matches a Go function or method declaration; group 1 is
// its name, and the match ends at the "(" that opens its parameters.
var goFuncDeclRe = regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*(?:\[[^\]]*\])?\(`)

// goFuncResult reads a Go function declaration on one line: its name, and
// whether its results include an error.
func goFuncResult(lg lang.Lang, code string) (name string, errs, ok bool) {
	if lg != lang.Go {
		return "", false, false
	}
	m := goFuncDeclRe.FindStringSubmatchIndex(code)
	if m == nil {
		return "", false, false
	}
	depth := 0
	for i := m[1] - 1; i < len(code); i++ {
		switch code[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return code[m[2]:m[3]], goErrorResultRe.MatchString(code[i+1:]), true
			}
		}
	}
	return "", false, false // parameters continue on the next line
}

// goErrorResultRe matches a result list that includes an error.
var goErrorResultRe = regexp.MustCompile(`\berror\b`)

// errorExitRe matches a line that raises or builds an error: its message
// is the error path itself, which tests may not go away with (see
// messagesOut).
var errorExitRe = regexp.MustCompile(`\b(?:raise|throw|Errorf|Err)\b|\b(?:bail|panic)!?\s*\(|\berrors\.New\b|\b[A-Z]\w*(?:Error|Exception)\s*\(`)

// typeNullRe finds a null-like member of a type: `string | undefined`,
// `str | None`, `Optional[str]`, an optional field or parameter `id?: T`.
var typeNullRe = regexp.MustCompile(`\|\s*(undefined|null|None)\b|\b(undefined|null|None)\s*\||\b(Optional)\[|[\w$]\?\s*:\s*[\w$<\[{(]`)

// typeNullKeys returns the null-like keywords that the types on a line of
// code admit, one per occurrence (Optional[...] admits None, id?: T
// admits undefined).
func typeNullKeys(code string) []string {
	var out []string
	for _, m := range typeNullRe.FindAllStringSubmatch(code, -1) {
		switch {
		case m[1] != "":
			out = append(out, m[1])
		case m[2] != "":
			out = append(out, m[2])
		case m[3] != "":
			out = append(out, "None")
		default:
			out = append(out, "undefined")
		}
	}
	return out
}

// mention adds the identifiers of a line of code, and the pieces of its
// dotted or slashed string literals ('pkg.module.name', 'a/b/c'), to into.
func mention(code string, strs []string, into map[string]bool) {
	for _, id := range idents(code) {
		into[id] = true
	}
	for _, s := range strs {
		for _, piece := range strPieces(s) {
			into[piece] = true
		}
	}
}

// strPieceRe splits string literals into name-like pieces.
var strPieceRe = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

// strPieces returns the name-like pieces of a short string literal: the
// target of mock.patch("pkg.module.get_token"), a flag key, a path.
func strPieces(s string) []string {
	if len(s) > 120 || strings.ContainsAny(s, " \t\n") {
		return nil
	}
	return strPieceRe.FindAllString(s, -1)
}

var (
	// declRes find names that a line of code declares besides functions
	// and types: constants and variables, fields, typed parameters and
	// object keys, assignments, Python parameters.
	declRes = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:const|let|var|val|static|final|readonly)\s+([A-Za-z_$][\w$]*)`),
		regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*[?!]?\s*:(?:[^:=]|$)`),
		regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*:?=(?:[^=>]|$)`),
	}
	// enumMemberRe matches a line that declares an enum member or variant:
	// `RecursionLimitExceeded,`, `Pending = "pending",`, `Done(Code)`.
	enumMemberRe = regexp.MustCompile(`^\s*([A-Z][A-Za-z0-9_]*)\s*(?:[,({=]|$)`)
	// pyParamsRe captures the parameter list of a Python def.
	pyParamsRe = regexp.MustCompile(`\bdef\s+\w+\s*\((.*)`)
	// goFieldRe matches a Go struct field or a parameter on its own line.
	goFieldRe = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s+[\[\]*\w.]+`)
)

// declarations adds the names a line of production code declares.
func declarations(lg lang.Lang, code string, into map[string]bool) {
	definitions(lg, code, into)
	for _, re := range declRes {
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			into[m[1]] = true
		}
	}
	if m := enumMemberRe.FindStringSubmatch(code); m != nil {
		into[m[1]] = true
	}
	switch lg {
	case lang.Python:
		if m := pyParamsRe.FindStringSubmatch(code); m != nil {
			for _, id := range idents(m[1]) {
				into[id] = true
			}
		}
	case lang.Go:
		if m := goFieldRe.FindStringSubmatch(code); m != nil {
			into[m[1]] = true
		}
	}
}

// distinctName reports a name specific enough to tie a test to the code
// that declared it: at least five characters and a compound
// (camelCase, snake_case or an UPPER_CASE constant), so that words like
// `value` or `result` never do.
func distinctName(s string) bool {
	if len(s) < 5 || stopWords[s] || notDefs[s] {
		return false
	}
	hump, under := false, false
	for i := 1; i < len(s); i++ {
		c, p := s[i], s[i-1]
		if c >= 'A' && c <= 'Z' && p >= 'a' && p <= 'z' {
			hump = true
		}
		if p == '_' && i > 1 && c != '_' {
			under = true
		}
	}
	return hump || under
}

// nullWordRe finds null-like keywords.
var nullWordRe = regexp.MustCompile(`\b(?:undefined|null|None|nil)\b`)

// flagCheckRe matches a feature-flag check in code.
var flagCheckRe = regexp.MustCompile(`(?i)feature[_-]?flag|\buse(?:Feature|Flag)\w*\s*\(|\bfeatures\s*\.\s*(?:includes|has|get)\s*\(|\bis_?feature_?(?:enabled|active|on)\b|\bhas_?feature\s*\(|\bflag_?enabled\b|\bgetFeatureValue\b|\bisFlagEnabled\b|\bfeatures\.has\b|_FLAG\b`)

// flagTestRe matches a test that is about a feature flag being off or on:
// its name says so, or its body toggles one.
var flagTestRe = regexp.MustCompile(`(?i)feature[ _-]?flag|\bflags?\b|\bfeatureflag|\bfeatures\s*:\s*\[|flag_?(?:enabled|disabled|on|off)|feature_?(?:enabled|disabled|on|off)|_FLAG\b|\bset_?flag\b|\bmockFlag|\bflagged\b|\bwith(?:out)?(?: the)? [\w-]+ feature\b|\bfeature (?:is )?(?:on|off|enabled|disabled)\b`)

// importLineStemRe captures the module named by an import line: the last
// path or dotted segment of a JS/TS specifier, a Python module, a Go or
// Rust path.
var importLineStemRe = regexp.MustCompile(`(?:\bfrom\s+['"]|\brequire\s*\(\s*['"]|\bimport\s+['"]|^\s*from\s+|^\s*import\s+)([\w@./~-]+)`)

// importStems adds the module stems named by an import line.
func importStems(ln core.Line, into map[string]bool) {
	if !importLineRe.MatchString(ln.Code) {
		return
	}
	m := importLineStemRe.FindStringSubmatch(ln.NoComment)
	if m == nil {
		return
	}
	spec := strings.Trim(m[1], "./")
	if i := strings.LastIndexAny(spec, "./"); i >= 0 {
		spec = spec[i+1:]
	}
	if spec != "" {
		into[strings.ToLower(spec)] = true
	}
}

// modulePath returns a path without its extension or a trailing /index,
// the form in which import specifiers name modules.
func modulePath(p string) string {
	if ext := path.Ext(p); ext != "" {
		p = strings.TrimSuffix(p, ext)
	}
	return strings.TrimSuffix(p, "/index")
}

// refsVanished returns a name that the change removed from production
// code and that old test o is about, if any: its body mentions it, a
// helper of its file that it calls does, or its name (or its suite's)
// does.
func (l *ledger) refsVanished(o *instance) string {
	if len(l.sourceChanged) == 0 && !l.anySourceDeleted() {
		return ""
	}
	v := l.source().vanished
	if len(v) == 0 {
		return ""
	}
	for _, w := range strPieceRe.FindAllString(o.block.Name, -1) {
		if v[w] {
			return w
		}
	}
	local := l.localHelpers(o.file)
	seen := map[string]bool{}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		ll := o.seg.lines[rel-1]
		for _, id := range idents(ll.Code) {
			if id == o.block.Bare || seen[id] {
				continue
			}
			seen[id] = true
			if v[id] {
				return id
			}
			for h := range local[id] {
				if v[h] {
					return h
				}
			}
		}
		for _, s := range ll.Strings {
			for _, piece := range strPieces(s) {
				if v[piece] {
					return piece
				}
			}
		}
	}
	return ""
}

// localHelpers returns, for each function defined outside tests on the old
// side of test file f, the identifiers its body mentions (cached).
func (l *ledger) localHelpers(f *core.FileInfo) map[string]map[string]bool {
	if h, ok := l.locals[f]; ok {
		return h
	}
	h := map[string]map[string]bool{}
	l.locals[f] = h
	for _, seg := range l.segments(f, before) {
		lines := seg.lines
		for i, ll := range lines {
			var name string
			end := i
			if seg.lang.IsJSLike() {
				if m := jsConstDefRe.FindStringSubmatch(ll.Code); m != nil {
					name, end = m[1], jsStatementEnd(lines, i)
				}
			}
			if name == "" && mayDefine(seg.lang, ll.Code) {
				defs := map[string]bool{}
				definitions(seg.lang, ll.Code, defs)
				if len(defs) != 1 {
					continue
				}
				for d := range defs {
					name = d
				}
				end = blockEnd(seg.lang, lines, i)
			}
			if name == "" || end <= i || l.inOldTest(f, seg.lineNo(i)) {
				continue
			}
			ids := map[string]bool{}
			for k := i; k <= end; k++ {
				for _, id := range idents(lines[k].Code) {
					ids[id] = true
				}
			}
			h[name] = ids
		}
	}
	return h
}

// anySourceDeleted reports whether the change deleted a non-test file.
func (l *ledger) anySourceDeleted() bool {
	for _, f := range l.a.Files {
		if f.Diff.Kind == diff.Deleted && !l.testOn(f, before) {
			return true
		}
	}
	return false
}

// refsTouched reports whether old test o mentions a function or type that
// the change edited and kept: the code it exercised changed.
func (l *ledger) refsTouched(o *instance) bool {
	t := l.source().touched
	if len(t) == 0 {
		return false
	}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		for _, id := range idents(o.seg.lines[rel-1].Code) {
			if t[id] && id != o.block.Bare && len(id) >= 3 && !stopWords[id] {
				return true
			}
		}
	}
	return false
}

// refsModule reports whether old test o mentions a name defined in a
// production file the change modified.
func (l *ledger) refsModule(o *instance) bool {
	m := l.source().modules
	if len(m) == 0 {
		return false
	}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		for _, id := range idents(o.seg.lines[rel-1].Code) {
			if m[id] && id != o.block.Bare && len(id) >= 3 && !stopWords[id] && !notDefs[id] {
				return true
			}
		}
	}
	return false
}

// aboutFlag reports a test of a feature flag (its name or body mentions
// one) deleted by a change that removed feature-flag checks from
// production code: the flag went away, and with it the branch it guarded.
func (l *ledger) aboutFlag(o *instance) bool {
	if !l.source().flagsRemoved {
		return false
	}
	if flagTestRe.MatchString(o.block.Name) {
		return true
	}
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		if flagTestRe.MatchString(o.seg.lines[rel-1].NoComment) {
			return true
		}
	}
	return false
}

// migrationTestRe matches the test of one numbered data migration
// (tests/sentry/migrations/test_1207_swap_ids.py): such tests are retired
// once later migrations supersede theirs.
var migrationTestRe = regexp.MustCompile(`(?:^|/)migrations?/(?:[\w-]+/)*test_?\d{3,}[\w-]*\.py$|(?:^|/)migrations?/(?:[\w-]+/)*\d{3,}[\w.-]*\.(?:test|spec)\.[jt]sx?$`)

// importsGone reports whether a deleted test file imports a module that
// the change deleted: its tests went with the code they covered.
func (l *ledger) importsGone(f *core.FileInfo) bool {
	gone := l.source().gone
	if len(gone) == 0 {
		return false
	}
	dir := path.Dir(f.Path)
	for _, seg := range l.segments(f, before) {
		for _, ll := range seg.lines {
			if !importLineRe.MatchString(ll.Code) && !strings.Contains(ll.Code, "import") {
				continue
			}
			for _, spec := range importSpecs(langOf(f, before), ll) {
				if strings.HasPrefix(spec, ".") {
					if gone[modulePath(path.Join(dir, spec))] {
						return true
					}
					continue
				}
				if goneSuffix(gone, spec) {
					return true
				}
			}
		}
	}
	return false
}

var (
	// jsSpecRe captures module specifiers of JS/TS imports and requires.
	jsSpecRe = regexp.MustCompile(`(?:\bfrom\s*|\brequire\s*\(\s*|\bimport\s*\(\s*|^\s*import\s+)['"]([^'"]+)['"]`)
	// pySpecRe captures the module of a Python import.
	pySpecRe = regexp.MustCompile(`^\s*(?:from\s+([\w.]+)\s+import\b|import\s+([\w.]+))`)
)

// importSpecs returns the module specifiers an import line names, as
// slash-separated paths (relative ones keep their leading dots).
func importSpecs(lg lang.Lang, ll lang.LexedLine) []string {
	var out []string
	switch {
	case lg.IsJSLike():
		for _, m := range jsSpecRe.FindAllStringSubmatch(ll.NoComment, -1) {
			out = append(out, m[1])
		}
	case lg == lang.Python:
		if m := pySpecRe.FindStringSubmatch(ll.NoComment); m != nil {
			mod := m[1] + m[2]
			if !strings.HasPrefix(mod, ".") {
				out = append(out, strings.ReplaceAll(mod, ".", "/"))
			}
		}
	}
	return out
}

// goneSuffix reports whether a non-relative specifier (an alias such as
// @/components/x/ThreadView, a package path, a dotted Python module)
// names a deleted module: its last two segments match the end of a
// deleted file's path (narc cannot resolve the alias's root).
func goneSuffix(gone map[string]bool, spec string) bool {
	parts := strings.Split(strings.Trim(spec, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	tail := "/" + strings.Join(parts[len(parts)-2:], "/")
	for g := range gone {
		if strings.HasSuffix("/"+g, tail) {
			return true
		}
	}
	return false
}

// testedElsewhere reports whether a function of the modified production
// code that old test o calls is also called by a test the change wrote or
// edited: the behavior is still under test after a rework, if not by o.
func (l *ledger) testedElsewhere(o *instance) bool {
	if l.edited == nil {
		l.edited = map[string]bool{}
		added := map[*core.FileInfo]map[int]bool{}
		for _, n := range l.news {
			if added[n.file] == nil {
				added[n.file] = map[int]bool{}
				for _, ln := range n.file.Added {
					added[n.file][ln.No] = true
				}
			}
			changed := false
			for rel := n.block.Start; rel <= n.block.End && rel-1 < len(n.seg.lines) && !changed; rel++ {
				changed = added[n.file][n.line(rel)]
			}
			if !changed {
				continue
			}
			for rel := n.block.Header; rel <= n.block.End && rel-1 < len(n.seg.lines); rel++ {
				for _, m := range callRe.FindAllStringSubmatch(n.seg.lines[rel-1].Code, -1) {
					l.edited[lastName(m[1])] = true
				}
			}
		}
	}
	mods := l.source().modules
	for rel := o.block.Header; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		for _, m := range callRe.FindAllStringSubmatch(o.seg.lines[rel-1].Code, -1) {
			name := lastName(m[1])
			if len(name) >= 4 && !stopWords[name] && !notDefs[name] && mods[name] && l.edited[name] {
				return true
			}
		}
	}
	return false
}

// keyRe matches a string literal that reads like a key rather than a
// message: a feature flag, an option or setting name, a template path
// ("organizations:snapshot-head-check", "preprod.snapshots.enabled",
// "sentry/login.html"). A compound identifier ("includeMeasuredMetadata",
// "acp_env") reads like a key too (see retiredKey).
var keyRe = regexp.MustCompile(`^[A-Za-z@][\w@]*(?:[-:./][\w@]+)+$`)

// retiredKey returns a key-like string literal that old test o uses and
// that the change removed from production code without adding it back
// (see sourceModel.valuesOut), or "": the flag, option or setting the test
// exercised was retired.
func (l *ledger) retiredKey(o *instance) string {
	if len(l.sourceChanged) == 0 {
		return ""
	}
	out := l.source().valuesOut
	if len(out) == 0 {
		return ""
	}
	from := o.block.Start
	if from < 1 {
		from = o.block.Header
	}
	for rel := from; rel <= o.block.End && rel-1 < len(o.seg.lines); rel++ {
		for _, s := range o.seg.lines[rel-1].Strings {
			if s = strings.TrimSpace(s); len(s) >= 6 && out[s] && (keyRe.MatchString(s) || distinctName(s) && isIdentLike(s)) {
				return s
			}
		}
	}
	return ""
}

// isIdentLike reports whether s is written like one identifier.
func isIdentLike(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return s != ""
}

// depthChange returns how many more brackets a line of code opens than it
// closes.
func depthChange(code string) int {
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
