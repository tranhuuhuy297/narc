package accounting

import (
	"path"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/testblocks"
)

// Assertion helpers: a test that calls balanceIs(t, a, 150) checks what
// balanceIs asserts. Helpers defined in the change's test code are known
// up front; helpers in files the change did not touch are looked up only
// when a test seems to have lost assertions.

// assertionHelpers finds the functions on one side of a file that contain
// assertions and counts them.
func (l *ledger) assertionHelpers(f *core.FileInfo, sd side, into map[string]int) {
	for _, seg := range l.segments(f, sd) {
		helpersIn(f.Lang, seg, into)
	}
}

// helpersIn counts the assertions of every function defined in seg, by
// name, including functions without assertions (count 0), so callers can
// tell a helper that checks nothing from one that was not found. A name
// defined twice keeps its larger count.
//
// Besides plain function definitions it recognizes JS/TS arrow functions
// held in constants, whose parameters and expression bodies may span
// several lines and which may be curried (`const equals = (want) =>
// (model) => expect(f(model)).toEqual(want)`); constants built by calling
// such a factory (`const TEXT_ONLY = equals({})`), which check what the
// factory's closure checks; and Go methods, which credit their receiver
// type, so a test double whose Read method calls t.Error counts for the
// test that passes it to the code under test.
func helpersIn(l lang.Lang, seg *segment, into map[string]int) {
	lines := seg.lines
	put := func(name string, n int) {
		if old, ok := into[name]; !ok || n > old {
			into[name] = n
		}
	}
	type alias struct{ name, of string }
	var aliases []alias
	for i, ll := range lines {
		if l.IsJSLike() {
			if m := jsConstDefRe.FindStringSubmatchIndex(ll.Code); m != nil {
				name, rest := ll.Code[m[2]:m[3]], ll.Code[m[5]:]
				end := jsStatementEnd(lines, i)
				if jsDefinesFunction(lines, i, end) {
					n := testblocksCount(seg, rest)
					for k := i + 1; k <= end; k++ {
						n += seg.count(k)
					}
					put(name, n)
				} else if c := bareCallRe.FindStringSubmatch(strings.TrimSpace(rest)); c != nil {
					aliases = append(aliases, alias{name, c[1]})
				}
				continue
			}
		}
		if !mayDefine(l, ll.Code) {
			continue
		}
		defs := map[string]bool{}
		definitions(l, ll.Code, defs)
		if len(defs) != 1 {
			continue
		}
		n := 0
		if at := strings.Index(ll.Code, "=>"); at >= 0 {
			n += testblocksCount(seg, ll.Code[at+2:]) // a one-line arrow body
		}
		for k, end := i+1, blockEnd(l, lines, i); k <= end; k++ {
			n += seg.count(k)
		}
		for name := range defs {
			put(name, n)
		}
		if l == lang.Go {
			if m := goReceiverRe.FindStringSubmatch(ll.Code); m != nil {
				put(m[1], n)
			}
		}
	}
	for range 2 { // aliases of aliases
		for _, a := range aliases {
			if n := into[a.of]; n > 0 {
				put(a.name, n)
			}
		}
	}
}

