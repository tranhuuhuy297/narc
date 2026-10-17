package testblocks

import (
	"regexp"
	"strings"

	"github.com/huyth45/narc/internal/lang"
)

// Ruby (RSpec, minitest) and Elixir (ExUnit) blocks are delimited by
// keywords: `it "x" do ... end`, `it { ... }`, `def test_x ... end`,
// `test "x" do ... end`.

var (
	// The head must be followed by something that starts its arguments, so
	// that Ruby 3.4's implicit block parameter (`it.save!`) is not a test.
	rubyHead = regexp.MustCompile(`^\s*(?:RSpec\s*\.\s*)?([xf]?(?:describe|context|feature|it|specify|example|scenario|its)|shared_examples(?:_for)?|shared_context|test|pending)(?:\s*[('"{:A-Z]|\s+do\b)`)
	rubyDef  = regexp.MustCompile(`^\s*def\s+(test_\w*[?!]?)`)
	rubyCls  = regexp.MustCompile(`^\s*class\s+([\w:]+)`)
	// Metadata that skips an example: it 'x', :skip do / skip: 'reason'.
	rubySkipMeta = regexp.MustCompile(`(?:,\s*:(?:skip|pending)\b|\b(?:skip|pending):\s*[^\s,]+)`)
	rubyEndless  = regexp.MustCompile(`^\s*def\s+[\w.?!]+(?:\([^)]*\))?\s*=[^=~]`)

	elixirHead = regexp.MustCompile(`^\s*(describe|test|property)\b\s*(?:\(|["'])`)
	elixirTag  = regexp.MustCompile(`^\s*@(?:tag|moduletag)\b.*\b(?:skip|pending)\b`)
)

type tokRole byte

const (
	roleNone tokRole = iota
	roleOpenKw
	roleOpenBrace
	roleCloseKw
	roleCloseBrace
)

type rtok struct {
	pos  int
	word string
	role tokRole
}

var rubyTokRe = regexp.MustCompile(`[A-Za-z_]\w*[?!]?|[{}]`)

// tokens splits the code into words and braces and assigns block roles.
func (t *text) tokens() []rtok {
	ruby := t.lang == lang.Ruby
	var out []rtok
	loopLine := -1 // a while/until/for whose optional `do` must not open a block
	for _, m := range rubyTokRe.FindAllStringIndex(t.code, -1) {
		w := t.code[m[0]:m[1]]
		tk := rtok{pos: m[0], word: w}
		var prev, next byte
		if m[0] > 0 {
			prev = t.code[m[0]-1]
		}
		if m[1] < len(t.code) {
			next = t.code[m[1]]
		}
		method := prev == '.' || prev == ':' || prev == '@' || prev == '$' ||
			(next == ':' && (m[1]+1 >= len(t.code) || t.code[m[1]+1] != ':'))
		line := t.lineOf(m[0])
		switch {
		case w == "{":
			if ruby {
				tk.role = roleOpenBrace
			}
		case w == "}":
			if ruby {
				tk.role = roleCloseBrace
			}
		case method:
		case w == "end":
			tk.role = roleCloseKw
		case w == "do":
			if line != loopLine {
				tk.role = roleOpenKw
			}
		case !ruby:
			if w == "fn" {
				tk.role = roleOpenKw
			}
		case w == "def":
			if !rubyEndless.MatchString(t.lines[line].Code) {
				tk.role = roleOpenKw
			}
		case w == "class" || w == "module" || w == "begin" || w == "case":
			tk.role = roleOpenKw
		case w == "if" || w == "unless" || w == "while" || w == "until" || w == "for":
			if t.statementStart(m[0]) {
				tk.role = roleOpenKw
				if w == "while" || w == "until" || w == "for" {
					loopLine = line
				}
			}
		}
		out = append(out, tk)
	}
	return out
}

// statementStart reports whether a Ruby keyword at pos begins a statement
// (`if x` or `y = if x`) rather than being a modifier (`return if x`).
func (t *text) statementStart(pos int) bool {
	code, col := t.lineCode(pos)
	p := strings.TrimSpace(code[:col])
	return p == "" || strings.ContainsRune("=([{,;|&!?", rune(p[len(p)-1]))
}

// matchEnd returns the offset of the token closing the block opened by
// toks[k].
func matchEnd(toks []rtok, k int, last int) (int, bool) {
	var stack []tokRole
	for j := k; j < len(toks); j++ {
		switch toks[j].role {
		case roleOpenKw, roleOpenBrace:
			stack = append(stack, toks[j].role)
		case roleCloseKw, roleCloseBrace:
			want := roleOpenKw
			if toks[j].role == roleCloseBrace {
				want = roleOpenBrace
			}
			// Pop to the matching opener, tolerating stray tokens.
			for len(stack) > 0 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if top == want {
					break
				}
			}
		}
		if len(stack) == 0 {
			return toks[j].pos, true
		}
	}
	return last, false
}

