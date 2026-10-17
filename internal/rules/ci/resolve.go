package ci

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/lang"
)

// Entry points such as `npm run ci`, `make all`, `just check`,
// `./scripts/ci.sh` and `uses: ./.github/actions/verify` hide what they run
// behind a name. When the definition is readable — in the change, or in
// the repository at the new revision for files the change does not touch
// (core.Analysis.ReadNew) — ci-check-removed looks inside instead of
// trusting the name: `make all` that runs the tests covers a removed
// `pytest` step, and `npm run verify` whose script is only `tsc` does not
// cover a removed `jest` step. Definitions that run something narc cannot
// classify fall back to the name, as before.

// maxExpandDepth bounds nested entry points (a script calling a target
// calling a script ...).
const maxExpandDepth = 6

// repoFiles reads files at either revision, changed or not.
type repoFiles struct {
	a     *core.Analysis
	byNew map[string]*core.FileInfo // changed files by new path
	byOld map[string]*core.FileInfo // changed files by old path
}

type repoKey struct{}

// repoOf returns the analysis's file reader (memoized).
func repoOf(a *core.Analysis) *repoFiles {
	return a.Memo(repoKey{}, func() any {
		r := &repoFiles{a: a, byNew: map[string]*core.FileInfo{}, byOld: map[string]*core.FileInfo{}}
		for _, f := range a.Files {
			if f.Diff.Kind != diff.Deleted {
				r.byNew[f.Path] = f
			}
			if f.OldPath != "" {
				r.byOld[f.OldPath] = f
			}
		}
		return r
	}).(*repoFiles)
}

// doc is one revision of a file prepared for command extraction: the
// changed file itself, or a stand-in for a file the change does not touch.
type doc struct {
	f *core.FileInfo
	s side
}

// unchangedKey memoizes the lexed content of an unchanged file.
type unchangedKey struct{ path string }

// get returns file p at the old or the new revision. Sides that are only
// known from hunks (a bare patch) are not returned: a partial definition
// would look like one that runs less than it does.
func (r *repoFiles) get(p string, old bool) (doc, bool) {
	if p == "" || p == unknownDir {
		return doc{}, false
	}
	if old {
		if f, ok := r.byOld[p]; ok {
			s := oldSide(f)
			return doc{f, s}, s.full
		}
		if f, ok := r.byNew[p]; ok && (f.Diff.Kind == diff.Added || f.OldPath != p) {
			return doc{}, false // the change created it (or renamed it here)
		}
	} else {
		if f, ok := r.byNew[p]; ok {
			s := newSide(f)
			return doc{f, s}, s.full
		}
		if _, ok := r.byOld[p]; ok {
			return doc{}, false // deleted or renamed away
		}
	}
	// Unchanged: both revisions read the same.
	d := r.a.Memo(unchangedKey{p}, func() any {
		b, ok := r.a.ReadNew(p)
		if !ok {
			return (*doc)(nil)
		}
		f := standIn(p)
		return &doc{f: f, s: fullSide(lang.Lex(f.Lang, string(b)))}
	}).(*doc)
	if d == nil {
		return doc{}, false
	}
	return *d, true
}

// standIn builds a FileInfo for a file the change does not touch, enough
// for kindOf and commandLines.
func standIn(p string) *core.FileInfo {
	return &core.FileInfo{
		Diff:    &diff.FileDiff{OldPath: p, NewPath: p, Kind: diff.Modified},
		Path:    p,
		OldPath: p,
		Lang:    lang.Detect(p),
		IsCI:    core.IsCIPath(p),
		Config:  core.ConfigKind(p),
	}
}

// unknownDir marks a path or working directory that cannot be determined
// statically (cd "$DIR", working-directory: ${{ matrix.dir }}).
const unknownDir = "\x00"

// joinDir resolves the relative path rel against the repository-relative
// directory dir ("" is the root). It returns unknownDir for paths that
// depend on variables or leave the repository.
func joinDir(dir, rel string) string {
	if dir == unknownDir || rel == "" || strings.ContainsAny(rel, "$`*?{}") ||
		strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") {
		return unknownDir
	}
	p := path.Clean(path.Join(dir, rel))
	switch {
	case p == ".":
		return ""
	case p == ".." || strings.HasPrefix(p, "../"):
		return unknownDir
	}
	return p
}

