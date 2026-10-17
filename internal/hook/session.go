package hook

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/huyth45/narc/internal/core"
	"github.com/huyth45/narc/internal/engine"
	"github.com/huyth45/narc/internal/git"
)

// sessionState is what narc remembers about one agent session. It lives in
// <git-dir>/narc/sessions/<id>.json, so it is per work tree and never
// committed.
type sessionState struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	// Baseline is the commit (or, in an empty repository, the empty tree)
	// that was checked out when the session started. Stop checks diff the
	// work tree against it, so changes the agent commits still count.
	Baseline string `json:"baseline"`
	// Preexisting are fingerprints of findings that already existed when
	// the session started; they are never blamed on the agent.
	Preexisting []string `json:"preexisting,omitempty"`
	// Accepted are fingerprints narc already reported to the human after
	// giving up on them; they no longer block this session.
	Accepted []string `json:"accepted,omitempty"`
	// Head is the commit HEAD pointed at when narc last looked ("" before
	// the first commit). syncHead works out from the reflog how HEAD
	// moved since then.
	Head string `json:"head,omitempty"`
	// Imported are fingerprints of findings that git operations bringing
	// in other people's commits (a pull, merge, rebase onto another
	// branch, cherry-pick or checkout) added to the session's changes.
	// They are not the agent's doing and are never blamed on it.
	Imported []string `json:"imported,omitempty"`
	// Reviewed identifies the waivers and configuration changes the human
	// was told about (or that already existed when the session started),
	// so they are reported once.
	Reviewed []string `json:"reviewed,omitempty"`
	// Stops counts blocked stops per agent ("main", "subagent:<id>").
	Stops       map[string]*stopRecord `json:"stops,omitempty"`
	Started     time.Time              `json:"started"`
	Updated     time.Time              `json:"updated"`
	NarcVersion string                 `json:"narc_version,omitempty"`

	path string
}

type stopRecord struct {
	// Blocks is the number of consecutive stops narc blocked. It resets
	// when a stop is clean or when narc gives up.
	Blocks int `json:"blocks"`
	// Last is a digest of the findings narc blocked on last time.
	Last string `json:"last,omitempty"`
	// LoopBase is the harness's own follow-up count (Cursor's loop_count)
	// when the current streak of blocks began. Harnesses may count
	// follow-ups over a whole conversation; only this streak's count
	// matters.
	LoopBase int `json:"loop_base,omitempty"`
}

// sessionMaxAge is how long session files are kept.
const sessionMaxAge = 30 * 24 * time.Hour

func sessionDir(repo *git.Repo) string { return filepath.Join(repo.GitDir, "narc", "sessions") }

// sessionFileName maps a session ID to a safe file name.
func sessionFileName(id string) string {
	if id == "" {
		id = "default"
	}
	safe := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, id)
	if safe != id || len(safe) > 100 || strings.HasPrefix(safe, ".") {
		sum := sha1.Sum([]byte(id))
		if len(safe) > 64 {
			safe = safe[:64]
		}
		safe = strings.TrimLeft(safe, ".") + "-" + hex.EncodeToString(sum[:4])
	}
	return safe + ".json"
}

func newSession(repo *git.Repo, id, harness string, env Env) *sessionState {
	return &sessionState{
		ID: id, Harness: harness, Started: env.Now().UTC(), NarcVersion: env.Version,
		path: filepath.Join(sessionDir(repo), sessionFileName(id)),
	}
}

// loadSession returns the saved state of a session, or nil.
func loadSession(repo *git.Repo, id string) *sessionState {
	p := filepath.Join(sessionDir(repo), sessionFileName(id))
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var s sessionState
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	s.path = p
	return &s
}

// save writes the state atomically (hooks may run concurrently).
func (s *sessionState) save(now time.Time) error {
	s.Updated = now.UTC()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".session-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// known returns the fingerprints that must not be blamed on the agent.
func (s *sessionState) known() map[string]bool {
	m := map[string]bool{}
	if s == nil {
		return m
	}
	for _, f := range s.Preexisting {
		m[f] = true
	}
	for _, f := range s.Accepted {
		m[f] = true
	}
	for _, f := range s.Imported {
		m[f] = true
	}
	return m
}

func (s *sessionState) stop(agent string) *stopRecord {
	if s.Stops == nil {
		s.Stops = map[string]*stopRecord{}
	}
	r := s.Stops[agent]
	if r == nil {
		r = &stopRecord{}
		s.Stops[agent] = r
	}
	return r
}

func (s *sessionState) accept(fs []core.Finding) {
	s.Accepted = sortedUnique(append(s.Accepted, fingerprints(fs)...))
}

// snapshot records a new session: the current HEAD as the baseline, the
// findings that already exist in the work tree, and the waivers and
// .narc.yml changes already there (never reported as the agent's). It
// also returns the work-tree changeset it scanned.
func (w *workspace) snapshot(id, harness string, env Env) (*sessionState, *core.Changeset, error) {
	s := newSession(w.repo, id, harness, env)
	s.Baseline, s.Head = w.headBaseline(), w.headCommit()
	cs, err := w.repo.WorkingChangeset(s.Baseline, true)
	if err != nil {
		return nil, nil, err
	}
	res := engine.Run(cs, engine.Options{Config: w.cfg})
	s.Preexisting = sortedUnique(fingerprints(res.Active()))
	for _, it := range reviewItems(res, cs, w.blockThreshold()) {
		s.Reviewed = append(s.Reviewed, it.key)
	}
	s.Reviewed = sortedUnique(s.Reviewed)
	return s, cs, nil
}

// review returns a note for the human about the items not reported yet in
// this session ("" if none), and records them as reported.
func (s *sessionState) review(items []reviewItem) string {
	seen := toSet(s.Reviewed...)
	var fresh []reviewItem
	for _, it := range items {
		if !seen[it.key] {
			seen[it.key] = true
			fresh = append(fresh, it)
			s.Reviewed = append(s.Reviewed, it.key)
		}
	}
	s.Reviewed = sortedUnique(s.Reviewed)
	return reviewNotice(fresh)
}

// headBaseline is the commit HEAD points at, or the empty tree in a
// repository without commits.
func (w *workspace) headBaseline() string {
	if w.repo.HasHead() {
		if sha, err := w.repo.Resolve("HEAD"); err == nil {
			return sha
		}
	}
	return w.repo.EmptyTree()
}

// baselineFor returns the session's baseline if it still exists, else ""
// (meaning HEAD).
func (w *workspace) baselineFor(s *sessionState) string {
	if s == nil || s.Baseline == "" {
		return ""
	}
	if _, err := w.repo.Run("rev-parse", "--verify", "--quiet", s.Baseline+"^{tree}"); err != nil {
		return ""
	}
	return s.Baseline
}

// pruneSessions removes session files older than sessionMaxAge.
func pruneSessions(repo *git.Repo, now time.Time) {
	dir := sessionDir(repo)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > sessionMaxAge {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func fingerprints(fs []core.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Fingerprint)
	}
	return out
}

func sortedUnique(ss []string) []string {
	sort.Strings(ss)
	out := ss[:0]
	for i, s := range ss {
		if i == 0 || s != ss[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// findingsDigest identifies a set of findings.
func findingsDigest(fs []core.Finding) string {
	fps := sortedUnique(fingerprints(fs))
	sum := sha1.Sum([]byte(strings.Join(fps, ",")))
	return hex.EncodeToString(sum[:8])
}
