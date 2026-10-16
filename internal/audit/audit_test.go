package audit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huyth45/narc/internal/config"
	"github.com/huyth45/narc/internal/git"
	_ "github.com/huyth45/narc/internal/rules/integrity"
)

func TestAttribute(t *testing.T) {
	cases := []struct {
		name, author, email, body string
		agent, model              string
		automation                bool
	}{
		{"claude trailer", "Jane", "jane@x.io", "fix tests\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)\n\nCo-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>", "Claude Code", "Opus 4.5", false},
		{"claude plain trailer", "Jane", "jane@x.io", "fix\n\nCo-authored-by: Claude <noreply@anthropic.com>", "Claude Code", "", false},
		{"claude footer only", "Jane", "jane@x.io", "fix\n\nGenerated with Claude Code", "Claude Code", "", false},
		{"claude app", "claude[bot]", "209825114+claude[bot]@users.noreply.github.com", "Apply review", "Claude Code", "", false},
		{"copilot", "Copilot", "198982749+Copilot@users.noreply.github.com", "Initial plan", "Copilot", "", false},
		{"copilot autofix is a human accepting a suggestion", "Bob", "bob@x.io", "fix\n\nCo-authored-by: Copilot Autofix powered by AI <175728472+Copilot@users.noreply.github.com>", "", "", false},
		{"copilot agent coauthor", "Bob", "bob@x.io", "fix\n\nCo-authored-by: copilot-swe-agent[bot] <198982749+Copilot@users.noreply.github.com>", "Copilot", "", false},
		{"copilot cli", "Bob", "bob@x.io", "fix\n\nCo-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>", "Copilot", "", false},
		{"claude 1M context", "Jane", "jane@x.io", "fix\n\nCo-Authored-By: Claude Opus 4.5 (1M context) <noreply@anthropic.com>", "Claude Code", "Opus 4.5", false},
		{"codex trailer", "Jane", "jane@x.io", "fix\n\nCo-authored-by: Codex <noreply@openai.com>", "Codex", "", false},
		{"codex connector", "chatgpt-codex-connector[bot]", "199175422+chatgpt-codex-connector[bot]@users.noreply.github.com", "fix", "Codex", "", false},
		{"cursor made-with", "Jane", "jane@x.io", "fix\n\nMade-with: Cursor", "Cursor", "", false},
		{"replit", "Jane", "jane@x.io", "Update\n\nReplit-Commit-Author: Agent", "Replit Agent", "", false},
		{"replit user", "Jane", "jane@x.io", "Update\n\nReplit-Commit-Author: User", "", "", false},
		{"crush", "Jane", "jane@x.io", "fix\n\nAssisted-by: Crush:qwen3.7-max", "Crush", "qwen3.7-max", false},
		{"kernel assisted-by", "Jane", "jane@x.io", "fix\n\nAssisted-by: Claude:claude-fable-5", "Claude Code", "claude-fable-5", false},
		{"entire wrapper", "Jane", "jane@x.io", "fix\n\nEntire-Agent: Cursor\nEntire-Checkpoint: 01M52V0KCBJNPCDZXQWTFWK06J", "Cursor", "", false},
		{"gemini code assist suggestion", "Bob", "bob@x.io", "fix\n\nCo-authored-by: gemini-code-assist[bot] <176961590+gemini-code-assist[bot]@users.noreply.github.com>", "", "", false},
		{"cursor", "Cursor Agent", "cursoragent@cursor.com", "Refactor", "Cursor", "", false},
		{"devin", "devin-ai-integration[bot]", "158243242+devin-ai-integration[bot]@users.noreply.github.com", "Add feature", "Devin", "", false},
		{"jules", "google-labs-jules[bot]", "161369871+google-labs-jules[bot]@users.noreply.github.com", "Fix", "Jules", "", false},
		{"aider author", "Paul (aider)", "paul@x.io", "feat: thing", "Aider", "", false},
		{"aider trailer", "Paul", "paul@x.io", "feat: thing\n\nCo-authored-by: aider (anthropic/claude-sonnet-4-5) <aider@aider.chat>", "Aider", "anthropic/claude-sonnet-4-5", false},
		{"amp", "Sam", "sam@x.io", "fix\n\nAmp-Thread-ID: https://ampcode.com/threads/T-123\nCo-authored-by: Amp <amp@ampcode.com>", "Amp", "", false},
		{"dependabot", "dependabot[bot]", "49699333+dependabot[bot]@users.noreply.github.com", "Bump x", "", "", true},
		{"human", "Alice", "alice@x.io", "Fix race in scheduler\n\nCo-authored-by: Bob <bob@x.io>", "", "", false},
		{"human mentions claude", "Alice", "alice@x.io", "Document how to use Claude Code with this repo", "", "", false},
	}
	for _, c := range cases {
		cm := &Commit{AuthorName: c.author, AuthorEmail: c.email, Body: c.body, Subject: strings.SplitN(c.body, "\n", 2)[0], Trailers: parseTrailers(c.body)}
		a := Attribute(cm)
		if a.Agent != c.agent || a.Model != c.model || a.Automation != c.automation {
			t.Errorf("%s: got agent=%q model=%q automation=%v (signal %q), want agent=%q model=%q automation=%v",
				c.name, a.Agent, a.Model, a.Automation, a.Signal, c.agent, c.model, c.automation)
		}
	}
}

