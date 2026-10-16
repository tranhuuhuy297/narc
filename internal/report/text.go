package report

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sanitize makes untrusted text (paths, code, messages, waiver reasons)
// safe to print on a terminal. Control characters are shown in caret
// notation (ESC becomes ^[, so source code cannot smuggle escape sequences
// into the user's terminal), tabs become two spaces, and bidirectional
// controls ("Trojan Source") are made visible as <U+202E>. Newlines are
// kept only when keepNewlines is set; otherwise they become ^J too.
func sanitize(s string, keepNewlines bool) string {
	if isPrintableASCII(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines:
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("  ")
		case r < 0x20:
			b.WriteByte('^')
			b.WriteByte(byte(r) + '@')
		case r == 0x7f:
			b.WriteString("^?")
		case (r >= 0x80 && r < 0xa0) || isBidiControl(r):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeCode is sanitize for a line of code shown verbatim (a Markdown
// code fence): tabs are kept, since they render fine there.
func sanitizeCode(s string) string {
	if !strings.Contains(s, "\t") {
		return sanitize(s, false)
	}
	parts := strings.Split(s, "\t")
	for i, p := range parts {
		parts[i] = sanitize(p, false)
	}
	return strings.Join(parts, "\t")
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

func isBidiControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// wideRanges are code points that terminals draw two columns wide: East
// Asian wide/fullwidth characters and emoji with default emoji
// presentation. Sorted, inclusive.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0},
	{0x23f3, 0x23f3}, {0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f},
	{0x2693, 0x2693}, {0x26a1, 0x26a1}, {0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5},
	{0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3}, {0x26f5, 0x26f5},
	{0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728},
	{0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf},
	{0xa960, 0xa97f}, {0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f},
	{0xff00, 0xff60}, {0xffe0, 0xffe6}, {0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf}, {0x1f18e, 0x1f18e},
	{0x1f191, 0x1f19a}, {0x1f200, 0x1f251}, {0x1f300, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f7e0, 0x1f7eb},
	{0x1f900, 0x1f9ff}, {0x1fa70, 0x1faff}, {0x20000, 0x3fffd},
}

// runeWidth is the number of terminal columns r occupies.
func runeWidth(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r < 0x300:
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		// combining marks, variation selectors, zero-width joiners
		return 0
	}
	i := sort.Search(len(wideRanges), func(i int) bool { return wideRanges[i][1] >= r })
	if i < len(wideRanges) && wideRanges[i][0] <= r {
		return 2
	}
	return 1
}

// textWidth is the display width of s in columns. ANSI escape sequences
// (which only narc itself emits; see sanitize) take no space.
func textWidth(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w += runeWidth(r)
		i += size
	}
	return w
}

// truncate shortens plain text s to at most width columns, ending it with
// ellipsis when anything was cut.
func truncate(s string, width int, ellipsis string) string {
	if textWidth(s) <= width {
		return s
	}
	limit := width - textWidth(ellipsis)
	if limit < 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > limit {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return strings.TrimRight(b.String(), " ") + ellipsis
}

// clipBytes caps s at n bytes, cutting at a rune boundary and ending it
// with ellipsis when anything was cut. Use it where size, not display
// width, is what matters (Markdown comments have a size limit).
func clipBytes(s string, n int, ellipsis string) string {
	if len(s) <= n {
		return s
	}
	cut := max(n-len(ellipsis), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// truncateLeft is truncate for paths: it keeps the end (the file name).
func truncateLeft(s string, width int, ellipsis string) string {
	if textWidth(s) <= width {
		return s
	}
	limit := width - textWidth(ellipsis)
	if limit < 0 {
		return ""
	}
	rs := []rune(s)
	w, i := 0, len(rs)
	for i > 0 && w+runeWidth(rs[i-1]) <= limit {
		i--
		w += runeWidth(rs[i])
	}
	return ellipsis + string(rs[i:])
}

// wrap breaks plain text into lines of at most width columns at spaces.
// Newlines start new lines; words wider than width are split.
func wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, wrapLine(para, width)...)
	}
	return out
}

func wrapLine(s string, width int) []string {
	var lines []string
	var cur strings.Builder
	curW := 0
	flush := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		curW = 0
	}
	for _, word := range strings.Fields(s) {
		ww := textWidth(word)
		for ww > width {
			if curW > 0 {
				flush()
			}
			head := truncate(word, width, "")
			if head == "" { // a single rune wider than the line
				_, size := utf8.DecodeRuneInString(word)
				head = word[:size]
			}
			lines = append(lines, head)
			word = word[len(head):]
			ww = textWidth(word)
		}
		switch {
		case ww == 0:
		case curW == 0:
			cur.WriteString(word)
			curW = ww
		case curW+1+ww <= width:
			cur.WriteByte(' ')
			cur.WriteString(word)
			curW += 1 + ww
		default:
			flush()
			cur.WriteString(word)
			curW = ww
		}
	}
	if curW > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

// padRight pads s with spaces to width columns.
func padRight(s string, width int) string {
	if w := textWidth(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// padLeft right-aligns s in width columns.
func padLeft(s string, width int) string {
	if w := textWidth(s); w < width {
		return strings.Repeat(" ", width-w) + s
	}
	return s
}

// codeSplit splits evidence or removed code into lines. The CR of a CRLF
// line ending is dropped: it is how the file was saved, not something to
// show (sanitize would print it as ^M). A CR inside a line is kept, so it
// still shows up.
func codeSplit(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// capLines keeps at most n lines, replacing the last one kept with
// ellipsis when lines were dropped.
func capLines(lines []string, n int, ellipsis string) []string {
	if len(lines) > n {
		return append(lines[:n-1:n-1], ellipsis)
	}
	return lines
}

// balancedBackticks reports whether s has an even number of backticks, so
// `code spans` can be highlighted without coloring a stray tick's tail.
func balancedBackticks(s string) bool { return strings.Count(s, "`")%2 == 0 }
