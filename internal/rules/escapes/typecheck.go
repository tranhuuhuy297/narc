package escapes

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var typeCheckSuppressed = &core.Rule{
	ID:       "type-check-suppressed",
	Title:    "Type check suppressed",
	Category: "escapes",
	Severity: core.Warning,
	Summary:  "A comment or pragma now tells the type checker to stop reporting errors (@ts-ignore, # type: ignore, @phpstan-ignore, ...).",
	Hint: "Fix the type error instead of hiding it: correct the types, narrow the value with a type guard, or fix the call. " +
		"If the checker is genuinely wrong, use the checked, specific form (`@ts-expect-error`, `# type: ignore[code]`) " +
		"with a reason, so the suppression fails as soon as the underlying problem is fixed.",
	Docs: "Type errors are verification: they are the compiler telling you a value can be the wrong shape. " +
		"Agents under pressure to get `tsc` or `mypy` green often silence the error instead of fixing it.\n\n" +
		"```ts\n// bad: the error is hidden, the bug ships\n// @ts-ignore\nconst total = order.items.reduce(sum)\n\n" +
		"// good: fix the type, or narrow it\nconst total = (order.items ?? []).reduce(sum, 0)\n```\n\n" +
		"`@ts-expect-error` is reported as a note: it at least fails once the error disappears. " +
		"In test files, `@ts-expect-error` is the normal way to feed deliberately wrong input and is not reported. " +
		"A narrow, coded ignore (`# type: ignore[arg-type]`) is a note; a bare `@ts-ignore` or `# type: ignore` in " +
		"source code is a warning, lowered to a note with a concrete reason. Not reported: `@ts-expect-error` in " +
		"type-level test suites (`*.test-d.ts`), `ignore[unreachable]` on a defensive branch, ignores the file already " +
		"has for the same error code, lines copied verbatim from code the change removed, and ignores on existing " +
		"lines when the same change widens the type checker's scope (mypy.ini, tsconfig). " +
		"File-level inline configuration that switches checks off counts too: `# mypy: disable-error-code=...`, " +
		"`# pyright: basic`, `# pyright: reportXxx=false`.",
}

func init() {
	typeCheckSuppressed.Check = checkTypeSuppressed
	core.Register(typeCheckSuppressed)
}

var (
	typescript = []lang.Lang{lang.TypeScript, lang.JavaScript}
	python     = []lang.Lang{lang.Python}
	php        = []lang.Lang{lang.PHP}
)

// typeSpecs are the type-checker suppressions narc knows.
var typeSpecs = []*dirSpec{
	{tool: "TypeScript", langs: typescript, wide: regexp.MustCompile(`nocheck`), wideWhere: "in the whole file",
		re: regexp.MustCompile(`^[\s*]*(?P<dir>@ts-(?:ignore|nocheck))\b(?P<note>.*)$`)},
	{tool: "TypeScript", langs: typescript, checked: true,
		re: regexp.MustCompile(`^[\s*]*(?P<dir>@ts-expect-error)\b(?P<note>.*)$`)},
	{tool: "Flow", langs: []lang.Lang{lang.JavaScript},
		re: regexp.MustCompile(`^[\s*]*(?P<dir>\$Flow(?:FixMe|Ignore|ExpectedError|Issue))(?:\[(?P<rules>[^\]]*)\])?(?P<note>.*)$`)},
	// Above the first statement, a bare `# type: ignore` covers the module
	// (see moduleLevelIgnore).
	{tool: "mypy", langs: python, sameLine: true, wideWhere: "in the whole module",
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>type:\s*ignore)(?:\[(?P<rules>[^\]]*)\])?(?P<note>.*)$`)},
	{tool: "Pyright", langs: python, sameLine: true,
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>pyright:\s*ignore)(?:\[(?P<rules>[^\]]*)\])?(?P<note>.*)$`)},
	{tool: "mypy", langs: python, wide: regexp.MustCompile(`.`), wideWhere: "in the whole file",
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>mypy:\s*ignore-errors)\b(?P<note>.*)$`)},
	// Inline configuration that switches error codes off for the file.
	{tool: "mypy", langs: python, wide: regexp.MustCompile(`.`), wideWhere: "in the whole file",
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>mypy:\s*disable-error-code)\s*=\s*["']?(?P<rules>[\w-]+(?:\s*,\s*[\w-]+)*)["']?(?P<note>.*)$`)},
	{tool: "Pyright", langs: python, wide: regexp.MustCompile(`.`), wideWhere: "in the whole file",
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>pyright:\s*basic)\b(?P<note>.*)$`)},
	{tool: "Pyright", langs: python, wide: regexp.MustCompile(`.`), wideWhere: "in the whole file",
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>pyright:)\s*(?P<rules>report\w+)\s*=\s*(?:false|none|"none"|information|"information")(?P<note>.*)$`)},
	{tool: "Pyre", langs: python, sameLine: true, wide: regexp.MustCompile(`all-errors`), wideWhere: "in the whole file",
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>pyre-(?:ignore|fixme)(?:-all-errors)?)(?:\[(?P<rules>[^\]]*)\])?(?P<note>.*)$`)},
	{tool: "pytype", langs: python, sameLine: true,
		re: regexp.MustCompile(`(?:^|#)\s*(?P<dir>pytype:\s*disable)\s*=\s*(?P<rules>[\w-]+(?:\s*,\s*[\w-]+)*)(?P<note>.*)$`)},
	{tool: "PHPStan", langs: php,
		re: regexp.MustCompile(`^[\s*]*(?P<dir>@phpstan-ignore-(?:next-)?line)\b(?P<note>.*)$`)},
	{tool: "PHPStan", langs: php,
		re: regexp.MustCompile(`^[\s*]*(?P<dir>@phpstan-ignore)(?:\s+(?P<rules>[\w.-]+(?:\s*,\s*[\w.-]+)*))?(?P<note>\s.*)?$`)},
	{tool: "Psalm", langs: php,
		re: regexp.MustCompile(`^[\s*]*(?P<dir>@psalm-suppress)\s+(?P<rules>[\w\\]+)(?P<note>.*)$`)},
	{tool: "the compiler", noun: "nullable warning", langs: []lang.Lang{lang.CSharp}, code: true, wide: regexp.MustCompile(`.`),
		wideWhere: "for the rest of the file",
		re:        regexp.MustCompile(`^\s*#\s*nullable\s+(?P<dir>disable)\b`)},
}

