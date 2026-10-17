package ci

import (
	"path"
	"regexp"
	"strings"
)

// shellCmd is one simple command of a shell command list, e.g. "pytest -q"
// in "cd app && pytest -q || true".
type shellCmd struct {
	// Words are the command's words with quotes removed. Redirections and
	// their targets are dropped.
	Words []string
	// Op is the operator that follows the command: "&&", "||", ";", "|",
	// "&", or "" for the last command.
	Op string
}

// splitShell splits shell text into simple commands. It is a tolerant
// approximation of POSIX shell syntax: quotes, escapes, $(...) and backticks
// are respected, subshell and group delimiters are ignored, and a '#' that
// starts a word begins a comment.
func splitShell(s string) []shellCmd {
	var (
		out   []shellCmd
		words []string
		word  strings.Builder
		inW   bool // a word is in progress (possibly empty, e.g. "")
		redir bool // the next word is a redirection target
	)
	endWord := func() {
		if !inW {
			return
		}
		w := word.String()
		word.Reset()
		inW = false
		if redir {
			redir = false
			return
		}
		if isRedirection(w) {
			// "2>&1" or ">out.log" carry their target; a bare ">" does not.
			redir = strings.TrimLeft(w, "0123456789&<>") == "" && !strings.HasSuffix(w, "&1") && !strings.HasSuffix(w, "&2")
			return
		}
		words = append(words, w)
	}
	endCmd := func(op string) {
		endWord()
		redir = false
		if len(words) > 0 {
			out = append(out, shellCmd{Words: words, Op: op})
		} else if op != "" && len(out) > 0 && out[len(out)-1].Op == "" {
			// "(a) || b": the operator after a closing paren belongs to
			// the command before it.
			out[len(out)-1].Op = op
		}
		words = nil
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\'':
			inW = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				word.WriteRune(rs[j])
				j++
			}
			i = j
		case c == '"':
			inW = true
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				if rs[j] == '\\' && j+1 < len(rs) {
					j++
				}
				word.WriteRune(rs[j])
				j++
			}
			i = j
		case c == '\\' && i+1 < len(rs):
			inW = true
			if rs[i+1] != '\n' {
				word.WriteRune(rs[i+1])
			}
			i++
		case c == '$' && i+2 < len(rs) && rs[i+1] == '{' && rs[i+2] == '{':
			// A GitHub/Forgejo expression, ${{ matrix.pkg }}: one word part.
			inW = true
			j := i + 3
			for j+1 < len(rs) && !(rs[j] == '}' && rs[j+1] == '}') {
				j++
			}
			j = min(j+2, len(rs))
			word.WriteString(string(rs[i:j]))
			i = j - 1
		case c == '$' && i+1 < len(rs) && rs[i+1] == '(' || c == '`':
			// Command substitution stays inside the current word.
			inW = true
			j := skipSubst(rs, i)
			word.WriteString(string(rs[i:j]))
			i = j - 1
		case c == '#' && !inW:
			// Comment to end of line.
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			i--
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		case c == '\n':
			endCmd(";")
		case c == ';':
			endCmd(";")
		case c == '&' && i+1 < len(rs) && rs[i+1] == '&':
			endCmd("&&")
			i++
		case c == '|' && i+1 < len(rs) && rs[i+1] == '|':
			endCmd("||")
			i++
		case c == '|':
			endCmd("|")
		case c == '&' && (word.Len() > 0 && strings.HasSuffix(word.String(), ">") || i+1 < len(rs) && rs[i+1] == '>'):
			// Part of a redirection such as 2>&1 or &>file.
			inW = true
			word.WriteRune(c)
		case c == '&':
			endCmd("&")
		case c == '(' || c == ')':
			endWord()
		case c == '{' && !inW && (i+1 == len(rs) || rs[i+1] == ' ' || rs[i+1] == '\t' || rs[i+1] == '\n'):
			// "{ cmd; }" group; "{posargs}" or "{a,b}" stay words
			endWord()
		case c == '}' && !inW:
			endWord()
		default:
			inW = true
			word.WriteRune(c)
		}
	}
	endCmd("")
	// The last command's operator is always "".
	if n := len(out); n > 0 && out[n-1].Op == ";" {
		out[n-1].Op = ""
	}
	return out
}

