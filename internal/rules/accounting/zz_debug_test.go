package accounting

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/tranhuuhuy297/narc/internal/config"
	"github.com/tranhuuhuy297/narc/internal/core"
	"github.com/tranhuuhuy297/narc/internal/git"
)

func TestZZDebug(t *testing.T) {
	dir, sha := os.Getenv("NARC_DEBUG_REPO"), os.Getenv("NARC_DEBUG_SHA")
	if dir == "" {
		t.Skip()
	}
	r, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := r.CommitChangeset(sha)
	if err != nil {
		t.Fatal(err)
	}
	settings := config.Default().Settings()
	a := &core.Analysis{Settings: settings, Source: cs.Source}
	for _, fd := range cs.Files {
		if fd.Binary || core.IsIgnoredByDefault(fd.Path()) {
			continue
		}
		a.Files = append(a.Files, core.NewFileInfo(fd, cs.Source, settings))
	}
	l := ledgerFor(a)
	want := os.Getenv("NARC_DEBUG_NAMES")
	var v []string
	for k := range l.source().vanished {
		if want == "" || strings.Contains(","+want+",", ","+k+",") {
			v = append(v, k)
		}
	}
	sort.Strings(v)
	fmt.Println("VANISHED", len(l.source().vanished), v)
	for _, n := range strings.Split(want, ",") {
		if n == "" {
			continue
		}
		fmt.Printf("NAME %s vanished=%v touched=%v module=%v defined=%v\n", n, l.source().vanished[n], l.source().touched[n], l.source().modules[n], l.definedNames()[n])
	}
	fmt.Println("sourceChanged", len(l.sourceChanged), "removed", l.sourceRemoved, "added", l.sourceAdded, "flagsRemoved", l.source().flagsRemoved)
	for _, o := range l.deletions {
		fmt.Printf("DEL %s :: %s touched=%v module=%v covers=%v excuse=%q fresh=%d\n", o.file.Path, o.name(), l.refsTouched(o), l.refsModule(o), l.coversModified(o.file), l.excuse(o), l.newTestsIn()[o.file])
	}
	if pf := os.Getenv("NARC_DEBUG_FILE"); pf != "" {
		for _, o := range l.olds {
			if strings.Contains(o.file.Path, pf) {
				mn := ""
				if o.match != nil {
					mn = o.match.name()
				}
				fmt.Printf("OLD %s how=%d match=%q explained=%v vanished=%q\n", o.name(), o.how, mn, o.match == nil && l.explained(o), l.refsVanished(o))
			}
		}
	}
	for _, d := range l.drops {
		fmt.Printf("DROP %s :: %s %d->%d helped=%d via=%q rewritten=%v how=%d lost=%v\n", d.old.file.Path, d.old.name(), d.old.assertions(), d.new.assertions(), d.helped, d.via, d.rewritten, d.new.how, d.lost[0].raw)
	}
}
