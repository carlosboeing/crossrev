// reads.go — the reads block of the review prompt.
//
// The block tells the reviewer which read path this call runs with, in the
// same words whatever the harness: served means CrossRev's read tool is the
// only read path, supplied means the prompt carries everything and there is
// no read tool. It takes the mode as a plain string — prompt is a tier-2
// package and may not import the harness tier — and an empty mode renders
// nothing, so prompts built without a mode keep their bytes exactly.

package prompt

// ReadsBlock renders the reads block for one review call: how the reviewer
// may read files beyond the supplied content, if at all.
func ReadsBlock(mode string) string {
	if mode == "served" {
		return "## Reads\n\n" +
			"You have one file-reading tool in this environment: `read_file`. It is the only " +
			"way to read files — you have no shell command tool and no other file tool, so never " +
			"attempt to run a command or to read a file any other way.\n\n" +
			"`read_file` takes a repository path, a revision (`base` for the pull request base, " +
			"`head` for the pull request head), and an optional line range (`start_line`, " +
			"`end_line`). Read from the revision the evidence calls for. Large results arrive " +
			"cut with the next start line named: continue from there when the rest matters. A " +
			"refused read names its reason instead of content: record the limit and judge from " +
			"the supplied content rather than retrying the same read.\n"
	}
	return "## Reads\n\n" +
		"You have no file-reading tool in this environment. Judge the supplied diff and file " +
		"content alone: never attempt to run a command or to reach for a file outside what you " +
		"were given. If something cannot be checked without reading further, say so in the " +
		"answer and answer anyway.\n"
}
