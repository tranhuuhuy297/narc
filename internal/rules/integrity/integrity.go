// Package integrity holds repository-policy rules.
package integrity

import (
	"fmt"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
)

var protectedPath = &core.Rule{
	ID:       "protected-path",
	Title:    "Protected file changed",
	Category: "integrity",
	Severity: core.Error,
	Summary:  "A file matching a `protected:` glob in .narc.yml was modified or deleted.",
	Hint:     "Revert the change to the protected file. If it really must change, a human should make or approve that change.",
	Docs: "Some files define what \"correct\" means: golden outputs, contract tests, the CI pipeline itself. " +
		"List them under `protected:` in `.narc.yml` and narc flags any change to them, so an agent cannot " +
		"move the goalposts.\n\n```yaml\nprotected:\n  - \"tests/golden/**\"\n  - \".github/workflows/**\"\n```",
}

func init() {
	// Check is assigned here, not in the literal, to avoid an
	// initialization cycle (the check refers back to the rule).
	protectedPath.Check = checkProtected
	core.Register(protectedPath)
}

func checkProtected(a *core.Analysis) []core.Finding {
	if len(a.Settings.Protected) == 0 {
		return nil
	}
	var out []core.Finding
	for _, f := range a.Files {
		hit := core.MatchAny(a.Settings.Protected, f.Path) || (f.OldPath != "" && core.MatchAny(a.Settings.Protected, f.OldPath))
		// Adding new files under a protected path (a new golden file for a
		// new test) does not move existing goalposts.
		if !hit || f.Diff.Kind == diff.Added {
			continue
		}
		add, del := f.Diff.Stats()
		var msg string
		switch f.Diff.Kind {
		case diff.Deleted:
			msg = "protected file was deleted"
		case diff.Renamed:
			msg = fmt.Sprintf("protected file was renamed from %s", f.OldPath)
		default:
			msg = fmt.Sprintf("protected file was modified (+%d -%d)", add, del)
		}
		line := 0
		if len(f.Added) > 0 {
			line = f.Added[0].No
		} else if len(f.Removed) > 0 {
			line = f.AnchorForOld(f.Removed[0].No)
		}
		out = append(out, protectedPath.Finding(f, line, msg))
	}
	return out
}
