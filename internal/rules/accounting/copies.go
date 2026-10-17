package accounting

import (
	"bytes"
	"fmt"
	"path"
	"sort"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/testblocks"
)

// "Remove duplicate tests" deletes tests whose twins live in test files
// the change does not touch. Those files are only read when a test seems
// to have been deleted.

// copyTest is a test found in an unchanged file.
type copyTest struct {
	bag  map[string]int
	size int
}

// testDirNames are subdirectories that hold the tests of their parent.
var testDirNames = []string{"__tests__", "tests", "test", "spec", "specs"}

// copyElsewhere reports whether a near-identical test with the same name
// as old test o exists in an unchanged test file near it: in its
// directory, its test subdirectories, or (for a test directory) its
// parent. Only files that mention the test's name are parsed.
func (l *ledger) copyElsewhere(o *instance) bool {
	if l.a == nil || o.block.Bare == "" {
		return false
	}
	name := []byte(o.block.Bare)
	for _, p := range l.nearbyTestFiles(o.file) {
		e := l.extFile(p)
		if e == nil || !bytes.Contains(e.src, name) {
			continue
		}
		for _, c := range l.testsOf(p, e)[o.block.Bare] {
			if diceBags(o.bag(), o.size(), c.bag, c.size) >= 0.9 {
				return true
			}
		}
	}
	return false
}

// testsOf returns the tests of an unchanged file, by bare name (cached).
func (l *ledger) testsOf(p string, e *extSource) map[string][]copyTest {
	if t, ok := l.nearby[p]; ok {
		return t
	}
	tests := map[string][]copyTest{}
	l.nearby[p] = tests
	seg := &segment{lines: lang.Lex(e.lang, string(e.src)), whole: true, hunk: -1, lang: e.lang}
	for _, blk := range testblocks.Find(e.lang, seg.lines) {
		bag, size := bodyBag(seg, blk)
		tests[blk.Bare] = append(tests[blk.Bare], copyTest{bag: bag, size: size})
	}
	return tests
}

// nearbyTestFiles lists the unchanged test files near f, at most
// maxHelperFiles of them (cached per directory and language family).
func (l *ledger) nearbyTestFiles(f *core.FileInfo) []string {
	dir := path.Dir(f.Path)
	key := fmt.Sprintf("%s\x00%d", dir, f.Lang)
	if ps, ok := l.nearPaths[key]; ok {
		return ps
	}
	var out []string
	l.nearPaths[key] = nil
	if _, ok := l.a.Source.(core.Lister); !ok || l.a.Source == nil {
		return nil
	}
	dirs := map[string]bool{dir: true}
	for _, d := range testDirNames {
		dirs[path.Join(dir, d)] = true
		if path.Base(dir) == d {
			dirs[path.Dir(dir)] = true
		}
	}
	var ds []string
	for d := range dirs {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	changed := l.changedPaths()
	for _, d := range ds {
		for _, p := range l.listDir(d) {
			if len(out) >= maxHelperFiles || path.Dir(p) != d || changed[p] || !core.IsTestPath(p) {
				continue
			}
			lg := lang.Detect(p)
			if sameFamily(lg, f.Lang) && testblocks.Supported(lg) {
				out = append(out, p)
			}
		}
	}
	l.nearPaths[key] = out
	return out
}
