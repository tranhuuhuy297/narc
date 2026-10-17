# Changelog

## v0.1.0 — first release

- `narc scan`: flags changes that weaken verification — skipped or focused tests,
  deleted or hollowed tests, removed or weakened assertions, tautologies, retries,
  type and lint suppressions, `any` casts, swallowed errors, test-environment
  branches, hard-coded answers, `|| true` and removed steps in CI, neutered test
  scripts, lowered coverage and strictness, excluded tests, forced snapshot updates,
  bypassed hooks, protected paths, and attempts to switch narc itself off.
  29 rules across JavaScript/TypeScript, Python, Go, Rust, Java, Kotlin, C#, PHP,
  Ruby, Swift, Dart, Elixir and C/C++, plus CI and tooling configs.
- Output as pretty terminal, JSON, SARIF 2.1.0, Markdown and GitHub annotations.
- Live agent guardrails: `narc install claude | codex | gemini | cursor | opencode | git`.
- GitHub Action with inline annotations, code-scanning upload and a sticky PR comment.
- `narc audit`: per-agent rap sheet of git history, with a shareable SVG card.
- Inline (`narc:allow <rule> reason="..."`) and config waivers that require a reason.
- Calibrated on 21,760 real commits from 15 open-source repositories; see docs/real-world.md.
