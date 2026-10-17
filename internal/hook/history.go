package hook

import (
	"strconv"
	"strings"
)

// A session's changes are everything between its baseline commit and the
// work tree, so commits the agent makes still count. HEAD can also move
// in ways that bring in work that is not the agent's: the human (or the
// agent) pulls a teammate's commits, merges or rebases onto another
// branch, cherry-picks, or checks out another commit. Without telling
// these apart, a stop would blame the agent for a teammate's skipped test.
//
// syncHead reads how HEAD moved from the reflog. Commits, amends,
// reverts, the replayed steps of a rebase, and resets that drop commits
// are the agent's; every other move (including anything narc cannot
// classify) imports the findings of the range it moved over. Imported
// findings are kept only while the session's commits still contain them,
// so checking out an old commit and coming back does not hide a test the
// agent deletes later.

// maxReflog bounds how many reflog entries narc reads.
const maxReflog = 500

// headMove is one move of HEAD from one commit to another, from the reflog.
type headMove struct {
	from, to string // commits; "" for no commit
	subject  string // reflog subject, e.g. "pull: Fast-forward"
}

// headCommit returns the commit HEAD points at, or "" without commits.
func (w *workspace) headCommit() string {
	if !w.repo.HasHead() {
		return ""
	}
	sha, err := w.repo.Resolve("HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// lastHead is the commit narc last saw HEAD at. State written before
// narc tracked it has only the baseline, which is where HEAD was then.
func (w *workspace) lastHead(s *sessionState) string {
	if s.Head != "" {
		return s.Head
	}
	if sha, err := w.repo.Resolve(s.Baseline); err == nil {
		return sha
	}
	return ""
}

// syncHead accounts for HEAD moves since narc last looked (see above) and
// reports whether the session state changed.
func (w *workspace) syncHead(s *sessionState) bool {
	cur, last := w.headCommit(), w.lastHead(s)
	if cur == last {
		return false
	}
	s.Head = cur
	base := w.baselineFor(s)
	if base == "" || cur == "" {
		// The session is judged against HEAD (or there is no commit):
		// committed changes are not part of it.
		s.Imported = nil
		return true
	}
	moves, ok := w.headMoves(last, cur)
	if !ok {
		moves = []headMove{{from: last, to: cur}} // unknown: assume imported
	}
	imported := toSet(s.Imported...)
	threshold := w.blockThreshold()
	for _, m := range moves {
		if m.from == m.to || w.ownMove(m) {
			continue
		}
		from := m.from
		if from == "" {
			from = w.repo.EmptyTree()
		}
		cs, err := w.repo.RangeChangeset(from, m.to)
		if err != nil {
			// Unable to tell what came in: stop judging the session's
			// earlier commits rather than risk blaming the agent for them.
			s.Baseline, s.Imported = cur, nil
			return true
		}
		res := w.run(cs)
		for _, f := range res.Active() {
			imported[f.Fingerprint] = true
		}
		// Waivers and narc settings that came in are not the session's
		// to review either.
		for _, it := range reviewItems(res, cs, threshold) {
			s.Reviewed = append(s.Reviewed, it.key)
		}
	}
	s.Reviewed = sortedUnique(s.Reviewed)
	s.Imported = nil
	if len(imported) == 0 {
		return true
	}
	// Keep what the session's commits still contain.
	present := imported // if the commits cannot be read, keep them all
	if cs, err := w.repo.RangeChangeset(base, cur); err == nil {
		present = map[string]bool{}
		for _, f := range w.run(cs).Findings {
			present[f.Fingerprint] = imported[f.Fingerprint]
		}
	}
	for fp, ok := range present {
		if ok {
			s.Imported = append(s.Imported, fp)
		}
	}
	s.Imported = sortedUnique(s.Imported)
	return true
}

// headMoves returns HEAD's moves from last to cur, newest first, read from
// the reflog. ok is false when the reflog does not reach back to last.
func (w *workspace) headMoves(last, cur string) (moves []headMove, ok bool) {
	out, err := w.repo.Run("log", "--walk-reflogs", "-n", strconv.Itoa(maxReflog), "--format=%H%x00%gs", "HEAD", "--")
	if err != nil {
		return nil, false
	}
	type entry struct{ sha, subject string }
	var entries []entry
	for _, line := range strings.Split(string(out), "\n") {
		if sha, subject, found := strings.Cut(line, "\x00"); found {
			entries = append(entries, entry{sha, subject})
		}
	}
	if len(entries) == 0 || entries[0].sha != cur {
		return nil, false
	}
	// Each entry records where HEAD went; it came from the entry before it.
	for i, e := range entries {
		if e.sha == last {
			return moves, true
		}
		from := ""
		if i+1 < len(entries) {
			from = entries[i+1].sha
		} else if last != "" || len(entries) == maxReflog {
			return nil, false
		}
		moves = append(moves, headMove{from: from, to: e.sha, subject: e.subject})
	}
	return moves, true
}

// rebaseSteps are the reflog actions of a rebase that replay the branch's
// own commits (as opposed to "start", which checks out the new base).
var rebaseSteps = toSet("pick", "reword", "edit", "squash", "fixup", "continue")

// ownMove reports whether a HEAD move is the session's own work: a commit
// (or amend), a revert, a rebase replaying the branch's commits, or a
// reset that drops commits. Merges, pulls, rebases onto a new base,
// cherry-picks, checkouts and anything unrecognized are not.
func (w *workspace) ownMove(m headMove) bool {
	command, action := reflogAction(m.subject)
	switch command {
	case "commit":
		return action == "" || action == "initial" || action == "amend"
	case "revert":
		return true
	case "rebase", "pull":
		return rebaseSteps[action]
	case "reset":
		// Moving back along the branch undoes commits; moving anywhere
		// else (reset --hard origin/main) brings other commits in.
		if m.from == "" || m.to == "" {
			return false
		}
		_, err := w.repo.Run("merge-base", "--is-ancestor", m.to, m.from)
		return err == nil
	}
	return false
}

// reflogAction splits a reflog subject such as "rebase -i (pick): fix
// parser" or "commit (amend): add tests" into its command ("rebase",
// "commit") and the action in parentheses ("pick", "amend", or "").
func reflogAction(subject string) (command, action string) {
	head, _, _ := strings.Cut(subject, ": ")
	command, _, _ = strings.Cut(head, " ")
	if strings.HasSuffix(head, ")") {
		if i := strings.LastIndex(head, " ("); i >= 0 {
			action = head[i+2 : len(head)-1]
		}
	}
	return command, action
}
