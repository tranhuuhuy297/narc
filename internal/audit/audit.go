// Package audit walks git history, attributes commits to AI coding agents
// (or humans), runs narc's rules on each commit, and aggregates a rap sheet.
package audit

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/engine"
	"github.com/tranhuuhuy297/narc/internal/git"
)

// Commit is the metadata of one commit.
type Commit struct {
	SHA            string    `json:"sha"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	Time           time.Time `json:"time"`
	Subject        string    `json:"subject"`
	Body           string    `json:"-"`
	Trailers       []Trailer `json:"-"`
}

// Short returns the abbreviated SHA.
func (c *Commit) Short() string {
	if len(c.SHA) > 8 {
		return c.SHA[:8]
	}
	return c.SHA
}

// Trailer is a "Key: value" line from a commit message.
type Trailer struct{ Key, Value string }

// Offense is a commit with findings.
type Offense struct {
	Commit      *Commit        `json:"commit"`
	Attribution Attribution    `json:"attribution"`
	Findings    []core.Finding `json:"findings"`
	Errors      int            `json:"errors"`
	Warnings    int            `json:"warnings"`
}

// Stats aggregates one agent (or the human baseline).
type Stats struct {
	Name     string         `json:"name"`
	Human    bool           `json:"human,omitempty"`
	Commits  int            `json:"commits"`
	Flagged  int            `json:"flagged_commits"`
	Errors   int            `json:"errors"`
	Warnings int            `json:"warnings"`
	ByRule   map[string]int `json:"by_rule"`
	Models   map[string]int `json:"models,omitempty"`
	Offenses []*Offense     `json:"offenses,omitempty"`
}

// Rate is the share of commits with at least one offense.
func (s *Stats) Rate() float64 {
	if s.Commits == 0 {
		return 0
	}
	return float64(s.Flagged) / float64(s.Commits)
}

// TopRules returns the most frequent offenses, most frequent first.
func (s *Stats) TopRules(n int) []RuleCount {
	var out []RuleCount
	for id, c := range s.ByRule {
		out = append(out, RuleCount{id, c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Rule < out[j].Rule
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// RuleCount is a rule with an offense count.
type RuleCount struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// Report is the result of an audit.
type Report struct {
	Repo        string        `json:"repo"`
	WebURL      string        `json:"web_url,omitempty"` // e.g. https://github.com/owner/repo
	Rev         string        `json:"rev"`
	Scanned     int           `json:"commits_scanned"`
	Skipped     int           `json:"commits_skipped"` // automation bots, oversized commits, errors
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	MinSeverity core.Severity `json:"min_severity"`
	Agents      []*Stats      `json:"agents"`
	Humans      *Stats        `json:"humans"`
	Duration    time.Duration `json:"-"`
	Truncated   bool          `json:"truncated,omitempty"` // --max reached
}

// AgentCommits returns the number of commits attributed to any agent.
func (r *Report) AgentCommits() int {
	n := 0
	for _, a := range r.Agents {
		n += a.Commits
	}
	return n
}

// CommitURL returns a web link for a commit, if the remote is known.
func (r *Report) CommitURL(sha string) string {
	if r.WebURL == "" {
		return ""
	}
	return r.WebURL + "/commit/" + sha
}

// Options configures an audit.
type Options struct {
	Config      *config.Config
	Rev         string // revision or range to walk (default HEAD)
	Max         int    // maximum commits to scan (0 = no limit)
	Since       string // passed to git log --since
	Agent       string // only report this agent (case-insensitive substring)
	IncludeInfo bool   // also count info-level findings (default: warnings and errors only)
	Workers     int    // parallel workers (default 8)
	MaxFiles    int    // skip commits touching more files than this (default 400)
	Progress    func(done, total int)
}

// Run audits a repository.
func Run(repo *git.Repo, opts Options) (*Report, error) {
	start := time.Now()
	if opts.Rev == "" {
		opts.Rev = "HEAD"
	}
	if opts.Workers <= 0 {
		opts.Workers = 8
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 400
	}
	if opts.Config == nil {
		opts.Config = config.Default()
	}
	minSev := core.Warning
	if opts.IncludeInfo {
		minSev = core.Info
	}
	if !repo.HasHead() {
		return nil, errors.New("this repository has no commits yet")
	}
	commits, truncated, err := listCommits(repo, opts)
	if err != nil {
		return nil, err
	}

	rep := &Report{
		Repo:        repoName(repo),
		WebURL:      webURL(repo),
		Rev:         opts.Rev,
		MinSeverity: minSev,
		Truncated:   truncated,
		Humans:      newStats("Humans", true),
	}
	if len(commits) > 0 {
		rep.To = commits[0].Time
		rep.From = commits[len(commits)-1].Time
	}

	type job struct {
		i int
		c *Commit
		a Attribution
	}
	type result struct {
		i        int
		findings []core.Finding
		skipped  bool
	}
	jobs := make(chan job)
	results := make([]result, len(commits))
	attrs := make([]Attribution, len(commits))
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for w := 0; w < opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := git.Open(repo.Root) // own cat-file process per worker
			if err != nil {
				for j := range jobs {
					results[j.i] = result{i: j.i, skipped: true}
				}
				return
			}
			defer r.Close()
			for j := range jobs {
				fs, ok := analyzeCommit(r, j.c, opts, minSev)
				results[j.i] = result{i: j.i, findings: fs, skipped: !ok}
				if opts.Progress != nil {
					mu.Lock()
					done++
					opts.Progress(done, len(commits))
					mu.Unlock()
				}
			}
		}()
	}
	for i, c := range commits {
		attrs[i] = Attribute(c)
		jobs <- job{i, c, attrs[i]}
	}
	close(jobs)
	wg.Wait()

	byAgent := map[string]*Stats{}
	for i, c := range commits {
		a := attrs[i]
		res := results[i]
		if res.skipped || a.Automation {
			rep.Skipped++
			continue
		}
		rep.Scanned++
		var st *Stats
		if a.Agent == "" {
			st = rep.Humans
		} else {
			st = byAgent[a.Agent]
			if st == nil {
				st = newStats(a.Agent, false)
				byAgent[a.Agent] = st
			}
			if a.Model != "" {
				st.Models[a.Model]++
			}
		}
		st.Commits++
		if len(res.findings) == 0 {
			continue
		}
		off := &Offense{Commit: c, Attribution: a, Findings: res.findings}
		for _, f := range res.findings {
			st.ByRule[f.RuleID]++
			switch f.Severity {
			case core.Error:
				off.Errors++
				st.Errors++
			case core.Warning:
				off.Warnings++
				st.Warnings++
			}
		}
		st.Flagged++
		st.Offenses = append(st.Offenses, off)
	}
	for _, st := range byAgent {
		if opts.Agent != "" && !strings.Contains(strings.ToLower(st.Name), strings.ToLower(opts.Agent)) {
			continue
		}
		rep.Agents = append(rep.Agents, st)
	}
	sort.Slice(rep.Agents, func(i, j int) bool {
		if rep.Agents[i].Commits != rep.Agents[j].Commits {
			return rep.Agents[i].Commits > rep.Agents[j].Commits
		}
		return rep.Agents[i].Name < rep.Agents[j].Name
	})
	for _, st := range append(rep.Agents, rep.Humans) {
		sortOffenses(st.Offenses)
	}
	rep.Duration = time.Since(start)
	return rep, nil
}

func newStats(name string, human bool) *Stats {
	return &Stats{Name: name, Human: human, ByRule: map[string]int{}, Models: map[string]int{}}
}

// sortOffenses orders the worst offenses first: most errors, then most
// warnings, then most recent.
func sortOffenses(os []*Offense) {
	sort.SliceStable(os, func(i, j int) bool {
		a, b := os[i], os[j]
		if a.Errors != b.Errors {
			return a.Errors > b.Errors
		}
		if a.Warnings != b.Warnings {
			return a.Warnings > b.Warnings
		}
		return a.Commit.Time.After(b.Commit.Time)
	})
}

func analyzeCommit(r *git.Repo, c *Commit, opts Options, minSev core.Severity) ([]core.Finding, bool) {
	cs, err := r.CommitChangeset(c.SHA)
	if err != nil {
		return nil, false
	}
	if len(cs.Files) > opts.MaxFiles {
		return nil, false
	}
	res := engine.Run(cs, engine.Options{Config: opts.Config})
	return res.AtLeast(minSev), true
}

const (
	fieldSep  = "\x1f"
	recordSep = "\x1e"
)

func listCommits(repo *git.Repo, opts Options) ([]*Commit, bool, error) {
	args := []string{"log", "--no-merges", "--no-color", "--format=" + strings.Join([]string{"%H", "%an", "%ae", "%cn", "%ce", "%ct", "%B"}, "%x1f") + "%x1e"}
	if opts.Max > 0 {
		args = append(args, "--max-count="+strconv.Itoa(opts.Max+1))
	}
	if opts.Since != "" {
		args = append(args, "--since="+opts.Since)
	}
	args = append(args, opts.Rev, "--")
	out, err := repo.Run(args...)
	if err != nil {
		return nil, false, err
	}
	var commits []*Commit
	for _, rec := range bytes.Split(out, []byte(recordSep)) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(bytes.TrimSpace(rec)) == 0 {
			continue
		}
		parts := strings.SplitN(string(rec), fieldSep, 7)
		if len(parts) < 7 {
			continue
		}
		ts, _ := strconv.ParseInt(parts[5], 10, 64)
		body := strings.TrimRight(parts[6], "\n")
		subject := body
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			subject = body[:i]
		}
		commits = append(commits, &Commit{
			SHA: parts[0], AuthorName: parts[1], AuthorEmail: parts[2],
			CommitterName: parts[3], CommitterEmail: parts[4],
			Time: time.Unix(ts, 0).UTC(), Subject: strings.TrimSpace(subject), Body: body,
			Trailers: parseTrailers(body),
		})
	}
	truncated := false
	if opts.Max > 0 && len(commits) > opts.Max {
		commits = commits[:opts.Max]
		truncated = true
	}
	return commits, truncated, nil
}

var trailerRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]{1,40}):\s*(.+?)\s*$`)

