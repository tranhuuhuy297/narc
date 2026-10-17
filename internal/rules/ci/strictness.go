package ci

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

var strictnessLowered = &core.Rule{
	ID:       "strictness-lowered",
	Title:    "Type or lint strictness lowered",
	Category: "ci",
	Severity: core.Warning,
	Summary:  "Type-checker or linter settings were relaxed: `\"strict\": false`, mypy `ignore_errors`, ESLint rules downgraded, PHPStan level lowered, new ruff/flake8 ignores, ...",
	Hint: "Keep the stricter setting and fix the errors it reports. If one module genuinely cannot meet it " +
		"yet, scope the exception to that module with a comment explaining why, rather than relaxing the " +
		"whole project.",
	Docs: "Turning the checker down is the configuration-level version of `// @ts-ignore`: instead of one " +
		"suppressed error, every future error of that kind disappears.\n\n" +
		"```diff\n   \"compilerOptions\": {\n-    \"strict\": true,\n+    \"strict\": false,       // BUSTED\n```\n\n" +
		"Reported (warning unless noted, with the old → new value): tsconfig `strict` true → false (error) " +
		"or removed, the `strict*`/`noImplicit*`/`noUnchecked*` family and `checkJs` turned off; mypy " +
		"`strict`, `disallow_untyped_defs`, `check_untyped_defs`, ... turned off and `ignore_errors = True` " +
		"(except for tests, migrations and stubs); pyright `typeCheckingMode` and `report*` severities " +
		"lowered; ESLint and Biome rule severities lowered (error → warn → off); PHPStan level lowered; " +
		"Psalm errorLevel raised; `--max-warnings` raised, `cargo clippy -- -D warnings` and `mypy --strict` " +
		"dropped; mypy `disable_error_code` gaining codes (except for tests). A tsconfig option removed " +
		"because the file `extends` a base is checked against that base when it can be read. Info: codes " +
		"newly ignored by ruff/flake8/pylint, golangci-lint linters disabled, RuboCop cops disabled.",
}

func init() {
	strictnessLowered.Check = scoped(checkStrictness)
	core.Register(strictnessLowered)
}

func checkStrictness(a *core.Analysis) []core.Finding {
	var out []core.Finding
	tsMoved := tsStrictElsewhere(a)
	moved := strictBaseline(a)
	for _, f := range a.Files {
		if len(f.Added)+len(f.Removed) == 0 || !isConfigFile(f) {
			continue
		}
		base := strings.ToLower(path.Base(f.Path))
		if strings.HasPrefix(base, "psalm.xml") {
			out = append(out, psalmLevel(f)...)
			continue
		}
		old, new, ok := configSides(f)
		if !ok {
			out = append(out, fragmentStrictness(f)...)
			continue
		}
		switch {
		case f.Config == core.CfgTSConfig:
			out = append(out, tsconfigStrictness(f, old, new, tsMoved, tsBaseReader(a))...)
		case f.Config == core.CfgESLint, f.Config == core.CfgPackageJSON:
			out = append(out, eslintStrictness(f, old, new, moved)...)
		case f.Config == core.CfgBiome:
			out = append(out, biomeStrictness(f, old, new)...)
		case f.Config == core.CfgGolangci:
			out = append(out, golangciStrictness(f, old, new)...)
		case f.Config == core.CfgPHPStan:
			out = append(out, phpstanLevel(f, old, new)...)
		case f.Config == core.CfgRubocop:
			out = append(out, rubocopStrictness(f, old, new)...)
		}
		switch f.Config {
		case core.CfgMypy, core.CfgSetupCfg, core.CfgPyproject:
			out = append(out, mypyStrictness(f, old, new, moved)...)
		}
		if f.Config == core.CfgPyproject || base == "pyrightconfig.json" {
			out = append(out, pyrightStrictness(f, old, new)...)
		}
		switch f.Config {
		case core.CfgRuff, core.CfgPyproject, core.CfgFlake8, core.CfgSetupCfg, core.CfgTox, core.CfgPylint:
			out = append(out, ignoreListStrictness(f, old, new, moved)...)
		}
	}
	out = append(out, commandStrictness(a, moved)...)
	return out
}

// baseline is changeset-wide state that lets a setting move between files
// (.eslintrc → eslint.config.js, setup.cfg → pyproject.toml, ruff.toml →
// pyproject.toml) without being reported as new or removed.
type baseline struct {
	eslintOff  map[string]bool            // ESLint rules already off in an old config
	mypyNewOn  map[string]bool            // global mypy options on in a new config
	ignoredOld map[string]map[string]bool // tool → codes ignored in an old config
	// mypyOldDisabled are mypy error codes already disabled in an old
	// config (a setup.cfg → pyproject.toml move keeps them).
	mypyOldDisabled map[string]bool
}

