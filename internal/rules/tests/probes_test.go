package tests_test

import (
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/ruletest"
)

// Probes are small one-file changes that pin down behaviour too fine-grained
// for a corpus case: look-alikes that must stay clean, cancellation of moved
// or edited markers, and edge cases of individual detectors.

type probe struct {
	path, old, new string // old "-" means the file is new
	want           string // expected rule IDs, comma-separated; "" = clean
}

func (p probe) run() []core.Finding {
	c := &ruletest.Case{Name: "probe", Old: map[string][]byte{}, New: map[string][]byte{}}
	if p.old != "-" {
		c.Old[p.path] = []byte(p.old)
	}
	c.New[p.path] = []byte(p.new)
	return c.Run(nil).Active()
}

func runProbe(t *testing.T, ps []probe) {
	t.Helper()
	for i, p := range ps {
		c := &ruletest.Case{Name: "probe", Old: map[string][]byte{}, New: map[string][]byte{}}
		if p.old != "-" {
			c.Old[p.path] = []byte(p.old)
		}
		c.New[p.path] = []byte(p.new)
		res := c.Run(nil)
		got := strings.Join(ruletest.Got(res), ",")
		if got != p.want {
			var msgs []string
			for _, f := range res.Active() {
				msgs = append(msgs, f.RuleID+": "+f.Message)
			}
			t.Errorf("probe %d (%s): want [%s] got [%s]\n  %s\n  new: %q", i, p.path, p.want, got, strings.Join(msgs, "\n  "), p.new)
		}
	}
}

func TestPrecisionProbes(t *testing.T) {
	runProbe(t, []probe{
		{"a.test.ts", "x\n", "x\nconst skip = options.skip ?? 0;\n", ""},
		{"a.test.ts", "x\n", "x\ndescribe('only admins', () => {\n  it('sees only admin rows', () => { expect(rows).toHaveLength(2) })\n})\n", ""},
		{"tests/test_a.py", "x = 1\n", "x = 1\n\ndef test_skip_empty_rows():\n    assert parse(data, skip_empty=True) == []\n", ""},
		{"tests/test_a.py", "x = 1\n", "x = 1\n\n@pytest.mark.parametrize(\"skip\", [True, False])\ndef test_flags(skip):\n    assert run(skip=skip).ok\n", ""},
		{"a_test.go", "package a\n", "package a\n\nfunc TestA(t *testing.T) {\n\tif t.Skipped() {\n\t\treturn\n\t}\n}\n", ""},
		{"spec/a_spec.rb", "x\n", "x\nskip_before_action :authenticate\npending = create(:order, status: :pending)\nit \"is pending until paid\" do\n  expect(order).to be_pending\nend\nlet(:skip) { true }\n", ""},
		{"tests/ATests.cs", "x\n", "x\n[Fact(DisplayName = \"pays\", Timeout = 1000)]\n[Trait(\"Category\", \"Skip\")]\npublic void Pays() => Assert.Equal(1, Pay());\n", ""},
		{"Tests/ATests.swift", "x\n", "x\nlet view = Button(\"x\").disabled(true)\n", ""},
		{"test/a_test.dart", "x\n", "x\nfinal page = await repo.page(skip: offset, take: 10);\n", ""},
		{"a.test.ts", "x\n", "x\nexpect(typeof fn).toBe('function');\nexpect(Array.isArray([])).toBe(true);\nexpect(wrapper.exists()).toBe(true);\n", ""},
		{"tests/test_a.py", "x\n", "x\nassert response.json() == response.json()\nself.assertIsNotNone(self.client)\nassert 0 < len(items)\n", ""},
		{"src/lib.rs", "#[cfg(test)]\nmod t {}\n", "#[cfg(test)]\nmod t {\n    #[test]\n    fn f() { assert!(matches!(x, Some(_))); }\n}\n", ""},
		{"a.test.ts", "it('a', () => {\n  expect(a).toEqual(b);\n});\n", "it('a', () => {\n  expect(a).toStrictEqual(b);\n});\n", ""},
		{"a.test.ts", "it('a', () => {\n  expect(fn).toThrow('boom');\n});\n", "it('a', () => {\n  expect(fn).toThrow(TypeError);\n});\n", ""},
		{"tests/test_a.py", "def test_a():\n    assert result == expected\n", "def test_a():\n    assert result is not None\n    assert result.id == expected.id\n", ""},
		{"tests/test_a.py", "def test_a():\n    assert total == 107\n", "def test_a():\n    # assert total == 108 would be wrong: VAT is 7%\n    assert total == Decimal(\"107\")\n", ""},
		{"a.test.ts", "it('a', () => {\n  expect(x).toBe(1);\n});\n", "it('a', () => {\n  // expect(x).toBe(1) used to be here, see b.test.ts\n  expect(x).toBe(1);\n});\n", ""},
		{"src/http.ts", "x\n", "x\nit('not a test file', () => {});\nexport const it = { skip: true };\n", ""},
		{"test/a.spec.js", "x\n", "x\nconst runner = xit ? 1 : 2;\nmyLib.xit('a');\n", ""},
		{"spec/a_spec.rb", "x\n", "x\ndescribe Report do\n  it \"is generated\", :slow do\n  end\nend\n", ""},
		// tricky positives
		{"a.test.ts", "it('a', () => {\n  expect(total).toHaveLength(3);\n});\n", "it('a', () => {\n  expect(total.length).toBeGreaterThan(0);\n});\n", "assertion-weakened"},
		{"tests/test_a.py", "def test_a():\n    assert len(items) == 3\n", "def test_a():\n    assert items\n", "assertion-weakened"},
		{"a_test.go", "package a\n", "package a\n\nfunc TestA(t *testing.T) {\n\tassert.Equal(t, http.StatusOK, http.StatusOK)\n}\n", "tautology"},
		{"playwright.config.ts", "export default defineConfig({\n  retries: process.env.CI ? 2 : 0,\n});\n", "export default defineConfig({\n  retries: process.env.CI ? 3 : 0,\n});\n", "retry-added"},
		{"tests/test_a.py", "def test_a():\n    run()\n", "@retry(stop=stop_after_attempt(3))\ndef test_a():\n    run()\n", "retry-added"},
		{"a.test.ts", "describe('x', () => {\n  it('a', () => {});\n});\n", "describe('x', () => {\n  it.each([1, 2]).skip('a %i', () => {});\n  test.concurrent.skip('b', async () => {});\n});\n", "skip-added"},
	})
}

