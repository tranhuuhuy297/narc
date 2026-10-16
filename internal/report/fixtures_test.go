package report

import (
	"time"

	"github.com/huyth45/narc/internal/config"
	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/engine"
)

// The fixtures are hand-built so the goldens do not move when rule
// packages change their wording. They cover every shape a finding takes:
// errors, warnings and notes; additions, replacements (Before + Evidence)
// and removals (Before only); file-level findings; waivers from inline
// comments and from .narc.yml; a waiver without a reason; long messages
// and code lines; text that needs escaping in every format.

var testRules = []*core.Rule{
	{
		ID: "skip-added", Title: "Test skipped", Category: "tests", Severity: core.Error,
		Summary: "New skip/disable markers (`it.skip`, `xit`, `@pytest.mark.skip`, `t.Skip`, `#[ignore]`, `@Disabled`).",
		Hint:    "Fix the code under test so the test passes instead of skipping it. If the test itself is wrong, fix the test and say why in the commit.",
		Docs: "A skipped test cannot fail, so skipping a failing test hides the bug instead of fixing it.\n\n" +
			"Bad:\n\n```ts\nit.skip('rejects expired tokens', async () => {\n  expect(await verify(expired)).toBe(false)\n})\n```\n\n" +
			"Good: fix `verify()` so the test passes. narc recognizes:\n\n" +
			"- `it.skip`, `describe.skip`, `xit`, `xdescribe` (Jest, Vitest, Mocha)\n" +
			"- `@pytest.mark.skip` and `pytest.skip()` (pytest)\n" +
			"- `t.Skip()` (Go), `#[ignore]` (Rust), `@Disabled` (JUnit)\n\n" +
			"See [the waiver guide](https://github.com/huyth45/narc#waivers) when a skip is **really** intended.",
	},
	{
		ID: "focus-added", Title: "Test focused", Category: "tests", Severity: core.Error,
		Summary: "`.only`, `fit`, `fdescribe`: silently skips every other test in the file.",
		Hint:    "Remove the focus marker so the whole suite runs.",
	},
	{
		ID: "assertion-weakened", Title: "Assertion weakened", Category: "tests", Severity: core.Warning,
		Summary: "A specific matcher was replaced by a weaker one (`toEqual(x)` → `toBeDefined()`), or a tolerance was widened.",
		Hint:    "Keep the precise assertion and fix the code until it holds.",
	},
	{
		ID: "assertions-removed", Title: "Assertions removed", Category: "accounting", Severity: core.Error,
		Summary: "A surviving test lost assertions (`3 → 1`).",
		Hint:    "Restore the removed assertions; if one is obsolete, replace it with an assertion about the new behavior.",
	},
	{
		ID: "test-file-deleted", Title: "Test file deleted", Category: "accounting", Severity: core.Warning,
		Summary: "A test file was deleted and its tests were not re-added elsewhere.",
		Hint:    "Keep the tests, or move them to the file that now covers this code.",
	},
	{
		ID: "any-cast", Title: "Type escape (any cast)", Category: "escapes", Severity: core.Warning,
		Summary: "`as any`, `as unknown as T`, `<any>`, `cast(Any, ...)` (info in tests).",
		Hint:    "Fix the types instead of casting them away.",
	},
	{
		ID: "lint-suppressed", Title: "Lint rule suppressed", Category: "escapes", Severity: core.Warning,
		Summary: "`eslint-disable`, `# noqa`, `//nolint`, `#[allow(...)]`, `@SuppressWarnings` (info in tests).",
		Hint:    "Fix the lint error instead of silencing it.",
	},
	{
		ID: "hardcoded-answer", Title: "Hardcoded test answer", Category: "escapes", Severity: core.Warning,
		Summary: "Source special-cases a literal that the tests use as input or expected output.",
		Hint:    "Implement the general logic; the tests should pass for inputs they do not mention.",
	},
	{
		ID: "ci-failure-ignored", Title: "CI failure ignored", Category: "ci", Severity: core.Error,
		Summary: "`continue-on-error: true`, `allow_failure: true`, `|| true` on a verification command.",
		Hint:    "Let the step fail and fix what makes it fail.",
	},
	{
		ID: "coverage-lowered", Title: "Coverage threshold lowered", Category: "ci", Severity: core.Error,
		Summary: "Coverage thresholds were lowered or removed.",
		Hint:    "Restore the threshold and add tests for the uncovered code.",
	},
	{
		ID: "protected-path", Title: "Protected file changed", Category: "integrity", Severity: core.Error,
		Summary: "A file matching a `protected:` glob in .narc.yml was modified or deleted.",
		Hint:    "Revert the change to the protected file. If it really must change, a human should make or approve that change.",
	},
	{
		ID: "waiver-missing-reason", Title: "Waiver without a reason", Category: "integrity", Severity: core.Warning,
		Summary: "A narc:allow comment must say why the waived change is acceptable.",
		Hint:    `Add a reason: narc:allow <rule> reason="why this is fine (link to issue)"`,
	},
}

