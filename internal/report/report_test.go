package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
)

func renderString(t *testing.T, fn func(io.Writer) error) string {
	t.Helper()
	return string(render(t, fn))
}

func TestNilInputs(t *testing.T) {
	var buf bytes.Buffer
	for name, err := range map[string]error{
		"Pretty":   Pretty(&buf, nil, Options{}),
		"JSON":     JSON(&buf, nil, Options{}),
		"SARIF":    SARIF(&buf, nil, testRules, Options{}),
		"Markdown": Markdown(&buf, nil, Options{}),
		"GitHub":   GitHub(&buf, nil, Options{}),
		"Explain":  Explain(&buf, nil, Options{}),
	} {
		if err == nil {
			t.Errorf("%s(nil) returned no error", name)
		}
	}
}

// TestPlainHasNoEscapes: without Color, not a single escape byte may be
// written, whatever the format.
func TestPlainHasNoEscapes(t *testing.T) {
	o := reproducible()
	outputs := map[string]string{
		"pretty":   renderString(t, func(w io.Writer) error { return Pretty(w, bustedResult(), o) }),
		"rules":    renderString(t, func(w io.Writer) error { return RulesTable(w, testRules, o) }),
		"explain":  renderString(t, func(w io.Writer) error { return Explain(w, testRule("skip-added"), o) }),
		"markdown": renderString(t, func(w io.Writer) error { return Markdown(w, bustedResult(), o) }),
		"github":   renderString(t, func(w io.Writer) error { return GitHub(w, bustedResult(), o) }),
		"json":     renderString(t, func(w io.Writer) error { return JSON(w, bustedResult(), o) }),
		"sarif":    renderString(t, func(w io.Writer) error { return SARIF(w, bustedResult(), testRules, o) }),
	}
	for name, out := range outputs {
		if strings.Contains(out, "\x1b") {
			t.Errorf("%s output contains an escape byte", name)
		}
	}
}

// TestPrettyFitsWidth: every line of the terminal output fits the width,
// including long messages, long code, long paths and the footer.
func TestPrettyFitsWidth(t *testing.T) {
	res := bustedResult()
	res.Findings = append(res.Findings, with(finding("any-cast", "packages/very-long-package-name/src/generated/clients/internal/v2/payments/refunds/handlers.ts", 9,
		"`as any` cast added"), func(f *core.Finding) { f.Evidence = "x as any" }))
	for _, width := range []int{40, 50, 64, 80, 100, 140} {
		for _, ascii := range []bool{false, true} {
			for _, color := range []bool{false, true} {
				o := reproducible()
				o.Width, o.ASCII, o.Color = width, ascii, color
				o.HideDuration = false
				outs := map[string]string{
					"pretty": renderString(t, func(w io.Writer) error { return Pretty(w, res, o) }),
					"clean":  renderString(t, func(w io.Writer) error { return Pretty(w, cleanResult(), o) }),
					"rules":  renderString(t, func(w io.Writer) error { return RulesTable(w, testRules, o) }),
				}
				if width >= 80 {
					// Explain never cuts copyable examples or the docs
					// link, so it only promises to fit common widths.
					outs["explain"] = renderString(t, func(w io.Writer) error { return Explain(w, testRule("skip-added"), o) })
				}
				for name, out := range outs {
					for i, l := range strings.Split(out, "\n") {
						if w := textWidth(l); w > width {
							t.Errorf("%s width=%d ascii=%v color=%v line %d is %d columns: %q", name, width, ascii, color, i+1, w, l)
						}
					}
				}
			}
		}
	}
}

func TestPrettyDuration(t *testing.T) {
	o := reproducible()
	o.HideDuration = false
	out := renderString(t, func(w io.Writer) error { return Pretty(w, bustedResult(), o) })
	if !strings.Contains(out, "· 42ms") {
		t.Errorf("duration missing from footer:\n%s", out)
	}
	o.HideDuration = true
	if out := renderString(t, func(w io.Writer) error { return Pretty(w, bustedResult(), o) }); strings.Contains(out, "42ms") {
		t.Errorf("HideDuration did not hide the duration")
	}
}

