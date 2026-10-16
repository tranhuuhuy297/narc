// Package git wraps the git CLI for narc.
package git

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
)

// MaxFileSize bounds the size of files narc reads into memory.
const MaxFileSize = 2 << 20

// ErrNotRepo is returned when the directory is not inside a git work tree.
var ErrNotRepo = errors.New("not a git repository (run narc inside a repo, or pass --patch)")

// Repo is a git work tree.
type Repo struct {
	Root   string // absolute path of the work tree root
	GitDir string // absolute path of the .git directory (per-worktree)

	mu    sync.Mutex
	batch *catFile
}

// Open finds the repository containing dir.
func Open(dir string) (*Repo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git is not installed or not on PATH")
	}
	out, err := run(dir, nil, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		return nil, ErrNotRepo
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, ErrNotRepo
	}
	return &Repo{Root: lines[0], GitDir: lines[1]}, nil
}

// Close releases background processes.
func (r *Repo) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.batch != nil {
		r.batch.close()
		r.batch = nil
	}
}

func run(dir string, stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.quotePath=false", "-c", "color.ui=false", "--no-pager"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// Run executes a git command in the repository root.
func (r *Repo) Run(args ...string) ([]byte, error) { return run(r.Root, nil, args...) }

// HasHead reports whether HEAD points at a commit.
func (r *Repo) HasHead() bool {
	_, err := r.Run("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	return err == nil
}

// Resolve resolves a revision to a commit SHA.
func (r *Repo) Resolve(rev string) (string, error) {
	out, err := r.Run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("unknown revision %q", rev)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the merge base of a and b.
func (r *Repo) MergeBase(a, b string) (string, error) {
	out, err := r.Run("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no merge base between %s and %s", a, b)
	}
	return strings.TrimSpace(string(out)), nil
}

// EmptyTree returns the object ID of the empty tree for this repository's
// hash algorithm.
func (r *Repo) EmptyTree() string {
	out, err := run(r.Root, strings.NewReader(""), "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	}
	return strings.TrimSpace(string(out))
}

var diffFlags = []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "-M", "--unified=3",
	"--src-prefix=a/", "--dst-prefix=b/", "--ignore-submodules"}

// Diff runs `git diff <args>` and parses the result.
func (r *Repo) Diff(args ...string) ([]*diff.FileDiff, error) {
	out, err := r.Run(append(append([]string{}, diffFlags...), args...)...)
	if err != nil {
		return nil, err
	}
	return diff.Parse(bytes.NewReader(out))
}

// Untracked lists untracked, non-ignored files.
func (r *Repo) Untracked() ([]string, error) {
	out, err := r.Run("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

// ReadWorktree reads a file from the work tree.
func (r *Repo) ReadWorktree(p string) ([]byte, bool) {
	full := filepath.Join(r.Root, filepath.FromSlash(p))
	st, err := os.Lstat(full)
	if err != nil || !st.Mode().IsRegular() || st.Size() > MaxFileSize {
		return nil, false
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, false
	}
	return b, true
}

// Blob returns the content of path at rev ("" for the index, i.e. ":path").
func (r *Repo) Blob(rev, p string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.batch == nil {
		b, err := startCatFile(r.Root)
		if err != nil {
			return nil, false
		}
		r.batch = b
	}
	spec := rev + ":" + p
	if strings.ContainsAny(p, "\n") {
		out, err := r.Run("show", spec)
		return out, err == nil
	}
	data, ok, err := r.batch.get(spec)
	if err != nil {
		r.batch.close()
		r.batch = nil
		return nil, false
	}
	return data, ok
}

type catFile struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startCatFile(dir string) (*catFile, error) {
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = dir
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &catFile{cmd: cmd, in: in, out: bufio.NewReaderSize(out, 1<<16)}, nil
}

func (c *catFile) get(spec string) ([]byte, bool, error) {
	if _, err := io.WriteString(c.in, spec+"\n"); err != nil {
		return nil, false, err
	}
	header, err := c.out.ReadString('\n')
	if err != nil {
		return nil, false, err
	}
	header = strings.TrimSuffix(header, "\n")
	if strings.HasSuffix(header, " missing") || strings.HasSuffix(header, " ambiguous") {
		return nil, false, nil
	}
	parts := strings.Fields(header)
	if len(parts) < 3 {
		return nil, false, fmt.Errorf("unexpected cat-file header %q", header)
	}
	size, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, false, err
	}
	buf := make([]byte, size+1) // content + trailing LF
	if _, err := io.ReadFull(c.out, buf); err != nil {
		return nil, false, err
	}
	if parts[1] != "blob" || size > MaxFileSize {
		return nil, false, nil
	}
	return buf[:size], true, nil
}

func (c *catFile) close() {
	c.in.Close()
	_ = c.cmd.Wait()
}

// ---- changesets ----

type source struct {
	oldFn  func(string) ([]byte, bool)
	newFn  func(string) ([]byte, bool)
	listFn func(dir string) []string
}

func (s source) Old(p string) ([]byte, bool) { return s.oldFn(p) }
func (s source) New(p string) ([]byte, bool) { return s.newFn(p) }

// ListNew implements core.Lister.
func (s source) ListNew(dir string) []string {
	if s.listFn == nil {
		return nil
	}
	return s.listFn(dir)
}

func splitNUL(b []byte) []string {
	var out []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// listWorktree lists tracked and untracked (non-ignored) files under dir.
func (r *Repo) listWorktree(dir string) []string {
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	if dir != "" {
		args = append(args, "--", dir)
	}
	out, err := r.Run(args...)
	if err != nil {
		return nil
	}
	return splitNUL(out)
}

// listRev lists the files of a revision under dir.
func (r *Repo) listRev(rev, dir string) []string {
	args := []string{"ls-tree", "-r", "-z", "--name-only", rev}
	if dir != "" {
		args = append(args, "--", dir)
	}
	out, err := r.Run(args...)
	if err != nil {
		return nil
	}
	return splitNUL(out)
}

// HooksDir returns the directory git runs hooks from, honouring
// core.hooksPath.
func (r *Repo) HooksDir() (string, error) {
	out, err := r.Run("rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// WorkingChangeset compares base (default HEAD) with the work tree,
// including staged changes and, optionally, untracked files.
func (r *Repo) WorkingChangeset(base string, untracked bool) (*core.Changeset, error) {
	label := ""
	if base == "" {
		if r.HasHead() {
			base = "HEAD"
		} else {
			base = r.EmptyTree()
			label = "empty repository → working tree"
		}
	}
	files, err := r.Diff(base)
	if err != nil {
		return nil, err
	}
	if untracked {
		paths, err := r.Untracked()
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			b, ok := r.ReadWorktree(p)
			if !ok {
				continue
			}
			files = append(files, diff.Compute("", p, nil, b, false, true, diff.DefaultContext))
		}
	}
	if label == "" {
		label = shortRev(base) + " → working tree"
	}
	return &core.Changeset{
		Files: files,
		Label: label,
		Source: source{
			oldFn:  func(p string) ([]byte, bool) { return r.Blob(base, p) },
			newFn:  r.ReadWorktree,
			listFn: r.listWorktree,
		},
	}, nil
}

// StagedChangeset compares HEAD with the index.
func (r *Repo) StagedChangeset() (*core.Changeset, error) {
	base := "HEAD"
	if !r.HasHead() {
		base = r.EmptyTree()
	}
	files, err := r.Diff("--cached", base)
	if err != nil {
		return nil, err
	}
	return &core.Changeset{
		Files: files,
		Label: "staged changes",
		Source: source{
			oldFn: func(p string) ([]byte, bool) { return r.Blob(base, p) },
			newFn: func(p string) ([]byte, bool) { return r.Blob("", p) },
			listFn: func(dir string) []string {
				args := []string{"ls-files", "-z", "--cached"}
				if dir != "" {
					args = append(args, "--", dir)
				}
				out, err := r.Run(args...)
				if err != nil {
					return nil
				}
				return splitNUL(out)
			},
		},
	}, nil
}

// RangeChangeset compares two revisions.
func (r *Repo) RangeChangeset(from, to string) (*core.Changeset, error) {
	files, err := r.Diff(from, to)
	if err != nil {
		return nil, err
	}
	return &core.Changeset{
		Files: files,
		Label: shortRev(from) + ".." + shortRev(to),
		Source: source{
			oldFn:  func(p string) ([]byte, bool) { return r.Blob(from, p) },
			newFn:  func(p string) ([]byte, bool) { return r.Blob(to, p) },
			listFn: func(dir string) []string { return r.listRev(to, dir) },
		},
	}, nil
}

// BaseChangeset compares merge-base(base, HEAD) with HEAD — what a pull
// request against base would contain.
func (r *Repo) BaseChangeset(base string) (*core.Changeset, error) {
	mb, err := r.MergeBase(base, "HEAD")
	if err != nil {
		return nil, err
	}
	cs, err := r.RangeChangeset(mb, "HEAD")
	if err != nil {
		return nil, err
	}
	cs.Label = base + "...HEAD"
	return cs, nil
}

// CommitChangeset returns the changes introduced by a single commit
// (against its first parent, or the empty tree for root commits). It uses
// one git process: diff-tree handles root commits with --root.
func (r *Repo) CommitChangeset(sha string) (*core.Changeset, error) {
	args := append([]string{"diff-tree", "-p", "-r", "--root", "--no-commit-id", "-m", "--first-parent"}, diffFlags[1:]...)
	out, err := r.Run(append(args, sha)...)
	if err != nil {
		return nil, err
	}
	files, err := diff.Parse(bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	parent := sha + "^"
	return &core.Changeset{
		Files: files,
		Label: shortRev(sha),
		Source: source{
			oldFn:  func(p string) ([]byte, bool) { return r.Blob(parent, p) },
			newFn:  func(p string) ([]byte, bool) { return r.Blob(sha, p) },
			listFn: func(dir string) []string { return r.listRev(sha, dir) },
		},
	}, nil
}

// ParseRange splits "A..B" or "A...B" into its ends. For "A...B" the start
// is the merge base.
func (r *Repo) ParseRange(spec string) (from, to string, err error) {
	if i := strings.Index(spec, "..."); i >= 0 {
		a, b := spec[:i], spec[i+3:]
		if b == "" {
			b = "HEAD"
		}
		mb, err := r.MergeBase(a, b)
		return mb, b, err
	}
	if i := strings.Index(spec, ".."); i >= 0 {
		a, b := spec[:i], spec[i+2:]
		if b == "" {
			b = "HEAD"
		}
		return a, b, nil
	}
	return "", "", fmt.Errorf("invalid range %q (want A..B or A...B)", spec)
}

func shortRev(s string) string {
	if len(s) == 40 || len(s) == 64 {
		if _, err := strconv.ParseUint(s[:8], 16, 64); err == nil {
			return s[:8]
		}
	}
	return s
}
