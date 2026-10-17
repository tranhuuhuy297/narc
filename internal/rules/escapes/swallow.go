package escapes

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/lang"
	"github.com/huyth45/narc/internal/rules/rulekit"
)

var errorSwallowed = &core.Rule{
	ID:       "error-swallowed",
	Title:    "Error swallowed",
	Category: "escapes",
	Severity: core.Warning,
	Summary:  "New code catches an error and throws it away (except: pass, empty catch {}, .catch(() => {}), _ = err, rescue nil, ...).",
	Hint: "Handle the error: log it with context, return or re-raise it, or catch only the specific exception you expect and " +
		"say in a comment why ignoring it is safe. A swallowed error makes failing code look successful to its callers and its tests.",
	Docs: "When a test fails with an exception, the fastest way to make it pass is to catch the exception and do nothing. " +
		"The failure is still there; it is just invisible.\n\n" +
		"```python\n# bad\ntry:\n    charge(card)\nexcept Exception:\n    pass\n\n# good\ntry:\n    charge(card)\nexcept CardDeclined as e:\n    log.warning(\"card declined: %s\", e)\n    return PaymentResult.declined(e.reason)\n```\n\n" +
		"Also reported: Kotlin Flow `.catch { }`, Go's `defer func() { recover() }()`, empty Node.js " +
		"`process.on(\"uncaughtException\" | \"unhandledRejection\")` handlers, empty rejection handlers " +
		"(`.then(onDone, () => {})`), and catch blocks that only touch the error to quiet a linter (`void err`).\n\n" +
		"Python handlers are reported only for bare `except:`, `Exception`, `BaseException` and `AssertionError` " +
		"(`except FileNotFoundError: pass` is idiomatic). In test code, a handler is reported (as a note) when it " +
		"wraps code the test already ran or guards an assertion; a new test that drives a failure path on purpose " +
		"and then checks the aftermath is not reported. Handlers with a comment " +
		"that explains why ignoring the error is safe, and a specific exception caught into a variable named " +
		"`ignored` are reported as notes. Not reported: best-effort cleanup (`try? removeItem`, `@unlink`, " +
		"`with suppress(Exception): os.remove(tmp)`), a PHP " +
		"`@` call whose result is checked, and the JUnit idiom of catching the exception a test expects " +
		"(`fail(\"...\"); } catch (ParseException expected) {}`).",
}

func init() {
	errorSwallowed.Check = checkErrorSwallowed
	core.Register(errorSwallowed)
}

// swallow is an error handler that discards the error.
type swallow struct {
	start, end int
	key        string // kind and caught types: the identity used to net out moved code
	msg        string
	// expectedInTest: an empty catch of a specific exception that a test
	// expects (`fail("..."); } catch (ParseException expected) {}`), the
	// JUnit-style way to assert that code throws.
	expectedInTest bool
	// namedIgnored: a specific exception caught into a variable named to say
	// it is deliberately ignored (`catch (InterruptedException ignored)`).
	namedIgnored bool
	// guard is the code the handler protects (the try block, or the
	// expression before `rescue nil` / `.catch(...)`), when it is known.
	guard []guardStmt
}

// guardStmt is one piece of the code an error handler protects.
type guardStmt struct {
	line int
	code string
	// whole: the piece is a complete line inside the guarded block, so an
	// unchanged line means the change wrapped existing code.
	whole bool
}

func (s swallow) lines() (int, int) { return s.start, s.end }
func (s swallow) ident() string     { return s.key }

func checkErrorSwallowed(a *core.Analysis) []core.Finding {
	files := rulekit.Files(a, func(f *core.FileInfo) bool { return f.Lang.IsProgramming() })
	var out []core.Finding
	for _, fs := range newConstructs(files, findSwallows) {
		f, s, sw := fs.file, fs.side, fs.v
		if cleanupGuard(sw.guard) {
			continue // try { fs.unlinkSync(tmp) } catch {}: best-effort cleanup
		}
		sev := errorSwallowed.Severity
		if testLines(a, f)(sw.start) {
			if sw.expectedInTest {
				continue // asserting that the code throws, not swallowing
			}
			if sw.guard != nil && !hidesFailure(f, sw.guard) {
				continue // a new test driving the failure path on purpose
			}
			sev = core.Info
		}
		note := strings.Join([]string{s.comments(sw.start, sw.end), commentsAbove(f, s, sw.start)}, " ")
		if explained(note) || sw.namedIgnored {
			sev = downgrade(sev)
		}
		fd := newFinding(errorSwallowed, f, sw.start, sw.msg)
		fd.Severity = sev
		out = append(out, fd)
	}
	return out
}

