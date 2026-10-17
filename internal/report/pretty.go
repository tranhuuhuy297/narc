package report

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
)

// Layout of the pretty report: file headers sit at indent 2, findings at
// 4, and a finding's message, code and hint at 6.
const (
	indentFile    = "  "
	indentFinding = "    "
	indentBody    = "      "

	// maxCodeLines caps how much multi-line evidence is shown per side.
	maxCodeLines = 6
	// minTitleWidth is the narrowest a finding title is cut to before its
	// location is dropped from the headline instead.
	minTitleWidth = 12
)

// Pretty writes the human-readable terminal report: a verdict banner with
// the integrity score, findings grouped by file with the offending code
// (and the code it replaced), fix hints, waived findings when asked for,
// and a summary footer.
func Pretty(w io.Writer, res *engine.Result, o Options) error {
	if res == nil {
		return errNilResult
	}
	t := newTerm(o)
	v := newView(res)
	t.banner(v)
	if len(v.active) == 0 {
		t.cleanNote(v)
	}
	for _, g := range groupByFile(v.active) {
		t.fileGroup(g)
	}
	if o.ShowWaived {
		t.waivedList(v.waived)
	}
	t.internalErrors(res.Errors)
	t.footer(v)
	return t.flush(w)
}

// banner writes the verdict badge, the integrity score and its meter, and
// the scanned range underneath.
func (t *term) banner(v *view) {
	vd := v.verdict
	t.blank()
	var b strings.Builder
	b.WriteString(indentFile)
	if e := t.g.verdict(vd); e != "" {
		b.WriteString(e + " ")
	}
	labelCol := textWidth(b.String())
	if t.p {
		b.WriteString(t.p.paint(badgeStyle(vd), " "+vd.label+" "))
		labelCol++ // align with the badge text, not its padding
	} else {
		b.WriteString(vd.label)
	}
	b.WriteString("  integrity ")
	b.WriteString(t.p.paint(bold(verdictStyle(vd)), strconv.Itoa(v.score)))
	b.WriteString(t.p.paint(sDim, "/100"))
	if meter := "  " + t.meter(v.score, verdictStyle(vd)); textWidth(b.String())+textWidth(meter) <= t.width {
		b.WriteString(meter)
	}
	t.line(b.String())
	if label := v.res.Label; label != "" {
		t.line(strings.Repeat(" ", labelCol), t.p.paint(sDim, t.fit(label, t.width-labelCol)))
	}
	t.blank()
}

// meter draws the score as ten cells.
func (t *term) meter(score int, st style) string {
	on := min(max((score+5)/10, 0), 10)
	m := t.p.paint(st, strings.Repeat(t.g.meterOn, on)) + t.p.paint(sDim, strings.Repeat(t.g.meterOff, 10-on))
	if t.o.ASCII {
		m = "[" + m + "]"
	}
	return m
}

// cleanNote replaces the findings when there are none to show.
func (t *term) cleanNote(v *view) {
	msg := "No skipped tests, weakened assertions or CI escapes in this change."
	switch {
	case len(v.waived) > 0:
		// "No skipped tests" would be false: a waived finding may well be
		// a skipped test, accepted with a reason.
		msg = fmt.Sprintf("Nothing in this change weakens verification, apart from %s accepted with a reason.",
			plural(len(v.waived), "waived finding"))
	case v.res.Files == 0 && v.res.Skipped > 0:
		msg = fmt.Sprintf("Nothing to analyze: all %s were skipped (binary, vendored, generated or ignored).",
			plural(v.res.Skipped, "changed file"))
	case v.res.Files == 0:
		msg = "Nothing to analyze: there are no changed files."
	}
	t.para(indentFile, indentFile, msg, sNone)
	t.blank()
}

func (t *term) fileGroup(g fileGroup) {
	t.line(indentFile, t.pathHeader(g.path))
	for i, f := range g.findings {
		if i > 0 {
			t.blank()
		}
		t.finding(f)
	}
	t.blank()
}

