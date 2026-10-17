package accounting

import (
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// A test can be switched off without a skip marker: rename it so the test
// runner no longer collects it (test_refund → _test_refund, TestRefund →
// testRefund), or drop its @Test / #[test] / [Fact] marker. Its body,
// assertions included, stays in the file, so by the numbers nothing was
// lost. The ledger links such a vanished test to the plain function that
// took over its body.

// formerTest is the plain function a vanished test became.
type formerTest struct {
	name string // the function's name now
	line int    // new-side line of its declaration
}

// funcDecl finds a function declaration on one line; group 1 is the name.
type funcDecl struct {
	re *regexp.Regexp
	// nocomment: match the line's NoComment text instead of its Code, for
	// names that are string-like (Kotlin `backtick names`).
	nocomment bool
}

// cLikeDecl matches a method declaration in Java-like languages: optional
// annotations and modifiers, a return type, then the name and an opening
// parameter list on a line without a statement terminator.
var cLikeDecl = funcDecl{re: regexp.MustCompile(`^\s*(?:@\w+(?:\([^)]*\))?\s+)*(?:(?:public|private|protected|internal|static|final|abstract|override|virtual|async|synchronized|sealed|unsafe|inline|constexpr)\s+)*[\w<>\[\],.?:*&]+\s+(\w+)\s*\([^;]*$`)}

var funcDecls = map[lang.Lang]funcDecl{
	lang.Python:     {re: regexp.MustCompile(`^\s*(?:async\s+)?def\s+(\w+)`)},
	lang.Ruby:       {re: regexp.MustCompile(`^\s*def\s+(?:self\.)?(\w+[?!]?)`)},
	lang.Elixir:     {re: regexp.MustCompile(`^\s*defp?\s+(\w+[?!]?)`)},
	lang.Go:         {re: regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?(\w+)\s*[\[(]`)},
	lang.Rust:       {re: regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?fn\s+(\w+)`)},
	lang.Kotlin:     {re: regexp.MustCompile("\\bfun\\s+(?:<[^>]*>\\s*)?(?:[\\w.]+\\.)?(\\w+|`[^`]+`)\\s*\\("), nocomment: true},
	lang.Swift:      {re: regexp.MustCompile(`\bfunc\s+(\w+)\s*[<(]`)},
	lang.PHP:        {re: regexp.MustCompile(`\bfunction\s+(\w+)\s*\(`)},
	lang.Scala:      {re: regexp.MustCompile(`\bdef\s+(\w+)`)},
	lang.JavaScript: jsDecl,
	lang.TypeScript: jsDecl,
	lang.Java:       cLikeDecl,
	lang.CSharp:     cLikeDecl,
	lang.Dart:       cLikeDecl,
	lang.C:          cLikeDecl,
	lang.Cpp:        cLikeDecl,
}

// jsDecl matches function declarations, function-valued constants and
// class methods.
var jsDecl = funcDecl{re: regexp.MustCompile(`^\s*(?:export\s+)?(?:(?:async\s+)?function\*?\s+([\w$]+)|(?:const|let|var)\s+([\w$]+)\s*=\s*(?:async\s*)?(?:function\b|\([^()]*\)\s*(?::[^=]+)?=>|[\w$]+\s*=>)|(?:(?:async|static|private|public|protected)\s+)*([\w$]+)\s*\([^()]*\)\s*(?::[^{]+)?\{\s*$)`)}

// declWords lists, per language, words one of which every declaration
// line matched by funcDecls contains: a cheap filter before the regular
// expression.
var declWords = map[lang.Lang][]string{
	lang.Python: {"def"}, lang.Ruby: {"def"}, lang.Elixir: {"def"}, lang.Scala: {"def"},
	lang.Go: {"func"}, lang.Swift: {"func"}, lang.Rust: {"fn"}, lang.Kotlin: {"fun"}, lang.PHP: {"function"},
	lang.JavaScript: {"function", "=>", "{"}, lang.TypeScript: {"function", "=>", "{"},
	lang.Java: {"("}, lang.CSharp: {"("}, lang.Dart: {"("}, lang.C: {"("}, lang.Cpp: {"("},
}

// declName returns the name of a function declared on line ll, if any.
func declName(l lang.Lang, ll lang.LexedLine) string {
	d, ok := funcDecls[l]
	if !ok || !containsAnyOf(ll.Code, declWords[l]) {
		return ""
	}
	text := ll.Code
	if d.nocomment {
		text = ll.NoComment
	}
	m := d.re.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			if notDefs[g] || cLikeKeywords[g] {
				return ""
			}
			return g
		}
	}
	return ""
}

// cLikeKeywords cannot be function names; they show up where the loose
// Java-like pattern meets a control statement (`} else if (x) {`).
var cLikeKeywords = map[string]bool{"if": true, "for": true, "foreach": true, "while": true, "switch": true,
	"catch": true, "using": true, "lock": true, "fixed": true, "return": true, "sizeof": true, "typeof": true,
	"nameof": true, "await": true, "throw": true, "case": true, "synchronized": true}

