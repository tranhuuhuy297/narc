package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
)

// GitHub writes GitHub Actions workflow commands that annotate the diff:
//
//	::error file=src/a.test.ts,line=42,title=narc%3A Test skipped (skip-added)::message
//
// errors become ::error, warnings ::warning and notes ::notice; file-level
// findings omit line. Waived findings are annotated as notices only with
// Options.ShowWaived. A plain summary line follows the annotations.
func GitHub(w io.Writer, res *engine.Result, o Options) error {
	if res == nil {
		return errNilResult
	}
	v := newView(res)
	var b strings.Builder
	for _, f := range v.active {
		ghAnnotation(&b, ghCommand(f.Severity), f, ghTitle(f), ghMessage(f, o.ShowHints))
	}
	if o.ShowWaived {
		for _, f := range v.waived {
			msg := ghMessage(f, false) + "\nWaived: " + firstNonEmpty(f.WaiverReason, "no reason recorded")
			ghAnnotation(&b, "notice", f, "narc: waived "+f.RuleID, msg)
		}
	}
	summary := fmt.Sprintf("narc: %s, integrity %d/100: %s", v.verdict.label, v.score, countPhrase(v.errors, v.warns, v.infos))
	if n := len(v.waived); n > 0 {
		summary += fmt.Sprintf(", %d waived", n)
	}
	summary += fmt.Sprintf(" in %s", plural(res.Files, "file"))
	if len(res.Errors) > 0 {
		summary += fmt.Sprintf(" (%s failed to run)", plural(len(res.Errors), "rule"))
	}
	notices := v.infos
	if o.ShowWaived {
		notices += len(v.waived) // waived findings are annotated as notices too
	}
	if max(v.errors, v.warns, notices) > ghAnnotationLimit {
		summary += fmt.Sprintf(". GitHub shows at most %d annotations of each level per step; see --format markdown for the full list", ghAnnotationLimit)
	}
	// The summary is plain log text; keep it to one line that cannot be
	// mistaken for a workflow command.
	b.WriteString(sanitize(summary, false) + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// ghAnnotationLimit is how many annotations of each level GitHub displays
// per step; the rest are dropped silently.
const ghAnnotationLimit = 10

func ghAnnotation(b *strings.Builder, cmd string, f core.Finding, title, msg string) {
	props := []string{"file=" + ghProperty(f.Path)}
	if f.Line > 0 {
		props = append(props, fmt.Sprintf("line=%d", f.Line))
	}
	props = append(props, "title="+ghProperty(sanitize(title, false)))
	// Escape sequences in source code would otherwise color the job log.
	fmt.Fprintf(b, "::%s %s::%s\n", cmd, strings.Join(props, ","), ghData(sanitize(msg, true)))
}

func ghCommand(s core.Severity) string {
	switch s {
	case core.Error:
		return "error"
	case core.Warning:
		return "warning"
	}
	return "notice"
}

func ghTitle(f core.Finding) string {
	return fmt.Sprintf("narc: %s (%s)", firstNonEmpty(f.Title, f.RuleID), f.RuleID)
}

// ghMessage is the annotation body. The annotation sits on the new code,
// so the evidence is already visible; what is not visible is the code a
// change replaced or removed, so that is spelled out.
func ghMessage(f core.Finding, hints bool) string {
	msg := firstNonEmpty(f.Message, f.Title, f.RuleID)
	if f.Before != "" {
		label := "Removed"
		if f.Evidence != "" {
			label = "Before"
		}
		msg += "\n" + label + ": " + oneLine(f.Before)
	}
	if hints && strings.TrimSpace(f.Hint) != "" {
		msg += "\nHint: " + f.Hint
	}
	return msg
}

// ghData escapes a workflow command's message, per the runner's
// escapeData: %, CR and LF.
func ghData(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return strings.ReplaceAll(s, "\n", "%0A")
}

// ghProperty escapes a workflow command property value, per the runner's
// escapeProperty: ghData plus the delimiters ':' and ','.
func ghProperty(s string) string {
	s = ghData(s)
	s = strings.ReplaceAll(s, ":", "%3A")
	return strings.ReplaceAll(s, ",", "%2C")
}
