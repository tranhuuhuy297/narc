package audit

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/git"
)

// TestRealRepoAttribution is a manual check: NARC_REAL_REPOS=dir1,dir2 go test -run Real -v
func TestRealRepoAttribution(t *testing.T) {
	env := os.Getenv("NARC_REAL_REPOS")
	if env == "" {
		t.Skip("set NARC_REAL_REPOS")
	}
	for _, dir := range strings.Split(env, ",") {
		repo, err := git.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := Run(repo, Options{Max: 1500, Workers: 8})
		repo.Close()
		if err != nil {
			t.Fatal(err)
		}
		var parts []string
		for _, a := range rep.Agents {
			parts = append(parts, a.Name+"="+itoa(a.Commits))
		}
		sort.Strings(parts)
		t.Logf("%-40s scanned=%d skipped=%d humans=%d agents: %s  (%s)", rep.Repo, rep.Scanned, rep.Skipped, rep.Humans.Commits, strings.Join(parts, " "), rep.Duration)
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n)) }
func fmtInt(n int) string {
	s := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
