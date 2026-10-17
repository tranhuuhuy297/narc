package tests

import (
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

func lexed(lg lang.Lang, src string) core.Line {
	return core.Line{LexedLine: lang.LexLine(lg, src), No: 1}
}

func TestValueText(t *testing.T) {
	cases := []struct {
		lg       lang.Lang
		src, out string
		ok       bool
	}{
		{lang.Python, `assertEqual("a, b", x)`, `assertEqual("a%2C%20b", x)`, true},
		{lang.JavaScript, `expect('x').toBe("")`, `expect('x').toBe("")`, true},
		{lang.Python, `x = """doc"""`, `x = """doc"""`, true},
		{lang.TypeScript, "it(`a ${b}`, f)", "it(`${b}`, f)", false},
		{lang.Rust, `fn f<'a>(s: &'a str) { assert_eq!(s, "x") }`, `fn f<'a>(s: &'a str) { assert_eq!(s, "x") }`, true},
	}
	for _, c := range cases {
		got, ok := valueText(lexed(c.lg, c.src), c.lg)
		if got != c.out || ok != c.ok {
			t.Errorf("valueText(%q) = %q, %v; want %q, %v", c.src, got, ok, c.out, c.ok)
		}
		if c.ok && display(got) != c.src {
			t.Errorf("display(%q) = %q; want %q", got, display(got), c.src)
		}
	}
}

func TestParseAssertions(t *testing.T) {
	cases := []struct {
		lg   lang.Lang
		src  string
		fn   string
		pass passKind
		str  strength
	}{
		{lang.TypeScript, `expect(total).toEqual(42);`, "toEqual", passEqual, checksValue},
		{lang.TypeScript, `expect(total).toBeDefined();`, "toBeDefined", passDefined, checksExist},
		{lang.TypeScript, `expect(user).not.toBeNull();`, "toBeNull", passNotNull, checksExist},
		{lang.TypeScript, `expect(spy).toHaveBeenCalledWith(1, 2);`, "toHaveBeenCalledWith", passUnknown, checksValue},
		{lang.TypeScript, `expect(spy).toHaveBeenCalled();`, "toHaveBeenCalled", passUnknown, checksExist},
		{lang.TypeScript, `await expect(p).rejects.toThrow(TypeError);`, "toThrow", passUnknown, checksThrow},
		{lang.JavaScript, `expect(x).to.be.true;`, "true", passTruthy, checksExist},
		{lang.JavaScript, `expect(res.body).to.deep.equal({ id: 1 });`, "equal", passEqual, checksValue},
		{lang.JavaScript, `assert.strictEqual(a, b);`, "strictEqual", passEqual, checksValue},
		{lang.JavaScript, `assert.ok(true);`, "ok", passTruthy, checksExist},
		{lang.Python, `assert result == 5`, "assert", passTruthy, checksValue},
		{lang.Python, `assert result is not None`, "assert", passTruthy, checksExist},
		{lang.Python, `self.assertEqual(resp.status_code, 200)`, "assertEqual", passEqual, checksValue},
		{lang.Python, `with pytest.raises(ValueError, match="bad"):`, "raises", passUnknown, checksThrow},
		{lang.Go, `assert.Equal(t, want, got)`, "Equal", passEqual, checksValue},
		{lang.Go, `require.NotNil(t, got)`, "NotNil", passNotNull, checksExist},
		{lang.Go, `Expect(err).NotTo(HaveOccurred())`, "HaveOccurred", passUnknown, checksOther},
		{lang.Rust, `assert_eq!(parse("1"), Ok(1));`, "assert_eq!", passEqual, checksValue},
		{lang.Rust, `assert!(parse("1").is_ok());`, "assert!", passTruthy, checksExist},
		{lang.Java, `assertEquals("total", 42, invoice.total());`, "assertEquals", passEqual, checksValue},
		{lang.Java, `assertThat(list).hasSize(3);`, "hasSize", passUnknown, checksValue},
		{lang.Java, `assertThat(list, is(empty()));`, "empty", passUnknown, checksOther},
		{lang.Java, `assertThrows(IllegalArgumentException.class, () -> parse(""));`, "assertThrows", passUnknown, checksThrow},
		{lang.CSharp, `Assert.Equal(42, total);`, "Equal", passEqual, checksValue},
		{lang.CSharp, `Assert.That(total, Is.EqualTo(42));`, "EqualTo", passEqual, checksValue},
		{lang.CSharp, `Assert.That(user, Is.Not.Null);`, "Null", passNotNull, checksExist},
		{lang.CSharp, `result.Should().Be(42);`, "Be", passEqual, checksValue},
		{lang.CSharp, `Assert.Pass();`, "Pass", passAlways, checksOther},
		{lang.Ruby, `expect(user.name).to eq("Ann")`, "eq", passEqual, checksValue},
		{lang.Ruby, `expect(user).to be_truthy`, "be_truthy", passTruthy, checksExist},
		{lang.Ruby, `expect(user).not_to be_nil`, "be_nil", passNotNull, checksExist},
		{lang.Ruby, `assert_equal 5, total`, "assert_equal", passEqual, checksValue},
		{lang.Ruby, `expect { parse("") }.to raise_error(ArgumentError)`, "raise_error", passUnknown, checksThrow},
		{lang.PHP, `$this->assertSame(42, $total);`, "assertSame", passEqual, checksValue},
		{lang.PHP, `expect($total)->toBe(42);`, "toBe", passEqual, checksValue},
		{lang.Swift, `XCTAssertEqual(total, 42)`, "XCTAssertEqual", passEqual, checksValue},
		{lang.Swift, `#expect(total == 42)`, "#expect", passTruthy, checksValue},
		{lang.Kotlin, `total shouldBe 42`, "shouldBe", passEqual, checksValue},
		{lang.Dart, `expect(total, equals(42));`, "equals", passEqual, checksValue},
		{lang.Dart, `expect(total, isNotNull);`, "isNotNull", passNotNull, checksExist},
		{lang.Elixir, `assert total == 42`, "assert", passTruthy, checksValue},
		{lang.Cpp, `EXPECT_EQ(total, 42);`, "EXPECT_EQ", passEqual, checksValue},
		{lang.JavaScript, `assert.throws(() => parse(""), SyntaxError);`, "throws", passUnknown, checksThrow},
		{lang.Python, `with pytest.raises(expected_exception=KeyError):`, "raises", passUnknown, checksThrow},
		// a prefix or suffix check compares part of a value; one nested in a callback does not
		{lang.Python, `assert message.startswith("Unknown Sim table")`, "assert", passTruthy, checksValue},
		{lang.Go, `assert.True(t, strings.HasPrefix(url, base))`, "True", passTruthy, checksValue},
		{lang.TypeScript, "expect(lines.some((l) => l.startsWith(`oss/${file}`))).toBe(true);", "toBe", passEqual, checksOther},
		// a predicate call checks what the predicate checks; membership and
		// emptiness are specific, type and presence checks are not
		{lang.TypeScript, "expect(isNativelyEvaluable(String(code).slice(1))).toBe(true);", "toBe", passEqual, checksOther},
		{lang.Python, `assert _is_preflight_command(commands[1])`, "assert", passTruthy, checksOther},
		{lang.Java, `assertTrue(rig.runs().isEmpty());`, "assertTrue", passTruthy, checksValue},
		{lang.Java, `assertFalse(cart.items().isEmpty());`, "assertFalse", passFalsy, checksExist},
		{lang.TypeScript, `expect(ids.includes(3)).toBe(true);`, "toBe", passEqual, checksValue},
		{lang.Python, `assert isinstance(mcp_config, dict)`, "assert", passTruthy, checksExist},
		{lang.Rust, `assert!(result.is_err());`, "assert!", passTruthy, checksExist},
		{lang.TypeScript, `expect(Boolean(total)).toBe(true);`, "toBe", passEqual, checksExist},
		{lang.Python, `assert message.startswith("")`, "assert", passTruthy, checksExist},
	}
	for _, c := range cases {
		as := parseAssertions(c.lg, lexed(c.lg, c.src))
		if len(as) != 1 {
			t.Errorf("%s %q: got %d assertions %+v", c.lg, c.src, len(as), as)
			continue
		}
		a := as[0]
		if a.fn != c.fn || a.pass != c.pass || a.str != c.str {
			t.Errorf("%s %q: got fn=%s pass=%d str=%d; want fn=%s pass=%d str=%d", c.lg, c.src, a.fn, a.pass, a.str, c.fn, c.pass, c.str)
		}
	}
}

func TestThrowSpecs(t *testing.T) {
	cases := []struct {
		lg    lang.Lang
		src   string
		exc   string
		match bool
	}{
		{lang.JavaScript, `assert.throws(() => parse(""), SyntaxError);`, "SyntaxError", false},
		{lang.JavaScript, `assert.throws(() => parse(""), /unexpected/);`, "", true},
		{lang.TypeScript, `expect(() => parse("")).toThrow("unexpected end");`, "", true},
		{lang.Python, `with pytest.raises(ValueError, match="port"):`, "ValueError", true},
		{lang.Python, `with pytest.raises(expected_exception=KeyError):`, "KeyError", false},
		{lang.Java, `assertThrows(IllegalStateException.class, () -> svc.run());`, "IllegalStateException", false},
		{lang.Java, `assertThatThrownBy(() -> svc.run()).isInstanceOf(IOException.class).hasMessage("disk full");`, "IOException", true},
		{lang.CSharp, `Assert.Throws<ArgumentNullException>(() => new Svc(null));`, "ArgumentNullException", false},
		{lang.CSharp, `Assert.That(() => svc.Run(), Throws.TypeOf<TimeoutException>());`, "TimeoutException", false},
		{lang.PHP, `$this->expectException(\InvalidArgumentException::class);`, "InvalidArgumentException", false},
		{lang.Ruby, `expect { parse("") }.to raise_error(ArgumentError, /empty/)`, "ArgumentError", true},
		{lang.Kotlin, `assertFailsWith<IllegalArgumentException> { parse("") }`, "IllegalArgumentException", false},
		{lang.Dart, `expect(() => parse(""), throwsA(isA<FormatException>()));`, "FormatException", false},
		{lang.Rust, `#[should_panic(expected = "empty")]`, "panic", true},
	}
	for _, c := range cases {
		as := parseAssertions(c.lg, lexed(c.lg, c.src))
		if len(as) != 1 || as[0].str != checksThrow || as[0].exc != c.exc || as[0].match != c.match {
			t.Errorf("%s %q: got %+v; want exc=%q match=%v", c.lg, c.src, as, c.exc, c.match)
		}
	}
}

func TestNotAssertions(t *testing.T) {
	for _, c := range []struct {
		lg  lang.Lang
		src string
	}{
		{lang.Rust, `let v = parse(x).expect("valid input");`},
		{lang.Go, `if got.Equal(want) {`},
		{lang.TypeScript, `const ok = equal(a, b);`},
		{lang.CSharp, `var same = a.Equals(b);`},
		{lang.Python, `# assert x == 1`},
		{lang.JavaScript, `log("expect(true).toBe(true)")`},
	} {
		if as := parseAssertions(c.lg, lexed(c.lg, c.src)); len(as) != 0 {
			t.Errorf("%s %q: got %+v, want no assertions", c.lg, c.src, as)
		}
	}
}

func TestCannotFail(t *testing.T) {
	cases := []struct {
		lg     lang.Lang
		src    string
		always bool
	}{
		{lang.TypeScript, `expect(true).toBe(true);`, true},
		{lang.TypeScript, `expect(total).toBe(total);`, true},
		{lang.TypeScript, `expect(hash(a)).toBe(hash(a));`, false},
		{lang.TypeScript, `expect("a").toBe("b");`, false},
		{lang.TypeScript, `expect("ok").toBeDefined();`, true},
		{lang.TypeScript, `expect(1).toBeTruthy();`, true},
		{lang.TypeScript, `expect(0).toBeTruthy();`, false},
		{lang.TypeScript, `expect(result || true).toBeTruthy();`, true},
		{lang.TypeScript, `expect(result).toBeTruthy();`, false},
		{lang.TypeScript, `expect(1).not.toBe(2);`, true},
		{lang.JavaScript, `assert.ok(true);`, true},
		{lang.JavaScript, `assert.equal(a, a);`, true},
		{lang.JavaScript, `expect(true).to.be.true;`, true},
		{lang.Python, `assert True`, true},
		{lang.Python, `assert 1`, true},
		{lang.Python, `assert "x"`, true},
		{lang.Python, `assert x == x`, true},
		{lang.Python, `assert (result == 5, "wrong total")`, true},
		{lang.Python, `assert result == 5, "wrong total"`, false},
		{lang.Python, `assert []`, false},
		{lang.Python, `self.assertTrue(True)`, true},
		{lang.Python, `self.assertEqual(1, 1)`, true},
		{lang.Python, `self.assertEqual(resp.status, 200)`, false},
		{lang.Rust, `assert!(true);`, true},
		{lang.Rust, `assert_eq!(1, 1);`, true},
		{lang.Rust, `assert_eq!(x, x);`, true},
		{lang.Rust, `assert_eq!(x, y);`, false},
		{lang.Java, `assertTrue(true);`, true},
		{lang.Java, `assertEquals(1, 1);`, true},
		{lang.Java, `assertEquals("msg", 1, 1);`, true},
		{lang.Java, `assertEquals("msg", expected, actual);`, false},
		{lang.Java, `assertNotNull(new Invoice());`, true},
		{lang.CSharp, `Assert.True(true);`, true},
		{lang.CSharp, `Assert.Pass();`, true},
		{lang.CSharp, `Assert.Equal(expected, actual);`, false},
		{lang.Go, `assert.True(t, true)`, true},
		{lang.Go, `assert.Equal(t, x, x)`, true},
		{lang.Go, `assert.Equal(t, want, got)`, false},
		{lang.PHP, `$this->assertTrue(true);`, true},
		{lang.PHP, `$this->assertSame($total, $total);`, true},
		{lang.Swift, `XCTAssertTrue(true)`, true},
		{lang.Swift, `#expect(true)`, true},
		{lang.Ruby, `expect(true).to eq(true)`, true},
		{lang.Ruby, `expect(true).to be true`, true},
		{lang.Ruby, `assert true`, true},
		{lang.Ruby, `assert_equal 1, 1`, true},
		{lang.Ruby, `assert user.valid?`, false},
		{lang.Elixir, `assert 1 == 1`, true},
		{lang.Dart, `expect(true, isTrue);`, true},
		{lang.Dart, `expect(total, anything);`, true},
		{lang.Cpp, `EXPECT_TRUE(true);`, true},
		{lang.Kotlin, `true shouldBe true`, true},
		{lang.TypeScript, `expect(null).toBeNull();`, true},
		{lang.TypeScript, `expect(null).toBeUndefined();`, false},
		{lang.TypeScript, `expect(undefined).toBeUndefined();`, true},
		{lang.TypeScript, `expect(null).toBeDefined();`, true},
		{lang.Python, `self.assertIsNone(None)`, true},
		{lang.Go, `assert.NotNil(t, "ok")`, true},
		{lang.Swift, `XCTAssertNotNil(nil)`, false},
	}
	for _, c := range cases {
		l := lexed(c.lg, c.src)
		_, ok := valueText(l, c.lg)
		as := parseAssertions(c.lg, l)
		why := ""
		for _, a := range as {
			if w := cannotFail(a, c.lg, ok); w != "" {
				why = w
			}
		}
		if (why != "") != c.always {
			t.Errorf("%s %q: cannotFail = %q (parsed %+v); want always=%v", c.lg, c.src, why, as, c.always)
		}
	}
}

func TestWords(t *testing.T) {
	got := words("isCI && !hasDocker || RUN_SLOW_TESTS || HTTPServer")
	want := []string{"is", "ci", "has", "docker", "run", "slow", "tests", "http", "server"}
	if len(got) != len(want) {
		t.Fatalf("words = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("words = %q, want %q", got, want)
		}
	}
}
