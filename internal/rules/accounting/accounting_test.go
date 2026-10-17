package accounting_test

import (
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
	_ "github.com/tranhuuhuy297/narc/internal/rules/accounting"
	"github.com/tranhuuhuy297/narc/internal/ruletest"
)

func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }

// TestCorpusPatchOnly re-runs every case without full file contents, as
// `narc scan --patch` does. The rules must degrade gracefully: never crash,
// never accuse a legitimate change, and never report a rule the full
// analysis does not.
func TestCorpusPatchOnly(t *testing.T) { ruletest.RunDirPatchOnly(t, "testdata") }

// TestMessages pins the wording, severity and anchor of representative
// findings: messages must name the test and show the counts.
func TestMessages(t *testing.T) {
	cases := []struct {
		file, rule string
		sev        core.Severity
		line       int
		msg        string
	}{
		{"test-deleted-ts-expiry-check-removed", "test-deleted", core.Error, 9,
			`test "verify > rejects expired tokens" was deleted (it had 1 assertion) while the same change modified source code (src/auth/jwt.ts)`},
		{"test-deleted-java-no-source-change", "test-deleted", core.Warning, 14,
			`test "OrderServiceTest.rejectsZeroQuantity" was deleted (it had 1 assertion)`},
		{"test-file-deleted-py-rate-limit", "test-file-deleted", core.Warning, 0,
			`deleted test file had 3 tests (4 assertions) that were not moved elsewhere: "test_allows_burst_up_to_capacity", "test_blocks_when_empty", "test_refills_over_time"`},
		{"test-file-deleted-go-retry", "test-file-deleted", core.Error, 0,
			`deleted test file had 2 tests (2 assertions) that were not moved elsewhere: "TestBackoffDoubles", "TestBackoffIsCapped" while the same change modified source code (internal/retry/retry.go)`},
		{"assertions-removed-ts-dropped-3-to-1", "assertions-removed", core.Error, 7,
			`assertions in "createSession > rejects expired tokens" dropped 3 → 1`},
		// A rewrite with other assertions is info.
		{"assertions-removed-ts-renamed-and-trimmed", "assertions-removed", core.Error, 6,
			`assertions in "applies free shipping above 50 euros" dropped 3 → 2 (now "computes order totals")`},
		{"assertions-removed-py-rewritten", "assertions-removed", core.Info, 5,
			`assertions in "test_create_order_returns_created" dropped 3 → 2 while the test was rewritten with other assertions`},
		{"test-hollowed-py-pass", "test-hollowed", core.Error, 5,
			`test "test_reduced_rate_for_books" no longer asserts anything (2 → 0 assertions); its body is empty`},
		{"test-hollowed-py-early-return", "test-hollowed", core.Error, 0,
			`test "test_next_run_skips_weekends" no longer asserts anything (2 → 0 assertions); it returns before reaching its assertions`},
		{"test-hollowed-go-call-only", "test-hollowed", core.Error, 7,
			`test "TestMigrateIsIdempotent" no longer asserts anything (2 → 0 assertions)`},
		{"test-hollowed-js-bare-expect", "test-hollowed", core.Error, 6,
			`test "cart > sums line items with the coupon applied" no longer asserts anything (2 → 0 assertions); its expectations have no matcher, so they check nothing`},
		{"test-hollowed-ts-if-false", "test-hollowed", core.Error, 7,
			`test "createSession > expires after the configured TTL" no longer asserts anything (2 → 0 assertions); its assertions sit in a block that never runs`},
		// Tests switched off without being deleted: anchored on what is left.
		{"test-deleted-py-renamed-uncollected", "test-deleted", core.Error, 11,
			`test "test_refund_rejects_closed_invoice" no longer runs: it was renamed to "_test_refund_rejects_closed_invoice", which the test runner does not collect; its 2 assertions are still in the code while the same change modified source code (shop/refunds.py)`},
		{"test-deleted-java-test-annotation-removed", "test-deleted", core.Warning, 16,
			`test "CartTest.rejectsNegativeQuantity" no longer runs: its @Test marker was removed; its assertion is still in the code`},
		{"test-file-deleted-js-renamed-uncollected", "test-file-deleted", core.Error, 0,
			`test file was renamed to src/pricing.disabled.js, which the test runner does not collect, so its 2 tests (2 assertions) no longer run: "shippingCost > charges the base rate up to 2 kg", "shippingCost > rejects non-positive weights" while the same change modified source code (src/pricing.js)`},
		// A pruning pass over several files is one finding for the change.
		{"test-deleted-pruning-pass-reported-once", "test-deleted", core.Warning, 9,
			`3 tests were deleted from 3 test files (3 assertions): "calculate > adds three numbers", "formatResult > keeps integers as is", "convert > converts feet to meters"`},
		// A file rewritten with far fewer tests is one warning.
		{"test-deleted-rewrite-drops-most-tests", "test-deleted", core.Warning, 0,
			`the test file lost most of its tests in a rewrite: 7 old tests (7 assertions) are gone and only 2 new tests were written in their place, while the same change modified the source file they cover (src/features/ChatInput/hooks/useSelectExecutionTarget.ts); behavior the old tests checked may now be untested: "useSelectExecutionTarget > pins the given deviceId when switching to a specific device", "useSelectExecutionTarget > stores local verbatim to keep the in-process IPC path", "useSelectExecutionTarget > falls back to the gateway connection when no device is cached", "useSelectExecutionTarget > suppresses the failure toast for automatic defaults" and 3 more`},
		// A test-only change that trims many tests is one finding.
		{"assertions-removed-test-only-cleanup-reported-once", "assertions-removed", core.Warning, 9,
			`3 tests lost 4 assertions in a change that touches no source code: "HypothesisCard > renders the statement, rationale, and one-based ordinal", "HypothesisCard > shows the verdict once the agent has one", "HypothesisCard > marks a hypothesis in flight"`},
		{"test-file-deleted-sdk-wiring-suite-dropped", "test-file-deleted", core.Error, 0,
			`deleted test file had 4 tests (6 assertions) that were not moved elsewhere: "dictation SDK wiring > getDictationConfig calls GooseDictationConfig and returns providers map", "dictation SDK wiring > transcribeDictation forwards audio + mimeType + provider", "dictation SDK wiring > saveDictationModelSelection calls GooseDictationModelSelect", "dictation SDK wiring > saveDictationProviderSecret calls GooseSecretUpsert" while the same change modified source code (ui/goose2/src/shared/api/dictation.ts)`},
		// Source edits that mostly delete code make deletions info.
		{"test-deleted-legit-feature-removal-info", "test-deleted", core.Info, 0,
			`test "test_exports_csv_as_attachment" was deleted (it had 2 assertions) while the same change removed source code (20 lines removed, 1 added in reports/export.py)`},
	}
	for _, tc := range cases {
		c, err := ruletest.Load("testdata/" + tc.file + ".case")
		if err != nil {
			t.Fatal(err)
		}
		var found []core.Finding
		for _, f := range c.Run(nil).Active() {
			if f.RuleID == tc.rule {
				found = append(found, f)
			}
		}
		if len(found) != 1 {
			t.Errorf("%s: %d %s findings, want 1", tc.file, len(found), tc.rule)
			continue
		}
		f := found[0]
		if f.Message != tc.msg || f.Severity != tc.sev || (tc.line != 0 && f.Line != tc.line) {
			t.Errorf("%s:\n got [%s] line %d %q\nwant [%s] line %d %q", tc.file, f.Severity, f.Line, f.Message, tc.sev, tc.line, tc.msg)
		}
		if f.Hint == "" || !strings.Contains(f.Hint, " ") {
			t.Errorf("%s: missing hint", tc.file)
		}
		if f.RuleID != "test-file-deleted" && f.Before == "" {
			t.Errorf("%s: finding should show the removed code in Before", tc.file)
		}
	}
}

// TestCheatsStayVisible: every cheat case reports the accounting rules it
// expects at warning or above, the level the audit and the hooks show by
// default, unless the case pins a lower severity.
func TestCheatsStayVisible(t *testing.T) {
	cases, err := ruletest.LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if strings.Contains(c.Name, "-legit-") {
			continue
		}
		res := c.Run(nil)
		for _, id := range ruletest.InScope(c.Expect) {
			if _, pinned := c.ExpectSev[id]; pinned {
				continue
			}
			max := core.Off
			for _, f := range res.Active() {
				if f.RuleID == id && f.Severity > max {
					max = f.Severity
				}
			}
			if max < core.Warning {
				t.Errorf("%s: %s reported at %s, want warning or error", c.Path, id, max)
			}
		}
	}
}