// findSwallows finds error handlers that discard the error in one version
// of a file.
func findSwallows(f *core.FileInfo, s *side) []swallow {
	var out []swallow
	switch f.Lang {
	case lang.Python:
		out = pySwallows(s)
	case lang.Go:
		out = goSwallows(s)
	case lang.Ruby:
		out = rubySwallows(s)
	case lang.JavaScript, lang.TypeScript:
		out = append(append(braceCatches(s), promiseCatches(s)...), processHandlers(s)...)
	case lang.PHP:
		out = append(braceCatches(s), phpSilenced(s)...)
	case lang.Swift:
		out = append(braceCatches(s), swiftTryOptional(s)...)
	case lang.Kotlin:
		out = append(braceCatches(s), flowCatches(s)...)
	case lang.Java, lang.CSharp, lang.Dart, lang.Groovy, lang.Cpp:
		out = braceCatches(s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// cleanupCalls are calls that release or remove a resource: os.remove,
// fs.unlinkSync, FileManager.removeItem, os.RemoveAll, shutil.rmtree,
// close(). A business operation such as deleteUser or cancelOrder is not
// cleanup, and its failure matters.
const cleanupCalls = `(?:remove|delete|unlink|rmdir)(?:Sync|All|Files?|Dir|Directory|Item|Recursive|Temp\w*|Tmp\w*)?|` +
	`rm(?:Sync)?|rm_r?f?|rmtree|close|cancel|abort|invalidate|unregister|unsubscribe|disconnect|kill|dispose`

var (
	// bestEffortRe matches calls whose failure is routinely irrelevant:
	// cleanup (remove a temp file, close, cancel, unsubscribe), media
	// autoplay and cancellable sleeps (Swift's `try? await Task.sleep(...)`).
	// Ignoring their errors is an idiom, not an escape.
	bestEffortRe = regexp.MustCompile(`(?i)\b(?:` + cleanupCalls + `|play|sleep)\s*\([^()]*\)\s*$`)
	// cleanupRe matches cleanup calls only: an empty catch around
	// Thread.sleep loses the thread's interrupt, so it is no cleanup.
	cleanupRe = regexp.MustCompile(`(?i)\b(?:` + cleanupCalls + `)\s*\([^()]*\)\s*$`)
)

// bestEffort reports whether expr ends with a best-effort call.
func bestEffort(expr string) bool { return bestEffortRe.MatchString(strings.TrimRight(expr, " ;")) }

// hidesFailure reports whether a handler in test code hides a failure the
// test used to surface: it wraps code the test already ran (unchanged lines,
// or lines the change re-indented or re-wrapped), or it guards an
// assertion. A handler around code that is new with the test (`job.perform
// rescue nil` before asserting a retry was scheduled) drives a failure path
// deliberately.
func hidesFailure(f *core.FileInfo, guard []guardStmt) bool {
	removed := map[string]bool{}
	for _, l := range f.Removed {
		if k := guardKey(l.Code); k != "" {
			removed[k] = true
		}
	}
	ch := changedLines(f)
	for _, g := range guard {
		k := guardKey(g.code)
		if k == "" {
			continue
		}
		if assertionRe.MatchString(g.code) || removed[k] || (g.whole && !ch[g.line]) {
			return true
		}
	}
	return false
}

// cleanupGuard reports whether every statement a handler protects is a
// cleanup call (see cleanupRe).
func cleanupGuard(guard []guardStmt) bool {
	n := 0
	for _, g := range guard {
		code := strings.Trim(strings.TrimSpace(g.code), "{};")
		if code == "" {
			continue
		}
		if !cleanupRe.MatchString(code) {
			return false
		}
		n++
	}
	return n > 0
}

// assertionRe matches the assertion calls of common test frameworks.
var assertionRe = regexp.MustCompile(`(?i)\b(?:expect|assert\w*|should\w*|xctassert\w*|require\.\w+|t\.(?:error|fatal)\w*)\s*[(.!{]|^\s*assert\b`)

// guardKeyPrefixRe matches the keywords a re-wrapped statement gains or
// loses (`try save()` becomes `try? save()`, `run()` becomes `await run()`).
var guardKeyPrefixRe = regexp.MustCompile(`^(?:(?:await|try[?!]?|return|_\s*=|@)\s*)+`)

// guardKey normalizes a guarded statement for comparison with removed code.
func guardKey(code string) string {
	code = strings.Trim(strings.TrimSpace(code), "{}; \t")
	return rulekit.Normalize(guardKeyPrefixRe.ReplaceAllString(code, ""))
}

// ---- Python ----

var (
	pyExceptRe   = regexp.MustCompile(`^(\s*)except\b\s*\*?\s*(.*?)\s*(?:\bas\s+\w+)?\s*:(.*)$`)
	pySuppressRe = regexp.MustCompile(`\b(?:contextlib\.)?suppress\(\s*([\w.,\s]+?)\s*\)`)
	pyNameRe     = regexp.MustCompile(`[A-Za-z_][\w.]*`)
)

// pyCatchAll classifies the exception list of an except clause. It returns
// what the clause catches ("" when it is narrow: an idiomatic, deliberate
// ignore) and the exception names.
func pyCatchAll(types string) (what string, names []string) {
	names = pyNameRe.FindAllString(types, -1)
	if len(names) == 0 {
		return "every exception, even KeyboardInterrupt and SystemExit", nil
	}
	for _, n := range names {
		switch strings.TrimPrefix(n, "builtins.") {
		case "Exception", "BaseException":
			return "every exception", names
		case "AssertionError":
			return "failed assertions", names
		}
	}
	return "", names
}

func pySwallows(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		code, ok := s.code(n)
		if !ok {
			continue
		}
		if m := pySuppressRe.FindStringSubmatch(code); m != nil && !cleanupOnly(s, n, code) {
			if what, names := pyCatchAll(m[1]); what != "" && len(names) > 0 {
				out = append(out, swallow{start: n, end: n, key: "py:suppress:" + strings.Join(names, ","),
					msg: fmt.Sprintf("`suppress(%s)` silently discards %s raised in the with block", strings.Join(names, ", "), what)})
			}
		}
		m := pyExceptRe.FindStringSubmatch(code)
		if m == nil {
			continue
		}
		what, names := pyCatchAll(m[2])
		if what == "" {
			continue
		}
		end, body, ok := pyBody(s, n, indent(m[1]), m[3])
		if !ok || !onlyPass(body) {
			continue
		}
		clause := "except"
		if t := strings.TrimSpace(m[2]); t != "" {
			clause += " " + t
		}
		label := "`" + clause + ": " + body[0] + "`"
		if len(names) == 0 {
			label = "bare " + label
		}
		if len(names) > 0 && what != "failed assertions" {
			what += " raised in the try block"
		}
		out = append(out, swallow{start: n, end: end, key: "py:except:" + strings.Join(names, ","),
			msg: fmt.Sprintf("%s swallows %s", label, what), guard: pyTryBody(s, n, indent(m[1]))})
	}
	return out
}

var pyTryRe = regexp.MustCompile(`^try\s*:(.*)$`)

// pyTryBody returns the statements of the try block that the except clause
// on line n (indented ind) belongs to, or nil if it cannot be found.
func pyTryBody(s *side, n, ind int) []guardStmt {
	for m := n - 1; m >= 1; m-- {
		l, ok := s.at(m)
		if !ok {
			return nil
		}
		code := strings.TrimSpace(l.Code)
		if code == "" || indent(l.Raw) > ind {
			continue
		}
		if indent(l.Raw) < ind {
			return nil
		}
		if strings.HasPrefix(code, "except") {
			continue // an earlier clause of the same try
		}
		tm := pyTryRe.FindStringSubmatch(code)
		if tm == nil {
			return nil
		}
		var out []guardStmt
		if inline := strings.TrimSpace(tm[1]); inline != "" {
			out = append(out, guardStmt{line: m, code: inline})
		}
		for k := m + 1; k < n; k++ {
			kl, ok := s.at(k)
			if !ok {
				break
			}
			c := strings.TrimSpace(kl.Code)
			if c == "" {
				continue
			}
			if indent(kl.Raw) <= ind {
				break // the first except clause
			}
			out = append(out, guardStmt{line: k, code: c, whole: true})
		}
		return out
	}
	return nil
}

// cleanupOnly reports whether the with block opened on line n (code)
// only runs best-effort cleanup: `with suppress(Exception): os.remove(tmp)`.
func cleanupOnly(s *side, n int, code string) bool {
	l, _ := s.at(n)
	inline := ""
	if i := strings.LastIndex(code, ":"); i >= 0 {
		inline = code[i+1:]
	}
	_, body, ok := pyBody(s, n, indent(l.Raw), inline)
	if !ok {
		return false
	}
	for _, st := range body {
		if !bestEffort(st) {
			return false
		}
	}
	return true
}

// pyBody returns the statements of the block after a Python clause header
// at line n with the given indentation; inline is the code after the ':'.
func pyBody(s *side, n, ind int, inline string) (end int, stmts []string, ok bool) {
	if t := strings.TrimSpace(inline); t != "" {
		for _, st := range strings.Split(t, ";") {
			if st = strings.TrimSpace(st); st != "" {
				stmts = append(stmts, st)
			}
		}
		return n, stmts, true
	}
	end = n
	for m := n + 1; m <= s.len(); m++ {
		l, known := s.at(m)
		if !known {
			return 0, nil, false
		}
		code := strings.TrimSpace(l.Code)
		if code == "" {
			continue
		}
		if indent(l.Raw) <= ind {
			break
		}
		stmts = append(stmts, code)
		end = m
	}
	return end, stmts, len(stmts) > 0
}

func onlyPass(stmts []string) bool {
	if len(stmts) == 0 {
		return false
	}
	for _, st := range stmts {
		if st = strings.TrimRight(st, ";"); st != "pass" && st != "..." {
			return false
		}
	}
	return true
}

// ---- brace languages: empty catch blocks ----

var (
	catchKwRe    = regexp.MustCompile(`(?:^|[^.\w$])catch\b`)
	catchTypesRe = regexp.MustCompile(`\\?[A-Z][\w\\.]*|std::exception|\.\.\.`)
	// catchAllTypes catch everything, including a failed assertion.
	catchAllTypes = toSet("Throwable Error AssertionError AssertionFailedError Exception System.Exception \\Exception " +
		"\\Throwable \\Error std::exception ... BaseException")
	// intentVarRe matches catch variables named to say the exception is
	// expected or deliberately ignored.
	intentVarRe = regexp.MustCompile(`^(?:expected|ignored?|unused)(?:[A-Z]\w*)?$`)
	identRe     = regexp.MustCompile(`[A-Za-z_]\w*`)
	// failCallRe matches the explicit failure a test puts at the end of a
	// try block that must throw.
	failCallRe = regexp.MustCompile(`\b(?:fail|Fail|XCTFail|failure)\s*\(`)
)

func braceCatches(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		code, ok := s.code(n)
		if !ok || !strings.Contains(code, "catch") {
			continue
		}
		for _, loc := range catchKwRe.FindAllStringIndex(code, maxMatches) {
			if sw, ok := emptyCatch(s, n, code, loc[1]); ok {
				out = append(out, sw)
			}
		}
	}
	return out
}

// emptyCatch checks the catch clause whose keyword ends at code[pos].
func emptyCatch(s *side, n int, code string, pos int) (swallow, bool) {
	before := code[:pos-len("catch")]
	isClause, guard := afterTryBlock(s, n, before)
	if !isClause {
		return swallow{}, false // a method or property named catch, not a clause
	}
	rest := code[pos:]
	head := ""
	if t := strings.TrimLeft(rest, " \t"); strings.HasPrefix(t, "(") {
		open := pos + len(rest) - len(t)
		args, end, ok := rulekit.CallArgs(code, open)
		if !ok {
			return swallow{}, false
		}
		head = strings.Join(args, ", ")
		pos = end + 1
	}
	line, col := n, -1
	if i := strings.IndexByte(code[pos:], '{'); i >= 0 {
		between := code[pos : pos+i]
		if strings.ContainsAny(between, ";(") {
			return swallow{}, false
		}
		head = strings.TrimSpace(head + " " + between)
		col = pos + i
	} else if strings.TrimSpace(code[pos:]) == "" {
		// Allman style: the block opens on the next line.
		if line = s.nextCode(n); line > 0 {
			if c, _ := s.code(line); strings.HasPrefix(strings.TrimSpace(c), "{") {
				col = strings.IndexByte(c, '{')
			}
		}
	}
	if col < 0 {
		return swallow{}, false
	}
	end, inner, ok := s.block(line, col)
	body := strings.Trim(inner, " \t\n;")
	if !ok || !emptyBody(body, head) {
		return swallow{}, false
	}
	types := catchTypesRe.FindAllString(head, -1)
	what := "the error"
	label := "empty `catch` block"
	switch on := dartOnRe.FindStringSubmatch(before); {
	case on != nil: // Dart: `on FormatException catch (_) {}`
		types = []string{on[1]}
		label = fmt.Sprintf("empty `on %s catch` block", on[1])
		what = on[1]
	case len(types) > 0:
		label = fmt.Sprintf("empty `catch (%s)` block", shorten(head, 40))
		what = list(types)
		if types[0] == "..." {
			what = "every exception"
		}
	}
	if body != "" { // `void err`
		label = strings.TrimPrefix(label, "empty ") + " that only does `" + body + "`"
	}
	sw := swallow{start: n, end: end, key: fmt.Sprintf("%s:catch:%s", s.lg, strings.Join(types, "|")),
		msg: fmt.Sprintf("%s silently discards %s", label, what), guard: guard}
	switch {
	case s.lg == lang.Swift:
		// XCTFail records a failure without throwing, so even a catch-all
		// after it cannot swallow it.
		sw.expectedInTest = failsBefore(s, n, before)
	case specific(types):
		intent := false
		for _, id := range identRe.FindAllString(head, -1) {
			intent = intent || intentVarRe.MatchString(id)
		}
		sw.namedIgnored = intent
		sw.expectedInTest = intent || failsBefore(s, n, before)
	}
	return sw, true
}

// touchVarRe matches a statement that only mentions the caught error to
// keep "unused variable" checks quiet: `void e` (JavaScript), `_ = e`.
var touchVarRe = regexp.MustCompile(`^(?:void\s+([\w$]+)|_\s*=\s*([\w$]+))$`)

// emptyBody reports whether a catch block body (code, trimmed) does
// nothing with the error caught in head.
func emptyBody(body, head string) bool {
	if body == "" {
		return true
	}
	m := touchVarRe.FindStringSubmatch(body)
	if m == nil {
		return false
	}
	name := m[1] + m[2]
	for _, id := range identRe.FindAllString(head, -1) {
		if id == name {
			return true
		}
	}
	return false
}

var (
	// dartOnRe matches the exception type of Dart's `on Type catch (e)`.
	dartOnRe = regexp.MustCompile(`\bon\s+([\w.<>?]+)\s*$`)
	// tryBlockEndRe matches the code a catch clause follows: the end of the
	// try block (or of the previous clause), with Dart's `on Type` between.
	tryBlockEndRe = regexp.MustCompile(`\}\s*(?:on\s+[\w.<>?]+\s*)?$`)
	// tryOpenerRe matches what opens the block a catch clause follows.
	tryOpenerRe = regexp.MustCompile(`\btry\b.*$|\bdo\s*$|\bcatch\b.*$|\bon\s+[\w.<>?]+.*$`)
)

// tryHeadRe matches the head of a try block: `try`, `try (resources)`, or
// Swift's `do`.
var tryHeadRe = regexp.MustCompile(`(?:^|[^\w$.])(?:try|do)\b(.*)$`)

// afterTryBlock reports whether the catch keyword on line n, preceded by
// before on that line, follows the closing brace of a try block (or of an
// earlier catch), rather than being a method named catch. When the block
// is the try block itself, guard holds its statements.
func afterTryBlock(s *side, n int, before string) (isClause bool, guard []guardStmt) {
	line, text := n, strings.TrimSpace(before)
	for m := n - 1; text == "" && m >= 1 && m > n-5; m-- {
		c, ok := s.code(m)
		if !ok {
			return true, nil // cannot see: assume a clause, as most are
		}
		line, text = m, strings.TrimSpace(c)
	}
	if !tryBlockEndRe.MatchString(text) {
		return false, nil
	}
	code := before
	if line != n {
		code, _ = s.code(line)
	}
	head, openLine, openCol, ok := blockOpener(s, line, strings.LastIndexByte(code, '}'))
	if !ok {
		return true, nil
	}
	if !tryOpenerRe.MatchString(head) {
		return false, nil
	}
	if tm := tryHeadRe.FindStringSubmatch(head); tm != nil && !strings.Contains(head, "catch") {
		guard = blockStmts(s, openLine, openCol)
		if res := strings.TrimSpace(tm[1]); res != "" { // try (var in = open(path))
			guard = append(guard, guardStmt{line: openLine, code: strings.Trim(res, "()")})
		}
	}
	return true, guard
}

// blockStmts returns the lines of the brace block opened at code offset col
// of line n.
func blockStmts(s *side, n, col int) []guardStmt {
	_, inner, ok := s.block(n, col)
	if !ok {
		return nil
	}
	pieces := strings.Split(inner, "\n")
	out := []guardStmt{}
	for k, p := range pieces {
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, guardStmt{line: n + k, code: p, whole: k > 0 && k < len(pieces)-1})
	}
	return out
}

