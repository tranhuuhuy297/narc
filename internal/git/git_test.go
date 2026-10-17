package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, p, c string) {
	t.Helper()
	full := filepath.Join(dir, p)
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChangesets(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "src/a.go", "package a\n")
	write(t, dir, "src/a_test.go", "package a\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "one")
	write(t, dir, "src/a.go", "package a\n\nfunc A() {}\n")
	gitT(t, dir, "commit", "-qam", "two")
	write(t, dir, "src/b.go", "package a // untracked\n")
	write(t, dir, "src/a.go", "package a\n\nfunc A() { panic(1) }\n")

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	cs, err := r.WorkingChangeset("", true)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range cs.Files {
		paths = append(paths, f.Path())
	}
	if !reflect.DeepEqual(paths, []string{"src/a.go", "src/b.go"}) {
		t.Errorf("working paths = %v", paths)
	}
	if b, ok := cs.Source.New("src/a_test.go"); !ok || string(b) != "package a\n" {
		t.Errorf("unchanged file not readable: %q %v", b, ok)
	}
	if got := cs.Source.(core.Lister).ListNew("src"); !reflect.DeepEqual(got, []string{"src/a.go", "src/a_test.go", "src/b.go"}) {
		t.Errorf("ListNew = %v", got)
	}

	head, _ := r.Resolve("HEAD")
	ccs, err := r.CommitChangeset(head)
	if err != nil || len(ccs.Files) != 1 || ccs.Files[0].Path() != "src/a.go" {
		t.Fatalf("commit changeset: %v %+v", err, ccs)
	}
	if old, ok := ccs.Source.Old("src/a.go"); !ok || string(old) != "package a\n" {
		t.Errorf("commit old = %q", old)
	}
	root, _ := r.Resolve("HEAD~1")
	rcs, err := r.CommitChangeset(root)
	if err != nil || len(rcs.Files) != 2 {
		t.Fatalf("root commit changeset: %v %d files", err, len(rcs.Files))
	}
	if got := rcs.Source.(core.Lister).ListNew(""); len(got) != 2 {
		t.Errorf("root ListNew = %v", got)
	}
	if hd, err := r.HooksDir(); err != nil || filepath.Base(hd) != "hooks" {
		t.Errorf("HooksDir = %q %v", hd, err)
	}
}