func TestPrettyWaivedHiddenByDefault(t *testing.T) {
	o := reproducible()
	o.ShowWaived = false
	out := renderString(t, func(w io.Writer) error { return Pretty(w, bustedResult(), o) })
	if strings.Contains(out, "flaky upstream API") {
		t.Error("waiver reason shown without ShowWaived")
	}
	if !strings.Contains(out, "2 waived (--show-waived)") {
		t.Errorf("footer does not mention the waived findings:\n%s", out)
	}
}

// TestPrettySanitizes: source code must not be able to drive the user's
// terminal (escape sequences) or hide what it does (bidi overrides).
func TestPrettySanitizes(t *testing.T) {
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", "src/a\x1b[31m.test.ts", 3, "test \"x\x1b]0;pwned\x07\" was skipped"), func(f *core.Finding) {
			f.Evidence = "it.skip('x', () => {}) \x1b[2J\u202e// }"
			// A CR inside a line could hide what follows it; a trailing CR is
			// just a CRLF line ending and must not show up as noise.
			f.Before = "it('x', () => {})\r// hidden\r\n"
		}),
	}}
	for _, color := range []bool{false, true} {
		out := renderString(t, func(w io.Writer) error { return Pretty(w, res, Options{Color: color}) })
		for _, bad := range []string{"\x1b[2J", "\x1b]0;", "\x1b[31m.test", "\u202e", "\x07", "\r"} {
			if strings.Contains(out, bad) {
				t.Errorf("color=%v: output contains %q:\n%s", color, bad, out)
			}
		}
		for _, want := range []string{"^[[2J", "<U+202E>", "^[]0;pwned^G", "{})^M// hidden"} {
			if !strings.Contains(out, want) {
				t.Errorf("color=%v: output lacks the visible form %q:\n%s", color, want, out)
			}
		}
		if strings.Contains(out, "hidden^M") {
			t.Errorf("color=%v: a CRLF line ending is shown as ^M:\n%s", color, out)
		}
	}
}