func strictBaseline(a *core.Analysis) baseline {
	b := baseline{eslintOff: map[string]bool{}, mypyNewOn: map[string]bool{}, ignoredOld: map[string]map[string]bool{},
		mypyOldDisabled: map[string]bool{}}
	for _, f := range a.Files {
		if !isConfigFile(f) {
			continue
		}
		old, new, ok := configSides(f)
		if !ok {
			// A bare patch: a global `strict = true` added to a mypy config
			// still shows where a dropped --strict went.
			switch f.Config {
			case core.CfgMypy, core.CfgSetupCfg, core.CfgPyproject:
				for _, l := range f.Added {
					if fragmentMypyStrictRe.MatchString(l.NoComment) {
						b.mypyNewOn["strict"] = true
					}
				}
			}
			continue
		}
		if f.Config == core.CfgESLint || f.Config == core.CfgPackageJSON {
			rules, _ := eslintRules(old, f.Config == core.CfgPackageJSON)
			for k, e := range rules {
				if eslintRank(e.Value) == 0 {
					b.eslintOff[k[strings.IndexByte(k, 0)+1:]] = true
				}
			}
		}
		for _, codes := range mypyDisabledCodes(f, old) {
			for c := range codes {
				b.mypyOldDisabled[c] = true
			}
		}
		for k, s := range mypySettings(f, new) {
			if (s.scope == "[mypy]" || s.scope == "[tool.mypy]") && truthy(s.e.Value) {
				b.mypyNewOn[k[strings.IndexByte(k, 0)+1:]] = true
			}
		}
		for tool, codes := range ignoredCodes(f, old) {
			if b.ignoredOld[tool] == nil {
				b.ignoredOld[tool] = map[string]bool{}
			}
			for c := range codes {
				b.ignoredOld[tool][c] = true
			}
		}
	}
	return b
}

// strictFinding builds a finding at the new line of e, or at the old line
// of o when the setting was removed.
func strictFinding(f *core.FileInfo, o, n *entry, sev core.Severity, msg string) core.Finding {
	if n != nil {
		fd := sevFinding(strictnessLowered, f, n.Line, sev, msg)
		if o != nil {
			if l, ok := f.OldLine(o.Line); ok {
				fd.Before = strings.TrimSpace(l.Raw)
			}
		}
		return fd
	}
	return removalFinding(strictnessLowered, f, o.Line, sev, msg)
}

// lookup indexes scalar entries by their joined path (first occurrence).
func lookup(es []entry) map[string]*entry {
	m := map[string]*entry{}
	for i := range es {
		k := strings.Join(es[i].Path, "\x00")
		if _, ok := m[k]; !ok {
			m[k] = &es[i]
		}
	}
	return m
}

func pathKey(p ...string) string { return strings.Join(p, "\x00") }

// ---- TypeScript ----

// tsStrictFamily are the checks that "strict": true enables.
var tsStrictFamily = []string{"noImplicitAny", "strictNullChecks", "strictFunctionTypes", "strictBindCallApply",
	"strictPropertyInitialization", "noImplicitThis", "alwaysStrict", "useUnknownInCatchVariables"}

// tsExtraChecks are opt-in checks.
var tsExtraChecks = []string{"noUncheckedIndexedAccess", "noImplicitReturns", "noImplicitOverride",
	"exactOptionalPropertyTypes", "noFallthroughCasesInSwitch", "noPropertyAccessFromIndexSignature", "checkJs"}

// tsStrictElsewhere collects compiler options switched on in changed
// tsconfig files, so a setting moved into a shared base config is not
// reported as removed.
func tsStrictElsewhere(a *core.Analysis) map[string]bool {
	on := map[string]bool{}
	for _, f := range a.Files {
		if f.Config != core.CfgTSConfig {
			continue
		}
		_, new, ok := configSides(f)
		if !ok {
			continue
		}
		for _, e := range new {
			if len(e.Path) == 2 && e.Path[0] == "compilerOptions" && truthy(e.Value) {
				on[e.Path[1]] = true
			}
		}
	}
	return on
}

// tsBase reads the compiler options a tsconfig "extends" value turns on,
// following the base's own extends. ok is false when the base cannot be
// read (a package in node_modules, a bare patch).
type tsBase func(from, ext string) (on map[string]bool, ok bool)

// tsBaseReader resolves relative "extends" paths against the repository at
// the new revision (changed or not).
func tsBaseReader(a *core.Analysis) tsBase {
	r := repoOf(a)
	var read func(from, ext string, depth int) (map[string]bool, bool)
	read = func(from, ext string, depth int) (map[string]bool, bool) {
		ext = strings.Trim(ext, `"' `)
		if depth > 5 || !strings.HasPrefix(ext, ".") {
			return nil, false
		}
		p := joinDir(path.Dir(from), ext)
		d, ok := r.get(p, false)
		if !ok && !strings.HasSuffix(p, ".json") {
			p += ".json"
			d, ok = r.get(p, false)
		}
		if !ok {
			return nil, false
		}
		on := map[string]bool{}
		es := parseConfig(fmtJSON, d.s.text(), d.s.lines)
		for _, e := range es {
			if len(e.Path) == 1 && e.Path[0] == "extends" {
				if inherited, ok := read(p, e.Value, depth+1); ok {
					for k, v := range inherited {
						on[k] = v
					}
				} else if !strictBase(e.Value) {
					return nil, false // part of the chain is unreadable
				} else {
					on["strict"] = true
				}
			}
		}
		for _, e := range es {
			if len(e.Path) == 2 && e.Path[0] == "compilerOptions" {
				on[e.Path[1]] = truthy(e.Value)
			}
		}
		return on, true
	}
	return func(from, ext string) (map[string]bool, bool) { return read(from, ext, 0) }
}

// enables reports whether compiler options on (from a base config) turn
// opt on, directly or through "strict".
func enables(on map[string]bool, opt string) bool {
	if v, set := on[opt]; set {
		return v
	}
	return isStrictFamily(opt) && on["strict"]
}

