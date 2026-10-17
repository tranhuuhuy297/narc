package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
)

// Adversarial cases: input crafted to break a format (pings from a PR
// comment, Markdown that escapes its block, terminal control, oversized
// comments) and odd but legitimate input (CRLF files, deleted files,
// unicode paths, long rule IDs) that must render cleanly.

// githubVisible models what GitHub's mention filter sees in rendered inline
// Markdown: backslash escapes resolved, code spans (matched as CommonMark
// does) and <code> elements dropped. It also reports raw HTML other than
// <code>, and Markdown syntax that is still live outside code.
func githubVisible(md string) (text string, live []string) {
	var b strings.Builder
	for i := 0; i < len(md); {
		c := md[i]
		switch {
		case c == '\\' && i+1 < len(md) && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", md[i+1]) >= 0:
			b.WriteByte(md[i+1])
			i += 2
		case c == '`':
			n := backtickRun(md, i)
			end := -1
			for j := i + n; j < len(md); {
				if md[j] != '`' {
					j++
					continue
				}
				if m := backtickRun(md, j); m == n {
					end = j + m
					break
				} else {
					j += m
				}
			}
			if end < 0 {
				b.WriteString(md[i : i+n])
				i += n
				continue
			}
			b.WriteByte(' ') // a code span: a separate node, never a mention
			i = end
		case strings.HasPrefix(md[i:], "<code>"):
			j := strings.Index(md[i:], "</code>")
			if j < 0 {
				live = append(live, "unclosed <code>")
				i += len("<code>")
				continue
			}
			b.WriteByte(' ')
			i += j + len("</code>")
		case strings.IndexByte("<*_[]~&>$", c) >= 0: // "$" opens GitHub math
			live = append(live, string(c))
			b.WriteByte(c)
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), live
}

// ghMentionRe is (a superset of) what GitHub links as a user or team mention.
var ghMentionRe = regexp.MustCompile(`(^|[^A-Za-z0-9_])@[A-Za-z0-9]`)

func assertNoMention(t *testing.T, in, out string) {
	t.Helper()
	vis, live := githubVisible(out)
	if ghMentionRe.MatchString(vis) {
		t.Errorf("mdInline(%q) = %q leaves a live @mention (visible text %q)", in, out, vis)
	}
	if len(live) > 0 {
		t.Errorf("mdInline(%q) = %q leaves live Markdown/HTML syntax %q", in, out, live)
	}
}

func TestMarkdownMentionsCannotEscape(t *testing.T) {
	for _, in := range []string{
		"``@user` `", // a double-tick run with no partner next to a span
		"`x`@user",   // mention right after a code span
		"@user`x`",   // ...and right before one
		"``x``@user", // after a double-tick span
		"foo/@user",  // GitHub links after "/"
		"a.@user",    // ...after "."
		"@@user",     // ...after "@"
		"unbalanced `@user here",
		"\\`@user",         // backslash before a stray tick
		"(@user), [@user]", // punctuation around
		"@org/team-name ping",
		"émoji→@user",
		"`a` `b` @c `d`",
		"```@x``` and ``@y`` and `@z` @w",
	} {
		assertNoMention(t, in, mdInline(in))
	}
	// Look-alikes that are not mentions stay readable.
	for in, want := range map[string]string{
		"mail dev@example.com":             "mail dev@example.com",
		"`@pytest.mark.skip` was added":    "`@pytest.mark.skip` was added",
		"assertions dropped 3 → 1":         "assertions dropped 3 → 1",
		"node_modules/@types/node changed": `node\_modules/<code>@types/node</code> changed`,
	} {
		if got := mdInline(in); got != want {
			t.Errorf("mdInline(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMarkdownInlineRandom throws random mixes of the characters that
// matter at mdInline and checks that nothing live survives.
func TestMarkdownInlineRandom(t *testing.T) {
	alphabet := []string{"`", "``", "@", "u", "x", " ", "\\", "/", ".", "<", ">", "_", "*", "[", "]", "&", "~", "|", "$", "é"}
	rng := rand.New(rand.NewSource(1))
	for n := 0; n < 20000; n++ {
		var b strings.Builder
		for k := rng.Intn(14); k >= 0; k-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		in := b.String()
		assertNoMention(t, in, mdInline(in))
		if t.Failed() {
			return
		}
	}
}

func TestMarkdownCodeSpanRoundTrip(t *testing.T) {
	// mdCode must keep any text inside one span, whatever ticks it holds.
	for _, s := range []string{"a`b", "``", "`lead", "trail`", "x``y`z", "main → HEAD", "@user"} {
		out := mdCode(s)
		if vis, live := githubVisible(out); strings.TrimSpace(vis) != "" || len(live) > 0 {
			t.Errorf("mdCode(%q) = %q is not a single code span (outside: %q %q)", s, out, vis, live)
		}
	}
}

// TestMarkdownMessageCannotOpenBlock: a message continues the paragraph
// after the title line, where "# noqa" would become a heading and "- x" a
// list. Display text must not change.
func TestMarkdownMessageCannotOpenBlock(t *testing.T) {
	cases := map[string]string{
		"# noqa added for E501":  `\# noqa added for E501`,
		"- removed the timeout":  `\- removed the timeout`,
		"+ retries: 3":           `\+ retries: 3`,
		"1. first test dropped":  `1\. first test dropped`,
		"12) retried":            `12\) retried`,
		"===":                    `\===`,
		"   # indented heading":  `\# indented heading`,
		"10 tests removed":       `10 tests removed`, // no marker: untouched
		"`--no-verify` added":    "`--no-verify` added",
		"assertions dropped 3→1": "assertions dropped 3→1",
	}
	for msg, want := range cases {
		res := &engine.Result{Files: 1, Findings: []core.Finding{finding("lint-suppressed", "a.py", 3, msg)}}
		out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{HideDuration: true}) })
		if !strings.Contains(out, "\\\n"+want+"\n\n") {
			t.Errorf("message %q: want line %q in:\n%s", msg, want, out)
		}
	}
}

// TestMarkdownPathCannotBreakOut: git allows newlines in file names. A
// blank line inside the <summary> HTML block would end it and let the rest
// of the name render as Markdown (with live mentions).
func TestMarkdownPathCannotBreakOut(t *testing.T) {
	path := "src/a\n\n@evil **x**\n\n</code></summary>b.test.ts"
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", path, 3, "skipped"), func(f *core.Finding) { f.Evidence = "it.skip('x')" }),
		with(finding("skip-added", path, 9, "skipped"), func(f *core.Finding) { f.Waived, f.WaiverReason = true, "flaky, see #12" }),
	}}
	out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{LinkBase: "https://github.com/o/r/blob/abc"}) })
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "@evil") || strings.HasPrefix(l, "</code></summary>b") {
			t.Errorf("path broke out of its HTML block: line %q in\n%s", l, out)
		}
	}
	if !strings.Contains(out, "<summary><code>src/a^J^J@evil **x**^J^J&lt;/code&gt;&lt;/summary&gt;b.test.ts</code>") {
		t.Errorf("unexpected summary for a hostile path:\n%s", out)
	}
	if !strings.Contains(out, "(https://github.com/o/r/blob/abc/src/a%0A%0A@evil%20%2A%2Ax%2A%2A%0A%0A%3C/code%3E%3C/summary%3Eb.test.ts#L3)") {
		t.Errorf("line link does not escape the path:\n%s", out)
	}
}

