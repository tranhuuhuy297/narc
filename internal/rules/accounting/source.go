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
		gone: map[string]bool{}, importsDropped: map[string]bool{}, valuesOut: map[string]bool{}}
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
			for _, ln := range f.Removed {
				if l.rustInline(f) && l.inOldTest(f, ln.No) {
					continue
				}
				declarations(f.Lang, ln.Code, declared)
				if flagCheckRe.MatchString(ln.NoComment) {
					flagsOut++
				}
				importStems(ln, importedOut)
				for _, v := range ln.Strings {
					m.valuesOut[strings.TrimSpace(v)] = true
				}
			}
			for _, ln := range f.Added {
				for _, v := range ln.Strings {
					valuesIn[strings.TrimSpace(v)] = true
				}
				if flagCheckRe.MatchString(ln.NoComment) {
					flagsIn++
				}
				importStems(ln, importedIn)
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
	m.flagsRemoved = flagsOut > flagsIn
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
	return m
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

// mention adds the identifiers of a line of code, and the pieces of its
// dotted or slashed string literals ('pkg.module.name', 'a/b/c'), to into.
func mention(code string, strs []string, into map[string]bool) {
	for _, id := range identRe.FindAllString(code, -1) {
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
			for _, id := range identRe.FindAllString(m[1], -1) {
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

// flagCheckRe matches a feature-flag check in code.
var flagCheckRe = regexp.MustCompile(`(?i)feature[_-]?flag|\buse(?:Feature|Flag)\w*\s*\(|\bfeatures\s*\.\s*(?:includes|has|get)\s*\(|\bis_?feature_?(?:enabled|active|on)\b|\bhas_?feature\s*\(|\bflag_?enabled\b|\bgetFeatureValue\b|\bisFlagEnabled\b|\bfeatures\.has\b|_FLAG\b`)

// flagTestRe matches a test that is about a feature flag being off or on:
// its name says so, or its body toggles one.
var flagTestRe = regexp.MustCompile(`(?i)feature[ _-]?flag|\bflags?\b|\bfeatureflag|\bfeatures\s*:\s*\[|flag_?enabled|feature_?enabled|_FLAG\b|\bset_?flag\b|\bmockFlag|\bflagged\b`)

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
		for _, id := range identRe.FindAllString(ll.Code, -1) {
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
				for _, id := range identRe.FindAllString(lines[k].Code, -1) {
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
		for _, id := range identRe.FindAllString(o.seg.lines[rel-1].Code, -1) {
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
		for _, id := range identRe.FindAllString(o.seg.lines[rel-1].Code, -1) {
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