func findKeywordBlocks(t *text) []raw {
	toks := t.tokens()
	ruby := t.lang == lang.Ruby
	sep := " > "
	var suites []scope
	type test struct {
		r   raw
		pos int
	}
	var tests []test
	ti := 0 // first token at or after the current line
	for i, ll := range t.lines {
		for ti < len(toks) && toks[ti].pos < t.off[i] {
			ti++
		}
		code := ll.Code
		lineEnd := t.off[i] + len(code)
		var kind callKind
		var head string
		var headEnd int // offset where the arguments start
		var defTok = -1
		if ruby {
			if m := rubyHead.FindStringSubmatchIndex(code); m != nil {
				head = code[m[2]:m[3]]
				headEnd = t.off[i] + m[3]
				switch strings.TrimLeft(head, "xf") {
				case "describe", "context", "feature", "shared_examples", "shared_examples_for", "shared_context":
					kind = callSuite
				default:
					kind = callTest
				}
			} else if m := rubyDef.FindStringSubmatchIndex(code); m != nil {
				kind, head = callTest, code[m[2]:m[3]]
				for k := ti; k < len(toks) && toks[k].pos < lineEnd; k++ {
					if toks[k].word == "def" && toks[k].role == roleOpenKw {
						defTok = k
						break
					}
				}
			} else if m := rubyCls.FindStringSubmatchIndex(code); m != nil {
				kind, head = callSuite, code[m[2]:m[3]]
				for k := ti; k < len(toks) && toks[k].pos < lineEnd; k++ {
					if toks[k].word == "class" && toks[k].role == roleOpenKw {
						defTok = k
						break
					}
				}
			}
		} else if m := elixirHead.FindStringSubmatchIndex(code); m != nil {
			head = code[m[2]:m[3]]
			headEnd = t.off[i] + m[3]
			kind = callTest
			if head == "describe" {
				kind = callSuite
			}
		}
		if kind == callNone {
			continue
		}
		// The block opener: `def`/`class`, else the first do or { on the line.
		opener := defTok
		for k := ti; opener < 0 && k < len(toks) && toks[k].pos < lineEnd; k++ {
			if toks[k].pos >= headEnd && (toks[k].role == roleOpenBrace || toks[k].role == roleOpenKw && toks[k].word == "do") {
				opener = k
			}
		}
		name := ""
		if defTok >= 0 {
			name = head
		} else {
			name = t.keywordArgName(headEnd, lineEnd)
		}
		skipped := strings.HasPrefix(head, "x") || head == "pending" || rubySkipMeta.MatchString(ll.NoComment)
		if !ruby && kind == callTest && t.elixirSkipTag(i) {
			skipped = true
		}
		hl := i
		if opener < 0 {
			if kind == callTest && name != "" {
				// Elixir's one-line `test "x", do: assert ...`; otherwise a
				// test without a body: it 'is pending' / test "later".
				inline := !ruby && strings.Contains(code, "do:")
				tests = append(tests, test{raw{name: name, bare: name, start: hl, header: hl, end: hl,
					skipped: skipped || !inline}, t.off[i]})
			}
			continue
		}
		endPos, closed := matchEnd(toks, opener, len(t.code)-1)
		if name == "" { // it { is_expected.to eq(1) }: named after its body
			name = strings.Join(strings.Fields(t.nocommentRange(i, t.lineOf(endPos))), " ")
		}
		if kind == callSuite {
			suites = append(suites, scope{name: name, from: toks[opener].pos, to: endPos, skipped: skipped})
			continue
		}
		// A first statement of `skip`/`pending` skips the example.
		if k := opener + 1; k < len(toks) && (toks[k].word == "skip" || toks[k].word == "pending") && toks[k].role == roleNone {
			skipped = true
		}
		tests = append(tests, test{raw{name: name, bare: name, start: hl, header: hl, end: t.lineOf(endPos),
			skipped: skipped, open: !closed}, t.off[i]})
	}
	sortScopes(suites)
	out := make([]raw, 0, len(tests))
	for _, ts := range tests {
		q, sk := qualify(suites, ts.pos, ts.r.bare, sep)
		ts.r.name, ts.r.skipped = q, ts.r.skipped || sk
		out = append(out, ts.r)
	}
	return out
}

// keywordArgName names a Ruby/Elixir test or suite after its first
// argument: a string literal, a symbol, a constant, or the raw code.
func (t *text) keywordArgName(pos, lineEnd int) string {
	i := pos
	for i < lineEnd && (t.code[i] == ' ' || t.code[i] == '\t' || t.code[i] == '(') {
		i++
	}
	if i >= lineEnd {
		return ""
	}
	if c := t.code[i]; c == '"' || c == '\'' {
		return t.literal(i)
	}
	if t.code[i] == '{' || strings.HasPrefix(t.code[i:lineEnd], "do") {
		return ""
	}
	j := i
	for j < lineEnd && !strings.ContainsRune(",){", rune(t.code[j])) && !strings.HasPrefix(t.code[j:lineEnd], " do") {
		j++
	}
	return strings.TrimSpace(t.code[i:j])
}

// elixirSkipTag reports a `@tag :skip` directly above line i, or a
// `@moduletag :skip` anywhere before it.
func (t *text) elixirSkipTag(i int) bool {
	for k := i - 1; k >= 0; k-- {
		c := strings.TrimSpace(t.lines[k].NoComment)
		if c == "" || strings.HasPrefix(c, "@") {
			if elixirTag.MatchString(c) {
				return true
			}
			continue
		}
		break
	}
	for k := 0; k < i; k++ {
		if c := strings.TrimSpace(t.lines[k].NoComment); strings.HasPrefix(c, "@moduletag") && elixirTag.MatchString(c) {
			return true
		}
	}
	return false
}

// nocommentRange joins the NoComment text of lines from..to.
func (t *text) nocommentRange(from, to int) string {
	var b strings.Builder
	for i := from; i <= to && i < len(t.lines); i++ {
		b.WriteString(t.lines[i].NoComment)
		b.WriteByte(' ')
	}
	return b.String()
}