// runs is what an entry point was found to run.
type runs struct {
	checks []check
	// found is set when the definition was read.
	found bool
	// opaque is set when the definition runs something that neither
	// classifies nor is known to verify nothing (a custom tool, a Python
	// helper script): its name is then trusted instead.
	opaque bool
}

// expander resolves entry points at one revision.
type expander struct {
	r     *repoFiles
	old   bool
	depth int
	seen  map[string]bool
}

func newExpander(a *core.Analysis, old bool) *expander {
	return &expander{r: repoOf(a), old: old, seen: map[string]bool{}}
}

// resolve returns the checks an invocation in directory dir runs: what its
// definition runs when that can be read and understood, else what its name
// says (classifyAll). understood reports the former.
func (x *expander) resolve(iv invocation, dir string) (cs []check, understood bool) {
	x.seen = map[string]bool{}
	r := x.entry(iv, dir)
	if r.found && !r.opaque {
		return r.checks, true
	}
	return append(r.checks, classifyAll(iv)...), false
}

// entry reads the definition behind an invocation.
func (x *expander) entry(iv invocation, dir string) runs {
	if x.depth >= maxExpandDepth || dir == unknownDir {
		return runs{}
	}
	switch iv.Prog() {
	case "npm", "pnpm", "yarn", "bun":
		return x.pmScript(iv.Prog(), iv.Args[1:], dir)
	case "make", "gmake":
		return x.makeTargets(iv.Prog(), iv.Args[1:], dir, false)
	case "just":
		return x.makeTargets("just", iv.Args[1:], dir, true)
	case "source", ".":
		// a sourced script runs in the same shell: read it like one run by path
		if p := firstPositional(iv.Args[1:]); p != "" {
			return x.script(joinDir(dir, p), dir)
		}
		return runs{}
	}
	if _, runner := taskRunnerEco[iv.Prog()]; runner {
		return runs{} // ./gradlew, ./mvnw: the wrapper script is not the build
	}
	if p := opaqueEntryPoint(iv); p != "" {
		return x.script(joinDir(dir, p), dir)
	}
	return runs{}
}

// body resolves the commands of a script, recipe or step run in dir. A
// `cd` lasts until the end of its script (a shell file, a CI step), not
// past it: every Makefile recipe line and package script runs on its own.
func (x *expander) body(cmds []cmdLine, dir string) runs {
	out := runs{found: true}
	funcs := shellFunctions(cmds)
	walkCommands(cmds, func(cmdLine) string { return dir }, func(_ cmdLine, iv invocation, cur string) {
		if funcs[iv.Prog()] {
			return // a function of the same script: its body is read as lines
		}
		n := x.nested(iv, cur)
		out.checks = append(out.checks, n.checks...)
		out.opaque = out.opaque || n.opaque
	})
	return out
}

// walkCommands calls fn for every invocation of cmds with the directory it
// runs in: start(c) for the first line of each script, then wherever a
// `cd` in that script leads. The cd invocations themselves are not passed.
func walkCommands(cmds []cmdLine, start func(cmdLine) string, fn func(c cmdLine, iv invocation, dir string)) {
	cur, block := "", 0
	for i, c := range cmds {
		if i == 0 || c.block() != block {
			cur, block = start(c), c.block()
		}
		for _, iv := range invocations(c.Text) {
			if iv.Prog() == "cd" {
				cur = cdInto(cur, iv)
				continue
			}
			fn(c, iv, cur)
		}
	}
}

// nested resolves one command of a body.
func (x *expander) nested(iv invocation, dir string) runs {
	byName := classifyAll(iv)
	x.depth++
	sub := x.entry(iv, dir)
	x.depth--
	switch {
	case sub.found && !sub.opaque:
		return runs{checks: sub.checks}
	case len(byName) > 0:
		// The name vouches for what cannot be read: ./scripts/test.sh,
		// pytest, `npm run lint` without a package.json in sight.
		return runs{checks: append(sub.checks, byName...)}
	case sub.found:
		return runs{checks: sub.checks, opaque: true}
	case inert(iv):
		return runs{}
	}
	return runs{opaque: true}
}