// TestMarkdownHugeFindingFitsComment: one finding (a minified bundle line,
// a flood of zero-width characters, a pathological message) must not take
// the comment over GitHub's limit, since the first finding is always shown.
func TestMarkdownHugeFindingFitsComment(t *testing.T) {
	huge := strings.Repeat("x", 300000)
	zeroWidth := strings.Repeat("́", 100000)  // no columns, many bytes
	mentions := strings.Repeat("(@a ", 50000) // grows ~5x when escaped
	res := &engine.Result{
		Label:  strings.Repeat("main → HEAD ", 5000),
		Files:  1,
		Errors: make([]string, 500),
		Findings: []core.Finding{
			with(finding("hardcoded-answer", "dist-like/bundle.ts", 1, mentions), func(f *core.Finding) {
				f.Title = mentions
				f.Hint = mentions
				f.Evidence = huge + "\n" + zeroWidth + strings.Repeat("\nline", 500)
				f.Before = huge + "\n" + zeroWidth
			}),
			finding("any-cast", "z.ts", 1, "`as any` cast added"),
		},
	}
	for i := range res.Errors {
		res.Errors[i] = "rule x crashed: " + huge
	}
	for i := 0; i < 200; i++ {
		res.Findings = append(res.Findings, with(finding("skip-added", fmt.Sprintf("w/%d.test.ts", i), 1, "s"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, zeroWidth
		}))
	}
	out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{ShowHints: true}) })
	if len(out) > 65536 {
		t.Fatalf("report is %d bytes, over GitHub's 65,536 limit", len(out))
	}
	for _, want := range []string{"…", "and 490 more", "more waived findings"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if !strings.Contains(out, "dist-like/bundle.ts") {
		t.Error("the oversized finding was dropped instead of clipped")
	}
	if strings.Count(out, "<details") != strings.Count(out, "</details>") {
		t.Error("unbalanced <details>")
	}
	if !strings.HasPrefix(out, MarkdownMarker+"\n") {
		t.Error("marker missing")
	}
}

