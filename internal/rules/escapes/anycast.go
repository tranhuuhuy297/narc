package escapes

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/lang"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var anyCast = &core.Rule{
	ID:       "any-cast",
	Title:    "Cast to any",
	Category: "escapes",
	Severity: core.Warning,
	Summary:  "A value is now cast to `any` (or force-cast through `unknown`), switching off type checking for it.",
	Hint: "Give the value its real type: fix the type definitions, narrow it with a type guard, or validate it at the boundary " +
		"(zod, io-ts, pydantic). An `any` cast makes the type checker accept anything, including the bug it was reporting.",
	Docs: "`as any`, `<any>x`, `x as unknown as T` and Python's `cast(Any, x)` tell the type checker to trust the " +
		"programmer. They are a common way to make a type error disappear without fixing it.\n\n" +
		"```ts\n// bad\nconst user = (req as any).user\n\n// good\ninterface AuthedRequest extends Request { user: User }\nconst user = (req as AuthedRequest).user\n```\n\n" +
		"A warning means existing code was forced past a type error: the cast was added to a line that was already " +
		"there (`apiCreateSchedule({ body: request })` became `{ body: request as unknown as ApiBody }`, " +
		"`return Invoice.from_row(row)` became `return cast(Any, row)`), or code reaches into a dependency the change " +
		"upgrades through a cast (`cast(Any, client).get_snapshot()` next to the library bump), and the cast forces a " +
		"value of one named type into another, reads what the type says is not there (`(req as any).user`), or is " +
		"Python's `cast(Any, x)`. Casts written as part of new code are notes. Also notes: casts in " +
		"test code and test infrastructure (mocks, fixtures, stories), `as any` on a value that is only passed along " +
		"(`t(key as any)`), casts at a data boundary (a value narrowed by `isRecord()`/`typeof`, a parameter typed " +
		"`unknown`, a caught error, a parser's or validator's result, an object literal), and casts the file already " +
		"uses. Not reported: widening casts (`as unknown as Record<string, unknown>`), casts of `window`/`globalThis`, " +
		"a typeof-guarded cast to the checked primitive, casts of values that are already `any`, database handles " +
		"passed between wrapper types (`tx as unknown as AppDatabase`), and casts the codebase uses in many files.",
}

func init() {
	anyCast.Check = checkAnyCast
	core.Register(anyCast)
}

// castKind tells the escape casts apart.
type castKind uint8

const (
	castAny    castKind = iota // `x as any`, `<any>x`
	castDouble                 // `x as unknown as T`
	castPyAny                  // `cast(Any, x)`
)

// castPattern is one kind of escape cast.
type castPattern struct {
	kind  castKind
	langs []lang.Lang
	re    *regexp.Regexp
	// describe renders the message for a match (m holds submatch strings).
	describe func(operand string, m []string) string
}

var (
	operandRe = regexp.MustCompile(`([\w$]+(?:\.[\w$]+)*(?:\(\))?)\s*$`)
	importRe  = regexp.MustCompile(`^\s*(?:import|export)\b.*\bfrom\b`)
	// anyAngleCtxRe is what may precede a `<any>expr` cast; anything else
	// (an identifier) makes `<any>` a generic argument: useState<any>().
	anyAngleCtxRe = regexp.MustCompile(`(?:^|[=(,:?!&|{}\[;]|=>|\breturn|\bawait|\byield|\bcase|\bthrow)\s*$`)
)

var castPatterns = []castPattern{
	{kind: castAny, langs: []lang.Lang{lang.TypeScript}, re: regexp.MustCompile(`\bas\s+any\b`),
		describe: func(op string, _ []string) string {
			return fmt.Sprintf("`as any` cast on %s: the compiler no longer checks how it is used", op)
		}},
	// `x as unknown as T`, also written `(x as unknown) as T`.
	{kind: castDouble, langs: []lang.Lang{lang.TypeScript}, re: regexp.MustCompile(`\bas\s+unknown(?:\s*\)\s*|\s+)as\s+([\w$.]+(?:<[^>]*>)?(?:\[\])*)`),
		describe: func(op string, m []string) string {
			return fmt.Sprintf("double cast `as unknown as %s` forces %s into a type the compiler says it does not have", m[1], op)
		}},
	{kind: castAny, langs: []lang.Lang{lang.TypeScript}, re: regexp.MustCompile(`<any>\s*[\w$(\[{]`),
		describe: func(string, []string) string {
			return "`<any>` cast: the compiler no longer checks how the value is used"
		}},
	{kind: castPyAny, langs: python, re: regexp.MustCompile(`\b(?:typing\.|t\.)?cast\(\s*(?:(?:typing|t)\.)?Any\s*,`), describe: describeCastAny},
	{kind: castPyAny, langs: python, re: regexp.MustCompile(`\b(?:typing\.|t\.)?cast\(\s*["'](?:typing\.)?Any["']\s*,`), describe: describeCastAnyStr},
}

