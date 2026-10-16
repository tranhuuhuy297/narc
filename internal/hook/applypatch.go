package hook

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The apply_patch format used by Codex (and OpenCode for GPT models):
//
//	*** Begin Patch
//	*** Add File: src/new.py
//	+print("hi")
//	*** Delete File: src/old.py
//	*** Update File: src/app.py
//	*** Move to: src/main.py
//	@@ def greet():
//	-    return "hi"
//	+    return "hello"
//	*** End of File
//	*** End Patch
//
// Paths are relative to the agent's working directory. In an Update File
// section, "@@ <text>" optionally names a line to find first, and the
// chunk's context and "-" lines are then located after it. Several "@@"
// lines in a row narrow down step by step (a class, then a method in it).
const (
	patchBegin  = "*** Begin Patch"
	patchEnd    = "*** End Patch"
	patchAdd    = "*** Add File: "
	patchDelete = "*** Delete File: "
	patchUpdate = "*** Update File: "
	patchMove   = "*** Move to: "
	patchEOF    = "*** End of File"
)

type patchKind int

const (
	patchAddFile patchKind = iota
	patchDeleteFile
	patchUpdateFile
)

type patchFile struct {
	kind   patchKind
	path   string
	moveTo string
	added  []string // Add File: the file's lines
	chunks []patchChunk
}

type patchChunk struct {
	anchors  []string // text after each "@@": lines to locate first, in order
	old, new []string
	eof      bool // the chunk must match at the end of the file
}

// parsePatch parses an apply_patch body. Text before "*** Begin Patch"
// (such as a shell heredoc line) is ignored.
func parsePatch(text string) ([]patchFile, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) != patchBegin {
		i++
	}
	if i == len(lines) {
		return nil, errors.New("apply_patch: missing " + patchBegin)
	}
	i++
	var files []patchFile
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == patchEnd:
			return files, nil
		case t == "":
			i++
		case strings.HasPrefix(t, patchAdd):
			f := patchFile{kind: patchAddFile, path: strings.TrimSpace(t[len(patchAdd):])}
			for i++; i < len(lines) && strings.HasPrefix(lines[i], "+"); i++ {
				f.added = append(f.added, lines[i][1:])
			}
			files = append(files, f)
		case strings.HasPrefix(t, patchDelete):
			files = append(files, patchFile{kind: patchDeleteFile, path: strings.TrimSpace(t[len(patchDelete):])})
			i++
		case strings.HasPrefix(t, patchUpdate):
			f := patchFile{kind: patchUpdateFile, path: strings.TrimSpace(t[len(patchUpdate):])}
			i++
			if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), patchMove) {
				f.moveTo = strings.TrimSpace(strings.TrimSpace(lines[i])[len(patchMove):])
				i++
			}
			var err error
			if f.chunks, i, err = parseChunks(lines, i); err != nil {
				return nil, fmt.Errorf("apply_patch: %s: %w", f.path, err)
			}
			if len(f.chunks) == 0 && f.moveTo == "" {
				return nil, fmt.Errorf("apply_patch: update of %s has no changes", f.path)
			}
			files = append(files, f)
		default:
			return nil, fmt.Errorf("apply_patch: unexpected line %q", lines[i])
		}
	}
	return nil, errors.New("apply_patch: missing " + patchEnd)
}

// parseChunks reads the chunks of an Update File section starting at line
// i and returns the index of the first line after them.
func parseChunks(lines []string, i int) ([]patchChunk, int, error) {
	var chunks []patchChunk
	var cur *patchChunk
	flush := func() {
		if cur != nil && (len(cur.old) > 0 || len(cur.new) > 0) {
			chunks = append(chunks, *cur)
		}
		cur = nil
	}
	for ; i < len(lines); i++ {
		l := lines[i]
		// Markers start at column 0; " *** x" is a context line.
		if strings.TrimRight(l, " \t") == patchEOF {
			if cur != nil {
				cur.eof = true
			}
			continue
		}
		if strings.HasPrefix(l, "***") {
			break // the next file section, or the end of the patch
		}
		if strings.HasPrefix(l, "@@") {
			// "@@" lines right after one another narrow the same chunk.
			if cur == nil || len(cur.old) > 0 || len(cur.new) > 0 {
				flush()
				cur = &patchChunk{}
			}
			if a := strings.TrimSpace(l[2:]); a != "" {
				cur.anchors = append(cur.anchors, a)
			}
			continue
		}
		if cur == nil {
			// The first chunk may omit its "@@" line.
			cur = &patchChunk{}
		}
		switch {
		case l == "":
			// An empty line is an empty context line.
			cur.old = append(cur.old, "")
			cur.new = append(cur.new, "")
		case l[0] == ' ':
			cur.old = append(cur.old, l[1:])
			cur.new = append(cur.new, l[1:])
		case l[0] == '-':
			cur.old = append(cur.old, l[1:])
		case l[0] == '+':
			cur.new = append(cur.new, l[1:])
		default:
			return nil, i, fmt.Errorf("unexpected line in hunk: %q", l)
		}
	}
	flush()
	return chunks, i, nil
}

