package hook

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/huyth45/narc/internal/core"
)

func TestCoachWording(t *testing.T) {
	t.Parallel()
	found := []core.Finding{
		{RuleID: "skip-added", Path: "src/auth.test.ts", Line: 12, Message: `it.skip added to "rejects expired tokens"`,
			Hint: "Fix the code so the test passes instead of skipping it."},
		{RuleID: "skip-added", Path: "src/auth.test.ts", Line: 30, Message: `it.skip added to "refreshes tokens"`,
			Hint: "Fix the code so the test passes instead of skipping it."},
	}
	want := "narc blocked this edit: it weakens verification instead of fixing the code.\n" +
		"\n" +
		"✗ skip-added src/auth.test.ts:12 — it.skip added to \"rejects expired tokens\"\n" +
		"  → Fix the code so the test passes instead of skipping it.\n" +
		"✗ skip-added src/auth.test.ts:30 — it.skip added to \"refreshes tokens\"\n" +
		"\n" +
		"Fix the underlying problem so the original checks pass. Do not skip, delete or weaken tests, assertions, type checks, coverage or CI to get a passing run.\n" +
		"If the user explicitly asked for this change, add an inline waiver with a reason on the line above src/auth.test.ts:12, e.g. // narc:allow skip-added reason=\"<why>\". Do not add a waiver the user did not ask for."
	if got := coach(editCoach, found, false); got != want {
		t.Errorf("edit coach:\n%s\n--- want ---\n%s", got, want)
	}
	stop := coach(stopCoach, found[:1], true)
	wantContains(t, "stop coach", stop,
		"narc: do not stop yet. Your changes in this session weaken verification instead of fixing the code. You tried to stop again without resolving any of these.",
		"then finish. Anything left unresolved is reported to the user.")
	wantContains(t, "post coach", coach(postCoach, found[:1], false), "narc: the edit you just made", "Undo or rework it")
}

func TestCoachIsBounded(t *testing.T) {
	t.Parallel()
	var found []core.Finding
	for i := 0; i < 30; i++ {
		found = append(found, core.Finding{
			RuleID: fmt.Sprintf("rule-%d", i%4), Path: "src/very/deep/module/handler.ts", Line: i + 1,
			Message: strings.Repeat("assertion removed ", 40), Hint: strings.Repeat("restore the assertion ", 40),
		})
	}
	for _, style := range []coachStyle{editCoach, postCoach, stopCoach} {
		msg := coach(style, found, true)
		if len(msg) > maxCoachLen {
			t.Errorf("style %d: %d characters, want <= %d", style, len(msg), maxCoachLen)
		}
		if n := strings.Count(msg, "✗ "); n < 1 || n > maxListed {
			t.Errorf("style %d: %d findings listed", style, n)
		}
		if !utf8.ValidString(msg) {
			t.Errorf("style %d: message is not valid UTF-8", style)
		}
		wantContains(t, "message", msg, "more.", "Do not skip, delete or weaken tests", "narc:allow rule-0")
	}
}

func TestWaiverExample(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"src/app.ts":                `// narc:allow r reason="<why>"`,
		"pkg/server/server_test.go": `// narc:allow r reason="<why>"`,
		"tests/test_api.py":         `# narc:allow r reason="<why>"`,
		".github/workflows/ci.yml":  `# narc:allow r reason="<why>"`,
		"Makefile":                  `# narc:allow r reason="<why>"`,
		"package.json":              `an allow entry in .narc.yml (rule: r, path: "package.json", reason: "<why>")`,
		"docs/README.md":            `an allow entry in .narc.yml (rule: r, path: "docs/README.md", reason: "<why>")`,
	} {
		if got := waiverExample(core.Finding{RuleID: "r", Path: path, Line: 3}); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
	// A finding about a whole file (a deleted test file, say) has no line
	// an inline comment could sit on.
	deleted := core.Finding{RuleID: "protected-path", Path: "tests/contract/test_api.py", Message: "protected file was deleted"}
	if got, want := waiverExample(deleted), `an allow entry in .narc.yml (rule: protected-path, path: "tests/contract/test_api.py", reason: "<why>")`; got != want {
		t.Errorf("deleted file: %s, want %s", got, want)
	}
	wantContains(t, "coach", coach(editCoach, []core.Finding{deleted}, false),
		`If the user explicitly asked for this change, add a waiver with a reason as an allow entry in .narc.yml (rule: protected-path, path: "tests/contract/test_api.py"`)
	wantNotContains(t, "coach", coach(editCoach, []core.Finding{deleted}, false), "inline", "moving this code")
}

func TestNotices(t *testing.T) {
	t.Parallel()
	found := []core.Finding{
		{RuleID: "test-deleted", Path: "tests/test_api.py", Line: 40, Message: `test "test_rejects_bad_token" deleted`},
		{RuleID: "ci-failure-ignored", Path: ".github/workflows/ci.yml", Line: 22, Message: "`|| true` added to `npm test`"},
	}
	want := "narc: the agent stopped with 2 unresolved verification issues after 3 blocked attempts. Review before merging:\n" +
		"✗ test-deleted tests/test_api.py:40 — test \"test_rejects_bad_token\" deleted\n" +
		"✗ ci-failure-ignored .github/workflows/ci.yml:22 — `|| true` added to `npm test`\n" +
		"Run `narc scan` for details."
	if got := releaseNotice(found, 3); got != want {
		t.Errorf("release notice:\n%s\n--- want ---\n%s", got, want)
	}
	if got := editNotice(found); got != "narc blocked an edit to .github/workflows/ci.yml, tests/test_api.py (ci-failure-ignored, test-deleted)" {
		t.Errorf("edit notice: %s", got)
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("é", 100)
	for n := 5; n < 40; n++ {
		got := truncate(s, n)
		if len(got) > n || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
			t.Fatalf("truncate(%d) = %q", n, got)
		}
	}
	if truncate("short", 10) != "short" {
		t.Fatal("short strings must not change")
	}
}

func TestCoachDropsHintsBeforeFindings(t *testing.T) {
	t.Parallel()
	var found []core.Finding
	for i := 0; i < maxListed; i++ {
		found = append(found, core.Finding{RuleID: fmt.Sprintf("rule-%d", i), Path: "pkg/a.go", Line: i + 1,
			Message: "assertions dropped 3 → 1", Hint: strings.Repeat("restore it ", 38)})
	}
	msg := coach(editCoach, found, false)
	if len(msg) > maxCoachLen {
		t.Fatalf("%d characters", len(msg))
	}
	if n := strings.Count(msg, "✗ "); n != maxListed {
		t.Errorf("%d findings listed, want all %d", n, maxListed)
	}
	if n := strings.Count(msg, "  → "); n == 0 || n == maxListed {
		t.Errorf("%d hints shown; want the first few only", n)
	}
	wantContains(t, "message", msg, "✗ rule-0 pkg/a.go:1 — assertions dropped 3 → 1\n  → restore it")
}