// blockOpener returns the code before the '{' that the '}' at code offset
// col of line n closes, and where that '{' is; ok is false when it cannot
// be found.
func blockOpener(s *side, n, col int) (head string, line, at int, ok bool) {
	depth := 0
	for m := n; m >= 1 && m > n-400; m-- {
		code, known := s.code(m)
		if !known {
			return "", 0, 0, false
		}
		i := len(code) - 1
		if m == n {
			i = col
		}
		for ; i >= 0; i-- {
			switch code[i] {
			case '}':
				depth++
			case '{':
				depth--
				if depth == 0 {
					head := strings.TrimSpace(code[:i])
					if head == "" && m > 1 { // Allman: the opener is on the line above
						if c, ok := s.code(m - 1); ok {
							head = strings.TrimSpace(c)
						}
					}
					return head, m, i, true
				}
			}
		}
	}
	return "", 0, 0, false
}

// specific reports whether a catch clause names only exception types that
// cannot catch a failed assertion or every error.
func specific(types []string) bool {
	if len(types) == 0 {
		return false
	}
	for _, t := range types {
		if catchAllTypes[t] {
			return false
		}
	}
	return true
}

// failsBefore reports whether the try block that ends at the catch on line
// n (whose code before the catch keyword is head) ends with an explicit
// test failure: the code is expected to throw before reaching it.
func failsBefore(s *side, n int, head string) bool {
	if failCallRe.MatchString(head) {
		return true
	}
	seen := 0
	for m := n - 1; m >= 1 && seen < 2; m-- {
		c, ok := s.code(m)
		if !ok {
			return false
		}
		if strings.TrimSpace(c) == "" {
			continue
		}
		if failCallRe.MatchString(c) {
			return true
		}
		seen++
	}
	return false
}

