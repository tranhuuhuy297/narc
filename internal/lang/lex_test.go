package lang

import (
	"reflect"
	"testing"
)

func TestLexBasics(t *testing.T) {
	cases := []struct {
		lang          Lang
		src           string
		code, comment []string
		strings0      []string
	}{
		{JavaScript, `it.skip("works // not a comment", () => {}) // real comment`,
			[]string{`it.skip("", () => {}) `}, []string{"real comment"}, []string{"works // not a comment"}},
		{TypeScript, "const x = y as any; /* note */ z()",
			[]string{"const x = y as any;   z()"}, []string{"note"}, nil},
		{JavaScript, "const re = /\\/\\/ x/g; // c",
			[]string{"const re = //g; "}, []string{"c"}, nil},
		{JavaScript, "const t = `a ${b ? `x` : 'y'} c` // t",
			[]string{"const t = `${b ? `` : ''}` "}, []string{"t"}, nil},
		{Python, `assert x == 1  # type: ignore`,
			[]string{`assert x == 1  `}, []string{"type: ignore"}, nil},
		{Python, "s = '''a\n# not comment\n'''\n# yes",
			[]string{"s = '''", "", "'''", ""}, []string{"", "", "", "yes"}, []string{"a\n# not comment\n"}},
		{Go, "s := `raw // x`\n// c",
			[]string{"s := ``", ""}, []string{"", "c"}, []string{"raw // x"}},
		{Rust, "fn f<'a>(x: &'a str) -> char { 'x' } // c",
			[]string{"fn f<'a>(x: &'a str) -> char { '' } "}, []string{"c"}, []string{"x"}},
		{Rust, `let s = r#"a "quoted" b"#;`,
			[]string{`let s = r#""#;`}, []string{""}, []string{`a "quoted" b`}},
		{YAML, "run: npm test || true # don't fail\nname: Don't skip",
			[]string{"run: npm test || true ", "name: Don't skip"}, []string{"don't fail", ""}, nil},
		{YAML, "url: http://x/#frag",
			[]string{"url: http://x/#frag"}, []string{""}, nil},
		{Shell, "echo \"a # b\" # c",
			[]string{`echo "" `}, []string{"c"}, []string{"a # b"}},
		{Kotlin, "fun `returns 42`() { }",
			[]string{"fun ``() { }"}, []string{""}, []string{"returns 42"}},
	}
	for i, c := range cases {
		got := Lex(c.lang, c.src)
		var code, com []string
		for _, l := range got {
			code = append(code, l.Code)
			com = append(com, l.Comment)
		}
		if !reflect.DeepEqual(code, c.code) {
			t.Errorf("case %d (%s) code:\n got %q\nwant %q", i, c.lang, code, c.code)
		}
		if !reflect.DeepEqual(com, c.comment) {
			t.Errorf("case %d (%s) comment:\n got %q\nwant %q", i, c.lang, com, c.comment)
		}
		if c.strings0 != nil && !reflect.DeepEqual(got[0].Strings, c.strings0) {
			t.Errorf("case %d (%s) strings: got %q want %q", i, c.lang, got[0].Strings, c.strings0)
		}
	}
}

func TestLexLineCount(t *testing.T) {
	for _, src := range []string{"", "a", "a\n", "a\n\n", "a\nb", "/* x\ny */\n"} {
		n := len(Lex(Go, src))
		want := 0
		if src != "" {
			want = len(splitLines(src))
		}
		if n != want {
			t.Errorf("%q: got %d lines want %d", src, n, want)
		}
	}
}

func splitLines(s string) []string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
