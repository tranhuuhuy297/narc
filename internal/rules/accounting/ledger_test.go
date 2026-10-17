package accounting

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/ruletest"
)

func TestStem(t *testing.T) {
	cases := []struct {
		path string
		test bool
		want string
	}{
		{"src/exporter.ts", false, "exporter"},
		{"src/__tests__/exporter.test.ts", true, "exporter"},
		{"test/exporter.spec.js", true, "exporter"},
		{"tests/test_exporter.py", true, "exporter"},
		{"pkg/exporter/exporter_test.go", true, "exporter"},
		{"src/test/java/ExporterTest.java", true, "exporter"},
		{"Tests/ExporterTests.swift", true, "exporter"},
		{"spec/exporter_spec.rb", true, "exporter"},
		{"src/contest.spec.ts", true, "contest"},
		{"src/edit.ts", false, "edit"},
	}
	for _, c := range cases {
		if got := stem(c.path, c.test); got != c.want {
			t.Errorf("stem(%q, %v) = %q, want %q", c.path, c.test, got, c.want)
		}
	}
}

func TestCoveredBy(t *testing.T) {
	pool := tokenPool{}
	// Tokens as tokens() makes them: string contents carry a leading quote.
	pool.add([]string{"slugify", "title", "slug", `"Hello World, hello-world`, "Quote", `"DHL`, "590"})
	cases := []struct {
		toks []string
		want bool
	}{
		// Literal values found in a data row: covered.
		{[]string{"assertEquals", `"hello-world`, "slugify", `"Hello World`}, true},
		// Case-insensitive identifiers, half of them with a literal match.
		{[]string{"Assert", "Equal", `"DHL`, "quote", "Carrier"}, true},
		// A literal that appears nowhere: not covered.
		{[]string{"expect", "res", "status", "toBe", "401"}, false},
		// Identifiers only: three quarters must match.
		{[]string{"expect", "spy", "toHaveBeenCalledWith", "user"}, false},
		// Nothing specific (a tautology): nothing to lose.
		{[]string{"expect", "true", "toBe", "true"}, true},
		// Assertion messages are not values.
		{[]string{"t", "Errorf", `"Port = %d, want 8080`, "slug"}, true},
	}
	for i, c := range cases {
		if got := coveredBy(c.toks, pool); got != c.want {
			t.Errorf("case %d %v: covered = %v, want %v", i, c.toks, got, c.want)
		}
	}
}

func TestTestSupportAndFixtures(t *testing.T) {
	for p, want := range map[string]bool{
		"internal/testutil/bank.go":  true,
		"pkg/testing/assert.go":      true,
		"spec/support/helpers.rb":    false, // under spec/: a test file already
		"lib/test_helpers/stub.ex":   true,
		"internal/retry/retry.go":    false,
		"internal/testdata/input.go": false,
	} {
		if got := testSupport(p); got != want {
			t.Errorf("testSupport(%q) = %v, want %v", p, got, want)
		}
	}
	if !fixture("internal/testdata/input.go") || fixture("internal/retry/retry.go") {
		t.Error("fixture classification is wrong")
	}
}

func TestVouches(t *testing.T) {
	l := &ledger{helpers: map[string]int{"balanceIs": 1}}
	cases := []struct {
		code, next string
		want       bool
	}{
		{`expect(t.total).toBe(6000)`, "", true},
		{`{"2.0.0", "1.9.9", 1},`, "", true},
		{`want := Config{Port: 8080, Host: ""}`, "", true},
		{`listOf("a", "b") to 2,`, "", true},
		{`balanceIs(t, a, 150)`, "", true},
		{`if diff := cmp.Diff(want, cfg); diff != "" {`, `t.Errorf("mismatch: %s", diff)`, true},
		{`_ = formatter.string(cents: 1999)`, "", false},
		{`Migrate(db)`, "", false},
	}
	for _, c := range cases {
		if got := l.vouches(lang.Go, c.code, c.next); got != c.want {
			t.Errorf("vouches(%q) = %v, want %v", c.code, got, c.want)
		}
	}
}

// ledgerOf builds the ledger of a corpus case.
func ledgerOf(t *testing.T, name string) *ledger {
	t.Helper()
	c, err := ruletest.Load("testdata/" + name + ".case")
	if err != nil {
		t.Fatal(err)
	}
	cs := c.Changeset()
	a := &core.Analysis{Settings: config.Default().Settings()}
	for _, fd := range cs.Files {
		a.Files = append(a.Files, core.NewFileInfo(fd, cs.Source, a.Settings))
	}
	return build(a)
}

