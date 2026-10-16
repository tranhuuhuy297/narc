package diff

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

const sample = `commit 123
Author: x

diff --git a/src/auth.test.ts b/src/auth.test.ts
index 1111111..2222222 100644
--- a/src/auth.test.ts
+++ b/src/auth.test.ts
@@ -10,7 +10,6 @@ describe('auth', () => {
   it('rejects expired tokens', () => {
     const t = makeToken({ exp: 0 })
-    expect(verify(t)).toBe(false)
-    expect(log).toHaveBeenCalled()
+    expect(true).toBe(true)
   })
 
-- weird line that starts with dashes
+++ weird added line
diff --git a/old name.py b/new name.py
similarity index 90%
rename from old name.py
rename to new name.py
diff --git a/gone.go b/gone.go
deleted file mode 100644
index 3333333..0000000
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package gone
-func X() {}
diff --git a/img.png b/img.png
new file mode 100644
index 0000000..4444444
Binary files /dev/null and b/img.png differ
diff --git "a/q\"uote.txt" "b/q\"uote.txt"
new file mode 100644
--- /dev/null
+++ "b/q\"uote.txt"
@@ -0,0 +1 @@
+hi
\ No newline at end of file
`

func TestParseGit(t *testing.T) {
	files, err := ParseString(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("got %d files", len(files))
	}
	f := files[0]
	if f.Path() != "src/auth.test.ts" || f.Kind != Modified {
		t.Errorf("file0: %+v", f)
	}
	add, del := f.Stats()
	if add != 2 || del != 3 {
		t.Errorf("stats: +%d -%d", add, del)
	}
	if got := f.Added()[0]; got.NewNo != 12 || got.Text != "    expect(true).toBe(true)" {
		t.Errorf("added0: %+v", got)
	}
	if got := f.Removed()[2]; got.Text != "- weird line that starts with dashes" || got.OldNo != 16 {
		t.Errorf("removed2: %+v", got)
	}
	if files[1].Kind != Renamed || files[1].OldPath != "old name.py" || files[1].NewPath != "new name.py" || files[1].Similarity != 90 {
		t.Errorf("rename: %+v", files[1])
	}
	if files[2].Kind != Deleted || files[2].Path() != "gone.go" || len(files[2].Removed()) != 2 {
		t.Errorf("delete: %+v", files[2])
	}
	if !files[3].Binary || files[3].Kind != Added || files[3].Path() != "img.png" {
		t.Errorf("binary: %+v", files[3])
	}
	if files[4].Path() != `q"uote.txt` || len(files[4].Added()) != 1 {
		t.Errorf("quoted: %+v", files[4])
	}
}

func TestParsePlainDiff(t *testing.T) {
	src := "--- a.txt\t2024-01-01\n+++ a.txt\t2024-01-02\n@@ -1 +1 @@\n-x\n+y\n--- b.txt\n+++ b.txt\n@@ -1,0 +1 @@\n+z\n"
	files, err := ParseString(src)
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%d err=%v", len(files), err)
	}
	if files[0].Path() != "a.txt" || files[1].Path() != "b.txt" {
		t.Errorf("paths: %s %s", files[0].Path(), files[1].Path())
	}
}

func apply(a []string, hunks []Hunk) []string {
	var out []string
	pos := 0 // index into a
	for _, h := range hunks {
		start := h.OldStart - 1
		if h.OldLines == 0 {
			start = h.OldStart
		}
		out = append(out, a[pos:start]...)
		pos = start
		for _, l := range h.Lines {
			switch l.Op {
			case Context:
				out = append(out, a[pos])
				pos++
			case Del:
				pos++
			case Add:
				out = append(out, l.Text)
			}
		}
	}
	return append(out, a[pos:]...)
}

func TestComputeRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", "c", "d", "e", "{", "}", ""}
	gen := func(n int) []string {
		s := make([]string, n)
		for i := range s {
			s[i] = alphabet[r.Intn(len(alphabet))]
		}
		return s
	}
	for iter := 0; iter < 3000; iter++ {
		a := gen(r.Intn(30))
		b := append([]string(nil), a...)
		// mutate
		for m := r.Intn(6); m > 0; m-- {
			switch op := r.Intn(3); {
			case op == 0 && len(b) > 0:
				i := r.Intn(len(b))
				b = append(b[:i], b[i+1:]...)
			case op == 1:
				i := r.Intn(len(b) + 1)
				b = append(b[:i], append([]string{alphabet[r.Intn(len(alphabet))] + "x"}, b[i:]...)...)
			default:
				if len(b) > 0 {
					b[r.Intn(len(b))] = "z"
				}
			}
		}
		for _, ctx := range []int{0, 1, 3} {
			h := Hunks(a, b, ctx)
			got := apply(a, h)
			if !reflect.DeepEqual(norm(got), norm(b)) {
				t.Fatalf("iter %d ctx %d:\na=%q\nb=%q\ngot=%q\nhunks=%+v", iter, ctx, a, b, got, h)
			}
			// format + parse round trip
			fd := &FileDiff{OldPath: "f", NewPath: "f", Hunks: h}
			parsed, err := ParseString(Format([]*FileDiff{fd}))
			if err != nil {
				t.Fatal(err)
			}
			if len(h) == 0 {
				continue
			}
			if len(parsed) != 1 || !reflect.DeepEqual(parsed[0].Hunks, h) {
				t.Fatalf("iter %d: format/parse mismatch\n%s\nwant %+v\ngot  %+v", iter, Format([]*FileDiff{fd}), h, parsed[0].Hunks)
			}
		}
	}
}

func norm(s []string) string { return strings.Join(s, "\n") }

func TestComputeAddDelete(t *testing.T) {
	f := Compute("", "x.go", nil, []byte("a\nb\n"), false, true, 3)
	if f.Kind != Added || len(f.Added()) != 2 || f.Hunks[0].OldStart != 0 {
		t.Errorf("added: %+v", f)
	}
	f = Compute("x.go", "", []byte("a\nb\n"), nil, true, false, 3)
	if f.Kind != Deleted || len(f.Removed()) != 2 {
		t.Errorf("deleted: %+v", f)
	}
}