func describeCastAny(op string, _ []string) string {
	return fmt.Sprintf("`cast(Any, …)` added: the type checker stops checking %s", op)
}

func describeCastAnyStr(op string, _ []string) string {
	return fmt.Sprintf("`cast(\"Any\", …)` added: the type checker stops checking %s", op)
}

// castHit is one escape cast on a line.
type castHit struct {
	key string
	msg string

	kind    castKind
	operand string // the cast expression's operand when it is a name (`req`, `this.db`), else ""
	target  string // the type of a double cast
	// before is the code before the cast on its line, for operands that
	// end there (a call's closing parenthesis, an object literal's brace).
	before string
	// deref: the result is used right away (`(req as any).user`), reading
	// what the type says is not there.
	deref bool
	text  string // the cast as written, for looking up the same idiom elsewhere
	arg   string // the value cast by Python's cast(T, value)
}

func findCasts(f *core.FileInfo, l core.Line, old bool) []castHit {
	if strings.TrimSpace(l.Code) == "" || importRe.MatchString(l.Code) {
		return nil
	}
	var v *view
	var out []castHit
	for _, p := range castPatterns {
		if !langIn(f.Lang, p.langs...) {
			continue
		}
		if v == nil {
			v = newView(f.Lang, l.LexedLine)
		}
		for _, m := range v.matches(p.re) {
			before := tail(v.canon[:m[0]], 200)
			if strings.HasPrefix(v.canon[m[0]:], "<any>") {
				// <any>expr is not valid TSX, and after an identifier it is a
				// generic argument rather than a cast.
				if strings.HasSuffix(f.Path, ".tsx") || !anyAngleCtxRe.MatchString(before) {
					continue
				}
			}
			op := "the value"
			hit := castHit{kind: p.kind, before: before}
			if f.Lang == lang.Python {
				// cast(Any, value): the value is the second argument.
				open := m[0] + strings.IndexByte(v.canon[m[0]:], '(')
				if args, end, ok := rulekit.CallArgs(v.canon, open); ok && len(args) == 2 {
					op = "`" + shorten(args[1], 40) + "`"
					hit.arg = args[1]
					// cast(Any, client).fetch(): calls what the type does not declare.
					hit.deref = strings.HasPrefix(strings.TrimSpace(v.canon[end+1:]), ".")
				}
			} else if om := operandRe.FindStringSubmatch(before); om != nil {
				op = "`" + om[1] + "`"
				hit.operand = om[1]
			}
			subs := make([]string, len(m)/2)
			for i := range subs {
				if m[2*i] >= 0 {
					subs[i] = v.canon[m[2*i]:m[2*i+1]]
				}
			}
			if p.kind == castDouble {
				hit.target = subs[1]
				hit.text = "as unknown as " + subs[1]
			} else if hit.operand != "" {
				hit.text = hit.operand + " as any"
			}
			if p.kind == castAny {
				hit.deref = derefRe.MatchString(v.canon[m[1]:]) && parenthesized(before, hit.operand)
			}
			hit.key, hit.msg = rulekit.Normalize(subs[0]), p.describe(op, subs)
			out = append(out, hit)
		}
	}
	if f.Lang == lang.Python && v != nil {
		out = append(out, wrappedCast(f, l, old, v)...)
	}
	return out
}

var (
	// castOpenRe matches a cast( call whose arguments start on the next line.
	castOpenRe = regexp.MustCompile(`\b(?:typing\.|t\.)?cast\(\s*$`)
	// anyArgRe matches Any as the first argument at the start of a line.
	anyArgRe = regexp.MustCompile(`^\s*((?:(?:typing|t)\.)?Any|["'](?:typing\.)?Any["'])\s*,`)
)

