package hook

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
	"github.com/tranhuuhuy297/narc/internal/git"
)

// workspace is the project a hook runs in.
type workspace struct {
	dir  string    // the agent's working directory
	root string    // repository root, or dir outside git
	repo *git.Repo // nil outside a git work tree
	cfg  *config.Config
}

// projectDirVars name the environment variables harnesses set to the
// project root, consulted when a payload has no cwd.
var projectDirVars = map[string][]string{
	"claude": {"CLAUDE_PROJECT_DIR"},
	"gemini": {"GEMINI_PROJECT_DIR", "GEMINI_CWD"},
	"cursor": {"CURSOR_PROJECT_DIR"},
}

// dir returns the directory the agent works in.
func (r *request) dir(env Env) string {
	if r.cwd != "" {
		return r.cwd
	}
	if len(r.roots) > 0 && r.roots[0] != "" {
		return r.roots[0]
	}
	for _, k := range projectDirVars[r.harness] {
		if v := env.Getenv(k); v != "" {
			return v
		}
	}
	wd, _ := env.Getwd()
	return wd
}

// openWorkspace opens the repository containing dir and loads .narc.yml.
// Outside git, pre-edit checks still work with dir as the root; other
// checks need a repository (needRepo).
func openWorkspace(dir string, needRepo bool) (*workspace, error) {
	if dir == "" {
		return nil, errors.New("cannot determine the working directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	w := &workspace{dir: abs, root: abs}
	repo, err := git.Open(abs)
	switch {
	case err == nil:
		w.repo, w.root = repo, repo.Root
	case needRepo:
		return nil, err
	}
	cfg, err := config.Load(w.root)
	if err != nil {
		w.close()
		return nil, &configError{err}
	}
	w.cfg = cfg
	return w, nil
}

// configError is narc's configuration failing to load. Hooks fail open on
// it, so narc checks nothing until it is fixed; a stop tells the human.
type configError struct{ err error }

func (e *configError) Error() string { return "config: " + e.err.Error() }
func (e *configError) Unwrap() error { return e.err }

func (w *workspace) close() {
	if w.repo != nil {
		w.repo.Close()
	}
}

func (w *workspace) run(cs *core.Changeset) *engine.Result {
	return engine.Run(cs, engine.Options{Config: w.cfg})
}

// neverSeverity is above every finding's severity: nothing reaches it.
const neverSeverity = core.Error + 1

// blockThreshold is the severity at which hooks block the agent.
// config.ParseSeverity reads "off" (and "none", "false", ...) as core.Off,
// which sorts below info, so `block_on: off` would block on every finding;
// it means "never block".
func (w *workspace) blockThreshold() core.Severity {
	return effectiveThreshold(w.cfg.BlockThreshold())
}

func effectiveThreshold(s core.Severity) core.Severity {
	if s == core.Off {
		return neverSeverity
	}
	return s
}

// checkPreEdit judges a tool call before it runs.
func checkPreEdit(req *request, env Env) (verdict, error) {
	if len(req.ops) == 0 {
		return verdict{}, nil
	}
	base := absDir(req.dir(env))
	w, err := openWorkspace(editRoot(req.ops, base), false)
	if err != nil {
		return verdict{}, err
	}
	defer w.close()
	edits := w.replay(req.ops, base)
	if len(edits) == 0 {
		return verdict{}, nil
	}
	if v, broken := w.brokenConfigEdit(edits); broken {
		return v, nil
	}
	changes, s := w.editContext(req, env)
	found := w.judgeEdit(edits, changes, s.known())
	if len(found) == 0 {
		return verdict{}, nil
	}
	return verdict{action: deny, reason: coach(editCoach, found, false), notice: editNotice(found)}, nil
}

// absDir makes dir absolute ("" stays "": the directory is unknown).
func absDir(dir string) string {
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// editRoot returns the directory to open the workspace in for an edit:
// the agent's directory, unless the first file the edit touches belongs to
// another git repository (a session started in a parent folder of the
// project, a nested repository, a submodule). That repository's
// .narc.yml, session and history then apply, and paths are relative to
// its root, as `narc scan` there would see them.
func editRoot(ops []fileOp, base string) string {
	if len(ops) == 0 || base == "" {
		return base
	}
	p := ops[0].path
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	owner := enclosingRepo(filepath.Dir(p))
	if owner == "" || sameDir(owner, enclosingRepo(base)) {
		return base
	}
	return owner
}

// enclosingRepo returns the nearest directory at or above dir that has a
// .git entry (a directory, or a file in worktrees and submodules), or "".
// dir itself need not exist (a new file in a new folder).
func enclosingRepo(dir string) string {
	for d := filepath.Clean(dir); ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// sameDir reports whether a and b are the same directory, symlinks
// resolved (macOS reports /var/... for /private/var/...).
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// brokenConfigEdit denies an edit that would leave narc's configuration
// unparseable: every hook would then fail open, turning narc off. Only the
// file narc loads counts (the first of config.FileNames that exists).
func (w *workspace) brokenConfigEdit(edits []pendingEdit) (verdict, bool) {
	for _, e := range edits {
		if !e.hasNew || !isNarcConfig(e.newPath) || !w.loadsConfig(e.newPath, edits) {
			continue
		}
		if _, err := config.Parse(e.new); err != nil {
			return verdict{
				action: deny,
				reason: fmt.Sprintf("narc blocked this edit: it would make %s invalid (%s), and narc stops checking anything while its configuration cannot be loaded. "+
					"Fix the YAML and try again.", e.newPath, truncate(oneLine(err.Error()), maxMsgLen)),
				notice: "narc blocked an edit that would make " + e.newPath + " invalid",
			}, true
		}
	}
	return verdict{}, false
}

// loadsConfig reports whether narc would load its configuration from name
// after the edits: no file that config.Load prefers exists then.
func (w *workspace) loadsConfig(name string, edits []pendingEdit) bool {
	after := map[string]bool{}
	for _, e := range edits {
		if e.hasOld {
			after[e.oldPath] = false
		}
		if e.hasNew {
			after[e.newPath] = true
		}
	}
	for _, n := range config.FileNames {
		if n == name {
			return true
		}
		exists, edited := after[n]
		if !edited {
			_, err := os.Stat(filepath.Join(w.root, n))
			exists = err == nil
		}
		if exists {
			return false
		}
	}
	return false
}

// editContext returns the session's changes since its baseline (used to
// judge an edit in context) and its saved state; either may be nil. The
// first edit of a session without saved state records the session
// snapshot: nothing the agent edited through tools has happened yet.
func (w *workspace) editContext(req *request, env Env) (*core.Changeset, *sessionState) {
	if w.repo == nil {
		return nil, nil
	}
	s := loadSession(w.repo, req.sessionID)
	if s == nil && req.sessionID != "" {
		snap, cs, err := w.snapshot(req.sessionID, req.harness, env)
		if err != nil {
			return nil, nil
		}
		_ = snap.save(env.Now())
		return cs, snap
	}
	// This runs before every edit: skip reading untracked files when there
	// are too many to use as context anyway.
	untracked := true
	if paths, err := w.repo.Untracked(); err != nil || len(paths) > maxContextFiles {
		untracked = false
	}
	cs, err := w.repo.WorkingChangeset(w.baselineFor(s), untracked)
	if err != nil {
		return nil, s
	}
	return cs, s
}

// checkPostEdit gives feedback on files a tool call just changed: their
// changes since the session baseline, minus what was already there.
func checkPostEdit(req *request, env Env) (verdict, error) {
	if len(req.ops) == 0 {
		return verdict{}, nil
	}
	base := absDir(req.dir(env))
	w, err := openWorkspace(editRoot(req.ops, base), true)
	if err != nil {
		return verdict{}, err
	}
	defer w.close()
	paths := w.touched(req.ops, base)
	if len(paths) == 0 {
		return verdict{}, nil
	}
	s := loadSession(w.repo, req.sessionID)
	if s != nil && w.syncHead(s) {
		_ = s.save(env.Now())
	}
	cs, err := w.repo.WorkingChangeset(w.baselineFor(s), true)
	if err != nil {
		return verdict{}, err
	}
	found := unknown(inPaths(w.run(cs).AtLeast(w.blockThreshold()), paths), s.known())
	if len(found) == 0 {
		return verdict{}, nil
	}
	return verdict{action: feedback, reason: coach(postCoach, found, false)}, nil
}

// checkStop decides whether the agent may stop.
func checkStop(req *request, env Env) (verdict, error) {
	if req.aborted {
		return verdict{}, nil
	}
	w, err := openWorkspace(req.dir(env), true)
	if err != nil {
		return verdict{}, err
	}
	defer w.close()
	s := loadSession(w.repo, req.sessionID)
	// dirty: the state changed and must be saved even if the stop is clean.
	dirty := false
	if s == nil {
		// No session-start hook ran: blame everything since HEAD, and
		// remember HEAD so later commits by the agent still count.
		s = newSession(w.repo, req.sessionID, req.harness, env)
		s.Baseline, s.Head = w.headBaseline(), w.headCommit()
	} else {
		// A pull, merge or rebase since the last look brings in work
		// that is not the agent's.
		dirty = w.syncHead(s)
	}
	cs, err := w.repo.WorkingChangeset(w.baselineFor(s), true)
	if err != nil {
		return verdict{}, err
	}
	threshold := w.blockThreshold()
	res := w.run(cs)
	found := unknown(res.AtLeast(threshold), s.known())
	// review tells the human, once, about waivers and narc settings that
	// let changes through. Subagents leave it to the main agent's stop.
	review := func() string {
		if req.kind == subagentStop {
			return ""
		}
		return s.review(reviewItems(res, cs, threshold))
	}

	agent := "main"
	if req.kind == subagentStop {
		agent = "subagent:" + req.agentID
	}
	rec := s.stop(agent)
	if len(found) == 0 {
		var v verdict
		if notice := review(); notice != "" {
			v = verdict{action: release, notice: notice}
		}
		if dirty || v.action == release || *rec != (stopRecord{}) {
			*rec = stopRecord{}
			_ = s.save(env.Now())
		}
		return v, nil
	}

	// attempts counts this streak's blocks. Where the harness reports its
	// own follow-up count, it also bounds the streak, even if narc's state
	// was lost.
	if rec.Blocks == 0 {
		rec.LoopBase = req.loopCount
	}
	attempts := max(rec.Blocks, req.loopCount-rec.LoopBase)
	continuing := req.stopActive != nil && *req.stopActive
	if attempts >= w.cfg.Hook.MaxBlocks {
		return giveUp(s, rec, req, found, attempts, review, env), nil
	}
	digest := findingsDigest(found)
	repeated := continuing && digest == rec.Last
	rec.Blocks++
	rec.Last = digest
	if err := s.save(env.Now()); err != nil && (continuing || req.loopCount > 0) {
		// Without saved state narc cannot count its blocks; never risk an
		// endless loop. This is a repeated stop, so narc blocked at least
		// once before.
		return giveUp(s, rec, req, found, max(attempts, 1), review, env), nil
	}
	return verdict{action: block, reason: coach(stopCoach, found, repeated)}, nil
}

// giveUp lets the agent stop after hook.max_blocks blocked attempts. The
// main agent's unresolved findings are shown to the human once and do not
// block again in this session; a subagent's are left for the main agent's
// stop to report.
func giveUp(s *sessionState, rec *stopRecord, req *request, found []core.Finding, attempts int, review func() string, env Env) verdict {
	*rec = stopRecord{}
	if req.kind == subagentStop {
		_ = s.save(env.Now())
		return verdict{}
	}
	s.accept(found)
	notice := releaseNotice(found, attempts)
	if r := review(); r != "" {
		notice += "\n\n" + r
	}
	_ = s.save(env.Now())
	return verdict{action: release, notice: notice}
}

// reviewItem is something in the session's changes that narc lets through
// but the human should know about.
type reviewItem struct {
	key     string       // identifies the item, so it is reported once
	finding core.Finding // a finding a waiver suppressed, or
	config  string       // narc's configuration file, which changed
}

// reviewItems lists the findings at or above the block threshold that a
// waiver (narc:allow or a .narc.yml allow entry) suppressed, and changes
// to narc's own configuration, which decides what narc checks and blocks.
// The coaching messages teach the agent how to waive a finding, so a
// person should see every waiver it adds.
func reviewItems(res *engine.Result, cs *core.Changeset, threshold core.Severity) []reviewItem {
	var items []reviewItem
	for _, fd := range cs.Files {
		p := fd.Path()
		if !isNarcConfig(p) {
			continue
		}
		state := "deleted"
		if fd.NewPath != "" && cs.Source != nil {
			if b, ok := cs.Source.New(fd.NewPath); ok {
				sum := sha1.Sum(b)
				state = hex.EncodeToString(sum[:8])
			}
		}
		items = append(items, reviewItem{key: "config:" + p + ":" + state, config: p})
	}
	for _, f := range res.Waived() {
		if f.Severity >= threshold {
			items = append(items, reviewItem{key: f.Fingerprint, finding: f})
		}
	}
	return items
}

// isNarcConfig reports whether a repository path is narc's configuration.
func isNarcConfig(p string) bool {
	for _, n := range config.FileNames {
		if p == n {
			return true
		}
	}
	return false
}

// startSession records the session baseline and pre-existing findings.
// A resumed session keeps its original state.
func startSession(req *request, env Env) (verdict, error) {
	if req.sessionID == "" {
		return verdict{}, nil
	}
	w, err := openWorkspace(req.dir(env), true)
	if err != nil {
		return verdict{}, err
	}
	defer w.close()
	if loadSession(w.repo, req.sessionID) != nil {
		return verdict{}, nil
	}
	s, _, err := w.snapshot(req.sessionID, req.harness, env)
	if err != nil {
		return verdict{}, err
	}
	pruneSessions(w.repo, env.Now())
	if err := s.save(env.Now()); err != nil {
		return verdict{}, fmt.Errorf("saving session state: %w", err)
	}
	return verdict{}, nil
}

// inPaths keeps findings in the given files.
func inPaths(fs []core.Finding, paths map[string]bool) []core.Finding {
	var out []core.Finding
	for _, f := range fs {
		if paths[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// unknown drops findings whose fingerprints are known.
func unknown(fs []core.Finding, known map[string]bool) []core.Finding {
	var out []core.Finding
	for _, f := range fs {
		if !known[f.Fingerprint] {
			out = append(out, f)
		}
	}
	return out
}