// cdInto follows `cd dir`.
func cdInto(dir string, iv invocation) string {
	ps := positionals(iv.Args[1:])
	if len(ps) == 0 {
		return "" // cd: back to the repository root (in CI)
	}
	return joinDir(dir, ps[0])
}

// shellFunctionRe matches a shell function definition: `run_tests() {` or
// `function lint {`.
var shellFunctionRe = regexp.MustCompile(`^\s*(?:function\s+)?([A-Za-z_][\w:-]*)\s*\(\s*\)|^\s*function\s+([A-Za-z_][\w:-]*)`)

// shellFunctions returns the functions that cmds define.
func shellFunctions(cmds []cmdLine) map[string]bool {
	out := map[string]bool{}
	for _, c := range cmds {
		if m := shellFunctionRe.FindStringSubmatch(c.Text); m != nil {
			out[strings.ToLower(m[1]+m[2])] = true
		}
	}
	return out
}

// inertProgs never run verification and never hide it: shell built-ins,
// file and package management, builds and code generation.
var inertProgs = map[string]bool{
	"": true, "echo": true, "printf": true, "true": true, ":": true, "cd": true, "pushd": true, "popd": true,
	"rm": true, "rmdir": true, "mkdir": true, "cp": true, "mv": true, "ln": true, "touch": true, "cat": true,
	"export": true, "set": true, "unset": true, "sleep": true, "chmod": true, "chown": true, "wait": true,
	"trap": true, "shift": true, "local": true, "declare": true, "readonly": true,
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true, "while": true, "until": true,
	"do": true, "done": true, "case": true, "esac": true, "in": true, "function": true, "return": true, "exit": true,
	"[": true, "[[": true, "]]": true, "test": true, "tee": true, "date": true, "env": true, "which": true,
	"curl": true, "wget": true, "tar": true, "unzip": true, "zip": true, "gzip": true, "git": true,
	"rimraf": true, "del-cli": true, "shx": true, "mkdirp": true, "ls": true, "pwd": true, "basename": true,
	"dirname": true, "realpath": true, "head": true, "tail": true, "sort": true, "uniq": true, "wc": true,
	"webpack": true, "rollup": true, "esbuild": true, "tsup": true, "babel": true, "swc": true, "parcel": true,
	"vite": true, "next": true, "nuxt": true, "nuxi": true, "astro": true, "remix": true, "svelte-kit": true,
	"prisma": true, "protoc": true, "buf": true, "openapi-generator": true, "graphql-codegen": true,
	"pip": true, "pipx": true, "twine": true, "build": true, "venv": true, "virtualenv": true, "maturin": true,
	"gem": true, "mkdocs": true, "sphinx-build": true, "hugo": true, "jekyll": true, "typedoc": true,
	"kubectl": true, "helm": true, "terraform": true, "aws": true, "gcloud": true,
	"husky": true, "patch-package": true, "node-gyp": true, "corepack": true, "nvm": true, "rustup": true,
	"apt-get": true, "apt": true, "brew": true, "apk": true, "yum": true, "sudo": true,
}

// inertSubcommands are subcommands of verification-capable tools that only
// build, install or generate.
var inertSubcommands = map[string]map[string]bool{
	"go":       {"build": true, "install": true, "generate": true, "mod": true, "get": true, "env": true, "version": true, "work": true},
	"cargo":    {"build": true, "fetch": true, "install": true, "doc": true, "update": true, "generate-lockfile": true},
	"dotnet":   {"build": true, "restore": true, "publish": true, "pack": true, "tool": true},
	"poetry":   {"install": true, "lock": true, "build": true, "sync": true},
	"uv":       {"sync": true, "pip": true, "lock": true, "venv": true, "build": true},
	"pdm":      {"install": true, "sync": true, "lock": true, "build": true},
	"bundle":   {"install": true, "config": true},
	"composer": {"install": true, "dump-autoload": true, "update": true},
	"mix":      {"deps.get": true, "compile": true},
	"swift":    {"build": true},
	"flutter":  {"pub": true, "build": true},
	"dart":     {"pub": true, "compile": true},
	"deno":     {"cache": true, "compile": true, "install": true},
	// docker build can run a Dockerfile's test stage and compose up a test
	// service, so only image and registry housekeeping is inert.
	"docker": {"pull": true, "push": true, "login": true, "logout": true, "tag": true, "network": true, "volume": true},
	"podman": {"pull": true, "push": true, "login": true, "logout": true, "tag": true},
}