// findFormerTests links each vanished test to a plain function in the same
// file that took over its body: (nearly) the same tokens, not a test now,
// not a plain function before the change (so not some old helper that
// happens to look alike), and referenced nowhere else in the test code of
// the change, so nothing runs it. This needs full contents: in a patch, a
// test marker or a test class may lie outside the hunks.
func (l *ledger) findFormerTests() {
	byFile := map[*core.FileInfo][]*instance{}
	for _, o := range l.olds {
		f := o.file
		_, renamed := l.renamedTo[f]
		if o.match == nil && !o.partial && !o.skipped() && o.size() >= 3 && f.Diff.Kind != diff.Deleted && !renamed &&
			fullContent(f) {
			byFile[f] = append(byFile[f], o)
		}
	}
	if len(byFile) == 0 {
		return
	}
	refs := l.newRefs()
	for f, olds := range byFile {
		wasPlain := l.oldPlainFuncs(f)
		seg := l.segments(f, after)[0] // full contents: one segment
		for i, ll := range seg.lines {
			name := declName(f.Lang, ll)
			if name == "" || wasPlain[name] || l.inNewTest(f, i+1) {
				continue
			}
			end := blockEnd(f.Lang, seg.lines, i)
			if end <= i {
				continue
			}
			bag, size, own := rangeBag(seg, i, end, name)
			if refs[strings.Trim(name, "`")] > own {
				continue // called or referenced: it runs as part of something else
			}
			replaced := markerAdded(f, seg, i)
			var best *instance
			bestSim := 0.0
			for _, o := range olds {
				if replaced && o.block.Bare == strings.Trim(name, "`") {
					// Same name under a new attribute: the test marker was
					// swapped for one of a framework narc may not know
					// (@RetryingTest, [RetryFact]), not removed.
					continue
				}
				if s := diceBags(o.bag(), o.size(), bag, size); o.former == nil && s > bestSim {
					best, bestSim = o, s
				}
			}
			if best != nil && bestSim >= 0.7 {
				best.former = &formerTest{name: strings.Trim(name, "`"), line: i + 1}
			}
		}
	}
}

// testishAttr matches attribute names that may mark a test in some
// framework: @RetryingTest, [RetryFact], #[tokio::test], @Property.
var testishAttr = regexp.MustCompile(`(?i)test|fact|theory|spec|case|propert|fuzz|bench|scenario|example`)

// markerAdded reports whether the change added an attribute or annotation
// that may be a test marker directly above the declaration on line i
// (0-based) of seg. Lint and metadata attributes (#[allow(dead_code)],
// @SuppressWarnings) do not count.
func markerAdded(f *core.FileInfo, seg *segment, i int) bool {
	added := map[int]bool{}
	for _, ln := range f.Added {
		added[ln.No] = true
	}
	for k := i - 1; k >= 0; k-- {
		ll := seg.lines[k]
		c := strings.TrimSpace(ll.Code)
		if !strings.HasPrefix(c, "@") && !strings.HasPrefix(c, "#[") && !strings.HasPrefix(c, "[") {
			return false
		}
		if added[seg.lineNo(k)] && testishAttr.MatchString(c) {
			return true
		}
	}
	return false
}

// oldPlainFuncs returns the names of the functions in the old version of f
// that were not tests.
func (l *ledger) oldPlainFuncs(f *core.FileInfo) map[string]bool {
	tests := map[string]bool{}
	for _, o := range l.olds {
		if o.file == f {
			tests[o.block.Bare] = true
		}
	}
	out := map[string]bool{}
	for _, ll := range f.OldLines() {
		if name := declName(f.Lang, ll); name != "" && !tests[strings.Trim(name, "`")] {
			out[name] = true
		}
	}
	return out
}

// inNewTest reports whether new-side line no of f lies inside a test.
func (l *ledger) inNewTest(f *core.FileInfo, no int) bool {
	for _, n := range l.news {
		if n.file == f && n.line(n.block.Start) <= no && no <= n.line(n.block.End) {
			return true
		}
	}
	return false
}

// newRefs counts the identifiers (and whole string literals, which can
// name a method, as in @MethodSource("cases")) in the new test code of the
// change.
func (l *ledger) newRefs() map[string]int {
	refs := map[string]int{}
	for _, f := range l.a.Files {
		if !l.testSide(f, after) {
			continue
		}
		for _, seg := range l.segments(f, after) {
			for _, ll := range seg.lines {
				for _, id := range idents(ll.Code) {
					refs[id]++
				}
				for _, s := range ll.Strings {
					refs[strings.TrimSpace(s)]++
				}
			}
		}
	}
	return refs
}

// rangeBag returns the token multiset of lines from..to of a segment (0-based,
// inclusive), leaving out the function's own name on its declaration line.
// own counts the references to name inside the range.
func rangeBag(seg *segment, from, to int, name string) (bag map[string]int, size, own int) {
	bare := strings.Trim(name, "`")
	bag = map[string]int{}
	for i := from; i <= to && i < len(seg.lines); i++ {
		ll := seg.lines[i]
		for _, id := range idents(ll.Code) {
			if id == bare {
				own++
			}
		}
		for _, s := range ll.Strings {
			if strings.TrimSpace(s) == bare {
				own++
			}
		}
		for _, tk := range tokens(ll.Code, ll.Strings) {
			if i == from && (tk == bare || tk == `"`+bare) {
				continue
			}
			bag[tk]++
			size++
		}
	}
	return bag, size, own
}

// diceBags is the Sørensen–Dice similarity of two token multisets.
func diceBags(a map[string]int, an int, b map[string]int, bn int) float64 {
	if an+bn == 0 {
		return 0
	}
	common := 0
	for tk, n := range a {
		common += min(n, b[tk])
	}
	return 2 * float64(common) / float64(an+bn)
}
