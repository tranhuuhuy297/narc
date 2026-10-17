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
		// Source edits that mostly delete code keep deletions warnings.
		{"test-deleted-feature-removal-stays-warning", "test-deleted", core.Warning, 0,
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
