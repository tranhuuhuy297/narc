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
		"Casts in test code (mocks, deliberately malformed input) are reported as notes.",
}

func init() {
	anyCast.Check = checkAnyCast
	core.Register(anyCast)
}

// castPattern is one kind of escape cast.
type castPattern struct {
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
	{langs: []lang.Lang{lang.TypeScript}, re: regexp.MustCompile(`\bas\s+any\b`),
		describe: func(op string, _ []string) string {
			return fmt.Sprintf("`as any` cast on %s: the compiler no longer checks how it is used", op)
		}},
	// `x as unknown as T`, also written `(x as unknown) as T`.
	{langs: []lang.Lang{lang.TypeScript}, re: regexp.MustCompile(`\bas\s+unknown(?:\s*\)\s*|\s+)as\s+([\w$.]+(?:<[^>]*>)?(?:\[\])*)`),
		describe: func(op string, m []string) string {
			return fmt.Sprintf("double cast `as unknown as %s` forces %s into a type the compiler says it does not have", m[1], op)
		}},
	{langs: []lang.Lang{lang.TypeScript}, re: regexp.MustCompile(`<any>\s*[\w$(\[{]`),
		describe: func(string, []string) string {
			return "`<any>` cast: the compiler no longer checks how the value is used"
		}},
	{langs: python, re: regexp.MustCompile(`\b(?:typing\.|t\.)?cast\(\s*(?:(?:typing|t)\.)?Any\s*,`), describe: describeCastAny},
	{langs: python, re: regexp.MustCompile(`\b(?:typing\.|t\.)?cast\(\s*["'](?:typing\.)?Any["']\s*,`), describe: describeCastAnyStr},
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
			if f.Lang == lang.Python {
				// cast(Any, value): the value is the second argument.
				open := m[0] + strings.IndexByte(v.canon[m[0]:], '(')
				if args, _, ok := rulekit.CallArgs(v.canon, open); ok && len(args) == 2 {
					op = "`" + shorten(args[1], 40) + "`"
				}
			} else if om := operandRe.FindStringSubmatch(before); om != nil {
				op = "`" + om[1] + "`"
			}
			subs := make([]string, len(m)/2)
			for i := range subs {
				if m[2*i] >= 0 {
					subs[i] = v.canon[m[2*i]:m[2*i+1]]
				}
			}
			out = append(out, castHit{key: rulekit.Normalize(subs[0]), msg: p.describe(op, subs)})
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
	return []castHit{{key: rulekit.Normalize(call + arg + ","), msg: desc(op, nil)}}
}

func checkAnyCast(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool { return langIn(f.Lang, lang.TypeScript, lang.Python) })
	occs := netNew(files, findCasts, func(h castHit) []string { return []string{h.key} })
	var out []core.Finding
	sides := map[*core.FileInfo]*side{}
	for _, o := range occs {
		f := o.file
		if sides[f] == nil {
			sides[f] = newSide(f)
		}
		sev := anyCast.Severity
		if testLines(a, f)(o.line.No) {
			sev = core.Info
		}
		if explained(lineNote(f, sides[f], o.line.No)) {
			sev = downgrade(sev)
		}
		fd := newFinding(anyCast, f, o.line.No, o.v.msg)
		fd.Severity = sev
		out = append(out, fd)
	}
	return out
}
