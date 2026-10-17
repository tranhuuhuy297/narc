// Package ruletest runs narc's fixture corpus.
//
// A case file (*.case) looks like this:
//
//	# Agent skipped the failing test instead of fixing it.
//	expect: skip-added
//	-- old/src/auth.test.ts --
//	it('rejects expired tokens', () => { ... })
//	-- new/src/auth.test.ts --
//	it.skip('rejects expired tokens', () => { ... })
//
// Header lines starting with '#' describe the case. "expect:" lists the rule
// IDs that must fire (comma-separated; "clean" or empty for none). An ID may
// carry a severity, "any-cast@info", which must be the highest severity
// among that rule's active findings. Files
// that exist only under old/ are deletions, only under new/ are additions.
// An optional "-- .narc.yml --" section provides configuration, and the
// header "patch-only: true" analyzes the case without full file contents
// (as with `narc scan --patch`). "message:" gives the change a commit
// message (one line; "\n" stands for a line break), as `narc audit` and
// `narc scan <sha>` do.
package ruletest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/engine"
)

// Case is one corpus entry.
type Case struct {
	Path      string
	Name      string
	Desc      string
	Expect    []string
	ExpectSev map[string]core.Severity
	Old, New  map[string][]byte
	Config    *config.Config
	PatchOnly bool
	Message   string
}

// Load parses a case file.
func Load(path string) (*Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, string(b))
}

// Parse parses case file content.
func Parse(path, src string) (*Case, error) {
	c := &Case{Path: path, Name: strings.TrimSuffix(filepath.Base(path), ".case"),
		Old: map[string][]byte{}, New: map[string][]byte{}, ExpectSev: map[string]core.Severity{}}
	lines := strings.SplitAfter(src, "\n")
	i := 0
	var desc []string
	for ; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], "\r\n")
		if strings.HasPrefix(l, "-- ") && strings.HasSuffix(l, " --") {
			break
		}
		switch {
		case strings.HasPrefix(l, "#"):
			desc = append(desc, strings.TrimSpace(strings.TrimPrefix(l, "#")))
		case strings.HasPrefix(l, "expect:"):
			for _, id := range strings.Split(strings.TrimPrefix(l, "expect:"), ",") {
				id = strings.TrimSpace(id)
				if i := strings.IndexByte(id, '@'); i >= 0 {
					sev, err := core.ParseSeverity(id[i+1:])
					if err != nil || sev == core.Off {
						return nil, fmt.Errorf("%s:%d: bad severity in %q", path, i+1, id)
					}
					id = id[:i]
					c.ExpectSev[id] = sev
				}
				if id != "" && id != "clean" && id != "none" {
					c.Expect = append(c.Expect, id)
				}
			}
		case strings.HasPrefix(l, "message:"):
			c.Message = strings.ReplaceAll(strings.TrimSpace(strings.TrimPrefix(l, "message:")), `\n`, "\n")
		case strings.HasPrefix(l, "patch-only:"):
			c.PatchOnly = strings.TrimSpace(strings.TrimPrefix(l, "patch-only:")) == "true"
		case strings.TrimSpace(l) == "":
		default:
			return nil, fmt.Errorf("%s:%d: unexpected header line %q", path, i+1, l)
		}
	}
	c.Desc = strings.Join(desc, " ")
	var name string
	var buf strings.Builder
	flush := func() error {
		if name == "" {
			return nil
		}
		content := []byte(buf.String())
		switch {
		case name == ".narc.yml":
			cfg, err := config.Parse(content)
			if err != nil {
				return fmt.Errorf("%s: config: %w", path, err)
			}
			c.Config = cfg
		case strings.HasPrefix(name, "old/"):
			c.Old[strings.TrimPrefix(name, "old/")] = content
		case strings.HasPrefix(name, "new/"):
			c.New[strings.TrimPrefix(name, "new/")] = content
		case strings.HasPrefix(name, "both/"):
			// unchanged context file (rarely needed)
			p := strings.TrimPrefix(name, "both/")
			c.Old[p], c.New[p] = content, content
		default:
			return fmt.Errorf("%s: section %q must start with old/, new/ or both/", path, name)
		}
		buf.Reset()
		return nil
	}
	for ; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimRight(l, "\r\n")
		if strings.HasPrefix(t, "-- ") && strings.HasSuffix(t, " --") && len(t) > 6 {
			if err := flush(); err != nil {
				return nil, err
			}
			name = strings.TrimSpace(t[3 : len(t)-3])
			continue
		}
		buf.WriteString(l)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(c.Old) == 0 && len(c.New) == 0 {
		return nil, fmt.Errorf("%s: no file sections", path)
	}
	sort.Strings(c.Expect)
	return c, nil
}

// LoadDir loads all *.case files under dir.
func LoadDir(dir string) ([]*Case, error) {
	var cases []*Case
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".case") {
			return nil
		}
		c, err := Load(p)
		if err != nil {
			return err
		}
		cases = append(cases, c)
		return nil
	})
	return cases, err
}

// Changeset builds the changeset for a case.
func (c *Case) Changeset() *core.Changeset {
	paths := map[string]bool{}
	for p := range c.Old {
		paths[p] = true
	}
	for p := range c.New {
		paths[p] = true
	}
	var sorted []string
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	cs := &core.Changeset{Label: c.Name, Message: c.Message}
	for _, p := range sorted {
		o, hasO := c.Old[p]
		n, hasN := c.New[p]
		if hasO && hasN && string(o) == string(n) {
			continue
		}
		fd := diff.Compute(p, p, o, n, hasO, hasN, diff.DefaultContext)
		if len(fd.Hunks) == 0 && !fd.Binary && hasO && hasN {
			continue
		}
		cs.Files = append(cs.Files, fd)
	}
	if !c.PatchOnly {
		cs.Source = &core.MemSource{OldFiles: c.Old, NewFiles: c.New}
	}
	return cs
}