func TestClipBytes(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdefghij", 6, "abc…"}, // "…" is 3 bytes
		{"日本語テキスト", 10, "日本…"},    // never splits a rune
		{"abc", 2, "…"},           // smaller than the ellipsis
		{"́́́", 5, "́…"},
	}
	for _, c := range cases {
		got := clipBytes(c.in, c.n, "…")
		if got != c.want {
			t.Errorf("clipBytes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !strings.HasSuffix(got, "…") && got != c.in {
			t.Errorf("clipBytes(%q) lost its ellipsis", c.in)
		}
	}
}

// TestOnlyWaivedIsNotDescribedAsSpotless: with every finding waived the
// verdict is clean, but saying "No skipped tests" would be false when a
// waived finding is a skipped test.
func TestOnlyWaivedIsNotDescribedAsSpotless(t *testing.T) {
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", "a.test.ts", 3, "test \"x\" was skipped"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, "upstream outage, see #12"
		}),
	}}
	pretty := renderString(t, func(w io.Writer) error { return Pretty(w, res, Options{HideDuration: true}) })
	md := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{HideDuration: true}) })
	for name, out := range map[string]string{"pretty": pretty, "markdown": md} {
		if strings.Contains(out, "No skipped tests") {
			t.Errorf("%s claims no skipped tests although one was waived:\n%s", name, out)
		}
		if !strings.Contains(out, "apart from 1 waived finding") {
			t.Errorf("%s does not mention the waived finding:\n%s", name, out)
		}
	}
	if !strings.Contains(pretty, "CLEAN") {
		t.Errorf("verdict changed:\n%s", pretty)
	}
	// Truly clean output keeps its wording.
	clean := renderString(t, func(w io.Writer) error { return Pretty(w, cleanResult(), Options{HideDuration: true}) })
	if !strings.Contains(clean, "No skipped tests") {
		t.Errorf("clean wording changed:\n%s", clean)
	}
}

// TestCRLFIsNotNoise: code from a CRLF file must not show ^M or raw CRs in
// any format; a CR inside a line still shows.
func TestCRLFIsNotNoise(t *testing.T) {
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("assertion-weakened", "src/a.test.ts", 8, "`toEqual(1)` was replaced by `toBeDefined()`"), func(f *core.Finding) {
			f.OldLine = 8
			f.Before = "expect(a).toEqual(1)\r\nexpect(b).toEqual(2)\r\n"
			f.Evidence = "expect(a).toBeDefined()\r"
		}),
	}}
	o := Options{HideDuration: true, ShowHints: true}
	outs := map[string]string{
		"pretty":   renderString(t, func(w io.Writer) error { return Pretty(w, res, o) }),
		"markdown": renderString(t, func(w io.Writer) error { return Markdown(w, res, o) }),
		"github":   renderString(t, func(w io.Writer) error { return GitHub(w, res, o) }),
		"sarif":    renderString(t, func(w io.Writer) error { return SARIF(w, res, testRules, o) }),
	}
	for name, out := range outs {
		if strings.Contains(out, "^M") || strings.Contains(out, "\r") || strings.Contains(out, `\r`) || strings.Contains(out, "%0D") {
			t.Errorf("%s shows the CRLF line ending:\n%s", name, out)
		}
	}
	if !strings.Contains(outs["markdown"], "- expect(a).toEqual(1)\n- expect(b).toEqual(2)\n+ expect(a).toBeDefined()\n") {
		t.Errorf("markdown diff fence:\n%s", outs["markdown"])
	}
	if !strings.Contains(outs["github"], "Before: expect(a).toEqual(1) ⏎ expect(b).toEqual(2)%0A") {
		t.Errorf("github before line:\n%s", outs["github"])
	}
}