func TestCancellationProbes(t *testing.T) {
	runProbe(t, []probe{
		// un-skip one test, skip another in a different place: still new
		{"a.test.ts", "it.skip('a', () => {});\n\nconst x = 1;\nconst y = 2;\nconst z = 3;\nconst w = 4;\n\nit('b', () => {});\n",
			"it('a', () => {});\n\nconst x = 1;\nconst y = 2;\nconst z = 3;\nconst w = 4;\n\nit.skip('b', () => {});\n", "skip-added"},
		// rename a skipped test and skip a new one in the same block
		{"a.test.ts", "it.skip('a', () => {});\n", "it.skip('b', () => {});\nit.skip('a renamed', () => {});\n", "skip-added"},
		// make a skipped test async: not new
		{"a.test.ts", "it.skip('a', () => {\n  run();\n});\n", "it.skip('a', async () => {\n  await run();\n});\n", ""},
	})
}

func TestOptionEditProbes(t *testing.T) {
	runProbe(t, []probe{
		{"test/a_test.dart", "void main() {\n  test('a', () {\n    expect(1, 1);\n  }, skip: true);\n}\n", "void main() {\n  test('a', () {\n    expect(1, 1);\n  }, skip: 'flaky on CI, see #12');\n}\n", ""},
		{"test/a.test.mjs", "test('a', { skip: true }, () => {});\n", "test('a', { skip: 'needs a db' }, () => {});\n", ""},
	})
}

