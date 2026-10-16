// Package diff parses unified diffs (as produced by git) and computes
// line diffs between two in-memory file versions.
package diff

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Kind describes what happened to a file in a diff.
type Kind int

const (
	Modified Kind = iota
	Added
	Deleted
	Renamed
	Copied
)

func (k Kind) String() string {
	switch k {
	case Added:
		return "added"
	case Deleted:
		return "deleted"
	case Renamed:
		return "renamed"
	case Copied:
		return "copied"
	default:
		return "modified"
	}
}

// Op is the kind of a single diff line.
type Op byte

const (
	Context Op = ' '
	Add     Op = '+'
	Del     Op = '-'
)

// Line is one line inside a hunk. OldNo/NewNo are 1-based line numbers in
// the old/new file; 0 when the line does not exist on that side.
type Line struct {
	Op    Op
	Text  string
	OldNo int
	NewNo int
}

// Hunk is a contiguous region of change.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Section            string // text after the closing @@, often a function signature
	Lines              []Line
}

// FileDiff is the diff of a single file.
type FileDiff struct {
	OldPath    string // "" when the file was added
	NewPath    string // "" when the file was deleted
	Kind       Kind
	Binary     bool
	Similarity int // rename/copy similarity percentage, if reported
	Hunks      []Hunk

	hdr bool // ---/+++ header already consumed
}

// Path returns the most meaningful path for the file: the new path, or the
// old path for deletions.
func (f *FileDiff) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Added returns all added lines across hunks.
func (f *FileDiff) Added() []Line { return f.lines(Add) }

// Removed returns all removed lines across hunks.
func (f *FileDiff) Removed() []Line { return f.lines(Del) }

func (f *FileDiff) lines(op Op) []Line {
	var out []Line
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Op == op {
				out = append(out, l)
			}
		}
	}
	return out
}

// Stats returns the number of added and removed lines.
func (f *FileDiff) Stats() (added, removed int) {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch l.Op {
			case Add:
				added++
			case Del:
				removed++
			}
		}
	}
	return
}

// Parse reads a unified diff (git or plain `diff -u` style) and returns the
// file diffs it contains. Lines outside file sections (commit headers,
// e-mail headers, etc.) are ignored.
func Parse(r io.Reader) ([]*FileDiff, error) {
	p := &parser{sc: bufio.NewScanner(r)}
	p.sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	return p.parse()
}

// ParseString is a convenience wrapper around Parse.
func ParseString(s string) ([]*FileDiff, error) { return Parse(strings.NewReader(s)) }

type parser struct {
	sc      *bufio.Scanner
	peeked  *string
	lineNum int
}

func (p *parser) next() (string, bool) {
	if p.peeked != nil {
		s := *p.peeked
		p.peeked = nil
		return s, true
	}
	if !p.sc.Scan() {
		return "", false
	}
	p.lineNum++
	return strings.TrimSuffix(p.sc.Text(), "\r"), true
}

func (p *parser) unread(s string) { p.peeked = &s }

