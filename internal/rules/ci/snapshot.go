package ci

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/diff"
)

var snapshotForced = &core.Rule{
	ID:       "snapshot-update-forced",
	Title:    "Snapshot update forced",
	Category: "ci",
	Severity: core.Warning,
	Summary:  "A test command now rewrites snapshots instead of comparing against them (`-u`, `--updateSnapshot`, `--update-snapshots`, `UPDATE_SNAPSHOTS=1`, `INSTA_UPDATE=always`, ...).",
	Hint: "Remove the update flag: a snapshot test must compare against the committed snapshot. Update " +
		"snapshots locally with a dedicated command, review the diff, and commit the new snapshots.",
	Docs: "A test that runs with `jest -u` cannot fail on a snapshot mismatch: it overwrites the snapshot " +
		"with whatever the code produces now. Added to a test script or a CI step, it silently accepts " +
		"every regression.\n\n" +
		"```diff\n-    \"test\": \"jest --ci\",\n+    \"test\": \"jest --ci -u\",      // SUSPICIOUS\n```\n\n" +
		"Covered: jest/vitest `-u`/`--updateSnapshot`/`--update`/`--ci=false`, Playwright and Bun " +
		"`--update-snapshots`, syrupy `--snapshot-update`, pytest-regressions `--force-regen`, " +
		"inline-snapshot `--inline-snapshot=fix`, Go `-update` golden flags, Flutter `--update-goldens`, " +
		"`cargo insta accept`, and `UPDATE_SNAPSHOTS`/`INSTA_UPDATE=always`/`INSTA_FORCE_PASS`/`TRYCMD=overwrite` " +
		"variables. Dedicated update scripts and CI jobs (named for updating, or committing the result) are fine.\n\n" +
		"Info (at most once per change): existing snapshots rewritten in a change that also edits source " +
		"code, when no changed line accounts for the new output — the new output may have been accepted " +
		"rather than checked. A rewrite is accounted for when the words new to the snapshot appear on changed " +
		"lines (an edited prompt string, a renamed token, a removed setting, a new component's id), when the " +
		"test or fixture the snapshot is named after changed, or when only metadata, order or structure " +
		"changed; a rewrite that changes only values (`$12.99` → `$13`) needs the same values on changed lines.",
}

func init() {
	// Fixture files are left out of what the rule reports on, but they
	// still explain rewritten snapshots (a changed test input).
	snapshotForced.Check = func(a *core.Analysis) []core.Finding { return checkSnapshot(scopedView(a), a) }
	core.Register(snapshotForced)
}

func checkSnapshot(a, full *core.Analysis) []core.Finding {
	var out []core.Finding
	out = append(out, snapshotCommands(a)...)
	out = append(out, snapshotEnvKeys(a)...)
	out = append(out, snapshotRewrites(a, full)...)
	return out
}

// updateIntentRe matches names of scripts, targets and jobs whose purpose
// is updating snapshots.
var updateIntentRe = regexp.MustCompile(`(?i)(update|snap|golden|regen|accept|bless|record|approve|refresh|overwrite)`)