// TestPrettyFitsWidthHostile: wide characters, tabs, unbreakable words,
// long rule IDs and control characters still fit every width.
func TestPrettyFitsWidthHostile(t *testing.T) {
	long := &core.Rule{ID: "verification-hook-bypassed", Title: "Verification hook bypassed", Category: "ci", Severity: core.Warning,
		Summary: "`--no-verify`, `HUSKY=0`, pre-commit hooks removed.", Hint: "Run the hooks and fix what they report."}
	res := &engine.Result{
		Label: "feature/日本語-ブランチ → HEAD\x1b[2J",
		Files: 3,
		Findings: []core.Finding{
			{RuleID: long.ID, Title: long.Title, Category: "ci", Severity: core.Warning, Path: ".husky/pre-commit", Line: 1,
				Message: "pre-commit hook emptied: `npx lint-staged` no longer runs", Evidence: "\t\texit 0 # 🚨🚨🚨 全部スキップ"},
			{RuleID: "skip-added", Title: "テストがスキップされました（とても長いタイトル）", Severity: core.Error, Path: "src/テスト/ユーザー.test.ts", Line: 12345,
				Message:  "テスト「期限切れのトークンを拒否する」がスキップされました " + strings.Repeat("https://example.com/a/very/long/unbreakable/url/", 4),
				Evidence: strings.Repeat("😀", 80), Before: "\x00\x01\x02" + strings.Repeat("x", 300), OldLine: 99999},
			{RuleID: "an-unreasonably-long-custom-rule-identifier-that-goes-on", Severity: core.Info, Path: "x", Message: "m"},
		},
	}
	for _, width := range []int{1, 39, 40, 41, 47, 53, 60, 77, 80, 100, 140} {
		for _, ascii := range []bool{false, true} {
			for _, color := range []bool{false, true} {
				o := Options{Width: width, ASCII: ascii, Color: color, ShowHints: true, ShowWaived: true}
				limit := max(width, minWidth)
				outs := map[string]string{
					"pretty": renderString(t, func(w io.Writer) error { return Pretty(w, res, o) }),
					"rules":  renderString(t, func(w io.Writer) error { return RulesTable(w, append([]*core.Rule{long}, testRules...), o) }),
				}
				// Explain's header must fit; its copyable examples and URL
				// are allowed to wrap in the terminal (documented).
				explain := renderString(t, func(w io.Writer) error { return Explain(w, long, o) })
				outs["explain-head"] = strings.Join(strings.SplitN(explain, "\n", 5)[:4], "\n")
				for name, out := range outs {
					for i, l := range strings.Split(out, "\n") {
						if w := textWidth(l); w > limit {
							t.Errorf("%s width=%d ascii=%v color=%v line %d is %d columns: %q", name, width, ascii, color, i+1, w, l)
						}
					}
					if !color && strings.Contains(out, "\x1b") {
						t.Errorf("%s: escape byte without color", name)
					}
				}
				if !strings.Contains(outs["pretty"], "verification-hook-bypassed") {
					t.Errorf("width=%d: the rule ID was cut:\n%s", width, outs["pretty"])
				}
			}
		}
	}
}