func TestPrettyNothingToAnalyze(t *testing.T) {
	out := renderString(t, func(w io.Writer) error {
		return Pretty(w, &engine.Result{Skipped: 3}, Options{HideDuration: true})
	})
	if !strings.Contains(out, "all 3 changed files were skipped") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestPrettySortsFindings(t *testing.T) {
	res := bustedResult()
	for i, j := 0, len(res.Findings)-1; i < j; i, j = i+1, j-1 {
		res.Findings[i], res.Findings[j] = res.Findings[j], res.Findings[i]
	}
	got := renderString(t, func(w io.Writer) error { return Pretty(w, res, reproducible()) })
	want := renderString(t, func(w io.Writer) error { return Pretty(w, bustedResult(), reproducible()) })
	if got != want {
		t.Error("output depends on the order of res.Findings")
	}
}

func TestJSONSchema(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(render(t, func(w io.Writer) error { return JSON(w, bustedResult(), Options{}) }), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "schema", "label", "verdict", "score", "counts", "files", "skipped", "lines", "findings", "errors", "duration_ms"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	if doc["version"] != "dev" || doc["verdict"] != "busted" || doc["duration_ms"] != float64(42) {
		t.Errorf("version=%v verdict=%v duration_ms=%v", doc["version"], doc["verdict"], doc["duration_ms"])
	}
	counts := doc["counts"].(map[string]any)
	if counts["error"] != float64(5) || counts["warning"] != float64(5) || counts["info"] != float64(1) || counts["waived"] != float64(2) {
		t.Errorf("counts = %v", counts)
	}
	findings := doc["findings"].([]any)
	if len(findings) != len(bustedResult().Findings) {
		t.Fatalf("%d findings, want %d", len(findings), len(bustedResult().Findings))
	}
	waived := 0
	for _, raw := range findings {
		f := raw.(map[string]any)
		for _, k := range []string{"rule", "title", "category", "severity", "path", "line", "message", "waived", "fingerprint"} {
			if _, ok := f[k]; !ok {
				t.Errorf("finding %v lacks %q", f["rule"], k)
			}
		}
		if f["waived"] == true {
			waived++
			if f["waiver_reason"] == "" {
				t.Errorf("waived finding %v has no reason", f["rule"])
			}
		}
	}
	if waived != 2 {
		t.Errorf("%d waived findings, want 2", waived)
	}

	clean := renderString(t, func(w io.Writer) error { return JSON(w, cleanResult(), Options{}) })
	if !strings.Contains(clean, `"findings": []`) || !strings.Contains(clean, `"errors": []`) {
		t.Errorf("empty lists must be [] not null:\n%s", clean)
	}
}

// sarifDoc mirrors the parts of SARIF 2.1.0 that GitHub code scanning
// requires, so the test fails if any of them goes missing.
type sarifDoc struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name           string `json:"name"`
				Version        string `json:"version"`
				InformationURI string `json:"informationUri"`
				Rules          []struct {
					ID               string `json:"id"`
					Name             string `json:"name"`
					ShortDescription struct{ Text string }
					FullDescription  struct{ Text string }
					Help             struct{ Text, Markdown string }
					HelpURI          string `json:"helpUri"`
					Default          struct {
						Level string `json:"level"`
					} `json:"defaultConfiguration"`
					Properties struct {
						Tags []string `json:"tags"`
					} `json:"properties"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Invocations []struct {
			ExecutionSuccessful *bool  `json:"executionSuccessful"`
			CommandLine         string `json:"commandLine"`
			Notifications       []struct {
				Level   string
				Message struct{ Text string }
			} `json:"toolExecutionNotifications"`
		} `json:"invocations"`
		OriginalURIBaseIDs map[string]struct {
			URI string `json:"uri"`
		} `json:"originalUriBaseIds"`
		Results []struct {
			RuleID    string `json:"ruleId"`
			RuleIndex *int   `json:"ruleIndex"`
			Level     string `json:"level"`
			Message   struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				Physical struct {
					Artifact struct {
						URI       string `json:"uri"`
						URIBaseID string `json:"uriBaseId"`
					} `json:"artifactLocation"`
					Region *struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
			PartialFingerprints map[string]string `json:"partialFingerprints"`
			Suppressions        []struct {
				Kind          string `json:"kind"`
				Status        string `json:"status"`
				Justification string `json:"justification"`
			} `json:"suppressions"`
		} `json:"results"`
	} `json:"runs"`
}

func decodeSARIF(t *testing.T, res *engine.Result, rules []*core.Rule, o Options) sarifDoc {
	t.Helper()
	var doc sarifDoc
	dec := json.NewDecoder(bytes.NewReader(render(t, func(w io.Writer) error { return SARIF(w, res, rules, o) })))
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("%d runs, want 1", len(doc.Runs))
	}
	return doc
}

func TestSARIFStructure(t *testing.T) {
	o := Options{Version: "v1.4.0", Root: "/home/ci/work/shop", Command: "narc scan --base main", Config: bustedConfig()}
	res := bustedResult()
	doc := decodeSARIF(t, res, testRules, o)
	if doc.Version != "2.1.0" || !strings.Contains(doc.Schema, "sarif-2.1.0") {
		t.Errorf("version=%q $schema=%q", doc.Version, doc.Schema)
	}
	run := doc.Runs[0]
	d := run.Tool.Driver
	if d.Name != "narc" || d.Version != "v1.4.0" || d.InformationURI != ProjectURL {
		t.Errorf("driver = %q %q %q", d.Name, d.Version, d.InformationURI)
	}
	if len(d.Rules) != len(testRules) {
		t.Errorf("%d rule descriptors, want %d", len(d.Rules), len(testRules))
	}
	levels := map[string]bool{"error": true, "warning": true, "note": true}
	for _, r := range d.Rules {
		if r.ID == "" || r.Name == "" || r.ShortDescription.Text == "" || r.FullDescription.Text == "" ||
			r.Help.Text == "" || r.Help.Markdown == "" || r.HelpURI == "" || !levels[r.Default.Level] || len(r.Properties.Tags) == 0 {
			t.Errorf("incomplete rule descriptor: %+v", r)
		}
	}
	if got := run.OriginalURIBaseIDs[srcRoot].URI; got != "file:///home/ci/work/shop/" {
		t.Errorf("%%SRCROOT%% = %q", got)
	}
	if len(run.Invocations) != 1 || run.Invocations[0].ExecutionSuccessful == nil ||
		run.Invocations[0].CommandLine != o.Command || len(run.Invocations[0].Notifications) != 1 {
		t.Errorf("invocations = %+v", run.Invocations)
	} else if n := run.Invocations[0].Notifications[0]; strings.Contains(n.Message.Text, "goroutine") {
		t.Errorf("notification carries the stack trace: %q", n.Message.Text)
	}

	if len(run.Results) != len(res.Findings) {
		t.Fatalf("%d results, want %d", len(run.Results), len(res.Findings))
	}
	byKind := map[string]string{}
	for _, r := range run.Results {
		if r.RuleIndex == nil || *r.RuleIndex < 0 || *r.RuleIndex >= len(d.Rules) || d.Rules[*r.RuleIndex].ID != r.RuleID {
			t.Errorf("%s: ruleIndex does not point at its rule", r.RuleID)
		}
		if !levels[r.Level] || r.Message.Text == "" {
			t.Errorf("%s: level=%q message=%q", r.RuleID, r.Level, r.Message.Text)
		}
		if len(r.Locations) != 1 {
			t.Fatalf("%s: %d locations", r.RuleID, len(r.Locations))
		}
		loc := r.Locations[0].Physical
		if loc.Artifact.URI == "" || strings.HasPrefix(loc.Artifact.URI, "/") || loc.Artifact.URIBaseID != srcRoot {
			t.Errorf("%s: artifactLocation = %+v", r.RuleID, loc.Artifact)
		}
		if loc.Region == nil || loc.Region.StartLine < 1 {
			t.Errorf("%s: region = %+v (startLine must be >= 1)", r.RuleID, loc.Region)
		}
		if r.PartialFingerprints[fingerprintKey] == "" {
			t.Errorf("%s: no partial fingerprint", r.RuleID)
		}
		for _, s := range r.Suppressions {
			byKind[r.RuleID] = s.Kind
			if s.Status != "accepted" || s.Justification == "" {
				t.Errorf("%s: suppression = %+v", r.RuleID, s)
			}
		}
	}
	// The protected-path finding was waived in .narc.yml, the skip inline.
	if byKind["protected-path"] != "external" || byKind["skip-added"] != "inSource" || len(byKind) != 2 {
		t.Errorf("suppression kinds = %v", byKind)
	}
	// File-level finding (deleted test file) still points at line 1.
	for _, r := range run.Results {
		if r.RuleID == "test-file-deleted" && r.Locations[0].Physical.Region.StartLine != 1 {
			t.Errorf("file-level finding startLine = %d", r.Locations[0].Physical.Region.StartLine)
		}
	}
}

func TestSARIFSuppressionKindWithoutConfig(t *testing.T) {
	res := &engine.Result{Findings: []core.Finding{
		with(finding("protected-path", "golden/a.txt", 0, "protected file was deleted"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, "regenerated by the release script"
		}),
		with(finding("skip-added", "a.test.ts", 4, "skipped"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, "upstream outage, see #12"
		}),
	}}
	doc := decodeSARIF(t, res, testRules, Options{})
	kinds := map[string]string{}
	for _, r := range doc.Runs[0].Results {
		kinds[r.RuleID] = r.Suppressions[0].Kind
	}
	// Inline waivers need a line to sit on, so a waived file-level
	// finding must come from .narc.yml.
	if kinds["protected-path"] != "external" || kinds["skip-added"] != "inSource" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestSARIFDescribesUnknownRules(t *testing.T) {
	res := &engine.Result{Findings: []core.Finding{
		{RuleID: "zz-custom", Title: "Custom check", Category: "custom", Severity: core.Warning, Path: "a.go", Line: 3, Message: "custom"},
		with(finding("skip-added", "a_test.go", 7, "t.Skip added"), func(*core.Finding) {}),
	}}
	doc := decodeSARIF(t, res, []*core.Rule{testRule("skip-added"), nil, testRule("skip-added")}, Options{})
	rules := doc.Runs[0].Tool.Driver.Rules
	if len(rules) != 2 || rules[0].ID != "skip-added" || rules[1].ID != "zz-custom" || rules[1].ShortDescription.Text != "Custom check" {
		t.Fatalf("rules = %+v", rules)
	}
	for _, r := range doc.Runs[0].Results {
		if rules[*r.RuleIndex].ID != r.RuleID {
			t.Errorf("%s has ruleIndex %d", r.RuleID, *r.RuleIndex)
		}
	}
	// No rules at all still yields a valid, empty rules array.
	var buf bytes.Buffer
	if err := SARIF(&buf, &engine.Result{}, nil, Options{}); err != nil || !strings.Contains(buf.String(), `"rules": []`) || !strings.Contains(buf.String(), `"results": []`) {
		t.Errorf("empty SARIF: err=%v\n%s", err, buf.String())
	}
}

func TestSARIFURIs(t *testing.T) {
	cases := []struct{ path, root, want string }{
		{"src/a.ts", "", "src/a.ts"},
		{"src/my file.ts", "", "src/my%20file.ts"},
		{"./src/a.ts", "", "src/a.ts"},
		{"/repo/src/a.ts", "/repo", "src/a.ts"},
		{"docs/100%.md", "", "docs/100%25.md"},
	}
	for _, c := range cases {
		if got := artifactURI(c.path, c.root); got != c.want {
			t.Errorf("artifactURI(%q, %q) = %q, want %q", c.path, c.root, got, c.want)
		}
	}
	if runtime.GOOS != "windows" {
		if got := rootURI("/home/ci/my repo"); got != "file:///home/ci/my%20repo/" {
			t.Errorf("rootURI = %q", got)
		}
	}
	if rootURI("") != "" {
		t.Error("empty root must give no base URI")
	}
}

func TestMarkdownEscaping(t *testing.T) {
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		with(finding("skip-added", "src/<b>x</b>.test.ts", 3,
			"test skipped with @pytest.mark.skip; ping @octocat, mail dev@example.com, <img src=x onerror=alert(1)> **bold** `<kept>`"),
			func(f *core.Finding) { f.Evidence = "```\n@pytest.mark.skip" }),
		with(finding("protected-path", "a|b.txt", 1, "modified"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, "ok | approved by @alice"
		}),
	}}
	out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{}) })
	if !strings.HasPrefix(out, MarkdownMarker+"\n") {
		t.Errorf("report does not start with the sticky-comment marker")
	}
	for _, want := range []string{
		"<code>@pytest.mark.skip</code>", "<code>@octocat</code>", "dev@example.com", `\<img src=x onerror=alert(1)\>`, `\*\*bold\*\*`, "`<kept>`",
		"<code>src/&lt;b&gt;x&lt;/b&gt;.test.ts</code>", // HTML-escaped inside <summary>
		"````typescript\n```\n@pytest.mark.skip\n````",  // fence longer than the code's backticks
		`ok \| approved by <code>@alice</code>`,         // table cell
		"`a\\|b.txt:1`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if mention := regexp.MustCompile(`(^|[^>])@octocat|(^|[^\\])<img`); mention.MatchString(out) {
		t.Errorf("unescaped mention or HTML:\n%s", out)
	}
}