func tsconfigStrictness(f *core.FileInfo, old, new []entry, movedOn map[string]bool, readBase tsBase) []core.Finding {
	o, n := lookup(old), lookup(new)
	extendsChanged := !sameValue(o[pathKey("extends")], n[pathKey("extends")])
	var baseOn map[string]bool // what the new base enables, when readable
	baseRead := false
	if base := n[pathKey("extends")]; base != nil {
		baseOn, baseRead = readBase(f.Path, base.Value)
	}
	strictNow := n[pathKey("compilerOptions", "strict")] != nil && truthy(n[pathKey("compilerOptions", "strict")].Value)
	strictBefore := o[pathKey("compilerOptions", "strict")] != nil && truthy(o[pathKey("compilerOptions", "strict")].Value)
	var out []core.Finding
	opts := append(append([]string{"strict"}, tsStrictFamily...), tsExtraChecks...)
	for _, opt := range opts {
		oe, ne := o[pathKey("compilerOptions", opt)], n[pathKey("compilerOptions", opt)]
		was := oe != nil && truthy(oe.Value)
		name := "compilerOptions." + opt
		switch {
		case was && ne != nil && falsy(ne.Value):
			sev := core.Warning
			if opt == "strict" {
				sev = core.Error
			}
			out = append(out, strictFinding(f, oe, ne, sev, fmt.Sprintf("%s %s → %s", name, oe.Value, ne.Value)))
		case was && ne == nil && baseRead && enables(baseOn, opt):
			// the extended base config turns it on
		case was && ne == nil && extendsChanged && baseRead && !movedOn[opt] && !(opt != "strict" && strictNow && isStrictFamily(opt)):
			out = append(out, strictFinding(f, oe, nil, core.Warning,
				fmt.Sprintf("%s (%s) removed, and the new base %s does not enable it", name, oe.Value, quote(n[pathKey("extends")].Value))))
		case was && ne == nil && !extendsChanged && !movedOn[opt]:
			if opt != "strict" && strictNow && isStrictFamily(opt) {
				continue // still enabled by "strict": true
			}
			if opt == "strict" && allStrictFlags(n) {
				continue // "strict" spelled out as its individual checks
			}
			if opt == "checkJs" && (n[pathKey("compilerOptions", "allowJs")] == nil || !truthy(n[pathKey("compilerOptions", "allowJs")].Value)) {
				continue // allowJs went too: there is no JavaScript left to check
			}
			if base := n[pathKey("extends")]; base != nil {
				if strictBase(base.Value) {
					continue // the @tsconfig/* bases all enable "strict"
				}
				msg := fmt.Sprintf("%s (%s) removed: the check is off unless %s enables it", name, oe.Value, quote(base.Value))
				if baseRead {
					msg = fmt.Sprintf("%s (%s) removed, and the base %s does not enable it", name, oe.Value, quote(base.Value))
				}
				out = append(out, strictFinding(f, oe, nil, core.Warning, msg))
				continue
			}
			out = append(out, strictFinding(f, oe, nil, core.Warning, fmt.Sprintf("%s (%s) removed: the check is now off", name, oe.Value)))
		case !was && oe == nil && ne != nil && falsy(ne.Value) && isStrictFamily(opt) && (strictNow || strictBefore):
			// an explicit override that carves a hole into "strict": true
			out = append(out, strictFinding(f, nil, ne, core.Warning,
				fmt.Sprintf("%s set to %s, overriding \"strict\": true", name, ne.Value)))
		}
	}
	return out
}

// strictBase reports whether a tsconfig "extends" value is one of the
// shared @tsconfig/* bases, which all set "strict": true.
func strictBase(v string) bool {
	return strings.HasPrefix(strings.Trim(v, `"' `), "@tsconfig/")
}

// allStrictFlags reports whether every check of the strict family is
// explicitly on.
func allStrictFlags(n map[string]*entry) bool {
	for _, opt := range tsStrictFamily {
		e := n[pathKey("compilerOptions", opt)]
		if e == nil || !truthy(e.Value) {
			return false
		}
	}
	return true
}

func isStrictFamily(opt string) bool {
	for _, o := range tsStrictFamily {
		if o == opt {
			return true
		}
	}
	return false
}

func sameValue(a, b *entry) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Value == b.Value
}

// ---- ESLint / Biome ----

// eslintRank ranks a rule severity: 2 error, 1 warn, 0 off, -1 unknown.
func eslintRank(v string) int {
	switch strings.ToLower(strings.Trim(v, `"' `)) {
	case "error", "2":
		return 2
	case "warn", "warning", "1", "info":
		return 1
	case "off", "0", "false", "none":
		return 0
	}
	return -1
}

var rankNames = []string{"off", "warn", "error"}

// eslintRules returns rule → severity entries, keyed by rule name plus the
// "files" scope of the config block it is in.
func eslintRules(es []entry, isPackage bool) (map[string]*entry, map[string]string) {
	rules, scope := map[string]*entry{}, map[string]string{}
	for i := range es {
		e := &es[i]
		p := e.Path
		if isPackage {
			if len(p) == 0 || p[0] != "eslintConfig" {
				continue
			}
			p = p[1:]
		}
		ri := -1
		for j, k := range p {
			if k == "rules" {
				ri = j
			}
		}
		if ri < 0 || len(p) != ri+2 {
			continue
		}
		files := strings.Join(groupValues(es, e.Group, "files"), ",")
		if ri > 0 && p[0] == "overrides" {
			files = "overrides:" + files
		}
		k := files + "\x00" + p[ri+1]
		if _, seen := rules[k]; seen {
			continue // later items of ["error", {...}] are options
		}
		rules[k] = e
		scope[k] = files
	}
	return rules, scope
}