// ---- Kotlin Flow ----

var (
	flowCatchRe   = regexp.MustCompile(`\.catch\s*\{`)
	lambdaParamRe = regexp.MustCompile(`^\s*\w+\s*->`)
)

// flowCatches finds Kotlin Flow `.catch { }` operators with an empty body,
// which drop every upstream exception.
func flowCatches(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		code, ok := s.code(n)
		if !ok || !strings.Contains(code, ".catch") {
			continue
		}
		for _, loc := range flowCatchRe.FindAllStringIndex(code, maxMatches) {
			end, inner, ok := s.block(n, loc[1]-1)
			if !ok || strings.TrimSpace(lambdaParamRe.ReplaceAllString(inner, "")) != "" {
				continue
			}
			out = append(out, swallow{start: n, end: end, key: "kt:flow-catch",
				msg: "empty `.catch { }` operator silently drops every exception from the upstream flow"})
		}
	}
	return out
}

// ---- JavaScript promises ----

var (
	promiseCatchRe = regexp.MustCompile(`\.catch\(\s*`)
	arrowHeadRe    = regexp.MustCompile(`^(?:async\s+)?(?:\(\s*(?:[\w$]+(?:\s*:\s*[\w$<>\[\]|. ]+)?)?\s*\)|[\w$]+)\s*=>\s*`)
	fnHeadRe       = regexp.MustCompile(`^(?:async\s+)?function\s*[\w$]*\s*\([^)]*\)\s*`)
	noopRe         = regexp.MustCompile(`^(?:[\w$]+\.)?noop\s*\)`)
	emptyValueRe   = regexp.MustCompile(`^(?:null|undefined|void\s+0)\s*\)`)
	// bareReturnRe matches a handler body that does nothing: empty, or only
	// a bare `return` (of null or undefined).
	bareReturnRe = regexp.MustCompile(`^[\s;]*(?:return(?:\s+(?:null|undefined|void\s+0))?[\s;]*)?$`)
)

