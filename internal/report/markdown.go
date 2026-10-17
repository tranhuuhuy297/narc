package report

import (
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

const (
	// mdLimit keeps a Markdown report comfortably under GitHub's 65,536
	// character limit for comments (sizes are counted in bytes, which is
	// never less than characters). Findings get what the other sections
	// leave over.
	mdLimit = 60000
	// The waived-findings table is capped by rows, by size, and by the
	// length of each reason.
	mdMaxWaivedRows  = 50
	mdWaivedBudget   = 12000
	mdMaxReasonBytes = 300
	// mdOpenAll is how many findings a report may have before only the
	// files with errors start out expanded.
	mdOpenAll = 15
	// One finding must never eat the whole budget (a minified bundle line
	// can be hundreds of kilobytes), so its parts are capped too: code by
	// lines and bytes per line, prose (title, message, hint) by bytes.
	// Bytes, not columns: zero-width characters take no columns at all.
	// Even with the worst escaping blow-up (an @mention grows about 5x),
	// a finding stays well under what the other sections leave over.
	mdMaxCodeLines     = 20
	mdMaxCodeLineBytes = 300
	mdMaxProseBytes    = 1000
	// Internal errors and the label sit outside the findings budget.
	mdMaxErrors     = 10
	mdMaxErrorBytes = 300
	mdMaxLabelBytes = 200
)

var mdSevEmoji = map[core.Severity]string{core.Error: "🔴", core.Warning: "🟡", core.Info: "🔵"}

// mdSev is the emoji for a severity. Anything unexpected is shown as a
// note, like the other formats do: an empty emoji would leave "** title**",
// which GitHub renders as literal asterisks.
func mdSev(s core.Severity) string {
	if e, ok := mdSevEmoji[s]; ok {
		return e
	}
	return mdSevEmoji[core.Info]
}

const mdWaivedEmoji = "⚪"

var mdVerdictEmoji = map[string]string{"busted": "🚨", "suspicious": "🤨", "clean": "✅"}

// Markdown writes a report for PR comments and $GITHUB_STEP_SUMMARY: a
// verdict header, a summary table, findings grouped by file in collapsible
// sections with their code and hints, the waived findings (always listed,
// so reviewers can audit waivers), and a footer. It starts with
// MarkdownMarker so a bot can update one sticky comment. Very large
// reports are cut to fit GitHub's comment size limit.
func Markdown(w io.Writer, res *engine.Result, o Options) error {
	if res == nil {
		return errNilResult
	}
	v := newView(res)
	var head, findings, waived, footer strings.Builder
	head.WriteString(MarkdownMarker + "\n")
	mdHeader(&head, v)
	mdInternalErrors(&head, res.Errors)
	mdWaived(&waived, v.waived)
	mdFooter(&footer, v, o)
	mdFindings(&findings, v, o, mdLimit-head.Len()-waived.Len()-footer.Len())
	_, err := io.WriteString(w, head.String()+findings.String()+waived.String()+footer.String())
	return err
}

func mdHeader(b *strings.Builder, v *view) {
	fmt.Fprintf(b, "### %s narc: %s · integrity %d/100\n\n", mdVerdictEmoji[v.verdict.key], v.verdict.label, v.score)

	scope := "this change"
	if v.res.Label != "" {
		scope = mdCode(clipBytes(v.res.Label, mdMaxLabelBytes, "…"))
	}
	switch {
	case v.verdict.key == "busted":
		fmt.Fprintf(b, "narc found **%s** in %s. Errors are high-confidence signs that verification was weakened instead of the code being fixed.\n\n",
			countPhrase(v.errors, v.warns, v.infos), scope)
	case v.verdict.key == "suspicious":
		fmt.Fprintf(b, "narc found **%s** in %s. Warnings are often cheats but sometimes legitimate, so each needs a human look.\n\n",
			countPhrase(v.errors, v.warns, v.infos), scope)
	case v.infos > 0:
		verb := "are"
		if v.infos == 1 {
			verb = "is"
		}
		fmt.Fprintf(b, "Nothing in %s weakens verification; the %s below %s for context.\n\n", scope, plural(v.infos, "note"), verb)
	case len(v.waived) > 0:
		// "No skipped tests" would be false: a waived finding may well be
		// a skipped test, accepted with a reason.
		fmt.Fprintf(b, "Nothing in %s weakens verification, apart from %s accepted with a reason (listed below).\n\n",
			scope, plural(len(v.waived), "waived finding"))
	case v.res.Files == 0:
		fmt.Fprintf(b, "Nothing to analyze in %s.\n\n", scope)
	default:
		fmt.Fprintf(b, "No skipped tests, weakened assertions or CI escapes in %s.\n\n", scope)
	}

	b.WriteString("| " + mdSevEmoji[core.Error] + " Errors | " + mdSevEmoji[core.Warning] + " Warnings | " +
		mdSevEmoji[core.Info] + " Notes | " + mdWaivedEmoji + " Waived | Files | Lines |\n")
	b.WriteString("| :---: | :---: | :---: | :---: | :---: | :---: |\n")
	files := fmt.Sprint(v.res.Files)
	if v.res.Skipped > 0 {
		files += fmt.Sprintf(" (%d skipped)", v.res.Skipped)
	}
	fmt.Fprintf(b, "| %s | %s | %d | %d | %s | +%d −%d |\n\n",
		mdBoldIf(v.errors), mdBoldIf(v.warns), v.infos, len(v.waived), files, v.res.Added, v.res.Removed)
}

func mdBoldIf(n int) string {
	if n > 0 {
		return fmt.Sprintf("**%d**", n)
	}
	return "0"
}

func mdInternalErrors(b *strings.Builder, errs []string) {
	if len(errs) == 0 {
		return
	}
	fmt.Fprintf(b, "> [!WARNING]\n> %s failed to run, so this report may be incomplete.\n", plural(len(errs), "rule"))
	for i, e := range errs {
		if i == mdMaxErrors {
			fmt.Fprintf(b, "> - and %d more (see `--format json`)\n", len(errs)-i)
			break
		}
		fmt.Fprintf(b, "> - %s\n", mdCode(clipBytes(firstLine(e), mdMaxErrorBytes, "…")))
	}
	b.WriteString("\n")
}

// mdFindings writes the findings grouped by file, stopping with a note
// once the next finding would take the section over budget bytes.
func mdFindings(b *strings.Builder, v *view, o Options, budget int) {
	if len(v.active) == 0 {
		return
	}
	b.WriteString("#### Findings\n\n")
	used, shown := 0, 0
	for _, g := range groupByFile(v.active) {
		header := mdGroupHeader(g, len(v.active) <= mdOpenAll)
		var body strings.Builder
		full := false
		for _, f := range g.findings {
			var s strings.Builder
			mdFinding(&s, f, o)
			if shown > 0 && used+len(header)+body.Len()+s.Len() > budget {
				full = true
				break
			}
			body.WriteString(s.String())
			shown++
		}
		if body.Len() > 0 {
			section := header + body.String() + "</details>\n\n"
			b.WriteString(section)
			used += len(section)
		}
		if full {
			fmt.Fprintf(b, "> [!NOTE]\n> %s not shown: the report is too long for a comment. "+
				"Run `narc scan` locally, or upload SARIF to code scanning, to see them all.\n\n",
				plural(len(v.active)-shown, "more finding"))
			return
		}
	}
}

// mdGroupHeader opens a file's collapsible section. It starts expanded
// when the report is short or the file has errors.
func mdGroupHeader(g fileGroup, openAll bool) string {
	errs, warns, infos := severityCounts(g.findings)
	open := ""
	if openAll || errs > 0 {
		open = " open"
	}
	var counts []string
	for _, c := range []struct {
		n   int
		sev core.Severity
	}{{errs, core.Error}, {warns, core.Warning}, {infos, core.Info}} {
		if c.n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", mdSevEmoji[c.sev], c.n))
		}
	}
	// The path sits in a raw HTML block, which a blank line would end:
	// sanitize turns line breaks (git allows them in file names) into ^J,
	// so a crafted path cannot break out into Markdown.
	return fmt.Sprintf("<details%s>\n<summary><code>%s</code> · %s</summary>\n\n",
		open, html.EscapeString(sanitize(g.path, false)), strings.Join(counts, " · "))
}

