# Contributing to narc

Thanks for helping keep coding agents honest.

## The fastest useful contribution: a corpus case

narc's rules are specified by a fixture corpus. Every rule has cases it
**must** flag and legitimate cases it **must not** flag. If narc got
something wrong, the best bug report is a `.case` file:

```
# Agent skipped the failing test instead of fixing it.
expect: skip-added
-- old/src/auth.test.ts --
it('rejects expired tokens', () => { ... })
-- new/src/auth.test.ts --
it.skip('rejects expired tokens', () => { ... })
```

Put it in `internal/rules/<package>/testdata/` (see
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for which package owns which
rule), then run:

```bash
go test ./internal/rules/...
```

Name cheat cases `<rule>-<scenario>.case` and legitimate ones
`<rule>-legit-<scenario>.case`.

## Adding a rule

1. Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — especially "Design principles".
2. Add the rule to the right package under `internal/rules/`, register it in `init()`.
3. Add at least 8 cheat cases and 8 legitimate cases, in several languages.
4. Report only *net-new* patterns (`rulekit.Introduced`), so moved code never fires.
5. Write a specific message and a hint that would help an agent fix the real problem.
6. `make vet test accuracy` and include the accuracy diff in your PR.

## Development

```bash
make build      # ./narc
make test       # go test ./...
make vet        # go vet + gofmt check
make accuracy   # corpus precision/recall → docs/accuracy.md
make demo       # run the README demo
```

narc has one dependency (`gopkg.in/yaml.v3`). Please keep it that way.