func promiseCatches(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		code, ok := s.code(n)
		if !ok {
			continue
		}
		if strings.Contains(code, ".then(") {
			out = append(out, rejectionHandlers(n, code)...)
		}
		if !strings.Contains(code, ".catch(") {
			continue
		}
		for _, loc := range promiseCatchRe.FindAllStringIndex(code, maxMatches) {
			p := loc[1]
			rest := code[p:]
			end := n
			swallowed := false
			switch {
			case noopRe.MatchString(rest):
				swallowed = true
			default:
				h := arrowHeadRe.FindString(rest)
				if h == "" {
					h = fnHeadRe.FindString(rest)
				}
				if h == "" {
					continue
				}
				body := rest[len(h):]
				if strings.HasPrefix(body, "{") {
					e, inner, ok := s.block(n, p+len(h))
					swallowed = ok && bareReturnRe.MatchString(inner)
					end = e
				} else if strings.HasPrefix(h, "function") {
					continue
				} else {
					swallowed = emptyValueRe.MatchString(body)
				}
			}
			if !swallowed || bestEffort(tail(code[:loc[0]], 300)) {
				continue
			}
			handler := "() => {}"
			if args, _, ok := rulekit.CallArgs(code, loc[0]+len(".catch")); ok && len(args) == 1 && end == n {
				handler = args[0]
			}
			// The promise is the chain before .catch, possibly on the line above.
			guard := []guardStmt{{line: n, code: code[:loc[0]]}}
			if strings.TrimSpace(code[:loc[0]]) == "" {
				if prev := n - 1; prev >= 1 {
					if c, ok := s.code(prev); ok {
						guard = []guardStmt{{line: prev, code: c}}
					}
				}
			}
			out = append(out, swallow{start: n, end: end, key: "js:promise-catch", guard: guard,
				msg: fmt.Sprintf("`.catch(%s)` silently discards the rejection", shorten(handler, 40))})
		}
	}
	return out
}