// mdFinding writes one finding:
//
//	**🔴 Test skipped** · [`skip-added`](docs) · [line 42](blob)\
//	test "rejects expired tokens" was skipped with `it.skip`
//	```ts
//	it.skip('rejects expired tokens', async () => {
//	```
//	> 💡 hint
func mdFinding(b *strings.Builder, f core.Finding, o Options) {
	loc := where(f)
	if link := mdLineLink(o.LinkBase, f); link != "" {
		loc = "[" + loc + "](" + link + ")"
	}
	fmt.Fprintf(b, "**%s %s** · [%s](%s) · %s\\\n", mdSev(f.Severity), mdInline(mdClip(firstNonEmpty(f.Title, f.RuleID))),
		mdCode(f.RuleID), RuleDocURL(f.RuleID), loc)
	// The message continues the paragraph after a hard line break, where a
	// leading "# " or "- " would start a heading or a list.
	b.WriteString(mdLineStart(mdInline(mdClip(firstNonEmpty(f.Message, f.Title, f.RuleID)))) + "\n\n")

	switch {
	case f.Before != "":
		var code strings.Builder
		mdCodeLines(&code, "- ", f.Before)
		if f.Evidence != "" {
			mdCodeLines(&code, "+ ", f.Evidence)
		}
		mdFence(b, "diff", code.String())
	case f.Evidence != "":
		var code strings.Builder
		mdCodeLines(&code, "", f.Evidence)
		mdFence(b, fenceLang(f.Path), code.String())
	}
	if o.ShowHints && strings.TrimSpace(f.Hint) != "" {
		b.WriteString("> 💡 " + mdInline(mdClip(f.Hint)) + "\n\n")
	}
}