func TestMarkdownBudget(t *testing.T) {
	res := &engine.Result{Files: 900}
	long := strings.Repeat("assertions in a very descriptive test name dropped ", 4)
	for i := 0; i < 3000; i++ {
		res.Findings = append(res.Findings, with(finding("assertions-removed", fmt.Sprintf("src/module%03d/thing.test.ts", i/5), i%5+1, long),
			func(f *core.Finding) { f.Before = "expect(result.value).toEqual(expected.value)" }))
	}
	out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{ShowHints: true}) })
	if len(out) > 65536 {
		t.Errorf("report is %d bytes, over GitHub's comment limit", len(out))
	}
	if !strings.Contains(out, "more findings not shown") {
		t.Errorf("truncation note missing")
	}
	if strings.Count(out, "<details") != strings.Count(out, "</details>") {
		t.Errorf("unbalanced <details> after truncation")
	}
}

// TestMarkdownBudgetOneFile: the budget holds even when every finding is
// in the same file.
func TestMarkdownBudgetOneFile(t *testing.T) {
	res := &engine.Result{Files: 1}
	for i := 0; i < 2000; i++ {
		res.Findings = append(res.Findings, with(finding("any-cast", "src/generated_client.ts", i+1, strings.Repeat("`as any` cast added ", 5)),
			func(f *core.Finding) { f.Evidence = "const x = y as any;" }))
	}
	for i := 0; i < 80; i++ {
		res.Findings = append(res.Findings, with(finding("skip-added", fmt.Sprintf("legacy/t%02d.test.ts", i), 1, "skipped"), func(f *core.Finding) {
			f.Waived, f.WaiverReason = true, strings.Repeat("a very long justification ", 100)
		}))
	}
	out := renderString(t, func(w io.Writer) error { return Markdown(w, res, Options{ShowHints: true}) })
	if len(out) > 65536 {
		t.Errorf("report is %d bytes, over GitHub's comment limit", len(out))
	}
	if !strings.Contains(out, "more findings not shown") || !strings.Contains(out, "more waived findings |") {
		t.Errorf("truncation notes missing")
	}
	if strings.Count(out, "<details") != strings.Count(out, "</details>") {
		t.Errorf("unbalanced <details> after truncation")
	}
}