// TestRenderersDoNotMutate: renderers sort and fingerprint copies.
func TestRenderersDoNotMutate(t *testing.T) {
	res := bustedResult()
	for i := range res.Findings {
		res.Findings[i].Fingerprint = "" // as if built by hand
	}
	res.Findings[0], res.Findings[len(res.Findings)-1] = res.Findings[len(res.Findings)-1], res.Findings[0]
	before := append([]core.Finding(nil), res.Findings...)
	o := reproducible()
	for _, fn := range []func(io.Writer) error{
		func(w io.Writer) error { return Pretty(w, res, o) },
		func(w io.Writer) error { return JSON(w, res, o) },
		func(w io.Writer) error { return SARIF(w, res, testRules, o) },
		func(w io.Writer) error { return Markdown(w, res, o) },
		func(w io.Writer) error { return GitHub(w, res, o) },
	} {
		render(t, fn)
	}
	if !reflect.DeepEqual(before, res.Findings) {
		t.Error("a renderer modified res.Findings")
	}
	// Fingerprints are still filled in for output, and match the engine's.
	var doc struct {
		Findings []struct{ Rule, Fingerprint string }
	}
	if err := json.Unmarshal(render(t, func(w io.Writer) error { return JSON(w, res, o) }), &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, f := range bustedResult().Findings {
		want[f.Fingerprint] = true
	}
	for _, f := range doc.Findings {
		if !want[f.Fingerprint] {
			t.Errorf("%s: fingerprint %q differs from the engine's", f.Rule, f.Fingerprint)
		}
	}
}

// TestRenderConcurrently: renderers share no mutable state.
func TestRenderConcurrently(t *testing.T) {
	res := bustedResult()
	want := renderString(t, func(w io.Writer) error { return Markdown(w, res, reproducible()) })
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			_ = Pretty(io.Discard, res, Options{Color: true})
			_ = SARIF(io.Discard, res, testRules, Options{})
			if err := Markdown(&buf, res, reproducible()); err != nil || buf.String() != want {
				errs <- "concurrent Markdown output differs"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestSARIFOddFindings: deleted files, unicode and URL-special paths,
// findings without a message, and waivers without a reason still produce
// valid results that point at the right file.
func TestSARIFOddFindings(t *testing.T) {
	res := &engine.Result{Files: 4, Findings: []core.Finding{
		// Code removed from a file that no longer exists.
		with(finding("assertions-removed", "tests/old auth#1.test.ts", 0, ""), func(f *core.Finding) {
			f.OldLine = 7
			f.Before = "expect(x).toBe(1)"
		}),
		finding("skip-added", "src/ünï/código?.test.ts", 4, "skipped"),
		finding("any-cast", "a:b/c.ts", 2, "cast"),
		with(finding("protected-path", "golden/a.txt", 0, "protected file deleted"), func(f *core.Finding) {
			f.Waived = true // reason lost upstream: still a valid suppression
		}),
	}}
	doc := decodeSARIF(t, res, testRules, Options{})
	got := map[string]string{}
	for _, r := range doc.Runs[0].Results {
		loc := r.Locations[0].Physical
		u, err := url.Parse(loc.Artifact.URI)
		if err != nil {
			t.Errorf("%s: invalid URI %q: %v", r.RuleID, loc.Artifact.URI, err)
			continue
		}
		if u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/") {
			t.Errorf("%s: URI %q is not repository-relative", r.RuleID, loc.Artifact.URI)
		}
		got[r.RuleID] = strings.TrimPrefix(u.Path, "./")
		if loc.Region == nil || loc.Region.StartLine < 1 {
			t.Errorf("%s: region %+v", r.RuleID, loc.Region)
		}
		if strings.TrimSpace(r.Message.Text) == "" {
			t.Errorf("%s: empty message", r.RuleID)
		}
	}
	want := map[string]string{
		"assertions-removed": "tests/old auth#1.test.ts",
		"skip-added":         "src/ünï/código?.test.ts",
		"any-cast":           "a:b/c.ts",
		"protected-path":     "golden/a.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded paths = %v, want %v", got, want)
	}
	for _, r := range doc.Runs[0].Results {
		if r.RuleID == "assertions-removed" && !strings.Contains(r.Message.Text, "Removed: expect(x).toBe(1)") {
			t.Errorf("removed code missing from message: %q", r.Message.Text)
		}
	}
}

// TestGitHubOneLinePerAnnotation: nothing in a finding can split an
// annotation over two lines or inject a second workflow command.
func TestGitHubOneLinePerAnnotation(t *testing.T) {
	evil := "x\r\n::error file=evil.go::forged\n%0A\x1b[31m"
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", "a\nb.test.ts", 3, evil), func(f *core.Finding) {
			f.Title = evil
			f.Before = evil
			f.Hint = evil
		}),
		with(finding("skip-added", "c.test.ts", 3, "s"), func(f *core.Finding) { f.Waived, f.WaiverReason = true, evil }),
	}}
	out := renderString(t, func(w io.Writer) error { return GitHub(w, res, Options{ShowHints: true, ShowWaived: true}) })
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 annotations and a summary, got %d lines:\n%s", len(lines), out)
	}
	for _, l := range lines[:2] {
		if strings.Count(l, "::") < 2 || strings.Contains(l, "\x1b") || strings.Contains(l, "\r") {
			t.Errorf("bad annotation line %q", l)
		}
		// The data part may mention "::error", but only escaped newlines
		// precede it, so the runner never sees it at the start of a line.
		if strings.Contains(l, "%0A%0A") && strings.Contains(l, "\n") {
			t.Errorf("unescaped newline in %q", l)
		}
	}
	if !strings.HasPrefix(lines[0], "::error file=a%0Ab.test.ts,line=3,") {
		t.Errorf("file property not escaped: %q", lines[0])
	}
	if !strings.Contains(lines[0], "%250A") {
		t.Errorf("a literal %%0A in the message must be escaped as %%250A: %q", lines[0])
	}
}