// mdClip makes prose safe (see mdSafe), trims it and caps it at
// mdMaxProseBytes. Capping after sanitizing keeps the cap a real bound.
func mdClip(s string) string {
	return clipBytes(strings.TrimSpace(mdSafe(s)), mdMaxProseBytes, "…")
}

// mdCodeLines writes code for a fence, each line after prefix: at most
// mdMaxCodeLines lines of at most mdMaxCodeLineBytes each, sanitized like
// terminal output (tabs kept) so the comment shows what is really there.
func mdCodeLines(b *strings.Builder, prefix, code string) {
	for _, l := range capLines(codeSplit(code), mdMaxCodeLines, "…") {
		b.WriteString(prefix + clipBytes(sanitizeCode(l), mdMaxCodeLineBytes, "…") + "\n")
	}
}

// mdSafe puts untrusted text on one line and makes control characters and
// bidirectional overrides visible (see sanitize). Markdown reports are
// printed to terminals too, and in a PR comment an override such as U+202E
// would make the quoted code read differently from what it does.
func mdSafe(s string) string {
	s = strings.TrimRight(s, "\r\n")
	return sanitize(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " "), false)
}

// mdOrderedRe matches a leading ordered-list marker such as "1." or "2)".
var mdOrderedRe = regexp.MustCompile(`^\d{1,9}[.)]`)

// mdLineStart escapes the first character of rendered inline Markdown that
// starts a line, when it would otherwise open a block: an ATX heading
// ("#"), a bullet list ("-", "+"), an ordered list ("1.") or a setext
// underline ("=", "-"). Other block markers (">", "*", "_", "~", "<", "`")
// are escaped by mdInline already.
func mdLineStart(s string) string {
	if s == "" {
		return s
	}
	if strings.IndexByte("#-+=", s[0]) >= 0 {
		return `\` + s
	}
	if loc := mdOrderedRe.FindStringIndex(s); loc != nil {
		return s[:loc[1]-1] + `\` + s[loc[1]-1:]
	}
	return s
}

// mdLineLink links a finding's line in the repository, when a link base is
// configured. Findings without a new-side line get no link: the code they
// point at does not exist at that revision.
func mdLineLink(base string, f core.Finding) string {
	if base == "" || f.Line <= 0 {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/" + (&url.URL{Path: f.Path}).EscapedPath() + fmt.Sprintf("#L%d", f.Line)
}

func mdWaived(b *strings.Builder, fs []core.Finding) {
	if len(fs) == 0 {
		return
	}
	fmt.Fprintf(b, "<details>\n<summary>%s %s</summary>\n\n", mdWaivedEmoji, plural(len(fs), "waived finding"))
	b.WriteString("| Rule | Location | Reason |\n| --- | --- | --- |\n")
	start := b.Len()
	for i, f := range fs {
		if i == mdMaxWaivedRows || b.Len()-start > mdWaivedBudget {
			fmt.Fprintf(b, "| … | %s | |\n", plural(len(fs)-i, "more waived finding"))
			break
		}
		reason := strings.TrimSpace(f.WaiverReason)
		if reason == "" {
			reason = "_no reason recorded_"
		} else {
			reason = mdCell(clipBytes(mdSafe(reason), mdMaxReasonBytes, "…"))
		}
		fmt.Fprintf(b, "| [%s](%s) | %s | %s |\n", mdCell(mdCode(f.RuleID)), RuleDocURL(f.RuleID),
			strings.ReplaceAll(mdCode(f.Location()), "|", `\|`), reason)
	}
	b.WriteString("\n</details>\n\n")
}

func mdFooter(b *strings.Builder, v *view, o Options) {
	parts := []string{fmt.Sprintf("🕵️ [narc](%s) %s", ProjectURL, o.displayVersion())}
	if o.Command != "" {
		parts = append(parts, mdCode(o.Command))
	}
	if !o.HideDuration {
		parts = append(parts, mdText(formatDuration(v.res.Duration)))
	}
	b.WriteString("---\n<sub>" + strings.Join(parts, " · ") + "<br>")
	fmt.Fprintf(b, "False positive? Waive it with a reason: `narc:allow <rule> reason=\"…\"` in a comment on or above the line, "+
		"or an `allow:` entry in `.narc.yml`. [Rule reference](%s)</sub>\n", RulesDocURL)
}

