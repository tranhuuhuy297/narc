package testblocks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/lang"
)

// want describes an expected block. Lines are checked only when non-zero.
type want struct {
	name       string
	asserts    int
	skipped    bool
	start, end int
}

func check(t *testing.T, l lang.Lang, src string, wants ...want) {
	t.Helper()
	got := Find(l, lang.Lex(l, src))
	var gs, ws []string
	for _, b := range got {
		gs = append(gs, fmt.Sprintf("%q asserts=%d skipped=%v", b.Name, b.Assertions, b.Skipped))
		if b.Assertions != len(b.AssertionLines) {
			t.Errorf("%q: Assertions=%d but %d AssertionLines", b.Name, b.Assertions, len(b.AssertionLines))
		}
	}
	for _, w := range wants {
		ws = append(ws, fmt.Sprintf("%q asserts=%d skipped=%v", w.name, w.asserts, w.skipped))
	}
	if strings.Join(gs, "\n") != strings.Join(ws, "\n") {
		t.Fatalf("%s blocks:\n got:\n  %s\nwant:\n  %s", l, strings.Join(gs, "\n  "), strings.Join(ws, "\n  "))
	}
	for i, w := range wants {
		b := got[i]
		if w.start != 0 && b.Start != w.start || w.end != 0 && b.End != w.end {
			t.Errorf("%q: lines %d-%d, want %d-%d", b.Name, b.Start, b.End, w.start, w.end)
		}
	}
}

func TestJavaScript(t *testing.T) {
	src := `import { describe, it, expect } from 'vitest'
import { verifyToken } from '../src/jwt'

// it('commented out', () => { expect(1).toBe(1) })
describe('auth', () => {
  it('rejects expired tokens', async () => {
    const token = sign({ exp: 1 })
    await expect(verifyToken(token)).rejects.toThrow('expired')
    expect(audit.log).toHaveBeenCalledWith("token expired: it('x')")
  })

  describe('refresh', () => {
    test.each([[1, 2], [3, 4]])('adds %i + %i', (a, b) => {
      expect(add(a, b)).toMatchSnapshot()
    })
    it.skip('rotates keys', () => {
      expect(rotate()).toBe(true); expect(keys()).toHaveLength(2)
    })
    it.todo('revokes sessions')
  })
})

test('top level', () => expect(sum([])).toBe(0))
xit('legacy', function () { assert.equal(f(), 1) })
`
	check(t, lang.TypeScript, src,
		want{name: "auth > rejects expired tokens", asserts: 2, start: 6, end: 10},
		want{name: "auth > refresh > adds %i + %i", asserts: 1, start: 13, end: 15},
		want{name: "auth > refresh > rotates keys", asserts: 2, skipped: true},
		want{name: "auth > refresh > revokes sessions", skipped: true},
		want{name: "top level", asserts: 1, start: 23, end: 23},
		want{name: "legacy", asserts: 1, skipped: true},
	)
}

func TestJavaScriptFrameworks(t *testing.T) {
	// Playwright: test.describe, hooks and steps are not tests.
	check(t, lang.TypeScript, `import { test, expect } from '@playwright/test'
test.describe.serial('checkout', () => {
  test.beforeEach(async ({ page }) => { await page.goto('/') })
  test.use({ locale: 'de-DE' })
  test('pays with card', async ({ page }) => {
    await test.step('fill form', async () => {
      await page.fill('#card', '4242')
    })
    await expect(page.getByText('Thanks')).toBeVisible()
  })
  test.fixme('pays with paypal', async ({ page }) => {})
})
`,
		want{name: "checkout > pays with card", asserts: 1},
		want{name: "checkout > pays with paypal", skipped: true},
	)
	// node:test with subtests, node:assert, supertest, chai, Cypress.
	check(t, lang.JavaScript, `const test = require('node:test')
const assert = require('node:assert')
test('api', async (t) => {
  await t.test('lists users', async () => {
    await request(app).get('/users').expect(200)
    assert.strictEqual(res.body.length, 2)
  })
})
describe("cart", function () {
  it("adds items", function () {
    cart.add(item)
    cart.items.should.have.length(1)
    cy.get('[data-test=total]').should('contain', '9.99').and('be.visible')
  })
  context("when empty", () => {
    specify("total is zero", () => { expect(cart.total).to.equal(0) })
  })
})
`,
		want{name: "api", asserts: 2},
		want{name: "cart > adds items", asserts: 2},
		want{name: "cart > when empty > total is zero", asserts: 1},
	)
}

