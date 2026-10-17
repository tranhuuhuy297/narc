// Package accounting holds narc's test-accounting rules: tests and test
// files that disappeared (test-deleted, test-file-deleted), surviving tests
// that lost assertions (assertions-removed), and tests left with no
// assertions at all (test-hollowed).
//
// All four rules share one ledger per analysis (built once through
// Analysis.Memo). It finds the tests on both sides of every changed test
// file with package testblocks — from full file contents when both sides
// can be read, else from diff hunk fragments for both sides — and matches
// them across the whole change: by name in the same file, by bare name
// (suites reorganized), by name in another file (moved), and by body
// similarity (renamed).
//
// Assertions are accounted for globally, but only in test-side code (test
// files, test-support code, files that import a test framework): an
// assertion that moved to another test or a helper, or was folded into a
// parametrize table, an expected value or one structural assertion, is not
// a loss; one that moved into production code is. Helpers a test calls are
// credited, including helpers in unchanged files nearby (read lazily).
//
// Besides outright deletion, the ledger recognizes tests that were switched
// off while their code stays: renamed so the runner no longer collects them
// (test_x → _test_x, TestX → testX, class TestX → class X), stripped of
// their @Test/#[test]/[Fact] marker, or moved into a file the runner does
// not collect (x.test.js → x.disabled.js). It excuses deletions whose code
// under test was deleted, duplicates of tests that remain (in the change or
// in an unchanged file nearby), and assertions replaced by a run-time skip
// (reported by skip-added).
//
// Severity follows what the change did to verification that already
// existed: an error when a check stopped running next to an edit of the
// code it exercised ("break the code, delete the test"), or when
// assertions were simply deleted; a warning when the loss needs a human
// look; info when it reads as a rework or a removal (the code, flag or
// module under test went away, the test was rewritten, renamed narrower or
// replaced by a lookalike). Deletions are reported per file, and pruning
// passes and test-only assertion cleanups once per change.
//
// Most tests of a changed file are untouched by the diff. Their
// assertions are counted only on demand (testblocks.FindLazy), and the
// assertion statements far from every hunk are not counted at all, since
// they read the same on both sides. See docs/ARCHITECTURE.md.
package accounting