var (
	// jsConstDefRe matches the start of a constant definition and captures
	// its name and what follows the `=`.
	jsConstDefRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]*)?(=)(?:[^=>]|$)`)
	// goReceiverRe captures the receiver type of a Go method.
	goReceiverRe = regexp.MustCompile(`^func\s*\(\s*\w*\s*\*?\s*([A-Za-z_]\w*)`)
)

// testblocksCount counts the assertions in a piece of a line of seg.
func testblocksCount(seg *segment, code string) int {
	return testblocks.CountAssertions(seg.lang, code)
}

// jsStatementEnd returns the last line of the JS/TS statement starting on
// line i: brackets balance and the statement does not continue on the
// next line (with =>, a method chain, an operator or a closing bracket).
func jsStatementEnd(lines []lang.LexedLine, i int) int {
	depth := 0
	for k := i; k < len(lines) && k < i+300; k++ {
		code := strings.TrimSpace(lines[k].Code)
		for j := 0; j < len(code); j++ {
			switch code[j] {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
			}
		}
		if depth > 0 || code == "" {
			continue
		}
		if strings.HasSuffix(code, ";") {
			return k
		}
		if jsOpenEndRe.MatchString(code) {
			continue
		}
		next := ""
		for n := k + 1; n < len(lines) && n < k+20; n++ {
			if next = strings.TrimSpace(lines[n].Code); next != "" {
				break
			}
		}
		if !jsContinuedRe.MatchString(next) {
			return k
		}
	}
	return i
}

var (
	// jsOpenEndRe matches a line that cannot end a statement.
	jsOpenEndRe = regexp.MustCompile(`(?:=>|[=(,?:+\-*/|&.])$`)
	// jsContinuedRe matches a line that continues the statement above.
	jsContinuedRe = regexp.MustCompile(`^(?:=>|\.|\?|:|&&|\|\||\)|\]|\}|[+\-*/])`)
)

// jsDefinesFunction reports whether the constant defined on lines
// from..to holds a function: an arrow or a function expression before its
// body starts.
func jsDefinesFunction(lines []lang.LexedLine, from, to int) bool {
	for k := from; k <= to && k < len(lines); k++ {
		code := lines[k].Code
		if k == from {
			if eq := strings.IndexByte(code, '='); eq >= 0 {
				code = code[eq+1:]
			}
			if strings.HasPrefix(strings.TrimSpace(code), "{") || strings.HasPrefix(strings.TrimSpace(code), "[") {
				return false // an object or array literal
			}
		}
		if strings.Contains(code, "=>") || strings.Contains(code, "function") {
			return true
		}
	}
	return false
}

// helperCalls counts the assertions a test makes through helpers it calls:
// helpers defined in the change, else in unchanged files near the test.
func (l *ledger) helperCalls(in *instance) int {
	n := 0
	var external map[string]int
	for rel := in.block.Header + 1; rel <= in.block.End && rel-1 < len(in.seg.lines); rel++ {
		code := in.seg.lines[rel-1].Code
		calls := callRe.FindAllStringSubmatch(code, -1)
		if in.seg.lang == lang.Go {
			// A test double built with a composite literal: &failingReader{t: t}.
			calls = append(calls, goLiteralRe.FindAllStringSubmatch(code, -1)...)
		}
		for _, m := range calls {
			name := lastName(m[1])
			if name == in.block.Bare || stopWords[name] {
				continue
			}
			if c, ok := l.helpers[name]; ok {
				n += c
				continue
			}
			if external == nil {
				external = l.externalHelpers(in.file)
			}
			n += external[name]
		}
	}
	return n
}

// goLiteralRe finds Go composite literals of named types.
var goLiteralRe = regexp.MustCompile(`([A-Za-z_]\w*)\s*\{`)

// maxHelperFiles bounds the unchanged files read per test file, and
// maxHelperSize the size of each.
const (
	maxHelperFiles = 40
	maxHelperSize  = 512 << 10
)

// externalHelpers returns the assertion helpers defined in unchanged files
// near test file f: its directory and that directory's support
// subdirectories (helpers/, support/, testutil/, ...), files it imports
// by relative path, and conftest.py / spec_helper.rb / test_helper.rb up
// the tree. It returns an empty map when contents are unavailable.
func (l *ledger) externalHelpers(f *core.FileInfo) map[string]int {
	if h, ok := l.external[f.Path]; ok {
		return h
	}
	h := map[string]int{}
	l.external[f.Path] = h
	if l.a == nil {
		return h
	}
	changed := map[string]bool{}
	for _, cf := range l.a.Files {
		changed[cf.Path] = true
	}
	read := 0
	seen := map[string]bool{}
	scan := func(p string) {
		if seen[p] || changed[p] || read >= maxHelperFiles {
			return
		}
		seen[p] = true
		lg := lang.Detect(p)
		if !lg.IsProgramming() || !sameFamily(lg, f.Lang) {
			return
		}
		b, ok := l.a.ReadNew(p)
		if !ok || len(b) > maxHelperSize {
			return
		}
		read++
		helpersIn(lg, &segment{lines: lang.Lex(lg, string(b)), whole: true, hunk: -1, lang: lg}, h)
	}
	dir := path.Dir(f.Path)
	for _, p := range importedPaths(f, dir) {
		scan(p)
	}
	for d := dir; ; d = path.Dir(d) {
		for _, name := range helperFiles {
			scan(path.Join(d, name))
		}
		if d == "." || d == "/" {
			break
		}
	}
	list := dir
	if dir == "." {
		list = ""
	}
	if paths, ok := l.a.ListNew(list); ok {
		for _, p := range paths {
			if pd := path.Dir(p); pd == dir || (path.Dir(pd) == dir && helperDir(path.Base(pd))) {
				scan(p)
			}
		}
	}
	return h
}

// helperFiles are well-known shared test-setup files, looked up in the
// test's directory and its ancestors.
var helperFiles = []string{"conftest.py", "spec_helper.rb", "rails_helper.rb", "test_helper.rb", "test_helper.exs"}

// helperDir reports a directory name that typically holds test helpers.
func helperDir(name string) bool {
	switch strings.ToLower(name) {
	case "helpers", "helper", "support", "utils", "util", "matchers", "assertions", "fixtures", "shared":
		return true
	}
	return supportDirRe.MatchString(name)
}

// sameFamily reports whether a helper written in a can be called from a
// test written in b.
func sameFamily(a, b lang.Lang) bool {
	return a == b || a.IsJSLike() && b.IsJSLike() || (a == lang.C || a == lang.Cpp) && (b == lang.C || b == lang.Cpp)
}

var (
	// jsImportRe finds relative module specifiers: from './helpers',
	// require('../support/assert'), import('./x').
	jsImportRe = regexp.MustCompile(`(?:\bfrom\s*|\brequire\s*\(\s*|\bimport\s*\(\s*|^\s*import\s+)['"](\.{1,2}/[^'"]+)['"]`)
	// pyImportRe finds `from .helpers import x` and `from tests.helpers import x`.
	pyImportRe = regexp.MustCompile(`^\s*from\s+(\.*)([\w.]*)\s+import\b`)
	// rbImportRe finds require_relative 'support/matchers'.
	rbImportRe = regexp.MustCompile(`\brequire_relative\s*\(?\s*['"]([^'"]+)['"]`)
)

var jsExts = []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", "/index.ts", "/index.js"}

// importedPaths returns the candidate paths of the modules a test file
// imports by relative path (JS/TS, Python, Ruby).
func importedPaths(f *core.FileInfo, dir string) []string {
	var out []string
	lines := f.NewLines()
	if lines == nil || !fullContent(f) {
		return nil
	}
	for _, ll := range lines {
		switch {
		case f.Lang.IsJSLike():
			for _, m := range jsImportRe.FindAllStringSubmatch(ll.NoComment, -1) {
				base := path.Join(dir, m[1])
				for _, ext := range jsExts {
					out = append(out, base+ext)
				}
			}
		case f.Lang == lang.Python:
			m := pyImportRe.FindStringSubmatch(ll.NoComment)
			if m == nil || m[2] == "" {
				continue
			}
			mod := strings.ReplaceAll(m[2], ".", "/")
			roots := []string{dir, "."}
			if dots := len(m[1]); dots > 0 {
				d := dir
				for i := 1; i < dots; i++ {
					d = path.Dir(d)
				}
				roots = []string{d}
			}
			for _, r := range roots {
				out = append(out, path.Join(r, mod)+".py", path.Join(r, mod, "__init__.py"))
			}
		case f.Lang == lang.Ruby:
			for _, m := range rbImportRe.FindAllStringSubmatch(ll.NoComment, -1) {
				p := path.Join(dir, m[1])
				if !strings.HasSuffix(p, ".rb") {
					p += ".rb"
				}
				out = append(out, p)
			}
		}
	}
	return out
}

var (
	// bareCallRe matches a statement that is a bare function call (no
	// receiver): memberDefaults(user), await expectSaved(order).
	bareCallRe = regexp.MustCompile(`^(?:await\s+|try\s+)?([A-Za-z_][\w$]*)\s*!?\s*\(`)
	// importLineRe matches a line that imports names.
	importLineRe = regexp.MustCompile(`^\s*(?:import|from|use|using|require|include|#include)\b|\brequire\s*\(`)
	// supportImportRe matches an import that names a test-support module:
	// a relative path, a project alias, or a module named like a helper.
	supportImportRe = regexp.MustCompile(`(?i)['"](?:\.{1,2}/|@/|~/)|test|spec|helper|support|assert|check|expect|matcher|fixture|conftest`)
)

// notHelpers are calls that print, convert or wait rather than check.
var notHelpers = map[string]bool{"print": true, "println": true, "printf": true, "puts": true, "p": true, "pp": true,
	"log": true, "debug": true, "dump": true, "var_dump": true, "print_r": true, "echo": true, "dbg": true,
	"len": true, "str": true, "int": true, "float": true, "list": true, "dict": true, "repr": true, "format": true,
	"sprintf": true, "String": true, "Number": true, "Boolean": true, "sleep": true, "setTimeout": true,
	"render": true, "mount": true, "act": true, "fireEvent": true, "waitFor": true, "run": true, "exec": true}

// unresolvedHelper reports whether new test n hands its checks to a helper
// narc cannot read: a statement that is a bare call to a name the old
// test o never used, which is not defined anywhere in the change, does
// not look like a print or a conversion, and is imported from a
// test-support module (or, in a patch, from somewhere the patch does not
// show). It returns the helper's name.
func (l *ledger) unresolvedHelper(o, n *instance) (string, bool) {
	for rel := n.block.Header + 1; rel <= n.block.End && rel-1 < len(n.seg.lines); rel++ {
		if n.seg.count(rel-1) > 0 {
			continue // an assertion, already counted
		}
		m := bareCallRe.FindStringSubmatch(strings.TrimSpace(n.seg.lines[rel-1].Code))
		if m == nil {
			continue
		}
		name := m[1]
		if o.bag[name] > 0 || stopWords[name] || notHelpers[name] || cLikeKeywords[name] || notDefs[name] || l.definedNames()[name] {
			continue
		}
		if _, found := l.externalHelpers(n.file)[name]; found {
			continue // narc read it: it was credited (or checks nothing)
		}
		if imported, support := l.importOf(n.file, name); imported && support || !imported && !fullContent(n.file) {
			return name, true
		}
	}
	return "", false
}

// importOf looks for the line of f that imports name. support reports
// whether that import names a test-support module.
func (l *ledger) importOf(f *core.FileInfo, name string) (imported, support bool) {
	lines, ok := l.imports[f]
	if !ok {
		for _, seg := range l.segments(f, after) {
			for _, ll := range seg.lines {
				if importLineRe.MatchString(ll.Code) {
					lines = append(lines, ll)
				}
			}
		}
		l.imports[f] = lines
	}
	for _, ll := range lines {
		for _, id := range identRe.FindAllString(ll.Code, -1) {
			if id == name {
				return true, supportImportRe.MatchString(ll.NoComment)
			}
		}
	}
	return false, false
}
