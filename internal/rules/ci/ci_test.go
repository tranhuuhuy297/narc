package ci_test

import (
	"testing"

	"github.com/huyth45/narc/internal/core"
	_ "github.com/huyth45/narc/internal/rules/ci"
	"github.com/huyth45/narc/internal/ruletest"
)

// The corpus has two parts: testdata/diff holds cases that only need the
// change itself, testdata/repo holds cases whose verdict depends on files
// the change does not touch (both/ sections: an unchanged workflow, the
// package.json behind `npm run ci`, the Makefile behind `make all`).

func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }

// TestCorpusPatchOnly re-runs the diff-only corpus without full file
// contents, as `narc scan --patch` does: recall may drop, but nothing may
// crash and no case may gain a rule it does not expect.
func TestCorpusPatchOnly(t *testing.T) { ruletest.RunDirPatchOnly(t, "testdata/diff") }

// TestRepoCorpusPatchOnly runs the repository-context cases as bare
// patches. Without the repository, narc cannot see that a removed check
// still runs in an unchanged workflow or behind `make all`, so a legitimate
// case may only gain ci-check-removed; nothing may crash, and cheat cases
// may not gain rules they do not expect.
func TestRepoCorpusPatchOnly(t *testing.T) {
	cases, err := ruletest.LoadDir("testdata/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases in testdata/repo")
	}
	for _, c := range cases {
		c := *c
		c.PatchOnly = true
		t.Run(c.Name, func(t *testing.T) {
			res := c.Run(nil)
			for _, e := range res.Errors {
				t.Errorf("engine error: %s", e)
			}
			allowed := map[string]bool{"ci-check-removed": len(c.Expect) == 0}
			for _, id := range ruletest.InScope(c.Expect) {
				allowed[id] = true
			}
			for _, f := range res.Active() {
				if !allowed[f.RuleID] && core.RuleByID(f.RuleID) != nil {
					t.Errorf("%s (patch-only): unexpected %s: %s", c.Path, f.RuleID, f.Message)
				}
			}
		})
	}
}