// skipSubst returns the index just past the command substitution starting
// at rs[i] ("$(" or "`").
func skipSubst(rs []rune, i int) int {
	if rs[i] == '`' {
		for j := i + 1; j < len(rs); j++ {
			if rs[j] == '`' {
				return j + 1
			}
		}
		return len(rs)
	}
	depth := 0
	for j := i + 1; j < len(rs); j++ {
		switch rs[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j + 1
			}
		}
	}
	return len(rs)
}

var redirRe = regexp.MustCompile(`^(\d*>>?|\d*<|&>>?|\d*>&\d*|<<<?)`)

func isRedirection(w string) bool { return redirRe.MatchString(w) }

// joinWords renders words back into a readable command line.
func joinWords(ws []string) string {
	out := make([]string, len(ws))
	for i, w := range ws {
		if w == "" || strings.ContainsAny(w, " \t") {
			w = `"` + w + `"`
		}
		out[i] = w
	}
	return strings.Join(out, " ")
}

// invocation is a simple command with environment assignments and wrapper
// commands (sudo, npx, poetry run, python -m, ...) peeled off.
type invocation struct {
	Env  map[string]string // leading NAME=value assignments (including env/cross-env ones)
	Args []string          // Args[0] is the normalized program name
	Word string            // the program word as written ("./scripts/test.sh")
	Raw  []string          // the original words
	// Keywords are the shell reserved words in front of the command:
	// "if" and "!" in `if ! pytest; then`, "then" in `then exit 0`.
	Keywords []string
}

// leadingKeywords are reserved words that can precede a simple command.
var leadingKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "while": true, "until": true, "do": true, "!": true,
}

// has reports whether the invocation is preceded by keyword k.
func (iv invocation) has(k string) bool {
	for _, w := range iv.Keywords {
		if w == k {
			return true
		}
	}
	return false
}

// Prog returns the normalized program name, or "".
func (iv invocation) Prog() string {
	if len(iv.Args) == 0 {
		return ""
	}
	return iv.Args[0]
}

// String renders the invocation as it was written.
func (iv invocation) String() string { return joinWords(iv.Raw) }

var envAssignRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// wrapperFlagsWithValue lists, per wrapper, flags that consume the next word.
var wrapperFlagsWithValue = map[string]map[string]bool{
	"sudo":     {"-u": true, "-g": true, "-C": true},
	"nice":     {"-n": true},
	"timeout":  {"-s": true, "-k": true, "--signal": true, "--kill-after": true},
	"xvfb-run": {"-s": true, "--server-args": true, "-n": true, "--server-num": true, "-e": true, "--error-file": true, "-f": true},
	"env":      {"-u": true, "--unset": true, "-C": true, "--chdir": true},
	"env-cmd":  {"-f": true, "--file": true, "-e": true, "--environments": true},
	"dotenv":   {"-e": true, "-v": true, "-c": true},
	"npx":      {"-p": true, "--package": true, "-c": true, "--call": true},
	"uv":       {"--with": true, "--extra": true, "--group": true, "--python": true, "-p": true, "--package": true, "--directory": true, "--project": true, "--env-file": true, "--with-requirements": true},
	"uvx":      {"--from": true, "--with": true, "--python": true, "-p": true},
	"conda":    {"-n": true, "--name": true, "-p": true, "--prefix": true},
	"pixi":     {"-e": true, "--environment": true, "--manifest-path": true},
	"nix":      {},
	"bundle":   {"--gemfile": true},
}

// dockerFlagsWithValue are docker/compose run/exec flags that consume the
// next word.
var dockerFlagsWithValue = map[string]bool{
	"-e": true, "--env": true, "--env-file": true, "-w": true, "--workdir": true, "-u": true, "--user": true,
	"-v": true, "--volume": true, "--entrypoint": true, "-p": true, "--publish": true, "--name": true,
	"--network": true, "--platform": true, "-l": true, "--label": true, "--mount": true, "--pull": true,
	"-f": true, "--file": true, "--project-name": true, "--profile": true,
}