func TestGitHubEscaping(t *testing.T) {
	if got := ghData("100% done\r\nnext"); got != "100%25 done%0D%0Anext" {
		t.Errorf("ghData = %q", got)
	}
	if got := ghProperty("a:b,c%\n"); got != "a%3Ab%2Cc%25%0A" {
		t.Errorf("ghProperty = %q", got)
	}
	res := &engine.Result{Files: 1, Findings: []core.Finding{
		finding("skip-added", "dir,with:odd/a.test.ts", 3, "skipped\n::add-mask::secret"),
		finding("test-file-deleted", "tests/old_test.py", 0, "deleted"),
	}}
	out := renderString(t, func(w io.Writer) error { return GitHub(w, res, Options{}) })
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 annotations and a summary line, got:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "::error file=dir%2Cwith%3Aodd/a.test.ts,line=3,title=narc%3A Test skipped (skip-added)::skipped%0A::add-mask::secret") {
		t.Errorf("annotation = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "::warning file=tests/old_test.py,title=") {
		t.Errorf("file-level annotation must omit line: %q", lines[1])
	}
	if strings.HasPrefix(lines[2], "::") {
		t.Errorf("summary looks like a workflow command: %q", lines[2])
	}
}

func TestColorEnabled(t *testing.T) {
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

	type colorCase struct {
		env  map[string]string
		f    *os.File
		want bool
	}
	cases := []colorCase{
		{nil, regular, false},
		{nil, pw, false},
		{nil, nil, false},
		{map[string]string{"FORCE_COLOR": "1"}, regular, true},
		{map[string]string{"FORCE_COLOR": "0"}, regular, false},
		{map[string]string{"FORCE_COLOR": "false"}, regular, false},
		{map[string]string{"CLICOLOR_FORCE": "1"}, pw, true},
		{map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, regular, false},
		{map[string]string{"GITHUB_ACTIONS": "true"}, pw, true},
		{map[string]string{"GITHUB_ACTIONS": "true"}, regular, false},
		{map[string]string{"GITHUB_ACTIONS": "true", "TERM": "dumb"}, pw, false},
	}
	if runtime.GOOS != "windows" {
		// /dev/null is a character device: the isatty check says yes.
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		defer devnull.Close()
		cases = append(cases,
			colorCase{nil, devnull, true},
			colorCase{map[string]string{"TERM": "dumb"}, devnull, false},
			colorCase{map[string]string{"CLICOLOR": "0"}, devnull, false},
			colorCase{map[string]string{"NO_COLOR": "yes"}, devnull, false},
		)
	}
	for i, c := range cases {
		for k, v := range c.env {
			os.Setenv(k, v)
		}
		if got := ColorEnabled(c.f); got != c.want {
			t.Errorf("case %d (%v): ColorEnabled = %v, want %v", i, c.env, got, c.want)
		}
		for k := range c.env {
			os.Setenv(k, "")
		}
	}
}