func TestTernaryProbes(t *testing.T) {
	runProbe(t, []probe{
		{"a.test.ts", "x\nit('a', () => {});\n", "x\nconst itIfDocker = hasDocker ? it : it.skip;\nitIfDocker('a', () => {});\n", "skip-added"},
		{"a.test.ts", "x\nit('a', () => {});\n", "x\nconst itUnlessWin = process.platform === 'win32' ? it.skip : it;\nitUnlessWin('a', () => {});\n", "skip-added"},
		// an alias used only by tests the change adds gates nothing that ran before
		{"a.test.ts", "x\nit('a', () => {});\n", "x\nconst itIfDocker = hasDocker ? it : it.skip;\nit('a', () => {});\nitIfDocker('b', () => {});\n", ""},
	})
}

func TestModuleProbes(t *testing.T) {
	runProbe(t, []probe{
		{"tests/test_m.py", "import pytest\n\ndef test_a():\n    assert f() == 1\n", "import pytest\n\npytestmark = pytest.mark.skip(reason=\"wip\")\n\ndef test_a():\n    assert f() == 1\n", "skip-added"},
		{"src/test/groovy/ASpec.groovy", "class ASpec extends Specification {\n  def \"adds\"() {\n    expect: 1 + 1 == 2\n  }\n}\n", "class ASpec extends Specification {\n  @IgnoreRest\n  def \"adds\"() {\n    expect: 1 + 1 == 2\n  }\n}\n", "focus-added"},
	})
}

func TestCommentedProbes(t *testing.T) {
	runProbe(t, []probe{
		// one of several identical assertions is commented out
		{"a.test.ts", "it('a', async () => {\n  expect(res.status).toBe(200);\n});\n\nit('b', async () => {\n  expect(other).toBe(1);\n  expect(res.status).toBe(200);\n});\n",
			"it('a', async () => {\n  expect(res.status).toBe(200);\n});\n\nit('b', async () => {\n  expect(other).toBe(1);\n  // expect(res.status).toBe(200);\n});\n", "assertion-commented"},
		// the assertion moved and a copy stays as a comment: still live
		{"a.test.ts", "it('a', () => {\n  expect(x).toBe(1);\n  run();\n});\n", "it('a', () => {\n  run();\n  // expect(x).toBe(1);\n  expect(x).toBe(1);\n});\n", ""},
	})
}

func TestRetryProbes(t *testing.T) {
	runProbe(t, []probe{
		{"playwright.config.ts", "export default defineConfig({ retries: 0, workers: 4 });\n", "export default defineConfig({ retries: 0, workers: 8 });\n", ""},
		{"e2e/a.spec.ts", "test.describe('a', () => {\n  test('x', async () => {});\n});\n", "test.describe('a', () => {\n  test.describe.configure({ mode: 'serial', retries: 2 });\n  test('x', async () => {});\n});\n", "retry-added"},
		{"cypress/e2e/a.cy.ts", "it('x', () => {});\n", "it('x', { retries: { runMode: 2, openMode: 0 } }, () => {});\n", "retry-added"},
		{"Makefile", "test:\n\tpytest -q\n", "test:\n\tpytest -q --reruns 3\n", "retry-added"},
		{"scripts/ci.sh", "set -e\nnpm test\n", "set -e\ncurl --retry 5 -fsSL https://example.com/x | sh\nnpm test\n", ""},
	})
}