// activateRe matches sourced files that only set up the environment.
var activateRe = regexp.MustCompile(`(^[~/$])|(^|/)(activate|env|\.env[\w.-]*|[\w.-]*\.env|nvm\.sh|setup-env[\w.-]*)$`)

// inert reports whether a command neither verifies nor could.
func inert(iv invocation) bool {
	prog := iv.Prog()
	if w := iv.Word; strings.Contains(w, "/") && !strings.HasPrefix(w, "/bin/") && !strings.HasPrefix(w, "/usr/bin/") {
		return false // ./build or bin/setup is the project's own script, which may run anything
	}
	if inertProgs[prog] {
		return true
	}
	sub := firstPositional(iv.Args[min(1, len(iv.Args)):])
	if prog == "source" || prog == "." {
		// environment setup outside the repository (a virtualenv, nvm, a
		// generated .env file); a project script would have been read
		return sub == "" || activateRe.MatchString(sub)
	}
	if subs, ok := inertSubcommands[prog]; ok {
		return subs[sub]
	}
	switch prog {
	case "npm", "pnpm", "yarn", "bun":
		return pmBuiltins[sub] && sub != "exec" && sub != "dlx" && sub != "x" && sub != "start" && sub != "restart"
	case "gradle", "mvn":
		return len(classifyAll(iv)) == 0 // task lists are understood well enough
	case "tsc":
		return true // classified when it type-checks; --watch/--init only build
	}
	return false
}

// ---- package.json scripts ----

// pmScript resolves `npm test`, `npm run X`, `yarn X`, `pnpm X` against
// package.json in dir (or the --prefix/--cwd/--dir it names).
func (x *expander) pmScript(pm string, args []string, dir string) runs {
	name, dir, ok := pmScriptName(pm, args, dir)
	if !ok || dir == unknownDir {
		return runs{}
	}
	file := joinDir(dir, "package.json")
	scripts, ok := x.r.scripts(file, x.old)
	if !ok {
		return runs{}
	}
	main, ok := scripts[name]
	if !ok {
		return runs{}
	}
	key := file + "#" + name
	if x.seen[key] {
		return runs{found: true}
	}
	x.seen[key] = true
	// npm runs preX and postX around X.
	cmds := append(append(append([]cmdLine{}, scripts["pre"+name]...), main...), scripts["post"+name]...)
	return x.body(cmds, dir)
}

// defKey memoizes a parsed definition file at one revision.
type defKey struct {
	kind, path string
	old        bool
}

// scripts returns the package.json scripts of file by name (memoized).
func (r *repoFiles) scripts(file string, old bool) (map[string][]cmdLine, bool) {
	m := r.a.Memo(defKey{"scripts", file, old}, func() any {
		d, ok := r.get(file, old)
		if !ok {
			return map[string][]cmdLine(nil)
		}
		scripts := map[string][]cmdLine{}
		for _, c := range scriptCommands(d.f, d.s) {
			if !strings.Contains(c.Owner, " ") { // "husky pre-push" and lint-staged globs are not scripts
				scripts[c.Owner] = append(scripts[c.Owner], c)
			}
		}
		return scripts
	}).(map[string][]cmdLine)
	return m, m != nil
}

// makefile is a parsed Makefile or justfile.
type makefile struct {
	recipes map[string][]cmdLine
	targets map[string]int
	deps    map[string][]string
	goal    string // the default goal
}

