package core

import (
	"sort"
	"strings"
	"sync"

	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// ContentSource gives access to full file contents before and after a
// change. Implementations must be safe for concurrent use.
type ContentSource interface {
	// Old returns the content of path (an old-side path) before the change.
	Old(path string) ([]byte, bool)
	// New returns the content of path (a new-side path) after the change.
	New(path string) ([]byte, bool)
}

// Lister is an optional ContentSource extension that lists files on the new
// side, including files the change did not touch.
type Lister interface {
	// ListNew returns the slash-separated paths of all files on the new
	// side under dir ("" for the whole tree).
	ListNew(dir string) []string
}

// MemSource is an in-memory ContentSource.
type MemSource struct {
	OldFiles map[string][]byte
	NewFiles map[string][]byte
}

func (m *MemSource) Old(p string) ([]byte, bool) { b, ok := m.OldFiles[p]; return b, ok }
func (m *MemSource) New(p string) ([]byte, bool) { b, ok := m.NewFiles[p]; return b, ok }

// ListNew implements Lister.
func (m *MemSource) ListNew(dir string) []string {
	var out []string
	for p := range m.NewFiles {
		if dir == "" || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Changeset is the input to an analysis: a set of file diffs plus,
// optionally, access to full file contents.
type Changeset struct {
	Files  []*diff.FileDiff
	Source ContentSource // may be nil (e.g. a bare .patch file)
	Label  string        // human description, e.g. "HEAD → working tree"
}

// Settings carries the configuration that rules need.
type Settings struct {
	// Protected globs: any change to a matching path is a finding.
	Protected []string
	// TestGlobs are extra globs that mark files as tests.
	TestGlobs []string
}

// Analysis is what rules operate on.
type Analysis struct {
	Files    []*FileInfo
	Settings Settings
	// Source reads full contents, including files the change did not
	// touch (nil for bare patches). Prefer ReadNew/ListNew.
	Source ContentSource

	memoMu sync.Mutex
	memo   map[any]any
}

// ReadNew returns the new-side content of any file in the repository,
// changed or not (e.g. the existing tests of a changed source file). It
// returns false when contents are unavailable (bare patches).
func (a *Analysis) ReadNew(path string) ([]byte, bool) {
	if a.Source == nil {
		return nil, false
	}
	return a.Source.New(path)
}

// ListNew lists new-side files under dir, changed or not. ok is false when
// the source cannot list files (bare patches).
func (a *Analysis) ListNew(dir string) (paths []string, ok bool) {
	l, isLister := a.Source.(Lister)
	if a.Source == nil || !isLister {
		return nil, false
	}
	return l.ListNew(dir), true
}

// Memo returns the value cached under key, computing it with build on
// first use. Rules use it to share expensive work (parsed configs, test
// ledgers) within one analysis.
func (a *Analysis) Memo(key any, build func() any) any {
	a.memoMu.Lock()
	if v, ok := a.memo[key]; ok {
		a.memoMu.Unlock()
		return v
	}
	a.memoMu.Unlock()
	v := build()
	a.memoMu.Lock()
	defer a.memoMu.Unlock()
	if a.memo == nil {
		a.memo = map[any]any{}
	}
	if prev, ok := a.memo[key]; ok {
		return prev
	}
	a.memo[key] = v
	return v
}

// TestFiles returns the files classified as tests.
func (a *Analysis) TestFiles() []*FileInfo {
	var out []*FileInfo
	for _, f := range a.Files {
		if f.IsTest {
			out = append(out, f)
		}
	}
	return out
}

// Line is a lexed line of a diff.
type Line struct {
	lang.LexedLine
	// No is the line number on its own side: new-side for added and
	// context lines, old-side for removed lines.
	No int
	// Hunk is the index of the hunk the line belongs to.
	Hunk int
}

// FileInfo is a changed file prepared for rules.
type FileInfo struct {
	Diff    *diff.FileDiff
	Path    string // new path, or old path for deletions
	OldPath string // old path ("" for additions)
	Lang    lang.Lang

	IsTest bool   // test code (by path, or by language-specific content)
	IsCI   bool   // CI pipeline definition
	Config string // config kind (see ConfigKind), "" if not a known config file

	// Added and Removed are the lexed added/removed lines.
	Added   []Line
	Removed []Line

	src ContentSource

	once sync.Once
	// oldPart/newPart are lexed only on lines inside hunks (other lines
	// have just Raw). Full lexing happens lazily, on first use.
	oldPart, newPart []lang.LexedLine
	oldFull, newFull []lang.LexedLine
	oldOnce, newOnce sync.Once
	oldKeep, newKeep func(int) bool
	hasOld           bool
	hasNew           bool
	oldRaw           []byte
	newRaw           []byte
	// Fallbacks reconstructed from hunks when full content is unavailable.
	oldFrag map[int]lang.LexedLine
	newFrag map[int]lang.LexedLine
}

// NewFileInfo prepares a file diff for analysis.
func NewFileInfo(fd *diff.FileDiff, src ContentSource, s Settings) *FileInfo {
	f := &FileInfo{Diff: fd, Path: fd.Path(), OldPath: fd.OldPath, src: src}
	f.Lang = lang.Detect(f.Path)
	f.IsCI = IsCIPath(f.Path)
	f.Config = ConfigKind(f.Path)
	f.IsTest = IsTestPath(f.Path) || MatchAny(s.TestGlobs, f.Path)
	f.load()
	if !f.IsTest && f.Lang == lang.Rust && f.hasRustTests() {
		// Rust unit tests live next to the code; rules look at test markers
		// such as #[ignore] anywhere in the file.
		f.IsTest = true
	}
	f.Added, f.Removed = f.lexDiffLines()
	return f
}

var rustTestMarkers = []string{"#[cfg(test)]", "#[test]", "#[rstest", "#[test_case", "::test]", "::test("}

// hasRustTests reports whether a Rust source file contains unit tests on
// either side of the change (full contents when available, else hunks).
func (f *FileInfo) hasRustTests() bool {
	has := func(s string) bool {
		for _, m := range rustTestMarkers {
			if strings.Contains(s, m) {
				return true
			}
		}
		return false
	}
	if (f.hasNew && has(string(f.newRaw))) || (f.hasOld && has(string(f.oldRaw))) {
		return true
	}
	for _, h := range f.Diff.Hunks {
		for _, l := range h.Lines {
			if has(l.Text) {
				return true
			}
		}
	}
	return false
}

func (f *FileInfo) load() {
	f.once.Do(func() {
		if f.src != nil {
			if f.Diff.Kind != diff.Added && f.OldPath != "" {
				f.oldRaw, f.hasOld = f.src.Old(f.OldPath)
			}
			if f.Diff.Kind != diff.Deleted && f.Diff.NewPath != "" {
				f.newRaw, f.hasNew = f.src.New(f.Diff.NewPath)
			}
		}
		f.oldKeep, f.newKeep = hunkRanges(f.Diff)
		if f.hasOld {
			f.oldPart = lang.LexSome(f.Lang, string(f.oldRaw), f.oldKeep)
		}
		if f.hasNew {
			f.newPart = lang.LexSome(f.Lang, string(f.newRaw), f.newKeep)
		}
		if !f.hasOld || !f.hasNew {
			f.oldFrag, f.newFrag = f.fragments()
		}
	})
}

// hunkRanges returns predicates for the old-side and new-side lines that
// fall inside a hunk (changed lines and their context).
func hunkRanges(d *diff.FileDiff) (old, new func(int) bool) {
	type span struct{ a, b int }
	var os, ns []span
	for _, h := range d.Hunks {
		os = append(os, span{h.OldStart, h.OldStart + h.OldLines - 1})
		ns = append(ns, span{h.NewStart, h.NewStart + h.NewLines - 1})
	}
	in := func(ss []span) func(int) bool {
		return func(n int) bool {
			for _, s := range ss {
				if n >= s.a && n <= s.b {
					return true
				}
			}
			return false
		}
	}
	return in(os), in(ns)
}

// fragments lexes each hunk's old and new side on its own. It is used when
// full file contents are not available.
func (f *FileInfo) fragments() (old, new map[int]lang.LexedLine) {
	old, new = map[int]lang.LexedLine{}, map[int]lang.LexedLine{}
	for _, h := range f.Diff.Hunks {
		var oldText, newText []string
		var oldNos, newNos []int
		for _, l := range h.Lines {
			if l.Op != diff.Add {
				oldText = append(oldText, l.Text)
				oldNos = append(oldNos, l.OldNo)
			}
			if l.Op != diff.Del {
				newText = append(newText, l.Text)
				newNos = append(newNos, l.NewNo)
			}
		}
		for i, ll := range lang.Lex(f.Lang, strings.Join(oldText, "\n")) {
			if i < len(oldNos) {
				old[oldNos[i]] = ll
			}
		}
		for i, ll := range lang.Lex(f.Lang, strings.Join(newText, "\n")) {
			if i < len(newNos) {
				new[newNos[i]] = ll
			}
		}
	}
	return old, new
}

func (f *FileInfo) lexDiffLines() (added, removed []Line) {
	for hi, h := range f.Diff.Hunks {
		for _, l := range h.Lines {
			switch l.Op {
			case diff.Add:
				ll, ok := f.NewLine(l.NewNo)
				if !ok || ll.Raw != l.Text {
					ll = lang.LexLine(f.Lang, l.Text)
				}
				added = append(added, Line{LexedLine: ll, No: l.NewNo, Hunk: hi})
			case diff.Del:
				ll, ok := f.OldLine(l.OldNo)
				if !ok || ll.Raw != l.Text {
					ll = lang.LexLine(f.Lang, l.Text)
				}
				removed = append(removed, Line{LexedLine: ll, No: l.OldNo, Hunk: hi})
			}
		}
	}
	return
}

// HasOld reports whether the full old content is available.
func (f *FileInfo) HasOld() bool { return f.hasOld }

// HasNew reports whether the full new content is available.
func (f *FileInfo) HasNew() bool { return f.hasNew }

// OldContent returns the full old content, if available.
func (f *FileInfo) OldContent() ([]byte, bool) { return f.oldRaw, f.hasOld }

// NewContent returns the full new content, if available.
func (f *FileInfo) NewContent() ([]byte, bool) { return f.newRaw, f.hasNew }

// OldLines returns the full lexed old content (nil if unavailable).
func (f *FileInfo) OldLines() []lang.LexedLine {
	if !f.hasOld {
		return nil
	}
	f.oldOnce.Do(func() { f.oldFull = lang.Lex(f.Lang, string(f.oldRaw)) })
	return f.oldFull
}

// NewLines returns the full lexed new content (nil if unavailable).
func (f *FileInfo) NewLines() []lang.LexedLine {
	if !f.hasNew {
		return nil
	}
	f.newOnce.Do(func() { f.newFull = lang.Lex(f.Lang, string(f.newRaw)) })
	return f.newFull
}

// NewLine returns lexed new-side line n (1-based), from full content when
// available and otherwise from hunk context.
func (f *FileInfo) NewLine(n int) (lang.LexedLine, bool) {
	if f.hasNew {
		if n < 1 || n > len(f.newPart) {
			return lang.LexedLine{}, false
		}
		if f.newKeep(n) {
			return f.newPart[n-1], true
		}
		return f.NewLines()[n-1], true
	}
	l, ok := f.newFrag[n]
	return l, ok
}

// OldLine returns lexed old-side line n (1-based).
func (f *FileInfo) OldLine(n int) (lang.LexedLine, bool) {
	if f.hasOld {
		if n < 1 || n > len(f.oldPart) {
			return lang.LexedLine{}, false
		}
		if f.oldKeep(n) {
			return f.oldPart[n-1], true
		}
		return f.OldLines()[n-1], true
	}
	l, ok := f.oldFrag[n]
	return l, ok
}

// AnchorForOld maps a removed old-side line to the new-side line at which
// the removal happened (the first new-side line after it). It returns 0 for
// deleted files.
func (f *FileInfo) AnchorForOld(oldNo int) int {
	if f.Diff.Kind == diff.Deleted {
		return 0
	}
	for _, h := range f.Diff.Hunks {
		lastNew := h.NewStart - 1
		if h.NewLines == 0 {
			lastNew = h.NewStart
		}
		for _, l := range h.Lines {
			if l.NewNo > 0 {
				lastNew = l.NewNo
			}
			if l.Op == diff.Del && l.OldNo == oldNo {
				n := lastNew + 1
				if n < 1 {
					n = 1
				}
				// Clamp to the end of the new file.
				if f.hasNew && n > len(f.newPart) {
					n = len(f.newPart)
				}
				return n
			}
		}
	}
	return 0
}

// AddedText returns the code (comments and string contents stripped) of
// all added lines joined by newlines. Handy for whole-diff searches.
func (f *FileInfo) AddedText() string {
	var b strings.Builder
	for _, l := range f.Added {
		b.WriteString(l.Code)
		b.WriteByte('\n')
	}
	return b.String()
}

// RemovedText is AddedText for removed lines.
func (f *FileInfo) RemovedText() string {
	var b strings.Builder
	for _, l := range f.Removed {
		b.WriteString(l.Code)
		b.WriteByte('\n')
	}
	return b.String()
}
