package core

import (
	"path"
	"strings"
)

// MatchGlob matches a slash-separated path against a gitignore-style glob:
//
//   - any run of characters except '/'
//     **     any run of characters including '/' (whole path segments)
//     ?      one character except '/'
//     [abc]  character class (as in path.Match)
//
// A pattern without a '/' matches the file's base name or any directory
// name on its path. A trailing '/' matches everything below a directory.
// A leading '/' anchors the pattern at the repository root.
func MatchGlob(pattern, p string) bool {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	anchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	if !anchored && !strings.Contains(pattern, "/") {
		for _, seg := range strings.Split(p, "/") {
			if ok, _ := path.Match(pattern, seg); ok {
				return true
			}
		}
		return false
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			pat = pat[1:]
			if len(pat) == 0 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], segs[0]); !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// MatchAny reports whether p matches any of the globs.
func MatchAny(globs []string, p string) bool {
	for _, g := range globs {
		if MatchGlob(g, p) {
			return true
		}
	}
	return false
}