// makefile returns the parsed Makefile/justfile at file (memoized).
func (r *repoFiles) makefile(file string, old, just bool) (*makefile, bool) {
	m := r.a.Memo(defKey{"make", file, old}, func() any {
		d, ok := r.get(file, old)
		if !ok {
			return (*makefile)(nil)
		}
		mf := &makefile{recipes: map[string][]cmdLine{}}
		for _, c := range makeCommands(d.s, just) {
			if c.Owner != "" {
				mf.recipes[c.Owner] = append(mf.recipes[c.Owner], c)
			}
		}
		mf.targets, mf.deps = makeTargetsAndDeps(d.s)
		mf.goal = defaultGoal(d.s, mf.targets, just)
		return mf
	}).(*makefile)
	return m, m != nil
}

// pmScriptName returns the package.json script a package-manager command
// runs, and the directory of that package.json. ok is false for commands
// that run something else, or scripts of workspaces that cannot be told
// apart (--filter, --workspace).
func pmScriptName(pm string, args []string, dir string) (name, at string, ok bool) {
	at = dir
	ws := args
	for len(ws) > 0 && strings.HasPrefix(ws[0], "-") {
		f, v, hasV := strings.Cut(ws[0], "=")
		switch f {
		case "--prefix", "-C", "--dir", "--cwd":
			if !hasV {
				if len(ws) < 2 {
					return "", "", false
				}
				v, ws = ws[1], ws[1:]
			}
			at = joinDir(at, v)
		case "-w", "--workspace", "--filter", "-F", "--workspaces", "-ws", "-r", "--recursive", "--if-present":
			return "", "", false
		default:
			if pmFlagsWithValue[f] && !hasV && len(ws) > 1 {
				ws = ws[1:]
			}
		}
		ws = ws[1:]
	}
	if len(ws) == 0 {
		return "", "", false
	}
	sub := ws[0]
	// npm reads its own flags anywhere before "--": `npm test --workspaces`
	// runs every workspace's script, not this one.
	for _, w := range ws[1:] {
		if w == "--" {
			break
		}
		if w == "--workspaces" || w == "-ws" || w == "-w" || w == "-r" || w == "--recursive" || w == "--if-present" ||
			strings.HasPrefix(w, "--workspace") || strings.HasPrefix(w, "--filter") {
			return "", "", false
		}
	}
	switch {
	case sub == "test" || sub == "t" || sub == "tst":
		if pm == "bun" {
			return "", "", false // bun's own test runner, not a script
		}
		name = "test"
	case sub == "run" || sub == "run-script" || sub == "rum" || sub == "urn":
		name = firstPositional(ws[1:])
	case pm != "npm" && !pmBuiltins[sub] && sub != "workspace" && sub != "workspaces":
		name = sub
	}
	return name, at, name != ""
}

// ---- Makefile and justfile targets ----

var defaultGoalRe = regexp.MustCompile(`^\.DEFAULT_GOAL\s*:?=\s*(\S+)`)

// makeTargets resolves `make [targets]` / `just [recipes]` in dir.
func (x *expander) makeTargets(prog string, args []string, dir string, just bool) runs {
	file := ""
	for i := 0; i < len(args); i++ {
		f, v, hasV := strings.Cut(args[i], "=")
		if !hasV && i+1 < len(args) {
			v = args[i+1]
		}
		switch f {
		case "-C", "--directory", "-d", "--working-directory":
			if f == "-d" && !just {
				continue // make -d is --debug
			}
			dir = joinDir(dir, v)
		case "-f", "--file", "--makefile", "--justfile":
			file = v
		default:
			continue
		}
		if !hasV {
			i++
		}
	}
	if dir == unknownDir {
		return runs{}
	}
	var names []string
	for _, p := range positionalsSkipping(args, taskRunnerFlags[prog]) {
		if !strings.Contains(p, "=") {
			names = append(names, p)
		}
	}
	candidates := []string{"GNUmakefile", "makefile", "Makefile"}
	if just {
		candidates = []string{"justfile", "Justfile", ".justfile"}
	}
	if file != "" {
		candidates = []string{file}
	}
	var mf *makefile
	for _, c := range candidates {
		var ok bool
		if mf, ok = x.r.makefile(joinDir(dir, c), x.old, just); ok {
			file = joinDir(dir, c)
			break
		}
	}
	if mf == nil {
		return runs{}
	}
	targets, deps := mf.targets, mf.deps
	if len(names) == 0 {
		if mf.goal == "" {
			return runs{}
		}
		names = []string{mf.goal}
	}
	out := runs{found: true}
	var expand func(t string) bool
	expand = func(t string) bool {
		if _, ok := targets[t]; !ok {
			return false // a pattern rule, an included file, or a typo: cannot tell
		}
		key := file + "#" + t
		if x.seen[key] {
			return true
		}
		x.seen[key] = true
		for _, dep := range deps[t] {
			if _, isTarget := targets[dep]; isTarget {
				if !expand(dep) {
					return false
				}
			}
		}
		r := x.body(mf.recipes[t], dir)
		out.checks = append(out.checks, r.checks...)
		out.opaque = out.opaque || r.opaque
		return true
	}
	for _, n := range names {
		if !expand(n) {
			return runs{}
		}
	}
	return out
}