func (p *parser) parse() ([]*FileDiff, error) {
	var files []*FileDiff
	var cur *FileDiff
	for {
		line, ok := p.next()
		if !ok {
			break
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &FileDiff{}
			a, b := parseGitHeaderPaths(line[len("diff --git "):])
			cur.OldPath, cur.NewPath = a, b
			files = append(files, cur)
		case cur != nil && strings.HasPrefix(line, "new file mode"):
			cur.Kind = Added
		case cur != nil && strings.HasPrefix(line, "deleted file mode"):
			cur.Kind = Deleted
		case cur != nil && strings.HasPrefix(line, "rename from "):
			cur.Kind = Renamed
			cur.OldPath = unquote(line[len("rename from "):])
		case cur != nil && strings.HasPrefix(line, "rename to "):
			cur.Kind = Renamed
			cur.NewPath = unquote(line[len("rename to "):])
		case cur != nil && strings.HasPrefix(line, "copy from "):
			cur.Kind = Copied
			cur.OldPath = unquote(line[len("copy from "):])
		case cur != nil && strings.HasPrefix(line, "copy to "):
			cur.Kind = Copied
			cur.NewPath = unquote(line[len("copy to "):])
		case cur != nil && (strings.HasPrefix(line, "similarity index ") || strings.HasPrefix(line, "dissimilarity index ")):
			v := strings.TrimSuffix(line[strings.LastIndexByte(line, ' ')+1:], "%")
			cur.Similarity, _ = strconv.Atoi(v)
		case cur != nil && strings.HasPrefix(line, "Binary files "):
			cur.Binary = true
		case cur != nil && line == "GIT binary patch":
			cur.Binary = true
		case strings.HasPrefix(line, "--- "):
			nextLine, ok := p.next()
			if !ok || !strings.HasPrefix(nextLine, "+++ ") {
				if ok {
					p.unread(nextLine)
				}
				continue
			}
			oldP := stripPrefix(unquote(trimTimestamp(line[4:])))
			newP := stripPrefix(unquote(trimTimestamp(nextLine[4:])))
			if cur == nil || len(cur.Hunks) > 0 || cur.hdr {
				// Plain unified diff without a "diff --git" header.
				cur = &FileDiff{}
				files = append(files, cur)
			}
			cur.hdr = true
			if oldP == "/dev/null" {
				cur.Kind = Added
				cur.OldPath = ""
			} else {
				cur.OldPath = oldP
			}
			if newP == "/dev/null" {
				cur.Kind = Deleted
				cur.NewPath = ""
			} else {
				cur.NewPath = newP
			}
		case strings.HasPrefix(line, "@@ "):
			if cur == nil {
				cur = &FileDiff{}
				files = append(files, cur)
			}
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("diff line %d: %w", p.lineNum, err)
			}
			if err := p.readHunkBody(&h); err != nil {
				return nil, err
			}
			cur.Hunks = append(cur.Hunks, h)
		}
	}
	for _, f := range files {
		f.finalize()
	}
	return files, nil
}

func (f *FileDiff) finalize() {
	if f.Kind == Added {
		f.OldPath = ""
	}
	if f.Kind == Deleted {
		f.NewPath = ""
	}
	if f.Kind == Modified && f.OldPath != "" && f.NewPath != "" && f.OldPath != f.NewPath {
		f.Kind = Renamed
	}
}