// wrappedCast finds `cast(Any, value)` split over lines, as formatters
// wrap long calls:
//
//	user = cast(
//	    Any, payload["user"]
//	)
func wrappedCast(f *core.FileInfo, l core.Line, old bool, v *view) []castHit {
	ms := v.matches(castOpenRe)
	if len(ms) == 0 {
		return nil
	}
	next, ok := sideLine(f, old, l.No+1)
	if !ok {
		return nil
	}
	nv := newView(f.Lang, next)
	am := anyArgRe.FindStringSubmatchIndex(nv.canon)
	if am == nil {
		return nil
	}
	call := strings.TrimSpace(v.canon[ms[0][0]:])
	arg := nv.canon[am[2]:am[3]]
	op := "the value"
	if rest := strings.Trim(strings.TrimSpace(nv.canon[am[1]:]), ",)"); rest != "" {
		op = "`" + shorten(rest, 40) + "`"
	}
	desc := describeCastAny
	if strings.ContainsAny(arg, `"'`) {
		desc = describeCastAnyStr
	}
	return []castHit{{key: rulekit.Normalize(call + arg + ","), msg: desc(op, nil), kind: castPyAny}}
}

func checkAnyCast(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool {
		return langIn(f.Lang, lang.TypeScript, lang.Python) && !isFixtureData(f.Path)
	})
	occs := netNew(files, findCasts, func(h castHit) []string { return []string{h.key} })
	var out []core.Finding
	sides := map[*core.FileInfo]*side{}
	pairs := map[*core.FileInfo][]rulekit.Pair{}
	for _, o := range occs {
		f := o.file
		if sides[f] == nil {
			sides[f] = newSide(f)
			pairs[f] = rulekit.Pairs(f)
		}
		s := sides[f]
		sev, report := castSeverity(a, f, s, o.line.No, o.v)
		if !report {
			continue
		}
		msg := o.v.msg
		// A cast written as part of new code is how that code chose to type
		// a value; a cast added to an existing line forces code that used to
		// type-check past an error. So is calling into a dependency the
		// change upgrades through a cast that hides the new API from the
		// checker (`cast(Any, client).get_snapshot()` with the library bump).
		if sev == core.Warning && !castOnExisting(f, pairs[f], o.line, o.v) {
			if pkg := derefBump(a, f, o.v); pkg != "" {
				msg += fmt.Sprintf(" (the change upgrades %s, whose API this reaches through the cast)", pkg)
			} else {
				sev = core.Info
			}
		}
		if explained(lineNote(f, s, o.line.No)) {
			sev = downgrade(sev)
		}
		fd := newFinding(anyCast, f, o.line.No, msg)
		fd.Severity = sev
		out = append(out, fd)
	}
	return out
}

// derefBump returns the upgraded dependency that a dereferenced cast in f
// may be hiding the API of, or "".
func derefBump(a *core.Analysis, f *core.FileInfo, h castHit) string {
	if !h.deref || (h.kind != castAny && h.kind != castPyAny) {
		return ""
	}
	return importedBump(a, f)
}

var (
	// derefRe matches a cast result that is used right away:
	// `(req as any).user`, `(x as any)[k]`, `(fn as any)(...)`.
	derefRe = regexp.MustCompile(`^\s*\)\s*!?\s*(?:\?\.|\.|\[|\()`)
	// widenRe matches double-cast targets that widen instead of narrowing:
	// reading a typed value as a bag of unknowns checks more, not less.
	widenRe = regexp.MustCompile(`^(?:unknown|any|object|\{\}|unknown\[\]|any\[\]|Array<unknown>|` +
		`Record<\s*(?:string|symbol|number|PropertyKey|string\s*\|\s*symbol)\s*,\s*(?:unknown|any)\s*>)$`)
	primitiveRe = regexp.MustCompile(`^(?:string|number|boolean|bigint|symbol)$`)
	// globalOperands are the runtime's global objects, which carry
	// properties set by other scripts that no declaration describes.
	globalOperands = toSet("window globalThis global self document navigator")
	// caughtNames are the conventional names of a caught error, whose type
	// is unknown by definition.
	caughtNames = toSet("e err error ex exc exception reason cause caught")
	// boundaryCalleeRe matches functions that turn untyped input into
	// data: parsers, validators, mergers, decoders, deep copies.
	boundaryCalleeRe = regexp.MustCompile(`(?i)(?:^|\.)(?:\w*(?:parse|merge|validat|sanitiz|normaliz|coerce|deserializ|decode|clone|fromjson|fromwire|load|read)\w*|structuredClone)$`)
	timerCalleeRe    = regexp.MustCompile(`(?:^|\.)(?:setTimeout|setInterval|setImmediate|requestAnimationFrame|requestIdleCallback)$`)
	calleeRe         = regexp.MustCompile(`([\w$]+(?:\??\.[\w$]+)*)\s*(?:<[^<>]*>)?\s*$`)
)

