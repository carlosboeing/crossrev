package diff

import "strings"

// Excluded is the diff with whole file sections dropped: a path that equals one
// of the given paths, or sits under one of them as a directory. Everything else
// passes through byte for byte (lib/diff.sh:213-231).
//
// This exists because the backlog destination can be a path inside the
// repository under review, and a leg that reviews the file its own findings are
// written to reviews its own output.
//
// The patterns are operator-supplied configuration and are compared literally
// rather than as patterns. Interpolating one into a regular expression made a
// dot match any character, dropped the end anchor so `BACKLOG.md` also matched
// `BACKLOG.md.old`, and turned an unbalanced bracket in a filename into a syntax
// error that killed the whole diff. So `docs/*.md` names the one file actually
// called that, and `docs/?.md` names nothing.
//
// When no pattern survives the empty-string filter the diff is returned
// verbatim, terminal newline or not, because the shell short-circuits to `cat`.
// Once the parser does the work every kept line ends with a newline, whether the
// input's last line did or not.
func (d *Diff) Excluded(patterns []string) []byte {
	list := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p != "" {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return append([]byte(nil), d.raw...)
	}

	// The shell joins the paths with newlines and awk splits them again, because
	// a path may contain a space and every other separator can appear in one too.
	// A pattern that itself carries a newline is therefore two patterns, and that
	// is reproduced rather than corrected.
	ex := strings.Split(strings.Join(list, "\n"), "\n")
	for i := range ex {
		// Trailing slashes come off so that `docs/backlog` and `docs/backlog/`
		// mean the same directory.
		ex[i] = strings.TrimRight(ex[i], "/")
	}

	// Either side of a section is checked, not the new one alone, so a rename out
	// of an excluded directory is still dropped.
	drop := make([]bool, len(d.sections))
	for i, s := range d.sections {
		drop[i] = hits(s.pathA, ex) || hits(s.pathB, ex)
	}

	var out []byte
	for _, l := range d.lines {
		if drop[l.section] {
			continue
		}
		out = append(out, l.raw...)
		out = append(out, '\n')
	}
	return out
}

// hits answers whether a path names an excluded file, or sits inside an excluded
// directory (lib/diff.sh:110-118). A section with no path on that side is never
// excluded by it.
func hits(path string, ex []string) bool {
	if path == "" {
		return false
	}
	for _, e := range ex {
		if e == "" {
			continue
		}
		if path == e || strings.HasPrefix(path, e+"/") {
			return true
		}
	}
	return false
}
