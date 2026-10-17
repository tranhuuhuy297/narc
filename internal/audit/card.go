package audit

import (
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"unicode"
)

// Card renders a 1200×630 SVG share card (the size social networks use for
// link previews).
func Card(w io.Writer, r *Report) error {
	const W, H = 1200, 630
	var b strings.Builder
	esc := xmlEscaper.Replace

	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="ui-monospace, SFMono-Regular, Menlo, Consolas, 'Liberation Mono', monospace">`, W, H, W, H)
	b.WriteString(`
<defs>
  <pattern id="grid" width="24" height="24" patternUnits="userSpaceOnUse"><path d="M24 0H0V24" fill="none" stroke="#1b2330" stroke-width="1"/></pattern>
  <linearGradient id="fade" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#0b0f14" stop-opacity="0"/><stop offset="1" stop-color="#0b0f14"/></linearGradient>
  <pattern id="tape" width="40" height="40" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><rect width="20" height="40" fill="#111"/></pattern>
</defs>
<rect width="1200" height="630" fill="#0b0f14"/>
<rect width="1200" height="630" fill="url(#grid)"/>
<rect y="380" width="1200" height="250" fill="url(#fade)"/>
`)
	// police tape across the top-right corner
	b.WriteString(`<g transform="translate(905 -30) rotate(28)">
  <rect width="420" height="46" fill="#f5c400"/><rect width="420" height="46" fill="url(#tape)" opacity="0.18"/>
  <text x="210" y="31" text-anchor="middle" font-size="20" font-weight="700" fill="#111" letter-spacing="3">NARC · DO NOT CROSS · NARC</text>
</g>
`)
	// header
	b.WriteString(`<rect x="60" y="54" width="232" height="54" rx="6" fill="#d7263d"/>
<text x="176" y="91" text-anchor="middle" font-size="30" font-weight="800" fill="#fff" letter-spacing="4">RAP SHEET</text>
`)
	fmt.Fprintf(&b, `<text x="316" y="80" font-size="28" font-weight="700" fill="#e6edf3">%s</text>
`, esc(truncate(r.Repo, 42)))
	sub := fmt.Sprintf("%d commits · %s → %s", r.Scanned, dateOf(r.From), dateOf(r.To))
	fmt.Fprintf(&b, `<text x="316" y="106" font-size="18" fill="#8b949e">%s</text>
`, esc(sub))

	rows := append([]*Stats{}, r.Agents...)
	if len(rows) > 5 {
		rows = rows[:5]
	}
	rows = append(rows, r.Humans)
	maxRate := 0.0
	for _, s := range rows {
		if s.Rate() > maxRate {
			maxRate = s.Rate()
		}
	}
	if maxRate == 0 {
		maxRate = 1
	}

	y := 170
	rowH := 66
	if len(rows) <= 3 {
		rowH = 86
	}
	if len(r.Agents) == 0 {
		fmt.Fprintf(&b, `<text x="60" y="%d" font-size="24" fill="#e6edf3">No agent-attributed commits found.</text>
`, y+30)
		y += rowH
	}
	for _, s := range rows {
		name := s.Name
		color := agentColor(s.Name)
		if s.Human {
			name = "Humans"
			color = "#6e7681"
		}
		if s.Human {
			fmt.Fprintf(&b, `<line x1="60" y1="%d" x2="1140" y2="%d" stroke="#30363d" stroke-dasharray="4 6"/>
`, y-14, y-14)
		}
		cy := y + 22
		// mugshot badge
		fmt.Fprintf(&b, `<circle cx="88" cy="%d" r="26" fill="%s"/>`, cy, color)
		fmt.Fprintf(&b, `<text x="88" y="%d" text-anchor="middle" font-size="20" font-weight="800" fill="#0b0f14">%s</text>
`, cy+7, esc(initials(name)))
		label := name
		if s.Human {
			label = "Humans (baseline)"
		}
		fmt.Fprintf(&b, `<text x="130" y="%d" font-size="24" font-weight="700" fill="#e6edf3">%s</text>
`, cy-2, esc(truncate(label, 20)))
		detail := fmt.Sprintf("%d of %d commits flagged", s.Flagged, s.Commits)
		if top := s.TopRules(1); len(top) > 0 {
			detail += fmt.Sprintf(" · top: %s ×%d", top[0].Rule, top[0].Count)
		}
		fmt.Fprintf(&b, `<text x="130" y="%d" font-size="16" fill="#8b949e">%s</text>
`, cy+22, esc(truncate(detail, 52)))
		// bar
		barX, barW := 700, 340
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="18" rx="9" fill="#161b22" stroke="#30363d"/>`, barX, cy-9, barW)
		fill := int(float64(barW) * s.Rate() / maxRate)
		if s.Flagged > 0 && fill < 18 {
			fill = 18
		}
		barColor := "#3fb950"
		switch {
		case s.Errors > 0:
			barColor = "#f85149"
		case s.Flagged > 0:
			barColor = "#d29922"
		}
		if fill > 0 {
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="18" rx="9" fill="%s"/>`, barX, cy-9, fill, barColor)
		}
		fmt.Fprintf(&b, `<text x="1140" y="%d" text-anchor="end" font-size="26" font-weight="800" fill="%s">%s</text>
`, cy+9, barColor, esc(pct(s.Rate())))
		y += rowH
	}

	fmt.Fprintf(&b, `<text x="60" y="590" font-size="18" fill="#8b949e">Your agent says the tests pass. <tspan fill="#e6edf3" font-weight="700">narc</tspan> checks whether it cheated.</text>
<text x="1140" y="590" text-anchor="end" font-size="18" fill="#58a6ff">github.com/tranhuuhuy297/narc</text>
</svg>
`)
	_, err := io.WriteString(w, b.String())
	return err
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

var knownColors = map[string]string{
	"Claude Code": "#d97757", "Codex": "#10a37f", "Copilot": "#8957e5", "Cursor": "#e6edf3",
	"Devin": "#3fb6ff", "Jules": "#a142f4", "Gemini CLI": "#4285f4", "Aider": "#14b014",
	"OpenCode": "#f0883e", "Amp": "#f34e3f",
}

func agentColor(name string) string {
	if c, ok := knownColors[name]; ok {
		return c
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	colors := []string{"#ff7b72", "#ffa657", "#d2a8ff", "#79c0ff", "#56d364", "#e3b341", "#f778ba"}
	return colors[int(h.Sum32())%len(colors)]
}

func initials(name string) string {
	var out []rune
	for _, w := range strings.Fields(name) {
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, unicode.ToUpper(r))
				break
			}
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}