func checkTypeSuppressed(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool { return f.Lang.IsProgramming() && !isFixtureData(f.Path) })
	found := newDirectives(files, typeSpecs, typeKeys)
	if len(found) == 0 {
		return nil
	}
	copied := removedLines(a)
	widened := typeConfigChanged(a)
	var out []core.Finding
	sides := map[*core.FileInfo]*side{}
	for _, df := range found {
		f := df.file
		inTest := testish(a, f, df.line.No)
		if df.d.spec.checked && (inTest || typeTestPathRe.MatchString(f.Path)) {
			continue // the normal way to pass deliberately wrong input in a test, or a negative type assertion
		}
		if sides[f] == nil {
			sides[f] = newSide(f)
		}
		s := sides[f]
		if moduleLevelIgnore(s, df) {
			df.d.scope = scopeFile
		}
		// A line copied verbatim from code the change removed (a helper
		// inlined into two scripts) carries an existing suppression.
		if copied[rulekit.Normalize(df.line.Raw)] {
			continue
		}
		if len(df.rules) > 0 && onlyCodes(df.rules, "unreachable") {
			continue // a defensive branch the checker proves unreachable: more checking, not less
		}
		// The change widens what the type checker covers and records the
		// errors it now reports on code that was already there.
		if widened && df.d.scope != scopeFile && coversExisting(f, s, changedLines(f), df) {
			continue
		}
		// The file already silences the same error code on other lines:
		// the change follows the file's idiom for the same gap.
		if df.d.scope != scopeFile && oldFileIgnores(f, df) {
			continue
		}
		sev := typeCheckSuppressed.Severity
		if df.d.spec.checked || inTest {
			sev = core.Info
		}
		// A narrow, coded ignore (`# type: ignore[arg-type]`) silences one
		// known error on one line, usually a gap in a library's stubs; a bare
		// one hides every error there, now and later.
		if len(df.rules) > 0 && df.d.scope != scopeFile {
			sev = core.Info
		}
		if df.d.scope != scopeFile && explained(directiveNote(f, s, df)) {
			sev = downgrade(sev)
		}
		if missingStubs(f, df) {
			sev = core.Info // the library ships no types: no error in this code is hidden
		}
		fd := newFinding(typeCheckSuppressed, f, df.line.No, typeMessage(f.Lang, df))
		fd.Severity = sev
		out = append(out, fd)
	}
	return out
}

// typeTestPathRe matches type-level test suites (tsd, expect-type, dtslint),
// where `@ts-expect-error` asserts that bad code fails to compile.
var typeTestPathRe = regexp.MustCompile(`\.test-d\.[cm]?tsx?$|\.type-?tests?\.[cm]?tsx?$|(^|/)(type-?tests?|types?-tests?|tsd|dtslint|test-d)/`)

// onlyCodes reports whether every rule is one of codes.
func onlyCodes(rules []string, codes ...string) bool {
	for _, r := range rules {
		if !slices.Contains(codes, strings.ToLower(strings.TrimSpace(r))) {
			return false
		}
	}
	return len(rules) > 0
}

// removedLinesKey memoizes removedLines.
type removedLinesKey struct{}

// removedLines returns the normalized text of every line the change removed.
func removedLines(a *core.Analysis) map[string]bool {
	return a.Memo(removedLinesKey{}, func() any {
		out := map[string]bool{}
		for _, f := range a.Files {
			for _, l := range f.Removed {
				if k := rulekit.Normalize(l.Raw); k != "" {
					out[k] = true
				}
			}
		}
		return out
	}).(map[string]bool)
}