func TestJavaScriptHelpersAndNonTests(t *testing.T) {
	check(t, lang.JavaScript, `function expectValidUser(u) {
  expect(u.id).toBeDefined()
}
it('creates a user', () => {
  const user = create({ name: 'ann' })
  expectValidUser(user)
  const claims = verifyToken(token) // exercises the code, not an assertion
  checkInvariants(store)
})
it('has a template name ${x}', () => {})
const re = /it\('x'\)/
for (const kind of ['a', 'b']) {
  it(`+"`handles ${kind} input`"+`, () => { expect(parse(kind)).toBeTruthy() })
}
`,
		want{name: "creates a user", asserts: 2},
		want{name: "has a template name ${x}"},
		want{name: "handles ${kind} input", asserts: 1},
	)
}

func TestPython(t *testing.T) {
	src := `import pytest

def helper():
    assert True

@pytest.mark.parametrize(
    "a,b,expected",
    [(1, 2, 3), (-1, -2, -3)],
)
def test_add(a, b, expected):
    """Adds numbers.

    def test_not_a_test(): assert False
    """
    assert add(a, b) == expected

async def test_fetch(client):
    resp = await client.get("/")
    assert resp.status == 200
    with pytest.raises(KeyError):
        resp.json()["missing"]


class TestAuth:
    @pytest.mark.skip(reason="flaky")
    def test_expired(self):
        assert verify(expired) is None

    def test_mock(self, mailer):
        send_welcome(user)
        mailer.send.assert_called_once_with(user.email)

    def _helper(self):
        assert False

class TestUnit(unittest.TestCase):
    def testLegacyName(self):
        self.assertEqual(f(1), 2)
        self.assertRaises(ValueError, f, None)

def test_hollow():
    pass

def test_dead():
    return
    assert compute() == 42
`
	check(t, lang.Python, src,
		want{name: "test_add", asserts: 1, start: 6, end: 15},
		want{name: "test_fetch", asserts: 2, start: 17, end: 21},
		want{name: "TestAuth::test_expired", asserts: 1, skipped: true, start: 25, end: 27},
		want{name: "TestAuth::test_mock", asserts: 1},
		want{name: "TestUnit::testLegacyName", asserts: 2},
		want{name: "test_hollow", asserts: 0},
		want{name: "test_dead", asserts: 0},
	)
}