// defaultGoal returns the target `make`/`just` runs without arguments.
func defaultGoal(s side, targets map[string]int, just bool) string {
	if !just {
		for i := 1; i <= len(s.lines); i++ {
			if l, ok := s.get(i); ok {
				if m := defaultGoalRe.FindStringSubmatch(l.NoComment); m != nil {
					return m[1]
				}
			}
		}
	}
	if just {
		if _, ok := targets["default"]; ok {
			return "default"
		}
	}
	type tl struct {
		name string
		line int
	}
	var ts []tl
	for t, n := range targets {
		if !strings.ContainsAny(t, "%$") {
			ts = append(ts, tl{t, n})
		}
	}
	sort.Slice(ts, func(i, j int) bool {
		return ts[i].line < ts[j].line || ts[i].line == ts[j].line && ts[i].name < ts[j].name
	})
	if len(ts) == 0 {
		return ""
	}
	return ts[0].name
}

// ---- scripts run by path ----

// script resolves a shell script run by path.
func (x *expander) script(p, dir string) runs {
	if p == "" || p == unknownDir {
		return runs{}
	}
	d, ok := x.r.get(p, x.old)
	if !ok || kindOf(d.f) != kindShell && !isShebangScript(d.s) {
		return runs{} // missing, or a Python/Node/Ruby script narc cannot read
	}
	if x.seen[p] {
		return runs{found: true}
	}
	x.seen[p] = true
	return x.body(commandLines(d.f, d.s), dir)
}

// ---- local actions and reusable workflows ----

// setupActionRe matches actions that set up the job (check out code,
// install toolchains, restore caches, move artifacts) without verifying.
// Actions that run arbitrary code (github-script, docker build) are not
// among them.
var setupActionRe = regexp.MustCompile(`^(actions/(checkout|cache|upload-artifact|download-artifact|labeler|stale)|[\w.-]+/setup-[\w.-]+|[\w.-]+/[\w.-]*-toolchain|[\w.-]+/action-setup|[\w.-]+/cache[\w.-]*)(@|$)`)