// Run analyzes the case with the given rules (nil = all registered).
func (c *Case) Run(rules []*core.Rule) *engine.Result {
	cfg := c.Config
	if cfg == nil {
		cfg = config.Default()
	}
	return engine.Run(c.Changeset(), engine.Options{Config: cfg, Rules: rules})
}

// Got returns the sorted, de-duplicated IDs of active findings.
func Got(res *engine.Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range res.Active() {
		if !seen[f.RuleID] {
			seen[f.RuleID] = true
			out = append(out, f.RuleID)
		}
	}
	sort.Strings(out)
	return out
}

// InScope filters rule IDs to those registered in the current binary, so a
// rule package's tests ignore expectations that belong to other packages.
func InScope(ids []string) []string {
	var out []string
	for _, id := range ids {
		if core.RuleByID(id) != nil {
			out = append(out, id)
		}
	}
	return out
}

// RunDir runs every case under dir as a subtest. Expectations about rules
// that are not registered in this test binary are ignored.
func RunDir(t *testing.T, dir string) {
	t.Helper()
	cases, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatalf("no cases in %s", dir)
	}
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			res := c.Run(nil)
			for _, e := range res.Errors {
				t.Errorf("engine error: %s", e)
			}
			want := InScope(c.Expect)
			got := Got(res)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s\n%s\nwant: %v\ngot:  %v\nfindings:\n%s\ndiff:\n%s",
					c.Path, c.Desc, want, got, describe(res), diff.Format(c.Changeset().Files))
				return
			}
			for id, sev := range c.ExpectSev {
				if core.RuleByID(id) == nil {
					continue
				}
				if max := maxSeverity(res, id); max != sev {
					t.Errorf("%s: %s: want severity %s, got %s\nfindings:\n%s", c.Path, id, sev, max, describe(res))
				}
			}
		})
	}
}

func maxSeverity(res *engine.Result, id string) core.Severity {
	max := core.Off
	for _, f := range res.Active() {
		if f.RuleID == id && f.Severity > max {
			max = f.Severity
		}
	}
	return max
}

// RunDirPatchOnly re-runs every case under dir without full file contents
// (as with `narc scan --patch`). Recall may drop, but it must never crash,
// legitimate cases must stay clean, and cheat cases must not gain rule IDs
// they did not expect.
func RunDirPatchOnly(t *testing.T, dir string) {
	t.Helper()
	cases, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		c := *c
		c.PatchOnly = true
		t.Run(c.Name, func(t *testing.T) {
			res := c.Run(nil)
			for _, e := range res.Errors {
				t.Errorf("engine error: %s", e)
			}
			want := map[string]bool{}
			for _, id := range InScope(c.Expect) {
				want[id] = true
			}
			for _, id := range Got(res) {
				if !want[id] {
					t.Errorf("%s (patch-only): unexpected %s\nfindings:\n%s", c.Path, id, describe(res))
				}
			}
		})
	}
}

func describe(res *engine.Result) string {
	var b strings.Builder
	for _, f := range res.Findings {
		w := ""
		if f.Waived {
			w = " (waived: " + f.WaiverReason + ")"
		}
		fmt.Fprintf(&b, "  [%s] %s %s — %s%s\n      evidence=%q before=%q\n", f.Severity, f.RuleID, f.Location(), f.Message, w, f.Evidence, f.Before)
	}
	if b.Len() == 0 {
		return "  (none)\n"
	}
	return b.String()
}

// Score is a confusion count.
type Score struct{ TP, FP, FN int }

// Precision returns TP/(TP+FP), or 1 when nothing was reported.
func (s Score) Precision() float64 {
	if s.TP+s.FP == 0 {
		return 1
	}
	return float64(s.TP) / float64(s.TP+s.FP)
}

// Recall returns TP/(TP+FN), or 1 when nothing was expected.
func (s Score) Recall() float64 {
	if s.TP+s.FN == 0 {
		return 1
	}
	return float64(s.TP) / float64(s.TP+s.FN)
}

// Evaluate scores cases per rule (case-level: a rule is a TP for a case if it
// was expected and fired).
func Evaluate(cases []*Case) (total Score, perRule map[string]*Score, cleanCases, cleanCorrect int) {
	perRule = map[string]*Score{}
	get := func(id string) *Score {
		if perRule[id] == nil {
			perRule[id] = &Score{}
		}
		return perRule[id]
	}
	for _, c := range cases {
		res := c.Run(nil)
		want := map[string]bool{}
		for _, id := range InScope(c.Expect) {
			want[id] = true
		}
		got := map[string]bool{}
		for _, id := range Got(res) {
			got[id] = true
		}
		if len(want) == 0 {
			cleanCases++
			if len(got) == 0 {
				cleanCorrect++
			}
		}
		for id := range want {
			if got[id] {
				get(id).TP++
				total.TP++
			} else {
				get(id).FN++
				total.FN++
			}
		}
		for id := range got {
			if !want[id] {
				get(id).FP++
				total.FP++
			}
		}
	}
	return
}
