package integrity

import (
	"fmt"

	"github.com/tranhuuhuy297/narc/internal/core"
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
		// Only files narc analyzes matter; docs that a generator starts
		// owning are not an escape.
		if !f.Lang.IsProgramming() && !f.IsCI && f.Config == "" {
			continue
		}
		oldB, hasOld := f.OldContent()
		newB, hasNew := f.NewContent()
		if !hasOld || !hasNew || core.IsGeneratedContent(oldB) || !core.IsGeneratedContent(newB) {
			continue
		}
		// The marker must be one the change added near the top of the file.
		line := 0
		for _, l := range f.Added {
			if l.No > 40 {
				break
			}
			if core.IsGeneratedContent([]byte(l.Comment)) || core.IsGeneratedContent([]byte(l.Raw)) && l.Comment != "" {
				line = l.No
				break
			}
		}
		if line == 0 {
			continue
		}
		fd := generatedMarker.Finding(f, line, "a generated-file marker was added to a hand-written file, so tools that skip generated code (narc included) would ignore it")
		if add, del := f.Diff.Stats(); del > 0 && del*2 > len(f.OldLines()) && add > 0 {
			fd.Severity = core.Warning // most of the file was replaced: maybe a real switch to codegen
		}
		out = append(out, fd)
	}
	if len(out) >= 3 {
		// Several files at once looks like a code generator taking over.
		for i := range out {
			out[i].Severity = core.Warning
			out[i].Message += fmt.Sprintf(" (%d files gained markers in this change; if a generator now owns them, regenerate rather than hand-edit)", len(out))
		}
	}
	return out
}
