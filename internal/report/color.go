package report

import (
	"os"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
)

// ColorEnabled reports whether output written to f should be colored. It
// follows the common conventions, in order of precedence:
//
//   - NO_COLOR set to anything non-empty disables color (no-color.org);
//   - FORCE_COLOR enables it, or disables it when set to "0"/"false"
//     (the supports-color convention);
//   - CLICOLOR_FORCE set to anything but "0" enables it (bixense.com/clicolors;
//     "0" means "not forced", so detection continues);
//   - CLICOLOR=0 or TERM=dumb disables it;
//   - GitHub Actions renders ANSI colors in job logs, so color stays on
//     there unless f is a regular file;
//   - otherwise color is on only when f is a terminal (a character device).
func ColorEnabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("FORCE_COLOR"))); v != "" {
		return v != "0" && v != "false"
	}
	if v := strings.TrimSpace(os.Getenv("CLICOLOR_FORCE")); v != "" && v != "0" {
		return true
	}
	if os.Getenv("CLICOLOR") == "0" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return !fi.Mode().IsRegular()
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// style is an SGR parameter list, e.g. "1;38;5;203". The palette sticks to
// the 256-color cube, which every modern terminal (and every truecolor
// terminal) renders the same way, and picks tones that stay readable on
// both dark and light backgrounds.
type style string

const (
	sNone   style = ""
	sBold   style = "1"
	sDim    style = "38;5;245"
	sRed    style = "38;5;203"
	sYellow style = "38;5;214"
	sBlue   style = "38;5;68"
	sGreen  style = "38;5;71"
	sCode   style = "38;5;137"

	// Verdict badges: bold text on a solid background.
	sBadgeBusted     style = "1;38;5;231;48;5;160"
	sBadgeSuspicious style = "1;38;5;16;48;5;214"
	sBadgeClean      style = "1;38;5;16;48;5;78"
)

// bold returns st in bold.
func bold(st style) style {
	if st == sNone || st == sBold {
		return sBold
	}
	return "1;" + st
}

func sevStyle(s core.Severity) style {
	switch s {
	case core.Error:
		return sRed
	case core.Warning:
		return sYellow
	}
	return sBlue
}

func badgeStyle(v verdict) style {
	switch v.key {
	case "busted":
		return sBadgeBusted
	case "suspicious":
		return sBadgeSuspicious
	}
	return sBadgeClean
}

func verdictStyle(v verdict) style {
	if v.key == "clean" {
		return sGreen
	}
	return sevStyle(v.sev)
}

// painter applies styles when color is on and is a no-op otherwise, so
// plain output never contains a single escape byte.
type painter bool

func (p painter) paint(st style, s string) string {
	if !p || st == sNone || s == "" {
		return s
	}
	return "\x1b[" + string(st) + "m" + s + "\x1b[0m"
}

// code paints s with base, highlighting `code spans` with sCode. Spans
// may continue across calls (wrapped lines), so the caller threads inCode
// through; it is ignored unless the whole text has balanced backticks.
func (p painter) code(s string, base style, inCode *bool) string {
	if !p || inCode == nil {
		return p.paint(base, s)
	}
	var b strings.Builder
	for s != "" {
		if !*inCode {
			i := strings.IndexByte(s, '`')
			if i < 0 {
				b.WriteString(p.paint(base, s))
				break
			}
			b.WriteString(p.paint(base, s[:i]))
			s, *inCode = s[i:], true
			// The opening tick is part of the span; look for the closing
			// one after it.
			j := strings.IndexByte(s[1:], '`')
			if j < 0 {
				b.WriteString(p.paint(sCode, s))
				break
			}
			b.WriteString(p.paint(sCode, s[:j+2]))
			s, *inCode = s[j+2:], false
			continue
		}
		// Continuing a span from the previous line.
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(p.paint(sCode, s))
			break
		}
		b.WriteString(p.paint(sCode, s[:i+1]))
		s, *inCode = s[i+1:], false
	}
	return b.String()
}

// glyphs are the symbols the terminal renderers draw with.
type glyphs struct {
	busted, suspicious, clean string // verdict emoji
	err, warn, info, waived   string // severity markers
	hint, gutter, rule, dot   string
	meterOn, meterOff         string
	ellipsis, minus           string
	lquote, rquote            string
}

var (
	unicodeGlyphs = glyphs{
		busted: "🚨", suspicious: "🤨", clean: "✅",
		err: "✖", warn: "▲", info: "●", waived: "○",
		hint: "↳", gutter: "│", rule: "─", dot: "·",
		meterOn: "▰", meterOff: "▱",
		ellipsis: "…", minus: "−",
		lquote: "“", rquote: "”",
	}
	asciiGlyphs = glyphs{
		err: "x", warn: "!", info: "i", waived: "o",
		hint: "->", gutter: "|", rule: "-", dot: "-",
		meterOn: "#", meterOff: ".",
		ellipsis: "...", minus: "-",
		lquote: `"`, rquote: `"`,
	}
)

func (g glyphs) sev(s core.Severity) string {
	switch s {
	case core.Error:
		return g.err
	case core.Warning:
		return g.warn
	}
	return g.info
}

func (g glyphs) verdict(v verdict) string {
	switch v.key {
	case "busted":
		return g.busted
	case "suspicious":
		return g.suspicious
	}
	return g.clean
}
