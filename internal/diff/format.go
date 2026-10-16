package diff

import (
	"fmt"
	"strings"
)

// Format renders file diffs as a git-style unified diff.
func Format(files []*FileDiff) string {
	var b strings.Builder
	for _, f := range files {
		oldP, newP := f.OldPath, f.NewPath
		if oldP == "" {
			oldP = newP
		}
		if newP == "" {
			newP = oldP
		}
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", oldP, newP)
		switch f.Kind {
		case Added:
			b.WriteString("new file mode 100644\n")
		case Deleted:
			b.WriteString("deleted file mode 100644\n")
		case Renamed:
			fmt.Fprintf(&b, "rename from %s\nrename to %s\n", f.OldPath, f.NewPath)
		}
		if f.Binary {
			fmt.Fprintf(&b, "Binary files a/%s and b/%s differ\n", oldP, newP)
			continue
		}
		if len(f.Hunks) == 0 {
			continue
		}
		if f.Kind == Added {
			b.WriteString("--- /dev/null\n")
		} else {
			fmt.Fprintf(&b, "--- a/%s\n", f.OldPath)
		}
		if f.Kind == Deleted {
			b.WriteString("+++ /dev/null\n")
		} else {
			fmt.Fprintf(&b, "+++ b/%s\n", f.NewPath)
		}
		for _, h := range f.Hunks {
			fmt.Fprintf(&b, "@@ -%s +%s @@", fmtRange(h.OldStart, h.OldLines), fmtRange(h.NewStart, h.NewLines))
			if h.Section != "" {
				b.WriteString(" " + h.Section)
			}
			b.WriteByte('\n')
			for _, l := range h.Lines {
				b.WriteByte(byte(l.Op))
				b.WriteString(l.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func fmtRange(start, n int) string {
	if n == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}
