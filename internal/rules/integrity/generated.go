package integrity

import (
	"github.com/huyth45/narc/internal/core"
)

var generatedMarker = &core.Rule{
	ID:       "generated-marker-added",
	Title:    "Hand-written file marked as generated",
	Category: "integrity",
	Severity: core.Error,
	Summary:  "A \"generated\" marker (@generated, Code generated ... DO NOT EDIT, auto-generated) was added to an existing hand-written file, which hides it from narc, linters and coverage.",
	Hint:     "Remove the marker. Only files produced by a code generator should carry it; if this file really is generated now, regenerate it from its source in the same change.",
	Docs: "Many tools, narc included, skip generated files. Adding a generated marker to a hand-written file is " +
		"an easy way to make every check ignore it. narc keeps analyzing such files and reports the marker.\n\n" +
		"```ts\n// @generated            <- generated-marker-added\nexport function total(lines: Line[]) { ... }\n```\n\n" +
		"When most of the file was rewritten at the same time (a real switch to code generation), the finding is a warning.",
}

func init() {
	generatedMarker.Check = checkGeneratedMarker
	core.Register(generatedMarker)
}

func checkGeneratedMarker(a *core.Analysis) []core.Finding {
	var out []core.Finding
	for _, f := range a.Files {
		oldB, hasOld := f.OldContent()
		newB, hasNew := f.NewContent()
		if !hasOld || !hasNew || core.IsGeneratedContent(oldB) || !core.IsGeneratedContent(newB) {
			continue
		}
		line := 0
		for _, l := range f.Added {
			if core.IsGeneratedContent([]byte(l.Raw)) {
				line = l.No
				break
			}
		}
		fd := generatedMarker.Finding(f, line, "a generated-file marker was added to a hand-written file, so tools that skip generated code (narc included) would ignore it")
		if add, del := f.Diff.Stats(); del > 0 && del*2 > len(f.OldLines()) && add > 0 {
			fd.Severity = core.Warning // most of the file was replaced: maybe a real switch to codegen
		}
		out = append(out, fd)
	}
	return out
}
