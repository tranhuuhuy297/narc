# narc architecture

narc is a single static Go binary that reads a diff (working tree, staged
changes, a commit range, a PR, or one agent edit), and reports changes that
**weaken verification instead of fixing code**: skipped tests, deleted or
hollowed assertions, tautologies, `|| true` in CI, lowered coverage
thresholds, type/lint escapes, test-environment special-casing, and so on.

It is deterministic, offline, needs no API keys, and only ever looks at what
*changed*, so it never complains about legacy code.

## Design principles

1. **Precision over recall.** A false "your agent cheated" accusation kills
   trust. Every rule must ship with clean (legitimate) corpus cases that it
   does *not* fire on. When in doubt, lower the severity rather than drop the
   rule.
2. **Diff-only, net-new only.** A pattern counts only if the change
   *introduced* it. Moving or reformatting code must not fire — use
   `rulekit.Introduced` / `rulekit.IntroducedNet`.
3. **Specific messages.** Say exactly what happened:
   `assertions in "rejects expired tokens" dropped 3 → 1`, not "assertions changed".
4. **Coaching hints.** Every finding has a hint that tells a human *or an agent*
   what to do instead. Hooks feed these hints back to the agent.
5. **Waivers need reasons.** `narc:allow <rule> reason="..."` inline, or
   `allow:` in `.narc.yml`. No reason → waiver ignored + `waiver-missing-reason`.
6. **Never crash the user's workflow.** Rules run under `recover()`. Hooks fail
   open on internal errors.
7. **No new dependencies** beyond `gopkg.in/yaml.v3`.

## Severity semantics

| severity | label        | meaning                                                         |
|----------|--------------|-----------------------------------------------------------------|
| error    | BUSTED       | high-confidence integrity violation (blocks agents by default)  |
| warning  | SUSPICIOUS   | often a cheat, sometimes legitimate; needs a human look         |
| info     | NOTE         | worth knowing; never fails a build by default                   |

## Package layout

```
cmd/narc/                 CLI entry point (flag-based subcommands)
internal/diff/            unified diff parser + Myers line diff + formatter
internal/lang/            language detection + lexer (code / comment / string per line)
internal/core/            Finding, Rule registry, FileInfo, Analysis, classification, globs
internal/config/          .narc.yml
internal/engine/          runs rules, applies waivers, scores, sorts
internal/git/             git CLI wrapper, changeset constructors, cat-file blob reader
internal/testblocks/      test-function extraction + per-test assertion counting
internal/rules/rulekit/   helpers shared by rules (Introduced, Pairs, CallArgs, ...)
internal/rules/tests/     skip/focus markers, tautologies, weakened/commented assertions, retries
internal/rules/accounting/ deleted tests, deleted test files, assertions removed, hollowed tests
internal/rules/escapes/   type/lint suppressions, any-casts, swallowed errors, test-env branches, hardcoded answers
internal/rules/ci/        CI failure ignored, checks removed, test scripts neutered, coverage/strictness lowered, tests excluded, snapshot updates
internal/rules/integrity/ protected paths, narc-config-weakened (+ waiver-missing-reason, produced by the engine)
internal/rules/all/       imports every rule package (blank imports)
internal/ruletest/        fixture-corpus harness (*.case files)
internal/report/          pretty terminal, JSON, SARIF 2.1.0, Markdown, GitHub annotations
internal/audit/           git-history audit: agent attribution, rap sheet, SVG share card
internal/hook/            agent hook handlers + installers (Claude Code, Codex, Gemini, Cursor, OpenCode, git)
```

Dependency direction: `diff, lang → core → config → engine`; rules import
`core, lang, diff, rulekit, testblocks` (never `engine`); `ruletest` imports
`engine`; `report, audit, hook` import `engine, core, git, config`; `cmd/narc`
imports everything.

## Key types (internal/core)

- `core.Changeset{Files []*diff.FileDiff; Source ContentSource; Label}` — input.
  `Source` gives full old/new file contents when available (git modes, hooks,
  corpus); it is nil for bare patches, in which case `FileInfo` falls back to
  lexing hunk fragments.
- `core.FileInfo` — one changed file: `Path, OldPath, Lang, IsTest, IsCI,
  Config` (config kind, see `core.Cfg*`), `Added/Removed []core.Line` (lexed),
  plus `OldLines()/NewLines()` (full lexed content or nil), `NewLine(n)`,
  `OldLine(n)`, `OldContent()/NewContent()`, `AnchorForOld(oldNo)`.
- `core.Line` embeds `lang.LexedLine{Raw, Code, NoComment, Comment, Strings}`
  and adds `No` (line number on its own side) and `Hunk`.
  - `Code`: comments removed, string *contents* removed (`it.skip("x")` → `it.skip("")`).
  - `NoComment`: comments removed, strings intact.
  - `Comment`: comment text only (markers stripped).