// snapshotUpdate returns the flag or variable that makes an invocation
// rewrite snapshots, or "".
func snapshotUpdate(iv invocation) string {
	for k, v := range iv.Env {
		if r := snapshotEnv(k, v); r != "" {
			return r
		}
	}
	c, ok := classify(iv)
	if !ok {
		if iv.Prog() == "cargo" && firstPositional(iv.Args[1:]) == "insta" && hasArg(iv.Args, "accept", "review") {
			return "cargo insta " + positionalAt(iv.Args[1:], 1)
		}
		return ""
	}
	args := iv.Args[1:]
	has := func(spellings ...string) string {
		for _, a := range args {
			for _, s := range spellings {
				if a == s || strings.HasPrefix(a, s+"=") && !strings.HasSuffix(a, "=false") && !strings.HasSuffix(a, "=missing") && !strings.HasSuffix(a, "=none") && !strings.HasSuffix(a, "=create") {
					return a
				}
			}
		}
		return ""
	}
	prog := iv.Prog()
	switch {
	case prog == "jest" || prog == "vitest" || prog == "react-scripts" || prog == "craco" || c.Eco == ecoJS && (c.Tool == prog+" test" || strings.HasPrefix(c.Tool, prog+" run ")):
		if f := has("-u", "--updateSnapshot", "--update", "--update-snapshots"); f != "" {
			return f
		}
		for _, a := range args {
			if a == "--ci=false" {
				return a
			}
		}
	case c.Tool == "playwright test" || c.Tool == "bun test":
		return has("-u", "--update-snapshots")
	case c.Eco == ecoPython && c.Category == catTest:
		if f := has("--snapshot-update", "--force-regen", "--regen-all"); f != "" {
			return f
		}
		// inline-snapshot: only "fix" and "update" rewrite existing values;
		// create, trim, review, report and disable do not hide a mismatch.
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--inline-snapshot="); ok {
				for _, mode := range strings.Split(v, ",") {
					if mode == "fix" || mode == "update" {
						return a
					}
				}
			}
		}
	case prog == "go" || prog == "gotestsum":
		return has("-update", "--update", "-update-golden", "-update-goldens", "-golden", "-update_golden")
	case prog == "flutter":
		return has("--update-goldens")
	case prog == "deno":
		return has("--update", "-u")
	case prog == "cargo":
		if firstPositional(args) == "insta" {
			return has("--accept", "--force-update-snapshots")
		}
	}
	return ""
}

// snapshotEnv returns NAME=value when an environment variable forces
// snapshot updates.
func snapshotEnv(name, value string) string {
	value = strings.Trim(strings.TrimSpace(value), `"'`)
	v := strings.ToLower(value)
	switch strings.ToUpper(name) {
	case "UPDATE_SNAPSHOTS", "UPDATE_SNAPSHOT", "SNAPSHOT_UPDATE", "SNAPSHOTS_UPDATE", "UPDATE_GOLDEN", "UPDATE_GOLDENS", "INSTA_FORCE_PASS":
		if truthy(v) {
			return name + "=" + value
		}
	case "INSTA_UPDATE":
		if v == "always" || v == "unseen" {
			return name + "=" + value
		}
	case "TRYCMD", "SNAPSHOTS":
		if v == "overwrite" {
			return name + "=" + value
		}
	}
	return ""
}

// updateContext reports whether a command belongs to something meant to
// update snapshots: a script/target named so, or a CI job named so or one
// that commits its results.
func updateContext(f *core.FileInfo, c cmdLine) bool {
	if updateIntentRe.MatchString(c.Owner) {
		return true
	}
	if kindOf(f) != kindCIYAML {
		return false
	}
	s := newSide(f)
	job, step := yamlOwner(s, c.Line)
	if updateIntentRe.MatchString(job) || updateIntentRe.MatchString(step) {
		return true
	}
	// A job that commits or opens a PR with the regenerated files.
	anc := yamlAncestors(s, c.Line)
	for _, a := range anc {
		if a.Key == job && job != "" {
			text := strings.Join(linesText(s, a.Line, blockEnd(s, a.Line, a.Indent)), "\n")
			return strings.Contains(text, "git commit") || strings.Contains(text, "git push") ||
				strings.Contains(text, "create-pull-request") || strings.Contains(text, "auto-commit") ||
				strings.Contains(text, "git-auto-commit")
		}
	}
	return false
}

func linesText(s side, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		if l, ok := s.get(i); ok {
			out = append(out, l.NoComment)
		}
	}
	return out
}

