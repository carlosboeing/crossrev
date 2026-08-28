package runlog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/runlog"
)

// TestPublishIsIdempotent runs a masked body through a second time, which is
// the case gh_review_comment_create creates every time GitHub refuses to anchor
// a line and it falls back to a plain comment (lib/log.sh:138-141).
func TestPublishIsIdempotent(t *testing.T) {
	var noLog *runlog.Log
	once, err := noLog.Publish("the token is ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	twice, err := noLog.Publish(once)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if twice != once {
		t.Errorf("a second pass changed the body:\n got %q\nwant %q", twice, once)
	}
}

// TestRedactFileRewritesInPlace covers the transcript kept on disk
// (log_redact_file, lib/log.sh:174), including the mode the rewrite leaves
// behind: the file is 0600 afterwards whatever it was before, because the
// rewrite goes through a private temporary file.
func TestRedactFileRewritesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review.attempt-1.stdout")
	if err := os.WriteFile(path, []byte("start\nghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var noLog *runlog.Log
	noLog.RedactFile(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "start\nghp_AAAAAA…[redacted]\nend\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	assertMode(t, path, 0o600)
}

// TestRedactFileLeavesEverythingElseAlone: the Bash function returns early for
// anything that is not a regular file, because it runs from an EXIT trap that
// must not care.
func TestRedactFileLeavesEverythingElseAlone(t *testing.T) {
	dir := t.TempDir()
	var noLog *runlog.Log
	noLog.RedactFile(filepath.Join(dir, "does-not-exist"))
	noLog.RedactFile(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory did not survive: %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %04o, want %04o", path, got, want)
	}
}
