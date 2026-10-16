package report

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

// checkGolden compares got with testdata/<name>.golden, or rewrites the
// file when the test runs with -update.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/report -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (run with -update to accept):\n%s", path, firstDiff(string(want), string(got)))
	}
}

// firstDiff describes the first differing line, quoted so escape codes
// and trailing spaces are visible.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n  want %q\n   got %q", i+1, w, g)
		}
	}
	return "(no line differs; check trailing newlines)"
}

func render(t *testing.T, fn func(io.Writer) error) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// reproducible are the options goldens render with: no duration, a fixed
// version, hints and waivers on (as `narc scan` shows them).
func reproducible() Options {
	return Options{ShowHints: true, ShowWaived: true, HideDuration: true, Version: "1.4.0",
		Command: "narc scan --base main", Config: bustedConfig()}
}

func TestGolden(t *testing.T) {
	opts := reproducible()
	color := opts
	color.Color = true
	narrow := opts
	narrow.Width = 60
	ascii := opts
	ascii.ASCII = true
	quiet := opts
	quiet.ShowHints, quiet.ShowWaived = false, false
	sarif := opts
	sarif.Root = "/home/ci/work/shop"
	linked := opts
	linked.LinkBase = "https://github.com/acme/shop/blob/4f2c9e1"

	cases := []struct {
		name string
		fn   func(io.Writer) error
	}{
		{"pretty_busted", func(w io.Writer) error { return Pretty(w, bustedResult(), opts) }},
		{"pretty_busted_color", func(w io.Writer) error { return Pretty(w, bustedResult(), color) }},
		{"pretty_busted_narrow", func(w io.Writer) error { return Pretty(w, bustedResult(), narrow) }},
		{"pretty_busted_ascii", func(w io.Writer) error { return Pretty(w, bustedResult(), ascii) }},
		{"pretty_suspicious_quiet", func(w io.Writer) error { return Pretty(w, suspiciousResult(), quiet) }},
		{"pretty_suspicious_color", func(w io.Writer) error { return Pretty(w, suspiciousResult(), color) }},
		{"pretty_clean", func(w io.Writer) error { return Pretty(w, cleanResult(), opts) }},
		{"pretty_clean_color", func(w io.Writer) error { return Pretty(w, cleanResult(), color) }},
		{"json_busted", func(w io.Writer) error { return JSON(w, bustedResult(), opts) }},
		{"json_clean", func(w io.Writer) error { return JSON(w, cleanResult(), opts) }},
		{"sarif_busted", func(w io.Writer) error { return SARIF(w, bustedResult(), testRules, sarif) }},
		{"markdown_busted", func(w io.Writer) error { return Markdown(w, bustedResult(), linked) }},
		{"markdown_suspicious", func(w io.Writer) error { return Markdown(w, suspiciousResult(), quiet) }},
		{"markdown_clean", func(w io.Writer) error { return Markdown(w, cleanResult(), opts) }},
		{"github_busted", func(w io.Writer) error { return GitHub(w, bustedResult(), opts) }},
		{"rules_table", func(w io.Writer) error { return RulesTable(w, testRules, Options{}) }},
		{"rules_table_color", func(w io.Writer) error { return RulesTable(w, testRules, Options{Color: true}) }},
		{"rules_markdown", func(w io.Writer) error { return RulesMarkdown(w, testRules) }},
		{"explain_skip_added", func(w io.Writer) error { return Explain(w, testRule("skip-added"), Options{}) }},
		{"explain_skip_added_color", func(w io.Writer) error { return Explain(w, testRule("skip-added"), Options{Color: true}) }},
		{"explain_waiver_missing_reason", func(w io.Writer) error {
			return Explain(w, testRule("waiver-missing-reason"), Options{Width: 72})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkGolden(t, c.name, render(t, c.fn)) })
	}
}
