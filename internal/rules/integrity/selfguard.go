package integrity

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
	"github.com/tranhuuhuy297/narc/internal/rules/rulekit"
)

var configWeakened = &core.Rule{
	ID:       "narc-config-weakened",
	Title:    "narc itself was weakened",
	Category: "integrity",
	Severity: core.Error,
	Summary:  "The change turns narc off or down: rules disabled, ignore globs or waivers added, protected paths dropped, or narc's agent hooks and CI step removed.",
	Hint: "Leave narc's configuration and hooks as they were and fix the code instead. Tuning narc is a human decision: " +
		"make it in a separate, explained change.",
	Docs: "An agent that is blocked by narc may try to switch narc off instead of fixing the code. This rule watches " +
		"narc's own configuration (`.narc.yml`) and the places that run narc (agent hook settings, git hooks, CI).\n\n" +
		"Errors: rules turned `off`, `fail_on: never`, `hook.block_on` raised, `ignore` globs that cover everything, " +
		"protected paths removed, `.narc.yml` deleted or made unparseable, `narc hook` / `narc scan` removed from " +
		"`.claude/settings.json`, `.codex/hooks.json`, `.gemini/settings.json`, `.cursor/hooks.json` or a CI workflow, " +
		"and `\"disableAllHooks\": true`.\n\nWarnings: severities lowered, `ignore` globs and `allow:` waivers added.\n\n" +
		"```yaml\n# .narc.yml\nrules:\n  skip-added: off     # <- narc-config-weakened\n```",
}

func init() {
	configWeakened.Check = checkConfigWeakened
	core.Register(configWeakened)
}

func isNarcConfig(p string) bool {
	if strings.Contains(p, "/") {
		return false
	}
	for _, n := range config.FileNames {
		if p == n {
			return true
		}
	}
	return false
}

// agentHookFiles are files where narc's agent hooks are installed.
func isHookConfig(p string) bool {
	switch p {
	case ".claude/settings.json", ".claude/settings.local.json", ".codex/hooks.json", ".codex/config.toml",
		".gemini/settings.json", ".cursor/hooks.json", ".opencode/plugins/narc.ts", "lefthook.yml", "lefthook.yaml",
		".pre-commit-config.yaml", ".pre-commit-config.yml":
		return true
	}
	return strings.HasPrefix(p, ".husky/") || strings.HasPrefix(p, ".githooks/")
}

var (
	narcInvocation = regexp.MustCompile(`\bnarc(\.exe)?["']?\s+(hook|scan)\b|tranhuuhuy297/narc\b|narc-(before|after)-|\bnarc\s*$`)
	disableHooksRe = regexp.MustCompile(`"disableAllHooks"\s*:\s*true`)
)

func checkConfigWeakened(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range a.Files {
		switch {
		case isNarcConfig(f.Path) || (f.OldPath != "" && isNarcConfig(f.OldPath)):
			out = append(out, configChanges(f)...)
		case isHookConfig(f.Path) || f.IsCI:
			out = append(out, hookRemovals(a, f)...)
		}
	}
	return out
}

// hookRemovals reports removed lines that invoked narc and were not re-added
// anywhere in the change, plus Claude Code's global hook kill switch.
func hookRemovals(a *core.Analysis, f *core.FileInfo) []core.Finding {
	var out []core.Finding
	readded := map[string]int{}
	for _, g := range a.Files {
		for _, l := range g.Added {
			if narcInvocation.MatchString(l.Raw) {
				readded[rulekit.Normalize(narcCall(l.Raw))]++
			}
		}
	}
	for _, l := range f.Removed {
		if !narcInvocation.MatchString(l.Raw) {
			continue
		}
		k := rulekit.Normalize(narcCall(l.Raw))
		if readded[k] > 0 {
			readded[k]--
			continue
		}
		where := "the agent hook"
		if f.IsCI {
			where = "the CI check"
		}
		fd := configWeakened.Removal(f, l.No, fmt.Sprintf("narc was removed from %s in %s", where, f.Path))
		if f.Diff.Kind == diff.Deleted {
			fd.Message = fmt.Sprintf("%s, which ran narc, was deleted", f.Path)
		}
		out = append(out, fd)
		break // one finding per file is enough
	}
	for _, m := range rulekit.IntroducedNet(f, disableHooksRe, rulekit.Raw) {
		out = append(out, configWeakened.Finding(f, m.Line.No, "\"disableAllHooks\": true switches off every hook, including narc"))
	}
	return out
}

var narcCallRe = regexp.MustCompile(`narc(?:\.exe)?["']?\s+(?:hook|scan)(?:\s+[\w.-]+)*|tranhuuhuy297/narc|narc-(?:before|after)-[\w-]+`)

// narcCall extracts just the narc invocation from a line ("narc hook claude
// stop", "tranhuuhuy297/narc"), so reformatting the surrounding JSON/YAML or
// pinning an action version is not mistaken for a removal.
func narcCall(s string) string {
	if m := narcCallRe.FindString(s); m != "" {
		return m
	}
	return s
}