// makeVars maps Make/just variables that conventionally hold a tool to the
// tool's name, so "$(PYTHON) -m pytest" reads like "python -m pytest".
var makeVars = map[string]string{
	"python": "python", "python3": "python", "py": "python", "pytest": "pytest", "go": "go", "gotest": "go",
	"npm": "npm", "yarn": "yarn", "pnpm": "pnpm", "bun": "bun", "npx": "npx", "node": "node", "cargo": "cargo",
	"poetry": "poetry", "uv": "uv", "make": "make", "gradle": "gradle", "mvn": "mvn", "maven": "mvn",
	"dotnet": "dotnet", "php": "php", "composer": "composer", "bundle": "bundle", "rake": "rake",
	"tox": "tox", "mypy": "mypy", "ruff": "ruff", "eslint": "eslint", "jest": "jest", "vitest": "vitest",
	"golangci_lint": "golangci-lint", "golangcilint": "golangci-lint", "phpunit": "phpunit", "phpstan": "phpstan",
}

var makeVarRe = regexp.MustCompile(`^\$[({]([A-Za-z_][A-Za-z0-9_]*)[)}]$`)

// progName normalizes a program word: directories, extensions and version
// suffixes are removed and wrapper scripts are mapped to their tool
// ("./gradlew" → "gradle", "python3.12" → "python").
func progName(w string) string {
	if m := makeVarRe.FindStringSubmatch(w); m != nil {
		if t, ok := makeVars[strings.ToLower(m[1])]; ok {
			return t
		}
		return strings.ToLower(m[1])
	}
	w = strings.ReplaceAll(w, `\`, "/")
	base := strings.ToLower(path.Base(w))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		base = strings.TrimSuffix(base, ext)
	}
	switch {
	case base == "gradlew":
		return "gradle"
	case base == "mvnw":
		return "mvn"
	case base == "py.test":
		return "pytest"
	case base == "py" || strings.HasPrefix(base, "python") && strings.Trim(base[len("python"):], "0123456789.") == "":
		return "python"
	case strings.HasPrefix(base, "pip") && strings.Trim(base[len("pip"):], "0123456789.") == "":
		return "pip"
	case base == "docker-compose":
		return "docker"
	}
	return base
}

// parseInvocation peels environment assignments and wrapper commands off a
// simple command.
func parseInvocation(words []string) invocation {
	iv := invocation{Env: map[string]string{}, Raw: words}
	ws := words
	for len(ws) > 0 && leadingKeywords[ws[0]] {
		iv.Keywords = append(iv.Keywords, ws[0])
		ws = ws[1:]
	}
	for guard := 0; len(ws) > 0 && guard < 16; guard++ {
		if m := envAssignRe.FindStringSubmatch(ws[0]); m != nil {
			iv.Env[m[1]] = m[2]
			ws = ws[1:]
			continue
		}
		prog := progName(ws[0])
		rest, ok := unwrap(prog, ws[1:], iv.Env)
		if !ok {
			break
		}
		ws = rest
	}
	if len(ws) > 0 {
		iv.Word = ws[0]
		iv.Args = append([]string{progName(ws[0])}, ws[1:]...)
		// "python -m pytest" → "pytest"
		if iv.Args[0] == "python" {
			if mod, rest := pythonModule(iv.Args[1:]); mod != "" {
				iv.Args = append([]string{mod}, rest...)
			}
		}
	}
	return iv
}

// unwrap removes one wrapper command (with its options) and returns the
// wrapped command's words. ok is false when prog is not a wrapper.
func unwrap(prog string, rest []string, env map[string]string) ([]string, bool) {
	skipFlags := func(ws []string, withValue map[string]bool) []string {
		for len(ws) > 0 && strings.HasPrefix(ws[0], "-") {
			if ws[0] == "--" {
				return ws[1:]
			}
			f := ws[0]
			ws = ws[1:]
			if withValue[f] && len(ws) > 0 {
				ws = ws[1:]
			}
		}
		return ws
	}
	assigns := func(ws []string) []string {
		for len(ws) > 0 {
			m := envAssignRe.FindStringSubmatch(ws[0])
			if m == nil {
				break
			}
			env[m[1]] = m[2]
			ws = ws[1:]
		}
		return ws
	}
	switch prog {
	case "command":
		if len(rest) > 0 && (rest[0] == "-v" || rest[0] == "-V") {
			return nil, false // `command -v pytest` looks a program up; it does not run it
		}
		ws := skipFlags(rest, nil)
		return ws, len(ws) > 0
	case "sudo", "time", "exec", "nohup", "nice", "stdbuf", "xvfb-run", "xvfb-maybe", "catchsegv", "dotenv", "env-cmd":
		ws := skipFlags(rest, wrapperFlagsWithValue[prog])
		return ws, len(ws) > 0
	case "env", "cross-env", "cross-env-shell":
		ws := assigns(skipFlags(rest, wrapperFlagsWithValue["env"]))
		return ws, len(ws) > 0
	case "timeout":
		ws := skipFlags(rest, wrapperFlagsWithValue[prog])
		if len(ws) > 1 {
			return ws[1:], true // drop the duration
		}
		return nil, false
	case "npx", "pnpx", "bunx":
		ws := skipFlags(rest, wrapperFlagsWithValue["npx"])
		return ws, len(ws) > 0
	case "uvx":
		ws := skipFlags(rest, wrapperFlagsWithValue[prog])
		return ws, len(ws) > 0
	case "npm", "pnpm", "yarn", "bun":
		// "npm exec jest", "pnpm exec vitest", "pnpm dlx x", "yarn dlx x", "bun x x"
		ws := skipFlags(rest, pmFlagsWithValue)
		if len(ws) > 1 && (ws[0] == "exec" || ws[0] == "dlx" || ws[0] == "x") {
			ws = skipFlags(ws[1:], wrapperFlagsWithValue["npx"])
			return ws, len(ws) > 0
		}
		return nil, false
	case "uv", "poetry", "pipenv", "pdm", "rye", "pixi", "bundle", "conda", "micromamba", "mamba":
		// "<tool> run|exec <cmd>"
		ws := skipFlags(rest, wrapperFlagsWithValue[prog])
		if len(ws) > 1 && (ws[0] == "run" || ws[0] == "exec" && (prog == "bundle" || prog == "pixi")) {
			ws = skipFlags(ws[1:], wrapperFlagsWithValue[prog])
			return ws, len(ws) > 0
		}
		return nil, false
	case "hatch":
		// "hatch run [env:]cmd"
		ws := skipFlags(rest, nil)
		if len(ws) > 1 && ws[0] == "run" {
			ws = append([]string{}, ws[1:]...)
			if i := strings.LastIndex(ws[0], ":"); i >= 0 {
				ws[0] = ws[0][i+1:]
			}
			return ws, true
		}
		return nil, false
	case "nix":
		for i, w := range rest {
			if w == "-c" || w == "--command" {
				return rest[i+1:], i+1 < len(rest)
			}
		}
		return nil, false
	case "docker", "podman":
		// docker run [opts] IMAGE CMD..., docker compose run|exec [opts] SERVICE CMD...
		ws := skipFlags(rest, dockerFlagsWithValue)
		if len(ws) > 0 && ws[0] == "compose" {
			ws = skipFlags(ws[1:], dockerFlagsWithValue)
		}
		if len(ws) == 0 || ws[0] != "run" && ws[0] != "exec" {
			return nil, false
		}
		ws = skipFlags(ws[1:], dockerFlagsWithValue)
		if len(ws) > 1 {
			return ws[1:], true // drop the image/service
		}
		return nil, false
	case "direnv":
		if len(rest) > 2 && rest[0] == "exec" {
			return rest[2:], true
		}
		return nil, false
	case "node":
		// "node node_modules/.bin/jest" → "jest"; "node --test" is a runner.
		ws := skipFlags(rest, nil)
		if len(ws) > 0 && strings.Contains(ws[0], "node_modules/") {
			return ws, true
		}
		return nil, false
	case "php":
		// "php vendor/bin/phpunit" → "phpunit"
		ws := skipFlags(rest, nil)
		if len(ws) > 0 && (strings.Contains(ws[0], "vendor/bin/") || strings.Contains(ws[0], "/bin/")) {
			return ws, true
		}
		return nil, false
	}
	return nil, false
}

// pythonModule returns the module run by "python [opts] -m module args".
func pythonModule(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-m" && i+1 < len(args):
			return args[i+1], args[i+2:]
		case strings.HasPrefix(a, "-m") && len(a) > 2:
			return a[2:], args[i+1:]
		case a == "-W" || a == "-X":
			i++ // option with a value
		case strings.HasPrefix(a, "-"):
		default:
			return "", nil
		}
	}
	return "", nil
}

// flagValue reports whether args contain flag (as "--flag", "--flag=v" or
// "--flag v") and returns its value ("" when it has none).
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if a == flag {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(a, flag+"=") {
			return a[len(flag)+1:], true
		}
	}
	return "", false
}

// hasArg reports whether any argument equals one of want.
func hasArg(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}
