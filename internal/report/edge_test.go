package report

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
)

// More edge cases found while hardening: protocol details of code scanning
// and GitHub Markdown, narrow terminals, and color conventions.

// TestSARIFRepeatedFingerprintsStayDistinct: fingerprints ignore line
// numbers, so the same cast added twice in one file fingerprints the same.
// Code scanning merges results with equal fingerprints into one alert, so
// each occurrence needs a distinct value, and the first keeps the plain
// fingerprint (alerts uploaded before stay matched).
func TestSARIFRepeatedFingerprintsStayDistinct(t *testing.T) {
	cast := func(line int) core.Finding {
		return with(finding("any-cast", "src/a.ts", line, "`as any` cast added"), func(f *core.Finding) { f.Evidence = "x as any" })
	}
	res := &engine.Result{Files: 2, Findings: []core.Finding{cast(10), cast(20), cast(30),
		with(finding("any-cast", "src/b.ts", 10, "`as any` cast added"), func(f *core.Finding) { f.Evidence = "x as any" })}}
	for i := range res.Findings {
		res.Findings[i].ComputeFingerprint()
	}
	plain := res.Findings[0].Fingerprint
	if res.Findings[1].Fingerprint != plain {
		t.Fatal("test premise: identical changes on different lines share a fingerprint")
	}
	doc := decodeSARIF(t, res, testRules, Options{})
	var got []string
	seen := map[string]bool{}
	for _, r := range doc.Runs[0].Results {
		fp := r.PartialFingerprints[fingerprintKey]
		if seen[fp] {
			t.Errorf("fingerprint %q is used twice; code scanning would merge the alerts", fp)
		}
		seen[fp] = true
		got = append(got, fp)
	}
	want := []string{plain, plain + ":2", plain + ":3", res.Findings[3].Fingerprint}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("fingerprints = %v, want %v", got, want)
	}
	// JSON keeps the engine's fingerprint: hooks compare it across runs.
	var j struct {
		Findings []struct{ Fingerprint string }
	}
	if err := json.Unmarshal(render(t, func(w io.Writer) error { return JSON(w, res, Options{}) }), &j); err != nil {
		t.Fatal(err)
	}
	if j.Findings[1].Fingerprint != plain {
		t.Errorf("JSON fingerprint = %q, want the engine's %q", j.Findings[1].Fingerprint, plain)
	}
}

// TestMarkdownEscapesDollarSigns: GitHub renders $...$ as math, which
// would eat the dollar signs of shell variables in a test name. Code
// spans are literal anyway and stay as they are.
func TestMarkdownEscapesDollarSigns(t *testing.T) {
	cases := map[string]string{
		`test "reads $HOME/$USER" was skipped`: `test "reads \$HOME/\$USER" was skipped`,
		"`$CI` checked before `npm test`":      "`$CI` checked before `npm test`",
		"$$x$$ and a$b$c":                      `\$\$x\$\$ and a\$b\$c`,
		"costs 5$":                             `costs 5\$`,
	}
	for in, want := range cases {
		if got := mdInline(in); got != want {
			t.Errorf("mdInline(%q) = %q, want %q", in, got, want)
		}
	}
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", "a.test.sh", 3, `test "exports $PATH:$GOPATH/bin" was skipped`), func(f *core.Finding) {
			f.Evidence = `skip "exports $PATH:$GOPATH/bin"`
		}),
		with(finding("skip-added", "b.test.sh", 3, "s"), func(f *core.Finding) { f.Waived, f.WaiverReason = true, "costs $5, saves $10" }),
	}}
	out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{HideDuration: true}) })
	for _, want := range []string{
		`test "exports \$PATH:\$GOPATH/bin" was skipped`, // prose: escaped
		"\nskip \"exports $PATH:$GOPATH/bin\"\n",         // code fence: verbatim
		`costs \$5, saves \$10`,                          // waived reason (table cell)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestPrettyWaivedLongRuleID: a waived finding of a rule with a long ID
// fits the width, and its location is never cut to nothing.
func TestPrettyWaivedLongRuleID(t *testing.T) {
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", "src/legacy/partner/sync.test.ts", 12, "s"), func(f *core.Finding) {
			f.RuleID = "an-unreasonably-long-custom-rule-identifier-that-goes-on-and-on"
			f.Waived, f.WaiverReason = true, "flaky upstream API, see #812"
		}),
		with(finding("skip-added", "src/b.test.ts", 4, "s"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, "upstream outage, see #12"
		}),
	}}
	for _, width := range []int{40, 50, 64, 80, 100} {
		for _, ascii := range []bool{false, true} {
			o := Options{Width: width, ASCII: ascii, ShowWaived: true, HideDuration: true}
			out := renderString(t, func(w io.Writer) error { return Pretty(w, res, o) })
			for i, l := range strings.Split(out, "\n") {
				if w := textWidth(l); w > width {
					t.Errorf("width=%d ascii=%v line %d is %d columns: %q", width, ascii, i+1, w, l)
				}
				if strings.HasSuffix(l, " ") {
					t.Errorf("width=%d ascii=%v line %d has trailing spaces: %q", width, ascii, i+1, l)
				}
			}
			if !strings.Contains(out, "sync.test.ts:12") {
				t.Errorf("width=%d ascii=%v: location of the long-ID waiver is missing:\n%s", width, ascii, out)
			}
			// A short ID keeps its location on the same line.
			if !strings.Contains(out, "skip-added  src/b.test.ts:4") {
				t.Errorf("width=%d ascii=%v: short-ID waiver lost its one-line layout:\n%s", width, ascii, out)
			}
		}
	}
}

