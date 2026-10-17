package ci

import (
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// entry is one scalar setting of a structured configuration file, e.g.
// compilerOptions.strict = true in tsconfig.json.
type entry struct {
	// Path holds the keys from the document root. List elements share
	// their list's path, so ["a","b"] under "exclude" are two entries with
	// Path ["exclude"] and Item set.
	Path  []string
	Value string // scalar text with quotes removed
	Line  int    // 1-based line of the value
	Item  bool   // the value is a list element (or an INI continuation line)
	// Group identifies the innermost object that is a list element (an
	// ESLint flat-config block, a [[tool.mypy.overrides]] table, a
	// pre-commit hook), so settings can be read next to their siblings.
	// 0 means not inside such an object.
	Group int
}

// Name renders the path for messages: "compilerOptions.strict".
func (e entry) Name() string { return strings.Join(e.Path, ".") }

// Last returns the last path segment.
func (e entry) Last() string {
	if len(e.Path) == 0 {
		return ""
	}
	return e.Path[len(e.Path)-1]
}

// configFormat is how a configuration file is parsed.
type configFormat int

const (
	fmtNone configFormat = iota
	fmtJSON              // JSON, JSONC and JavaScript/TypeScript object literals
	fmtYAML
	fmtTOML
	fmtINI
)

// formatOf picks the parser for a file.
func formatOf(f *core.FileInfo) configFormat {
	switch f.Lang {
	case lang.JSON, lang.JavaScript, lang.TypeScript:
		return fmtJSON
	case lang.YAML:
		return fmtYAML
	case lang.TOML:
		return fmtTOML
	case lang.INI:
		return fmtINI
	}
	switch path.Ext(strings.ToLower(f.Path)) {
	case ".neon":
		return fmtYAML
	}
	return fmtNone
}

// configSides parses the old and new versions of a configuration file.
// A side is nil when the file does not exist on it. ok is false when the
// file's full content is not available (a bare patch), in which case
// callers fall back to line heuristics.
func configSides(f *core.FileInfo) (old, new []entry, ok bool) {
	format := formatOf(f)
	if format == fmtNone {
		return nil, nil, false
	}
	if f.Diff.Kind != diff.Added {
		src, lines, ok := wholeContent(f, true)
		if !ok {
			return nil, nil, false
		}
		old = parseConfig(format, src, lines)
	}
	if f.Diff.Kind != diff.Deleted {
		src, lines, ok := wholeContent(f, false)
		if !ok {
			return nil, nil, false
		}
		new = parseConfig(format, src, lines)
	}
	return old, new, true
}

// wholeContent returns the full text and lexed lines of one side of f.
// Without full contents (a bare patch) it still succeeds for a file that
// was added or deleted outright, whose single hunk holds every line.
func wholeContent(f *core.FileInfo, old bool) (string, []lang.LexedLine, bool) {
	if old && f.HasOld() {
		raw, _ := f.OldContent()
		return string(raw), f.OldLines(), true
	}
	if !old && f.HasNew() {
		raw, _ := f.NewContent()
		return string(raw), f.NewLines(), true
	}
	whole := old && f.Diff.Kind == diff.Deleted || !old && f.Diff.Kind == diff.Added
	if !whole {
		return "", nil, false
	}
	s := fragmentSide(f, old)
	for _, k := range s.known {
		if !k {
			return "", nil, false
		}
	}
	return s.text(), s.lines, true
}

// parseConfig parses content in the given format. It never fails: invalid
// input yields whatever entries could be recovered.
func parseConfig(format configFormat, src string, lines []lang.LexedLine) []entry {
	switch format {
	case fmtJSON:
		p := &objParser{toks: tokenize(lines, false)}
		p.top()
		return p.out
	case fmtTOML:
		p := &objParser{toks: tokenize(lines, true), toml: true}
		p.tomlTop()
		return p.out
	case fmtYAML:
		if es, ok := yamlEntries(src); ok {
			return es
		}
		return yamlLineEntries(lines)
	case fmtINI:
		return iniEntries(lines)
	}
	return nil
}

// ---- JSON / JavaScript object literals / TOML values ----

type tok struct {
	kind byte // one of {}[]():,=; '\n', 's' (string) or 'w' (word)
	text string
	line int
}

// tokenize splits comment-free source into tokens. Strings keep their
// contents without quotes and stop at the end of the line; in TOML mode
// triple-quoted strings may span lines and '...' strings have no escapes.
func tokenize(lines []lang.LexedLine, toml bool) []tok {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.NoComment)
	}
	s := []rune(b.String())
	var out []tok
	ln := 1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\n':
			out = append(out, tok{kind: '\n', line: ln})
			ln++
		case c == ' ' || c == '\t' || c == '\r':
		case strings.ContainsRune("{}[]():,=;", c):
			out = append(out, tok{kind: byte(c), text: string(c), line: ln})
		case c == '"' || c == '\'' || c == '`':
			start := ln
			var v strings.Builder
			if toml && i+2 < len(s) && s[i+1] == c && s[i+2] == c {
				j := i + 3
				for j < len(s) && !(s[j] == c && j+2 < len(s) && s[j+1] == c && s[j+2] == c) {
					if s[j] == '\n' {
						ln++
					}
					v.WriteRune(s[j])
					j++
				}
				out = append(out, tok{kind: 's', text: v.String(), line: start})
				i = j + 2
				continue
			}
			escapes := !toml || c == '"'
			j := i + 1
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if escapes && s[j] == '\\' && j+1 < len(s) && s[j+1] != '\n' {
					j++
				}
				v.WriteRune(s[j])
				j++
			}
			out = append(out, tok{kind: 's', text: v.String(), line: start})
			i = j
			if j < len(s) && s[j] == '\n' {
				i = j - 1 // let the newline be tokenized
			}
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\r\n{}[]():,=;\"'`", s[j]) {
				j++
			}
			out = append(out, tok{kind: 'w', text: string(s[i:j]), line: ln})
			i = j - 1
		}
	}
	return out
}