// escapeRules are rules whose whole purpose is to stop escapes from the
// type system or the test runner; switching one off is worth a note even
// when it was not configured before.
var escapeRules = map[string]bool{
	"@typescript-eslint/no-explicit-any": true, "@typescript-eslint/ban-ts-comment": true,
	"@typescript-eslint/no-non-null-assertion": true, "@typescript-eslint/no-unsafe-assignment": true,
	"@typescript-eslint/no-unsafe-member-access": true, "@typescript-eslint/no-unsafe-call": true,
	"@typescript-eslint/no-unsafe-return": true, "@typescript-eslint/no-unsafe-argument": true,
	"@typescript-eslint/no-floating-promises": true, "@typescript-eslint/no-misused-promises": true,
	"react-hooks/exhaustive-deps": true, "react-hooks/rules-of-hooks": true,
	"jest/no-disabled-tests": true, "jest/no-focused-tests": true, "vitest/no-disabled-tests": true,
	"vitest/no-focused-tests": true, "mocha/no-exclusive-tests": true, "mocha/no-skipped-tests": true,
	"no-undef": true,
}

// testScope reports whether an ESLint files scope only covers tests.
func testScope(files string) bool {
	files = strings.TrimPrefix(files, "overrides:")
	if files == "" {
		return false
	}
	for _, g := range strings.Split(files, ",") {
		if !looksLikeTestPath(g) {
			return false
		}
	}
	return true
}

func eslintStrictness(f *core.FileInfo, old, new []entry, base baseline) []core.Finding {
	isPkg := f.Config == core.CfgPackageJSON
	or, _ := eslintRules(old, isPkg)
	nr, scope := eslintRules(new, isPkg)
	var out []core.Finding
	for _, k := range sortedEntryKeys(nr) {
		ne := nr[k]
		rule := k[strings.IndexByte(k, 0)+1:]
		nrank := eslintRank(ne.Value)
		if nrank < 0 {
			continue
		}
		if oe, ok := or[k]; ok {
			if orank := eslintRank(oe.Value); orank > nrank && !testScope(scope[k]) {
				out = append(out, strictFinding(f, oe, ne, core.Warning,
					fmt.Sprintf("ESLint rule %s lowered %s → %s", rule, rankNames[orank], rankNames[nrank])))
			}
			continue
		}
		if nrank == 0 && escapeRules[rule] && !testScope(scope[k]) && !base.eslintOff[rule] {
			out = append(out, strictFinding(f, nil, ne, core.Info,
				fmt.Sprintf("ESLint rule %s turned off", rule)))
		}
	}
	return out
}

// sortedEntryScopes returns the keys of a scope map in order.
func sortedEntryScopes(m map[string]map[string]*entry) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	return sortedKeys(set)
}

func sortedEntryKeys(m map[string]*entry) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	return sortedKeys(set)
}

func biomeStrictness(f *core.FileInfo, old, new []entry) []core.Finding {
	rules := func(es []entry) map[string]*entry {
		m := map[string]*entry{}
		for i := range es {
			p := es[i].Path
			if len(p) >= 4 && p[0] == "linter" && p[1] == "rules" {
				if len(p) == 5 && p[4] != "level" {
					continue
				}
				m[p[2]+"/"+p[3]] = &es[i]
			}
			if len(p) == 2 && p[0] == "linter" && p[1] == "enabled" {
				m["(linter enabled)"] = &es[i]
			}
		}
		return m
	}
	or, nr := rules(old), rules(new)
	var out []core.Finding
	if oe, ne := or["(linter enabled)"], nr["(linter enabled)"]; ne != nil && falsy(ne.Value) && (oe == nil || truthy(oe.Value)) {
		out = append(out, strictFinding(f, oe, ne, core.Warning, "Biome linter disabled (linter.enabled = false)"))
	}
	for _, k := range sortedEntryKeys(nr) {
		oe, ne := or[k], nr[k]
		if oe == nil || k == "(linter enabled)" {
			continue
		}
		if orank, nrank := eslintRank(oe.Value), eslintRank(ne.Value); orank >= 0 && nrank >= 0 && orank > nrank {
			out = append(out, strictFinding(f, oe, ne, core.Warning,
				fmt.Sprintf("Biome rule %s lowered %s → %s", k, oe.Value, ne.Value)))
		}
	}
	return out
}

// ---- mypy / pyright ----

// mypyChecks are mypy options where True is stricter.
var mypyChecks = []string{"strict", "disallow-untyped-defs", "check-untyped-defs", "disallow-any-generics",
	"disallow-incomplete-defs", "disallow-untyped-calls", "disallow-untyped-decorators", "warn-return-any",
	"no-implicit-optional", "strict-optional", "strict-equality", "warn-unused-ignores", "no-implicit-reexport",
	"disallow-subclassing-any", "warn-redundant-casts", "extra-checks", "disallow-any-unimported"}

// lenientModuleRe matches module patterns where relaxed typing is normal.
var lenientModuleRe = regexp.MustCompile(`(?i)(^|[.\-_/*"' (,\[])(tests?|testing|conftest|migrations|alembic|stubs|vendor(ed)?|generated|_pb2|docs|examples|scripts)([.\-_/*"' ),\]]|$)`)

// mypySetting is one mypy option in a section/table.
type mypySetting struct {
	e     *entry
	scope string // "[mypy]", "[mypy-pkg.*]", "overrides(module=...)"
}

