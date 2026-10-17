package tests

import (
	"strings"
	"testing"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/diff"
)

// awkward lines: unbalanced brackets, unterminated strings, markers at the
// very end of a line, unicode, and very long lines.
var awkward = []string{
	"", "(", ")", "((((", "))))", "{", "}", "[", "]", `"`, "'", "`", `"unterminated`, "/*", "*/", "#", "//",
	"expect(", "expect(x", "expect(x).", "expect(x).not.", "expect(x).to", "expect(x).to ", "expect { ", "expect(x, ",
	"it.skip", "it.skip(", "it.skipIf(", "it.skipIf(x)(", "xit(", "test.skip(cond", "this.skip(", "pending(",
	"@pytest.mark.skipif(", "@pytest.mark.xfail(", "pytest.skip(", "@unittest.skipIf(", "self.skipTest(",
	"t.Skip(", "t.Skipf(", "#[cfg_attr(", "#[cfg_attr(x", "#[ignore =", "@Disabled(", "@DisabledIf(",
	"assumeTrue(", "[Fact(Skip =", "[Ignore(", "Skip.If(", "xit \"", "skip \"", "it \"x\", skip:", "->skip(",
	"XCTSkipIf(", "@Test(.disabled(", "}, skip:", "@tag skip:", "TEST(A, DISABLED_", "assert", "assert ",
	"assert (", "assert (x,", "assert x ==", "assert == x", "assert_eq!(", "assert_eq!(x,", "assert!(", "#expect(",
	"self.assertEqual(", "self.assertEqual(x,", "assertEquals(\"m\",", "Assert.That(", "Assert.That(x,",
	"x.Should().", "x.Should().Be(", "x shouldBe", "assert.Equal(t,", "assert.InDelta(t, 1, 2,", "toBeCloseTo(",
	"pytest.approx(1, rel=", "jest.retryTimes(", "this.retries(", "test.describe.configure(", "@pytest.mark.flaky(",
	"@RetryingTest(", "[RetryFact(", "#[retry(", "retries:", "retry:", "--reruns", "--reruns=", "pytest --reruns",
	"// expect(x).toBe(1)", "# assert x == 1", "/* assertEquals(1, x) */", "expect(\"a(b\").toBe(\"a(b\")",
	"expect(`${a}`).toBe(`${a}`)", "assert 'a' == \"a\"", "assert 1 < 2 < 3", "assert x ? y : z", "assert a -> b",
	"expect(ü).toBe(ü) ✓ → ünïcödé", strings.Repeat("expect(x).toBe(x); ", 200), strings.Repeat("(", 500),
	// continuation lines, block delimiters and the other hardened forms
	".toBe(", ").toBe(true)", "}).toThrow(", "  .", "->toBe(", strings.Repeat(").x", 300), `"""`, "'''", "=begin", "=end",
	"/* expect(x).toBe(1)", "*/", "st.Skip(", "test.Skip()", "QUnit.only(", "QUnit.module.skip(", "focus", "focus \"x\" do",
	`"f:x" {`, `test("!x") {`, "flaky = True", "#[cfg_attr(all(), ignore)]", "#[cfg_attr(not(any()), ignore)]",
	"@pt.mark.skip", "<p>it.skip(</p>", "test('a', () => f(skip: 1), skip: true);", "}, solo: true, retry: 3);",
	"\"quarantineMode\": true", "cucumber-js --retry 2", "expect(x === x).toBe(true)", "assert a and b == c and",
}

var awkwardPaths = []string{
	"a.test.ts", "a.spec.js", "test_a.py", "a_test.go", "src/lib.rs", "tests/a.rs", "src/test/java/ATest.java",
	"src/test/kotlin/ATest.kt", "tests/ATests.cs", "spec/a_spec.rb", "tests/ATest.php", "Tests/ATests.swift",
	"test/a_test.dart", "test/a_test.exs", "tests/a_test.cc", "src/test/scala/ASuite.scala", "tests/ASpec.groovy",
	"playwright.config.ts", "pytest.ini", "pom.xml", "build.gradle", ".github/workflows/ci.yml", "package.json",
	"Makefile", "scripts/test.sh", "cypress/component/a.cy.tsx", "BUILD.bazel", ".testcaferc.json",
}

func TestRulesNeverPanic(t *testing.T) {
	content := strings.Join(awkward, "\n") + "\n"
	reversed := make([]string, len(awkward))
	for i, l := range awkward {
		reversed[len(awkward)-1-i] = l
	}
	other := strings.Join(reversed, "\n") + "\n"
	for _, p := range awkwardPaths {
		for _, patchOnly := range []bool{false, true} {
			src := &core.MemSource{OldFiles: map[string][]byte{p: []byte(other)}, NewFiles: map[string][]byte{p: []byte(content)}}
			fd := diff.Compute(p, p, []byte(other), []byte(content), true, true, diff.DefaultContext)
			var cs core.ContentSource = src
			if patchOnly {
				cs = nil
			}
			a := &core.Analysis{Files: []*core.FileInfo{core.NewFileInfo(fd, cs, core.Settings{})}}
			for _, r := range []*core.Rule{skipAdded, focusAdded, tautology, assertionWeakened, assertionCommented, retryAdded} {
				func() {
					defer func() {
						if e := recover(); e != nil {
							t.Errorf("%s on %s (patch-only=%v) panicked: %v", r.ID, p, patchOnly, e)
						}
					}()
					r.Check(a)
				}()
			}
		}
	}
}
