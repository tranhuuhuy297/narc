package hook

import (
	"strings"
	"testing"
)

func TestApplyChunks(t *testing.T) {
	t.Parallel()
	const file = "def greet(name):\n    return f\"hi {name}\"\n\n\ndef add(a, b):\n    return a + b\n\n\ndef sub(a, b):\n    return a - b\n"
	tests := []struct {
		name  string
		patch string
		want  string
		fail  bool
	}{
		{
			name:  "first chunk without @@",
			patch: " def add(a, b):\n-    return a + b\n+    return b + a\n",
			want:  strings.Replace(file, "return a + b", "return b + a", 1),
		},
		{
			name:  "anchor selects the right function",
			patch: "@@ def sub(a, b):\n-    return a - b\n+    return b - a\n",
			want:  strings.Replace(file, "return a - b", "return b - a", 1),
		},
		{
			name: "two chunks in order",
			patch: "@@ def greet(name):\n-    return f\"hi {name}\"\n+    return f\"hello {name}\"\n" +
				"@@ def sub(a, b):\n     return a - b\n+\n+\n+def mul(a, b):\n+    return a * b\n",
			want: strings.Replace(file, "hi {name}", "hello {name}", 1) + "\n\ndef mul(a, b):\n    return a * b\n",
		},
		{
			name:  "end of file anchors at the last lines",
			patch: "@@\n-    return a - b\n+    return a - b  # checked\n*** End of File\n",
			want:  strings.Replace(file, "return a - b\n", "return a - b  # checked\n", 1),
		},
		{
			name:  "pure addition goes at the end",
			patch: "@@\n+\n+\n+def neg(a):\n+    return -a\n",
			want:  file + "\n\ndef neg(a):\n    return -a\n",
		},
		{
			name:  "trailing whitespace and typographic quotes still match",
			patch: "@@\n-    return f“hi {name}”   \n+    return f\"hey {name}\"\n",
			want:  strings.Replace(file, "hi {name}", "hey {name}", 1),
		},
		{
			name:  "context not found",
			patch: "@@ def mul(a, b):\n-    return a * b\n+    return 0\n",
			fail:  true,
		},
		{
			name:  "removed lines not found",
			patch: "@@\n-    return a * b\n+    return 0\n",
			fail:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			chunks, next, err := parseChunks(strings.Split(strings.TrimSuffix(tc.patch, "\n"), "\n"), 0)
			if err != nil {
				t.Fatal(err)
			}
			if next != len(strings.Split(strings.TrimSuffix(tc.patch, "\n"), "\n")) {
				t.Fatalf("parseChunks stopped at line %d", next)
			}
			got, err := applyChunks(file, chunks)
			if tc.fail {
				if err == nil {
					t.Fatalf("want an error, got:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// Several "@@" lines narrow down step by step, so the method of the second
// class is changed, not the first method with that name.
func TestApplyChunksStackedAnchors(t *testing.T) {
	t.Parallel()
	const file = "class A:\n    def run(self):\n        return 1\n\n\nclass B:\n    def run(self):\n        return 1\n"
	lines := strings.Split("@@ class B:\n@@     def run(self):\n-        return 1\n+        return 2", "\n")
	chunks, _, err := parseChunks(lines, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || strings.Join(chunks[0].anchors, "|") != "class B:|def run(self):" {
		t.Fatalf("chunks: %+v", chunks)
	}
	got, err := applyChunks(file, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(file, "class B:\n    def run(self):\n        return 1", "class B:\n    def run(self):\n        return 2", 1); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePatch(t *testing.T) {
	t.Parallel()
	patch := "cat <<'EOF' | apply_patch\n" + // shell wrappers are ignored
		"*** Begin Patch\n" +
		"*** Add File: docs/notes.md\n" +
		"+# Notes\n" +
		"+\n" +
		"+ *** not a header\n" +
		"*** Delete File: old/legacy.py\n" +
		"*** Update File: src/app.py\n" +
		"*** Move to: src/main.py\n" +
		"@@ class App:\n" +
		" *** context that looks like a marker\n" +
		"-    debug = True\n" +
		"+    debug = False\n" +
		"\n" +
		"*** Update File: src/rename_only.py\n" +
		"*** Move to: src/renamed.py\n" +
		"*** End Patch\n" +
		"EOF\n"
	files, err := parsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	if f := files[0]; f.kind != patchAddFile || f.path != "docs/notes.md" || strings.Join(f.added, "|") != "# Notes|| *** not a header" {
		t.Errorf("add: %+v", f)
	}
	if f := files[1]; f.kind != patchDeleteFile || f.path != "old/legacy.py" {
		t.Errorf("delete: %+v", f)
	}
	f := files[2]
	if f.kind != patchUpdateFile || f.path != "src/app.py" || f.moveTo != "src/main.py" || len(f.chunks) != 1 {
		t.Fatalf("update: %+v", f)
	}
	c := f.chunks[0]
	if strings.Join(c.anchors, "|") != "class App:" || strings.Join(c.old, "|") != "*** context that looks like a marker|    debug = True|" ||
		strings.Join(c.new, "|") != "*** context that looks like a marker|    debug = False|" {
		t.Errorf("chunk: %+v", c)
	}
	if f := files[3]; f.moveTo != "src/renamed.py" || len(f.chunks) != 0 {
		t.Errorf("rename: %+v", f)
	}

	for name, bad := range map[string]string{
		"no begin":        "*** Update File: a.py\n@@\n-x\n+y\n*** End Patch\n",
		"no end":          "*** Begin Patch\n*** Add File: a.py\n+x\n",
		"bad line":        "*** Begin Patch\n*** Update File: a.py\n@@\n?x\n*** End Patch\n",
		"unknown section": "*** Begin Patch\n*** Rename File: a.py\n*** End Patch\n",
		"empty update":    "*** Begin Patch\n*** Update File: a.py\n*** End Patch\n",
	} {
		if _, err := parsePatch(bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestApplyReplacements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		exists  bool
		reps    []replacement
		want    string
		ok      bool
	}{
		{"first occurrence only", "a a a", true, []replacement{{old: "a", new: "b"}}, "b a a", true},
		{"all occurrences", "a a a", true, []replacement{{old: "a", new: "b", all: true}}, "b b b", true},
		{"sequential edits see earlier ones", "x = 1", true, []replacement{{old: "1", new: "2"}, {old: "x = 2", new: "y = 2"}}, "y = 2", true},
		{"missing text fails", "x = 1", true, []replacement{{old: "z", new: "q"}}, "", false},
		{"later edit fails, whole edit fails", "x = 1", true, []replacement{{old: "1", new: "2"}, {old: "1", new: "3"}}, "", false},
		{"empty old string creates a file", "", false, []replacement{{old: "", new: "new\n"}}, "new\n", true},
		{"empty old string on a non-empty file fails", "x", true, []replacement{{old: "", new: "y"}}, "", false},
		{"edit of a missing file fails", "", false, []replacement{{old: "x", new: "y"}}, "", false},
		{"LF edit on a CRLF file", "a\r\nb\r\n", true, []replacement{{old: "a\nb", new: "a\nc"}}, "a\r\nc\r\n", true},
	}
	for _, tc := range tests {
		got, ok := applyReplacements(tc.content, tc.exists, tc.reps)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