// snapshotCommands reports test commands that gained a snapshot-update
// flag or variable.
func snapshotCommands(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range commandFiles(a) {
		added, removed := changedCommands(f)
		before := multiset{}
		for _, c := range removed {
			for _, iv := range invocations(c.Text) {
				if u := snapshotUpdate(iv); u != "" {
					before.add(u)
				}
			}
		}
		addSet := lineSet(f.Added)
		for _, c := range added {
			if updateContext(f, c) {
				continue
			}
			if kindOf(f) == kindScripts || kindOf(f) == kindMake || kindOf(f) == kindPyproject {
				// only scripts and targets that are the test entry points
				if _, ok := verificationName(c.Owner); !ok && c.Owner != "" {
					continue
				}
			}
			for _, iv := range invocations(c.Text) {
				u := snapshotUpdate(iv)
				if u == "" || before.take(u) {
					continue
				}
				where := ""
				if c.Owner != "" {
					where = " in " + ownerLabel(kindOf(f), c.Owner)
				} else if d := describeOwner(yamlOwner(newSide(f), c.Line)); d != "" && kindOf(f) == kindCIYAML {
					where = " in " + d
				}
				out = append(out, snapshotForced.Finding(f, firstChanged(c, addSet),
					fmt.Sprintf("%s%s runs with `%s`: snapshots are rewritten instead of checked, so mismatches can never fail", quote(iv.String()), where, u)))
			}
		}
	}
	return out
}

var envKeyRe = regexp.MustCompile(`^\s*(?:-\s+)?([A-Z][A-Z0-9_]*)\s*[:=]\s*(.+?)\s*$`)

// snapshotEnvKeys reports snapshot-update variables added to CI env blocks.
func snapshotEnvKeys(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range a.Files {
		if kindOf(f) != kindCIYAML {
			continue
		}
		removed := multiset{}
		for _, l := range f.Removed {
			if m := envKeyRe.FindStringSubmatch(l.NoComment); m != nil {
				removed.add(m[1] + "=" + m[2])
			}
		}
		for _, l := range f.Added {
			m := envKeyRe.FindStringSubmatch(l.NoComment)
			if m == nil || removed.take(m[1]+"="+m[2]) {
				continue
			}
			u := snapshotEnv(m[1], m[2])
			if u == "" || updateContext(f, cmdLine{Line: l.No, End: l.No}) {
				continue
			}
			where := "the pipeline"
			if d := describeOwner(yamlOwner(newSide(f), l.No)); d != "" {
				where = d
			}
			out = append(out, snapshotForced.Finding(f, l.No,
				fmt.Sprintf("`%s` set for %s: snapshot tests there rewrite their snapshots instead of failing", u, where)))
		}
	}
	return out
}

// ---- rewritten snapshots ----

var jestExportRe = regexp.MustCompile("^exports\\[`(.*)`\\] = ")

// snapshotEntries splits a Jest/Vitest .snap file into name → body.
func snapshotEntries(content []byte) map[string]string {
	out := map[string]string{}
	name := ""
	var body strings.Builder
	for _, line := range diff.SplitLines(string(content)) {
		if m := jestExportRe.FindStringSubmatch(line); m != nil {
			if name != "" {
				out[name] = body.String()
			}
			name = m[1]
			body.Reset()
		}
		if name != "" {
			body.WriteString(line + "\n")
		}
	}
	if name != "" {
		out[name] = body.String()
	}
	return out
}