// typeConfigRe matches type checker configuration files; mentionRe, the
// lines of shared configuration files that configure a type checker.
var (
	typeConfigRe  = regexp.MustCompile(`^(?:\.?mypy\.ini|mypy\.toml|pyrightconfig\.json|ty\.toml|tsconfig[\w.-]*\.json|jsconfig\.json)$`)
	sharedCfgRe   = regexp.MustCompile(`^(?:setup\.cfg|pyproject\.toml|tox\.ini|\.pre-commit-config\.ya?ml)$`)
	typeMentionRe = regexp.MustCompile(`(?i)\b(?:mypy|pyright|basedpyright|ty|tsc|typecheck|type-check|strict\w*|include|files|packages)\b`)
)

// typeConfigChanged reports whether the change edits type checker
// configuration (adding lines to it), e.g. to type-check more code.
func typeConfigChanged(a *core.Analysis) bool {
	for _, f := range a.Files {
		base := path.Base(f.Path)
		switch {
		case typeConfigRe.MatchString(base):
			if len(f.Added) > 0 {
				return true
			}
		case sharedCfgRe.MatchString(base):
			for _, l := range f.Added {
				if typeMentionRe.MatchString(l.Raw) {
					return true
				}
			}
		}
	}
	return false
}

// oldFileIgnores reports whether the old version of f already had the
// same suppression (same directive and error codes) on another line.
func oldFileIgnores(f *core.FileInfo, df dirFinding) bool {
	if f.Diff.Kind == diff.Added {
		return false
	}
	want := typeKeys(dirHit{df.d, strings.Join(df.rules, ",")})[0]
	olds := oldSide(f) // the hunks' old lines when there is no full content
	for _, n := range olds.lineNos() {
		l, _ := olds.at(n)
		if l.Comment == "" {
			continue
		}
		for _, d := range findDirectives(typeSpecs, f.Lang, l) {
			if typeKeys(dirHit{d, strings.Join(d.rules, ",")})[0] == want {
				return true
			}
		}
	}
	return false
}

// missingStubCodes are the error codes for a third-party module without
// type information.
var missingStubCodes = toSet("import import-untyped import-not-found reportmissingimports reportmissingtypestubs " +
	"reportmissingmodulesource")

// missingStubs reports whether df only silences "module has no type stubs"
// on a Python import statement.
func missingStubs(f *core.FileInfo, df dirFinding) bool {
	if f.Lang != lang.Python || len(df.rules) == 0 || !pyImportRe.MatchString(df.line.Code) {
		return false
	}
	for _, r := range df.rules {
		if !missingStubCodes[strings.ToLower(r)] {
			return false
		}
	}
	return true
}

// moduleLevelIgnore reports whether df is a bare `# type: ignore` on a line
// of its own before any statement of a Python module: mypy then ignores
// the whole module, not one line.
func moduleLevelIgnore(s *side, df dirFinding) bool {
	if df.d.spec.tool != "mypy" || !df.d.ownLine || len(df.d.rules) > 0 ||
		!strings.HasPrefix(strings.ReplaceAll(df.d.name, " ", ""), "type:ignore") {
		return false
	}
	for n := 1; n < df.line.No; n++ {
		l, ok := s.at(n)
		if !ok || strings.TrimSpace(l.Code) != "" {
			return false
		}
	}
	return true
}

// typeKeys identifies a type suppression by its exact directive (they
// differ in strength) and the error code it covers, followed by the
// broader suppressions it may replace without being a new escape:
// `# type: ignore` narrowed to `# type: ignore[arg-type]`, or @ts-ignore
// upgraded to the checked @ts-expect-error.
func typeKeys(h dirHit) []string {
	tool := strings.ToLower(h.d.spec.tool)
	name := strings.ToLower(strings.ReplaceAll(h.d.name, " ", ""))
	r := strings.ToLower(h.rule)
	if r == "" {
		r = "*"
	}
	keys := []string{tool + ":" + name + ":" + r}
	for _, old := range renamedCodes[tool+":"+r] {
		keys = append(keys, tool+":"+name+":"+old)
	}
	if r != "*" {
		keys = append(keys, tool+":"+name+":*")
	}
	switch name {
	case "@ts-expect-error":
		keys = append(keys, tool+":@ts-ignore:*", tool+":@ts-nocheck:*")
	case "@ts-ignore":
		keys = append(keys, tool+":@ts-nocheck:*")
	}
	return keys
}

// renamedCodes maps error codes that a checker split or renamed to their
// former names, so updating `# type: ignore[import]` to the code a newer
// mypy reports is not a new suppression.
var renamedCodes = map[string][]string{
	"mypy:import-untyped":   {"import"},
	"mypy:import-not-found": {"import"},
}

func typeMessage(lg lang.Lang, df dirFinding) string {
	d := df.d
	noun := d.spec.noun
	if noun == "" {
		noun = "type error"
	}
	what := "any " + noun
	if len(df.rules) > 0 {
		what = list(df.rules) + " errors"
	}
	msg := fmt.Sprintf("%s added: %s no longer reports %s %s", d.quoted(lg), d.spec.tool, what, d.where())
	if d.spec.checked {
		msg += " (checked: the build fails once the error is gone)"
	}
	return msg
}
