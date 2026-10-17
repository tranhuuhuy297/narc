// Package testblocks finds test functions in lexed source code and counts
// the assertions inside them.
//
// It works on []lang.LexedLine (see package lang). Structure — brackets,
// keywords, indentation, test declarations — is matched on each line's Code
// text, where comments are removed and string contents are blanked, so a
// brace or a keyword inside a string or comment never confuses it. Test
// names come from the string literals themselves (via NoComment/Strings).
//
// API:
//
//	Find(l, lines) []Block         every test in lines, in source order
//	CountAssertions(l, code) int   assertions on one line of Code text
//	IsAssertion(l, code) bool      CountAssertions(l, code) > 0
//	Supported(l) bool              whether Find knows l's test frameworks
//
// A Block carries the test's qualified and bare names, its line range
// (including decorators/attributes), the line that names it, its assertion
// count and the line of every assertion, whether it is unconditionally
// skipped, and whether its end lies beyond the input (Open). Subtests that
// are not separate declarations (Go t.Run, node:test t.test, Catch2
// SECTION) count toward their parent.
//
// Assertions that cannot check anything are not counted but listed:
// Unreachable holds those after a bare return at the top of the body
// (Return is that return's line) or inside if (false) / if False: /
// #if 0, and Inert holds expectations without a matcher, such as a lone
// expect(total) or assertThat(x).
//
// Recognized frameworks:
//
//   - JavaScript/TypeScript: it/test/specify (+ x/f prefixes and the
//     .skip/.only/.todo/.each/.for/.concurrent/.failing/... modifiers),
//     describe/context/suite nesting, Playwright test() and test.describe,
//     node:test, Deno.test.
//   - Python: pytest test* functions (incl. async) and test* methods of
//     classes that are collected: named Test*/*Test/*Tests, or with a base
//     class (unittest.TestCase); decorators are part of the block.
//   - Go: func TestX/BenchmarkX/FuzzX/ExampleX (incl. suite methods);
//     "// Output:" comments count as the assertion of an example.
//   - Rust: fns under #[test], #[tokio::test], #[rstest], #[test_case(..)],
//     ... ; #[should_panic] counts as an assertion.
//   - Java/Kotlin: @Test, @ParameterizedTest, @RepeatedTest, @TestFactory,
//     @TestTemplate, @Property, JUnit Pioneer @RetryingTest/@CartesianTest,
//     @FuzzTest, @ArchTest; JUnit 3 public void testX(); Kotlin backtick
//     names; Kotest/Spek/ScalaTest test("..") { } calls.
//   - C#: [Fact], [Theory] and their variants ([RetryFact], [WpfFact], ...),
//     [Test], [TestMethod], [TestCase], ...
//   - PHP: PHPUnit test* methods, @test docblocks, #[Test]; Pest it()/test().
//   - Ruby: RSpec it/specify/example/scenario/its with do..end or { },
//     describe/context nesting; minitest def test_*.
//   - Swift: XCTest func test*(), Swift Testing @Test.
//   - Dart: test()/testWidgets()/group().
//   - Elixir: ExUnit test "..." do ... end, describe nesting.
//   - C/C++: GoogleTest TEST/TEST_F/TEST_P, Catch2/doctest TEST_CASE.
//
// Assertions are expect(...), assert*, t.Error*/t.Fatal*/t.Fail*, testify
// assert./require., gomega Expect(...), assert!/assert_eq!/prop_assert*,
// assertThat/assertEquals/verify(...), Assert.*, $this->assert*/expect*,
// XCTAssert*, #expect/#require, should/must matchers, pytest.raises/warns,
// mock .assert_called*, snapshot matchers, plus calls to helpers whose
// name starts with assert/expect (anywhere) or verify/check/should/must/
// ensure (as a statement), so extracting assertions into a helper does
// not look like removing them. Asymmetric matchers (expect.any(...)) are
// arguments, not assertions, and NUnit's Assert.Pass/Ignore/Inconclusive/
// Warn/Multiple check nothing themselves.
//
// Everything is heuristic and best-effort. Nothing here fails or panics on
// malformed or partial input such as the fragment of a file shown in a diff
// hunk.
package testblocks