// objParser is a tolerant parser for nested object/array literals. It
// understands JSON, JSONC, the object literals of JS/TS config files
// (module.exports = {...}, defineConfig({...}), export default [...]) and
// TOML documents.
type objParser struct {
	toks   []tok
	i      int
	out    []entry
	toml   bool
	group  int // group of the object being parsed
	groups int // groups allocated so far
}

func (p *objParser) peek() tok {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return tok{}
}

func (p *objParser) skipNL() {
	for p.i < len(p.toks) && p.toks[p.i].kind == '\n' {
		p.i++
	}
}

func (p *objParser) emit(pth []string, v string, line int, item bool) {
	p.out = append(p.out, entry{Path: append([]string(nil), pth...), Value: v, Line: line, Item: item, Group: p.group})
}

func with(pth []string, k string) []string {
	return append(append([]string(nil), pth...), k)
}

// top parses a whole JSON/JS document: every object or array literal that
// is not part of another value is parsed with the root path, and bare
// "key: value" pairs (as in a diff fragment) are read too.
func (p *objParser) top() {
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		switch t.kind {
		case '{':
			p.groupedObject(nil)
		case '[':
			p.array(nil)
		case 's', 'w':
			p.i++
			p.skipNL()
			if p.peek().kind == ':' && t.kind == 's' {
				p.i++
				p.value([]string{t.text}, false)
			}
		default:
			p.i++
		}
	}
}

func (p *objParser) object(pth []string) {
	p.i++ // '{'
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		switch t.kind {
		case '}':
			p.i++
			return
		case 's', 'w':
			if strings.HasPrefix(t.text, "...") {
				p.skipValue() // spread
				continue
			}
			p.i++
			p.skipNL()
			switch n := p.peek(); {
			case n.kind == ':' || p.toml && n.kind == '=':
				p.i++
				p.value(with(pth, t.text), false)
			case n.kind == '(':
				// method shorthand: name(...) { body }
				p.skipBalanced()
				p.skipNL()
				if p.peek().kind == '{' {
					p.skipBalanced()
				}
			}
		case '[':
			// computed key: [expr]: value
			p.skipBalanced()
			if p.peek().kind == ':' {
				p.i++
				p.skipValue()
			}
		default:
			p.i++
		}
	}
}

// groupedObject parses an object that starts a new entry group.
func (p *objParser) groupedObject(pth []string) {
	save := p.group
	p.groups++
	p.group = p.groups
	p.object(pth)
	p.group = save
}

func (p *objParser) array(pth []string) {
	p.i++ // '['
	for p.i < len(p.toks) {
		switch p.peek().kind {
		case ']':
			p.i++
			return
		case ',', '\n':
			p.i++
		case '}', ')':
			return // unbalanced input
		case '{':
			p.groupedObject(pth) // an object element gets its own group
		default:
			before := p.i
			p.value(pth, true)
			if p.i == before {
				p.i++
			}
		}
	}
}

