package report

import (
	"io"
	"strings"
)

// term accumulates terminal output for one render (Pretty, RulesTable,
// Explain) and knows the width, glyphs and colors to use.
type term struct {
	b         strings.Builder
	o         Options
	p         painter
	g         glyphs
	width     int
	lastBlank bool
	// hinted records hints already shown, so repeated findings of one rule
	// don't repeat the same advice.
	hinted map[string]bool
}

func newTerm(o Options) *term {
	g := unicodeGlyphs
	if o.ASCII {
		g = asciiGlyphs
	}
	return &term{o: o, p: painter(o.Color), g: g, width: o.width()}
}

// line writes the concatenation of parts as one line.
func (t *term) line(parts ...string) {
	for _, s := range parts {
		t.b.WriteString(s)
	}
	t.b.WriteByte('\n')
	t.lastBlank = false
}

// blank writes an empty line, never two in a row.
func (t *term) blank() {
	if !t.lastBlank {
		t.b.WriteByte('\n')
		t.lastBlank = true
	}
}

// para writes text word-wrapped to the terminal width. first prefixes the
// first line and rest the continuation lines (both may be styled already);
// the text is painted with st, with `code spans` highlighted.
func (t *term) para(first, rest, text string, st style) {
	text = sanitize(text, true)
	avail := t.width - max(textWidth(first), textWidth(rest))
	if avail < 12 {
		avail = 12
	}
	var inCode *bool
	if balancedBackticks(text) {
		inCode = new(bool)
	}
	for i, l := range wrap(text, avail) {
		prefix := rest
		if i == 0 {
			prefix = first
		}
		t.line(prefix, t.p.code(l, st, inCode))
	}
}

// flow writes items joined by sep, breaking lines between items (never
// inside one) to fit the width. Items and sep may be styled.
func (t *term) flow(indent string, items []string, sep string) {
	var cur strings.Builder
	curW, sepW, indW := 0, textWidth(sep), textWidth(indent)
	for _, it := range items {
		w := textWidth(it)
		if curW > 0 && indW+curW+sepW+w > t.width {
			t.line(indent, cur.String())
			cur.Reset()
			curW = 0
		}
		if curW > 0 {
			cur.WriteString(sep)
			curW += sepW
		}
		cur.WriteString(it)
		curW += w
	}
	if curW > 0 {
		t.line(indent, cur.String())
	}
}

// fit sanitizes untrusted single-line text and truncates it to avail
// columns.
func (t *term) fit(s string, avail int) string {
	return truncate(sanitize(s, false), max(avail, 1), t.g.ellipsis)
}

func (t *term) flush(w io.Writer) error {
	_, err := io.WriteString(w, t.b.String())
	return err
}
