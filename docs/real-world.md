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

## Results (second run, warnings and errors only)

| repository | commits | agent commits | flagged | human commits | flagged | main agents |
|---|---:|---:|---:|---:|---:|---|
| `OpenHands/OpenHands` | 1,443 | 740 | 16 (2.2%) | 703 | 13 (1.8%) | OpenHands 587, Claude Code 119 |
| `PostHog/posthog` | 1,497 | 325 | 6 (1.8%) | 1,172 | 8 (0.7%) | Claude Code 321, Cursor 4 |
| `QwenLM/qwen-code` | 1,497 | 578 | 6 (1.0%) | 919 | 10 (1.1%) | Qwen Code 533, Claude Code 32 |
| `astral-sh/ruff` | 1,318 | 10 | 0 (0.0%) | 1,308 | 4 (0.3%) | Claude Code 10 |
| `block/goose` | 1,264 | 106 | 4 (3.8%) | 1,158 | 31 (2.7%) | Claude Code 96, Cursor 6 |
| `calcom/cal.com` | 1,497 | 615 | 5 (0.8%) | 882 | 21 (2.4%) | Devin 560, Claude Code 52 |
| `charmbracelet/crush` | 1,456 | 258 | 3 (1.2%) | 1,198 | 4 (0.3%) | Crush 236, Claude Code 22 |
| `coder/coder` | 1,382 | 25 | 0 (0.0%) | 1,357 | 14 (1.0%) | Claude Code 25 |
| `getsentry/sentry` | 1,489 | 263 | 4 (1.5%) | 1,226 | 30 (2.4%) | Claude Code 134, Cursor 66 |
| `home-assistant/core` | 1,480 | 500 | 5 (1.0%) | 980 | 15 (1.5%) | Claude Code 487, Copilot 9 |
| `lobehub/lobe-chat` | 1,498 | 732 | 13 (1.8%) | 766 | 16 (2.1%) | Claude Code 732 |
| `mastra-ai/mastra` | 1,494 | 57 | 2 (3.5%) | 1,437 | 28 (1.9%) | Claude Code 30, Devin 26 |
| `n8n-io/n8n` | 1,497 | 548 | 7 (1.3%) | 949 | 23 (2.4%) | Claude Code 506, Cursor 42 |
| `steveyegge/beads` | 1,475 | 848 | 23 (2.7%) | 627 | 19 (3.0%) | Claude Code 840, Cursor 8 |
| `steveyegge/gastown` | 1,473 | 719 | 4 (0.6%) | 754 | 8 (1.1%) | Claude Code 692, Copilot 13 |
| **total** | **21,760** | **6,324** | **98 (1.5%)** | **15,436** | **244 (1.6%)** | |

In these repositories, whose changes are reviewed by maintainers before they
merge, agent-attributed commits were not flagged more often than human ones
(1.5% vs 1.6%). Merged history is the filtered end of the pipeline: a
shortcut an agent takes during a session is usually caught by its human or
by review before it lands, so it never shows up here. The audit is a way to
look back; the live hooks and the CI check are where narc is meant to work,
inside the session and on the pull request.

### Blind review of 54 findings

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
  also exactly what deleting a failing test looks like, which is why these
  stay at warning.
- `assertion-weakened` is the noisiest rule for its volume (6 of 8 sampled
  findings were wrong), and is the next one to improve.

Every pattern the reviewers marked as wrong or routine is now a legitimate
case in the corpus, so it cannot regress silently. If narc is wrong on your
code, [report it](../../../issues/new?template=false-positive.yml) — the
report becomes a corpus case too.