// value parses one value. Objects and arrays recurse; anything else is read
// as a scalar expression up to the next separator. Object literals nested in
// expressions (defineConfig({...}), cond ? {...} : {...}) are parsed with
// the same path.
func (p *objParser) value(pth []string, item bool) {
	p.skipNL()
	switch p.peek().kind {
	case '{':
		p.object(pth)
		return
	case '[':
		p.array(pth)
		return
	}
	var parts []string
	line, single, isStr := 0, true, false
	depth := 0
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if depth == 0 && strings.IndexByte(",}];)\n", t.kind) >= 0 {
			break
		}
		switch t.kind {
		case '(':
			depth++
		case ')':
			depth--
		case '\n':
			p.i++
			continue
		case '{':
			p.groupedObject(pth) // defineConfig({...}), cond ? {...} : {...}
			single = false
			continue
		case '[':
			p.skipBalanced()
			single = false
			continue
		}
		if line == 0 {
			line = t.line
		}
		if len(parts) > 0 {
			single = false
		}
		isStr = t.kind == 's'
		parts = append(parts, t.text)
		p.i++
	}
	if len(parts) == 0 {
		return
	}
	v := strings.Join(parts, " ")
	if single && isStr {
		v = parts[0]
	}
	p.emit(pth, v, line, item)
}

// skipValue skips tokens up to the next ',' or closing bracket at depth 0.
func (p *objParser) skipValue() {
	depth := 0
	for p.i < len(p.toks) {
		switch p.toks[p.i].kind {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			if depth == 0 {
				return
			}
			depth--
		case ',':
			if depth == 0 {
				p.i++
				return
			}
		}
		p.i++
	}
}

// skipBalanced skips a bracketed group starting at the current token.
func (p *objParser) skipBalanced() {
	depth := 0
	for p.i < len(p.toks) {
		switch p.toks[p.i].kind {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
			if depth <= 0 {
				p.i++
				return
			}
		}
		p.i++
	}
}

// tomlTop parses a TOML document: [table] / [[array.of.tables]] headers and
// dotted key = value lines.
func (p *objParser) tomlTop() {
	var table []string
	for p.i < len(p.toks) {
		t := p.peek()
		switch t.kind {
		case '\n':
			p.i++
		case '[':
			p.i++
			p.group = 0
			if p.peek().kind == '[' {
				// [[array.of.tables]]: each instance is a group
				p.groups++
				p.group = p.groups
			}
			for p.peek().kind == '[' {
				p.i++
			}
			var keys []string
			for p.i < len(p.toks) && p.peek().kind != ']' && p.peek().kind != '\n' {
				keys = append(keys, keyParts(p.peek())...)
				p.i++
			}
			table = keys
			p.skipLine()
		case 's', 'w':
			var keys []string
			for p.i < len(p.toks) && p.peek().kind != '=' && p.peek().kind != '\n' {
				keys = append(keys, keyParts(p.peek())...)
				p.i++
			}
			if p.peek().kind != '=' {
				continue
			}
			p.i++
			p.value(append(append([]string(nil), table...), keys...), false)
			p.skipLine()
		default:
			p.i++
		}
	}
}

func (p *objParser) skipLine() {
	for p.i < len(p.toks) && p.toks[p.i].kind != '\n' {
		p.i++
	}
}

// keyParts splits a TOML key token: bare words on dots, quoted keys whole.
func keyParts(t tok) []string {
	if t.kind == 's' {
		return []string{t.text}
	}
	var out []string
	for _, k := range strings.Split(t.text, ".") {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// ---- YAML ----

// yamlEntries parses YAML (all documents) with gopkg.in/yaml.v3.
func yamlEntries(src string) ([]entry, bool) {
	dec := yaml.NewDecoder(strings.NewReader(src))
	w := &yamlWalker{}
	ok := false
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			break
		}
		ok = true
		w.walk(&n, nil, false)
	}
	return w.out, ok || strings.TrimSpace(src) == ""
}

type yamlWalker struct {
	out           []entry
	group, groups int
}

func (w *yamlWalker) walk(n *yaml.Node, pth []string, item bool) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			w.walk(c, pth, false)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value == "<<" {
				continue
			}
			w.walk(v, with(pth, k.Value), false)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind == yaml.MappingNode {
				save := w.group
				w.groups++
				w.group = w.groups
				w.walk(c, pth, true)
				w.group = save
				continue
			}
			w.walk(c, pth, true)
		}
	case yaml.ScalarNode:
		w.out = append(w.out, entry{Path: append([]string(nil), pth...), Value: n.Value, Line: n.Line, Item: item, Group: w.group})
	}
}

