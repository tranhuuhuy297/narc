package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huyth45/narc/internal/core"
)

// RenderOptions controls output.
type RenderOptions struct {
	Color        bool
	Width        int
	MaxOffenses  int  // worst offenses listed per agent (default 3)
	HideDuration bool // for golden tests
}

type palette struct{ on bool }

func (p palette) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (p palette) bold(s string) string   { return p.wrap("1", s) }
func (p palette) dim(s string) string    { return p.wrap("2", s) }
func (p palette) red(s string) string    { return p.wrap("38;5;203", s) }
func (p palette) yellow(s string) string { return p.wrap("38;5;221", s) }
func (p palette) green(s string) string  { return p.wrap("38;5;114", s) }
func (p palette) cyan(s string) string   { return p.wrap("38;5;117", s) }
func (p palette) gray(s string) string   { return p.wrap("38;5;245", s) }
func (p palette) inverse(s string) string {
	return p.wrap("1;38;5;231;48;5;160", s)
}

func pad(s string, n int) string {
	w := utf8.RuneCountInString(stripANSI(s))
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func padLeft(s string, n int) string {
	w := utf8.RuneCountInString(stripANSI(s))
	if w >= n {
		return s
	}
	return strings.Repeat(" ", n-w) + s
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && r == 'm':
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if n <= 1 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", 100*f) }

func dateOf(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Format("2006-01-02")
}

func modelSummary(m map[string]int) string {
	type kv struct {
		k string
		v int
	}
	var all []kv
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	var parts []string
	for i, x := range all {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(all)-3))
			break
		}
		parts = append(parts, x.k)
	}
	return strings.Join(parts, ", ")
}

// Pretty renders the rap sheet for a terminal.
func Pretty(w io.Writer, r *Report, o RenderOptions) error {
	p := palette{o.Color}
	if o.Width <= 0 {
		o.Width = 100
	}
	if o.MaxOffenses <= 0 {
		o.MaxOffenses = 3
	}
	var b strings.Builder
	title := " RAP SHEET "
	fmt.Fprintf(&b, "\n %s  %s\n", p.inverse(title), p.bold(r.Repo))
	meta := fmt.Sprintf("%d commits scanned", r.Scanned)
	if r.Scanned > 0 {
		meta += fmt.Sprintf(" (%s → %s)", dateOf(r.From), dateOf(r.To))
	}
	if r.Skipped > 0 {
		meta += fmt.Sprintf(", %d skipped", r.Skipped)
	}
	if !o.HideDuration {
		meta += fmt.Sprintf(" · %s", r.Duration.Round(10*time.Millisecond))
	}
	if r.Truncated {
		meta += " · limited by --max"
	}
	fmt.Fprintf(&b, " %s\n\n", p.gray(meta))

	if len(r.Agents) == 0 {
		fmt.Fprintf(&b, " %s\n", p.bold("No commits attributed to an AI agent."))
		fmt.Fprintf(&b, " %s\n", p.gray("narc recognizes agents by commit trailers and authors (Co-Authored-By: Claude, copilot-swe-agent[bot],"))
		fmt.Fprintf(&b, " %s\n\n", p.gray("Cursor Agent, (aider), Devin, Jules, Codex, Amp, ...). Agents that leave no trace count as humans."))
	}

	nameW := 18
	for _, a := range r.Agents {
		if n := utf8.RuneCountInString(a.Name) + 2; n > nameW {
			nameW = n
		}
	}
	header := fmt.Sprintf(" %s%s%s%s  %s", pad("SUSPECT", nameW), padLeft("COMMITS", 8), padLeft("FLAGGED", 9), padLeft("RATE", 8), "TOP OFFENSES")
	fmt.Fprintf(&b, "%s\n", p.gray(header))
	row := func(s *Stats) {
		rate := pct(s.Rate())
		switch {
		case s.Flagged == 0:
			rate = p.green(rate)
		case s.Errors > 0:
			rate = p.red(rate)
		default:
			rate = p.yellow(rate)
		}
		name := s.Name
		if s.Human {
			name = "Humans (baseline)"
		}
		var tops []string
		for _, rc := range s.TopRules(3) {
			tops = append(tops, fmt.Sprintf("%s ×%d", rc.Rule, rc.Count))
		}
		top := strings.Join(tops, ", ")
		if top == "" {
			top = p.gray("—")
		}
		used := 1 + nameW + 8 + 9 + 8 + 2
		fmt.Fprintf(&b, " %s%s%s%s  %s\n", pad(p.bold(name), nameW), padLeft(fmt.Sprint(s.Commits), 8),
			padLeft(fmt.Sprint(s.Flagged), 9), padLeft(rate, 8), truncate(top, o.Width-used))
		if len(s.Models) > 0 {
			fmt.Fprintf(&b, " %s\n", p.gray("  models: "+modelSummary(s.Models)))
		}
	}
	for _, a := range r.Agents {
		row(a)
	}
	fmt.Fprintf(&b, " %s\n", p.gray(strings.Repeat("─", min(o.Width-2, nameW+27))))
	row(r.Humans)

	shown := false
	for _, a := range append(append([]*Stats{}, r.Agents...), r.Humans) {
		if len(a.Offenses) == 0 {
			continue
		}
		if !shown {
			fmt.Fprintf(&b, "\n %s\n", p.bold("WORST OFFENSES"))
			shown = true
		}
		label := a.Name
		if a.Human {
			label = "Humans"
		}
		fmt.Fprintf(&b, "\n %s\n", p.cyan(label))
		for i, off := range a.Offenses {
			if i == o.MaxOffenses {
				fmt.Fprintf(&b, "   %s\n", p.gray(fmt.Sprintf("… %d more flagged commits (narc audit --format json for all)", len(a.Offenses)-i)))
				break
			}
			who := ""
			if off.Attribution.Model != "" {
				who = " " + off.Attribution.Model
			}
			fmt.Fprintf(&b, "   %s %s%s  %s\n", p.yellow(off.Commit.Short()), p.gray(dateOf(off.Commit.Time)), p.gray(who),
				truncate(fmt.Sprintf("%q", off.Commit.Subject), o.Width-30))
			for j, f := range off.Findings {
				if j == 4 {
					fmt.Fprintf(&b, "      %s\n", p.gray(fmt.Sprintf("… %d more", len(off.Findings)-j)))
					break
				}
				glyph := p.yellow("!")
				if f.Severity == core.Error {
					glyph = p.red("✗")
				}
				line := fmt.Sprintf("%s %s  %s", pad(f.RuleID, 20), f.Location(), f.Message)
				fmt.Fprintf(&b, "      %s %s\n", glyph, truncate(line, o.Width-9))
			}
		}
	}
	fmt.Fprintf(&b, "\n %s\n", p.gray("Inspect a commit: narc scan <sha> · share: narc audit --card rapsheet.svg"))
	fmt.Fprintf(&b, " %s\n\n", p.gray("Attribution uses commit metadata only; findings are heuristics — read the diff before you judge."))
	_, err := io.WriteString(w, b.String())
	return err
}

