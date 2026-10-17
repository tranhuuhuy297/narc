// Package ci holds narc's rules about the machinery that runs verification:
// CI pipelines, package scripts, Makefiles, tox/nox, git hooks and the
// configuration of test runners, coverage gates, type checkers and linters.
//
// Rules:
//
//   - ci-failure-ignored: a test/lint/type-check command made unable to fail
//     (|| true, continue-on-error, allow_failure, set +e, ...).
//   - ci-check-removed: a verification step removed from CI or disabled, and
//     not run anywhere else in the change.
//   - test-script-neutered: a test script or target turned into a no-op, or
//     its runner given filters that drop tests.
//   - coverage-lowered: a coverage threshold lowered or removed.
//   - strictness-lowered: type-checker or linter settings relaxed.
//   - tests-excluded: test paths added to ignore lists or removed from
//     include lists.
//   - snapshot-update-forced: test commands that rewrite snapshots instead of
//     comparing against them.
//   - verification-hook-bypassed: git hooks skipped or removed.
//
// The rules share four building blocks:
//
//   - shell.go and verify.go split shell text into simple commands, peel off
//     wrappers (npx, poetry run, docker compose run, ...) and classify each
//     command as verification or not (classify, classifyAll).
//   - source.go extracts the commands of every file kind that holds them
//     (CI YAML, Jenkinsfile, package.json scripts, Makefile/justfile
//     recipes, tox, pyproject task tables, shell scripts), with the
//     script/target/job they belong to.
//   - entries.go parses JSON, JS/TS config objects, YAML, TOML and INI into
//     path/value entries with line numbers, so configuration can be compared
//     before and after a change independently of formatting.
//   - resolve.go reads files the change does not touch (Analysis.ReadNew,
//     ListNew): it resolves entry points (`npm run ci`, `make all`, scripts
//     run by path, local actions) to what they run, and collects the checks
//     of unchanged pipelines that gate pull requests.
//
// Every rule compares the old and new side of the change and reports only
// what the change introduced; settings and commands that merely moved
// (between steps, jobs or files) cancel out.
package ci