func TestMatchKinds(t *testing.T) {
	cases := []struct {
		name string
		want map[string]matchKind // old test name -> how it was found again
	}{
		{"test-deleted-legit-moved-to-other-file", map[string]matchKind{
			"auth > logs in with a valid password": byName,
			"auth > rejects expired tokens":        moved,
		}},
		{"test-deleted-legit-renamed", map[string]matchKind{"test_total": renamed}},
		{"test-deleted-legit-describe-flattened", map[string]matchKind{
			"Queue > basic operations > enqueues items in order":  byBare,
			"Queue > basic operations > dequeues the oldest item": byBare,
		}},
		{"test-deleted-legit-unittest-to-pytest", map[string]matchKind{
			"TestLRUCache::test_evicts_least_recently_used": byBare,
			"TestLRUCache::test_rejects_zero_capacity":      byBare,
		}},
		{"test-file-deleted-legit-kotlin-to-kotest", map[string]matchKind{
			"WordWrapTest.wraps at the column limit": moved,
			"WordWrapTest.keeps short lines intact":  moved,
		}},
		{"test-deleted-legit-parametrized", map[string]matchKind{
			"test_slug_lowercases":       unmatched,
			"test_slug_strips_accents":   unmatched,
			"test_slug_collapses_dashes": unmatched,
		}},
	}
	for _, c := range cases {
		l := ledgerOf(t, c.name)
		got := map[string]matchKind{}
		for _, o := range l.olds {
			got[o.name()] = o.how
		}
		for name, how := range c.want {
			if g, ok := got[name]; !ok || g != how {
				t.Errorf("%s: %q matched as %v (found=%v), want %v; all: %v", c.name, name, g, ok, how, got)
			}
		}
		if len(l.deletions)+len(l.drops) != 0 {
			t.Errorf("%s: legitimate change produced %d deletions and %d drops", c.name, len(l.deletions), len(l.drops))
		}
	}
}

func TestLedgerIsCachedPerAnalysis(t *testing.T) {
	a := &core.Analysis{}
	if ledgerFor(a) != ledgerFor(a) {
		t.Error("the same analysis must reuse its ledger")
	}
	if ledgerFor(&core.Analysis{}) == ledgerFor(a) {
		t.Error("a new analysis must get a new ledger")
	}
}

// renameAnalysis builds the analysis of one file that git reports as
// renamed from oldPath to newPath, plus any extra changed files.
func renameAnalysis(oldPath, newPath, oldSrc, newSrc string, extra map[string][2]string) *core.Analysis {
	src := &core.MemSource{OldFiles: map[string][]byte{oldPath: []byte(oldSrc)}, NewFiles: map[string][]byte{newPath: []byte(newSrc)}}
	fds := []*diff.FileDiff{diff.Compute(oldPath, newPath, []byte(oldSrc), []byte(newSrc), true, true, diff.DefaultContext)}
	for p, c := range extra {
		src.OldFiles[p], src.NewFiles[p] = []byte(c[0]), []byte(c[1])
		fds = append(fds, diff.Compute(p, p, []byte(c[0]), []byte(c[1]), true, true, diff.DefaultContext))
	}
	a := &core.Analysis{Settings: config.Default().Settings(), Source: src}
	for _, fd := range fds {
		a.Files = append(a.Files, core.NewFileInfo(fd, src, a.Settings))
	}
	return a
}

const pricingSpec = `const { shippingCost } = require('./pricing')

describe('shippingCost', () => {
  it('charges the base rate up to 2 kg', () => {
    expect(shippingCost(1.5)).toBe(499)
  })

  it('rejects non-positive weights', () => {
    expect(() => shippingCost(0)).toThrow(RangeError)
  })
})
`