func TestJSONRoundTripsHostileText(t *testing.T) {
	msg := "it.skip(\"<b>&amp;</b>\")   \x00 \xff"
	res := &engine.Result{Files: 1, Findings: []core.Finding{finding("skip-added", "a.test.ts", 1, msg)}}
	var doc struct {
		Findings []struct{ Message string }
	}
	out := render(t, func(w io.Writer) error { return JSON(w, res, Options{}) })
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	// Invalid UTF-8 becomes U+FFFD (encoding/json), everything else is kept.
	if want := strings.ToValidUTF8(msg, "�"); doc.Findings[0].Message != want {
		t.Errorf("message = %q, want %q", doc.Findings[0].Message, want)
	}
	if !bytes.Contains(out, []byte(`<b>&amp;</b>`)) {
		t.Errorf("HTML characters were escaped in JSON:\n%s", out)
	}
}

// TestMarkdownShowsControlCharacters: Markdown reports reach terminals
// (`narc scan --format markdown`) and PR comments, where a bidi override
// would make quoted code read differently from what it does. No control
// character or override from the input may come out raw.
func TestMarkdownShowsControlCharacters(t *testing.T) {
	evil := "a\x1b[2J\x07\u202eb\u2066c\u0085d\x7f"
	res := &engine.Result{
		Label:  "main → " + evil,
		Files:  1,
		Errors: []string{"rule x crashed: " + evil},
		Findings: []core.Finding{
			with(finding("skip-added", "src/"+evil+".test.ts", 3, "skipped "+evil), func(f *core.Finding) {
				f.Title = "Test " + evil
				f.Hint = "fix " + evil
				f.Evidence = "\tit.skip('x') " + evil
				f.Before = "it('x') " + evil
			}),
			with(finding("skip-added", "w.test.ts", 1, "s"), func(f *core.Finding) { f.Waived, f.WaiverReason = true, "reason "+evil }),
		},
	}
	out := renderString(t, func(w io.Writer) error {
		return Markdown(w, res, Options{ShowHints: true, Command: "narc scan", LinkBase: "https://github.com/o/r/blob/abc"})
	})
	for _, r := range out {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r < 0xa0) || isBidiControl(r) {
			t.Fatalf("raw control character %U in Markdown output:\n%q", r, out)
		}
	}
	for _, want := range []string{"^[[2J", "<U+202E>", "\tit.skip('x')"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// SARIF alert messages are shown by code scanning: same treatment.
	// (Property bags are data and stay verbatim.)
	for _, r := range decodeSARIF(t, res, testRules, Options{}).Runs[0].Results {
		if strings.ContainsAny(r.Message.Text, "\x1b\x07\u202e\u2066\u0085\x7f") {
			t.Errorf("SARIF message carries raw control characters: %q", r.Message.Text)
		}
	}
}

// TestOrderIndependent: one rule firing several times on the same line
// (removals anchored at one place) renders the same whatever the input
// order.
func TestOrderIndependent(t *testing.T) {
	mk := func(old int, before string) core.Finding {
		return with(finding("assertions-removed", "a.test.ts", 10, "assertions in \"x\" dropped 3 → 1"), func(f *core.Finding) {
			f.OldLine, f.Before = old, before
		})
	}
	fs := []core.Finding{mk(14, "expect(c).toBe(3)"), mk(12, "expect(a).toBe(1)"), mk(13, "expect(b).toBe(2)"), mk(12, "expect(a).toBe(0)")}
	var outs []string
	for k := 0; k < len(fs); k++ {
		rot := append(append([]core.Finding(nil), fs[k:]...), fs[:k]...)
		res := &engine.Result{Files: 1, Findings: rot}
		o := Options{HideDuration: true}
		outs = append(outs,
			renderString(t, func(w io.Writer) error { return Pretty(w, res, o) })+
				renderString(t, func(w io.Writer) error { return Markdown(w, res, o) })+
				renderString(t, func(w io.Writer) error { return SARIF(w, res, nil, o) }))
	}
	for k := 1; k < len(outs); k++ {
		if outs[k] != outs[0] {
			t.Fatalf("rotation %d renders differently:\n%s", k, firstDiff(outs[0], outs[k]))
		}
	}
	if i, j := strings.Index(outs[0], "12 │ - expect(a).toBe(0)"), strings.Index(outs[0], "14 │ - expect(c)"); i < 0 || j < i {
		t.Errorf("removals not in old-line order:\n%s", outs[0])
	}
}