// TestPrettyNarrowRemovalKeepsLocation: code removed from a test shows its
// old line number in the frame. When the headline has no room for the
// location, the new-side line must still be shown somewhere: it is where
// the reader has to look, and what an inline waiver goes on.
func TestPrettyNarrowRemovalKeepsLocation(t *testing.T) {
	removal := with(finding("assertions-removed", "src/auth/session.test.ts", 57,
		`assertions in "refreshes an expiring session" dropped 3 → 1`), func(f *core.Finding) {
		f.OldLine = 61
		f.Before = "expect(session.expiresAt).toBeGreaterThan(Date.now())"
	})
	added := with(finding("assertions-removed", "src/auth/session.test.ts", 88, "assertions dropped 2 → 1"), func(f *core.Finding) {
		f.Evidence = "expect(user).toBeDefined()"
	})
	res := &engine.Result{Files: 1, Findings: []core.Finding{removal, added}}
	for _, width := range []int{40, 44, 50, 60, 100} {
		out := renderString(t, func(w io.Writer) error { return Pretty(w, res, Options{Width: width, HideDuration: true}) })
		if !strings.Contains(out, "line 57") {
			t.Errorf("width=%d: the new-side line of the removal is gone:\n%s", width, out)
		}
		if !strings.Contains(out, "61 │ - expect(") {
			t.Errorf("width=%d: removed code lost its old line number:\n%s", width, out)
		}
		// Evidence is numbered with the finding's line: no extra line.
		if strings.Count(out, "line 88") > 1 || !strings.Contains(out, "88 │ expect(user)") {
			t.Errorf("width=%d: evidence frame:\n%s", width, out)
		}
		for i, l := range strings.Split(out, "\n") {
			if w := textWidth(l); w > width {
				t.Errorf("width=%d line %d is %d columns: %q", width, i+1, w, l)
			}
		}
	}
}

// TestColorEnabledClicolorForceZero: CLICOLOR_FORCE=0 means "not forced"
// (bixense.com/clicolors), so detection carries on rather than turning
// color off; FORCE_COLOR=0 does turn it off (supports-color).
func TestColorEnabledClicolorForceZero(t *testing.T) {
	for _, k := range []string{"NO_COLOR", "FORCE_COLOR", "CLICOLOR_FORCE", "CLICOLOR", "TERM", "GITHUB_ACTIONS"} {
		t.Setenv(k, "")
	}
	regular, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	t.Setenv("CLICOLOR_FORCE", "0")
	if ColorEnabled(regular) {
		t.Error("CLICOLOR_FORCE=0 colored a regular file")
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	if !ColorEnabled(pw) {
		t.Error("CLICOLOR_FORCE=0 turned color off in a GitHub Actions log")
	}
	t.Setenv("GITHUB_ACTIONS", "")
	if runtime.GOOS != "windows" {
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer devnull.Close()
		if !ColorEnabled(devnull) {
			t.Error("CLICOLOR_FORCE=0 turned color off on a character device")
		}
		t.Setenv("FORCE_COLOR", "0")
		if ColorEnabled(devnull) {
			t.Error("FORCE_COLOR=0 must turn color off")
		}
	}
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "yes")
	if !ColorEnabled(regular) {
		t.Error("CLICOLOR_FORCE=yes must force color")
	}
}