// ---- Markdown escaping ----

// mentionRe matches @handles (and decorators such as @pytest.mark.skip,
// which GitHub would otherwise treat as a mention of user "pytest").
// GitHub links a mention after any non-word character, including "/",
// ".", "@" and "`", so only a preceding word character rules one out (as
// in dev@example.com). A handle swallows any "@" glued to it: in "@a@b"
// GitHub links only @a, but once @a is wrapped, "@b" would start a text
// node of its own and be linked.
var mentionRe = regexp.MustCompile(`(^|[^A-Za-z0-9_])(@[A-Za-z0-9](?:[A-Za-z0-9_.:/@-]*[A-Za-z0-9_])?)`)

// mdEscaper escapes inline Markdown syntax in plain text. "$" is escaped
// because GitHub renders $...$ as math: a test named "reads $HOME/$USER"
// would otherwise lose its dollar signs and turn into a formula.
var mdEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
	"<", `\<`, ">", `\>`, "~", `\~`, "&", `\&`, "$", `\$`,
)

// mdInline renders untrusted text as inline Markdown. `code spans` are
// kept as they are; elsewhere Markdown and HTML syntax is escaped and
// @mentions are wrapped in <code>, so a PR comment never pings anyone.
//
// Code spans are matched the way CommonMark does it (see mapCodeSpans). A
// run of backticks without a partner is literal text and gets escaped.
// Matching any other way lets text that is escaped here end up outside a
// code span on GitHub, or text kept verbatim here end up outside one.
func mdInline(s string) string {
	return mapCodeSpans(mdSafe(s), mdText, func(span string) string { return span })
}

// mapCodeSpans rewrites s piecewise: text is applied to each stretch
// outside code spans, span to each code span (backticks included). Spans
// are matched as CommonMark does: a run of n backticks is closed only by
// the next run of exactly n, and a run without a partner is plain text.
func mapCodeSpans(s string, text, span func(string) string) string {
	var b strings.Builder
	start := 0 // start of the plain text not written yet
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		n := backtickRun(s, i)
		end := -1
		for j := i + n; j < len(s); {
			if s[j] != '`' {
				j++
				continue
			}
			m := backtickRun(s, j)
			if m == n {
				end = j + m
				break
			}
			j += m
		}
		if end < 0 { // no closing run: the backticks are literal
			i += n
			continue
		}
		b.WriteString(text(s[start:i]))
		b.WriteString(span(s[i:end]))
		i, start = end, end
	}
	b.WriteString(text(s[start:]))
	return b.String()
}

// backtickRun is the length of the run of backticks starting at s[i].
func backtickRun(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

// mdText escapes plain (non-code) text. Mentions are wrapped in an HTML
// <code> element rather than a backtick span: a backtick next to a code
// span the text sits between could merge with it and leave the mention
// outside any span.
func mdText(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range mentionRe.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[4], m[5] // the @handle group; it holds no HTML syntax
		b.WriteString(mdEscaper.Replace(s[last:start]))
		b.WriteString("<code>" + s[start:end] + "</code>")
		last = end
	}
	b.WriteString(mdEscaper.Replace(s[last:]))
	return b.String()
}

// mdCell is mdInline for a table cell, where pipes must be escaped even
// inside code spans.
func mdCell(s string) string { return strings.ReplaceAll(mdInline(s), "|", `\|`) }

// mdCode renders s as a code span, whatever backticks it contains.
func mdCode(s string) string {
	s = mdSafe(s)
	ticks := "`"
	for strings.Contains(s, ticks) {
		ticks += "`"
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return ticks + s + ticks
}

// mdFence writes a fenced code block, with a fence longer than any run of
// backticks in the code.
func mdFence(b *strings.Builder, info, code string) {
	fence := "```"
	for strings.Contains(code, fence) {
		fence += "`"
	}
	b.WriteString(fence + info + "\n" + code + fence + "\n\n")
}

// fenceLang is the code-fence language for a path, for syntax highlighting.
func fenceLang(p string) string {
	if l := lang.Detect(p); l != lang.Unknown {
		return l.String()
	}
	return ""
}