// TestHardeningProbes pins fixes found by adversarial review: look-alikes
// that used to fire, and spellings that used to slip through.
func TestHardeningProbes(t *testing.T) {
	runProbe(t, []probe{
		// literal spellings that differ at runtime are not "equal literals"
		{"tests/test_a.py", "x = 1\n", "x = 1\n\ndef test_a():\n    assert b\"abc\" == \"abc\"\n    assert r\"\\d\" == \"\\d\"\n", ""},
		{"spec/a_spec.rb", "x\n", "x\nit 'a' do\n  expect('a\\n').to eq(\"a\\n\")\nend\n", ""},
		{"a.test.ts", "x\n", "x\nit('a', () => {\n  expect('\\x41').not.toBe('A');\n});\n", ""},
		// a skip option nested in a call inside a one-line test body
		{"a.test.ts", "x\n", "x\ntest('x', () => run({ skip: true, only: true, retry: 3 }));\n", ""},
		// an options object inside the test body is not the test's options
		{"a.test.ts", "x\n", "x\nit('a', async () => {\n  const opts = {\n    retries: 3,\n    skip: true,\n  };\n  await run(opts);\n});\n", ""},
		// `}, { ... })` closing some other call is not a test option
		{"a.test.ts", "it('a', async () => {\n  await x();\n});\n", "it('a', async () => {\n  await pRetry(async () => {\n    await x();\n  }, { retries: 3 });\n});\n", ""},
		// Cypress multi-line retries: a raised runMode is still a raise
		{"cypress.config.ts", "export default defineConfig({\n  retries: {\n    runMode: 1,\n    openMode: 0,\n  },\n});\n",
			"export default defineConfig({\n  retries: {\n    runMode: 4,\n    openMode: 0,\n  },\n});\n", "retry-added"},
		// Go: t.Skip → t.Skipf in the same test is an edit, not a new skip
		{"a_test.go", "package a\n\nfunc TestA(t *testing.T) {\n\tt.Skip(\"flaky\")\n}\n", "package a\n\nfunc TestA(t *testing.T) {\n\tt.Skipf(\"flaky: %s\", reason)\n}\n", ""},
		// bracket access and continuation lines for focus and skip
		{"a.test.ts", "x\n", "x\ndescribe['only']('a', () => {});\n", "focus-added"},
		{"a.test.ts", "x\n", "x\ntest\n  .skip('a', () => {});\n", "skip-added"},
	})
}

// TestOneFindingPerMarker checks that a marker matched by two detectors is
// reported once.
func TestOneFindingPerMarker(t *testing.T) {
	ps := []probe{
		{"test/a.spec.js", "x\n", "x\ncontext.skip('when offline', () => {});\n", "skip-added"},
		{"test/a.spec.js", "x\n", "x\nit('a', function () {\n  context.skip();\n});\n", "skip-added"},
		// a formatter-split assertion is read once, whichever line holds the matcher
		{"test/a.spec.js", "it('a', () => {\n  expect(\n    total(cart),\n  ).toBe(\n    42,\n  );\n});\n", "it('a', () => {\n  expect(\n    total(cart),\n  ).toBeDefined();\n});\n", "assertion-weakened"},
		{"test/a.spec.js", "it('a', () => {\n  expect(total(cart)).toBe(\n    42,\n  );\n});\n", "it('a', () => {\n  expect(total(cart)).toBeDefined();\n});\n", "assertion-weakened"},
	}
	for i, p := range ps {
		fs := p.run()
		if len(fs) != 1 || fs[0].RuleID != p.want {
			t.Errorf("probe %d: want one %s finding, got %v", i, p.want, fs)
		}
	}
}

// TestProbeSeverity checks severities and messages of small probes.
func TestProbeSeverity(t *testing.T) {
	cases := []struct {
		p    probe
		sev  core.Severity
		want string
	}{
		// the reason is a string, not a condition, even before `do`
		{probe{"spec/a_spec.rb", "x\nit \"pages\" do\n  run\nend\n", "x\nit \"pages\", skip: \"flaky\" do\n  run\nend\n", "skip-added"}, core.Error, `(reason: "flaky")`},
		// a new test added already skipped: a warning, since nothing that ran before stops running
		{probe{"spec/a_spec.rb", "x\n", "x\nit \"pages\", skip: \"flaky\" do\n  run\nend\n", "skip-added"}, core.Warning, `new test "pages" is added already skipped with skip: (reason: "flaky")`},
		{probe{"src/test/java/ATest.java", "class ATest {\n  @Test\n  void a() {}\n}\n", "class ATest {\n  @org.junit.Ignore\n  @Test\n  void a() {}\n}\n", "skip-added"}, core.Error, "test a is now skipped with @Ignore"},
	}
	for i, c := range cases {
		fs := c.p.run()
		if len(fs) != 1 || fs[0].RuleID != c.p.want || fs[0].Severity != c.sev || !strings.Contains(fs[0].Message, c.want) {
			t.Errorf("case %d: want one %s %s finding containing %q, got %+v", i, c.sev, c.p.want, c.want, fs)
		}
	}
}