// TestRenamedOutOfTheRunner: git reports a rename (one FileDiff, no
// hunks for a pure rename) from a test file name to one the runner does
// not collect, including a rename that changes the language.
func TestRenamedOutOfTheRunner(t *testing.T) {
	source := map[string][2]string{"src/pricing.js": {"function shippingCost(w) { if (w <= 0) throw new RangeError('w') }\n", "function shippingCost(w) {}\n"}}
	cases := []struct {
		from, to string
		extra    map[string][2]string
		want     string // substring of the test-file-deleted message; "" for no finding
		sev      core.Severity
	}{
		{"src/pricing.test.js", "src/pricing.disabled.js", nil,
			"test file was renamed to src/pricing.disabled.js, which the test runner does not collect, so its 2 tests (2 assertions) no longer run", core.Warning},
		{"src/pricing.test.js", "src/pricing.test.js.bak", source,
			"renamed to src/pricing.test.js.bak", core.Error},
		// Still collected, or plausibly collected: no finding.
		{"src/pricing.test.js", "src/pricing.spec.js", source, "", 0},
		{"src/pricing.test.js", "src/__tests__/pricing.js", source, "", 0},
		{"src/test/kotlin/pricing.test.js", "src/jvmTest/kotlin/pricing.js", source, "", 0},
		{"src/pricing.test.js", "src/pricing.integration-test.js", source, "", 0},
	}
	for _, c := range cases {
		a := renameAnalysis(c.from, c.to, pricingSpec, pricingSpec, c.extra)
		var got []core.Finding
		for _, r := range []*core.Rule{testDeleted, testFileDeleted, assertionsRemoved, testHollowed} {
			got = append(got, r.Check(a)...)
		}
		switch {
		case c.want == "" && len(got) > 0:
			t.Errorf("%s → %s: unexpected %s: %s", c.from, c.to, got[0].RuleID, got[0].Message)
		case c.want != "" && (len(got) != 1 || got[0].RuleID != "test-file-deleted" || !strings.Contains(got[0].Message, c.want) || got[0].Severity != c.sev):
			t.Errorf("%s → %s: got %v, want one test-file-deleted [%s] containing %q", c.from, c.to, got, c.sev, c.want)
		}
	}
}

// TestFormerTestNeedsAnUnreferencedNewFunction checks the guards of the
// "test turned into a plain function" detection.
func TestFormerTestNeedsAnUnreferencedNewFunction(t *testing.T) {
	const old = `package billing

import "testing"

func TestLateFee(t *testing.T) {
	if got := LateFee(3, 10000); got != 300 {
		t.Errorf("LateFee(3) = %d, want 300", got)
	}
}
`
	cases := []struct {
		name, src string
		former    bool
	}{
		{"renamed and unused", strings.Replace(old, "func TestLateFee", "func testLateFee", 1), true},
		{"renamed and called as a subtest",
			strings.Replace(old, "func TestLateFee", "func TestFees(t *testing.T) { t.Run(\"late\", testLateFee) }\n\nfunc testLateFee", 1), false},
		{"deleted outright", "package billing\n", false},
	}
	for _, c := range cases {
		a := renameAnalysis("billing/fees_test.go", "billing/fees_test.go", old, c.src, nil)
		l := build(a)
		var former *formerTest
		for _, o := range l.olds {
			former = o.former
		}
		if (former != nil) != c.former {
			t.Errorf("%s: former = %+v, want %v", c.name, former, c.former)
		}
	}
}

func BenchmarkLedgerLargeTestFile(b *testing.B) {
	var o, n strings.Builder
	o.WriteString("describe('suite', () => {\n")
	n.WriteString("describe('suite', () => {\n")
	for i := 0; i < 3000; i++ {
		body := fmt.Sprintf("  it('case %d', async () => {\n    const res = await thing({ id: %d })\n    expect(res.status).toBe(200)\n    expect(res.body).toEqual({ id: %d })\n  })\n\n", i, i, i)
		o.WriteString(body)
		if i%500 == 7 {
			body = strings.Replace(body, "expect(res.body)", "void (res.body)", 1)
		}
		n.WriteString(body)
	}
	o.WriteString("})\n")
	n.WriteString("})\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := renameAnalysis("src/a.test.ts", "src/a.test.ts", o.String(), n.String(), nil)
		if l := build(a); len(l.drops) != 6 {
			b.Fatalf("%d drops, want 6", len(l.drops))
		}
	}
}

// TestOneSideTooLargeToRead: when only one side of a modified file can be
// read in full (the new version grew past the size limit for reading
// contents), both sides are read from the hunks. Tests outside the hunks
// must not look deleted.
func TestOneSideTooLargeToRead(t *testing.T) {
	var old strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&old, "it('case %d', () => {\n  expect(f(%d)).toBe(%d)\n})\n\n", i, i, i*2)
	}
	added := old.String() + "it('new case', () => {\n  expect(f(99)).toBe(198)\n})\n"
	const p = "src/f.test.ts"
	fd := diff.Compute(p, p, []byte(old.String()), []byte(added), true, true, diff.DefaultContext)
	for _, src := range []*core.MemSource{
		{OldFiles: map[string][]byte{p: []byte(old.String())}, NewFiles: map[string][]byte{}},
		{OldFiles: map[string][]byte{}, NewFiles: map[string][]byte{p: []byte(added)}},
	} {
		a := &core.Analysis{Settings: config.Default().Settings(), Source: src}
		a.Files = []*core.FileInfo{core.NewFileInfo(fd, src, a.Settings)}
		if l := build(a); len(l.deletions)+len(l.drops) != 0 {
			t.Errorf("old readable=%v: %d deletions, %d drops after only adding a test", len(src.OldFiles) > 0, len(l.deletions), len(l.drops))
		}
	}
}

