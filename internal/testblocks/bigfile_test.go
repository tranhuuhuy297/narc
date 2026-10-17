package testblocks

import (
	"os"
	"testing"
	"time"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// The tests and benchmarks in this file measure Find on a large real test
// file named by NARC_BIG_FILE (TypeScript), e.g. a 50,000-line generated
// suite; they are skipped without it.

func TestZZBigFile(t *testing.T) {
	p := os.Getenv("NARC_BIG_FILE")
	if p == "" {
		t.Skip()
	}
	b, _ := os.ReadFile(p)
	t0 := time.Now()
	lines := lang.Lex(lang.TypeScript, string(b))
	t1 := time.Now()
	bs := Find(lang.TypeScript, lines)
	t2 := time.Now()
	n := 0
	for _, l := range lines {
		n += CountAssertions(lang.TypeScript, l.Code)
	}
	t3 := time.Now()
	t.Logf("lines=%d tests=%d lex=%v find=%v count=%v asserts=%d", len(lines), len(bs), t1.Sub(t0), t2.Sub(t1), t3.Sub(t2), n)
}

func BenchmarkZZBigFind(b *testing.B) {
	p := os.Getenv("NARC_BIG_FILE")
	if p == "" {
		b.Skip()
	}
	src, _ := os.ReadFile(p)
	lines := lang.Lex(lang.TypeScript, string(src))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Find(lang.TypeScript, lines)
	}
}

func BenchmarkZZHeadsWhole(b *testing.B) {
	p := os.Getenv("NARC_BIG_FILE")
	if p == "" {
		b.Skip()
	}
	src, _ := os.ReadFile(p)
	t := newText(lang.TypeScript, lang.Lex(lang.TypeScript, string(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		jsCalls.head.FindAllStringSubmatchIndex(t.code, -1)
	}
}

func BenchmarkZZHeadsLines(b *testing.B) {
	p := os.Getenv("NARC_BIG_FILE")
	if p == "" {
		b.Skip()
	}
	src, _ := os.ReadFile(p)
	t := newText(lang.TypeScript, lang.Lex(lang.TypeScript, string(src)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t.findAll(jsCalls.head, jsCalls.need)
	}
}

func BenchmarkZZFindLazy(b *testing.B) {
	p := os.Getenv("NARC_BIG_FILE")
	if p == "" {
		b.Skip()
	}
	src, _ := os.ReadFile(p)
	lines := lang.Lex(lang.TypeScript, string(src))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FindLazy(lang.TypeScript, lines, func(int, int) bool { return false })
	}
}
