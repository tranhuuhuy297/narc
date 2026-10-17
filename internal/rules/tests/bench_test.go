package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
)

// BenchmarkLargeRewrite runs all six rules over a 15,000-line rewrite of a
// test file in which every test is weakened and has a commented-out
// assertion. The audit runs rules over thousands of commits, so they must
// stay linear.
func BenchmarkLargeRewrite(b *testing.B) {
	var o, n strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&o, "it('case %d', () => {\n  const r = compute(%d);\n  expect(r.total).toEqual(%d);\n  // note %d\n});\n", i, i, i*2, i)
		fmt.Fprintf(&n, "it('case %d', async () => {\n  const r = await compute(%d);\n  expect(r.total).toBeDefined();\n  // expect(r.total).toEqual(%d);\n});\n", i, i, i*2)
	}
	p := "src/big.test.ts"
	src := &core.MemSource{OldFiles: map[string][]byte{p: []byte(o.String())}, NewFiles: map[string][]byte{p: []byte(n.String())}}
	fd := diff.Compute(p, p, []byte(o.String()), []byte(n.String()), true, true, diff.DefaultContext)
	rules := []*core.Rule{skipAdded, focusAdded, tautology, assertionWeakened, assertionCommented, retryAdded}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := &core.Analysis{Files: []*core.FileInfo{core.NewFileInfo(fd, src, core.Settings{})}}
		total := 0
		for _, r := range rules {
			total += len(r.Check(a))
		}
		if i == 0 {
			b.Logf("findings: %d", total)
		}
	}
}
