package escapes_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/tranhuuhuy297/narc/internal/rules/escapes"
	"github.com/tranhuuhuy297/narc/internal/ruletest"
)

func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }

// TestCorpusSize keeps every rule backed by enough cheating and legitimate
// scenarios.
func TestCorpusSize(t *testing.T) {
	minLegit := map[string]int{"hardcoded-answer": 12, "test-env-branch": 12}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"type-check-suppressed", "any-cast", "lint-suppressed", "coverage-ignored",
		"error-swallowed", "test-env-branch", "hardcoded-answer"} {
		cheat, legit := 0, 0
		for _, e := range entries {
			name := e.Name()
			switch {
			case strings.HasPrefix(name, rule+"-legit-"):
				legit++
			case strings.HasPrefix(name, rule+"-"):
				cheat++
			}
		}
		wantLegit := minLegit[rule]
		if wantLegit == 0 {
			wantLegit = 8
		}
		if cheat < 8 || legit < wantLegit {
			t.Errorf("%s: %d cheating and %d legitimate cases, want at least 8 and %d", rule, cheat, legit, wantLegit)
		}
	}
}

// TestMessages checks that messages name what happened.
func TestMessages(t *testing.T) {
	want := map[string]string{
		"lint-suppressed-widened":                      "no-alert",
		"type-check-suppressed-ts-ignore":              "@ts-ignore",
		"hardcoded-answer-ts-factorial":                "n == 7 to return 5040",
		"error-swallowed-python-except-pass":           "except Exception: pass",
		"test-env-branch-node-env-return":              "behaves differently under tests",
		"any-cast-request-user":                        "`req`",
		"coverage-ignored-istanbul-branch":             "istanbul ignore next",
		"test-env-branch-go-env-alias":                 `compares env (read from APP_ENV) with "test"`,
		"lint-suppressed-eslint-inline-config":         "@typescript-eslint/no-explicit-any in the whole file",
		"lint-suppressed-spotbugs":                     "SpotBugs bug pattern EI_EXPOSE_REP",
		"error-swallowed-dart-on-catch":                "on FormatException catch",
		"any-cast-parenthesized-double-cast":           "as unknown as User",
		"hardcoded-answer-python-match":                "n == 27 to return 111",
		"type-check-suppressed-python-module-ignore":   "in the whole module",
		"error-swallowed-node-process-handler":         `process.on("unhandledRejection")`,
		"hardcoded-answer-existing-test-ts":            "used by the existing test at src/text/slugify.test.ts:9",
		"hardcoded-answer-existing-go-package-test":    "to return 0x8f1b3c2d",
		"test-env-branch-switch-node-env":              `switches on NODE_ENV and handles "test" separately`,
		"error-swallowed-promise-then-rejection":       "rejection handler",
		"error-swallowed-js-void-error":                "only does `void err`",
		"coverage-ignored-jacoco-generated":            "`@Generated` added",
		"type-check-suppressed-tsx-jsx-comment":        "on the next line",
		"any-cast-python-wrapped-cast":                 "`payload[\"user\"]`",
		"lint-suppressed-pylint-disable-next":          "broad-exception-caught on the next line",
		"hardcoded-answer-alternative-inputs":          "n == 12 || n == 13 to return 78",
		"coverage-ignored-many-in-new-file":            "6 coverage exclusions added in this file",
		"lint-suppressed-legit-realworld-revive-style": "`//nolint:revive` added",
	}
	for name, sub := range want {
		c, err := ruletest.Load(filepath.Join("testdata", name+".case"))
		if err != nil {
			t.Fatal(err)
		}
		res := c.Run(nil)
		ok := false
		for _, f := range res.Active() {
			if strings.Contains(f.Message, sub) {
				ok = true
			}
		}
		if !ok {
			var msgs []string
			for _, f := range res.Active() {
				msgs = append(msgs, f.Message)
			}
			t.Errorf("%s: no message contains %q; got %q", name, sub, msgs)
		}
	}
}

// TestCorpusPatchOnly replays the corpus as bare patches (no full file
// contents, as with `narc scan --patch`): rules must not crash, legitimate
// changes must stay clean, and cheats must not gain unexpected findings.
// Severities that are part of a rule's contract (notes in test code and
// for explained suppressions, warnings in bootstrap files) are pinned in the
// case files as `expect: rule@severity`.
func TestCorpusPatchOnly(t *testing.T) { ruletest.RunDirPatchOnly(t, "testdata") }

// TestLineEndings replays the corpus with CRLF line endings and with a
// UTF-8 byte-order mark: files saved on Windows must get the same verdict.
func TestLineEndings(t *testing.T) {
	cases, err := ruletest.LoadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]func(string) string{
		"crlf": func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") },
		"bom":  func(s string) string { return "\ufeff" + s },
	}
	for _, c := range cases {
		want := strings.Join(ruletest.Got(c.Run(nil)), ",")
		for name, conv := range variants {
			v := *c
			v.Old, v.New = map[string][]byte{}, map[string][]byte{}
			for p, b := range c.Old {
				v.Old[p] = []byte(conv(string(b)))
			}
			for p, b := range c.New {
				v.New[p] = []byte(conv(string(b)))
			}
			res := v.Run(nil)
			for _, e := range res.Errors {
				t.Errorf("%s (%s): %s", c.Name, name, e)
			}
			if got := strings.Join(ruletest.Got(res), ","); got != want {
				t.Errorf("%s (%s): got [%s], want [%s] as with LF endings", c.Name, name, got, want)
			}
		}
	}
}

