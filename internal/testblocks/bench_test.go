package testblocks

import (
	"strings"
	"testing"

	"github.com/huyth45/narc/internal/lang"
)

func benchSource() string {
	var b strings.Builder
	b.WriteString("import { thing } from './thing'\n\ndescribe('suite', () => {\n")
	for i := 0; i < 300; i++ {
		b.WriteString("  it('does thing number one', async () => {\n    const res = await thing({ id: 1, name: 'x' })\n    if (res.ok) { log(res) }\n    expect(res.status).toBe(200)\n    expect(res.body).toEqual({ id: 1 })\n  })\n\n")
	}
	b.WriteString("})\n")
	return b.String()
}

func BenchmarkFind(b *testing.B) {
	lines := lang.Lex(lang.TypeScript, benchSource())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Find(lang.TypeScript, lines)
	}
}

func BenchmarkCountAssertions(b *testing.B) {
	lines := lang.Lex(lang.TypeScript, benchSource())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, l := range lines {
			CountAssertions(lang.TypeScript, l.Code)
		}
	}
}
