package ci

import (
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/tranhuuhuy297/narc/internal/core"
)

// Projects that run two JavaScript linters (Biome and ESLint, ESLint and
// Oxlint) move rules between them: the rule is turned on in one config
// and off in the other, often in separate changes, with a comment such as
// `"noConsole": "off", // oxlint owns no-console`. Turning a rule off is
// only a relaxation when no other linter of the repository enforces it.

// linterConfigRe matches the configuration files of JavaScript linters.
var linterConfigRe = regexp.MustCompile(`^(\.oxlintrc(\.[a-z]+)*\.jsonc?|oxlintrc\.jsonc?|oxlint\.config\.[cm]?[jt]s|\.eslintrc(\.[a-z]+)?|eslint\.config\.[cm]?[jt]s|biome\.jsonc?)$`)

// handoffClaimRe matches a comment that says another tool now enforces the
// rule.
var handoffClaimRe = regexp.MustCompile(`(?i)\b(oxlint|eslint|biome|typescript|tsc|prettier|stylelint|dprint)\b[^.;]*\b(owns?|handles?|covers?|enforces?|checks?|replaces?)\b|\b(owned|handled|covered|enforced|replaced|superseded)\s+by\b|\bin\s+favou?r\s+of\b|\bmoved\s+to\s+(oxlint|eslint|biome)\b`)

// ruleNameRe finds kebab-case rule names in a comment ("no-console",
// "react/jsx-curly-brace-presence").
var ruleNameRe = regexp.MustCompile(`[@a-z0-9_/]*[a-z0-9]+(?:-[a-z0-9]+)+`)

// biomeToESLint maps Biome rules whose ESLint/Oxlint names are not the
// kebab-case spelling of the Biome name.
var biomeToESLint = map[string][]string{
	"useConsistentCurlyBraces":  {"jsx-curly-brace-presence"},
	"noUnusedVariables":         {"no-unused-vars"},
	"noUnusedImports":           {"no-unused-vars", "unused-imports"},
	"useExhaustiveDependencies": {"exhaustive-deps"},
	"useHookAtTopLevel":         {"rules-of-hooks"},
	"noExplicitAny":             {"no-explicit-any"},
	"noNonNullAssertion":        {"no-non-null-assertion"},
	"useConst":                  {"prefer-const"},
	"noDoubleEquals":            {"eqeqeq"},
	"useTemplate":               {"prefer-template"},
	"noVar":                     {"no-var"},
	"useOptionalChain":          {"prefer-optional-chain"},
	"noBannedTypes":             {"no-restricted-types", "ban-types"},
	"noEmptyInterface":          {"no-empty-interface", "no-empty-object-type"},
}

// kebab renders a camelCase rule name in kebab case: noConsole → no-console.
func kebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ruleBase drops a plugin prefix: "@typescript-eslint/no-explicit-any" →
// "no-explicit-any", "style/noInferrableTypes" → "noInferrableTypes".
func ruleBase(r string) string {
	if i := strings.LastIndexByte(r, '/'); i >= 0 {
		return r[i+1:]
	}
	return r
}

type linterConfigsKey struct{}

// linterConfigs lists the JavaScript linter configs of the repository at
// the new revision (memoized; nil when it cannot be listed).
func linterConfigs(a *core.Analysis) []string {
	return a.Memo(linterConfigsKey{}, func() any {
		paths, ok := a.ListNew("")
		var out []string
		if ok {
			for _, p := range paths {
				if strings.Contains(p, "node_modules/") || isFixture(p) {
					continue
				}
				if linterConfigRe.MatchString(path.Base(p)) {
					out = append(out, p)
				}
			}
		} else {
			// a bare patch: only the changed files are known
			for _, f := range a.Files {
				if linterConfigRe.MatchString(path.Base(f.Path)) {
					out = append(out, f.Path)
				}
			}
		}
		return out
	}).([]string)
}

