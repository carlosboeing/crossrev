package main

import "testing"

// The peeled form (`refs/tags/v0.6.2^{}`) is the commit an annotated tag
// points at; the direct form is a lightweight tag. action.yml reads both and
// so does this, because which kind a release carries is not this code's
// business.
func TestTagForSHAReadsBothTagForms(t *testing.T) {
	const sha = "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"
	out := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/tags/v0.6.1\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\trefs/tags/v0.6.2\n" +
		sha + "\trefs/tags/v0.6.2^{}\n"
	if got := tagForSHA(out, sha); got != "v0.6.2" {
		t.Errorf("tagForSHA = %q, want v0.6.2", got)
	}
}

func TestTagForSHAAnswersUntaggedForACommitNoTagPointsAt(t *testing.T) {
	out := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/tags/v0.6.1\n"
	if got := tagForSHA(out, "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"); got != untaggedRef {
		t.Errorf("tagForSHA = %q, want %q", got, untaggedRef)
	}
}

// A branch or a non-release tag is not a pin comment. action.yml matches
// ^v[0-9]+\.[0-9]+\.[0-9]+$ and so does this.
func TestTagForSHAIgnoresRefsThatAreNotReleaseTags(t *testing.T) {
	const sha = "3cbdb5489de49c1503b82e8a6ce48567a5e9e8f4"
	out := sha + "\trefs/tags/nightly\n" + sha + "\trefs/heads/main\n"
	if got := tagForSHA(out, sha); got != untaggedRef {
		t.Errorf("tagForSHA = %q, want %q", got, untaggedRef)
	}
}
