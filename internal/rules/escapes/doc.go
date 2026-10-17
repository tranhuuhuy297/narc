// Package escapes holds narc's "escape hatch" rules: changes that make the
// verification tools stop looking instead of making the code correct.
//
//   - type-check-suppressed: @ts-ignore, # type: ignore, @phpstan-ignore, ...
//   - any-cast: `as any`, `as unknown as T`, `<any>`, cast(Any, ...)
//   - lint-suppressed: eslint-disable, # noqa, //nolint, #[allow(...)], ...
//   - coverage-ignored: istanbul/c8 ignore, # pragma: no cover, LCOV_EXCL, ...
//   - error-swallowed: except: pass, empty catch {}, .catch(() => {}), _ = err
//   - test-env-branch: production code that asks "am I running under a test?"
//   - hardcoded-answer: source special-cases the exact values a test uses
//
// Every rule reports only what the change introduced: a pattern that was
// moved, re-indented or reformatted (within a file or between files) is
// cancelled against the removed code it came from. See docs/ARCHITECTURE.md.
package escapes