var promiseThenRe = regexp.MustCompile(`\.then\(`)

// rejectionHandlers finds `.then(onFulfilled, () => {})`: a rejection
// handler that does nothing, the two-argument spelling of an empty .catch.
func rejectionHandlers(n int, code string) []swallow {
	var out []swallow
	for _, loc := range promiseThenRe.FindAllStringIndex(code, maxMatches) {
		args, _, ok := rulekit.CallArgs(code, loc[1]-1)
		if !ok || len(args) != 2 || !emptyHandler(args[1]) || bestEffort(tail(code[:loc[0]], 300)) {
			continue
		}
		out = append(out, swallow{start: n, end: n, key: "js:promise-then-rejection",
			guard: []guardStmt{{line: n, code: code[:loc[0]]}},
			msg:   fmt.Sprintf("`.then(…, %s)` rejection handler silently discards the rejection", shorten(args[1], 30))})
	}
	return out
}

// emptyHandler reports whether a callback expression does nothing:
// `() => {}`, `() => undefined`, `function () {}`, `noop`.
func emptyHandler(fn string) bool {
	fn = strings.TrimSpace(fn)
	if noopRe.MatchString(fn + ")") {
		return true
	}
	h := arrowHeadRe.FindString(fn)
	if h == "" {
		h = fnHeadRe.FindString(fn)
	}
	if h == "" {
		return false
	}
	body := strings.TrimSpace(fn[len(h):])
	if strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") {
		return bareReturnRe.MatchString(body[1 : len(body)-1])
	}
	return !strings.HasPrefix(h, "function") && emptyValueRe.MatchString(body+")")
}

// processHandlerRe matches a Node.js process-wide error handler.
var processHandlerRe = re(`\bprocess\.on\(\s*<q>(uncaughtException|unhandledRejection)<q>\s*,\s*`)

// processHandlers finds process-wide error handlers that do nothing, which
// hide every crash and every unhandled rejection in the process.
func processHandlers(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		v := s.view(n)
		if v == nil || !strings.Contains(v.canon, "process.on") {
			continue
		}
		ms := v.matches(processHandlerRe)
		code, _ := s.code(n)
		i := strings.Index(code, "process.on(")
		if len(ms) == 0 || i < 0 {
			continue
		}
		event := v.canon[ms[0][2]:ms[0][3]]
		// Parse the handler from the code, where string contents are gone
		// and cannot unbalance the brackets.
		if args, _, ok := rulekit.CallArgs(code, i+len("process.on")); ok && len(args) == 2 {
			if !emptyHandler(args[1]) {
				continue
			}
			what := map[string]string{"uncaughtException": "uncaught exception", "unhandledRejection": "unhandled rejection"}[event]
			out = append(out, swallow{start: n, end: n, key: "js:process-" + event,
				msg: fmt.Sprintf("empty `process.on(\"%s\")` handler silently swallows every %s in the process", event, what)})
		}
	}
	return out
}

// ---- Go ----

var (
	goDiscardErrRe = regexp.MustCompile(`^\s*_\s*=\s*(err|\w*Err|\w*Error)\s*;?\s*$`)
	goIfErrRe      = regexp.MustCompile(`^\s*(?:\}\s*else\s+)?if\s+(?:[^{;]*;\s*)?(err|\w*Err)\s*!=\s*nil\s*\{`)
	goReturnNilsRe = regexp.MustCompile(`^return\s+nil(?:\s*,\s*nil)*$`)
	// goRecoverRe matches a recover() whose value nobody looks at:
	// `defer func() { recover() }()`, `_ = recover()`.
	goRecoverRe = regexp.MustCompile(`(?:^\s*|\{\s*|;\s*)(?:_\s*=\s*)?recover\(\)\s*(?:;|\}|$)`)
)