// TestEvasionProbes pins spellings of the markers that a plain search would
// miss: aliases, other handle names, spaced dots, other frameworks,
// formatter-split assertions and block comments around unchanged lines.
func TestEvasionProbes(t *testing.T) {
	runProbe(t, []probe{
		{"tests/test_a.py", "import pytest as pt\n\ndef test_a():\n    assert f() == 1\n", "import pytest as pt\n\n@pt.mark.skip(reason=\"later\")\ndef test_a():\n    assert f() == 1\n", "skip-added"},
		{"a_test.go", "package a\n\nfunc TestA(test *testing.T) {\n\trun()\n}\n", "package a\n\nfunc TestA(test *testing.T) {\n\ttest.Skip(\"later\")\n\trun()\n}\n", "skip-added"},
		{"a.test.ts", "x\n", "x\nit .skip('a', () => {});\n", "skip-added"},
		{"test/a.test.js", "x\n", "x\nQUnit.only('a', (assert) => { assert.ok(f()); });\n", "focus-added"},
		{"spec/a_spec.rb", "describe A do\n  it \"adds\" do\n    expect(add(1, 1)).to eq(2)\n  end\nend\n", "describe A do\n  focus \"adds\" do\n    expect(add(1, 1)).to eq(2)\n  end\nend\n", "focus-added"},
		{"a.test.ts", "it('a', () => {\n  expect(res.body).toEqual({ id: 1, total: 42 });\n});\n", "it('a', () => {\n  expect(res.body).toEqual(expect.objectContaining({}));\n});\n", "assertion-weakened"},
		{"a.test.ts", "it('a', () => {\n  expect(result)\n    .toEqual({ id: 1 });\n});\n", "it('a', () => {\n  expect(result)\n    .toBeDefined();\n});\n", "assertion-weakened"},
		{"a.test.ts", "it('a', () => {\n  run();\n});\n", "it('a', () => {\n  run();\n  expect(\n    true\n  ).toBe(true);\n});\n", "tautology"},
		{"a.test.ts", "it('a', () => {\n  run();\n});\n", "it('a', () => {\n  const x = run();\n  expect(x === x).toBe(true);\n});\n", "tautology"},
		{"a.test.ts", "it('a', () => {\n  expect(total)\n    .toBe(107);\n});\n", "it('a', () => {\n  // expect(total)\n  //   .toBe(107);\n});\n", "assertion-commented"},
		{"tests/test_a.py", "def test_a():\n    total = f()\n    assert total == 107\n", "def test_a():\n    total = f()\n    \"\"\"\n    assert total == 107\n    \"\"\"\n", "assertion-commented"},
		{"src/test/java/a/ATest.java", "class ATest {\n  @Test\n  void totals() {\n    assertEquals(107, total());\n  }\n}\n", "class ATest {\n  @Test\n  void totals() {\n    /* disabled for now\n    assertEquals(107, total());\n    */\n  }\n}\n", "assertion-commented"},
		{"BUILD.bazel", "go_test(\n    name = \"a_test\",\n    srcs = [\"a_test.go\"],\n)\n", "go_test(\n    name = \"a_test\",\n    srcs = [\"a_test.go\"],\n    flaky = True,\n)\n", "retry-added"},
		{"package.json", "{\n  \"scripts\": {\n    \"test:bdd\": \"cucumber-js\"\n  }\n}\n", "{\n  \"scripts\": {\n    \"test:bdd\": \"cucumber-js --retry 2\"\n  }\n}\n", "retry-added"},
		{"a.test.ts", "it('x', () => {\r\n  expect(f()).toBe(1);\r\n});\r\n", "it('x', () => {\r\n  // expect(f()).toBe(1);\r\n});\r\n", "assertion-commented"},
		// a zero lower bound only checks that something is there
		{"tests/test_a.py", "class T(unittest.TestCase):\n    def test_a(self):\n        self.assertEqual(len(items), 3)\n", "class T(unittest.TestCase):\n    def test_a(self):\n        self.assertGreater(len(items), 0)\n", "assertion-weakened"},
		{"a_test.go", "package a\n\nfunc TestA(t *testing.T) {\n\tassert.Len(t, items, 3)\n}\n", "package a\n\nfunc TestA(t *testing.T) {\n\tassert.Greater(t, len(items), 0)\n}\n", "assertion-weakened"},
		// t.pass() directly in the test body verifies nothing
		{"test/a.test.js", "x\n", "x\ntest('runs', (t) => {\n  run();\n  t.pass();\n  t.end();\n});\n", "tautology"},
	})
}