func testRule(id string) *core.Rule {
	for _, r := range testRules {
		if r.ID == id {
			return r
		}
	}
	panic("no test rule " + id)
}

// finding builds a finding the way the engine fills it in.
func finding(id, path string, line int, msg string) core.Finding {
	r := testRule(id)
	return core.Finding{RuleID: r.ID, Title: r.Title, Category: r.Category, Severity: r.Severity,
		Path: path, Line: line, Message: msg, Hint: r.Hint}
}

func with(f core.Finding, edit func(*core.Finding)) core.Finding {
	edit(&f)
	return f
}

// bustedResult is a messy, realistic agent change.
func bustedResult() *engine.Result {
	fs := []core.Finding{
		with(finding("ci-failure-ignored", ".github/workflows/ci.yml", 31,
			"`npm test` can no longer fail the build: `|| true` was appended"), func(f *core.Finding) {
			f.OldLine = 31
			f.Before = "run: npm test"
			f.Evidence = "run: npm test || true"
		}),
		with(finding("coverage-lowered", "jest.config.js", 12,
			"coverage threshold for lines lowered 90% → 70%, branches: 85% → 60%"), func(f *core.Finding) {
			f.OldLine = 12
			f.Before = "lines: 90, branches: 85,"
			f.Evidence = "lines: 70, branches: 60,"
		}),
		with(finding("any-cast", "src/billing/invoice.ts", 14,
			"`as any` cast added in calculateTotal()"), func(f *core.Finding) {
			f.Evidence = "const total = (order as any).total_cents / 100;"
		}),
		with(finding("hardcoded-answer", "src/billing/invoice.ts", 203,
			`production code special-cases 4999, the exact input used by "applies the loyalty discount" in src/billing/invoice.test.ts, and returns the test's expected value 4499 directly instead of computing it`),
			func(f *core.Finding) {
				f.Evidence = `if (order.subtotalCents === 4999 && order.customer.tier === "gold") { return { ...order, totalCents: 4499, discountApplied: true }; }`
				f.Hint = "Delete the special case and fix the discount calculation; \"applies the loyalty discount\" must pass for any gold-tier subtotal, not just 4999."
			}),
		with(finding("lint-suppressed", "src/billing/invoice.test.ts", 9,
			"eslint-disable-next-line added for @typescript-eslint/no-explicit-any"), func(f *core.Finding) {
			f.Severity = core.Info
			f.Evidence = "// eslint-disable-next-line @typescript-eslint/no-explicit-any"
		}),
		with(finding("skip-added", "src/auth/session.test.ts", 42,
			`test "rejects expired tokens" was skipped with it.skip`), func(f *core.Finding) {
			f.Evidence = "it.skip('rejects expired tokens', async () => {"
		}),
		with(finding("assertions-removed", "src/auth/session.test.ts", 57,
			`assertions in "refreshes an expiring session" dropped 3 → 1`), func(f *core.Finding) {
			f.OldLine = 61
			f.Before = "expect(session.expiresAt).toBeGreaterThan(Date.now())"
		}),
		with(finding("assertion-weakened", "src/auth/session.test.ts", 88,
			"`toEqual({ id: 7, role: \"admin\" })` was replaced by the weaker `toBeDefined()`"), func(f *core.Finding) {
			f.OldLine = 92
			f.Before = `expect(user).toEqual({ id: 7, role: "admin" })`
			f.Evidence = "expect(user).toBeDefined()"
		}),
		with(finding("test-file-deleted", "tests/test_payments.py", 0,
			`test file deleted with 6 tests ("test_refund_partial", "test_refund_full", and 4 more); none were re-added elsewhere`),
			func(*core.Finding) {}),
		// Waived by an allow: entry in .narc.yml.
		with(finding("protected-path", "tests/golden/invoice.txt", 1,
			"protected file was modified (+1 -1)"), func(f *core.Finding) {
			f.Evidence = "Total: 100.00 EUR"
			f.Waived, f.WaiverReason = true, "tax rate changed by finance, ticket FIN-12"
		}),
		// Waived inline, with a reason.
		with(finding("skip-added", "src/legacy/client.test.js", 12,
			`test "syncs with partner API" was skipped with it.skip`), func(f *core.Finding) {
			f.Evidence = `it.skip("syncs with partner API", () => { // narc:allow skip-added reason="flaky upstream API, see #812"`
			f.Waived, f.WaiverReason = true, "flaky upstream API, see #812"
		}),
		// An inline waiver without a reason does not apply...
		with(finding("focus-added", "src/legacy/client.test.js", 30,
			"`describe.only` focuses this suite; the other 14 tests in the file are silently skipped"), func(f *core.Finding) {
			f.Evidence = `describe.only("retry policy", () => { // narc:allow focus-added`
		}),
		// ...and is reported itself.
		with(finding("waiver-missing-reason", "src/legacy/client.test.js", 30,
			"narc:allow for focus-added has no reason, so it was ignored"), func(f *core.Finding) {
			f.Evidence = `describe.only("retry policy", () => { // narc:allow focus-added`
		}),
	}
	for i := range fs {
		fs[i].ComputeFingerprint()
	}
	return &engine.Result{
		Label:    "main → HEAD",
		Findings: sortedFindings(fs),
		Files:    9,
		Skipped:  2,
		Added:    214,
		Removed:  87,
		Errors:   []string{"rule retry-added crashed: runtime error: index out of range [3] with length 3\ngoroutine 7 [running]:\nruntime/debug.Stack()"},
		Duration: 42 * time.Millisecond,
	}
}