- `core.Rule{ID, Title, Category, Severity, Summary, Docs, Hint, Check}`.
  Build findings with `rule.Finding(file, newLine, msg)` (added code) or
  `rule.Removal(file, oldLine, msg)` (removed code). Override `Severity`,
  `Evidence`, `Before`, `Hint` on the returned value when useful.
- `core.Analysis{Files, Settings, Source}`; `Settings{Protected, TestGlobs}`.
  `a.ReadNew(path)` / `a.ListNew(dir)` read files the change did *not* touch
  (e.g. the existing tests of a changed source file); they return false in
  patch-only mode. `a.Memo(key, build)` caches work shared between rules.

### Writing a rule

```go
var skipAdded = &core.Rule{ID: "skip-added", Title: "...", Category: "tests",
    Severity: core.Error, Summary: "...", Hint: "...", Docs: "..."}

func init() {
    skipAdded.Check = checkSkip   // assign here: avoids an init cycle
    core.Register(skipAdded)
}
```

- Match against `l.Code` for code patterns, `l.Comment` for directives such as
  `@ts-ignore` / `# noqa`, `l.NoComment` when you need string literal values.
- Report only net-new patterns: `rulekit.Introduced(files, re, rulekit.Code)`.
- Use `rulekit.Pairs(f)` for "X was replaced by Y" rules (thresholds, matchers).
- Filter by `f.Lang`, `f.IsTest`, `f.IsCI`, `f.Config`.
- Keep rules fast: the audit runs every rule over thousands of commits.

## Fixture corpus (internal/ruletest)

Each rule package has `testdata/*.case` files and a test:

```go
package tests_test   // external test package!
import (
    "testing"
    _ "github.com/tranhuuhuy297/narc/internal/rules/tests"
    "github.com/tranhuuhuy297/narc/internal/ruletest"
)
func TestCorpus(t *testing.T) { ruletest.RunDir(t, "testdata") }
```

Case format:

```
# One-line description of the scenario.
expect: skip-added, any-cast        # or: expect: clean
-- old/src/auth.test.ts --
...old content...
-- new/src/auth.test.ts --
...new content...
```

Files only under `old/` are deletions, only under `new/` additions, and
`both/` sections are unchanged files (visible through `ReadNew`). Optional
`-- .narc.yml --` section and `patch-only: true` header. A case asserts the
exact *set* of rule IDs that fire (among rules registered in the test
binary); `expect: any-cast@info` also pins the highest severity.
`ruletest.RunDirPatchOnly` re-runs a corpus without full contents.

Name cheat cases `<rule>-<scenario>.case` and legitimate ones
`<rule>-legit-<scenario>.case`. Every rule needs **both**. Make them realistic:
real-looking code in several languages, the kind of diff a coding agent would
actually produce.

`internal/rules/all` has a test that runs the whole corpus with every rule and
prints precision/recall (`go test ./internal/rules/all -run Accuracy -v`).

## Rules (v1)

| ID | sev | package | what |
|----|-----|---------|------|
| skip-added | error | tests | new skip/disable markers (`it.skip`, `xit`, `@pytest.mark.skip`, `t.Skip`, `#[ignore]`, `@Disabled`, ...) |
| focus-added | error | tests | `.only`, `fit`, `fdescribe` — silently skips everything else |
| tautology | error | tests | assertions that cannot fail (`expect(true).toBe(true)`, `assert True`, `assert_eq!(x, x)`) |
| assertion-weakened | warning | tests | specific matcher replaced by a weaker one (`toEqual(x)` → `toBeDefined()`), tolerances widened |
| assertion-commented | error | tests | an assertion turned into a comment |
| retry-added | warning | tests | retries/flaky markers added to make a failing test pass eventually |
| test-deleted | warning/error | accounting | a test disappeared (not moved/renamed); error when source also changed |
| test-file-deleted | warning/error | accounting | a test file deleted and its tests not re-added elsewhere |
| assertions-removed | error/warning | accounting | a surviving test lost assertions (`3 → 1`) |
| test-hollowed | error | accounting | a test that asserted something now asserts nothing |
| type-check-suppressed | warning | escapes | `@ts-ignore`, `@ts-nocheck`, `# type: ignore`, `# pyright: ignore`, `@phpstan-ignore`, ... |
| any-cast | warning | escapes | `as any`, `as unknown as T`, `<any>`, `cast(Any, ...)` (info in tests) |
| lint-suppressed | warning | escapes | `eslint-disable`, `# noqa`, `//nolint`, `#[allow(...)]`, `@SuppressWarnings`, ... (info in tests) |
| coverage-ignored | warning | escapes | `istanbul ignore`, `c8 ignore`, `# pragma: no cover`, `//go:coverage ignore`, ... |
| error-swallowed | warning | escapes | `except: pass`, empty `catch {}`, `.catch(() => {})`, `_ = err` |
| test-env-branch | error | escapes | production code that checks whether it runs under tests |
| hardcoded-answer | warning | escapes | source special-cases a literal that the tests use as input/expected output |
| ci-failure-ignored | error | ci | `continue-on-error: true`, `allow_failure: true`, `|| true` on a verification command |
| ci-check-removed | error | ci | a test/lint/typecheck step removed or disabled (`if: false`) in CI |
| test-script-neutered | error | ci | `"test"` script / Makefile target replaced, `--passWithNoTests`, `-k "not ..."`, `--deselect` |
| coverage-lowered | error | ci | coverage thresholds lowered or removed |
| strictness-lowered | warning | ci | `"strict": false`, mypy `ignore_errors`, eslint rules turned off, PHPStan level lowered |
| tests-excluded | warning | ci | test paths added to ignore/exclude lists; `//go:build ignore` on a test file |
| snapshot-update-forced | warning | ci | `-u` / `--updateSnapshot` / `UPDATE_SNAPSHOTS=1` added to test commands |
| verification-hook-bypassed | warning | ci | `--no-verify`, `HUSKY=0`, pre-commit hooks removed |
| protected-path | error | integrity | change under a `protected:` glob (new files are fine) |
| narc-config-weakened | error/warning | integrity | `.narc.yml` loosened (rules off, `fail_on: never`, ignores/waivers added, protected paths dropped) or narc's hooks/CI step removed |
| waiver-missing-reason | warning | integrity (engine) | `narc:allow` without a reason |