func goSwallows(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		code, ok := s.code(n)
		if !ok {
			continue
		}
		if m := goDiscardErrRe.FindStringSubmatch(code); m != nil {
			out = append(out, swallow{start: n, end: n, key: "go:discard-err",
				msg: fmt.Sprintf("`_ = %s` discards the error instead of handling it", m[1])})
			continue
		}
		if goRecoverRe.MatchString(code) {
			out = append(out, swallow{start: n, end: n, key: "go:discard-recover",
				msg: "`recover()` with its result discarded turns every panic into silent success"})
			continue
		}
		m := goIfErrRe.FindStringSubmatchIndex(code)
		if m == nil {
			continue
		}
		name := code[m[2]:m[3]]
		end, inner, ok := s.block(n, m[1]-1)
		if !ok {
			continue
		}
		var stmts []string
		for _, st := range strings.FieldsFunc(inner, func(r rune) bool { return r == '\n' || r == ';' }) {
			if st = strings.TrimSpace(st); st != "" {
				stmts = append(stmts, st)
			}
		}
		switch {
		case len(stmts) == 0:
			out = append(out, swallow{start: n, end: end, key: "go:empty-if-err",
				msg: fmt.Sprintf("empty `if %s != nil {}` block ignores the error", name)})
		case len(stmts) == 1 && goReturnNilsRe.MatchString(stmts[0]):
			results := goResults(s, n)
			nils := strings.Count(stmts[0], "nil")
			if len(results) == nils && results[len(results)-1] == "error" {
				out = append(out, swallow{start: n, end: end, key: "go:return-nil-on-err",
					msg: fmt.Sprintf("`if %s != nil { %s }` drops the error: callers see success", name, stmts[0])})
			}
		}
	}
	return out
}

// goResults returns the result types of the function enclosing line n, or
// nil if it cannot be determined.
func goResults(s *side, n int) []string {
	depth := 0
	for m := n - 1; m >= 1 && m > n-1000; m-- {
		code, ok := s.code(m)
		if !ok {
			return nil
		}
		for i := len(code) - 1; i >= 0; i-- {
			switch code[i] {
			case '}':
				depth++
			case '{':
				if depth > 0 {
					depth--
					continue
				}
				if sig, ok := goSignature(s, m, code[:i]); ok {
					return goResultTypes(sig)
				}
			}
		}
	}
	return nil
}

var (
	goFuncRe       = regexp.MustCompile(`\bfunc\b`)
	goMethodNameRe = regexp.MustCompile(`^[A-Za-z_]\w*\s*[(\[]`)
)

// goSignature returns the text of a func signature ending at line m (whose
// code before the '{' is head), following a parameter list that spans lines.
func goSignature(s *side, m int, head string) (string, bool) {
	text := head
	if !goFuncRe.MatchString(text) {
		if !strings.HasPrefix(strings.TrimSpace(text), ")") {
			return "", false
		}
		for k := m - 1; k >= 1 && k > m-20; k-- {
			c, ok := s.code(k)
			if !ok {
				return "", false
			}
			text = c + " " + text
			if goFuncRe.MatchString(c) {
				break
			}
		}
		if !goFuncRe.MatchString(text) {
			return "", false
		}
	}
	locs := goFuncRe.FindAllStringIndex(text, -1)
	return text[locs[len(locs)-1][1]:], true
}

// goResultTypes parses "(r *T) Name(params) (int, error)" (or a closure's
// "(params) error") and returns the result types.
func goResultTypes(sig string) []string {
	rest := strings.TrimSpace(sig)
	group := func() (string, bool) {
		if !strings.HasPrefix(rest, "(") {
			return "", false
		}
		args, end, ok := rulekit.CallArgs(rest, 0)
		if !ok {
			return "", false
		}
		rest = strings.TrimSpace(rest[end+1:])
		return strings.Join(args, ","), true
	}
	skipName := func() {
		i := 0
		for i < len(rest) && (rest[i] == '_' || rest[i] >= 'a' && rest[i] <= 'z' || rest[i] >= 'A' && rest[i] <= 'Z' || rest[i] >= '0' && rest[i] <= '9') {
			i++
		}
		rest = strings.TrimSpace(rest[i:])
		if strings.HasPrefix(rest, "[") { // type parameters
			if j := strings.Index(rest, "]"); j >= 0 {
				rest = strings.TrimSpace(rest[j+1:])
			}
		}
	}
	if strings.HasPrefix(rest, "(") {
		if _, ok := group(); !ok {
			return nil
		}
		if goMethodNameRe.MatchString(rest) { // that was a method receiver
			skipName()
			if _, ok := group(); !ok {
				return nil
			}
		}
	} else {
		skipName()
		if _, ok := group(); !ok {
			return nil
		}
	}
	if rest == "" {
		return []string{}
	}
	if strings.HasPrefix(rest, "(") {
		inner, ok := group()
		if !ok {
			return nil
		}
		var types []string
		for _, r := range strings.Split(inner, ",") {
			f := strings.Fields(r)
			if len(f) > 0 {
				types = append(types, f[len(f)-1])
			}
		}
		return types
	}
	return []string{strings.Fields(rest)[0]}
}

// ---- Ruby ----

