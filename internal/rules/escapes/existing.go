package escapes

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

// This file decides whether an escape hatch weakens verification that
// already existed. narc's severities follow one principle: an escape that
// silences a check on code that was already there (a cast or suppression
// added to an existing line, a file-wide disable added to an existing file)
// needs a human look; the same escape written as part of new code is
// ordinary hygiene, reported as a note.

// ---- casts on existing code ----

// tsAsRe matches the `as` keyword of a TypeScript type assertion.
var tsAsRe = regexp.MustCompile(`\s+as\s+`)

// pyCastRe matches a Python typing.cast call.
var pyCastRe = regexp.MustCompile(`\b(?:typing\.|t\.)?cast\(`)

// stripCasts reduces a line of code to what it computes: type assertions
// (`x as T`, `x as unknown as T`, `<any>x`, Python's `cast(T, x)`) are
// dropped, and so is what wrapping a value in a cast adds or moves
// (whitespace, parentheses, `?.` for `.`, a trailing `;` or `,`). A line
// that only gained a cast compares equal to the line it replaced.
func stripCasts(lg lang.Lang, code string) string {
	if lg == lang.Python {
		code = stripPyCasts(code)
	} else {
		code = stripTSCasts(code)
	}
	var b strings.Builder
	for i := 0; i < len(code); i++ {
		switch c := code[i]; c {
		case ' ', '\t', '\r', '(', ')':
			continue
		case '?':
			if i+1 < len(code) && code[i+1] == '.' {
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return strings.TrimRight(b.String(), ";,")
}

// stripTSCasts removes TypeScript type assertions from code.
func stripTSCasts(s string) string {
	s = strings.ReplaceAll(s, "<any>", "")
	var b strings.Builder
	for k := 0; k < 64; k++ {
		loc := tsAsRe.FindStringIndex(s)
		if loc == nil {
			break
		}
		b.WriteString(s[:loc[0]])
		end := skipType(s, loc[1])
		if end == loc[1] { // `as` not followed by a type: keep it
			b.WriteString(s[loc[0]:loc[1]])
		}
		s = s[end:]
	}
	b.WriteString(s)
	return b.String()
}

// stripPyCasts replaces `cast(T, value)` with `value`.
func stripPyCasts(s string) string {
	for k := 0; k < 16; k++ {
		m := pyCastRe.FindStringIndex(s)
		if m == nil {
			break
		}
		args, end, ok := rulekit.CallArgs(s, m[1]-1)
		if !ok || len(args) != 2 {
			break
		}
		s = s[:m[0]] + args[1] + s[end+1:]
	}
	return s
}

// skipType returns the offset just past the TypeScript type that starts at
// s[i] (`Foo`, `Array<Foo>[]`, `Parameters<typeof f>[0]["body"]`,
// `string[] | undefined`), or i when no type starts there.
func skipType(s string, i int) int {
	end := i
	for {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		w := i
		for i < len(s) && (isIdentByte(s[i]) || s[i] == '.') {
			i++
		}
		switch word := s[w:i]; {
		case i == w:
			if i >= len(s) || (s[i] != '{' && s[i] != '(' && s[i] != '[') {
				return end
			}
			i = skipBalanced(s, i)
		case word == "typeof" || word == "keyof" || word == "readonly" || word == "unique":
			continue
		}
		for i < len(s) && (s[i] == '<' || s[i] == '[') {
			i = skipBalanced(s, i)
		}
		end = i
		j := i
		for j < len(s) && s[j] == ' ' {
			j++
		}
		if j+1 < len(s) && (s[j] == '|' || s[j] == '&') && s[j+1] != s[j] && s[j+1] != '=' {
			i = j + 1
			continue
		}
		return end
	}
}

// skipBalanced returns the offset just past the bracket group that opens at
// s[i], or len(s) when it does not close on the line.
func skipBalanced(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '<', '(', '[', '{':
			depth++
		case '>':
			if i > 0 && s[i-1] == '=' {
				continue // the arrow of a function type
			}
			depth--
		case ')', ']', '}':
			depth--
		}
		if depth == 0 {
			return i + 1
		}
	}
	return len(s)
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// castOnExisting reports whether the cast h on added line l of f was added
// to existing code: a removed line of the same hunk computes the same thing
// once casts are set aside (`apiCreateSchedule({ body: request })` became
// `apiCreateSchedule({ body: request as unknown as Body })`), or the line is
// a one-line edit of an existing statement that now casts a value the old
// statement used (`return Invoice.from_row(row)` became
// `return cast(Any, row)`). Either way, code that used to type-check is
// forced past an error.
func castOnExisting(f *core.FileInfo, pairs []rulekit.Pair, l core.Line, h castHit) bool {
	k := stripCasts(f.Lang, l.Code)
	if len(k) < 4 {
		return false
	}
	for _, r := range f.Removed {
		if r.Hunk == l.Hunk && stripCasts(f.Lang, r.Code) == k {
			return true
		}
	}
	op := h.operand
	if op == "" {
		op = h.arg
	}
	if op == "" {
		return false
	}
	for _, p := range pairs {
		if len(p.Removed) != 1 || len(p.Added) != 1 || p.Added[0].No != l.No {
			continue
		}
		old := p.Removed[0].Code
		head := castStmtHead(l.Code)
		return head != "" && castStmtHead(old) == head && strings.Contains(old, op)
	}
	return false
}

// castStmtHeadRe matches the head of a statement: a return, or the target of
// an assignment or an object property.
var castStmtHeadRe = regexp.MustCompile(`^\s*(?:(return|yield)\b|(?:(?:export\s+)?(?:const|let|var)\s+)?([\w$.\[\]]+)\s*(?::\s*[^=]+)?\s*=[^=>]|([\w$]+)\s*:)`)

// castStmtHead returns what a statement assigns to or returns, or "".
func castStmtHead(code string) string {
	m := castStmtHeadRe.FindStringSubmatch(code)
	if m == nil {
		return ""
	}
	return m[1] + m[2] + m[3]
}

// ---- directives on existing code ----

// directiveOnExisting reports whether a suppression directive covers code
// that was already there: a line- or declaration-scoped directive whose
// target line predates the change, or a file-wide one added to a file
// that already existed.
func directiveOnExisting(f *core.FileInfo, s *side, ch map[int]bool, df dirFinding) bool {
	if df.d.scope == scopeFile {
		return f.Diff.Kind != diff.Added && hasOldCode(f)
	}
	if coversExisting(f, s, ch, df) {
		return true
	}
	return df.d.scope == scopeItem && f.Diff.Kind != diff.Added && itemOnExisting(f, s, df)
}

// declNameRes find the name a declaration line declares: a keyword-led
// declaration (`fn load`, `def sync`, `class Cache`), else a method or
// function signature (`public <T> T get(String key)`).
var (
	declKeywordRe = regexp.MustCompile(`\b(?:fn|def|func|class|struct|enum|interface|trait|object|record|module|function)\s+([A-Za-z_]\w*)`)
	declCallRe    = regexp.MustCompile(`([A-Za-z_]\w*)\s*(?:<[^<>]*>)?\s*\(`)
	notDeclName   = toSet("if for while switch catch return new throw await typeof sizeof assert")
)

// declName returns the name declared on a line of code, or "".
func declName(code string) string {
	if m := declKeywordRe.FindStringSubmatch(code); m != nil {
		return m[1]
	}
	if m := declCallRe.FindStringSubmatch(code); m != nil && !notDeclName[m[1]] {
		return m[1]
	}
	return ""
}

// itemOnExisting reports whether a declaration-scoped directive (an
// attribute or annotation) covers a declaration that the old version of f
// already had: `#[allow(clippy::unwrap_used)]` on an existing `fn load`
// whose body the change rewrote still silences the lint on existing code.
func itemOnExisting(f *core.FileInfo, s *side, df dirFinding) bool {
	n := df.line.No
	name := ""
	for k := 0; k < 4 && name == ""; k++ {
		c, _ := s.code(n)
		rest := c
		if k == 0 {
			// The declaration may follow the attribute on its line.
			if m := df.d.spec.re.FindStringIndex(c); m != nil {
				rest = c[m[1]:]
			}
		}
		if t := strings.TrimSpace(rest); t != "" && !strings.HasPrefix(t, "#[") && !strings.HasPrefix(t, "@") && !strings.HasPrefix(t, "[") {
			name = declName(t)
			break
		}
		if n = s.nextCode(n); n == 0 {
			return false
		}
	}
	if name == "" {
		return false
	}
	olds := oldSide(f)
	decl := declarer(name)
	for _, m := range olds.lineNos() {
		if c, ok := olds.code(m); ok && strings.Contains(c, name) && decl(c) {
			return true
		}
	}
	return false
}

// stmtKeywordRe matches lines that start a statement rather than a
// declaration.
var stmtKeywordRe = regexp.MustCompile(`^\s*(?:return|throw|await|yield|new|else|case|if|for|while)\b`)

// declarer returns a test for whether a line of code declares name: a
// keyword-led declaration (`fn load`), a typed signature
// (`public byte[] export(`), or a method head that opens its body
// (`async load(id) {`). A call such as `return store.get(key)` or
// `const x = await get(id)` declares nothing.
func declarer(name string) func(code string) bool {
	q := regexp.QuoteMeta(name)
	typed := regexp.MustCompile(`(?:^|[\s(])(?:[\w$<>\[\],.?]+\s+)+` + q + `\s*(?:<[^<>]*>)?\s*\(`)
	head := regexp.MustCompile(`^\s*(?:(?:async|static|get|set|public|private|protected|override)\s+)*` + q + `\s*\([^;]*\)\s*(?::[^{;]+)?\{\s*$`)
	return func(code string) bool {
		if m := declKeywordRe.FindStringSubmatch(code); m != nil {
			return m[1] == name
		}
		if stmtKeywordRe.MatchString(code) {
			return false
		}
		if head.MatchString(code) {
			return true
		}
		loc := typed.FindStringIndex(code)
		if loc == nil {
			return false
		}
		at := strings.LastIndex(code[:loc[1]], name)
		return !opWordBeforeRe.MatchString(code[:at]) && !strings.Contains(code[:at], "=")
	}
}

// opWordBeforeRe matches an operator keyword right before a name.
var opWordBeforeRe = regexp.MustCompile(`\b(?:await|return|new|yield|throw|typeof|void|delete|in|of|else|case|not|and|or|go|defer)\s+$`)

// hasOldCode reports whether the old version of f has any code.
func hasOldCode(f *core.FileInfo) bool {
	olds := oldSide(f)
	for _, n := range olds.lineNos() {
		if c, ok := olds.code(n); ok && strings.TrimSpace(c) != "" {
			return true
		}
	}
	return false
}

// ---- dependency upgrades ----

// depFileRe matches dependency manifests and lock-step requirement lists.
var depFileRe = regexp.MustCompile(`(?i)(^|/)(requirements[\w.-]*\.(?:txt|in)|constraints[\w.-]*\.txt|manifest\.json|` +
	`package\.json|pyproject\.toml|setup\.cfg|setup\.py|Pipfile)$`)

// depSpecRes extract a dependency name from a manifest line that pins a
// version: `motioneye-client==0.4.1`, `"@lobehub/ui": "^2.1.0"`,
// `httpx = "^0.27"`, `"aiohttp>=3.9"`.
var depSpecRes = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|["'\s,\[])([A-Za-z0-9][A-Za-z0-9_.\-]*)(?:\[[^\]]*\])?\s*(?:===?|>=|~=|<=|!=|>|<)\s*v?\d`),
	regexp.MustCompile(`"(@?[A-Za-z0-9][\w.\-]*(?:/[\w.\-]+)?)"\s*:\s*"[~^>=<v\s]*\d`),
	regexp.MustCompile(`^\s*([A-Za-z0-9][\w.\-]*)\s*=\s*(?:"[~^>=<v\s]*\d|\{[^}]*\bversion\s*=\s*"[~^>=<v\s]*\d)`),
}

// depName canonicalizes a package or module name for comparing a manifest
// entry with an import: `motioneye-client` and `motioneye_client` are one.
func depName(s string) string {
	s = strings.ToLower(s)
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' {
			return -1
		}
		return r
	}, s)
}

