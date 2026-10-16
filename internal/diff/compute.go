package diff

import "strings"

// DefaultContext is the number of context lines around each change.
const DefaultContext = 3

// maxEditDistance bounds the Myers search. Beyond it we fall back to a
// whole-file replacement, which is still a correct (if less minimal) diff.
const maxEditDistance = 4000

// SplitLines splits content into lines without their terminators. A trailing
// newline does not produce an extra empty line.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// Compute returns the diff between two versions of a file. oldPath or
// newPath may be empty to signal an added or deleted file. hasOld/hasNew
// report whether each side exists.
func Compute(oldPath, newPath string, old, new []byte, hasOld, hasNew bool, context int) *FileDiff {
	f := &FileDiff{OldPath: oldPath, NewPath: newPath}
	switch {
	case !hasOld && hasNew:
		f.Kind, f.OldPath = Added, ""
	case hasOld && !hasNew:
		f.Kind, f.NewPath = Deleted, ""
	case oldPath != newPath:
		f.Kind = Renamed
	}
	if isBinary(old) || isBinary(new) {
		f.Binary = true
		return f
	}
	a := SplitLines(string(old))
	b := SplitLines(string(new))
	f.Hunks = Hunks(a, b, context)
	return f
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

type edit struct {
	op   Op
	a, b int // indices into old/new (0-based); -1 when not applicable
}

// Hunks computes unified-diff hunks between line slices a and b.
func Hunks(a, b []string, context int) []Hunk {
	script := editScript(a, b)
	if context < 0 {
		context = 0
	}
	// Find indices of changes and group them.
	var hunks []Hunk
	i := 0
	for i < len(script) {
		if script[i].op == Context {
			i++
			continue
		}
		// Start of a change group: back up for context.
		start := i - context
		if start < 0 {
			start = 0
		}
		for start < i && script[start].op != Context {
			start++
		}
		// Extend until we see more than 2*context equal lines in a row.
		end := i
		for end < len(script) {
			if script[end].op != Context {
				end++
				continue
			}
			run := 0
			j := end
			for j < len(script) && script[j].op == Context {
				run++
				j++
			}
			if j == len(script) || run > 2*context {
				end += min(run, context)
				break
			}
			end = j
		}
		hunks = append(hunks, buildHunk(script[start:end], a, b))
		i = end
	}
	return hunks
}

func buildHunk(ops []edit, a, b []string) Hunk {
	var h Hunk
	h.OldStart, h.NewStart = -1, -1
	for _, e := range ops {
		switch e.op {
		case Context:
			if h.OldStart < 0 {
				h.OldStart = e.a + 1
			}
			if h.NewStart < 0 {
				h.NewStart = e.b + 1
			}
			h.Lines = append(h.Lines, Line{Op: Context, Text: a[e.a], OldNo: e.a + 1, NewNo: e.b + 1})
			h.OldLines++
			h.NewLines++
		case Del:
			if h.OldStart < 0 {
				h.OldStart = e.a + 1
			}
			h.Lines = append(h.Lines, Line{Op: Del, Text: a[e.a], OldNo: e.a + 1})
			h.OldLines++
		case Add:
			if h.NewStart < 0 {
				h.NewStart = e.b + 1
			}
			h.Lines = append(h.Lines, Line{Op: Add, Text: b[e.b], NewNo: e.b + 1})
			h.NewLines++
		}
	}
	// Unified diff convention: a zero-length range starts at the line before.
	if h.OldStart < 0 || h.OldLines == 0 {
		h.OldStart = posBefore(ops, true)
	}
	if h.NewStart < 0 || h.NewLines == 0 {
		h.NewStart = posBefore(ops, false)
	}
	return h
}

// posBefore returns the 1-based line number preceding the hunk on one side,
// which is how unified diffs encode empty ranges.
func posBefore(ops []edit, old bool) int {
	for _, e := range ops {
		if old && e.op == Add {
			return e.a
		}
		if !old && e.op == Del {
			return e.b
		}
	}
	return 0
}

// editScript computes a minimal edit script with Myers' O(ND) algorithm,
// after trimming the common prefix and suffix. For Add ops, .a holds the
// number of old lines consumed so far (used for empty-range positions), and
// symmetrically .b for Del ops.
func editScript(a, b []string) []edit {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []edit
	for i := 0; i < pre; i++ {
		out = append(out, edit{Context, i, i})
	}
	mid := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	for _, e := range mid {
		e.a += pre
		e.b += pre
		out = append(out, e)
	}
	for i := 0; i < suf; i++ {
		ai, bi := len(a)-suf+i, len(b)-suf+i
		out = append(out, edit{Context, ai, bi})
	}
	return out
}

func myers(a, b []string) []edit {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	replaceAll := func() []edit {
		var out []edit
		for i := 0; i < n; i++ {
			out = append(out, edit{Del, i, 0})
		}
		for j := 0; j < m; j++ {
			out = append(out, edit{Add, n, j})
		}
		return out
	}
	if n == 0 || m == 0 {
		return replaceAll()
	}
	maxD := n + m
	if maxD > maxEditDistance {
		maxD = maxEditDistance
	}
	offset := maxD + 1
	v := make([]int, 2*maxD+3)
	var trace [][]int
	found := false
	for d := 0; d <= maxD && !found; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	if !found {
		return replaceAll()
	}
	// Backtrack.
	var rev []edit
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		vv := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vv[offset+k-1] < vv[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vv[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, edit{Context, x, y})
		}
		if d > 0 {
			if x == prevX {
				y--
				rev = append(rev, edit{Add, x, y})
			} else {
				x--
				rev = append(rev, edit{Del, x, y})
			}
		}
		x, y = prevX, prevY
	}
	out := make([]edit, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return normalize(out)
}

// normalize reorders each run of changes so deletions precede additions,
// matching git's presentation.
func normalize(es []edit) []edit {
	out := make([]edit, 0, len(es))
	i := 0
	for i < len(es) {
		if es[i].op == Context {
			out = append(out, es[i])
			i++
			continue
		}
		j := i
		for j < len(es) && es[j].op != Context {
			j++
		}
		for _, e := range es[i:j] {
			if e.op == Del {
				out = append(out, e)
			}
		}
		for _, e := range es[i:j] {
			if e.op == Add {
				out = append(out, e)
			}
		}
		i = j
	}
	return out
}