// TestPythonClassCollection: pytest collects test* methods of Test*
// classes; unittest those of TestCase subclasses. A plain class is not
// collected, so its methods are not tests.
func TestPythonClassCollection(t *testing.T) {
	for _, c := range []struct {
		line string
		want bool
	}{
		{"class TestCheckout:", true},
		{"class CheckoutTests:", true},
		{"class Checkout(unittest.TestCase):", true},
		{"class Checkout(\n    unittest.TestCase,\n):", true},
		{"class Checkout:", false},
		{"class Checkout(object):", false},
		{"class _TestCheckout:", false},
	} {
		src := c.line + "\n    def test_pays(self):\n        assert pay() == 1\n"
		got := len(Find(lang.Python, lang.Lex(lang.Python, src))) == 1
		if got != c.want {
			t.Errorf("%q: test method collected = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestPythonSkips(t *testing.T) {
	check(t, lang.Python, `@unittest.skip("broken")
class TestOld(unittest.TestCase):
    def test_a(self):
        self.assertTrue(x)

def test_b():
    """Doc."""
    pytest.skip("not on CI")
    assert y

@pytest.mark.skipif(sys.platform == "win32", reason="posix")
def test_c():
    assert z
`,
		want{name: "TestOld::test_a", asserts: 1, skipped: true},
		want{name: "test_b", asserts: 1, skipped: true},
		want{name: "test_c", asserts: 1},
	)
}

func TestGo(t *testing.T) {
	src := "package auth\n" + `
func TestMain(m *testing.M) { os.Exit(m.Run()) }

func helper(t *testing.T) { t.Fatal("x") }

func TestVerify(t *testing.T) {
	tests := []struct{ in string; want error }{
		{"expired", ErrExpired},
		{"ok", nil},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := Verify(tc.in); got != tc.want {
				t.Errorf("Verify(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	got, err := Parse("a=1")
	require.NoError(t, err)
	assert.Equal(t, 1, got["a"])
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("mismatch (-want +got):\n%s", diff)
	}
}

func (s *AuthSuite) TestLogin() {
	s.Require().NoError(s.svc.Login("ann"))
	s.Equal(1, s.svc.Sessions())
}

func TestSkipped(t *testing.T) {
	t.Skip("flaky, see #12")
	if Verify("x") != nil {
		t.Error("bad")
	}
}

func BenchmarkVerify(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Verify("ok")
	}
}

func ExampleVerify() {
	fmt.Println(Verify("ok"))
	// Output: <nil>
}

func Testable() {}
`
	check(t, lang.Go, src,
		want{name: "TestVerify", asserts: 1, start: 7, end: 19},
		want{name: "TestParse", asserts: 3},
		want{name: "AuthSuite.TestLogin", asserts: 2},
		want{name: "TestSkipped", asserts: 1, skipped: true},
		want{name: "BenchmarkVerify", asserts: 0},
		want{name: "ExampleVerify", asserts: 1},
	)
}

func TestRust(t *testing.T) {
	src := `pub fn add(a: i32, b: i32) -> i32 { a + b }

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn adds() {
        assert_eq!(add(1, 2), 3);
        assert!(add(0, 0) == 0, "zero {}", "}");
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn fetches() -> Result<(), Error> {
        let r = client().get("/").await?;
        assert_matches!(r.status(), Status::Ok);
        Ok(())
    }

    #[test]
    #[should_panic(expected = "overflow")]
    fn overflows() {
        add(i32::MAX, 1);
    }

    #[rstest]
    #[case(1, 2)]
    #[case(3, 4)]
    fn cases(#[case] a: i32, #[case] b: i32) {
        prop_assert_eq!(add(a, b), add(b, a));
    }

    #[test]
    #[ignore]
    fn slow() {
        debug_assert!(heavy());
    }

    fn not_a_test() { assert!(true) }
}
`
	check(t, lang.Rust, src,
		want{name: "tests::adds", asserts: 2, start: 7, end: 11},
		want{name: "tests::fetches", asserts: 1},
		want{name: "tests::overflows", asserts: 1, start: 20, end: 24},
		want{name: "tests::cases", asserts: 1, start: 26},
		want{name: "tests::slow", asserts: 1, skipped: true},
	)
}

func TestJava(t *testing.T) {
	src := `package com.example;

import static org.junit.jupiter.api.Assertions.*;

@DisplayName("Auth")
class AuthServiceTest {
    private final AuthService service = new AuthService(Clock.fixed());

    @Test
    @DisplayName("rejects expired tokens")
    void rejectsExpiredTokens() throws Exception {
        Token token = service.issue("ann", Duration.ofSeconds(-1));
        assertThrows(TokenExpiredException.class, () -> service.verify(token));
        assertThat(service.audit()).containsExactly("expired");
    }

    @ParameterizedTest
    @ValueSource(strings = {"", " "})
    void rejectsBlankNames(String name) {
        assertFalse(service.isValid(name));
        verify(repo, never()).save(any());
    }

    @Disabled("flaky")
    @Test
    public void legacy() { Assertions.assertEquals(1, service.count()); }

    @Nested
    class WhenLocked {
        @Test
        void refusesLogin() {
            assert service.login("ann") == null;
        }
    }

    private void assertValid(Token t) { assertNotNull(t); }

    public void testJUnit3Style() {
        assertTrue(service.ready());
    }
}
`
	check(t, lang.Java, src,
		want{name: "AuthServiceTest.rejectsExpiredTokens", asserts: 2, start: 9, end: 15},
		want{name: "AuthServiceTest.rejectsBlankNames", asserts: 2, start: 17},
		want{name: "AuthServiceTest.legacy", asserts: 1, skipped: true},
		want{name: "AuthServiceTest.WhenLocked.refusesLogin", asserts: 1},
		want{name: "AuthServiceTest.testJUnit3Style", asserts: 1},
	)
}

func TestKotlin(t *testing.T) {
	src := "class TokenTest {\n" +
		"    private val clock = mockk<Clock>()\n\n" +
		"    @Test\n" +
		"    fun `rejects expired tokens`() {\n" +
		"        val result = verify(expired)\n" +
		"        result shouldBe Invalid\n" +
		"        assertEquals(1, audit.size)\n" +
		"    }\n\n" +
		"    @Test fun parses() = runTest {\n" +
		"        coVerify { repo.load(any()) }\n" +
		"        assertThat(parse(\"a\")).isEqualTo(A)\n" +
		"    }\n" +
		"}\n\n" +
		"class TokenSpec : FunSpec({\n" +
		"    context(\"refresh\") {\n" +
		"        test(\"rotates\") {\n" +
		"            rotate() shouldNotBe null\n" +
		"        }\n" +
		"    }\n" +
		"})\n"
	check(t, lang.Kotlin, src,
		want{name: "TokenTest.rejects expired tokens", asserts: 2, start: 4, end: 9},
		want{name: "TokenTest.parses", asserts: 2},
		want{name: "refresh > rotates", asserts: 1},
	)
}

func TestCSharp(t *testing.T) {
	src := `namespace Shop.Tests
{
    public class CartTests
    {
        [Fact]
        public void AddsItem()
        {
            var cart = new Cart();
            cart.Add(new Item("x", 10m));
            Assert.Single(cart.Items);
            cart.Total.Should().Be(10m);
        }

        [Theory]
        [InlineData(1, 2)]
        [InlineData(3, 4)]
        public async Task Sums(int a, int b)
        {
            var sum = await calc.Add(a, b);
            Assert.Equal(a + b, sum);
            mock.Verify(m => m.Log(It.IsAny<string>()), Times.Once);
        }

        [Fact(Skip = "flaky")]
        public void Skipped() => Assert.True(Flaky());

        private static void AssertValid(Cart c) { Assert.NotNull(c); }
    }
}
`
	check(t, lang.CSharp, src,
		want{name: "CartTests.AddsItem", asserts: 2, start: 5, end: 12},
		want{name: "CartTests.Sums", asserts: 2, start: 14, end: 22},
		want{name: "CartTests.Skipped", asserts: 1, skipped: true},
	)
}

func TestPHP(t *testing.T) {
	src := `<?php
final class InvoiceTest extends TestCase
{
    public function testTotalIncludesTax(): void
    {
        $invoice = new Invoice([new Line(100)]);
        $this->assertSame(107, $invoice->total());
        self::assertCount(1, $invoice->lines());
    }

    /** @test */
    public function it_rejects_negative_amounts(): void
    {
        $this->expectException(InvalidArgumentException::class);
        new Line(-1);
    }

    #[Test]
    public function rendersPdf(): void
    {
        $this->markTestSkipped('needs wkhtmltopdf');
        $this->assertFileExists($path);
    }

    public function testHttp(): void
    {
        $this->get('/invoices')->assertStatus(200)->assertJsonCount(3);
        $mailer->shouldReceive('send')->once();
    }

    private function helper(): void { $this->assertTrue(true); }
}
`
	check(t, lang.PHP, src,
		want{name: "InvoiceTest::testTotalIncludesTax", asserts: 2, start: 4, end: 9},
		want{name: "InvoiceTest::it_rejects_negative_amounts", asserts: 1},
		want{name: "InvoiceTest::rendersPdf", asserts: 1, skipped: true, start: 18},
		want{name: "InvoiceTest::testHttp", asserts: 3},
	)
	// Pest.
	check(t, lang.PHP, `<?php
describe('invoices', function () {
    it('sums lines', function () {
        expect(total([1, 2]))->toBe(3);
    });
    test('pdf', function () {
        expect(pdf())->not->toBeEmpty();
    })->skip();
});
`,
		want{name: "invoices > sums lines", asserts: 1},
		want{name: "invoices > pdf", asserts: 1, skipped: true},
	)
}

func TestRuby(t *testing.T) {
	src := `require "rails_helper"

RSpec.describe User, type: :model do
  let(:user) { build(:user) }

  describe "#valid?" do
    it "requires an email" do
      user.email = nil
      expect(user).not_to be_valid
      expect(user.errors[:email]).to include("can't be blank")
    end

    it { is_expected.to validate_presence_of(:name) }

    context "when admin" do
      it "can delete posts", :aggregate_failures do
        if user.admin?
          user.posts.each do |post|
            expect(post.destroy).to be_truthy
          end
        end
        user.reload.should be_admin
      end

      xit "is skipped" do
        expect(1).to eq(2)
      end

      it "skips inside" do
        skip "until #42"
        expect(x).to eq(1)
      end
    end
  end
end

class UserTest < Minitest::Test
  def test_full_name
    assert_equal "Ann Lee", User.new("Ann", "Lee").full_name
    refute_nil User.find_by(name: "Ann")
  end

  def test_with_block
    [1, 2].each { |n| assert n.positive? }
  end

  def helper_method
    assert true
  end
end
`
	check(t, lang.Ruby, src,
		want{name: "User > #valid? > requires an email", asserts: 2, start: 7, end: 11},
		want{name: "User > #valid? > it { is_expected.to validate_presence_of(:name) }", asserts: 1},
		want{name: "User > #valid? > when admin > can delete posts", asserts: 2, start: 16, end: 23},
		want{name: "User > #valid? > when admin > is skipped", asserts: 1, skipped: true},
		want{name: "User > #valid? > when admin > skips inside", asserts: 1, skipped: true},
		want{name: "UserTest > test_full_name", asserts: 2},
		want{name: "UserTest > test_with_block", asserts: 1},
	)
}

func TestElixir(t *testing.T) {
	src := `defmodule MyApp.AccountsTest do
  use MyApp.DataCase, async: true

  describe "register/1" do
    test "creates a user", %{repo: repo} do
      assert {:ok, user} = Accounts.register(%{email: "a@b.c"})
      assert user.email == "a@b.c"
      refute user.admin
    end

    @tag :skip
    test "sends a welcome email" do
      Enum.each(users, fn u ->
        assert_receive {:email, ^u}
      end)
    end

    test "not implemented yet"
  end

  test "outside describe", do: assert(1 + 1 == 2)
end
`
	check(t, lang.Elixir, src,
		want{name: "register/1 > creates a user", asserts: 3, start: 5, end: 9},
		want{name: "register/1 > sends a welcome email", asserts: 1, skipped: true, start: 12, end: 16},
		want{name: "register/1 > not implemented yet", skipped: true},
		want{name: "outside describe", asserts: 1},
	)
}

func TestSwift(t *testing.T) {
	src := `import XCTest
@testable import Auth

final class TokenTests: XCTestCase {
    func testRejectsExpired() throws {
        let token = try XCTUnwrap(Token(expiry: .distantPast))
        XCTAssertThrowsError(try verifier.verify(token))
    }

    func testSkipped() throws {
        throw XCTSkip("needs keychain")
    }

    func helper() { XCTAssertTrue(true) }
}

@Suite struct ParserTests {
    @Test("parses headers") func parsesHeaders() async throws {
        let h = try #require(parse("a: b"))
        #expect(h["a"] == "b")
    }

    @Test(.disabled("flaky")) func flaky() {
        #expect(Bool.random())
    }
}
`
	check(t, lang.Swift, src,
		want{name: "TokenTests.testRejectsExpired", asserts: 2, start: 5, end: 8},
		want{name: "TokenTests.testSkipped", skipped: true},
		want{name: "ParserTests.parsesHeaders", asserts: 2, start: 18},
		want{name: "ParserTests.flaky", asserts: 1, skipped: true},
	)
}

func TestDart(t *testing.T) {
	src := `void main() {
  group('Counter', () {
    test('starts at zero', () {
      expect(Counter().value, 0);
    });
    testWidgets('increments', (tester) async {
      await tester.tap(find.byIcon(Icons.add));
      await tester.pump();
      expect(find.text('1'), findsOneWidget);
      verify(() => analytics.log('tap')).called(1);
    });
    blocTest<CounterCubit, int>('emits [1]', build: () => CounterCubit(), act: (c) => c.inc(), expect: () => [1]);
    test('later', () {
      expect(slow(), completes);
    }, skip: 'needs network');
  });
}
`
	check(t, lang.Dart, src,
		want{name: "Counter > starts at zero", asserts: 1, start: 3, end: 5},
		want{name: "Counter > increments", asserts: 2},
		want{name: "Counter > emits [1]"},
		want{name: "Counter > later", asserts: 1, skipped: true},
	)
}

func TestCpp(t *testing.T) {
	src := `#include <gtest/gtest.h>

TEST(ParserTest, ParsesEmpty) {
  Parser p;
  ASSERT_TRUE(p.Parse(""));
  EXPECT_EQ(p.size(), 0u);
}

TEST_F(DbTest, DISABLED_Migrates) {
  EXPECT_TRUE(db.Migrate());
}

TEST_CASE("vectors can be sized", "[vector]") {
  std::vector<int> v(5);
  REQUIRE(v.size() == 5);
  SECTION("resizing bigger") {
    v.resize(10);
    CHECK(v.capacity() >= 10);
  }
}
`
	check(t, lang.Cpp, src,
		want{name: "ParserTest.ParsesEmpty", asserts: 2, start: 3, end: 7},
		want{name: "DbTest.DISABLED_Migrates", asserts: 1, skipped: true},
		want{name: "vectors can be sized", asserts: 2},
	)
}

func TestScala(t *testing.T) {
	check(t, lang.Scala, `class StackSpec extends AnyFunSpec {
  describe("A Stack") {
    it("pops in LIFO order") {
      val s = Stack(1, 2)
      s.pop() shouldBe 2
      assert(s.size == 1)
    }
    ignore("is slow") {
      assertResult(3)(slow())
    }
  }
}
`,
		want{name: "A Stack > pops in LIFO order", asserts: 2},
		want{name: "A Stack > is slow", asserts: 1, skipped: true},
	)
}

func TestCountAssertions(t *testing.T) {
	cases := []struct {
		l    lang.Lang
		code string
		want int
	}{
		{lang.JavaScript, `expect(a).toBe(1); expect(b).not.toEqual(2)`, 2},
		{lang.JavaScript, `const x = expectedValue(1)`, 0},
		{lang.JavaScript, `checkout(cart)`, 0},
		{lang.JavaScript, `const ok = verifyToken(t)`, 0},
		{lang.JavaScript, `verifyToken(t)`, 1},
		{lang.JavaScript, `await expect(page).toHaveURL(/done/)`, 1},
		{lang.JavaScript, `expect.assertions(2)`, 1},
		{lang.JavaScript, `await expect.soft(page.getByRole('status')).toHaveText('Saved')`, 1},
		// Asymmetric matchers are arguments, not assertions of their own.
		{lang.JavaScript, `expect(send).toHaveBeenCalledWith('a@b.c', expect.stringContaining('inv-1'))`, 1},
		{lang.JavaScript, `expect(user).toEqual(expect.objectContaining({ id: expect.any(String) }))`, 1},
		{lang.JavaScript, `await request(app).get('/users').expect(200)`, 1},
		{lang.JavaScript, `function assertOk(r) {`, 0},
		{lang.JavaScript, `t.is(sum(1, 2), 3)`, 1},
		{lang.TypeScript, `expectTypeOf(fn).returns.toEqualTypeOf<string>()`, 1},
		{lang.Python, `assert result == 3`, 1},
		{lang.Python, `assert_that(result).is_equal_to(3)`, 1},
		{lang.Python, `self.assertEqual(a, b)`, 1},
		{lang.Python, `mock_send.assert_called_once_with("a")`, 1},
		{lang.Python, `with pytest.raises(ValueError):`, 1},
		{lang.Python, `assertion = build()`, 0},
		{lang.Python, `def assert_valid(x):`, 0},
		{lang.Go, `if got != want { t.Errorf("got %v", got) }`, 1},
		{lang.Go, `require.NoError(t, err)`, 1},
		{lang.Go, `require.Equal(t, []any{"bash"}, allowedTools(t, cfg))`, 1},
		{lang.Go, `require.True(t, ok, "expected a permissions object")`, 1},
		{lang.Go, `require.Len(t, items, 2)`, 1},
		{lang.Go, `re := regexp.MustCompile("x")`, 0},
		{lang.Go, `Expect(err).NotTo(HaveOccurred())`, 1},
		{lang.Go, `c.Assert(v, qt.Equals, 1)`, 1},
		{lang.Go, `testutil.AssertJSONEqual(t, want, got)`, 1},
		{lang.Go, `ok := auth.CheckPassword(pw)`, 0},
		{lang.Rust, `assert_eq!(a, b); assert!(c);`, 2},
		{lang.Rust, `let v = parse(s).expect("valid");`, 0},
		{lang.Java, `assertThat(list).hasSize(2).contains("a");`, 1},
		{lang.Java, `verify(repo, times(1)).save(user);`, 1},
		{lang.Java, `Assertions.assertTimeout(ofSeconds(1), () -> run());`, 1},
		{lang.Kotlin, `result shouldBe 42`, 1},
		{lang.Kotlin, `verify { repo.save(any()) }`, 1},
		{lang.Kotlin, `assertFailsWith<IllegalArgumentException> { cart.add("a", -1) }`, 1},
		{lang.CSharp, `Assert.Throws<ArgumentException>(() => Parse(""));`, 1},
		{lang.CSharp, `result.Should().BeEquivalentTo(expected);`, 1},
		// NUnit calls that end or group a test without checking anything.
		{lang.CSharp, `Assert.Pass("done");`, 0},
		{lang.CSharp, `Assert.Inconclusive("needs a database");`, 0},
		{lang.CSharp, `Assert.Multiple(() => { Assert.That(a, Is.EqualTo(1)); });`, 1},
		{lang.PHP, `$this->assertSame(1, $x);`, 1},
		{lang.PHP, `$response->assertOk()->assertSee('Hi');`, 2},
		{lang.Ruby, `expect { run }.to raise_error(ArgumentError)`, 1},
		{lang.Ruby, `assert_equal 3, sum`, 1},
		{lang.Ruby, `_(sum).must_equal 3`, 1},
		{lang.Ruby, `assertion = nil`, 0},
		{lang.Elixir, `assert {:ok, _} = Repo.insert(cs)`, 1},
		{lang.Elixir, `refute_receive :done`, 1},
		{lang.Swift, `XCTAssertEqual(a, b); #expect(c)`, 2},
		{lang.Cpp, `EXPECT_EQ(a, b); REQUIRE(c);`, 2},
		{lang.Dart, `expect(find.text('1'), findsOneWidget);`, 1},
	}
	for _, c := range cases {
		code := lang.LexLine(c.l, c.code).Code
		if got := CountAssertions(c.l, code); got != c.want {
			t.Errorf("%s %q: got %d assertions, want %d", c.l, c.code, got, c.want)
		}
		if IsAssertion(c.l, code) != (c.want > 0) {
			t.Errorf("%s %q: IsAssertion disagrees with count", c.l, c.code)
		}
	}
}

// TestJSXApostrophe: an apostrophe in JSX text (Don't) is not a string
// delimiter, so the first test ends at its own closing line.
func TestJSXApostrophe(t *testing.T) {
	check(t, lang.TypeScript, `describe('Banner', () => {
  it('shows the message', () => {
    render(<Banner>Don't panic</Banner>)
    expect(screen.getByRole('alert')).toBeVisible()
  })

  it('can be dismissed', async () => {
    await user.click(screen.getByRole('button'))
    expect(screen.queryByRole('alert')).toBeNull()
  })
})
`,
		want{name: "Banner > shows the message", asserts: 1, start: 2, end: 5},
		want{name: "Banner > can be dismissed", asserts: 1, start: 7, end: 10},
	)
}

// TestUnbalancedTestIsCutOff: tests never nest, so a test whose brackets do
// not balance (a syntax error mid-edit, or code the lexer misreads) is cut
// off before the next test instead of swallowing it.
func TestUnbalancedTestIsCutOff(t *testing.T) {
	check(t, lang.TypeScript, `it('first', () => {
  expect(parse(`+"`"+`x`+"`"+`).toBe(1)
  render(
})

it('second', () => {
  expect(b).toBe(2)
})
`,
		want{name: "first", asserts: 1, start: 1, end: 5},
		want{name: "second", asserts: 1, start: 6, end: 8},
	)
}

func TestStringsAndCommentsIgnored(t *testing.T) {
	check(t, lang.JavaScript, `it('logs', () => {
  log("expect(x).toBe(1) and assert(y)")
  // expect(z).toBe(2)
  /* assert.equal(a, b) */
})
`, want{name: "logs", asserts: 0})
}

// TestFragments checks partial input as seen in diff hunks: unbalanced
// closers, a test cut off at the end, and a body without its header.
func TestFragments(t *testing.T) {
	check(t, lang.TypeScript, `    expect(a).toBe(1)
  })

  it('second', () => {
    expect(b).toBe(2)
`, want{name: "second", asserts: 1})
	got := Find(lang.TypeScript, lang.Lex(lang.TypeScript, "  it('cut', () => {\n    expect(1).toBe(1)\n"))
	if len(got) != 1 || !got[0].Open {
		t.Fatalf("cut-off test should be Open: %+v", got)
	}
	got = Find(lang.Go, lang.Lex(lang.Go, "func TestCut(t *testing.T) {\n\tif x {\n"))
	if len(got) != 1 || !got[0].Open {
		t.Fatalf("cut-off Go test should be Open: %+v", got)
	}
}

// TestNoPanic feeds every supported language truncated and garbled input.
func TestNoPanic(t *testing.T) {
	samples := []string{
		"it('x', () => { expect(1).toBe(1) })\n",
		"def test_x():\n    assert 1\n",
		"func TestX(t *testing.T) { t.Error(1) }\n",
		"#[test]\nfn x() { assert!(true) }\n",
		"@Test void x() { assertTrue(true); }\n",
		"[Fact] public void X() { Assert.True(true); }\n",
		"it 'x' do\n  expect(1).to eq(1)\nend\n",
		"test \"x\" do\n  assert 1\nend\n",
		"func testX() { XCTAssert(true) }\n",
		"TEST(A, B) { EXPECT_TRUE(1); }\n",
		"test('x', () { expect(1, 1); });\n",
		"public function testX() { $this->assertTrue(true); }\n",
		"`'\"(){}[]<>#[@ do end fn => : ;\n",
	}
	langs := []lang.Lang{lang.JavaScript, lang.TypeScript, lang.Python, lang.Go, lang.Rust, lang.Java, lang.Kotlin,
		lang.Scala, lang.CSharp, lang.PHP, lang.Ruby, lang.Swift, lang.Dart, lang.Elixir, lang.C, lang.Cpp}
	for _, l := range langs {
		for _, s := range samples {
			for cut := 0; cut <= len(s); cut++ {
				for _, src := range []string{s[:cut], s[cut:], strings.Repeat(s[:cut], 2)} {
					for _, b := range Find(l, lang.Lex(l, src)) {
						if b.Start < 1 || b.End < b.Start || b.Header < b.Start || b.Header > b.End {
							t.Fatalf("%s %q: bad range %+v", l, src, b)
						}
					}
				}
			}
		}
	}
	if Find(lang.Markdown, lang.Lex(lang.Markdown, "it('x', () => {})")) != nil {
		t.Error("unsupported language should yield no blocks")
	}
}

// TestPrefilterIsExact: the substring checks that skip assertion patterns
// must never change a count. Every line of the rule corpora is counted with
// and without them, in the language of its file.
func TestPrefilterIsExact(t *testing.T) {
	files, _ := filepath.Glob("../rules/*/testdata/*.case")
	if len(files) == 0 {
		t.Skip("no corpus found")
	}
	lines := 0
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		l := lang.Unknown
		var section []string
		check := func() {
			if l == lang.Unknown || len(section) == 0 {
				return
			}
			for _, ll := range lang.Lex(l, strings.Join(section, "\n")) {
				lines++
				for _, helpers := range []bool{true, false} {
					got, want := len(matchAssertions(l, ll.Code, helpers, true)), len(matchAssertions(l, ll.Code, helpers, false))
					if got != want {
						t.Errorf("%s (%s): %q: %d assertions with the prefilter, %d without", filepath.Base(p), l, ll.Code, got, want)
					}
				}
			}
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "-- ") && strings.HasSuffix(line, " --") {
				check()
				l, section = lang.Detect(strings.TrimSuffix(strings.TrimPrefix(line, "-- "), " --")), nil
				continue
			}
			section = append(section, line)
		}
		check()
	}
	if lines < 1000 {
		t.Fatalf("only %d corpus lines checked", lines)
	}
}

// TestDeadAndInertAssertions: assertions that can never run, and
// expectations without a matcher, are listed but not counted.
func TestDeadAndInertAssertions(t *testing.T) {
	cases := []struct {
		name               string
		l                  lang.Lang
		src                string
		asserts            int
		unreachable, inert int
		ret                int // Block.Return
	}{
		{"early return", lang.TypeScript, "it('x', () => {\n  return\n  expect(a).toBe(1)\n})\n", 0, 1, 0, 2},
		{"return after the checks", lang.TypeScript, "it('x', () => {\n  expect(a).toBe(1)\n  return\n})\n", 1, 0, 0, 3},
		{"nested return", lang.TypeScript, "it('x', () => {\n  if (!ok) {\n    return\n  }\n  expect(a).toBe(1)\n})\n", 1, 0, 0, 0},
		{"if (false)", lang.TypeScript, "it('x', () => {\n  if (false) {\n    expect(a).toBe(1)\n  }\n  expect(b).toBe(2)\n})\n", 1, 1, 0, 0},
		{"python early return", lang.Python, "def test_x():\n    return\n    assert a == 1\n", 0, 1, 0, 2},
		{"python if False", lang.Python, "def test_x():\n    if False:\n        assert a == 1\n    assert b == 2\n", 1, 1, 0, 0},
		{"#if 0", lang.Cpp, "TEST(A, B) {\n#if 0\n  EXPECT_EQ(a, 1);\n#endif\n  EXPECT_EQ(b, 2);\n}\n", 1, 1, 0, 0},
		{"bare expect", lang.JavaScript, "test('x', () => {\n  expect(total(cart))\n  expect(\n    cart.lines,\n  )\n})\n", 0, 0, 2, 0},
		{"matcher on the next line", lang.JavaScript, "test('x', () => {\n  expect(total(cart))\n    .toBe(10)\n})\n", 1, 0, 0, 0},
		{"assertThat without a check", lang.Java, "@Test\nvoid x() {\n  assertThat(total);\n  assertThat(cart).isEmpty();\n}\n", 1, 0, 1, 0},
		{"two-argument expect", lang.Dart, "test('x', () {\n  expect(total, equals(10));\n});\n", 1, 0, 0, 0},
		{"swift #expect", lang.Swift, "@Test func x() {\n  #expect(total == 10)\n}\n", 1, 0, 0, 0},
		{"supertest", lang.JavaScript, "it('x', async () => {\n  await request(app).get('/')\n    .expect(200)\n})\n", 1, 0, 0, 0},
	}
	for _, c := range cases {
		bs := Find(c.l, lang.Lex(c.l, c.src))
		if len(bs) != 1 {
			t.Errorf("%s: %d blocks, want 1", c.name, len(bs))
			continue
		}
		b := bs[0]
		if b.Assertions != c.asserts || len(b.Unreachable) != c.unreachable || len(b.Inert) != c.inert || b.Return != c.ret {
			t.Errorf("%s: asserts=%d unreachable=%v inert=%v return=%d, want %d/%d/%d/%d", c.name,
				b.Assertions, b.Unreachable, b.Inert, b.Return, c.asserts, c.unreachable, c.inert, c.ret)
		}
	}
}
