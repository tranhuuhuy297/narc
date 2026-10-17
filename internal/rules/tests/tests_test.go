package tests_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
	_ "github.com/tranhuuhuy297/narc/internal/rules/tests"
	"github.com/tranhuuhuy297/narc/internal/ruletest"
)

var ruleIDs = []string{"skip-added", "focus-added", "tautology", "assertion-weakened", "assertion-commented", "retry-added"}

func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }

func loadCase(t *testing.T, name string) *ruletest.Case {
	t.Helper()
	c, err := ruletest.Load(filepath.Join("testdata", name+".case"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestSeverity checks the severity nuances that the corpus format cannot
// express: environment-gated and issue-linked skips are warnings, and so on.
func TestSeverity(t *testing.T) {
	cases := []struct {
		name, rule string
		want       core.Severity
	}{
		{"skip-added-jest-it-skip", "skip-added", core.Error},
		{"skip-added-go-skip-on-error", "skip-added", core.Error},
		{"skip-added-pytest-skipif-true", "skip-added", core.Error},
		{"skip-added-go-short-mode", "skip-added", core.Warning},
		{"skip-added-pytest-skipif-platform", "skip-added", core.Warning},
		{"skip-added-vitest-skipif-platform", "skip-added", core.Warning},
		{"skip-added-issue-linked", "skip-added", core.Warning},
		{"skip-added-jest-todo-replaces-test", "skip-added", core.Warning},
		{"focus-added-jest-it-only", "focus-added", core.Error},
		{"tautology-pytest-assert-true", "tautology", core.Error},
		{"assertion-weakened-jest-toequal-todefined", "assertion-weakened", core.Warning},
		{"assertion-commented-jest", "assertion-commented", core.Error},
		{"assertion-commented-issue-linked", "assertion-commented", core.Warning},
		{"retry-added-jest-retrytimes", "retry-added", core.Warning},
		{"skip-added-pytest-imported-mark", "skip-added", core.Error},
		{"skip-added-xunit-multiline-attribute", "skip-added", core.Error},
		{"skip-added-node-test-options-object", "skip-added", core.Error},
		{"skip-added-pytest-conftest-runslow", "skip-added", core.Warning},
		{"skip-added-go-table-skip-flag", "skip-added", core.Warning},
		{"assertion-commented-replaced-expectation", "assertion-commented", core.Warning},
	}
	for _, c := range cases {
		res := loadCase(t, c.name).Run(nil)
		found := false
		for _, f := range res.Active() {
			if f.RuleID != c.rule {
				continue
			}
			found = true
			if f.Severity != c.want {
				t.Errorf("%s: %s severity = %s, want %s (%s)", c.name, c.rule, f.Severity, c.want, f.Message)
			}
		}
		if !found {
			t.Errorf("%s: no %s finding", c.name, c.rule)
		}
	}
}

// TestMessages checks that findings say exactly what happened.
func TestMessages(t *testing.T) {
	cases := []struct {
		name, rule, want string
	}{
		{"skip-added-jest-it-skip", "skip-added", `test "rejects expired tokens" is now skipped with it.skip`},
		{"skip-added-pytest-mark-skip", "skip-added", `test_partial_refund is now skipped with @pytest.mark.skip (reason: "flaky")`},
		{"skip-added-go-t-skip", "skip-added", `TestUploadResumes now skips itself with t.Skip`},
		{"skip-added-go-short-mode", "skip-added", "only when testing.Short() (environment-gated)"},
		{"skip-added-go-skip-on-error", "skip-added", "whenever err != nil"},
		{"focus-added-jest-it-only", "focus-added", `test "creates an invoice" is focused with it.only`},
		{"tautology-rust-assert-eq-same", "tautology", "assert_eq!(total, total) can never fail: it compares total with itself (in sums_line_items)"},
		{"tautology-python-tuple", "tautology", "a non-empty tuple is always truthy"},
		{"assertion-weakened-jest-toequal-todefined", "assertion-weakened", "expect(invoice.total).toEqual(107) → expect(invoice.total).toBeDefined()"},
		{"assertion-weakened-pytest-raises-exception", "assertion-weakened", "expected exception ValueError widened to Exception"},
		{"assertion-weakened-jest-tobecloseto-digits", "assertion-weakened", "toBeCloseTo digits 5 → 1"},
		{"assertion-weakened-pytest-approx-rel", "assertion-weakened", "pytest.approx rel default 1e-06 → 0.05"},
		{"assertion-commented-python", "assertion-commented", "assertion commented out: assert invoice.total == Decimal(\"107.00\") in test_invoice_total_includes_vat"},
		{"retry-added-playwright-config-raised", "retry-added", "playwright.config.ts: retries raised 0 → 3 (retries: 3)"},
		{"retry-added-pytest-flaky", "retry-added", "test_webhook_delivery now retries failures up to 3 times (@pytest.mark.flaky(reruns=3))"},
		{"focus-added-mocha-continuation-line", "focus-added", `suite "Queue persistence" is focused with describe.only`},
		{"focus-added-jasmine-fit-multiline", "focus-added", `test "renders one card per widget after the data loads" is focused with fit`},
		{"skip-added-node-test-options-object", "skip-added", `test "retries a webhook that answers 503 and then 200" is now skipped with skip: true`},
		{"skip-added-xunit-multiline-attribute", "skip-added", `test PartialRefund is now skipped with [Fact(Skip = ...)] (reason: "rounding differs on CI")`},
		{"skip-added-go-table-skip-flag", "skip-added", "only when tc.skip (a skip flag in the test data)"},
		{"skip-added-pytest-conftest-runslow", "skip-added", "pytest_collection_modifyitems now creates a pytest.mark.skip marker"},
		{"assertion-weakened-jest-expect-any", "assertion-weakened", "the expected value became expect.any(Number), which accepts almost anything"},
		{"assertion-weakened-jest-tobe-true-comparison", "assertion-weakened", "expect(hits).toHaveLength(3) → expect(hits.length > 0).toBe(true)"},
		{"assertion-commented-replaced-expectation", "assertion-commented", "(replaced by assert q.amount == 27.40)"},
		{"retry-added-jest-setup-file", "retry-added", "every test that loads jest.setup.ts now retries failures up to 3 times"},
		{"retry-added-vitest-trailing-options", "retry-added", `test "uploads pending changes" now retries failures up to 3 times ({ retry: 3 })`},
		{"retry-added-conftest-option", "retry-added", "(config.option.reruns = 3)"},
		{"assertion-commented-block-wrap", "assertion-commented", "2 assertions disabled by wrapping them in a block comment, starting with expect(invoice.vat).toBe(7)"},
		{"assertion-commented-python-string-wrap", "assertion-commented", "wrapping them in a triple-quoted string, starting with assert invoice.vat == Decimal(\"7.00\")"},
		{"assertion-commented-prettier-split", "assertion-commented", "assertion commented out: expect(results.map((r) => r.id)).toEqual(['invoice-guide', 'invoice-api', 'billing-faq'])"},
		{"skip-added-go-testmain-no-run", "skip-added", "TestMain never calls m.Run()"},
		{"skip-added-rust-cfg-never", "skip-added", "test parses_nested_blocks is now skipped with #[cfg(any())] — any() never holds, so the test is compiled out of every build"},
		{"skip-added-go-short-guard-removed", "skip-added", "it used to skip only when testing.Short(), now it always does"},
		{"skip-added-kotest-bang-prefix", "skip-added", `test "!applies the loyalty discount after tax" is now skipped with "!" name prefix`},
		{"skip-added-go-subtest-handle-name", "skip-added", "TestDuration now skips itself with st.Skip"},
		{"focus-added-kotest-f-prefix", "focus-added", `test "f:applies coupons" is focused with "f:" name prefix`},
		{"focus-added-minitest-focus", "focus-added", `test "renews at period end" is focused with minitest-focus`},
		{"focus-added-qunit-only", "focus-added", `test "strips accents" is focused with QUnit.only`},
		{"assertion-weakened-jest-empty-match-object", "assertion-weakened", "(the expected value became {}, which accepts almost anything)"},
		{"retry-added-bazel-flaky", "retry-added", `test target "payments_test" in services/payments/BUILD.bazel is now marked flaky = True`},
	}
	for _, c := range cases {
		res := loadCase(t, c.name).Run(nil)
		var msgs []string
		for _, f := range res.Active() {
			if f.RuleID == c.rule {
				msgs = append(msgs, f.Message)
			}
		}
		if !strings.Contains(strings.Join(msgs, "\n"), c.want) {
			t.Errorf("%s: %s messages %q do not contain %q", c.name, c.rule, msgs, c.want)
		}
	}
}

// TestCorpusPatchOnly runs the corpus through ruletest's patch-only mode:
// without full contents rules may miss things, but they must never report
// anything a case does not expect.
func TestCorpusPatchOnly(t *testing.T) { ruletest.RunDirPatchOnly(t, "testdata") }

// TestPatchOnly is the stricter version of TestCorpusPatchOnly: without
// full file contents (narc scan --patch) results must match exactly, except
// where the hunks alone cannot show what a line belongs to.
func TestPatchOnly(t *testing.T) {
	known := map[string]bool{
		// The options follow a test body longer than the hunk context, so the
		// declaration that `}, { retry: 3 });` closes is not in the patch.
		"retry-added-vitest-trailing-options": true,
	}
	cases, err := ruletest.LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		c.PatchOnly = true
		res := c.Run(nil)
		for _, e := range res.Errors {
			t.Errorf("%s: engine error: %s", c.Name, e)
		}
		want := strings.Join(ruletest.InScope(c.Expect), ",")
		got := strings.Join(ruletest.Got(res), ",")
		if (want != got) != known[c.Name] {
			t.Errorf("%s (patch-only): want [%s] got [%s] (known difference: %v)", c.Name, want, got, known[c.Name])
		}
	}
}

// TestFindingsHaveHints checks that every finding carries coaching advice
// and evidence.
func TestFindingsHaveHints(t *testing.T) {
	cases, err := ruletest.LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		for _, f := range c.Run(nil).Active() {
			if f.Hint == "" || f.Line <= 0 || f.Evidence == "" {
				t.Errorf("%s: %s finding lacks hint, line or evidence: %+v", c.Name, f.RuleID, f)
			}
		}
	}
}

// TestCorpusBalance enforces the corpus policy: every rule has at least
// eight cheating and eight legitimate cases.
func TestCorpusBalance(t *testing.T) {
	cases, err := ruletest.LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	cheat, legit := map[string]int{}, map[string]int{}
	for _, c := range cases {
		for _, id := range ruleIDs {
			if !strings.HasPrefix(c.Name, id+"-") {
				continue
			}
			if strings.HasPrefix(c.Name, id+"-legit-") {
				legit[id]++
				if len(c.Expect) != 0 {
					t.Errorf("%s: legitimate case expects %v", c.Name, c.Expect)
				}
			} else {
				cheat[id]++
				if !contains(c.Expect, id) {
					t.Errorf("%s: cheat case does not expect %s", c.Name, id)
				}
			}
		}
	}
	for _, id := range ruleIDs {
		if cheat[id] < 8 || legit[id] < 8 {
			t.Errorf("%s: %d cheat and %d legit cases, want at least 8 of each", id, cheat[id], legit[id])
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
