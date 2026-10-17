package hook

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/git"
)

// fileOp is one file operation requested by a tool call.
type fileOp struct {
	path    string // the file the tool reads: absolute, or relative to the agent's cwd
	newPath string // destination when the tool moves the file ("" = path)
	// apply returns the file after the tool ran, given its current content.
	// keep=false means the tool deletes the file. ok=false means the tool
	// itself would fail (e.g. old_string not found), so there is nothing
	// to judge.
	apply func(old []byte, exists bool) (content []byte, keep, ok bool)
}

func writeOp(path, content string) fileOp {
	return fileOp{path: path, apply: func([]byte, bool) ([]byte, bool, bool) {
		return []byte(content), true, true
	}}
}

func deleteOp(path string) fileOp {
	return fileOp{path: path, apply: func(_ []byte, exists bool) ([]byte, bool, bool) {
		return nil, false, exists
	}}
}

func replaceOp(path string, reps []replacement) fileOp {
	return fileOp{path: path, apply: func(old []byte, exists bool) ([]byte, bool, bool) {
		out, ok := applyReplacements(string(old), exists, reps)
		return []byte(out), true, ok
	}}
}

// replacement is one old_string → new_string edit.
type replacement struct {
	old, new string
	all      bool // replace every occurrence instead of the first
	// overwrite: an empty old string replaces the whole file, even one
	// that has content (OpenCode's edit tool).
	overwrite bool
}

// applyReplacements replays Edit/StrReplace/replace tools: each replacement
// in order, the first occurrence unless all is set. An empty old string
// creates a file that does not exist yet (or is empty), or with overwrite
// replaces the file. It reports false when the tool would fail.
func applyReplacements(content string, exists bool, reps []replacement) (string, bool) {
	for _, r := range reps {
		if r.old == "" {
			if exists && content != "" && !r.overwrite {
				return "", false
			}
			content, exists = r.new, true
			continue
		}
		if !exists {
			return "", false
		}
		old, nw := r.old, r.new
		if !strings.Contains(content, old) && strings.Contains(content, "\r\n") {
			// Agents send LF; the tools match CRLF files anyway.
			old = strings.ReplaceAll(old, "\n", "\r\n")
			nw = strings.ReplaceAll(nw, "\n", "\r\n")
		}
		if !strings.Contains(content, old) {
			return "", false
		}
		if r.all {
			content = strings.ReplaceAll(content, old, nw)
		} else {
			content = strings.Replace(content, old, nw, 1)
		}
	}
	return content, true
}

// pendingEdit is a file operation replayed in memory.
type pendingEdit struct {
	oldPath, newPath string // repository-relative; "" when absent on that side
	old, new         []byte
	hasOld, hasNew   bool
}

// fileState is a file's content at one point of a replay.
type fileState struct {
	content []byte
	exists  bool
}