// localAction resolves `uses: ./path` (a composite action directory or a
// reusable workflow file) to the checks its steps run.
func (x *expander) localAction(ref string) runs {
	if x.depth >= maxExpandDepth {
		return runs{}
	}
	p := joinDir("", strings.SplitN(ref, "@", 2)[0])
	if p == "" || p == unknownDir {
		return runs{}
	}
	candidates := []string{p + "/action.yml", p + "/action.yaml"}
	if strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml") {
		candidates = []string{p}
	}
	for _, c := range candidates {
		d, ok := x.r.get(c, x.old)
		if !ok {
			continue
		}
		if x.seen[c] {
			return runs{found: true}
		}
		x.seen[c] = true
		var cmds []cmdLine
		for _, cl := range dropHeredocs(joinContinuations(yamlCommands(d.s))) {
			if !nonGating(d.s, cl.Line) {
				cmds = append(cmds, cl)
			}
		}
		out := runs{found: true}
		walkCommands(cmds, func(cl cmdLine) string { return workingDir(d.s, cl.Line) }, func(_ cmdLine, iv invocation, dir string) {
			n := x.nested(iv, dir)
			out.checks = append(out.checks, n.checks...)
			out.opaque = out.opaque || n.opaque
		})
		for n := 1; n <= len(d.s.lines); n++ {
			l, ok := d.s.get(n)
			if !ok {
				continue
			}
			nd, ok := parseYAMLLine(l, n)
			if !ok || normKey(nd.Key) != "uses" {
				continue
			}
			u := strings.ToLower(nd.Value)
			switch {
			case strings.HasPrefix(u, "./"):
				x.depth++
				sub := x.localAction(nd.Value)
				x.depth--
				out.checks = append(out.checks, sub.checks...)
				out.opaque = out.opaque || !sub.found || sub.opaque
			case isVerificationAction(u):
				for prefix, c := range actionCategories {
					if strings.HasPrefix(u, prefix) {
						out.checks = append(out.checks, c)
					}
				}
				if !knownActionCategory(u) {
					out.opaque = true // a verification action narc cannot categorize
				}
			case !setupActionRe.MatchString(u):
				out.opaque = true
			}
		}
		return out
	}
	return runs{}
}

func knownActionCategory(u string) bool {
	for prefix := range actionCategories {
		if strings.HasPrefix(u, prefix) {
			return true
		}
	}
	return false
}

// ---- unchanged pipelines ----

// unchangedPipelines holds the checks that pipelines the change does not
// touch still run on pull requests.
type unchangedPipelines struct {
	checks []presentCheck
	listed bool // the repository could be listed
}

type pipelinesKey struct{}

// unchangedChecks returns the verification that runs in CI files the
// change does not touch and that gate pull requests (memoized).
func unchangedChecks(a *core.Analysis) unchangedPipelines {
	return a.Memo(pipelinesKey{}, func() any {
		r := repoOf(a)
		paths, listed := pipelinePaths(a)
		u := unchangedPipelines{listed: listed}
		for _, p := range paths {
			if !core.IsCIPath(p) || r.byNew[p] != nil || r.byOld[p] != nil || isActionFile(p) {
				continue
			}
			d, ok := r.get(p, false)
			if !ok {
				continue
			}
			k := kindOf(d.f)
			if k != kindCIYAML && k != kindJenkins {
				continue
			}
			if isWorkflowPath(p) && !gatesPullRequests(d) {
				continue // a nightly, release or deploy workflow
			}
			x := newExpander(a, false)
			walkCI(d.f, d.s, gatingLines(d.f, d.s), func(_ cmdLine, iv invocation, dir string) {
				cs, _ := x.resolve(iv, dir)
				for _, ch := range cs {
					u.checks = append(u.checks, presentCheck{check: ch, targets: targets(iv), from: p})
				}
			})
			if k == kindCIYAML {
				for _, pc := range actionChecks(a, d.f, d.s) {
					pc.from = p
					u.checks = append(u.checks, pc)
				}
			}
		}
		return u
	}).(unchangedPipelines)
}

// ciDirs hold pipeline definitions; ciRootFiles are the pipeline files
// that live at the repository root.
var (
	ciDirs = []string{".github/workflows", ".gitlab", ".circleci", ".buildkite", ".woodpecker", ".azure-pipelines",
		".forgejo/workflows", ".gitea/workflows", ".semaphore", "tekton"}
	ciRootFiles = []string{".gitlab-ci.yml", ".gitlab-ci.yaml", "Jenkinsfile", "jenkinsfile", ".travis.yml",
		".drone.yml", "bitbucket-pipelines.yml", "azure-pipelines.yml", "azure-pipelines.yaml", "appveyor.yml",
		".woodpecker.yml", ".cirrus.yml", "cloudbuild.yaml", "cloudbuild.yml", "buildspec.yml", "codemagic.yaml"}
)

