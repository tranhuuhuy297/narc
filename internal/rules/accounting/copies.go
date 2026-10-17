package accounting

import (
	"path"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/testblocks"
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
// parent.
func (l *ledger) copyElsewhere(o *instance) bool {
	if l.a == nil || o.block.Bare == "" {
		return false
	}
	for _, c := range l.nearbyTests(o.file)[o.block.Bare] {
		if diceBags(o.bag, o.size, c.bag, c.size) >= 0.9 {
			return true
		}
	}
	return false
}

// nearbyTests returns the tests of the unchanged test files near f, by
// bare name (cached per directory).
func (l *ledger) nearbyTests(f *core.FileInfo) map[string][]copyTest {
	dir := path.Dir(f.Path)
	if t, ok := l.nearby[dir]; ok {
		return t
	}
	tests := map[string][]copyTest{}
	l.nearby[dir] = tests
	dirs := map[string]bool{dir: true}
	for _, d := range testDirNames {
		dirs[path.Join(dir, d)] = true
		if path.Base(dir) == d {
			dirs[path.Dir(dir)] = true
		}
	}
	changed := map[string]bool{}
	for _, cf := range l.a.Files {
		changed[cf.Path] = true
		changed[cf.OldPath] = true
	}
	read := 0
	for d := range dirs {
		list := d
		if d == "." {
			list = ""
		}
		paths, ok := l.a.ListNew(list)
		if !ok {
			return tests
		}
		for _, p := range paths {
			if read >= maxHelperFiles || path.Dir(p) != d || changed[p] || !core.IsTestPath(p) {
				continue
			}
			lg := lang.Detect(p)
			if !sameFamily(lg, f.Lang) || !testblocks.Supported(lg) {
				continue
			}
			b, ok := l.a.ReadNew(p)
			if !ok || len(b) > maxHelperSize {
				continue
			}
			read++
			seg := &segment{lines: lang.Lex(lg, string(b)), whole: true, hunk: -1, lang: lg}
			for _, blk := range testblocks.Find(lg, seg.lines) {
				bag, size := bodyBag(seg, blk)
				tests[blk.Bare] = append(tests[blk.Bare], copyTest{bag: bag, size: size})
			}
		}
	}
	return tests
}
