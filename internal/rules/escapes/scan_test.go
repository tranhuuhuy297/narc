package escapes

import (
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

func TestViewCanonAndTokens(t *testing.T) {
	cases := []struct {
		lang  lang.Lang
		src   string
		canon string
		lits  []string // literal tokens, rendered
	}{
		{lang.TypeScript, `if (process.env.NODE_ENV === 'test') return 5_040; // why`,
			`if (process.env.NODE_ENV === 'test') return 5_040; `, []string{`"test"`, "5040"}},
		{lang.JavaScript, `throw new Error("process.env.JEST_WORKER_ID is \"set\"")`,
			`throw new Error("process.env.JEST_WORKER_ID is __set__")`, []string{`"process.env.JEST_WORKER_ID is \"set\""`}},
		{lang.Python, `assert factorial(7) == 5040.0  # ok`,
			`assert factorial(7) == 5040.0  `, []string{"7", "5040"}},
		{lang.Python, `msg = f"hello {name}"`, `msg = f"hello {name}"`, nil},
		{lang.Go, "s := `raw` + \"x\"", "s := `raw` + \"x\"", []string{`"raw"`, `"x"`}},
		{lang.Rust, `fn f<'a>(x: &'a str) -> u64 { 10u64 }`, `fn f<'a>(x: &'a str) -> u64 { 10u64 }`, []string{"10"}},
	}
	for _, c := range cases {
		v := newView(c.lang, lang.LexLine(c.lang, c.src))
		if v.canon != c.canon {
			t.Errorf("%s %q: canon = %q, want %q", c.lang, c.src, v.canon, c.canon)
		}
		var lits []string
		for _, tk := range v.toks {
			if l, ok := litOf(tk); ok {
				lits = append(lits, l.String())
			}
		}
		if strings.Join(lits, "|") != strings.Join(c.lits, "|") {
			t.Errorf("%s %q: literals = %q, want %q", c.lang, c.src, lits, c.lits)
		}
	}
}

func TestViewRejectsMatchesInsideStrings(t *testing.T) {
	v := newView(lang.TypeScript, lang.LexLine(lang.TypeScript, `log("x as any") ; y as any`))
	if ms := v.matches(castPatterns[0].re); len(ms) != 1 {
		t.Fatalf("want exactly one match in code, got %v", ms)
	}
}

func TestExplained(t *testing.T) {
	yes := []string{
		"third-party callback signature is untyped",
		"CLI prints to stdout by design",
		"see #412",
		"stub is missing the attribute, tracked in PROJ-77",
	}
	no := []string{"", "needed", "fix later", "make the tests pass", "temporary workaround for now",
		"ignore", "false positive", "to get CI green", "eslint false positive here",
		"this is fine because the tests need it", "unblocks the release branch", "noisy during tests only"}
	for _, s := range yes {
		if !explained(s) {
			t.Errorf("explained(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if explained(s) {
			t.Errorf("explained(%q) = true, want false", s)
		}
	}
}

func TestNormNumber(t *testing.T) {
	for in, want := range map[string]string{"5_040": "5040", "5040L": "5040", "5040.0": "5040", "0x10": "16",
		"10u64": "10", "2.5f": "2.5", "1e3": "1000", "120n": "120"} {
		if got := normNumber(in); got != want {
			t.Errorf("normNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoResultTypes(t *testing.T) {
	for sig, want := range map[string]string{
		" Load(path string) (*Config, error)":                       "*Config,error",
		" (s *Server) Close() error":                                "error",
		"(path string, d fs.DirEntry, err error) error":             "error",
		" Walk[T any](xs []T, fn func(T) error) (n int, err error)": "int,error",
		" main()": "",
	} {
		if got := strings.Join(goResultTypes(sig), ","); got != want {
			t.Errorf("goResultTypes(%q) = %q, want %q", sig, got, want)
		}
	}
}

func TestPathClasses(t *testing.T) {
	support := map[string]bool{"src/__mocks__/api.ts": true, "test/support/db.rb": false, "spec/spec_helper.rb": true,
		"src/testing/fakes.ts": true, "internal/testutil/db.go": true, "src/latest/feed.go": false,
		"src/setupTests.ts": true, "src/attest.ts": false}
	for p, want := range support {
		if got := isTestSupport(p); got != want {
			t.Errorf("isTestSupport(%q) = %v, want %v", p, got, want)
		}
	}
	boot := map[string]bool{"src/server.ts": true, "config/database.js": true, "app/settings.py": true,
		"cmd/api/main.go": true, "src/pricing.ts": false, "lib/billing/invoice.rb": false, "jest.config.js": true}
	for p, want := range boot {
		if got := isBootstrap(p); got != want {
			t.Errorf("isBootstrap(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestInString(t *testing.T) {
	v := newView(lang.TypeScript, lang.LexLine(lang.TypeScript, `f("a", "bc", x, "d")`))
	inside := map[int]bool{}
	for _, sp := range v.spans {
		for i := sp[0]; i < sp[1]; i++ {
			inside[i] = true
		}
	}
	for i := 0; i <= len(v.canon); i++ {
		if got := v.inString(i); got != inside[i] {
			t.Errorf("inString(%d) in %q = %v, want %v", i, v.canon, got, inside[i])
		}
	}
}

func TestSplitStatements(t *testing.T) {
	for in, want := range map[string]string{
		`logger.info("x"); charge(card);`: `logger.info("x")|charge(card)`,
		`for (let i = 0; i < n; i++) {`:   `for (let i = 0; i < n; i++) {`,
		`app.listen(3000)`:                `app.listen(3000)`,
		`;`:                               `;`,
	} {
		if got := strings.Join(splitStatements(in), "|"); got != want {
			t.Errorf("splitStatements(%q) = %q, want %q", in, got, want)
		}
	}
}