## CLI (cmd/narc)

```
narc scan [range] [--staged] [--base REF] [--patch FILE|-] [--format pretty|json|sarif|markdown|github]
          [--fail-on error|warning|info|never] [--output FILE] [--no-untracked] [--show-waived] [-C DIR]
narc audit [--since DATE] [--max N] [--all] [--format pretty|json|markdown] [--card FILE.svg] [--agent NAME]
narc hook <harness> <event>        # called by agent hooks; reads JSON on stdin
narc install <claude|codex|gemini|cursor|opencode|git|github> [--user|--project|--local]
narc uninstall <harness> [...]
narc rules [--format markdown]     # list rules
narc explain <rule-id>
narc init                          # write .narc.yml
narc version
```

Exit codes: 0 clean (below `--fail-on`), 1 findings at/above `--fail-on`,
2 usage or runtime error. Hooks use each harness's own protocol.

## Test blocks (internal/testblocks)

`testblocks.Find(lang, lines)` returns the tests in a lexed file (JS/TS,
Python, Go, Rust, Java, Kotlin, Scala, C#, PHP, Ruby, Swift, Dart, Elixir,
C/C++) with qualified names (`"auth > rejects expired tokens"`,
`"TestAuth::test_expired"`), line ranges, and per-test assertion counts.
Structure is matched on `Code` (strings and comments blanked), names come
from the literals. Calls to helpers named `assert*/expect*/verify*/check*/
should*/must*/ensure*` count as assertions, so extracting assertions into a
helper is not a removal.

The accounting rules build one ledger per analysis: tests on both sides of
every changed test file, matched across the whole change (same name, same
name under renamed suites, moved to another file, then body similarity), and
assertions counted across all changed files, so moved assertions, merged
table-driven tests and helpers are credited.

## Hooks (internal/hook)

- **Pre-edit** (Claude `Write|Edit|MultiEdit`, Codex `apply_patch`, Gemini
  `write_file|replace`, Cursor `Write|StrReplace|Edit|Delete`, OpenCode
  `edit|write|apply_patch`): replay the edit in memory, compare the file at
  the session baseline with its proposed content (with the session's other
  changes as context), and **deny** the edit when findings *introduced by
  this edit* reach `hook.block_on` (default error). An edit that would make
  `.narc.yml` unparseable is denied.
- **Stop / AfterAgent / session.idle**: scan everything changed since the
  session baseline (recorded at SessionStart or the first hook call, else
  HEAD), including untracked files, ignoring findings that already existed
  at session start. Block the stop with a coaching reason; after
  `hook.max_blocks` blocks let the agent stop and warn the human
  (`systemMessage`). State lives in `<git-dir>/narc/sessions/<id>.json`.
- **git pre-commit**: scan staged changes, exit 1 at the fail threshold.
- Fail open on internal errors (exit 0, note on stderr). `NARC_HOOK_LOG`
  captures raw payloads for debugging.

## Audit (internal/audit)

Walks `git log --no-merges`, attributes each commit to an agent (trailers such
as `Co-Authored-By: Claude`, bot authors like `copilot-swe-agent[bot]`,
`(aider)` author suffixes, "Generated with" footers, ...) or to a human, runs
the engine on each commit's own diff, and aggregates a **rap sheet**: per
agent (and model), commits, flagged commits, offenses by rule, and the
worst offenses with links. Humans are always shown as a baseline so the
comparison is fair. `--card` renders a 1200×630 SVG share card.
