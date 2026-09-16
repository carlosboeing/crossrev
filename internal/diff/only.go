package diff

// Only is the diff with only the named files' sections kept: a section stays
// when either side's path is one of the given paths, exactly, and everything
// else drops — including anything before the first `diff --git`, which
// belongs to no file. This is the batch prompt's slice: a batch carries its
// own files' hunks rather than the whole pull-request diff, so a large change
// the batch does not hold cannot price a small batch out of the prompt
// budget. Matching is exact rather than Excluded's directory semantics,
// because the paths name this batch's files, not operator-supplied
// directories.
//
// Like Excluded, every kept line ends with a newline whether the input's last
// line did or not. Unlike Excluded, no paths means no output: keeping
// everything on an empty list would re-admit the whole diff the slice exists
// to leave out.
func (d *Diff) Only(paths []string) []byte {
	keep := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p != "" {
			keep[p] = true
		}
	}
	var out []byte
	for _, l := range d.lines {
		s := d.sections[l.section]
		if !keep[s.pathA] && !keep[s.pathB] {
			continue
		}
		out = append(out, l.raw...)
		out = append(out, '\n')
	}
	return out
}
