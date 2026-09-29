package vcs

import (
	"bytes"
	"context"
	"fmt"
	"os"

	_ "embed"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/diff"
	"github.com/carlosboeing/crossrev/internal/intel"
)

//go:embed assets/crossrev.gitattributes
var reviewAttributes []byte

// ReviewAttributes is the embedded CrossRev git attributes file, byte for
// byte with the canonical `assets/crossrev.gitattributes` the sync script
// keeps it identical to. Hunk shaping passes it as `core.attributesFile`,
// so the function context `git diff -W` finds does not depend on the
// operator's git config. The accessor hands back a copy each time, the way
// the embedded skills do: one assignment anywhere would change what every
// later shaping call reads.
func ReviewAttributes() []byte { return bytes.Clone(reviewAttributes) }

// HunkContextRadius is how far context around a changed line a
// function-context hunk keeps: context lines within 100 lines of a changed
// line stay, the rest drop. A `-W` hunk widened to a whole file — a change
// in a language with no function pattern — clips back to this window
// instead.
const HunkContextRadius = 100

// diffAttrSourceMin is the git release whose global `--attr-source` reads
// .gitattributes from a tree-ish for every subcommand: below 2.41 shaping
// omits the flag and the -W hunks read attributes from the worktree, with
// one warning and a doctor line saying so.
const (
	diffAttrSourceMinMajor = 2
	diffAttrSourceMinMinor = 41
)

// ShapedFile is one required file's supplied hunk input: which of the
// three forms it takes, the shaped per-file diff the prompt numbers, and
// the access reason when the form is header-only.
type ShapedFile struct {
	// Form is the supplied form the file takes.
	Form intel.InputForm
	// Diff is the shaped diff: the whole-file hunk, the clipped
	// function-context hunks, or the header lines. Nil when shaping found
	// no header to show, and when the file never shaped at all.
	Diff []byte
	// Reason names the access limit or the changeless kind for a
	// header-only form. Empty for the hunk forms.
	Reason string
}

// HunkDiffSupport reports whether the installed git shapes -W hunks under
// the base tree's attributes. Below 2.41 there is no global --attr-source,
// so shaping omits it and answers one warning naming the version; the
// hunks still shape, and the clip still bounds them. A version git cannot
// parse fails the pass, the way the attribute read refuses a version it
// cannot read: shaping blind would review under unknown semantics.
func (r *Repository) HunkDiffSupport(ctx context.Context) (bool, *Warning, error) {
	output, err := r.Run(ctx, "version")
	if err != nil {
		return false, nil, err
	}
	if !output.OK() {
		return false, nil, fmt.Errorf("git version exited %d: %s", output.ExitCode, output.Stderr)
	}
	version, ok := ParseGitVersion(output.Text())
	if !ok {
		return false, nil, fmt.Errorf("could not parse a git version from %q", output.Text())
	}
	if version.AtLeast(diffAttrSourceMinMajor, diffAttrSourceMinMinor) {
		return true, nil, nil
	}
	return false, &Warning{
		Message: fmt.Sprintf("git %s is older than 2.41, so review hunks are shaped without --attr-source", version.Token),
		Hint:    "Whole-file and function-context hunks still apply. Upgrade git to 2.41 or newer to read .gitattributes from the base revision.",
	}, nil
}