var (
	rbRescueNilRe = regexp.MustCompile(`\S.*\brescue\s+nil\b`)
	rbRescueRe    = regexp.MustCompile(`^(\s*)rescue\b\s*(.*?)\s*(?:=>\s*\w+)?\s*(?:then)?\s*$`)
	rbEndRe       = regexp.MustCompile(`^(end|ensure|else|rescue)\b`)
	rbBroadRe     = regexp.MustCompile(`\b(StandardError|Exception)\b`)
)

func rubySwallows(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		code, ok := s.code(n)
		if !ok {
			continue
		}
		if loc := rbRescueNilRe.FindStringIndex(code); loc != nil {
			expr := code[:strings.LastIndex(code, "rescue")]
			if !bestEffort(expr) {
				out = append(out, swallow{start: n, end: n, key: "rb:rescue-nil", guard: []guardStmt{{line: n, code: expr}},
					msg: "`rescue nil` turns every StandardError into nil"})
			}
			continue
		}
		m := rbRescueRe.FindStringSubmatch(code)
		if m == nil {
			continue
		}
		types := strings.TrimSpace(m[2])
		broad := types == "" || rbBroadRe.MatchString(types)
		if !broad {
			continue
		}
		next := s.nextCode(n)
		if next == 0 {
			continue
		}
		end := n
		c, _ := s.code(next)
		c = strings.TrimSpace(c)
		if c == "nil" {
			end = next
			if next = s.nextCode(next); next == 0 {
				continue
			}
			c, _ = s.code(next)
			c = strings.TrimSpace(c)
		}
		if !rbEndRe.MatchString(c) {
			continue
		}
		what := "every StandardError"
		if strings.Contains(types, "Exception") {
			what = "every exception, even signals and exit"
		}
		out = append(out, swallow{start: n, end: end, key: "rb:rescue-empty:" + types, guard: rbBeginBody(s, n, indent(m[1])),
			msg: fmt.Sprintf("empty `rescue%s` swallows %s", strings.TrimRight(" "+types, " "), what)})
	}
	return out
}

var rbBlockStartRe = regexp.MustCompile(`^(?:begin|def\b.*|.*\bdo(?:\s*\|[^|]*\|)?)$`)

// rbBeginBody returns the statements that the rescue clause on line n
// (indented ind) protects: the body of its begin, def or do block.
func rbBeginBody(s *side, n, ind int) []guardStmt {
	var out []guardStmt
	for m := n - 1; m >= 1; m-- {
		l, ok := s.at(m)
		if !ok {
			return nil
		}
		code := strings.TrimSpace(l.Code)
		if code == "" {
			continue
		}
		if indent(l.Raw) > ind {
			out = append([]guardStmt{{line: m, code: code, whole: true}}, out...)
			continue
		}
		if indent(l.Raw) == ind && strings.HasPrefix(code, "rescue") {
			out = nil // an earlier clause: its body is not the protected code
			continue
		}
		if rbBlockStartRe.MatchString(code) {
			return out
		}
		return nil
	}
	return nil
}

// ---- PHP ----

var (
	phpAtRe = regexp.MustCompile(`@\s*\\?([a-zA-Z_]\w*)\s*\(`)
	// phpCheckedRe matches a line that tests the result of the silenced
	// call (`if (!@mkdir($dir) && !is_dir($dir))`, `@fopen(...) or throw`):
	// the failure is handled through the return value instead of a warning.
	phpCheckedRe = regexp.MustCompile(`(?i)\b(?:if|elseif|while)\s*\(|[!=]==?\s*false\b|\bfalse\s*[!=]==?|\?\?|\?:|\|\||&&|\bor\b`)
)

// phpSilenced finds the @ error-control operator on a call whose failure
// nothing checks.
func phpSilenced(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		v := s.view(n)
		if v == nil || !strings.Contains(v.canon, "@") {
			continue
		}
		checked := phpCheckedRe.MatchString(v.canon)
		for _, m := range v.matches(phpAtRe) {
			if m[0] > 0 && strings.ContainsAny(v.canon[m[0]-1:m[0]], "$>)]_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
				continue
			}
			fn := v.canon[m[2]:m[3]]
			switch {
			case bestEffort(fn + "()"):
				continue // @unlink($tmp): best-effort cleanup
			case strings.EqualFold(fn, "trigger_error"):
				continue // @trigger_error(..., E_USER_DEPRECATED): the deprecation-notice idiom
			case checked:
				continue
			}
			code, _ := s.code(n)
			out = append(out, swallow{start: n, end: n, key: "php:silence:" + fn,
				guard: []guardStmt{{line: n, code: strings.Replace(code, "@", "", 1)}},
				msg:   fmt.Sprintf("`@%s()` hides the errors and warnings the call raises", fn)})
		}
	}
	return out
}

// ---- Swift ----

var swiftTryOptRe = regexp.MustCompile(`^\s*(?:_\s*=\s*)?try\?\s+\S`)

// swiftTryOptional finds `try?` whose result is thrown away.
func swiftTryOptional(s *side) []swallow {
	var out []swallow
	for _, n := range s.lineNos() {
		if code, ok := s.code(n); ok && swiftTryOptRe.MatchString(code) && !bestEffort(code) {
			out = append(out, swallow{start: n, end: n, key: "swift:try-optional", guard: []guardStmt{{line: n, code: code}},
				msg: "`try?` with its result unused discards the error"})
		}
	}
	return out
}