// snapshotRewrites notes existing snapshots that were rewritten in a
// change that also edits source code, when the change does not explain
// the new output. A snapshot whose new words come from the changed lines
// of the change itself (an edited prompt string, a renamed token, a
// removed setting, a new component's name, a changed test input) is the
// intended result of the change; one whose output changed in ways no
// changed line accounts for (a number, a word nobody wrote) may have been
// accepted rather than checked. At most one note per change.
func snapshotRewrites(a, full *core.Analysis) []core.Finding {
	var source []string
	for _, f := range a.Files {
		if f.Lang.IsProgramming() && !f.IsTest && !core.IsSnapshotPath(f.Path) && !f.IsCI && f.Config == "" &&
			(len(f.Added) > 0 || len(f.Removed) > 0) {
			source = append(source, f.Path)
		}
	}
	if len(source) == 0 {
		return nil
	}
	var expl *snapTokens
	type rewrite struct {
		f       *core.FileInfo
		entries []string
	}
	var unexplained []rewrite
	for _, f := range a.Files {
		if !core.IsSnapshotPath(f.Path) || f.Diff.Kind != diff.Modified || len(f.Removed) == 0 {
			continue
		}
		oldB, ok1 := f.OldContent()
		newB, ok2 := f.NewContent()
		var rewritten []string
		if ok1 && ok2 && strings.HasSuffix(f.Path, ".snap") && strings.Contains(string(oldB), "exports[`") {
			oe, ne := snapshotEntries(oldB), snapshotEntries(newB)
			for name, body := range oe {
				if nb, ok := ne[name]; ok && nb != body {
					rewritten = append(rewritten, name)
				}
			}
			if len(rewritten) == 0 {
				continue // only snapshots added or removed
			}
		} else if len(f.Added) == 0 {
			continue
		}
		if linkedInput(f, full) {
			continue
		}
		if expl == nil {
			expl = explanationTokens(full)
		}
		if snapshotExplained(f, expl) {
			continue
		}
		sort.Strings(rewritten)
		unexplained = append(unexplained, rewrite{f, rewritten})
	}
	if len(unexplained) == 0 {
		return nil
	}
	first := unexplained[0]
	f := first.f
	what := "a snapshot was rewritten"
	switch {
	case len(first.entries) == 1:
		what = fmt.Sprintf("existing snapshot %q rewritten", first.entries[0])
	case len(first.entries) > 1:
		what = plural(len(first.entries), "existing snapshot") + " rewritten"
	}
	where := "in " + path.Base(f.Path)
	if len(unexplained) > 1 {
		where += fmt.Sprintf(" (and %s)", plural(len(unexplained)-1, "other snapshot file"))
	}
	also := nearestPath(f.Path, source)
	if len(source) > 1 {
		also = fmt.Sprintf("%s and %d other source files", also, len(source)-1)
	}
	line := f.AnchorForOld(f.Removed[0].No)
	if len(f.Added) > 0 {
		line = f.Added[0].No
	}
	return []core.Finding{sevFinding(snapshotForced, f, line, core.Info,
		fmt.Sprintf("%s %s alongside changes to %s, and no changed line accounts for the new output: check that it is intended, not just accepted", what, where, also))}
}

// nearestPath returns the path of ps that shares the longest directory
// prefix with p (the source next to the snapshot's test).
func nearestPath(p string, ps []string) string {
	best, bestN := ps[0], -1
	segs := strings.Split(p, "/")
	for _, q := range ps {
		qs := strings.Split(q, "/")
		n := 0
		for n < len(segs)-1 && n < len(qs)-1 && segs[n] == qs[n] {
			n++
		}
		if n > bestN {
			best, bestN = q, n
		}
	}
	return best
}

// snapTokens are the words and numbers on the changed lines of the
// change's other files: what can explain a snapshot's new content.
type snapTokens struct {
	words, nums map[string]bool
}

