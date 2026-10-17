package ci

import (
	"path"
	"regexp"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// side is one version (old or new) of a file as lexed lines. Without full
// content (a bare patch) only the lines that appear in hunks are known.
type side struct {
	lines []lang.LexedLine // lines[i] is line i+1
	known []bool
	full  bool
}

func oldSide(f *core.FileInfo) side {
	if f.Diff.Kind == diff.Added {
		return side{full: true}
	}
	if f.HasOld() {
		return fullSide(f.OldLines())
	}
	return fragmentSide(f, true)
}

func newSide(f *core.FileInfo) side {
	if f.Diff.Kind == diff.Deleted {
		return side{full: true}
	}
	if f.HasNew() {
		return fullSide(f.NewLines())
	}
	return fragmentSide(f, false)
}

func fullSide(ls []lang.LexedLine) side {
	known := make([]bool, len(ls))
	for i := range known {
		known[i] = true
	}
	return side{lines: ls, known: known, full: true}
}

func fragmentSide(f *core.FileInfo, old bool) side {
	var s side
	for _, h := range f.Diff.Hunks {
		for _, l := range h.Lines {
			n, get := l.NewNo, f.NewLine
			if old {
				n, get = l.OldNo, f.OldLine
			}
			if n <= 0 {
				continue
			}
			ll, ok := get(n)
			if !ok {
				continue
			}
			for len(s.lines) < n {
				s.lines = append(s.lines, lang.LexedLine{})
				s.known = append(s.known, false)
			}
			s.lines[n-1], s.known[n-1] = ll, true
		}
	}
	return s
}

// get returns line n (1-based).
func (s side) get(n int) (lang.LexedLine, bool) {
	if n < 1 || n > len(s.lines) || !s.known[n-1] {
		return lang.LexedLine{}, false
	}
	return s.lines[n-1], true
}

// text returns the full text of the side (unknown lines are empty).
func (s side) text() string {
	var b strings.Builder
	for _, l := range s.lines {
		b.WriteString(l.Raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// lineSet returns the line numbers of a file's added (or removed) lines.
func lineSet(ls []core.Line) map[int]bool {
	m := make(map[int]bool, len(ls))
	for _, l := range ls {
		m[l.No] = true
	}
	return m
}

// ---- command lines ----

// cmdLine is a line (or a run of continued lines) of a file that holds
// shell commands.
type cmdLine struct {
	Line, End int    // 1-based first and last physical line on its side
	Text      string // the shell text
	// Owner is the script, make target, tox section or task the command
	// belongs to, when the file format names it ("test", "testenv:lint").
	Owner string
	// IgnoreErr is set for a Makefile/justfile "-" recipe prefix or a tox
	// "- " command prefix: the command's exit status is ignored.
	IgnoreErr bool
	// Block identifies the script the line belongs to (a YAML block
	// scalar, a Jenkins sh '''...''' string, a whole shell file); 0 means
	// the line runs on its own (a Makefile recipe line, a package script).
	Block int
}

// block returns the script id of c: its Block, or its own line.
func (c cmdLine) block() int {
	if c.Block != 0 {
		return c.Block
	}
	return -c.Line
}

// overlaps reports whether any line of c is in set.
func (c cmdLine) overlaps(set map[int]bool) bool {
	for n := c.Line; n <= c.End; n++ {
		if set[n] {
			return true
		}
	}
	return false
}

// fileKind classifies the files that hold commands.
type fileKind int

const (
	kindNone      fileKind = iota
	kindCIYAML             // CI pipeline in YAML
	kindJenkins            // Jenkinsfile (Groovy)
	kindScripts            // package.json / composer.json / deno.json scripts
	kindMake               // Makefile, justfile
	kindTox                // tox.ini (and setup.cfg [tox:...])
	kindPyproject          // pyproject.toml task tables
	kindShell              // shell scripts, git hooks
	kindTaskYAML           // Taskfile.yml, .pre-commit-config.yaml, lefthook.yml
)

func kindOf(f *core.FileInfo) fileKind {
	base := strings.ToLower(path.Base(f.Path))
	switch {
	case f.IsCI && f.Lang == lang.YAML:
		return kindCIYAML
	case f.IsCI && (f.Lang == lang.Groovy || strings.Contains(base, "jenkinsfile")):
		return kindJenkins
	case f.Config == core.CfgPackageJSON || f.Config == core.CfgDeno || base == "composer.json":
		return kindScripts
	case f.Config == core.CfgMake:
		return kindMake
	case f.Config == core.CfgTox || f.Config == core.CfgSetupCfg:
		return kindTox
	case f.Config == core.CfgPyproject:
		return kindPyproject
	case f.Config == core.CfgPreCommit && f.Lang == lang.YAML, base == "taskfile.yml", base == "taskfile.yaml":
		return kindTaskYAML
	case f.Lang == lang.Shell, f.Config == core.CfgPreCommit && f.Lang == lang.Unknown:
		return kindShell
	}
	return kindNone
}

var shebangRe = regexp.MustCompile(`^#!.*\b(ba|z|da|k)?sh\b`)

// isShebangScript reports whether an extension-less file starts with a
// shell shebang.
func isShebangScript(s side) bool {
	l, ok := s.get(1)
	return ok && shebangRe.MatchString(l.Raw)
}

// commandLines extracts the shell commands of one side of a file.
func commandLines(f *core.FileInfo, s side) []cmdLine {
	var out []cmdLine
	switch kindOf(f) {
	case kindCIYAML, kindTaskYAML:
		out = yamlCommands(s)
	case kindJenkins:
		out = groovyCommands(s)
	case kindScripts:
		out = scriptCommands(f, s)
	case kindMake:
		out = makeCommands(s, strings.ToLower(path.Base(f.Path)) == "justfile")
	case kindTox:
		out = toxCommands(s)
	case kindPyproject:
		out = pyprojectCommands(s)
	case kindShell:
		out = shellLines(s)
	default:
		if f.Lang == lang.Unknown && isShebangScript(s) {
			out = shellLines(s)
		}
	}
	return dropHeredocs(joinContinuations(out))
}

// heredocRe finds a here-document operator and its delimiter: <<EOF,
// <<-'EOF', << "END".
var heredocRe = regexp.MustCompile(`<<-?\s*(?:'([A-Za-z_]\w*)'|"([A-Za-z_]\w*)"|\\?([A-Za-z_]\w*))`)

// heredocShells are programs that execute a here-document as commands.
var heredocShells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true, "ssh": true, "pwsh": true}

// dropHeredocs removes the bodies of here-documents, which are data (a
// file being written, a message) rather than commands, unless the
// here-document feeds a shell (`bash <<EOF`).
func dropHeredocs(cs []cmdLine) []cmdLine {
	var out []cmdLine
	delim, open := "", 0
	for _, c := range cs {
		if delim != "" && c.block() == open {
			if strings.TrimSpace(c.Text) == delim {
				delim = ""
			}
			continue
		}
		delim = "" // the script ended without the delimiter
		out = append(out, c)
		if strings.Contains(c.Text, "<<<") {
			continue // a here-string, on one line
		}
		m := heredocRe.FindStringSubmatch(c.Text)
		if m == nil {
			continue
		}
		feedsShell := false
		for _, sc := range splitShell(c.Text[:strings.Index(c.Text, m[0])]) {
			feedsShell = feedsShell || len(sc.Words) > 0 && heredocShells[progName(parseInvocation(sc.Words).Word)]
		}
		if !feedsShell {
			delim, open = m[1]+m[2]+m[3], c.block()
		}
	}
	return out
}

// joinContinuations merges commands continued with a trailing backslash.
// A backslash with nothing left to continue (the next line was a comment
// or blank) is dropped.
func joinContinuations(cs []cmdLine) []cmdLine {
	var out []cmdLine
	for _, c := range cs {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			if strings.HasSuffix(prev.Text, `\`) && c.Line == prev.End+1 {
				prev.Text = strings.TrimSuffix(prev.Text, `\`) + " " + c.Text
				prev.End = c.End
				continue
			}
		}
		out = append(out, c)
	}
	for i := range out {
		out[i].Text = strings.TrimSpace(strings.TrimSuffix(out[i].Text, `\`))
	}
	return out
}

func shellLines(s side) []cmdLine {
	var out []cmdLine
	for i := range s.lines {
		l, ok := s.get(i + 1)
		if !ok {
			continue
		}
		if t := strings.TrimSpace(l.NoComment); t != "" {
			out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: t, Block: 1})
		}
	}
	return out
}

// yamlCommandKeys are the YAML keys whose values are shell commands across
// CI systems (GitHub run, GitLab/Travis/Bitbucket script, CircleCI and
// Buildkite command, Drone/Woodpecker commands, Azure script/bash/pwsh,
// AppVeyor *_script, Taskfile cmds, pre-commit entry, lefthook run).
var yamlCommandKeys = map[string]bool{
	"run": true, "script": true, "scripts": true, "command": true, "commands": true, "cmd": true, "cmds": true,
	"before-script": true, "after-script": true, "before-install": true, "install": true,
	"inline": true, "inlinescript": true, "bash": true, "pwsh": true, "powershell": true, "sh": true,
	"entry": true, "ps": true,
}

func isCommandKey(k string) bool {
	k = normKey(k)
	return yamlCommandKeys[k] || strings.HasSuffix(k, "-script") || strings.HasSuffix(k, "-command")
}

// indentOf returns the number of leading spaces.
func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func isBlockIndicator(v string) bool {
	return v != "" && strings.Trim(v, "|>-+0123456789") == ""
}

// yamlCommands extracts commands from a YAML pipeline: inline values,
// block scalars and lists under command keys.
func yamlCommands(s side) []cmdLine {
	var (
		out       []cmdLine
		keyIndent = -1 // indentation of the command key whose block we are in
		scalar    bool // the block is a block scalar (run: |)
		folded    bool // lines of the block join into one command (run: >, plain multi-line scalars)
		itemInd   = -1 // inside a list block: indentation of a "- |" item scalar
		scalarAt  = 0  // line where the current scalar starts
	)
	// emit adds a command line, or appends it to the previous one when the
	// scalar is folded.
	emit := func(n int, text string) {
		if k := len(out); folded && k > 0 && out[k-1].Line >= scalarAt && out[k-1].End >= n-2 {
			out[k-1].Text += " " + text
			out[k-1].End = n
			return
		}
		out = append(out, cmdLine{Line: n, End: n, Text: text, Block: scalarAt})
	}
	for i := 0; i < len(s.lines); i++ {
		l, ok := s.get(i + 1)
		if !ok {
			keyIndent = -1
			continue
		}
		text := strings.TrimRight(l.NoComment, " \t")
		body := strings.TrimSpace(text)
		if body == "" {
			continue
		}
		indent := indentOf(text)
		if keyIndent >= 0 {
			inside := indent > keyIndent || !scalar && indent == keyIndent && strings.HasPrefix(body, "- ")
			if inside && scalar {
				emit(i+1, body)
				continue
			}
			if inside {
				switch {
				case itemInd >= 0 && indent > itemInd:
					emit(i+1, body)
					continue
				case strings.HasPrefix(body, "- ") || body == "-":
					item := strings.TrimSpace(strings.TrimPrefix(body, "-"))
					itemInd, folded, scalarAt = -1, false, i+1
					if isBlockIndicator(item) {
						itemInd, folded = indent, strings.HasPrefix(item, ">")
					} else if item != "" {
						out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: unquoteYAML(item), Block: scalarAt})
						folded = true // a plain item may continue on the next lines
					}
					continue
				default:
					if _, _, isKV := splitYAMLKey(body); !isKV {
						// continuation of a multi-line plain scalar
						emit(i+1, body)
						continue
					}
					// a mapping (CircleCI "- run:\n    name: x\n    command: y"):
					// leave the block and read the line normally
				}
			}
			keyIndent, itemInd = -1, -1
		}
		b, off := body, 0
		if strings.HasPrefix(b, "- ") {
			b, off = strings.TrimSpace(b[2:]), 2
		}
		if strings.HasPrefix(b, "{") && strings.HasSuffix(b, "}") {
			// a one-line flow mapping: - { name: Test, run: npm test }
			for _, kv := range splitFlowList(b[1 : len(b)-1]) {
				if k, v, ok := splitYAMLKey(kv); ok && isCommandKey(k) && v != "" {
					out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: unquoteYAML(v)})
				}
			}
			continue
		}
		k, v, isKV := splitYAMLKey(b)
		if !isKV || !isCommandKey(k) {
			continue
		}
		scalarAt = i + 1
		switch {
		case v == "":
			keyIndent, scalar, folded = indent+off, false, false
		case isBlockIndicator(v):
			keyIndent, scalar, folded = indent+off, true, strings.HasPrefix(v, ">")
		case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
			for _, it := range splitFlowList(v[1 : len(v)-1]) {
				out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: it})
			}
		default:
			out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: unquoteYAML(v), Block: scalarAt})
			keyIndent, scalar, folded = indent+off, false, true // plain scalar continuation lines fold
		}
	}
	return out
}

// splitFlowList splits a YAML flow sequence body on top-level commas.
func splitFlowList(s string) []string {
	var out []string
	var cur strings.Builder
	var q rune
	for _, r := range s {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			q = r
			cur.WriteRune(r)
		case r == ',':
			out = append(out, unquoteYAML(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if t := unquoteYAML(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

var groovyShRe = regexp.MustCompile(`\b(sh|bat|powershell|pwsh)\b\s*\(?\s*(?:script\s*:\s*)?('''|"""|'|")`)

// groovyCommands extracts sh/bat steps from a Jenkinsfile. Comments are
// ignored (NoComment keeps string literals, which hold the commands), so a
// comment that quotes an old `sh 'npm test || true'` is not a command.
func groovyCommands(s side) []cmdLine {
	var out []cmdLine
	closing, blockAt := "", 0
	for i := 0; i < len(s.lines); i++ {
		l, ok := s.get(i + 1)
		if !ok {
			closing = ""
			continue
		}
		raw := l.NoComment
		if closing != "" {
			if j := strings.Index(raw, closing); j >= 0 {
				raw, closing = raw[:j], ""
			}
			if t := strings.TrimSpace(raw); t != "" {
				out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: t, Block: blockAt})
			}
			continue
		}
		m := groovyShRe.FindStringSubmatchIndex(raw)
		if m == nil {
			continue
		}
		q := raw[m[4]:m[5]]
		rest := raw[m[5]:]
		if j := strings.Index(rest, q); j >= 0 {
			out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: rest[:j]})
			continue
		}
		if len(q) == 3 {
			closing, blockAt = q, i+1
			if t := strings.TrimSpace(rest); t != "" {
				out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: t, Block: blockAt})
			}
		}
	}
	return out
}

// scriptSections are the package.json-style keys whose entries are
// commands, mapped to how the owner is named.
var scriptSections = map[string]string{
	"scripts": "", "tasks": "", "husky.hooks": "husky ", "simple-git-hooks": "simple-git-hooks ", "lint-staged": "lint-staged ",
}

var jsonPairRe = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*"(.*)"\s*,?\s*$`)

// scriptCommands extracts scripts from package.json, composer.json and
// deno.json.
func scriptCommands(f *core.FileInfo, s side) []cmdLine {
	var out []cmdLine
	if !s.full {
		// Fragment: a "name": "value" line is a script unless the hunk
		// shows that it sits in another section.
		for i := range s.lines {
			if l, ok := s.get(i + 1); ok {
				if m := jsonPairRe.FindStringSubmatch(l.NoComment); m != nil {
					prefix, isCmd := fragmentSection(s, i+1)
					if isCmd {
						out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: unescapeJSON(m[2]), Owner: prefix + m[1]})
					}
				}
			}
		}
		return out
	}
	p := &objParser{toks: tokenize(s.lines, false)}
	p.top()
	for _, e := range p.out {
		if len(e.Path) < 2 {
			continue
		}
		for sec, prefix := range scriptSections {
			parts := strings.Split(sec, ".")
			if len(e.Path) < len(parts)+1 {
				continue
			}
			match := true
			for i, k := range parts {
				if e.Path[i] != k {
					match = false
					break
				}
			}
			if match {
				out = append(out, cmdLine{Line: e.Line, End: e.Line, Text: e.Value, Owner: prefix + e.Path[len(parts)]})
			}
		}
	}
	return out
}

// jsonOpenRe matches a line that opens a named object: `"scripts": {`.
var jsonOpenRe = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*\{\s*$`)

// fragmentSection finds the section of a package.json line in a fragment
// by walking up to the objects that enclose it (by indentation). It returns
// the owner prefix of the section (see scriptSections) and whether the
// section holds commands. When the enclosing object is not visible the line
// counts as a script.
func fragmentSection(s side, n int) (prefix string, isCmd bool) {
	l, _ := s.get(n)
	limit := indentOf(l.Raw)
	var keys []string // enclosing object keys, innermost first
	for i := n - 1; i >= 1 && limit > 0; i-- {
		pl, ok := s.get(i)
		if !ok {
			break
		}
		if ind := indentOf(pl.Raw); ind < limit && strings.TrimSpace(pl.Raw) != "" {
			m := jsonOpenRe.FindStringSubmatch(pl.NoComment)
			if m == nil {
				break // "{" of the document, or an array
			}
			keys = append(keys, m[1])
			limit = ind
		}
	}
	if len(keys) == 0 {
		return "", true
	}
	path := keys[0]
	if len(keys) > 1 {
		path = keys[1] + "." + keys[0]
	}
	for sec, p := range scriptSections {
		if path == sec || keys[0] == sec && !strings.Contains(sec, ".") {
			return p, true
		}
	}
	return "", false
}

// isHookOwner reports whether a package.json command owner is a git hook
// or lint-staged entry rather than an npm script.
func isHookOwner(owner string) bool {
	return strings.HasPrefix(owner, "husky ") || strings.HasPrefix(owner, "simple-git-hooks ") ||
		strings.HasPrefix(owner, "lint-staged ")
}

func unescapeJSON(s string) string { return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s) }

var (
	makeRuleRe    = regexp.MustCompile(`^([A-Za-z0-9_.%/${}()\- ]+?)\s*::?(?:[^=]|$)`)
	justRecipeRe  = regexp.MustCompile(`^@?([A-Za-z0-9_-]+)(?:\s+[^:=]*)?:(?:[^=]|$)`)
	makeAssignRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]+\s*(?:[:+?!]|::)?=`)
	recipePrefixR = regexp.MustCompile(`^[@+-]+`)
	// makeConditionalRe matches GNU make conditional directives.
	makeConditionalRe = regexp.MustCompile(`^\s*(ifeq|ifneq|ifdef|ifndef|else|endif)\b`)
)

// makeCommands extracts recipe lines from a Makefile or justfile, with the
// target they belong to.
func makeCommands(s side, just bool) []cmdLine {
	var out []cmdLine
	target := ""
	for i := range s.lines {
		l, ok := s.get(i + 1)
		if !ok {
			continue
		}
		raw := l.Raw
		if strings.TrimSpace(raw) == "" {
			continue
		}
		recipe := strings.HasPrefix(raw, "\t") || just && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t"))
		if recipe && target != "" {
			t := strings.TrimSpace(l.NoComment)
			if t == "" {
				continue
			}
			pre := recipePrefixR.FindString(t)
			body := strings.TrimSpace(t[len(pre):])
			if !just {
				body = strings.ReplaceAll(body, "$$", "$") // make's escape for a literal $
			}
			out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: body, Owner: target,
				IgnoreErr: strings.Contains(pre, "-")})
			continue
		}
		text := l.NoComment
		if !just && makeConditionalRe.MatchString(text) {
			continue // ifeq/else/endif inside a rule keep its recipe going
		}
		if makeAssignRe.MatchString(text) {
			target = ""
			continue
		}
		re := makeRuleRe
		if just {
			re = justRecipeRe
		}
		if m := re.FindStringSubmatch(text); m != nil {
			names := strings.Fields(m[1])
			target = ""
			for _, n := range names {
				if !strings.HasPrefix(n, ".") {
					target = n
					break
				}
			}
			// "target: deps ; command"
			if j := strings.Index(text, ";"); j >= 0 && !just {
				if t := strings.TrimSpace(text[j+1:]); t != "" {
					out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: t, Owner: target})
				}
			}
			continue
		}
		target = ""
	}
	return out
}

// toxCommands extracts the commands of tox environments.
func toxCommands(s side) []cmdLine {
	var out []cmdLine
	if !s.full {
		return shellLikeINI(s)
	}
	for _, e := range iniEntries(s.lines) {
		if len(e.Path) != 2 {
			continue
		}
		k := normKey(e.Path[1])
		if k != "commands" && k != "commands-pre" && k != "commands-post" {
			continue
		}
		t := e.Value
		ignore := strings.HasPrefix(t, "- ") || t == "-"
		t = strings.TrimSpace(strings.TrimPrefix(t, "-"))
		out = append(out, cmdLine{Line: e.Line, End: e.Line, Text: t, Owner: e.Path[0], IgnoreErr: ignore})
	}
	return out
}

// shellLikeINI is the fragment fallback for tox.ini: indented lines and
// "commands = ..." values are treated as commands.
func shellLikeINI(s side) []cmdLine {
	var out []cmdLine
	for i := range s.lines {
		l, ok := s.get(i + 1)
		if !ok {
			continue
		}
		t := l.NoComment
		if k, v, ok := strings.Cut(t, "="); ok && strings.HasPrefix(normKey(strings.TrimSpace(k)), "commands") {
			t = v
		} else if !strings.HasPrefix(t, " ") && !strings.HasPrefix(t, "\t") {
			continue
		}
		t = strings.TrimSpace(t)
		ignore := strings.HasPrefix(t, "- ")
		if t = strings.TrimSpace(strings.TrimPrefix(t, "-")); t != "" {
			out = append(out, cmdLine{Line: i + 1, End: i + 1, Text: t, IgnoreErr: ignore})
		}
	}
	return out
}

// pyprojectTaskTables are TOML tables of named tasks.
var pyprojectTaskTables = [][]string{
	{"tool", "poe", "tasks"}, {"tool", "pdm", "scripts"}, {"tool", "rye", "scripts"}, {"tool", "taskipy", "tasks"},
	{"tool", "hatch", "envs", "*", "scripts"},
}

// pyprojectCommands extracts task commands from pyproject.toml.
func pyprojectCommands(s side) []cmdLine {
	if !s.full {
		return nil
	}
	p := &objParser{toks: tokenize(s.lines, true), toml: true}
	p.tomlTop()
	var out []cmdLine
	for _, e := range p.out {
		for _, t := range pyprojectTaskTables {
			if len(e.Path) <= len(t) || !prefixMatch(e.Path, t) {
				continue
			}
			rest := e.Path[len(t):]
			// name = "cmd", name = ["cmd"], name.cmd = "cmd", name = { cmd = "..." }
			if len(rest) == 1 || len(rest) == 2 && (rest[1] == "cmd" || rest[1] == "shell" || rest[1] == "script") {
				out = append(out, cmdLine{Line: e.Line, End: e.Line, Text: e.Value, Owner: rest[0]})
			}
		}
	}
	return out
}

func prefixMatch(p, prefix []string) bool {
	if len(p) < len(prefix) {
		return false
	}
	for i, k := range prefix {
		if k != "*" && p[i] != k {
			return false
		}
	}
	return true
}

// ---- YAML structure (indentation based, works on fragments) ----

// yamlNode is a line of a YAML file seen as a node of the indentation tree.
type yamlNode struct {
	Line   int    // 1-based
	Indent int    // indentation of the key (for "- key:" lines, of the key after the dash)
	Dash   int    // indentation of the "-" for list items, else -1
	Key    string // key on the line, "" for a scalar list item
	Value  string
}

func parseYAMLLine(l lang.LexedLine, n int) (yamlNode, bool) {
	text := strings.TrimRight(l.NoComment, " \t")
	body := strings.TrimSpace(text)
	if body == "" || body == "---" {
		return yamlNode{}, false
	}
	nd := yamlNode{Line: n, Indent: indentOf(text), Dash: -1}
	if strings.HasPrefix(body, "- ") || body == "-" {
		nd.Dash = nd.Indent
		body = strings.TrimSpace(strings.TrimPrefix(body, "-"))
		nd.Indent += 2
	}
	if k, v, ok := splitYAMLKey(body); ok {
		nd.Key, nd.Value = k, unquoteYAML(v)
	} else {
		nd.Value = unquoteYAML(body)
	}
	return nd, true
}

// yamlBlock returns the lines of the mapping that contains the key on line
// n: the enclosing list item (a step) or the enclosing key's mapping (a
// job). For a key at the top level it returns the whole side.
func yamlBlock(s side, n int) (start, end int) {
	l, ok := s.get(n)
	if !ok {
		return n, n
	}
	nd, _ := parseYAMLLine(l, n)
	if nd.Dash >= 0 {
		return n, blockEnd(s, n, nd.Dash)
	}
	for i := n - 1; i >= 1; i-- {
		pl, ok := s.get(i)
		if !ok {
			return i + 1, blockEnd(s, n, nd.Indent-1)
		}
		p, ok := parseYAMLLine(pl, i)
		if !ok {
			continue
		}
		if p.Dash >= 0 && p.Dash < nd.Indent && p.Indent <= nd.Indent {
			return i, blockEnd(s, i, p.Dash)
		}
		if p.Dash < 0 && p.Indent < nd.Indent {
			return i, blockEnd(s, i, p.Indent)
		}
	}
	return 1, len(s.lines)
}

// stepBlock returns the lines of the list item (a CI step) that contains
// line n, however deeply nested ("path:" under a step's "with:"). Outside
// any list item it falls back to yamlBlock.
func stepBlock(s side, n int) (start, end int) {
	if l, ok := s.get(n); ok {
		if nd, ok := parseYAMLLine(l, n); ok && nd.Dash >= 0 {
			return n, blockEnd(s, n, nd.Dash)
		}
	}
	for _, a := range yamlAncestors(s, n) {
		if a.Dash >= 0 {
			return a.Line, blockEnd(s, a.Line, a.Dash)
		}
	}
	return yamlBlock(s, n)
}

// blockEnd returns the last line after line n that is indented more than
// indent (blank lines do not end a block).
func blockEnd(s side, n, indent int) int {
	end := n
	for i := n + 1; i <= len(s.lines); i++ {
		l, ok := s.get(i)
		if !ok {
			break
		}
		if strings.TrimSpace(l.NoComment) == "" {
			continue
		}
		if indentOf(strings.TrimRight(l.NoComment, " \t")) <= indent {
			break
		}
		end = i
	}
	return end
}

// yamlAncestors returns the key lines enclosing line n, innermost first.
func yamlAncestors(s side, n int) []yamlNode {
	anc, _ := yamlAncestorsTrunc(s, n)
	return anc
}

// yamlAncestorsTrunc is yamlAncestors that also reports whether the chain
// was cut short by a line the side does not know (a bare patch shows only
// hunks), so the outermost ancestor found may not be at the top level.
func yamlAncestorsTrunc(s side, n int) (out []yamlNode, truncated bool) {
	l, ok := s.get(n)
	if !ok {
		return nil, !s.full
	}
	cur, _ := parseYAMLLine(l, n)
	limit := cur.Indent
	if cur.Dash >= 0 {
		limit = cur.Dash
	}
	for i := n - 1; i >= 1 && limit > 0; i-- {
		pl, ok := s.get(i)
		if !ok {
			return out, true
		}
		p, ok := parseYAMLLine(pl, i)
		if !ok {
			continue
		}
		at := p.Indent
		if p.Dash >= 0 {
			at = p.Dash
		}
		if at < limit {
			out = append(out, p)
			limit = at
		}
	}
	return out, limit > 0 && !s.full
}

// gitlabReserved are top-level .gitlab-ci.yml keys that are not jobs.
var gitlabReserved = map[string]bool{
	"stages": true, "variables": true, "default": true, "include": true, "workflow": true, "image": true,
	"services": true, "cache": true, "before_script": true, "after_script": true,
}

// nonJobKeys are mapping keys that sit inside jobs and steps, so a bare
// patch that shows one as its outermost line does not show the job.
var nonJobKeys = map[string]bool{
	"steps": true, "with": true, "env": true, "strategy": true, "matrix": true, "services": true,
	"defaults": true, "container": true, "outputs": true, "permissions": true, "run": true, "script": true,
	"rules": true, "only": true, "except": true, "needs": true, "artifacts": true, "parameters": true,
	"variables": true, "on": true, "jobs": true, "stages": true, "workflows": true, "inputs": true,
}

// yamlOwner describes the job and step that line n belongs to, e.g.
// `job "test", step "Run unit tests"`.
func yamlOwner(s side, n int) (job, step string) {
	anc, truncated := yamlAncestorsTrunc(s, n)
	if l, ok := s.get(n); ok {
		if nd, ok := parseYAMLLine(l, n); ok && nd.Dash >= 0 {
			anc = append([]yamlNode{nd}, anc...)
		}
	}
	for i, a := range anc {
		if step == "" && a.Dash >= 0 {
			start, end := a.Line, blockEnd(s, a.Line, a.Dash)
			step = stepName(s, start, end, a.Indent)
		}
		if (a.Key == "jobs" || a.Key == "stages") && i > 0 && job == "" {
			job = anc[i-1].Key
			if c := anc[i-1]; c.Dash >= 0 {
				// Azure: jobs: - job: verify
				job = ""
				switch normKey(c.Key) {
				case "job", "deployment", "stage", "name", "template":
					job = c.Value
				}
			}
		}
		if a.Indent == 0 && a.Dash < 0 && job == "" && !gitlabReserved[a.Key] && a.Key != "jobs" && a.Key != "steps" && a.Key != "on" {
			job = a.Key
		}
	}
	if job == "" && truncated {
		// A bare patch hides "jobs:" above the hunk; the outermost visible
		// mapping key above the steps is the job ("  test-nightly:").
		// Only the outermost ancestor qualifies, and not when it is one of
		// a step's or job's own mappings ("with:", "env:").
		if len(anc) > 0 {
			if a := anc[len(anc)-1]; a.Dash < 0 && a.Key != "" && a.Value == "" && !gitlabReserved[a.Key] && !nonJobKeys[a.Key] {
				job = a.Key
			}
		}
	}
	return job, step
}

// stepName returns the name/displayName/label of the list item spanning
// start..end whose keys are at indent.
func stepName(s side, start, end, indent int) string {
	for i := start; i <= end; i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		nd, ok := parseYAMLLine(l, i)
		if !ok || nd.Indent != indent {
			continue
		}
		switch normKey(nd.Key) {
		case "name", "displayname", "label", "task":
			if nd.Value != "" && !isBlockIndicator(nd.Value) {
				return nd.Value
			}
		}
	}
	return ""
}

// describeOwner renders a job/step pair for messages.
func describeOwner(job, step string) string {
	switch {
	case job != "" && step != "":
		return `job "` + job + `", step "` + step + `"`
	case job != "":
		return `job "` + job + `"`
	case step != "":
		return `step "` + step + `"`
	}
	return ""
}