// mypySettings extracts mypy options keyed by scope + option.
func mypySettings(f *core.FileInfo, es []entry) map[string]mypySetting {
	out := map[string]mypySetting{}
	for i := range es {
		e := &es[i]
		p := e.Path
		var scope, opt string
		switch {
		case f.Config == core.CfgPyproject && len(p) == 3 && p[0] == "tool" && p[1] == "mypy":
			scope, opt = "[tool.mypy]", p[2]
		case f.Config == core.CfgPyproject && len(p) == 4 && p[0] == "tool" && p[1] == "mypy" && p[2] == "overrides":
			scope = "overrides(" + strings.Join(groupValues(es, e.Group, "module"), ",") + ")"
			opt = p[3]
		case f.Config != core.CfgPyproject && len(p) == 2 && (p[0] == "mypy" || strings.HasPrefix(p[0], "mypy-")):
			scope, opt = "["+p[0]+"]", p[1]
		default:
			continue
		}
		out[scope+"\x00"+normKey(opt)] = mypySetting{e: e, scope: scope}
	}
	return out
}

// benignMypyCodes are mypy error codes about third-party stubs and notes,
// not about type errors in the project's code.
var benignMypyCodes = map[string]bool{
	"import": true, "import-untyped": true, "import-not-found": true, "annotation-unchecked": true,
	"unused-ignore": true, "note": true,
}

// mypyDisabledCodes returns, per scope, the error codes a mypy config
// disables (disable_error_code), with the entry that names each.
func mypyDisabledCodes(f *core.FileInfo, es []entry) map[string]map[string]*entry {
	out := map[string]map[string]*entry{}
	for i := range es {
		e := &es[i]
		p := e.Path
		var scope string
		switch {
		case f.Config == core.CfgPyproject && len(p) == 3 && p[0] == "tool" && p[1] == "mypy":
			scope = "[tool.mypy]"
		case f.Config == core.CfgPyproject && len(p) == 4 && p[0] == "tool" && p[1] == "mypy" && p[2] == "overrides":
			scope = "overrides(" + strings.Join(groupValues(es, e.Group, "module"), ",") + ")"
		case f.Config != core.CfgPyproject && len(p) == 2 && (p[0] == "mypy" || strings.HasPrefix(p[0], "mypy-")):
			scope = "[" + p[0] + "]"
		default:
			continue
		}
		if normKey(e.Last()) != "disable-error-code" {
			continue
		}
		if out[scope] == nil {
			out[scope] = map[string]*entry{}
		}
		for _, c := range codeList(e.Value) {
			out[scope][c] = e
		}
	}
	return out
}

func mypyStrictness(f *core.FileInfo, old, new []entry, base baseline) []core.Finding {
	o, n := mypySettings(f, old), mypySettings(f, new)
	var out []core.Finding
	oldCodes := mypyDisabledCodes(f, old)
	newCodes := mypyDisabledCodes(f, new)
	for _, scope := range sortedEntryScopes(newCodes) {
		if scope != "[mypy]" && scope != "[tool.mypy]" && lenientModuleRe.MatchString(scope) {
			continue
		}
		var added []string
		var at *entry
		for _, c := range sortedEntryKeys(newCodes[scope]) {
			if oldCodes[scope][c] == nil && !base.mypyOldDisabled[c] && !benignMypyCodes[c] {
				added = append(added, c)
				if at == nil {
					at = newCodes[scope][c]
				}
			}
		}
		if len(added) > 0 {
			out = append(out, strictFinding(f, nil, at, core.Warning,
				fmt.Sprintf("mypy disable_error_code gained %s in %s: those type errors are no longer reported", strings.Join(added, ", "), scope)))
		}
	}
	isCheck := map[string]bool{}
	for _, c := range mypyChecks {
		isCheck[c] = true
	}
	keys := map[string]bool{}
	for k := range o {
		keys[k] = true
	}
	for k := range n {
		keys[k] = true
	}
	for _, k := range sortedKeys(keys) {
		opt := k[strings.IndexByte(k, 0)+1:]
		os, hasO := o[k]
		ns, hasN := n[k]
		scope := ns.scope
		if !hasN {
			scope = os.scope
		}
		if scope != "[mypy]" && scope != "[tool.mypy]" && lenientModuleRe.MatchString(scope) {
			continue // relaxing tests, migrations or stubs is normal
		}
		switch {
		case opt == "ignore-errors" && hasN && truthy(ns.e.Value) && (!hasO || !truthy(os.e.Value)):
			out = append(out, strictFinding(f, nil, ns.e, core.Warning,
				fmt.Sprintf("mypy ignore_errors = %s added for %s: type errors there are no longer reported", ns.e.Value, scope)))
		case !isCheck[opt]:
		case hasO && truthy(os.e.Value) && hasN && falsy(ns.e.Value):
			out = append(out, strictFinding(f, os.e, ns.e, core.Warning,
				fmt.Sprintf("mypy %s %s → %s in %s", ns.e.Last(), os.e.Value, ns.e.Value, scope)))
		case hasO && truthy(os.e.Value) && !hasN && !base.mypyNewOn[opt] && (opt == "strict" || !strictIn(n, scope)):
			out = append(out, strictFinding(f, os.e, nil, core.Warning,
				fmt.Sprintf("mypy %s (%s) removed from %s", os.e.Last(), os.e.Value, scope)))
		}
	}
	return out
}

// strictIn reports whether strict mode is on in a mypy scope.
func strictIn(m map[string]mypySetting, scope string) bool {
	s, ok := m[scope+"\x00strict"]
	return ok && truthy(s.e.Value)
}

var pyrightModes = map[string]int{"off": 0, "basic": 1, "standard": 2, "strict": 3, "recommended": 4, "all": 5}