// JSON renders the report as JSON.
func JSON(w io.Writer, r *Report) error {
	type agentJSON struct {
		*Stats
		Rate float64 `json:"flag_rate"`
	}
	out := struct {
		*Report
		Agents     []agentJSON `json:"agents"`
		Humans     agentJSON   `json:"humans"`
		DurationMS int64       `json:"duration_ms"`
	}{Report: r, DurationMS: r.Duration.Milliseconds()}
	for _, a := range r.Agents {
		out.Agents = append(out.Agents, agentJSON{a, a.Rate()})
	}
	out.Humans = agentJSON{r.Humans, r.Humans.Rate()}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// Markdown renders the report for issues, PRs and READMEs.
func Markdown(w io.Writer, r *Report, o RenderOptions) error {
	if o.MaxOffenses <= 0 {
		o.MaxOffenses = 5
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## 🚨 narc rap sheet — `%s`\n\n", r.Repo)
	fmt.Fprintf(&b, "%d commits scanned (%s → %s).\n\n", r.Scanned, dateOf(r.From), dateOf(r.To))
	b.WriteString("| suspect | commits | flagged | rate | top offenses |\n|---|---:|---:|---:|---|\n")
	row := func(s *Stats) {
		name := s.Name
		if s.Human {
			name = "_Humans (baseline)_"
		} else {
			name = "**" + name + "**"
		}
		var tops []string
		for _, rc := range s.TopRules(3) {
			tops = append(tops, fmt.Sprintf("`%s` ×%d", rc.Rule, rc.Count))
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s |\n", name, s.Commits, s.Flagged, pct(s.Rate()), strings.Join(tops, ", "))
	}
	for _, a := range r.Agents {
		row(a)
	}
	row(r.Humans)
	for _, a := range r.Agents {
		if len(a.Offenses) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n<details><summary><b>%s</b>: %d flagged commits</summary>\n\n", a.Name, len(a.Offenses))
		for i, off := range a.Offenses {
			if i == o.MaxOffenses {
				fmt.Fprintf(&b, "- … and %d more\n", len(a.Offenses)-i)
				break
			}
			sha := "`" + off.Commit.Short() + "`"
			if u := r.CommitURL(off.Commit.SHA); u != "" {
				sha = fmt.Sprintf("[`%s`](%s)", off.Commit.Short(), u)
			}
			fmt.Fprintf(&b, "- %s %s — %s\n", sha, dateOf(off.Commit.Time), escapeMD(off.Commit.Subject))
			for _, f := range off.Findings {
				fmt.Fprintf(&b, "  - `%s` %s — %s\n", f.RuleID, f.Location(), escapeMD(f.Message))
			}
		}
		b.WriteString("\n</details>\n")
	}
	b.WriteString("\n<sub>Generated by [narc](https://github.com/huyth45/narc). Attribution uses commit metadata; findings are heuristics.</sub>\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func escapeMD(s string) string {
	r := strings.NewReplacer("|", `\|`, "<", "&lt;", ">", "&gt;", "\n", " ")
	return r.Replace(s)
}