func (p *parser) readHunkBody(h *Hunk) error {
	oldLeft, newLeft := h.OldLines, h.NewLines
	oldNo, newNo := h.OldStart, h.NewStart
	for oldLeft > 0 || newLeft > 0 {
		line, ok := p.next()
		if !ok {
			return nil // truncated diff: keep what we have
		}
		if line == "" {
			// Some tools strip the single space of empty context lines.
			line = " "
		}
		switch line[0] {
		case ' ':
			h.Lines = append(h.Lines, Line{Op: Context, Text: line[1:], OldNo: oldNo, NewNo: newNo})
			oldNo++
			newNo++
			oldLeft--
			newLeft--
		case '-':
			h.Lines = append(h.Lines, Line{Op: Del, Text: line[1:], OldNo: oldNo})
			oldNo++
			oldLeft--
		case '+':
			h.Lines = append(h.Lines, Line{Op: Add, Text: line[1:], NewNo: newNo})
			newNo++
			newLeft--
		case '\\':
			// "\ No newline at end of file"
		default:
			p.unread(line)
			return nil
		}
	}
	// Consume a trailing "\ No newline" marker if present.
	if line, ok := p.next(); ok {
		if !strings.HasPrefix(line, `\`) {
			p.unread(line)
		}
	}
	return nil
}

func parseHunkHeader(line string) (Hunk, error) {
	// @@ -l,s +l,s @@ section
	var h Hunk
	rest := strings.TrimPrefix(line, "@@ ")
	end := strings.Index(rest, " @@")
	if end < 0 {
		return h, fmt.Errorf("malformed hunk header %q", line)
	}
	ranges := strings.Fields(rest[:end])
	if len(ranges) < 2 {
		return h, fmt.Errorf("malformed hunk header %q", line)
	}
	var err error
	if h.OldStart, h.OldLines, err = parseRange(strings.TrimPrefix(ranges[0], "-")); err != nil {
		return h, err
	}
	if h.NewStart, h.NewLines, err = parseRange(strings.TrimPrefix(ranges[len(ranges)-1], "+")); err != nil {
		return h, err
	}
	h.Section = strings.TrimSpace(rest[end+3:])
	return h, nil
}

func parseRange(s string) (start, count int, err error) {
	count = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		if count, err = strconv.Atoi(s[i+1:]); err != nil {
			return
		}
		s = s[:i]
	}
	start, err = strconv.Atoi(s)
	return
}

// parseGitHeaderPaths splits `a/foo b/foo` (possibly quoted) into paths
// without their a/ b/ prefixes.
func parseGitHeaderPaths(s string) (string, string) {
	if strings.HasPrefix(s, `"`) {
		a, rest := readQuoted(s)
		rest = strings.TrimSpace(rest)
		b := rest
		if strings.HasPrefix(rest, `"`) {
			b, _ = readQuoted(rest)
		}
		return stripPrefix(a), stripPrefix(b)
	}
	// Unquoted: both halves are usually identical, so split in the middle
	// on " b/" when possible.
	if i := strings.Index(s, " b/"); i >= 0 {
		return stripPrefix(s[:i]), stripPrefix(s[i+1:])
	}
	if i := strings.LastIndex(s, ` "`); i >= 0 {
		b, _ := readQuoted(s[i+1:])
		return stripPrefix(s[:i]), stripPrefix(b)
	}
	half := len(s) / 2
	return stripPrefix(s[:half]), stripPrefix(strings.TrimSpace(s[half:]))
}

func stripPrefix(p string) string {
	if p == "/dev/null" {
		return p
	}
	for _, pre := range []string{"a/", "b/", "i/", "w/", "c/", "o/"} {
		if strings.HasPrefix(p, pre) {
			return p[2:]
		}
	}
	return p
}

func trimTimestamp(s string) string {
	// Plain diff -u adds "\t2024-01-01 ..." after the path.
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		return s[:i]
	}
	return s
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		q, _ := readQuoted(s)
		return q
	}
	return s
}

// readQuoted reads a C-style quoted string as emitted by git and returns the
// decoded value and the remainder of the input.
func readQuoted(s string) (string, string) {
	if !strings.HasPrefix(s, `"`) {
		return s, ""
	}
	var buf bytes.Buffer
	i := 1
	for i < len(s) {
		c := s[i]
		if c == '"' {
			return buf.String(), s[i+1:]
		}
		if c == '\\' && i+1 < len(s) {
			i++
			switch e := s[i]; e {
			case 'n':
				buf.WriteByte('\n')
			case 't':
				buf.WriteByte('\t')
			case 'r':
				buf.WriteByte('\r')
			case 'a':
				buf.WriteByte('\a')
			case 'b':
				buf.WriteByte('\b')
			case 'f':
				buf.WriteByte('\f')
			case 'v':
				buf.WriteByte('\v')
			case '0', '1', '2', '3':
				if i+2 < len(s) {
					if v, err := strconv.ParseUint(s[i:i+3], 8, 8); err == nil {
						buf.WriteByte(byte(v))
						i += 2
						break
					}
				}
				buf.WriteByte(e)
			default:
				buf.WriteByte(e)
			}
			i++
			continue
		}
		buf.WriteByte(c)
		i++
	}
	return buf.String(), ""
}