// pyrightRank ranks a report* severity.
func pyrightRank(v string) int {
	switch strings.ToLower(strings.Trim(v, `"' `)) {
	case "error", "true":
		return 3
	case "warning":
		return 2
	case "information":
		return 1
	case "none", "false":
		return 0
	}
	return -1
}

// pyrightTypeErrors are report* rules that flag genuine type errors.
var pyrightTypeErrorRe = regexp.MustCompile(`^report(GeneralTypeIssues|Optional\w+|ArgumentType|ReturnType|AssignmentType|CallIssue|IndexIssue|AttributeAccessIssue|PossiblyUnbound|OperatorIssue|TypedDictNotRequiredAccess)$`)

func pyrightStrictness(f *core.FileInfo, old, new []entry) []core.Finding {
	settings := func(es []entry) map[string]*entry {
		m := map[string]*entry{}
		for i := range es {
			p := es[i].Path
			if f.Config == core.CfgPyproject {
				if len(p) != 3 || p[0] != "tool" || p[1] != "pyright" && p[1] != "basedpyright" {
					continue
				}
				p = p[2:]
			}
			if len(p) == 1 {
				m[p[0]] = &es[i]
			}
		}
		return m
	}
	o, n := settings(old), settings(new)
	var out []core.Finding
	if oe, ne := o["typeCheckingMode"], n["typeCheckingMode"]; oe != nil && ne != nil {
		if or, ok1 := pyrightModes[oe.Value]; ok1 {
			if nr, ok2 := pyrightModes[ne.Value]; ok2 && nr < or {
				out = append(out, strictFinding(f, oe, ne, core.Warning, fmt.Sprintf("pyright typeCheckingMode %s → %s", oe.Value, ne.Value)))
			}
		}
	}
	for _, k := range sortedEntryKeys(n) {
		if !strings.HasPrefix(k, "report") {
			continue
		}
		ne, oe := n[k], o[k]
		nr := pyrightRank(ne.Value)
		switch {
		case oe != nil:
			if or := pyrightRank(oe.Value); or > nr && nr >= 0 {
				out = append(out, strictFinding(f, oe, ne, core.Warning, fmt.Sprintf("pyright %s lowered %s → %s", k, oe.Value, ne.Value)))
			}
		case nr == 0 && pyrightTypeErrorRe.MatchString(k):
			out = append(out, strictFinding(f, nil, ne, core.Warning, fmt.Sprintf("pyright %s set to %s: these type errors are no longer reported", k, ne.Value)))
		}
	}
	return out
}

// ---- ignore lists (ruff, flake8, pylint) ----