// pathHeader shows the directory dimmed and the file name in bold, so the
// eye lands on the name.
func (t *term) pathHeader(p string) string {
	p = truncateLeft(sanitize(p, false), t.width-len(indentFile), t.g.ellipsis)
	if p == "" {
		p = "(unknown file)"
	}
	i := strings.LastIndexByte(p, '/') + 1
	return t.p.paint(sDim, p[:i]) + t.p.paint(sBold, p[i:])
}

// finding writes one finding:
//
//	✖ skip-added  Test skipped · line 42
//	  test "rejects expired tokens" was skipped with `it.skip`
//	  42 │ it.skip('rejects expired tokens', async () => {
//	  ↳ Fix the code under test instead of skipping the test.
func (t *term) finding(f core.Finding) {
	sev := sevStyle(f.Severity)
	// The rule ID is what a waiver names, so it is never cut (unless it
	// is wider than the terminal); the title gives way instead.
	marker := t.fit(t.g.sev(f.Severity)+" "+f.RuleID, t.width-len(indentFinding))
	loc := " " + t.g.dot + " " + where(f)
	title := f.Title
	if title == "" {
		title = f.RuleID
	}
	avail := t.width - len(indentFinding) - textWidth(marker) - 2
	// ownLine moves the location under the headline when it does not fit
	// there and the code frame does not show it either.
	ownLine := false
	if textWidth(title)+textWidth(loc) > avail {
		// Narrow terminal: drop the location when the code frame shows
		// the line number anyway, or when keeping it would leave the
		// title unreadable. Only evidence is numbered with the new-side
		// line; removed code alone shows its old line number.
		framed := f.Line > 0 && f.Evidence != ""
		if framed || avail-textWidth(loc) < minTitleWidth {
			loc, ownLine = "", !framed
		}
	}
	head := []string{indentFinding, t.p.paint(sev, marker)}
	if textWidth(title) <= avail || avail >= minTitleWidth {
		head = append(head, "  ", t.p.paint(sBold, t.fit(title, avail-textWidth(loc))), t.p.paint(sDim, loc))
	} // else a long rule ID leaves too little room: the ID alone names the rule
	t.line(head...)
	if ownLine {
		t.line(indentBody, t.p.paint(sDim, where(f)))
	}

	msg := f.Message
	if msg == "" {
		msg = title
	}
	t.para(indentBody, indentBody, msg, sNone)
	t.codeFrame(f)
	if t.o.ShowHints && strings.TrimSpace(f.Hint) != "" && !t.hinted[f.Hint] {
		if t.hinted == nil {
			t.hinted = map[string]bool{}
		}
		t.hinted[f.Hint] = true
		lead := t.g.hint + " "
		t.para(indentBody+t.p.paint(sDim, lead), indentBody+strings.Repeat(" ", textWidth(lead)), f.Hint, sDim)
	}
}

// codeRow is one line of a finding's code frame.
type codeRow struct {
	no   int    // line number, 0 for none
	mark string // "-", "+" or ""
	text string
	st   style
}

// codeFrame shows the offending code with line numbers. When the finding
// says what the code replaced or removed (Before), that comes first in red
// with a "-" marker and the new code follows with "+", like a diff.
func (t *term) codeFrame(f core.Finding) {
	var rows []codeRow
	if f.Before != "" {
		for i, l := range codeLines(f.Before, t.g.ellipsis) {
			rows = append(rows, codeRow{no: firstOnly(f.OldLine, i), mark: "-", text: l, st: sRed})
		}
	}
	if f.Evidence != "" {
		mark, st := "", sNone
		if f.Before != "" {
			mark, st = "+", sGreen
		}
		for i, l := range codeLines(f.Evidence, t.g.ellipsis) {
			rows = append(rows, codeRow{no: firstOnly(f.Line, i), mark: mark, text: l, st: st})
		}
	}
	numW := 1
	for _, r := range rows {
		numW = max(numW, len(strconv.Itoa(r.no)))
	}
	for _, r := range rows {
		num := ""
		if r.no > 0 {
			num = strconv.Itoa(r.no)
		}
		gutter := padLeft(num, numW) + " " + t.g.gutter + " "
		text := r.text
		if r.mark != "" {
			text = r.mark + " " + text
		}
		avail := t.width - len(indentBody) - textWidth(gutter)
		t.line(indentBody, t.p.paint(sDim, gutter), t.p.paint(r.st, t.fit(text, avail)))
	}
}