func TestRemoteToWeb(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":         "https://github.com/owner/repo",
		"https://github.com/owner/repo.git":     "https://github.com/owner/repo",
		"https://gitlab.com/group/sub/repo":     "https://gitlab.com/group/sub/repo",
		"ssh://git@codeberg.org/owner/repo.git": "https://codeberg.org/owner/repo",
		"/local/path/repo":                      "",
		"":                                      "",
	}
	for in, want := range cases {
		if got := remoteToWeb(in); got != want {
			t.Errorf("remoteToWeb(%q) = %q, want %q", in, got, want)
		}
	}
}

func gitCmd(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitAs(t *testing.T, dir, name, email, msg string, files map[string]string, when time.Time) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, nil, "add", "-A")
	date := when.Format(time.RFC3339)
	gitCmd(t, dir, []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", msg)
}

func TestRunIntegration(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, nil, "init", "-q", "-b", "main")
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	commitAs(t, dir, "Alice", "alice@x.io", "initial", map[string]string{
		"src/app.go": "package app\n", "golden/out.txt": "42\n"}, t0)
	commitAs(t, dir, "Alice", "alice@x.io", "feature", map[string]string{"src/app.go": "package app\n\nfunc F() int { return 1 }\n"}, t0.Add(time.Hour))
	commitAs(t, dir, "Jane", "jane@x.io", "make tests pass\n\nCo-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>",
		map[string]string{"golden/out.txt": "41\n"}, t0.Add(2*time.Hour))
	commitAs(t, dir, "Jane", "jane@x.io", "tidy\n\nCo-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>",
		map[string]string{"src/app.go": "package app\n\n// F returns one.\nfunc F() int { return 1 }\n"}, t0.Add(3*time.Hour))
	commitAs(t, dir, "dependabot[bot]", "49699333+dependabot[bot]@users.noreply.github.com", "Bump deps",
		map[string]string{"go.mod": "module x\n"}, t0.Add(4*time.Hour))

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	cfg, err := config.Parse([]byte("protected:\n  - \"golden/**\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Run(repo, Options{Config: cfg, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 4 || rep.Skipped != 1 {
		t.Fatalf("scanned=%d skipped=%d", rep.Scanned, rep.Skipped)
	}
	if len(rep.Agents) != 1 || rep.Agents[0].Name != "Claude Code" {
		t.Fatalf("agents: %+v", rep.Agents)
	}
	cl := rep.Agents[0]
	if cl.Commits != 2 || cl.Flagged != 1 || cl.ByRule["protected-path"] != 1 || cl.Models["Sonnet 4.5"] != 2 {
		t.Errorf("claude stats: %+v", cl)
	}
	if rep.Humans.Commits != 2 || rep.Humans.Flagged != 0 {
		t.Errorf("human stats: %+v", rep.Humans)
	}
	if !rep.From.Equal(t0) {
		t.Errorf("from = %v", rep.From)
	}

	for _, render := range []func(*bytes.Buffer) error{
		func(b *bytes.Buffer) error { return Pretty(b, rep, RenderOptions{Width: 100, HideDuration: true}) },
		func(b *bytes.Buffer) error { return Markdown(b, rep, RenderOptions{}) },
		func(b *bytes.Buffer) error { return JSON(b, rep) },
		func(b *bytes.Buffer) error { return Card(b, rep) },
	} {
		var b bytes.Buffer
		if err := render(&b); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if !strings.Contains(out, "Claude Code") {
			t.Errorf("render missing agent name:\n%s", out)
		}
	}
	var pretty bytes.Buffer
	Pretty(&pretty, rep, RenderOptions{Width: 100, HideDuration: true})
	if !strings.Contains(pretty.String(), "protected-path") || !strings.Contains(pretty.String(), "50.0%") {
		t.Errorf("pretty output:\n%s", pretty.String())
	}
	if testing.Verbose() {
		t.Log("\n" + pretty.String())
	}
}

func TestCardIsWellFormedXML(t *testing.T) {
	rep := &Report{Repo: `a&b <"repo">`, Humans: newStats("Humans", true)}
	var b bytes.Buffer
	if err := Card(&b, rep); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), `a&b`) || !strings.Contains(b.String(), "a&amp;b") {
		t.Errorf("repo name not escaped")
	}
}