// depsOf returns the dependencies a manifest line pins, with the line.
func depsOf(raw string) []string {
	var out []string
	for _, re := range depSpecRes {
		for _, m := range re.FindAllStringSubmatch(raw, 8) {
			if n := depName(m[1]); n != "" && n != "version" && n != "python" && n != "node" {
				out = append(out, n)
			}
		}
	}
	return out
}

// bumpedDepsKey memoizes bumpedDeps.
type bumpedDepsKey struct{}

// bumpedDeps returns the dependencies whose pinned version the change
// edits, by canonical name.
func bumpedDeps(a *core.Analysis) map[string]bool {
	return a.Memo(bumpedDepsKey{}, func() any {
		out := map[string]bool{}
		for _, f := range a.Files {
			if !depFileRe.MatchString(f.Path) || len(f.Removed) == 0 || len(f.Added) == 0 {
				continue
			}
			before := map[string]string{}
			for _, l := range f.Removed {
				for _, n := range depsOf(l.Raw) {
					before[n] = strings.TrimSpace(l.Raw)
				}
			}
			for _, l := range f.Added {
				for _, n := range depsOf(l.Raw) {
					if old, ok := before[n]; ok && old != strings.TrimSpace(l.Raw) {
						out[n] = true
					}
				}
			}
		}
		return out
	}).(map[string]bool)
}

