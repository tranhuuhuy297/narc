# narc on real commits

A fixture corpus can only show that narc does what its authors expected
([docs/accuracy.md](accuracy.md)). To see how it behaves on code nobody wrote
for it, we ran `narc audit` over the last 1,500 non-merge commits of 15 busy
open-source repositories with many AI-agent commits (October 2026).

## Method

1. `narc audit --max 1500 --format json` on each repository. Commits are
   attributed to an agent from their metadata (co-author trailers, bot
   authors, "Generated with" footers); everything else counts as human.
   Dependency and release bots are left out.
2. A first run produced 23,833 findings at all levels. Independent reviewers,
   who did not know whether a commit came from a human or an agent, read the
   real diffs of a stratified sample of 236 findings and classified each as
   *worth a look*, *accurate but routine*, or *wrong*. The patterns they found
   became new legitimate corpus cases, and the rules were recalibrated around
   one principle: **warnings and errors are for verification that existed
   before the change and got weaker; hygiene in new code is informational.**
3. A second run of the recalibrated build, then a fresh blind review of 54
   new findings at warning level or above.
4. A third pass on the two rule groups that review found weakest
   (`assertion-weakened`, and the accounting rules `test-deleted`,
   `assertions-removed`, `test-file-deleted`): every second-run finding of
   `assertion-weakened` and a sample of the accounting findings were traced
   back to their diffs, each pattern became a legitimate corpus case next to
   a cheating one that must keep firing, and the same audit was run again.

## Results (third run, warnings and errors only)

| repository | commits | agent commits | flagged | human commits | flagged | main agents |
|---|---:|---:|---:|---:|---:|---|
| `OpenHands/OpenHands` | 1,443 | 740 | 8 (1.1%) | 703 | 9 (1.3%) | OpenHands 587, Claude Code 119 |
| `PostHog/posthog` | 1,497 | 325 | 6 (1.8%) | 1,172 | 7 (0.6%) | Claude Code 321, Cursor 4 |
| `QwenLM/qwen-code` | 1,497 | 578 | 6 (1.0%) | 919 | 9 (1.0%) | Qwen Code 533, Claude Code 32 |
| `astral-sh/ruff` | 1,318 | 10 | 0 (0.0%) | 1,308 | 4 (0.3%) | Claude Code 10 |
| `block/goose` | 1,264 | 106 | 4 (3.8%) | 1,158 | 30 (2.6%) | Claude Code 96, Cursor 6 |
| `calcom/cal.com` | 1,497 | 615 | 4 (0.7%) | 882 | 17 (1.9%) | Devin 560, Claude Code 52 |
| `charmbracelet/crush` | 1,456 | 258 | 2 (0.8%) | 1,198 | 4 (0.3%) | Crush 236, Claude Code 22 |
| `coder/coder` | 1,382 | 25 | 0 (0.0%) | 1,357 | 12 (0.9%) | Claude Code 25 |
| `getsentry/sentry` | 1,489 | 263 | 4 (1.5%) | 1,226 | 26 (2.1%) | Claude Code 134, Cursor 66 |
| `home-assistant/core` | 1,480 | 500 | 4 (0.8%) | 980 | 14 (1.4%) | Claude Code 487, Copilot 9 |
| `lobehub/lobe-chat` | 1,498 | 732 | 11 (1.5%) | 766 | 14 (1.8%) | Claude Code 732 |
| `mastra-ai/mastra` | 1,494 | 57 | 2 (3.5%) | 1,437 | 24 (1.7%) | Claude Code 30, Devin 26 |
| `n8n-io/n8n` | 1,497 | 548 | 7 (1.3%) | 949 | 22 (2.3%) | Claude Code 506, Cursor 42 |
| `steveyegge/beads` | 1,475 | 848 | 23 (2.7%) | 627 | 19 (3.0%) | Claude Code 840, Cursor 8 |
| `steveyegge/gastown` | 1,473 | 719 | 3 (0.4%) | 754 | 5 (0.7%) | Claude Code 692, Copilot 13 |
| **total** | **21,760** | **6,324** | **84 (1.3%)** | **15,436** | **216 (1.4%)** | |

In these repositories, whose changes are reviewed by maintainers before they
merge, agent-attributed commits were not flagged more often than human ones
(1.3% vs 1.4%; 1.5% vs 1.6% in the second run). Merged history is the
filtered end of the pipeline: a shortcut an agent takes during a session is
usually caught by its human or by review before it lands, so it never shows
up here. The audit is a way to
look back; the live hooks and the CI check are where narc is meant to work,
inside the session and on the pull request.

### Blind review of 54 findings (second run)

| rule | findings (warning+error) | sampled | worth a look | accurate but routine | wrong |
|---|---:|---:|---:|---:|---:|
| `test-deleted` | 190 | 8 | 1 | 6 | 1 |
| `assertions-removed` | 134 | 8 | 0 | 6 | 2 |
| `test-file-deleted` | 54 | 8 | 3 | 3 | 2 |
| `skip-added` | 28 | 7 | 4 | 3 | 0 |
| `assertion-weakened` | 23 | 8 | 1 | 1 | 6 |
| `any-cast` | 4 | 4 | 4 | 0 | 0 |
| `lint-suppressed` | 4 | 4 | 0 | 4 | 0 |
| `ci-failure-ignored` | 3 | 3 | 3 | 0 | 0 |
| `verification-hook-bypassed` | 2 | 2 | 0 | 2 | 0 |
| `tautology` | 1 | 1 | 1 | 0 | 0 |
| `test-env-branch` | 1 | 1 | 1 | 0 | 0 |
| **all** | **444** | **54** | **18** | **25** | **11** |

