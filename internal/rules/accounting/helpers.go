package accounting

import (
	"path"
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
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
func helpersIn(l lang.Lang, seg *segment, into map[string]int) {
	lines := seg.lines
	for i, ll := range lines {
		if !mayDefine(l, ll.Code) {
			continue
		}
		defs := map[string]bool{}
		definitions(l, ll.Code, defs)
		if len(defs) != 1 {
			continue
		}
		n := 0
		for k, end := i+1, blockEnd(l, lines, i); k <= end; k++ {
			n += seg.count(k)
		}
		for name := range defs {
			if old, ok := into[name]; !ok || n > old {
				into[name] = n
			}
		}
	}
}

// helperCalls counts the assertions a test makes through helpers it calls:
// helpers defined in the change, else in unchanged files near the test.
func (l *ledger) helperCalls(in *instance) int {
	n := 0
	var external map[string]int
	for rel := in.block.Header + 1; rel <= in.block.End && rel-1 < len(in.seg.lines); rel++ {
		for _, m := range callRe.FindAllStringSubmatch(in.seg.lines[rel-1].Code, -1) {
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