// TestWaiverSource: the engine records where a waiver came from. JSON
// reports it, and SARIF trusts it over re-deriving it from the config.
func TestWaiverSource(t *testing.T) {
	inlineDespiteConfig := with(finding("protected-path", "tests/golden/invoice.txt", 1, "modified"), func(f *core.Finding) {
		// The allow: entry below also matches, but the engine says the
		// narc:allow comment on the line waived it.
		f.Waived, f.WaiverReason, f.WaiverSource = true, "golden regenerated, see #40", "inline"
	})
	configNoSource := with(finding("skip-added", "legacy/a.test.ts", 3, "skipped"), func(f *core.Finding) {
		f.Waived, f.WaiverReason = true, "legacy suite, replaced in #123" // built by hand: no source
	})
	configSource := with(finding("skip-added", "src/b.test.ts", 7, "skipped"), func(f *core.Finding) {
		f.Waived, f.WaiverReason, f.WaiverSource = true, "repo-wide waiver", "config"
	})
	res := &engine.Result{Files: 3, Findings: []core.Finding{inlineDespiteConfig, configNoSource, configSource}}
	cfg := config.Default()
	cfg.Allow = []config.Allow{
		{Rule: "protected-path", Path: "tests/golden/**", Reason: "x"},
		{Rule: "skip-added", Path: "legacy/**", Reason: "legacy suite, replaced in #123"},
	}
	kinds := map[string]string{}
	for _, r := range decodeSARIF(t, res, testRules, Options{Config: cfg}).Runs[0].Results {
		kinds[r.Locations[0].Physical.Artifact.URI] = r.Suppressions[0].Kind
	}
	want := map[string]string{"tests/golden/invoice.txt": "inSource", "legacy/a.test.ts": "external", "src/b.test.ts": "external"}
	for uri, k := range want {
		if kinds[uri] != k {
			t.Errorf("%s: suppression kind %q, want %q", uri, kinds[uri], k)
		}
	}

	var doc struct {
		Findings []map[string]any
	}
	if err := json.Unmarshal(render(t, func(w io.Writer) error { return JSON(w, res, Options{}) }), &doc); err != nil {
		t.Fatal(err)
	}
	sources := map[string]any{}
	for _, f := range doc.Findings {
		sources[f["path"].(string)] = f["waiver_source"]
	}
	if sources["tests/golden/invoice.txt"] != "inline" || sources["src/b.test.ts"] != "config" || sources["legacy/a.test.ts"] != nil {
		t.Errorf("waiver_source = %v", sources)
	}
}

// TestOneLineCodeIsCapped: annotation and alert messages quote removed
// code on one line; a removed snapshot or minified line must not turn
// into a 300 KB annotation. JSON keeps the whole line.
func TestOneLineCodeIsCapped(t *testing.T) {
	huge := `{"snapshot":"` + strings.Repeat("ab", 150000) + `"}`
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("assertion-weakened", "src/__snapshots__/a.test.ts.snap", 4, "snapshot assertion replaced"), func(f *core.Finding) {
			f.OldLine = 4
			f.Before = huge
			f.Evidence = "expect(x).toBeDefined()"
		}),
	}}
	gh := renderString(t, func(w io.Writer) error { return GitHub(w, res, Options{}) })
	if len(gh) > 4096 || !strings.Contains(gh, `Before: {"snapshot":"abab`) || !strings.Contains(gh, "…") {
		t.Errorf("GitHub annotation is %d bytes:\n%.300s", len(gh), gh)
	}
	for _, r := range decodeSARIF(t, res, testRules, Options{}).Runs[0].Results {
		if len(r.Message.Text) > 4096 || !strings.Contains(r.Message.Text, "Before: {") {
			t.Errorf("SARIF message is %d bytes: %.300s", len(r.Message.Text), r.Message.Text)
		}
	}
	var doc struct{ Findings []struct{ Before string } }
	if err := json.Unmarshal(render(t, func(w io.Writer) error { return JSON(w, res, Options{}) }), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Findings[0].Before != huge {
		t.Error("JSON must keep the full removed code")
	}
}