// TestLookalikeProbes pins changes that resemble the evasions above but are
// legitimate.
func TestLookalikeProbes(t *testing.T) {
	runProbe(t, []probe{
		// JSX text that quotes markers
		{"src/App.test.tsx", "it('renders', () => {\n  render(<App />);\n});\n", "it('renders', () => {\n  render(<p>Don't skip this step. it.skip('x') and it.only('y')</p>);\n  expect(screen.getByText(/skip/)).toBeInTheDocument();\n});\n", ""},
		// a skip moved from the decorator into the body of the same test
		{"tests/test_a.py", "import pytest\n\n@pytest.mark.skip(reason=\"slow\")\ndef test_a():\n    x = 1\n    y = 2\n    z = 3\n    w = 4\n    assert f() == 1\n", "import pytest\n\ndef test_a():\n    x = 1\n    y = 2\n    z = 3\n    w = 4\n    pytest.skip(\"slow\")\n    assert f() == 1\n", ""},
		// pytestmark narrowed to one test
		{"tests/test_m.py", "import pytest\n\npytestmark = pytest.mark.skip(reason=\"wip\")\n\ndef test_a():\n    assert f() == 1\n\ndef test_b():\n    assert g() == 2\n", "import pytest\n\n\ndef test_a():\n    assert f() == 1\n\n@pytest.mark.skip(reason=\"wip\")\ndef test_b():\n    assert g() == 2\n", ""},
		// focus narrowed from the suite to one test in it
		{"a.test.ts", "describe.only('cart', () => {\n  it('adds', () => {});\n  it('removes', () => {});\n});\n", "describe('cart', () => {\n  it.only('adds', () => {});\n  it('removes', () => {});\n});\n", ""},
		// lexer and decoder Skip methods
		{"internal/lex/lex_test.go", "package lex\n\nfunc TestNext(t *testing.T) {\n\tl := New(\"ab\")\n\t_ = l\n}\n", "package lex\n\nfunc TestNext(t *testing.T) {\n\tl := New(\"ab\")\n\tl.Skip()\n\tdec.Skip(2)\n\t_ = l\n}\n", ""},
		// Dart: skip: of a call inside a one-line test body
		{"test/repo_test.dart", "void main() {\n  test('lists', () async => expect(await repo.list(), hasLength(3)));\n}\n", "void main() {\n  test('lists', () async => expect(await repo.list(skip: 2), hasLength(1)));\n}\n", ""},
		// Capybara focus helper inside an example
		{"spec/system/a_spec.rb", "RSpec.describe \"Signup\" do\n  it \"validates\" do\n    visit root_path\n  end\nend\n", "RSpec.describe \"Signup\" do\n  it \"validates\" do\n    visit root_path\n    focus(\"#email\")\n  end\nend\n", ""},
		// JSDoc and docstrings opened and closed on added lines
		{"a.test.ts", "function check(inv) {\n  expect(inv.total).toBe(107);\n}\n", "/**\n * Shared check.\n */\nfunction check(inv) {\n  expect(inv.total).toBe(107);\n}\n", ""},
		{"tests/test_a.py", "def test_a():\n    assert f() == 1\n", "def test_a():\n    \"\"\"\n    f returns one.\n    \"\"\"\n    assert f() == 1\n", ""},
		// a conjunction that still compares a value
		{"tests/test_a.py", "def test_a():\n    assert user == expected\n", "def test_a():\n    assert user is not None and user.id == expected.id\n", ""},
		// a commented flaky flag next to flaky = False
		{"BUILD.bazel", "go_test(\n    name = \"a_test\",\n    flaky = True,\n)\n", "go_test(\n    name = \"a_test\",\n    # flaky = True,\n    flaky = False,\n)\n", ""},
		// retries moved from the config to a script with the same count
		{"playwright.config.ts", "export default defineConfig({\n  retries: 2,\n  workers: 4,\n});\n", "export default defineConfig({\n  workers: 4,\n});\n", ""},
		// a tokenizer that happens to be called t
		{"test/lexer.test.js", "x\n", "x\nit('skips whitespace', () => {\n  const t = tokenize('  a');\n  t.skip(2);\n  expect(t.next()).toBe('a');\n});\n", ""},
		// CRLF → LF conversion of a file with existing markers
		{"a.test.ts", "describe('a', () => {\r\n  it.skip('x', () => {\r\n    expect(true).toBe(true);\r\n  });\r\n  it.only('y', () => {});\r\n});\r\n", "describe('a', () => {\n  it.skip('x', () => {\n    expect(true).toBe(true);\n  });\n  it.only('y', () => {});\n});\n", ""},
	})
}