// codeLines splits evidence into display lines, capped at maxCodeLines.
func codeLines(s, ellipsis string) []string {
	return capLines(codeSplit(s), maxCodeLines, ellipsis)
}

// firstOnly numbers only the first line of multi-line evidence: rules do
// not promise that evidence lines are consecutive.
func firstOnly(no, i int) int {
	if i == 0 {
		return no
	}
	return 0
}

func (t *term) waivedList(fs []core.Finding) {
	if len(fs) == 0 {
		return
	}
	t.line(indentFile, t.p.paint(bold(sDim), "Waived"), t.p.paint(sDim, fmt.Sprintf(" (%d)", len(fs))))
	for _, f := range fs {
		avail := t.width - len(indentFinding)
		head := t.fit(t.g.waived+" "+f.RuleID, avail)
		loc := sanitize(f.Location(), false)
		if room := avail - textWidth(head) - 2; room >= min(textWidth(loc), minTitleWidth) {
			t.line(indentFinding, t.p.paint(sDim, head+"  "+truncateLeft(loc, room, t.g.ellipsis)))
		} else {
			// A long rule ID leaves too little room: the location gets a
			// line of its own rather than being cut to nothing.
			t.line(indentFinding, t.p.paint(sDim, head))
			t.line(indentBody, t.p.paint(sDim, truncateLeft(loc, t.width-len(indentBody), t.g.ellipsis)))
		}
		reason := strings.TrimSpace(f.WaiverReason)
		if reason == "" {
			reason = "no reason recorded"
		}
		t.para(indentBody, indentBody+" ", t.g.lquote+reason+t.g.rquote, sDim)
	}
	t.blank()
}

// internalErrors warns that some rules crashed, so a clean verdict is not
// the whole truth. Only the first line of each error is shown; JSON and
// SARIF carry the full text.
func (t *term) internalErrors(errs []string) {
	if len(errs) == 0 {
		return
	}
	lead := t.g.warn + " "
	msg := plural(len(errs), "rule") + " failed to run, so this report may be incomplete:"
	t.para(indentFile+t.p.paint(sYellow, lead), indentFile+strings.Repeat(" ", textWidth(lead)), msg, sYellow)
	for _, e := range errs {
		t.line(indentFinding, t.p.paint(sDim, t.fit(firstLine(e), t.width-len(indentFinding))))
	}
	t.blank()
}

// footer writes a rule and one compact summary line (wrapped between items
// on narrow terminals): counts by severity, waivers, files, lines and time.
func (t *term) footer(v *view) {
	t.line(indentFile, t.p.paint(sDim, strings.Repeat(t.g.rule, t.width-2*len(indentFile))))
	var items []string
	for _, c := range []struct {
		n   int
		sev core.Severity
	}{{v.errors, core.Error}, {v.warns, core.Warning}, {v.infos, core.Info}} {
		if c.n > 0 {
			items = append(items, t.p.paint(bold(sevStyle(c.sev)), plural(c.n, sevWord(c.sev))))
		}
	}
	if len(items) == 0 {
		items = append(items, t.p.paint(bold(sGreen), "no findings"))
	}
	if n := len(v.waived); n > 0 {
		s := fmt.Sprintf("%d waived", n)
		if !t.o.ShowWaived {
			s += " (--show-waived)"
		}
		items = append(items, t.p.paint(sDim, s))
	}
	files := plural(v.res.Files, "file")
	if v.res.Skipped > 0 {
		files += fmt.Sprintf(" (%d skipped)", v.res.Skipped)
	}
	items = append(items,
		t.p.paint(sDim, files),
		t.p.paint(sDim, fmt.Sprintf("+%d %s%d lines", v.res.Added, t.g.minus, v.res.Removed)))
	if !t.o.HideDuration {
		items = append(items, t.p.paint(sDim, formatDuration(v.res.Duration)))
	}
	t.flow(indentFile, items, t.p.paint(sDim, " "+t.g.dot+" "))
	t.blank()
}
