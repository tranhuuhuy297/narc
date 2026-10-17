package hook

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The parsers below read input written by agents and by users; none of
// them may panic, whatever the input. The seeds run as ordinary tests.

func FuzzApplyPatch(f *testing.F) {
	f.Add("*** Begin Patch\n*** Update File: a.py\n@@ def f():\n-    return 1\n+    return 2\n*** End of File\n*** End Patch\n", "def f():\n    return 1\n")
	f.Add("apply_patch *** Begin Patch\n*** Add File: b.py\n+x = 1\n*** Delete File: c.py\n*** End Patch", "")
	f.Add("*** Begin Patch\n*** Update File: a\n*** Move to: b\n@@\n a\n-b\n+c\n@@\n-d\n*** End of File\n*** End Patch\n", "a\nb\nc\nd\n")
	f.Fuzz(func(t *testing.T, patch, content string) {
		ops, err := patchOps(patch)
		if err != nil {
			return
		}
		for _, op := range ops {
			op.apply([]byte(content), true)
			op.apply(nil, false)
		}
	})
}

func FuzzToolOps(f *testing.F) {
	f.Add("Edit", `{"file_path":"/r/a.js","old_string":"a","new_string":"b","replace_all":true}`, "a\r\nb\n")
	f.Add("MultiEdit", `{"file_path":"a.js","edits":[{"old_string":"","new_string":"x"},{"old_string":"x","new_string":"y"}]}`, "")
	f.Add("edit", `{"filePath":"a.js","oldString":"","newString":"z"}`, "q")
	f.Add("shell", `{"command":["bash","-lc","cd x && apply_patch <<'EOF'\n*** Begin Patch\n*** Add File: y\n+1\n*** End Patch\nEOF"]}`, "")
	f.Add("apply_patch", `"*** Begin Patch\n*** End Patch"`, "")
	f.Fuzz(func(t *testing.T, tool, input, content string) {
		ops, err := toolOps(tool, json.RawMessage(input))
		if err != nil {
			return
		}
		for _, op := range ops {
			op.apply([]byte(content), content != "")
		}
	})
}

func FuzzSettingsJSON(f *testing.F) {
	f.Add([]byte(`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "narc hook claude stop"}]}]}, "model": "opus"}`))
	f.Add([]byte("// comment\n{\"a\": /* x */ 1, \"s\": \"// not a comment\"}"))
	f.Add([]byte(`{"a": 1, "a": 2, "n": 1e400}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		stripped, _ := stripJSONComments(b)
		doc, err := parseObject(stripped)
		if err != nil {
			return
		}
		out := encodeDocument(doc)
		again, err := parseObject(out)
		if err != nil {
			t.Fatalf("re-encoded settings do not parse: %v\n%s", err, out)
		}
		if !bytes.Equal(encodeDocument(again), out) {
			t.Fatalf("encoding is not stable:\n%s", out)
		}
	})
}

func FuzzReflogAction(f *testing.F) {
	f.Add("pull --rebase origin main (pick): fix (the) parser")
	f.Add("commit (amend): x: y")
	f.Fuzz(func(t *testing.T, subject string) {
		reflogAction(subject)
	})
}