// relatedDirs reports whether one directory contains the other, so a config
// at the root and one in site/ lint (some of) the same files.
func relatedDirs(a, b string) bool {
	if a == "." {
		a = ""
	}
	if b == "." {
		b = ""
	}
	return a == "" || b == "" || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// enforcedRules returns the rules (by base name) that a linter config turns
// on (warn or error), whatever its tool.
func enforcedRules(p string, es []entry) map[string]bool {
	on := map[string]bool{}
	base := path.Base(p)
	if strings.HasPrefix(base, "biome.") {
		for _, e := range es {
			if len(e.Path) >= 4 && e.Path[0] == "linter" && e.Path[1] == "rules" && (len(e.Path) == 4 || e.Path[4] == "level") {
				if eslintRank(e.Value) >= 1 {
					on[e.Path[3]] = true
				}
			}
		}
		return on
	}
	rules, _ := eslintRules(es, false)
	for k, e := range rules {
		if eslintRank(e.Value) >= 1 {
			on[ruleBase(k[strings.IndexByte(k, 0)+1:])] = true
		}
	}
	return on
}

// handoffNames returns the names under which other linters may know rule:
// its own base name, its kebab-case or camelCase twin, known Biome
// equivalents, and rule names that the comment on its line mentions.
func handoffNames(rule, comment string, biome bool) []string {
	b := ruleBase(rule)
	names := []string{b}
	if biome {
		names = append(names, kebab(b))
		names = append(names, biomeToESLint[b]...)
	} else {
		// ESLint → Biome: no-console → noConsole
		parts := strings.Split(b, "-")
		for i := 1; i < len(parts); i++ {
			if parts[i] != "" {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
		}
		names = append(names, strings.Join(parts, ""))
		for bn, es := range biomeToESLint {
			for _, e := range es {
				if e == b {
					names = append(names, bn)
				}
			}
		}
	}
	// Names from the comment count only when they share a word with the
	// rule itself (useConsistentCurlyBraces ↔ jsx-curly-brace-presence), so
	// pointing at some unrelated rule that is on does not verify anything.
	own := ruleWords(kebab(b))
	for _, m := range ruleNameRe.FindAllString(strings.ToLower(comment), -1) {
		m = ruleBase(m)
		for w := range ruleWords(m) {
			if own[w] {
				names = append(names, m)
				break
			}
		}
	}
	return names
}

// ruleStopWords are words that most lint rule names share.
var ruleStopWords = map[string]bool{"no": true, "use": true, "prefer": true, "require": true, "enforce": true,
	"disallow": true, "consistent": true, "valid": true, "jsx": true, "react": true}

// ruleWords returns the distinctive words of a kebab-case rule name, cut to
// four letters so "brace" and "braces" meet.
func ruleWords(name string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Split(strings.ToLower(name), "-") {
		if len(w) < 3 || ruleStopWords[w] {
			continue
		}
		if len(w) > 4 {
			w = w[:4]
		}
		out[w] = true
	}
	return out
}

// handedOff reports whether a lint rule that f turns off is enforced by
// another linter config of the repository (verified), or merely claimed to
// be by the comment on its line (claimed).
func handedOff(a *core.Analysis, f *core.FileInfo, rule string, line int, biome bool) (verified, claimed bool) {
	comment := ""
	if l, ok := f.NewLine(line); ok {
		comment = l.Comment
	}
	claimed = handoffClaimRe.MatchString(comment)
	names := handoffNames(rule, comment, biome)
	r := repoOf(a)
	dir := path.Dir(f.Path)
	for _, p := range linterConfigs(a) {
		if p == f.Path || !relatedDirs(dir, path.Dir(p)) {
			continue
		}
		d, ok := r.get(p, false)
		if !ok {
			continue
		}
		format := formatOf(d.f)
		if format == fmtNone {
			format = fmtJSON // .eslintrc, .oxlintrc without an extension
		}
		on := enforcedRules(p, parseConfig(format, d.s.text(), d.s.lines))
		for _, n := range names {
			if on[n] {
				return true, claimed
			}
		}
	}
	return false, claimed
}

// tsconfigPathRe finds tsconfig paths named in comments ("Same relaxations
// as packages/cli/tsconfig.json").
var tsconfigPathRe = regexp.MustCompile(`[\w@.\-/]*tsconfig[\w.\-]*\.json`)

// tsMirror returns a function reporting whether a strict-family override
// in the new tsconfig f mirrors an existing config: the tsconfig that
// governed code the change moves into f's package, a tsconfig its comments
// name, or the tsconfig of a sibling package. It returns nil for a bare
// patch, where none of them can be read.
func tsMirror(a *core.Analysis, f *core.FileInfo) func(opt string) bool {
	if a.Source == nil {
		return nil // a bare patch: the repository cannot be read
	}
	var refs []doc
	done := false
	r := repoOf(a)
	collect := func() {
		done = true
		dir := path.Dir(f.Path)
		if dir == "." {
			dir = ""
		}
		seen := map[string]bool{f.Path: true}
		add := func(p string, old bool) {
			if p == "" || seen[p] {
				return
			}
			seen[p] = true
			if d, ok := r.get(p, old); ok {
				refs = append(refs, d)
			}
		}
		// (a) code moved into the new package
		for _, g := range a.Files {
			if g.OldPath == "" || g.OldPath == g.Path || !strings.HasPrefix(g.Path, dir+"/") || strings.HasPrefix(g.OldPath, dir+"/") {
				continue
			}
			for d := path.Dir(g.OldPath); ; d = path.Dir(d) {
				p := path.Join(d, path.Base(f.Path))
				if d == "." {
					p = path.Base(f.Path)
				}
				if dd, ok := r.get(p, true); ok {
					if !seen[p] {
						seen[p] = true
						refs = append(refs, dd)
					}
					break
				}
				if d == "." || d == "/" || d == "" {
					break
				}
			}
			if len(refs) >= 3 {
				break
			}
		}
		// (b) configs its comments name
		if s := newSide(f); s.full {
			for i := 1; i <= len(s.lines); i++ {
				l, _ := s.get(i)
				for _, m := range tsconfigPathRe.FindAllString(l.Comment, -1) {
					add(path.Clean(m), false)
					add(joinDir(dir, m), false)
				}
			}
		}
		// (c) sibling packages: <parent>/*/tsconfig.json, and the same
		// leaf in cousin packages (packages/modules/*/backend/tsconfig.json)
		if dir == "" {
			return
		}
		parent := path.Dir(dir)
		if parent == "." {
			parent = ""
		}
		grand, pbase, leaf := path.Dir(parent), path.Base(parent), path.Base(dir)
		if parent == "" {
			grand, pbase = "", ""
		} else if grand == "." {
			grand = ""
		}
		paths, ok := a.ListNew(grand)
		if !ok {
			return
		}
		n := 0
		for _, p := range paths {
			rel := p
			if grand != "" {
				rel = strings.TrimPrefix(p, grand+"/")
			}
			segs := strings.Split(rel, "/")
			if segs[len(segs)-1] != path.Base(f.Path) {
				continue
			}
			sibling := parent == "" && len(segs) == 2 || parent != "" && len(segs) == 3 && segs[0] == pbase
			cousin := parent != "" && len(segs) == 3 && segs[1] == leaf
			if sibling || cousin {
				add(p, false)
				if n++; n >= 25 {
					break
				}
			}
		}
	}
	return func(opt string) bool {
		if !done {
			collect()
		}
		for _, d := range refs {
			o := lookup(parseConfig(fmtJSON, d.s.text(), d.s.lines))
			e, s := o[pathKey("compilerOptions", opt)], o[pathKey("compilerOptions", "strict")]
			if e != nil && falsy(e.Value) && s != nil && truthy(s.Value) {
				return true
			}
		}
		return false
	}
}