// replay reads each file the operations touch and applies the operations
// in memory, in order, so that a later operation sees the earlier ones (a
// patch may delete a file and add it back, or update it twice). It returns
// one pendingEdit per file whose content changes; a move pairs its source
// and destination. Operations on files outside the workspace, on
// unreadable or huge files, and operations the tool itself would reject
// are left out.
func (w *workspace) replay(ops []fileOp, cwd string) []pendingEdit {
	disk := map[string]fileState{} // before the tool call
	cur := map[string]fileState{}  // after the operations so far
	var order []string             // paths in the order they were first touched
	movedTo := map[string]string{} // source → destination of a move
	load := func(rel, abs string) (fileState, error) {
		if s, ok := cur[rel]; ok {
			return s, nil
		}
		b, exists, err := readCurrent(abs)
		if err != nil {
			return fileState{}, err
		}
		s := fileState{content: b, exists: exists}
		disk[rel], cur[rel] = s, s
		order = append(order, rel)
		return s, nil
	}
	for _, op := range ops {
		abs := w.abs(op.path, cwd)
		rel, ok := w.rel(abs)
		if !ok {
			continue
		}
		dest, destAbs := rel, abs
		if op.newPath != "" {
			destAbs = w.abs(op.newPath, cwd)
			if dest, ok = w.rel(destAbs); !ok {
				continue
			}
		}
		src, err := load(rel, abs)
		if err != nil {
			continue
		}
		content, keep, ok := op.apply(src.content, src.exists)
		if !ok || (keep && len(content) > git.MaxFileSize) {
			continue
		}
		if dest != rel {
			if _, err := load(dest, destAbs); err != nil {
				continue
			}
			cur[rel] = fileState{}
			movedTo[rel] = dest
		}
		if !keep {
			content = nil
		}
		cur[dest] = fileState{content: content, exists: keep}
	}

	var out []pendingEdit
	done := map[string]bool{}
	for _, rel := range order {
		if done[rel] {
			continue
		}
		done[rel] = true
		before, after := disk[rel], cur[rel]
		var e pendingEdit
		if before.exists {
			e.oldPath, e.old, e.hasOld = rel, before.content, true
		}
		if after.exists {
			e.newPath, e.new, e.hasNew = rel, after.content, true
		}
		// A file moved to a path that did not exist is a rename.
		if dest, moved := movedTo[rel]; moved && !done[dest] && before.exists && !after.exists && !disk[dest].exists && cur[dest].exists {
			done[dest] = true
			e.newPath, e.new, e.hasNew = dest, cur[dest].content, true
		}
		if !e.hasOld && !e.hasNew {
			continue
		}
		if e.hasOld && e.hasNew && e.oldPath == e.newPath && bytes.Equal(e.old, e.new) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// touched returns the repository-relative paths the operations name.
func (w *workspace) touched(ops []fileOp, cwd string) map[string]bool {
	out := map[string]bool{}
	for _, op := range ops {
		for _, p := range []string{op.path, op.newPath} {
			if p == "" {
				continue
			}
			if rel, ok := w.rel(w.abs(p, cwd)); ok {
				out[rel] = true
			}
		}
	}
	return out
}

func readCurrent(abs string) ([]byte, bool, error) {
	st, err := os.Stat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	if st.Size() > git.MaxFileSize {
		return nil, false, errors.New("file too large")
	}
	b, err := os.ReadFile(abs)
	return b, err == nil, err
}

// abs resolves a tool's path against the agent's cwd.
func (w *workspace) abs(p, cwd string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if cwd == "" {
		cwd = w.dir
	}
	return filepath.Join(cwd, p)
}

// rel returns abs relative to the workspace root, slash-separated, when it
// is inside the root (and not inside .git). Symlinks are resolved when the
// plain comparison fails: macOS reports /var/... for /private/var/...
func (w *workspace) rel(abs string) (string, bool) {
	rel, ok := relInside(w.root, abs)
	if !ok {
		root, err := filepath.EvalSymlinks(w.root)
		if err != nil {
			return "", false
		}
		if rel, ok = relInside(root, evalExisting(abs)); !ok {
			return "", false
		}
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return "", false
	}
	return rel, true
}

func relInside(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// evalExisting resolves symlinks in the longest existing prefix of p; the
// file itself may not exist yet.
func evalExisting(p string) string {
	dir, rest := p, ""
	for {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// maxContextFiles bounds how many other changed files are added as context
// to a pre-edit check; beyond it the edit is judged on its own.
const maxContextFiles = 300

// editChangeset builds a changeset from in-memory file versions plus the
// files in context (the session's other changes) that are not owned by
// the edit. owned must name every path any variant of the edit touches,
// so that the context is the same whichever variant is judged.
func editChangeset(edits []pendingEdit, context *core.Changeset, owned map[string]bool) *core.Changeset {
	mem := &core.MemSource{OldFiles: map[string][]byte{}, NewFiles: map[string][]byte{}}
	src := &layeredSource{top: mem, owned: owned}
	cs := &core.Changeset{Label: "proposed edit", Source: src}
	for _, e := range edits {
		fd := diff.Compute(e.oldPath, e.newPath, e.old, e.new, e.hasOld, e.hasNew, diff.DefaultContext)
		if fd.Kind == diff.Modified && !fd.Binary && len(fd.Hunks) == 0 {
			continue // nothing changes (or the file is absent on both sides)
		}
		cs.Files = append(cs.Files, fd)
		if e.hasOld {
			mem.OldFiles[e.oldPath] = e.old
		}
		if e.hasNew {
			mem.NewFiles[e.newPath] = e.new
		}
	}
	if context == nil {
		return cs
	}
	// Files the edit does not touch (a test's helpers, the tests of an
	// edited source file) are read and listed from the work tree, as
	// `narc scan` would, even when there are too many changes to add
	// them all as context.
	src.base = context.Source
	if l, ok := context.Source.(core.Lister); ok {
		cs.Source = &layeredLister{layeredSource: src, base: l}
	}
	if len(context.Files) > maxContextFiles {
		return cs
	}
	for _, fd := range context.Files {
		if owned[fd.OldPath] || owned[fd.NewPath] {
			continue
		}
		cs.Files = append(cs.Files, fd)
	}
	return cs
}

// pathsOfEdits returns every path the edits name on either side.
func pathsOfEdits(sets ...[]pendingEdit) map[string]bool {
	out := map[string]bool{}
	for _, edits := range sets {
		for _, e := range edits {
			if e.oldPath != "" {
				out[e.oldPath] = true
			}
			if e.newPath != "" {
				out[e.newPath] = true
			}
		}
	}
	return out
}

// rebase moves the old side of each edit back to the session baseline,
// using the session's changes: a file the agent created in this session
// had no old side, one it modified had its baseline content. It also
// returns, per edit, the change from the baseline to the file on disk now,
// and whether any edited file already differs from the baseline. Without
// session changes (outside git) the edits are returned as they are.
func rebase(edits []pendingEdit, session *core.Changeset) (rebased, before []pendingEdit, changed bool) {
	for _, e := range edits {
		base, differs := baseline(e, session)
		r := e
		r.oldPath, r.old, r.hasOld = base.path, base.content, base.exists
		rebased = append(rebased, r)
		b := pendingEdit{oldPath: base.path, old: base.content, hasOld: base.exists}
		if e.hasOld {
			b.newPath, b.new, b.hasNew = e.oldPath, e.old, true
		}
		before = append(before, b)
		changed = changed || differs
	}
	return rebased, before, changed
}

// baselineFile is a file as it was at the session baseline.
type baselineFile struct {
	path    string
	content []byte
	exists  bool
}

// baseline returns the edited file as it was when the session started, and
// whether that differs from the file on disk now.
func baseline(e pendingEdit, session *core.Changeset) (baselineFile, bool) {
	disk := baselineFile{path: e.oldPath, content: e.old, exists: e.hasOld}
	if session == nil || session.Source == nil {
		return disk, false
	}
	for _, fd := range session.Files {
		switch {
		case e.hasOld && fd.NewPath == e.oldPath:
			// Added, modified or renamed since the baseline.
			if fd.Kind == diff.Added || fd.OldPath == "" {
				return baselineFile{}, true
			}
			if b, ok := session.Source.Old(fd.OldPath); ok {
				return baselineFile{path: fd.OldPath, content: b, exists: true}, true
			}
			return disk, false // baseline content unavailable (e.g. too large)
		case !e.hasOld && fd.Kind == diff.Deleted && fd.OldPath == e.newPath:
			// Deleted in this session and now written again.
			if b, ok := session.Source.Old(fd.OldPath); ok {
				return baselineFile{path: fd.OldPath, content: b, exists: true}, true
			}
			return disk, false
		}
	}
	return disk, false
}

// judgeEdit returns the findings a proposed edit introduces, at or above
// the block threshold, in the files it edits.
//
// Each edited file is judged from where it stood at the session baseline
// to its content after the edit, with the session's other changes as
// context. Judging against the baseline rather than the file on disk keeps
// the agent's own work in progress from becoming the reference: rewriting
// a test it wrote ten minutes ago, or a golden file it created for its new
// test, does not weaken any check that existed before the session.
//
// When an edited file already differs from the baseline, a finding counts
// only if the file on disk does not already have it and the edit on its
// own (disk to proposed) shows that rule in that file. So an edit that
// makes an existing problem better is not blamed for what is left, while
// one that makes it worse is.
func (w *workspace) judgeEdit(edits []pendingEdit, session *core.Changeset, known map[string]bool) []core.Finding {
	threshold := w.blockThreshold()
	session = withListCache(session) // up to three analyses list the same tree
	rebased, before, changed := rebase(edits, session)
	owned := pathsOfEdits(edits, rebased)
	found := unknown(inPaths(w.run(editChangeset(rebased, session, owned)).AtLeast(threshold), owned), known)
	if len(found) == 0 || !changed {
		return found
	}
	onDisk := map[string]bool{}
	for _, f := range w.run(editChangeset(before, session, owned)).Findings {
		onDisk[f.Fingerprint] = true
	}
	shown := map[string]bool{} // rule and path pairs the edit shows by itself
	for _, f := range w.run(editChangeset(edits, session, owned)).Active() {
		shown[f.RuleID+"\x00"+f.Path] = true
	}
	var out []core.Finding
	for _, f := range found {
		if !onDisk[f.Fingerprint] && shown[f.RuleID+"\x00"+f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// layeredSource serves the edited files from memory and every other file
// from the context changeset's source.
type layeredSource struct {
	top   *core.MemSource
	owned map[string]bool
	base  core.ContentSource
}

func (s *layeredSource) Old(p string) ([]byte, bool) {
	if s.owned[p] || s.base == nil {
		return s.top.Old(p)
	}
	return s.base.Old(p)
}

func (s *layeredSource) New(p string) ([]byte, bool) {
	if s.owned[p] || s.base == nil {
		return s.top.New(p)
	}
	return s.base.New(p)
}

// layeredLister is a layeredSource whose context can list the work tree.
type layeredLister struct {
	*layeredSource
	base core.Lister
}

// ListNew implements core.Lister: the work tree's files as the edit
// leaves them, with the files it creates and without the ones it deletes
// or moves away.
func (s *layeredLister) ListNew(dir string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range s.base.ListNew(dir) {
		if !s.owned[p] {
			add(p)
		}
	}
	for _, p := range s.top.ListNew(dir) {
		add(p)
	}
	sort.Strings(out)
	return out
}

// listCache remembers a source's listings, which do not change while a
// hook runs.
type listCache struct {
	core.ContentSource
	lister core.Lister
	mu     sync.Mutex
	dirs   map[string][]string
}

func (c *listCache) ListNew(dir string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if paths, ok := c.dirs[dir]; ok {
		return paths
	}
	paths := c.lister.ListNew(dir)
	c.dirs[dir] = paths
	return paths
}

// withListCache returns cs with its listings cached (cs itself when its
// source cannot list files).
func withListCache(cs *core.Changeset) *core.Changeset {
	if cs == nil {
		return nil
	}
	l, ok := cs.Source.(core.Lister)
	if !ok {
		return cs
	}
	cached := *cs
	cached.Source = &listCache{ContentSource: cs.Source, lister: l, dirs: map[string][]string{}}
	return &cached
}