// pipelinePaths returns the repository's pipeline files at the new
// revision: listed from the CI directories (cheap pathspec listings, not
// the whole tree) plus the root-level pipeline files that exist. listed is
// false when the repository cannot be listed (a bare patch).
func pipelinePaths(a *core.Analysis) (paths []string, listed bool) {
	for _, d := range ciDirs {
		ps, ok := a.ListNew(d)
		listed = listed || ok
		paths = append(paths, ps...)
	}
	if !listed {
		return nil, false
	}
	for _, p := range ciRootFiles {
		if _, ok := a.ReadNew(p); ok {
			paths = append(paths, p)
		}
	}
	return paths, true
}

// isActionFile reports whether p defines a composite action, which runs
// only where a workflow uses it.
func isActionFile(p string) bool {
	b := path.Base(p)
	return b == "action.yml" || b == "action.yaml"
}

// gatesPullRequests reports whether a workflow runs on pull requests (or
// on pushes to every branch, which check pull request commits too).
func gatesPullRequests(d doc) bool {
	es := parseConfig(fmtYAML, d.s.text(), d.s.lines)
	trig := workflowTriggers(es)
	if len(trig) == 0 {
		return true // unparseable: assume it does
	}
	if _, mg := trig["merge_group"]; mg || runsOnPRs(trig) {
		return true
	}
	if _, push := trig["push"]; !push {
		return false
	}
	// on: push gates every branch unless it is limited to some branches
	// or to tags.
	limited := false
	for _, e := range es {
		if len(e.Path) >= 3 && (e.Path[0] == "on" || e.Path[0] == "true") && e.Path[1] == "push" {
			switch e.Path[2] {
			case "branches":
				if e.Value != "**" && e.Value != "*" {
					limited = true
				}
			case "tags", "paths":
				limited = true
			}
		}
	}
	return !limited
}

// walkCI is walkCommands for the commands of a CI file side, starting each
// script in its step's working directory.
func walkCI(f *core.FileInfo, s side, cmds []cmdLine, fn func(c cmdLine, iv invocation, dir string)) {
	start := func(cmdLine) string { return "" }
	if kindOf(f) == kindCIYAML {
		start = func(c cmdLine) string { return workingDir(s, c.Line) }
	}
	walkCommands(cmds, start, fn)
}

// gatingLines returns the command lines of a CI side that can fail the
// job (not in after_script).
func gatingLines(f *core.FileInfo, s side) []cmdLine {
	var out []cmdLine
	for _, c := range commandLines(f, s) {
		if kindOf(f) != kindCIYAML || !nonGating(s, c.Line) {
			out = append(out, c)
		}
	}
	return out
}

// workingDirRe matches a `working-directory:` setting.
var workingDirRe = regexp.MustCompile(`^\s*(?:-\s+)?working[-_]directory\s*:\s*(.+?)\s*$`)

// workingDir returns the directory a CI step's commands run in: its own
// `working-directory:`, else the job's or the workflow's `defaults.run`
// setting, else the repository root ("").
func workingDir(s side, n int) string {
	start, end := stepBlock(s, n)
	if dir, ok := workingDirIn(s, start, end, false); ok {
		return dir
	}
	job := jobBlock(s, n)
	if dir, ok := workingDirIn(s, job[0], job[1], true); ok {
		return dir
	}
	if dir, ok := workingDirIn(s, 1, len(s.lines), true); ok {
		return dir
	}
	return ""
}

// workingDirIn finds the first working-directory setting in lines
// start..end; with underDefaults, only one in a `defaults:` mapping.
func workingDirIn(s side, start, end int, underDefaults bool) (string, bool) {
	for i := start; i <= end; i++ {
		l, ok := s.get(i)
		if !ok {
			continue
		}
		m := workingDirRe.FindStringSubmatch(l.NoComment)
		if m == nil {
			continue
		}
		if underDefaults && !hasAncestor(s, i, "defaults") {
			continue
		}
		return joinDir("", unquoteYAML(m[1])), true
	}
	return "", false
}

// hasAncestor reports whether line n sits under a mapping key named key.
func hasAncestor(s side, n int, key string) bool {
	for _, a := range yamlAncestors(s, n) {
		if a.Key == key {
			return true
		}
	}
	return false
}