var (
	snapWordRe = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)
	snapNumRe  = regexp.MustCompile(`\d+(?:\.\d+)*`)
	// idLikeRe matches hashes, ids and other generated tokens.
	idLikeRe = regexp.MustCompile(`^[0-9a-fA-F]{8,}$|\d.*\d.*\d`)
	// hexRunRe matches hex runs of hashes and UUIDs, and long ids.
	hexRunRe = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b|\b[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}\b`)
	// snapMetaRe matches insta snapshot header lines.
	snapMetaRe = regexp.MustCompile(`^(assertion_line|source|expression|input_file|snapshot_kind|info|description):`)
)

const maxExplLines = 50000

// explanationTokens collects the tokens of every changed non-snapshot line.
func explanationTokens(a *core.Analysis) *snapTokens {
	t := &snapTokens{words: map[string]bool{}, nums: map[string]bool{}}
	n := 0
	for _, f := range a.Files {
		if core.IsSnapshotPath(f.Path) {
			continue
		}
		for _, ls := range [][]core.Line{f.Added, f.Removed} {
			for _, l := range ls {
				if n++; n > maxExplLines {
					return t
				}
				for _, w := range snapWords(l.Raw) {
					t.words[w] = true
				}
				for _, d := range snapNumRe.FindAllString(l.Raw, -1) {
					t.nums[d] = true
				}
			}
		}
	}
	return t
}

// snapTextTokens adds the words (3+ characters, not id-like) and numbers
// of a snapshot line to words and nums, skipping insta metadata headers.
func snapTextTokens(raw string, words, nums map[string]bool) {
	if snapMetaRe.MatchString(raw) {
		return
	}
	raw = hexRunRe.ReplaceAllString(raw, " ") // hashes, UUIDs, ids
	for _, w := range snapWords(raw) {
		if len(w) >= 3 && !idLikeRe.MatchString(w) {
			words[w] = true
		}
	}
	for _, d := range snapNumRe.FindAllString(raw, -1) {
		nums[d] = true
	}
}

// snapWords splits text into words; snake_case names count as their parts,
// so a parametrized test's snapshot name matches the test that produced it.
func snapWords(text string) []string {
	var out []string
	for _, w := range snapWordRe.FindAllString(text, -1) {
		if strings.ContainsAny(w, "_$") {
			for _, part := range strings.FieldsFunc(w, func(r rune) bool { return r == '_' || r == '$' }) {
				out = append(out, part)
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// snapLineTokens returns the tokens of changed snapshot lines.
func snapLineTokens(ls []core.Line) (words, nums map[string]bool) {
	words, nums = map[string]bool{}, map[string]bool{}
	for _, l := range ls {
		snapTextTokens(l.Raw, words, nums)
	}
	return words, nums
}

// snapSideTokens returns the tokens of every known line of a snapshot
// side: the whole file, or for a bare patch its hunks (context included).
func snapSideTokens(s side) (words, nums map[string]bool) {
	words, nums = map[string]bool{}, map[string]bool{}
	for i := 1; i <= len(s.lines); i++ {
		if l, ok := s.get(i); ok {
			snapTextTokens(l.Raw, words, nums)
		}
	}
	return words, nums
}

// novel returns the keys of a that are not in b.
func novel(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	return out
}

// mostlyIn reports whether at least 80% of ks are in set (all of them
// when there are fewer than five).
func mostlyIn(ks []string, set map[string]bool) bool {
	miss := 0
	for _, k := range ks {
		if !set[k] {
			miss++
		}
	}
	return miss <= len(ks)/5
}

// snapshotExplained reports whether the changed lines of the rest of the
// change account for a snapshot's rewrite: the words new to the snapshot
// (or, when it only lost words, the words it lost) appear on changed
// lines, or it changed only metadata, order or structure. A rewrite that
// changed only numbers is explained only by the same numbers on changed
// lines: a value nobody wrote ($12.99 → $13) is what an accepted
// regression looks like.
func snapshotExplained(f *core.FileInfo, t *snapTokens) bool {
	aw, an := snapLineTokens(f.Added)
	rw, rn := snapLineTokens(f.Removed)
	// New to the snapshot means absent from the whole other side, not
	// just from the changed lines: a new entry that repeats the structure
	// of the existing ones brings only its own new words.
	ow, on := snapSideTokens(oldSide(f))
	nw, _ := snapSideTokens(newSide(f))
	gainedW, lostW := novel(aw, ow), novel(rw, nw)
	rn = on
	switch {
	case len(gainedW) > 0:
		return mostlyIn(gainedW, t.words)
	case len(lostW) > 0:
		return mostlyIn(lostW, t.words)
	}
	gainedN := novel(an, rn)
	if len(gainedN) == 0 {
		// no new word or number: moved, reordered or removed lines, or
		// only metadata headers
		return true
	}
	return mostlyIn(gainedN, t.nums)
}

// linkedInput reports whether the change edits the input a snapshot was
// produced from: a file whose name the snapshot's file name embeds
// (PriceTag.test.tsx.snap and PriceTag.test.tsx, insta's
// invalid_syntax@unicode_name.py.snap and its unicode_name.py fixture).
// New output for a changed input is expected.
func linkedInput(f *core.FileInfo, full *core.Analysis) bool {
	name := path.Base(f.Path)
	for _, g := range full.Files {
		if g == f || core.IsSnapshotPath(g.Path) || len(g.Added)+len(g.Removed) == 0 {
			continue
		}
		if b := path.Base(g.Path); len(b) >= 5 && strings.Contains(b, ".") && strings.Contains(name, b) {
			return true
		}
	}
	return false
}
