package accounting

import (
	"os"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// BenchmarkHelpersBigFile measures helpersIn on a large real TypeScript
// test file named by NARC_BIG_FILE (skipped without it).
func BenchmarkHelpersBigFile(b *testing.B) {
	p := os.Getenv("NARC_BIG_FILE")
	if p == "" {
		b.Skip()
	}
	src, _ := os.ReadFile(p)
	lines := lang.Lex(lang.TypeScript, string(src))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		helpersIn(lang.TypeScript, &segment{lines: lines, whole: true, hunk: -1, lang: lang.TypeScript}, map[string]int{})
	}
}