func TestDefinitions(t *testing.T) {
	cases := []struct {
		l    lang.Lang
		code string
		want string // "" for none
	}{
		{lang.TypeScript, `export function formatPrice(cents: number): string {`, "formatPrice"},
		{lang.TypeScript, `  useAssistantCategories: (params) => useSWR(key, fetcher),`, "useAssistantCategories"},
		{lang.TypeScript, `  fetchUser: async (id: string) => {`, "fetchUser"},
		{lang.TypeScript, `  async getTeamsFeatureStates(input: {`, "getTeamsFeatureStates"},
		{lang.TypeScript, `  private resolveScope<T>(`, "resolveScope"},
		{lang.TypeScript, `  render(`, ""},               // a call left open, not a declaration
		{lang.TypeScript, `  onClick: handleClick,`, ""}, // a value, not a function
		{lang.Python, `def refund(order, partial=False):`, "refund"},
		{lang.Go, `func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {`, "handleLogin"},
		{lang.Rust, `    pub(super) fn is_regular_package(&self, resolver: &ResolverContext) -> bool {`, "is_regular_package"},
	}
	for _, c := range cases {
		got := map[string]bool{}
		definitions(c.l, lang.LexLine(c.l, c.code).Code, got)
		if c.want == "" && len(got) > 0 || c.want != "" && !got[c.want] {
			t.Errorf("definitions(%q) = %v, want %q", c.code, got, c.want)
		}
	}
}

// TestDuplicateInUnchangedFile: deleting a test is fine when a
// near-identical test with the same name lives in an unchanged test file
// next to it ("remove duplicate tests"), and still reported when the
// survivor checks something else.
func TestDuplicateInUnchangedFile(t *testing.T) {
	const deleted = `  it('uses the custom model for title generation', async () => {
    const titleModel = mockModel('Custom Title')
    const agent = new Agent({ model: mockModel('reply'), title: { model: titleModel } })
    await agent.reply('hi', { thread: 't-1' })
    expect(await agent.threadTitle('t-1')).toBe('Custom Title')
  })
`
	keep := "describe('agent', () => {\n  it('streams a reply', async () => {\n    expect(await new Agent({}).reply('hi')).toBe('hello')\n  })\n"
	old, cur := keep+"\n"+deleted+"})\n", keep+"})\n"
	for _, c := range []struct {
		survivor string
		want     int // deletions
	}{
		{"describe('title generation', () => {\n" + deleted + "})\n", 0},
		{"describe('title generation', () => {\n" + strings.Replace(deleted, "'Custom Title')\n  })", "'Fallback')\n  })", 1) + "})\n", 0},
		{"describe('title generation', () => {\n  it('uses the custom model for title generation', () => {\n    expect(titles()).toEqual([])\n  })\n})\n", 1},
	} {
		const p, sibling = "src/agent/agent.test.ts", "src/agent/__tests__/title-generation.test.ts"
		src := &core.MemSource{
			OldFiles: map[string][]byte{p: []byte(old), sibling: []byte(c.survivor)},
			NewFiles: map[string][]byte{p: []byte(cur), sibling: []byte(c.survivor)},
		}
		a := &core.Analysis{Settings: config.Default().Settings(), Source: src}
		a.Files = []*core.FileInfo{core.NewFileInfo(diff.Compute(p, p, []byte(old), []byte(cur), true, true, diff.DefaultContext), src, a.Settings)}
		if got := len(build(a).deletions); got != c.want {
			t.Errorf("survivor %q: %d deletions, want %d", c.survivor[:60], got, c.want)
		}
	}
}

func TestTestishPath(t *testing.T) {
	for p, want := range map[string]bool{
		"src/jvmTest/kotlin/Pricing.kt":            true,
		"packages/lib/login.integration-test.ts":   true,
		"e2e-specs/checkout.ts":                    true,
		"src/IntegrationTests/Cart.cs":             true,
		"src/latest/release.py":                    false,
		"src/attestation/verify.go":                false,
		"src/inspector/panel.ts":                   false,
		"src/testimonials/list.tsx":                false,
		"internal/testdata/pricing_integration.go": false,
	} {
		if got := testishPath(p); got != want {
			t.Errorf("testishPath(%q) = %v, want %v", p, got, want)
		}
	}
}