- **80%** of findings were factually accurate (worth a look or accurate but
  routine); **33%** were judged worth a reviewer's attention in merged,
  human-reviewed history.
- The "routine" findings are almost all tests deleted or trimmed together
  with the behaviour they covered (a feature removed, a revert, an API
  narrowed). In an agent session that is still worth a glance, because it is
  also exactly what deleting a failing test looks like. The third pass (below)
  moved the ones that leave a distinct trace to info.
- `assertion-weakened` was the noisiest rule for its volume (6 of 8 sampled
  findings were wrong); the third pass reworked how it pairs assertions.

### Third pass: assertion pairing and routine removals

Warnings and errors of the four rules, second run → third run, on the same
21,760 commits:

| repository | `assertion-weakened` | `test-deleted` | `assertions-removed` | `test-file-deleted` |
|---|---:|---:|---:|---:|
| `OpenHands/OpenHands` | 8 → 2 | 14 → 9 | 16 → 5 | 5 → 5 |
| `PostHog/posthog` | 2 → 0 | 9 → 9 | 2 → 2 | 1 → 1 |
| `QwenLM/qwen-code` | 3 → 2 | 4 → 4 | 8 → 7 | 1 → 1 |
| `astral-sh/ruff` | 0 → 0 | 2 → 2 | 1 → 1 | 0 → 0 |
| `block/goose` | 2 → 1 | 19 → 19 | 17 → 17 | 2 → 2 |
| `calcom/cal.com` | 3 → 0 | 9 → 7 | 10 → 6 | 18 → 18 |
| `charmbracelet/crush` | 0 → 0 | 1 → 1 | 3 → 2 | 1 → 1 |
| `coder/coder` | 1 → 0 | 10 → 6 | 5 → 5 | 0 → 0 |
| `getsentry/sentry` | 1 → 1 | 24 → 19 | 14 → 13 | 0 → 0 |
| `home-assistant/core` | 0 → 0 | 11 → 10 | 10 → 8 | 0 → 0 |
| `lobehub/lobe-chat` | 1 → 0 | 17 → 15 | 10 → 6 | 4 → 4 |
| `mastra-ai/mastra` | 2 → 1 | 15 → 13 | 11 → 10 | 12 → 12 |
| `n8n-io/n8n` | 0 → 0 | 21 → 21 | 12 → 11 | 0 → 0 |
| `steveyegge/beads` | 0 → 0 | 31 → 30 | 7 → 3 | 9 → 8 |
| `steveyegge/gastown` | 0 → 0 | 3 → 2 | 8 → 3 | 1 → 1 |
| **total** | **23 → 7** | **190 → 167** | **134 → 99** | **54 → 53** |

Accounting errors went from 104 to 83. All rules together: 444 → 369.

**`assertion-weakened`** now reads the whole test, not just the lines the diff
pairs. Most wrong findings came from the diff lining up an old value check
with a new existence check while the test still made the specific check a
few lines away: on a subject that was renamed (`result.mcpServers["sse"].url`
→ `result["sse"].url`, same expected URL), after an existence guard on a
variable derived from the subject (`const match =
vi.mocked(mutate).mock.calls...`), in another hunk of the same test, or with
`?.` dropped. A presence check inverted into an absence check (`'x' in cfg` →
`'x' not in cfg`) and a success value replaced by "the call now fails"
(`assert_eq!(r.ok(), Some(false))` → `assert!(r.is_err())`) are changed
expectations, not looser ones; a locator found through a page
(`page.getByTestId(...)`) is no longer taken for the page. When the old
assertion used a name, or expected a distinctive value, that the same
change removed from the source code, the finding is a note. The seven left
include an exact token count turned into a range, `toHaveLength(6)` turned
into `> 0`, and content checks collapsed into `toBeTruthy()`.

**The accounting rules** keep the line drawn by the corpus: removing an error
check, a guard or a branch together with the test that exercised it is the
"break the code, delete the test" move, and stays an error, because the old
test still compiles and fails. What moved to info are removals that leave a
trace a silenced test does not:

- the old test could no longer compile or run against the new code: it used
  a function, field, parameter or enum member the change removed from the
  source code; it passed `undefined`/`None` to a parameter whose type no
  longer admits it (`chatID: string | undefined` → `chatID: string`); or it
  checked the error of a Go function that no longer returns one;
- it expected a log line or text, or a distinctive value such as a version
  pin, that the change dropped from the source code without a replacement,
  or a config key or query parameter that the change retired;
- the commit is a labelled revert (`Revert "..."`, `revert(scope): ...`,
  "This reverts commit ...");
- the lost checks were absence checks such as
  `expect(mock).not.toHaveBeenCalled()`, or were folded into a helper that
  fails with `raise AssertionError(...)` / `pytest.fail(...)`.

Tests deleted in a change that touches no source code, or only code they do
not exercise (about 150 of the remaining findings), stay at warning on
purpose: that is the classic shortcut.

Every pattern the reviewers marked as wrong or routine is now a legitimate
case in the corpus, so it cannot regress silently. If narc is wrong on your
code, [report it](../../../issues/new?template=false-positive.yml) — the
report becomes a corpus case too.