func configChanges(f *core.FileInfo) []core.Finding {
	oldB, hasOld := f.OldContent()
	newB, hasNew := f.NewContent()
	if f.Diff.Kind == diff.Deleted {
		return []core.Finding{configWeakened.Finding(f, 0, path.Base(f.Path)+" was deleted, so narc falls back to its defaults")}
	}
	if !hasNew {
		return nil // patch-only: we cannot compare configs reliably
	}
	newCfg, err := config.Parse(newB)
	if err != nil {
		return []core.Finding{configWeakened.Finding(f, firstAdded(f), "narc's config no longer parses: "+firstLineOf(err.Error()))}
	}
	oldCfg := config.Default()
	if hasOld {
		if c, err := config.Parse(oldB); err == nil {
			oldCfg = c
		}
	}
	var out []core.Finding
	add := func(sev core.Severity, needle, msg string) {
		fd := configWeakened.Finding(f, findLine(f, needle), msg)
		fd.Severity = sev
		out = append(out, fd)
	}

	// Rule severities.
	ids := map[string]bool{}
	for id := range newCfg.Rules {
		ids[id] = true
	}
	for id := range oldCfg.Rules {
		ids[id] = true
	}
	for _, id := range sortedKeys(ids) {
		before, after, known := severities(id, oldCfg, newCfg)
		if !known {
			continue
		}
		switch {
		case after == core.Off && before != core.Off:
			add(core.Error, id, fmt.Sprintf("rule %s was turned off", id))
		case after < before:
			add(core.Warning, id, fmt.Sprintf("rule %s was downgraded %s → %s", id, before, after))
		}
	}

	// Thresholds.
	if ft, ok := newCfg.FailThreshold(); !ok {
		if _, was := oldCfg.FailThreshold(); was {
			add(core.Error, "fail_on", "fail_on: never — narc can no longer fail a build")
		}
	} else if of, ok := oldCfg.FailThreshold(); ok && ft > of {
		add(core.Warning, "fail_on", fmt.Sprintf("fail_on raised %s → %s", of, ft))
	}
	if nb, ob := newCfg.BlockThreshold(), oldCfg.BlockThreshold(); nb > ob {
		add(core.Error, "block_on", fmt.Sprintf("hook.block_on raised %s → %s, so agents are blocked less", ob, nb))
	}

	// Ignore globs.
	for _, g := range newOnly(newCfg.Ignore, oldCfg.Ignore) {
		sev := core.Warning
		if g == "**" || g == "*" || g == "/" || g == "**/*" {
			sev = core.Error
		}
		add(sev, g, fmt.Sprintf("narc now ignores %q", g))
	}
	// Protected paths.
	for _, g := range newOnly(oldCfg.Protected, newCfg.Protected) {
		msg := fmt.Sprintf("protected path %q was removed", g)
		fd := configWeakened.Finding(f, 0, msg)
		for _, l := range f.Removed {
			if strings.Contains(l.Raw, g) {
				fd = configWeakened.Removal(f, l.No, msg)
				break
			}
		}
		out = append(out, fd)
	}
	// Waivers.
	oldAllow := map[string]bool{}
	for _, al := range oldCfg.Allow {
		oldAllow[al.Rule+"\x00"+al.Path] = true
	}
	for _, al := range newCfg.Allow {
		if oldAllow[al.Rule+"\x00"+al.Path] {
			continue
		}
		scope := "everywhere"
		if al.Path != "" {
			scope = "for " + al.Path
		}
		add(core.Warning, al.Reason, fmt.Sprintf("new waiver for %s %s: %q", al.Rule, scope, al.Reason))
	}
	return out
}

// severities returns a rule's effective severity before and after. For a
// rule this binary does not know, only explicit settings are compared.
func severities(id string, oldCfg, newCfg *config.Config) (before, after core.Severity, known bool) {
	if r := core.RuleByID(id); r != nil {
		return oldCfg.SeverityFor(r), newCfg.SeverityFor(r), true
	}
	nv, nok := newCfg.Rules[id]
	if !nok {
		return 0, 0, false
	}
	after, _ = core.ParseSeverity(nv)
	before = core.Error // unknown default: assume the rule was active
	if ov, ook := oldCfg.Rules[id]; ook {
		before, _ = core.ParseSeverity(ov)
	} else if after != core.Off {
		return 0, 0, false
	}
	return before, after, true
}

func newOnly(now, before []string) []string {
	seen := map[string]bool{}
	for _, b := range before {
		seen[strings.TrimSpace(b)] = true
	}
	var out []string
	for _, n := range now {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			out = append(out, n)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findLine returns the first added line containing needle, else the first
// added line.
func findLine(f *core.FileInfo, needle string) int {
	for _, l := range f.Added {
		if needle != "" && strings.Contains(l.Raw, needle) {
			return l.No
		}
	}
	return firstAdded(f)
}

func firstAdded(f *core.FileInfo) int {
	if len(f.Added) > 0 {
		return f.Added[0].No
	}
	return 0
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