// TestOddInput feeds the rules malformed and extreme files: they must not
// crash or hang, with or without full file contents.
func TestOddInput(t *testing.T) {
	huge := strings.Repeat("x = y as any; ", 20000)
	deep := strings.Repeat("{", 5000) + strings.Repeat("}", 5000)
	files := map[string]string{
		"src/empty.ts":          "",
		"src/blank.py":          "\n\n\n",
		"src/huge.ts":           "const a = 1;\n" + huge + "\n",
		"src/deep.js":           "function f() " + deep + "\ntry { f() } catch (e) {\n",
		"src/open_comment.go":   "package x\n/* never closed\nfunc f() error {\n\tif err != nil {\n\t\treturn nil\n",
		"src/open_string.py":    "s = '''never closed\nexcept Exception:\n    pass\n",
		"src/bad_utf8.rb":       "x = \"\xff\xfe\" rescue nil\nbegin\n  y\nrescue\nend\n",
		"src/unicode.swift":     "let 名前 = \"🙂\"\ntry? 名前.save()\nif 名前 == \"テスト\" { return \"✓\" }\n",
		"src/switch.kt":         "fun f(n: Int) = when (n) {\n    7 -> 5040\n",
		"src/lone_catch.java":   "class A { void f() { } catch (Exception e) {\n",
		"src/cfg_test.rs":       "#[cfg(test)]\nmod tests {\n    fn t() { if cfg!(test) { return 1 }\n",
		"src/elixir.ex":         "defmodule A do\n  def f, do: Mix.env() == :test\n",
		"src/a_test.go":         "package x\nfunc TestF(t *testing.T) { if F(\"\") != \"\" {} }\n",
		"src/directive_only.cs": "#pragma warning disable\n#nullable disable\n[ExcludeFromCodeCoverage",
	}
	for path, content := range files {
		for _, deleted := range []bool{false, true} {
			for _, patchOnly := range []bool{false, true} {
				c := &ruletest.Case{Name: path, Old: map[string][]byte{}, New: map[string][]byte{}, PatchOnly: patchOnly}
				if deleted {
					c.Old[path] = []byte(content)
				} else {
					c.New[path] = []byte(content)
					c.Old[path] = []byte("// placeholder\n")
				}
				start := time.Now()
				res := c.Run(nil)
				for _, e := range res.Errors {
					t.Errorf("%s (deleted=%v, patch-only=%v): %s", path, deleted, patchOnly, e)
				}
				if d := time.Since(start); d > slowdown*5*time.Second {
					t.Errorf("%s (deleted=%v, patch-only=%v): took %s", path, deleted, patchOnly, d)
				}
				if deleted && len(res.Active()) > 0 {
					t.Errorf("%s: deleting a file reported %v", path, ruletest.Got(res))
				}
			}
		}
	}
}

// TestExistingTestVariants checks hardcoded-answer against unchanged test
// files in odd shapes: it must not crash, and line endings must not matter.
func TestExistingTestVariants(t *testing.T) {
	oldSrc := "export function factorial(n: number): number {\n  return n <= 1 ? 1 : n * factorial(n - 2);\n}\n"
	newSrc := "export function factorial(n: number): number {\n  if (n === 7) return 5040;\n  return n <= 1 ? 1 : n * factorial(n - 2);\n}\n"
	test := "import { factorial } from \"./factorial\";\n\nit(\"computes 7!\", () => {\n  expect(factorial(7)).toBe(5040);\n});\n"
	variants := []struct {
		name, content string
		fires         bool
	}{
		{"lf", test, true},
		{"crlf", strings.ReplaceAll(test, "\n", "\r\n"), true},
		{"bom", "\ufeff" + test, true},
		{"empty", "", false},
		{"binary", "\x00\xff\xfe" + strings.Repeat("\x00", 1000), false},
		{"huge", test + strings.Repeat("// padding\n", 200000), false}, // over the size limit: skipped
		{"one-line", strings.Repeat("expect(factorial(7)).toBe(5040); ", 5000), true},
	}
	for _, v := range variants {
		c := &ruletest.Case{Name: v.name,
			Old: map[string][]byte{"src/factorial.ts": []byte(oldSrc), "src/factorial.test.ts": []byte(v.content)},
			New: map[string][]byte{"src/factorial.ts": []byte(newSrc), "src/factorial.test.ts": []byte(v.content)}}
		res := c.Run(nil)
		for _, e := range res.Errors {
			t.Errorf("%s: %s", v.name, e)
		}
		got := strings.Join(ruletest.Got(res), ",") == "hardcoded-answer"
		if got != v.fires {
			t.Errorf("%s: hardcoded-answer fired = %v, want %v (%v)", v.name, got, v.fires, ruletest.Got(res))
		}
	}
}