// codeList splits "E501, W503" / multi-line values into codes.
func codeList(v string) []string {
	var out []string
	for _, c := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// ignoredCodes returns tool → code → entry for global ignores, and
// "tool glob" → code → entry for per-file ignores.
func ignoredCodes(f *core.FileInfo, es []entry) map[string]map[string]*entry {
	out := map[string]map[string]*entry{}
	add := func(tool string, e *entry, codes []string) {
		if out[tool] == nil {
			out[tool] = map[string]*entry{}
		}
		for _, c := range codes {
			if _, ok := out[tool][c]; !ok {
				out[tool][c] = e
			}
		}
	}
	for i := range es {
		e := &es[i]
		p := e.Path
		switch {
		case f.Config == core.CfgRuff || f.Config == core.CfgPyproject && len(p) > 2 && p[0] == "tool" && p[1] == "ruff":
			rel := p
			if f.Config == core.CfgPyproject {
				rel = p[2:]
			}
			if len(rel) > 0 && rel[0] == "lint" {
				rel = rel[1:] // [tool.ruff.lint] and [tool.ruff] are the same settings
			}
			switch {
			case len(rel) == 1 && (rel[0] == "ignore" || rel[0] == "extend-ignore"):
				add("ruff", e, codeList(e.Value))
			case len(rel) == 2 && (rel[0] == "per-file-ignores" || rel[0] == "extend-per-file-ignores"):
				if !looksLikeTestPath(rel[1]) {
					add("ruff per-file "+rel[1], e, codeList(e.Value))
				}
			}
		case len(p) == 2 && p[0] == "flake8" && (normKey(p[1]) == "ignore" || normKey(p[1]) == "extend-ignore"):
			add("flake8", e, codeList(e.Value))
		case len(p) == 2 && p[0] == "flake8" && normKey(p[1]) == "per-file-ignores":
			// "path: E1, E2" lines
			if glob, codes, ok := strings.Cut(e.Value, ":"); ok && !looksLikeTestPath(glob) {
				add("flake8 per-file "+strings.TrimSpace(glob), e, codeList(codes))
			}
		case f.Config == core.CfgPylint && len(p) == 2 && strings.EqualFold(p[0], "messages control") && p[1] == "disable",
			f.Config == core.CfgPyproject && len(p) == 4 && p[0] == "tool" && p[1] == "pylint" &&
				(strings.EqualFold(p[2], "messages control") || p[2] == "messages_control") && p[3] == "disable":
			add("pylint", e, codeList(e.Value))
		}
	}
	return out
}

func ignoreListStrictness(f *core.FileInfo, old, new []entry, base baseline) []core.Finding {
	o, n := ignoredCodes(f, old), ignoredCodes(f, new)
	var out []core.Finding
	tools := map[string]bool{}
	for t := range n {
		tools[t] = true
	}
	for _, tool := range sortedKeys(tools) {
		// one finding per line, listing every new code on it
		byLine := map[int][]string{}
		first := map[int]*entry{}
		var order []int
		for _, code := range sortedEntryKeys(n[tool]) {
			if _, had := o[tool][code]; had || base.ignoredOld[tool][code] {
				continue
			}
			e := n[tool][code]
			if first[e.Line] == nil {
				first[e.Line] = e
				order = append(order, e.Line)
			}
			byLine[e.Line] = append(byLine[e.Line], code)
		}
		for _, line := range order {
			verb := "now ignores"
			if strings.HasPrefix(tool, "pylint") {
				verb = "now disables"
			}
			out = append(out, strictFinding(f, nil, first[line], core.Info, fmt.Sprintf("%s %s %s", tool, verb, strings.Join(byLine[line], ", "))))
		}
	}
	return out
}

// ---- golangci-lint, PHPStan, Psalm, RuboCop ----

func golangciStrictness(f *core.FileInfo, old, new []entry) []core.Finding {
	list := func(es []entry, key string) map[string]*entry {
		m := map[string]*entry{}
		for i := range es {
			if len(es[i].Path) == 2 && es[i].Path[0] == "linters" && es[i].Path[1] == key {
				m[es[i].Value] = &es[i]
			}
		}
		return m
	}
	od, nd := list(old, "disable"), list(new, "disable")
	oe, ne := list(old, "enable"), list(new, "enable")
	var out []core.Finding
	for _, l := range sortedEntryKeys(nd) {
		if od[l] == nil {
			out = append(out, strictFinding(f, nil, nd[l], core.Info, fmt.Sprintf("golangci-lint linter %s disabled", l)))
		}
	}
	for _, l := range sortedEntryKeys(oe) {
		if ne[l] == nil && nd[l] == nil {
			out = append(out, strictFinding(f, oe[l], nil, core.Info, fmt.Sprintf("golangci-lint linter %s no longer enabled", l)))
		}
	}
	return out
}

// phpstanLevel compares parameters.level (max is the highest).
func phpstanLevel(f *core.FileInfo, old, new []entry) []core.Finding {
	get := func(es []entry) *entry {
		for i := range es {
			if len(es[i].Path) == 2 && es[i].Path[0] == "parameters" && es[i].Path[1] == "level" {
				return &es[i]
			}
		}
		return nil
	}
	oe, ne := get(old), get(new)
	if oe == nil || ne == nil || levelNum(ne.Value) >= levelNum(oe.Value) {
		return nil
	}
	return []core.Finding{strictFinding(f, oe, ne, core.Warning, fmt.Sprintf("PHPStan level lowered %s → %s", oe.Value, ne.Value))}
}

func levelNum(v string) float64 {
	if strings.EqualFold(strings.Trim(v, `"'`), "max") {
		return 100
	}
	n, ok := parseNum(v)
	if !ok {
		return -1
	}
	return n
}

var psalmLevelRe = regexp.MustCompile(`\berrorLevel\s*=\s*"(\d+)"`)

// psalmLevel reports a raised Psalm errorLevel (1 is strictest).
func psalmLevel(f *core.FileInfo) []core.Finding {
	find := func(ls []core.Line) (int, int) {
		for _, l := range ls {
			if m := psalmLevelRe.FindStringSubmatch(l.NoComment); m != nil {
				n, _ := strconv.Atoi(m[1])
				return n, l.No
			}
		}
		return 0, 0
	}
	ov, _ := find(f.Removed)
	nv, line := find(f.Added)
	if ov == 0 || nv <= ov {
		return nil
	}
	return []core.Finding{sevFinding(strictnessLowered, f, line, core.Warning, fmt.Sprintf("Psalm errorLevel raised %d → %d (higher is more lenient)", ov, nv))}
}

func rubocopStrictness(f *core.FileInfo, old, new []entry) []core.Finding {
	o, n := lookup(old), lookup(new)
	var out []core.Finding
	for _, e := range new {
		if len(e.Path) != 2 || e.Path[1] != "Enabled" || !falsy(e.Value) || !strings.Contains(e.Path[0], "/") {
			continue
		}
		oe := o[pathKey(e.Path...)]
		if oe != nil && falsy(oe.Value) {
			continue
		}
		ne := n[pathKey(e.Path...)]
		out = append(out, strictFinding(f, oe, ne, core.Info, fmt.Sprintf("RuboCop cop %s disabled", e.Path[0])))
	}
	return out
}

// ---- bare patches ----

// fragmentMypyStrictRe matches a mypy `strict = true` line.
var fragmentMypyStrictRe = regexp.MustCompile(`(?i)^\s*strict\s*=\s*true\s*$`)

var fragmentStrictRe = regexp.MustCompile(`^\s*"?(strict|noImplicitAny|strictNullChecks)"?\s*:\s*(true|false)\b`)

// fragmentStrictness handles tsconfig "strict" flips when only a diff is
// available.
func fragmentStrictness(f *core.FileInfo) []core.Finding {
	if f.Config != core.CfgTSConfig {
		return nil
	}
	var out []core.Finding
	for _, p := range rulekit.Pairs(f) {
		for _, r := range p.Removed {
			rm := fragmentStrictRe.FindStringSubmatch(r.NoComment)
			if rm == nil || rm[2] != "true" {
				continue
			}
			for _, a := range p.Added {
				if am := fragmentStrictRe.FindStringSubmatch(a.NoComment); am != nil && am[1] == rm[1] && am[2] == "false" {
					sev := core.Warning
					if rm[1] == "strict" {
						sev = core.Error
					}
					out = append(out, sevFinding(strictnessLowered, f, a.No, sev, fmt.Sprintf("compilerOptions.%s true → false", rm[1])))
				}
			}
		}
	}
	return out
}

// ---- command lines ----

var maxWarningsRe = regexp.MustCompile(`--max-warnings[=\s]+(-?\d+)`)

// maxWarnings reads an eslint --max-warnings value; -1 means no limit.
func maxWarnings(v string) int {
	n := atoi(v)
	if n < 0 {
		return int(^uint(0) >> 1)
	}
	return n
}

// commandStrictness compares changed linter/type-checker invocations with
// their previous versions: eslint --max-warnings raised or dropped, clippy
// -D warnings dropped, mypy --strict dropped, phpstan --level lowered.
func commandStrictness(a *core.Analysis, base baseline) []core.Finding {
	var out []core.Finding
	denyElsewhere := warningsDeniedElsewhere(a)
	for _, f := range commandFiles(a) {
		added, removed := changedCommands(f)
		if len(added) == 0 || len(removed) == 0 {
			continue
		}
		var olds []invocation
		for _, c := range removed {
			olds = append(olds, invocations(c.Text)...)
		}
		addSet := lineSet(f.Added)
		for _, c := range added {
			for _, iv := range invocations(c.Text) {
				prog := iv.Prog()
				var prev *invocation
				for i := range olds {
					if olds[i].Prog() == prog {
						prev = &olds[i]
						break
					}
				}
				if prev == nil {
					continue
				}
				line := firstChanged(c, addSet)
				oldText, newText := joinWords(prev.Args), joinWords(iv.Args)
				switch prog {
				case "eslint":
					om, nm := maxWarningsRe.FindStringSubmatch(oldText), maxWarningsRe.FindStringSubmatch(newText)
					switch {
					case om != nil && nm == nil:
						out = append(out, sevFinding(strictnessLowered, f, line, core.Warning,
							fmt.Sprintf("eslint --max-warnings %s dropped: warnings no longer fail the lint step", om[1])))
					case om != nil && nm != nil && maxWarnings(nm[1]) > maxWarnings(om[1]):
						out = append(out, sevFinding(strictnessLowered, f, line, core.Warning,
							fmt.Sprintf("eslint --max-warnings raised %s → %s", om[1], nm[1])))
					}
				case "cargo":
					if denyWarnings(prev.Args) && !denyWarnings(iv.Args) && firstPositional(iv.Args[1:]) == "clippy" &&
						!denyElsewhere && !rustflagsDeny(newSide(f)) && !denyWarningsEnv(iv.Env) {
						out = append(out, sevFinding(strictnessLowered, f, line, core.Warning,
							"`cargo clippy -- -D warnings` lost `-D warnings`: clippy findings no longer fail the build"))
					}
				case "mypy", "pyright":
					if hasArg(prev.Args, "--strict") && !hasArg(iv.Args, "--strict") && !(prog == "mypy" && base.mypyNewOn["strict"]) {
						out = append(out, sevFinding(strictnessLowered, f, line, core.Warning,
							fmt.Sprintf("%s --strict dropped from %s", prog, quote(iv.String()))))
					}
				case "phpstan":
					ol, ok1 := flagValue(prev.Args, "--level")
					nl, ok2 := flagValue(iv.Args, "--level")
					if !ok1 {
						ol, ok1 = flagValue(prev.Args, "-l")
					}
					if !ok2 {
						nl, ok2 = flagValue(iv.Args, "-l")
					}
					if ok1 && ok2 && levelNum(nl) < levelNum(ol) {
						out = append(out, sevFinding(strictnessLowered, f, line, core.Warning,
							fmt.Sprintf("PHPStan --level lowered %s → %s", ol, nl)))
					}
				}
			}
		}
	}
	return out
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// rustDenyRe matches settings that turn Rust warnings into errors outside
// the clippy command line: RUSTFLAGS="-Dwarnings", rustflags = ["-D",
// "warnings"] in .cargo/config.toml, or a [lints] table with warnings denied.
var rustDenyRe = regexp.MustCompile(`(?i)(rustflags\b.*-D\s*["']?\s*,?\s*["']?warnings|\bwarnings\s*=\s*["']?(deny|forbid)|\ball\s*=\s*\{?\s*(level\s*=\s*)?["'](deny|forbid))`)

// rustflagsDeny reports whether a side (a workflow, a script) sets
// RUSTFLAGS with -D warnings.
func rustflagsDeny(s side) bool {
	for i := 1; i <= len(s.lines); i++ {
		if l, ok := s.get(i); ok && rustDenyRe.MatchString(l.NoComment) {
			return true
		}
	}
	return false
}

// denyWarningsEnv reports RUSTFLAGS=-Dwarnings on the command itself.
func denyWarningsEnv(env map[string]string) bool {
	v, ok := env["RUSTFLAGS"]
	return ok && rustDenyRe.MatchString("RUSTFLAGS="+v)
}

// warningsDeniedElsewhere reports whether the change adds a setting that
// denies Rust warnings for the whole build (Cargo.toml [lints], config.toml
// rustflags, a RUSTFLAGS variable).
func warningsDeniedElsewhere(a *core.Analysis) bool {
	for _, f := range a.Files {
		for _, l := range f.Added {
			if rustDenyRe.MatchString(l.NoComment) {
				return true
			}
		}
	}
	return false
}

// denyWarnings reports whether args deny warnings (-D warnings, -Dwarnings,
// --deny warnings).
func denyWarnings(args []string) bool {
	for i, a := range args {
		if a == "-Dwarnings" || (a == "-D" || a == "--deny") && i+1 < len(args) && args[i+1] == "warnings" {
			return true
		}
	}
	return false
}