// parseTrailers extracts "Key: value" lines anywhere in the message body
// after the subject. Agents do not always put trailers in the final
// paragraph, so we scan every line.
func parseTrailers(body string) []Trailer {
	var out []Trailer
	lines := strings.Split(body, "\n")
	for _, l := range lines[1:] {
		if m := trailerRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			out = append(out, Trailer{Key: m[1], Value: m[2]})
		}
	}
	return out
}

func repoName(repo *git.Repo) string {
	if u := webURL(repo); u != "" {
		if p, err := url.Parse(u); err == nil {
			return strings.TrimPrefix(p.Path, "/")
		}
	}
	return filepath.Base(repo.Root)
}

var (
	scpRemote = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+?)(?:\.git)?/?$`)
)

// webURL derives an https URL for the origin remote (GitHub, GitLab,
// Codeberg, ...), or "" when unknown.
func webURL(repo *git.Repo) string {
	out, err := repo.Run("remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return remoteToWeb(strings.TrimSpace(string(out)))
}

func remoteToWeb(remote string) string {
	if remote == "" {
		return ""
	}
	if strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://") || strings.HasPrefix(remote, "ssh://") || strings.HasPrefix(remote, "git://") {
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return ""
		}
		host := u.Hostname()
		path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
		return "https://" + host + path
	}
	if m := scpRemote.FindStringSubmatch(remote); m != nil && !strings.HasPrefix(remote, "/") {
		return "https://" + m[1] + "/" + strings.TrimPrefix(m[2], "/")
	}
	return ""
}

// Summary returns a one-line description, used in share text.
func (r *Report) Summary() string {
	var parts []string
	for _, a := range r.Agents {
		parts = append(parts, fmt.Sprintf("%s %d/%d", a.Name, a.Flagged, a.Commits))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("narc audit %s: no agent commits in %d commits", r.Repo, r.Scanned)
	}
	return fmt.Sprintf("narc audit %s: %s (flagged/commits)", r.Repo, strings.Join(parts, ", "))
}
