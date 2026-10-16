<div align="center">

# narc

**Your AI agent says the tests pass. narc checks whether it cheated to get there.**

[![ci](https://github.com/huyth45/narc/actions/workflows/ci.yml/badge.svg)](https://github.com/huyth45/narc/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.23+-00ADD8?logo=go)](go.mod)
[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
![offline](https://img.shields.io/badge/runs-100%25%20offline-brightgreen)
![no API keys](https://img.shields.io/badge/API%20keys-none-brightgreen)

<img src="docs/demo.gif" alt="narc catching an agent that skipped a test, weakened an assertion, lowered coverage and added || true to CI" width="820">

</div>

Coding agents are under pressure to make the build green, and sometimes they take the shortcut: they skip the failing test, delete the assertion that fails, replace `toEqual(42)` with `toBeDefined()`, add `|| true` to CI, lower the coverage threshold, sprinkle `as any`, or quietly special-case the exact input the test uses.

Reading every diff for that is exhausting. **narc** reads the diff for you and flags changes that **weaken verification instead of fixing code**. It runs offline in milliseconds, needs no API key, and only looks at what *changed*, so it never nags about your legacy code.

```bash
brew install huyth45/tap/narc     # or: go install github.com/huyth45/narc/cmd/narc@latest
narc                              # scan uncommitted changes
narc install claude               # block cheating edits live in Claude Code (also codex, gemini, cursor, opencode)
narc audit                        # rap sheet: which agents weakened tests in your git history
```

## What it catches

| | rule | example |
|---|---|---|
| 🧪 | `skip-added` `focus-added` | `it.skip(...)`, `@pytest.mark.skip`, `t.Skip()`, `#[ignore]`, `@Disabled`, `.only` |
| 🧪 | `test-deleted` `test-file-deleted` | a failing test that silently disappears (moves and renames are recognized) |
| 🎯 | `assertions-removed` `test-hollowed` | `rejects expired tokens: 3 assertions → 1`, or a test that now asserts nothing |
| 🎯 | `tautology` | `expect(true).toBe(true)`, `assert True`, `assert_eq!(x, x)` |
| 🎯 | `assertion-weakened` | `toEqual(user)` → `toBeDefined()`, `pytest.raises(TokenExpired)` → `pytest.raises(Exception)` |
| 🎯 | `assertion-commented` `retry-added` | `// expect(total).toBe(107)`, `jest.retryTimes(5)`, `@pytest.mark.flaky` |
| 🙈 | `type-check-suppressed` `any-cast` `lint-suppressed` | `@ts-ignore`, `as any`, `# type: ignore`, `eslint-disable`, `//nolint` |
| 🙈 | `error-swallowed` `coverage-ignored` | `except: pass`, `catch {}`, `/* istanbul ignore next */` |
| 🕵️ | `test-env-branch` `hardcoded-answer` | `if (process.env.NODE_ENV === 'test') return true`, `if (n === 7) return 5040` |
| 🏗️ | `ci-failure-ignored` `ci-check-removed` | `npm test \|\| true`, `continue-on-error: true`, a deleted `pytest` step |
| 🏗️ | `coverage-lowered` `strictness-lowered` | `lines: 90 → 60`, `"strict": true → false` |
| 🏗️ | `test-script-neutered` `tests-excluded` `snapshot-update-forced` | `"test": "echo ok"`, `--passWithNoTests`, `-k "not slow"`, `-u` |
| 🔒 | `protected-path` | any change to files you list under `protected:` (golden files, contract tests) |

Languages: JavaScript, TypeScript, Python, Go, Rust, Java, Kotlin, C#, PHP, Ruby, Swift, Dart, Elixir, plus GitHub Actions, GitLab CI, CircleCI, Azure Pipelines, Jenkins, Buildkite and the usual config files (`package.json`, `tsconfig.json`, `pyproject.toml`, `jest`/`vitest`/`pytest`/`coverage` configs, Makefiles…). See all rules with `narc rules` or in [docs/rules.md](docs/rules.md).

## Four ways to use it

### 1. Before you commit or merge

```bash
narc                     # staged + unstaged + untracked changes vs HEAD
narc --staged            # exactly what you are about to commit
narc --base main         # what your PR contains
narc HEAD~3..HEAD        # any range
git diff | narc --patch -
```

Exit code `1` when something at or above `--fail-on` (default `error`) is found, so it drops into any script. `narc install git` adds a pre-commit hook.

### 2. As a live guardrail for your agent

```bash
narc install claude      # Claude Code: .claude/settings.json
narc install codex       # Codex CLI: .codex/hooks.json
narc install gemini      # Gemini CLI: .gemini/settings.json
narc install cursor      # Cursor: .cursor/hooks.json
narc install opencode    # OpenCode: .opencode/plugins/narc.ts
```

narc inspects every edit **before it is written**. When the agent tries to skip a test or weaken an assertion, the edit is denied and the agent is told exactly why and what to do instead. When it tries to finish with a weakened suite, narc sends it back to work (with a loop cap, so it never traps your session). Most of the time the agent then fixes the real bug.

### 3. In CI, on every pull request

```yaml
# .github/workflows/narc.yml   (or: narc install github)
on: pull_request
permissions: { contents: read, pull-requests: write, security-events: write }
jobs:
  narc:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: huyth45/narc@v1
```

You get inline annotations on the exact lines, a SARIF upload to code scanning, a job summary and a sticky PR comment. Elsewhere, use `--format sarif|markdown|json|github`.

### 4. Audit your history: the rap sheet

```bash
narc audit                       # last 1000 commits
narc audit --since "6 months ago" --card rapsheet.svg
```

narc attributes each commit to the agent that wrote it (from `Co-Authored-By` trailers, bot authors and similar traces left by Claude Code, Codex, Copilot, Cursor, Devin, Jules, Aider, Amp, OpenCode and others), runs every rule on that commit, and prints who weakened verification and how often. Human commits are always shown as a baseline, so the comparison is fair. `--card` writes a 1200×630 image you can share.

## When a change is legitimate

Sometimes skipping a test *is* the right call. Say why, right where it happens:

```ts
// narc:allow skip-added reason="upstream sandbox is down, tracked in #812"
it.skip("charges the card", async () => { ... })
```

or for a whole path, in `.narc.yml`:

```yaml
allow:
  - rule: test-file-deleted
    path: "tests/legacy/**"
    reason: "legacy suite replaced by tests/v2 (#1234)"
```

A waiver without a reason does not count, and narc tells you so. Reasons show up in reports, so your waivers double as an audit trail. Run `narc init` for a commented config file.

## How it works

- **Diff-aware.** narc parses the diff and loads the old and new version of each changed file. It only reports patterns the change *introduced*. Moved, re-indented or reformatted code does not count.
- **Understands code, not just lines.** A small lexer separates code, comments and strings in each language, so `"it.skip"` inside a string or a comment is ignored, and `// expect(x)` replacing a live `expect(x)` is caught.
- **Counts assertions per test.** narc finds test functions (Jest/Vitest/Mocha, pytest/unittest, Go, Rust, JUnit, xUnit/NUnit, PHPUnit, RSpec/minitest, XCTest, Dart, ExUnit) and compares assertion counts for the same test before and after, matching tests that moved or were renamed.
- **Deterministic and private.** No LLM, no network, no telemetry. Same diff, same answer.

## Accuracy

Every rule is specified by a corpus of realistic cheating diffs *and* legitimate look-alikes (refactors, renames, moved code, deliberate skips with issue links, strings that mention `.skip`…). See [docs/accuracy.md](docs/accuracy.md) for per-rule precision and recall, regenerated with `make accuracy`.

## When narc is wrong

narc is a heuristic tool, and it will sometimes be wrong:

- It can flag a legitimate change: deleting an obsolete test, or loosening an assertion the spec really did loosen. Add a waiver with a reason, and please [report it](../../issues/new?template=false-positive.yml).
- It can miss a cheat it has no rule for, or one spread across files in an unusual way. [Report those too](../../issues/new?template=missed-cheat.yml).
- Audit attribution only sees what agents leave in commit metadata. An agent that leaves no trace is counted as a human.

Treat findings as "a human should look at this", not as a verdict.

## FAQ

**Does it send my code anywhere?** No. narc is a single static binary that never makes network calls.

**Is it slow?** Typical diffs take a few milliseconds. `narc audit` scans about a thousand commits in seconds.

**Will it nag about my existing code?** No. It only looks at what changed.

**Can the agent just remove the hook?** narc also runs in CI (`narc install github`), and you can list `.claude/`, `.narc.yml` and your CI files under `protected:`, so tampering with them is itself flagged.

## Contributing

The most useful contribution is a `.case` file that shows narc getting something wrong. See [CONTRIBUTING.md](CONTRIBUTING.md) and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

If narc saves you from merging a hollowed-out test suite, consider [sponsoring](https://github.com/sponsors/huyth45). Org-wide policies, dashboards and audit exports for teams are on the roadmap; open a discussion if you need them.

## License

MIT