var (
	pyImportFromRe = regexp.MustCompile(`^\s*(?:from\s+([A-Za-z_][\w]*)[\w.]*\s+import\b|import\s+([A-Za-z_][\w]*))`)
	jsImportFromRe = regexp.MustCompile(`\bfrom\s+["']([^"']+)["']|\brequire\(\s*["']([^"']+)["']|\bimport\(\s*["']([^"']+)["']`)
)

// importedBump returns a dependency that f imports and the change upgrades,
// or "".
func importedBump(a *core.Analysis, f *core.FileInfo) string {
	bumped := bumpedDeps(a)
	if len(bumped) == 0 {
		return ""
	}
	for _, l := range f.NewLines() {
		var names []string
		switch {
		case f.Lang == lang.Python:
			if m := pyImportFromRe.FindStringSubmatch(l.Code); m != nil {
				names = append(names, m[1]+m[2])
			}
		case f.Lang.IsJSLike():
			for _, m := range jsImportFromRe.FindAllStringSubmatch(l.NoComment, 4) {
				spec := m[1] + m[2] + m[3]
				if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
					continue
				}
				parts := strings.SplitN(spec, "/", 3)
				name := parts[0]
				if strings.HasPrefix(name, "@") && len(parts) > 1 {
					name += "/" + parts[1]
				}
				names = append(names, name)
			}
		}
		for _, n := range names {
			if bumped[depName(n)] {
				return n
			}
		}
	}
	return ""
}