// parenthesized reports whether the cast's operand opens a parenthesized
// cast expression, `(req as any).user`, rather than a call's argument list,
// `new Date(value as any).getTime()`.
func parenthesized(before, operand string) bool {
	b := strings.TrimRight(before, " ")
	if operand != "" {
		b = strings.TrimRight(strings.TrimSuffix(b, operand), " ")
	}
	if !strings.HasSuffix(b, "(") {
		return false
	}
	b = strings.TrimSuffix(b, "(")
	if b == "" {
		return true
	}
	r := b[len(b)-1]
	return !(r == '_' || r == '$' || r == '>' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
}

// globalOperand reports whether a cast's operand is a runtime global or a
// property read off one (`window.__SERVER_CONFIG__`).
func globalOperand(op string) bool {
	head := op
	if i := strings.IndexAny(head, ".?["); i >= 0 {
		head = head[:i]
	}
	return globalOperands[head]
}

// castSeverity decides how a new escape cast on line n of f is reported:
// whether at all, and how loudly. A cast is a warning when it forces a value
// of a known type into another named type the compiler rejects, or reads
// what the type says is not there; casts at a data boundary (parsed or
// validated input, caught errors, object literals), casts of values that
// are already untyped, widening casts and idioms the codebase already
// uses everywhere are notes or not reported.
func castSeverity(a *core.Analysis, f *core.FileInfo, s *side, n int, h castHit) (core.Severity, bool) {
	sev := anyCast.Severity
	if testish(a, f, n) {
		sev = core.Info
	}
	switch h.kind {
	case castDouble:
		if widenRe.MatchString(strings.Join(strings.Fields(h.target), " ")) || globalOperand(h.operand) {
			return 0, false
		}
		if h.operand != "" && primitiveRe.MatchString(h.target) && guardedNear(s, n, h.operand, 3, typeofGuardRe) {
			return 0, false // if (typeof (json as unknown) === "string") return json as unknown as string
		}
		if untypedDecl(s, n, h.operand) || dbHandle(h) {
			return 0, false
		}
		if atBoundary(s, n, h) {
			sev = core.Info
		}
	case castAny:
		if globalOperand(h.operand) || untypedDecl(s, n, h.operand) {
			return 0, false
		}
		if !h.deref {
			sev = core.Info // passing or storing the value: the use site is unchecked, not the value
		} else if guardedNear(s, n, h.operand, 1, inGuardRe) {
			sev = core.Info // `'Color' in icon ? (icon as any).Color : icon`: the property was checked first
		}
	}
	if sev == core.Warning && h.text != "" {
		if old, ok := f.OldContent(); ok && strings.Contains(string(old), h.text) {
			sev = core.Info // the file's own convention
		} else if repoCount(a, f.Path, h.text, scanOpts{limit: 400, want: 3}) >= 3 {
			return 0, false // an idiom used across the codebase
		}
	}
	return sev, true
}

var (
	inGuardRe      = `<q>[\w$]+<q>\s+in\s+%s\b|\btypeof\s*\(?\s*%s\b|\b%s\s+instanceof\b|\bis[A-Z]\w*\(\s*%s\s*[,)]`
	typeofGuardRe  = `\btypeof\s*\(?\s*%s\b|\b%s\s+instanceof\b`
	narrowGuardRe  = `\bis[A-Z]\w*\(\s*%s\s*[,)]|\btypeof\s*\(?\s*%s\b|\b%s\s+instanceof\b|\bArray\.isArray\(\s*%s\s*\)|\b\w*(?:[Vv]alidat|[Pp]arse|[Aa]ssert)\w*\(\s*%s\s*[,)]`
	unknownParamRe = `(?:^|[(,{\s])%s\s*\??\s*:\s*(?:unknown|Record<\s*string\s*,\s*unknown\s*>|object|JsonValue|JSONValue|Json)\s*[,)=;]`
	catchBindingRe = `\bcatch\s*\(\s*%s\b`
	anyDeclaration = `\b(?:const|let|var)\s+%s\s*(?::\s*any\b|=\s*(?:await\s+)?\(*\s*[\w$.]+(?:\(\))?\s+as\s+any\b)|(?:^|[(,\s])%s\s*\??\s*:\s*any\s*[,)=;]`
)

var (
	// dbHandleTargetRe matches database and client handle types, and
	// dbHandleOperandRe the names such handles go by.
	dbHandleTargetRe  = regexp.MustCompile(`(?:Client|Database|Db|DB|Connection|Pool|Transaction|Tx|Knex|DataSource|EntityManager)$`)
	dbHandleOperandRe = regexp.MustCompile(`(?i)^(?:prisma|db|database|client|tx|trx|transaction|savepoint|conn|connection|pool|knex|drizzle|em|manager|datasource)$`)
)

// dbHandle reports whether a double cast hands a database transaction or
// an injected client to code typed for the same handle from another
// package or wrapper (`tx as unknown as AppDatabase`,
// `this.dbWrite.prisma as unknown as PrismaClient`): two declarations of
// one runtime object, not data forced into a shape.
func dbHandle(h castHit) bool {
	last := h.operand
	if i := strings.LastIndex(last, "."); i >= 0 {
		last = last[i+1:]
	}
	return h.operand != "" && dbHandleOperandRe.MatchString(last) && dbHandleTargetRe.MatchString(h.target)
}

// guardedNear reports whether one of the within lines above n (or n itself)
// matches pattern, with %s standing for operand.
func guardedNear(s *side, n int, operand string, within int, pattern string) bool {
	if operand == "" {
		return false
	}
	var re *regexp.Regexp
	for m := n; m >= 1 && m >= n-within; m-- {
		v := s.view(m)
		// Generated or minified lines are not where a value gets declared.
		if v == nil || len(v.canon) > 2000 || !strings.Contains(v.canon, operand) {
			continue
		}
		if re == nil {
			re = regexp.MustCompile(strings.ReplaceAll(strings.ReplaceAll(pattern, "%s", regexp.QuoteMeta(operand)), "<q>", "[\"'`]"))
		}
		if re.MatchString(v.canon) {
			return true
		}
	}
	return false
}

// untypedDecl reports whether operand is declared `any` (or assigned from an
// `as any` cast) in the lines above n: casting it again changes nothing.
func untypedDecl(s *side, n int, operand string) bool {
	if operand == "" || strings.Contains(operand, ".") {
		return false
	}
	return guardedNear(s, n, operand, 300, anyDeclaration)
}

// atBoundary reports whether a double cast sits where untyped data enters
// typed code: the operand was just narrowed by a type guard or validator,
// is a parameter typed unknown, is a caught error, or is the result of a
// parser, validator or merger; or the operand is an object or array
// literal being shaped into the type.
func atBoundary(s *side, n int, h castHit) bool {
	if op := h.operand; op != "" {
		last := op
		if i := strings.LastIndex(last, "."); i >= 0 {
			last = last[i+1:]
		}
		if caughtNames[last] || guardedNear(s, n, op, 40, catchBindingRe) {
			return true
		}
		if guardedNear(s, n, op, 15, narrowGuardRe) || guardedNear(s, n, op, 60, unknownParamRe) {
			return true
		}
		return boundaryCalleeRe.MatchString(strings.TrimSuffix(op, "()"))
	}
	b := strings.TrimRight(strings.TrimSpace(h.before), "( ")
	line := n
	if b == "" {
		// The operand ends on an earlier line: `mergeConfig(\n ...\n) as unknown as T`.
		if p := prevCode(s, n); p > 0 {
			line = p
		}
	}
	end := strings.TrimSpace(h.before)
	if end == "" {
		if v := s.view(line); v != nil {
			end = strings.TrimSpace(v.canon)
		}
		line++
	}
	switch {
	case strings.HasSuffix(end, "}"), strings.HasSuffix(end, "]"), strings.HasSuffix(end, "})"), strings.HasSuffix(end, "])"):
		return true // { ... } as unknown as T: a literal shaped into the type
	case strings.HasSuffix(end, ")"):
		if callee := closedCallee(s, line, h.before); callee != "" {
			// setTimeout returns a number in browsers and a Timeout in Node:
			// the cast bridges two declarations of the same runtime value.
			return boundaryCalleeRe.MatchString(callee) || strings.HasSuffix(callee, "JSON.parse") || timerCalleeRe.MatchString(callee)
		}
	}
	return false
}

// closedCallee returns the name of the function whose call ends just
// before the cast on line n (before is the code before the cast there),
// following the parentheses back over earlier lines.
func closedCallee(s *side, n int, before string) string {
	text := strings.TrimRight(before, " ")
	if strings.TrimSpace(text) == "" {
		n--
		text, _ = s.code(n)
		text = strings.TrimRight(text, " ")
	}
	depth := 0
	for k := 0; k < 40 && n >= 1; k++ {
		for i := len(text) - 1; i >= 0; i-- {
			switch text[i] {
			case ')', ']', '}':
				depth++
			case '(', '[', '{':
				depth--
				if depth == 0 {
					if text[i] != '(' {
						return ""
					}
					if m := calleeRe.FindStringSubmatch(text[:i]); m != nil {
						return m[1]
					}
					return ""
				}
			}
		}
		n--
		var ok bool
		if text, ok = s.code(n); !ok {
			return ""
		}
	}
	return ""
}