// ShapeFileDiff shapes one required file's supplied hunk input between
// base and head: the whole-file hunk for added, deleted and small files,
// clipped function-context hunks for large edited files, and header lines
// for binary, unreadable and changeless files.
//
// body is the evidence discovery already read — at the head, or at the
// base for a deletion — so the whole-file line count and the byte budget
// come from the same bytes the prompt would otherwise show. binary is the
// NUL-byte signal discovery recorded; unavailableReason is the unit's
// access reason, empty when the bytes read fine. support is what
// HunkDiffSupport answered: shaping omits --attr-source without it.
//
// A git failure on a content form fails the pass, the way a failed
// enumeration does: the hunk was promised and cannot be produced. A git
// failure behind a header-only form degrades to the reason alone, because
// the unit was already degraded and the header is garnish.
func (r *Repository) ShapeFileDiff(ctx context.Context, base, head core.Revision, change core.FileChange, body []byte, binary bool, unavailableReason string, support bool) (ShapedFile, error) {
	rng := base.SHA() + "..." + head.SHA()
	paths := hunkPathspec(change)

	unavailable := unavailableReason != ""
	if unavailable || binary {
		raw, err := r.runHunkDiff(ctx, nil, "", []string{"-U0", "--no-ext-diff", "--no-textconv", "-M", rng}, paths)
		if err != nil {
			return ShapedFile{Form: intel.FormDiffOnly, Reason: intel.DiffOnlyReason(change.Kind, len(body), binary, unavailableReason)}, nil
		}
		return ShapedFile{Form: intel.FormDiffOnly, Diff: diff.Parse(raw, core.RevisionPair{}).Headers(), Reason: intel.DiffOnlyReason(change.Kind, len(body), binary, unavailableReason)}, nil
	}

	// The command follows the size rule; the form follows the parsed
	// output through SelectForm, so a small file whose diff carries no
	// hunks still lands header-only and the two can never disagree on
	// which form a file takes.
	if change.Kind == core.ChangeAdded || change.Kind == core.ChangeDeleted || len(body) <= intel.FullTextMaxBytes {
		lines := countLines(body)
		raw, err := r.runHunkDiff(ctx, nil, "", []string{fmt.Sprintf("-U%d", lines), "--no-ext-diff", "--no-textconv", "-M", rng}, paths)
		if err != nil {
			return ShapedFile{}, err
		}
		parsed := diff.Parse(raw, core.RevisionPair{})
		switch form := intel.SelectForm(change.Kind, len(body), false, false, parsed.HasChanges()); form {
		case intel.FormDiffOnly:
			return ShapedFile{Form: form, Diff: parsed.Headers(), Reason: intel.DiffOnlyReason(change.Kind, len(body), false, "")}, nil
		default:
			return ShapedFile{Form: form, Diff: raw}, nil
		}
	}

	attrs, err := writeReviewAttributes()
	if err != nil {
		return ShapedFile{}, err
	}
	defer os.Remove(attrs)
	global := []string{"-c", "core.attributesFile=" + attrs}
	source := ""
	if support {
		source = "--attr-source=" + base.SHA()
	}
	raw, err := r.runHunkDiff(ctx, global, source, []string{"-W", "--no-ext-diff", "--no-textconv", "-M", rng}, paths)
	if err != nil {
		return ShapedFile{}, err
	}
	parsed := diff.Parse(raw, core.RevisionPair{})
	switch form := intel.SelectForm(change.Kind, len(body), false, false, parsed.HasChanges()); form {
	case intel.FormDiffOnly:
		return ShapedFile{Form: form, Diff: parsed.Headers(), Reason: intel.DiffOnlyReason(change.Kind, len(body), false, "")}, nil
	default:
		return ShapedFile{Form: form, Diff: parsed.ClipHunks(HunkContextRadius)}, nil
	}
}

// runHunkDiff runs one per-file `git diff` with the global options (the
// `-c` and `--attr-source` flags, which sit before the subcommand) and the
// diff options after it. A non-zero exit is an error: without --quiet git
// exits zero however many differences it found, so anything else is git
// refusing the invocation.
func (r *Repository) runHunkDiff(ctx context.Context, global []string, source string, opts, paths []string) ([]byte, error) {
	args := append([]string{}, global...)
	if source != "" {
		args = append(args, source)
	}
	args = append(args, "diff")
	args = append(args, opts...)
	args = append(args, "--")
	args = append(args, paths...)
	output, err := r.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if !output.OK() {
		return nil, fmt.Errorf("git diff exited %d: %s", output.ExitCode, output.Stderr)
	}
	return []byte(output.Stdout), nil
}

// hunkPathspec names the paths one file's diff runs over, literal so a
// name carrying glob magic stays a name. A rename passes both sides: with
// only the new path git cannot pair the sides and reports a delete plus
// an add. A deletion reads through its old path; every other kind through
// its current one.
func hunkPathspec(change core.FileChange) []string {
	if change.OldPath != "" && change.OldPath != change.Path {
		return []string{":(literal)" + change.OldPath, ":(literal)" + change.Path}
	}
	path := change.Path
	if change.Kind == core.ChangeDeleted && change.OldPath != "" {
		path = change.OldPath
	}
	return []string{":(literal)" + path}
}

// countLines counts the lines a -U count must cover for the whole file to
// render as one hunk: every newline, plus one more for a non-empty file
// whose last line is unterminated.
func countLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	n := bytes.Count(body, []byte{'\n'})
	if !bytes.HasSuffix(body, []byte{'\n'}) {
		n++
	}
	return n
}

// writeReviewAttributes materialises the embedded attributes for one
// shaping call. The file lives beside the call rather than in the
// repository so shaping reads no checkout state and leaves none behind;
// the caller removes it.
func writeReviewAttributes() (string, error) {
	f, err := os.CreateTemp("", "crossrev-attributes-*.gitattributes")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(ReviewAttributes()); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