func TestTextWidth(t *testing.T) {
	cases := map[string]int{
		"abc":                  3,
		"🚨 BUSTED":             9,
		"✅":                    2,
		"✖ ▲ ● ○ ↳ │ ─ ▰ ▱":    17,
		"日本語":                  6,
		"e\u0301":              1, // combining accent
		"\x1b[1;31mred\x1b[0m": 3,
	}
	for s, want := range cases {
		if got := textWidth(s); got != want {
			t.Errorf("textWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestWrapAndTruncate(t *testing.T) {
	got := wrap("the quick brown fox jumps over the lazy dog", 10)
	want := []string{"the quick", "brown fox", "jumps over", "the lazy", "dog"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrap = %q", got)
	}
	if got := wrap("supercalifragilistic", 8); strings.Join(got, "|") != "supercal|ifragili|stic" {
		t.Errorf("long word wrap = %q", got)
	}
	if got := wrap("", 8); len(got) != 1 || got[0] != "" {
		t.Errorf("empty wrap = %q", got)
	}
	if got := wrap("日本語", 1); strings.Join(got, "|") != "日|本|語" {
		t.Errorf("wide rune wider than the line = %q", got)
	}
	if got := truncate("expect(user).toEqual(admin)", 12, "…"); got != "expect(user…" || textWidth(got) != 12 {
		t.Errorf("truncate = %q", got)
	}
	if got := truncateLeft("src/very/long/path/file.ts", 12, "…"); got != "…ath/file.ts" {
		t.Errorf("truncateLeft = %q", got)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain":          "plain",
		"a\tb":           "a  b",
		"\x1b[2J":        "^[[2J",
		"del\x7f":        "del^?",
		"x\u202ey":       "x<U+202E>y",
		"line\nbreak":    "line^Jbreak",
		"caf\u00e9 → ok": "caf\u00e9 → ok",
	}
	for in, want := range cases {
		if got := sanitize(in, false); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitize("a\nb", true); got != "a\nb" {
		t.Errorf("keepNewlines: %q", got)
	}
}

// TestInlineCodeAcrossLines: a code span wrapped onto the next line stays
// highlighted, and the text after it does not.
func TestInlineCodeAcrossLines(t *testing.T) {
	p := painter(true)
	in := new(bool)
	l1 := p.code("was `npm test", sNone, in)
	l2 := p.code("|| true` now", sNone, in)
	if l1 != "was \x1b[38;5;137m`npm test\x1b[0m" {
		t.Errorf("line 1 = %q", l1)
	}
	if l2 != "\x1b[38;5;137m|| true`\x1b[0m now" {
		t.Errorf("line 2 = %q", l2)
	}
	if *in {
		t.Error("span still open")
	}
}

func TestMarkdownRuleDocsRenderer(t *testing.T) {
	r := &core.Rule{ID: "x", Title: "X", Severity: core.Info, Docs: "## Example\n\n~~~~py\n# not a heading\n~~~~\n\n1. first [link](https://e.x)\n2. __second__\n\n| a | b |"}
	out := renderString(t, func(w io.Writer) error { return Explain(w, r, Options{ASCII: true}) })
	for _, want := range []string{"  Example\n", "  | # not a heading\n", "  1. first link (https://e.x)\n", "  2. second\n", "  | a | b |\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "<1ms",
		42 * time.Millisecond:   "42ms",
		1340 * time.Millisecond: "1.3s",
		95 * time.Second:        "1m35s",
	}
	for d, want := range cases {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