// TestHardeningSeverity checks severities and messages of the hardening
// fixes: constant conditions, lost conditions, and reasons that only look
// like issue references.
func TestHardeningSeverity(t *testing.T) {
	cases := []struct {
		p    probe
		sev  core.Severity
		want string
	}{
		{probe{"tests/a.rs", "#[test]\nfn a() {\n    assert_eq!(f(), 1);\n}\n", "#[test]\n#[cfg_attr(all(), ignore)]\nfn a() {\n    assert_eq!(f(), 1);\n}\n", "skip-added"}, core.Error, "the condition all() always skips"},
		{probe{"a_test.go", "package a\n\nfunc TestA(t *testing.T) {\n\tif testing.Short() {\n\t\tt.Skip(\"slow\")\n\t}\n\trun()\n}\n", "package a\n\nfunc TestA(t *testing.T) {\n\tt.Skip(\"slow\")\n\trun()\n}\n", "skip-added"}, core.Error, "it used to skip only when testing.Short(), now it always does"},
		{probe{"a_test.go", "package a\n\nfunc TestA(t *testing.T) {\n\trun()\n}\n", "package a\n\nfunc TestA(t *testing.T) {\n\tt.Skip(\"timing issue on slow machines\")\n\trun()\n}\n", "skip-added"}, core.Error, `(reason: "timing issue on slow machines")`},
		{probe{"tests/test_a.py", "import pytest\n\ndef test_a():\n    assert f() == 1\n", "import pytest\n\n@pytest.mark.skip(reason=\"breaks with UTF-8 input\")\ndef test_a():\n    assert f() == 1\n", "skip-added"}, core.Error, "UTF-8"},
		{probe{"tests/test_a.py", "import pytest\n\ndef test_a():\n    assert f() == 1\n", "import pytest\n\n@pytest.mark.skip(reason=\"flaky, see issue 4521\")\ndef test_a():\n    assert f() == 1\n", "skip-added"}, core.Warning, "references an issue"},
		{probe{"a.test.ts", "it('a', () => {\n  expect(total).toBe(107);\n});\n", "it('a', () => {\n  expect(total).toBe(108); // expect(total).toBe(107);\n});\n", "assertion-commented"}, core.Warning, "(replaced by expect(total).toBe(108))"},
		{probe{"BUILD.bazel", "go_test(\n    name = \"a_test\",\n)\n", "go_test(\n    name = \"a_test\",\n    flaky = True,\n)\n", "retry-added"}, core.Warning, `test target "a_test" in BUILD.bazel is now marked flaky = True`},
	}
	for i, c := range cases {
		fs := c.p.run()
		if len(fs) != 1 || fs[0].RuleID != c.p.want || fs[0].Severity != c.sev || !strings.Contains(fs[0].Message, c.want) {
			t.Errorf("case %d: want one %s %s finding containing %q, got %+v", i, c.sev, c.p.want, c.want, fs)
		}
	}
}