// patchOps parses a patch into file operations.
func patchOps(text string) ([]fileOp, error) {
	files, err := parsePatch(text)
	if err != nil {
		return nil, err
	}
	ops := make([]fileOp, 0, len(files))
	for _, f := range files {
		f := f
		switch f.kind {
		case patchAddFile:
			content := joinLines(f.added)
			ops = append(ops, fileOp{path: f.path, apply: func([]byte, bool) ([]byte, bool, bool) {
				return []byte(content), true, true
			}})
		case patchDeleteFile:
			ops = append(ops, deleteOp(f.path))
		case patchUpdateFile:
			ops = append(ops, fileOp{path: f.path, newPath: f.moveTo, apply: func(old []byte, exists bool) ([]byte, bool, bool) {
				if !exists {
					return nil, false, false
				}
				out, err := applyChunks(string(old), f.chunks)
				return []byte(out), true, err == nil
			}})
		}
	}
	return ops, nil
}

// applyChunks applies an Update File section the way apply_patch does:
// chunks are located in order, with progressively looser line matching.
func applyChunks(content string, chunks []patchChunk) (string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	type repl struct {
		start, n int
		new      []string
	}
	var repls []repl
	at := 0
	for _, c := range chunks {
		for _, a := range c.anchors {
			p := seekSequence(lines, []string{a}, at, false)
			if p < 0 {
				return "", fmt.Errorf("context %q not found", a)
			}
			at = p + 1
		}
		if len(c.old) == 0 {
			// A pure addition goes at the end of the file.
			repls = append(repls, repl{len(lines), 0, c.new})
			continue
		}
		old, nw := c.old, c.new
		p := seekSequence(lines, old, at, c.eof)
		if p < 0 && old[len(old)-1] == "" {
			// A trailing empty context line may stand for the final newline.
			old = old[:len(old)-1]
			if len(nw) > 0 && nw[len(nw)-1] == "" {
				nw = nw[:len(nw)-1]
			}
			p = seekSequence(lines, old, at, c.eof)
		}
		if p < 0 {
			return "", fmt.Errorf("lines not found: %q", strings.Join(c.old, "\n"))
		}
		repls = append(repls, repl{p, len(old), nw})
		at = p + len(old)
	}
	sort.SliceStable(repls, func(i, j int) bool { return repls[i].start < repls[j].start })
	for k := len(repls) - 1; k >= 0; k-- {
		r := repls[k]
		tail := append([]string{}, lines[r.start+r.n:]...)
		lines = append(append(lines[:r.start], r.new...), tail...)
	}
	return joinLines(lines), nil
}

// joinLines joins lines with a trailing newline ("" for no lines).
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// seekSequence finds pattern in lines at or after start: exact match first,
// then ignoring trailing whitespace, then surrounding whitespace, then
// typographic punctuation. With eof it first tries the end of the file.
func seekSequence(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return start
	}
	if len(pattern) > len(lines) {
		return -1
	}
	from := start
	if eof {
		from = len(lines) - len(pattern)
	}
	for _, norm := range []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRight(s, " \t\r") },
		strings.TrimSpace,
		func(s string) string { return punctReplacer.Replace(strings.TrimSpace(s)) },
	} {
		for i := from; i <= len(lines)-len(pattern); i++ {
			match := true
			for j, p := range pattern {
				if norm(lines[i+j]) != norm(p) {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
	}
	return -1
}

// punctReplacer maps typographic dashes, quotes and spaces to ASCII.
var punctReplacer = strings.NewReplacer(
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "―", "-", "−", "-",
	"‘", "'", "’", "'", "‚", "'", "‛", "'",
	"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
	" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", "　", " ",
)
