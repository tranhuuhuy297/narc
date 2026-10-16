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

func TestLexSomeMatchesLex(t *testing.T) {
	srcs := map[Lang]string{
		JavaScript: "const a = `x ${b ? `y` : 'z'} w`; // c\n/* multi\nline */ it.skip('q', () => {})\nconst re = /a\\/b/g;\nconst s = `line1\nline2 ${x}\nline3`\n",
		Python:     "def f():\n    '''doc\n    it.skip\n    '''\n    return 1  # c\nx = 'a' \"b\"\n",
		Go:         "s := `raw\nmore`\n// c\nx := \"q\" /* z */ + y\n",
		YAML:       "a: 'it''s'\nb: \"x # y\" # c\nrun: |\n  npm test || true\n",
		Rust:       "let s = r#\"a\n\"b\"#; // c\nfn f<'a>() -> char { 'x' }\n",
	}
	for l, src := range srcs {
		full := Lex(l, src)
		for mask := 0; mask < 1<<len(full) && mask < 512; mask++ {
			keep := func(n int) bool { return mask&(1<<(n-1)) != 0 }
			some := LexSome(l, src, keep)
			if len(some) != len(full) {
				t.Fatalf("%s: line count %d vs %d", l, len(some), len(full))
			}
			for i := range full {
				if keep(i + 1) {
					if !reflect.DeepEqual(some[i], full[i]) {
						t.Fatalf("%s mask %b line %d:\nsome %+v\nfull %+v", l, mask, i+1, some[i], full[i])
					}
				} else if some[i].Raw != full[i].Raw {
					t.Fatalf("%s: raw mismatch on unkept line %d", l, i+1)
				}
			}
		}
	}
}