// yamlLineEntries is an indentation-based fallback for YAML that yaml.v3
// rejects (templated files, fragments). It understands block mappings,
// block sequences and one-line flow sequences, which covers configuration
// files in practice.
func yamlLineEntries(lines []lang.LexedLine) []entry {
	type frame struct {
		indent int
		key    string
	}
	var (
		stack []frame
		out   []entry
	)
	keys := func() []string {
		ks := make([]string, len(stack))
		for i, f := range stack {
			ks[i] = f.key
		}
		return ks
	}
	pop := func(indent int, inclusive bool) {
		for n := len(stack); n > 0 && (stack[n-1].indent > indent || inclusive && stack[n-1].indent == indent); n = len(stack) {
			stack = stack[:n-1]
		}
	}
	for i, l := range lines {
		text := strings.TrimRight(expandTabs(l.NoComment), " \t")
		body := strings.TrimLeft(text, " ")
		if body == "" || body == "---" || body == "..." {
			continue
		}
		indent := len(text) - len(body)
		item := false
		if body == "-" || strings.HasPrefix(body, "- ") {
			// A list may sit at its parent key's indentation ("key:\n- a"),
			// so only deeper frames end here.
			item = true
			pop(indent, false)
			body = strings.TrimSpace(body[1:])
			indent += 2
			if body == "" {
				continue
			}
		} else {
			pop(indent, true)
		}
		k, v, isKV := splitYAMLKey(body)
		switch {
		case !isKV:
			out = append(out, entry{Path: keys(), Value: unquoteYAML(body), Line: i + 1, Item: item})
		case v == "" || strings.Trim(v, "|>-+") == "":
			stack = append(stack, frame{indent: indent, key: k})
		case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
			for _, it := range strings.Split(v[1:len(v)-1], ",") {
				if it = unquoteYAML(it); it != "" {
					out = append(out, entry{Path: append(keys(), k), Value: it, Line: i + 1, Item: true})
				}
			}
		default:
			out = append(out, entry{Path: append(keys(), k), Value: unquoteYAML(v), Line: i + 1})
		}
	}
	return out
}

// expandTabs turns leading tabs into four spaces (NEON files, such as
// phpstan.neon, indent with tabs).
func expandTabs(s string) string {
	i := 0
	for i < len(s) && (s[i] == '\t' || s[i] == ' ') {
		i++
	}
	return strings.ReplaceAll(s[:i], "\t", "    ") + s[i:]
}

// groupValues returns the values of entries in group g whose path ends
// with key.
func groupValues(es []entry, g int, key string) []string {
	var out []string
	if g == 0 {
		return nil
	}
	for _, e := range es {
		if e.Group == g && pathEnds(e.Path, key) {
			out = append(out, e.Value)
		}
	}
	return out
}

// splitYAMLKey splits "key: value" (the key may be quoted).
func splitYAMLKey(s string) (key, val string, ok bool) {
	if strings.HasPrefix(s, `"`) || strings.HasPrefix(s, `'`) {
		q := s[:1]
		if j := strings.Index(s[1:], q); j >= 0 {
			rest := s[j+2:]
			if strings.HasPrefix(rest, ":") {
				return s[1 : j+1], strings.TrimSpace(rest[1:]), true
			}
		}
		return "", "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ':' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
			k := strings.TrimSpace(s[:i])
			if k == "" || strings.ContainsAny(k, "{}[]") {
				return "", "", false
			}
			return k, strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// ---- INI (setup.cfg, tox.ini, .coveragerc, .flake8, mypy.ini, ...) ----

// iniEntries parses INI files. Multi-line values become one entry per
// continuation line, with Item set.
func iniEntries(lines []lang.LexedLine) []entry {
	var (
		out     []entry
		section string
		cur     []string
	)
	for i, l := range lines {
		text := strings.TrimRight(l.NoComment, " \t\r")
		trim := strings.TrimSpace(text)
		if trim == "" {
			continue
		}
		if cur != nil && (text[0] == ' ' || text[0] == '\t') {
			out = append(out, entry{Path: cur, Value: trim, Line: i + 1, Item: true})
			continue
		}
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			section = strings.TrimSpace(trim[1 : len(trim)-1])
			cur = nil
			continue
		}
		j := strings.IndexAny(trim, "=:")
		if j <= 0 {
			cur = nil
			continue
		}
		cur = []string{section, strings.TrimSpace(trim[:j])}
		if v := strings.TrimSpace(trim[j+1:]); v != "" {
			out = append(out, entry{Path: cur, Value: v, Line: i + 1})
		}
	}
	return out
}

// ---- querying entries ----

// normKey canonicalizes a key for comparison: case and -/_ differences are
// ignored (flake8 accepts both extend-ignore and extend_ignore).
func normKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "_", "-"))
}

// pathEnds reports whether p, with keys normalized, ends with suffix.
func pathEnds(p []string, suffix ...string) bool {
	if len(suffix) > len(p) {
		return false
	}
	off := len(p) - len(suffix)
	for i, s := range suffix {
		if s != "*" && normKey(p[off+i]) != normKey(s) {
			return false
		}
	}
	return true
}

// indexOf returns the index of the first path segment equal to k
// (normalized), or -1.
func indexOf(p []string, k string) int {
	for i, s := range p {
		if normKey(s) == normKey(k) {
			return i
		}
	}
	return -1
}
