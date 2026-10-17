// Package tests holds narc's rules about test code itself: tests that were
// switched off (skip-added, focus-added), assertions that cannot fail
// (tautology), were made weaker (assertion-weakened) or were commented out
// (assertion-commented), and retries that paper over failures
// (retry-added). See docs/ARCHITECTURE.md.
//
// The rules share a small toolkit:
//
//   - text.go: valueText (code with string literals restored in a form that
//     cannot confuse argument splitting), literals and expression helpers;
//   - context.go: test names, guard conditions around a skip, environment
//     checks and issue references;
//   - introduced.go: net-new detection for markers, so moved, reformatted,
//     lightly edited or narrowed markers never count as new;
//   - fresh.go: whether the test a marker or assertion belongs to was added
//     by the change (renames and moves are not new), and how skips of new
//     tests are judged;
//   - jsdecl.go: JavaScript declaration forms (it['skip'], continuation
//     lines, options objects, JSX text);
//   - assertions.go: a multi-framework assertion parser used by tautology,
//     assertion-weakened and assertion-commented, which also joins
//     assertions that a formatter split over several lines (chained);
//   - wrapped.go: assertions disabled by a block comment added around them.
package tests