// TestRulesFollowCoreCategoryOrder: the rule listings group categories in
// core's order even when handed rules in another order, and the Markdown
// index lists rules in the same order as the sections.
func TestRulesFollowCoreCategoryOrder(t *testing.T) {
	custom := &core.Rule{ID: "zz-custom", Title: "Custom", Category: "custom", Severity: core.Info, Summary: "A custom check."}
	shuffled := []*core.Rule{
		testRule("protected-path"), custom, testRule("coverage-lowered"), testRule("any-cast"),
		testRule("assertions-removed"), testRule("skip-added"),
	}
	order := func(out string, needles ...string) {
		t.Helper()
		last := -1
		for _, n := range needles {
			i := strings.Index(out, n)
			if i < 0 || i < last {
				t.Errorf("%q is missing or out of order (want %q):\n%s", n, needles, out)
				return
			}
			last = i
		}
	}
	table := renderString(t, func(w io.Writer) error { return RulesTable(w, shuffled, Options{}) })
	order(table, "Tests (1)", "Accounting (1)", "Escapes (1)", "CI (1)", "Integrity (1)", "Custom (1)")

	md := renderString(t, func(w io.Writer) error { return RulesMarkdown(w, shuffled) })
	index, sections, ok := strings.Cut(md, "\n## Tests")
	if !ok {
		t.Fatalf("no Tests section:\n%s", md)
	}
	ids := []string{"(#skip-added)", "(#assertions-removed)", "(#any-cast)", "(#coverage-lowered)", "(#protected-path)", "(#zz-custom)"}
	order(index, ids...)
	order(sections, "### skip-added", "## Accounting", "### assertions-removed", "## Escapes", "### any-cast",
		"## CI", "### coverage-lowered", "## Integrity", "### protected-path", "## Custom", "### zz-custom")
}

// TestExplainKeepsCodeSpansVerbatim: rule docs quote Python dunders, globs
// and regexes in code spans, where emphasis and backslash escapes do not
// apply. Reducing them like prose turned `__init__.py` into `init.py`.
func TestExplainKeepsCodeSpansVerbatim(t *testing.T) {
	r := &core.Rule{ID: "coverage-ignored", Title: "Coverage exclusion added", Severity: core.Warning, Category: "escapes",
		Docs: "Idiomatic exclusions are not reported: `if __name__ == \"__main__\":` and `def __repr__`.\n\n" +
			"Globs such as `tests/**` and `src/**` stay intact, and so does `\\d+\\.\\d+`.\n\n" +
			"**Note:** see [`narc:allow`](https://github.com/tranhuuhuy297/narc#waivers) and __really__ \\*literal\\*."}
	out := renderString(t, func(w io.Writer) error { return Explain(w, r, Options{Width: 200}) })
	for _, want := range []string{
		"`if __name__ == \"__main__\":` and `def __repr__`.",
		"Globs such as `tests/**` and `src/**` stay intact, and so does `\\d+\\.\\d+`.",
		"Note: see `narc:allow` (https://github.com/tranhuuhuy297/narc#waivers) and really *literal*.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	// Highlighting still finds the spans.
	color := renderString(t, func(w io.Writer) error { return Explain(w, r, Options{Width: 200, Color: true}) })
	if !strings.Contains(color, "\x1b["+string(sCode)+"m`if __name__ == \"__main__\":`") {
		t.Errorf("code spans are not highlighted:\n%q", color)
	}
}