// bustedConfig is the .narc.yml behind bustedResult's external waiver.
func bustedConfig() *config.Config {
	c := config.Default()
	c.Protected = []string{"tests/golden/**"}
	c.Allow = []config.Allow{{Rule: "protected-path", Path: "tests/golden/invoice.txt", Reason: "tax rate changed by finance, ticket FIN-12"}}
	return c
}

// suspiciousResult has warnings and a note, but no errors.
func suspiciousResult() *engine.Result {
	fs := []core.Finding{
		with(finding("any-cast", "src/api/client.ts", 77, "`as unknown as Response` double cast added in fetchUser()"), func(f *core.Finding) {
			f.Evidence = "return (await res.json()) as unknown as Response;"
		}),
		with(finding("lint-suppressed", "src/api/client.test.ts", 3, "`eslint-disable` added for the whole file"), func(f *core.Finding) {
			f.Severity = core.Info
			f.Evidence = "/* eslint-disable */"
		}),
	}
	for i := range fs {
		fs[i].ComputeFingerprint()
	}
	return &engine.Result{Label: "staged changes", Findings: sortedFindings(fs), Files: 2, Added: 31, Removed: 4,
		Duration: 3 * time.Millisecond}
}

// cleanResult found nothing.
func cleanResult() *engine.Result {
	return &engine.Result{Label: "HEAD → working tree", Files: 4, Added: 120, Removed: 34, Duration: 900 * time.Microsecond}
}
