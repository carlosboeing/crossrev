package app

import (
	"os"
	"path/filepath"
	"testing"
)

// The defect this helper exists to close: os.WriteFile applies its mode
// argument on create only, exactly like the shell's umask, so a write onto a
// path somebody had widened to 0644 left the key at 0644 (lib/auth.sh:61-71).
func TestWrite0600SetsTheModeOverAWidenedFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ShoreLogic.loop.pem")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatalf("seeding the destination: %v", err)
	}
	if err := os.Chmod(dest, 0o644); err != nil {
		t.Fatalf("widening the destination: %v", err)
	}

	if err := write0600(dest, []byte("new")); err != nil {
		t.Fatalf("write0600: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if string(body) != "new" {
		t.Fatalf("body = %q, want %q", body, "new")
	}
	if _, err := os.Stat(dest + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the temporary file survived a successful write: %v", err)
	}
}

// A temporary left behind by an interrupted run would be written onto rather
// than created, which is the same defect one path along.
func TestWrite0600DoesNotInheritTheModeOfALeftoverTemporary(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ShoreLogic.loop.pem")
	if err := os.WriteFile(dest+".tmp", []byte("interrupted"), 0o644); err != nil {
		t.Fatalf("seeding the temporary: %v", err)
	}
	if err := os.Chmod(dest+".tmp", 0o644); err != nil {
		t.Fatalf("widening the temporary: %v", err)
	}

	if err := write0600(dest, []byte("new")); err != nil {
		t.Fatalf("write0600: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

// A write it cannot make returns non-zero and removes its own temporary file,
// so the call site can refuse by name rather than continuing over a key that is
// not there (lib/auth.sh:70).
func TestWrite0600ReportsAWriteItCannotMakeAndLeavesNoTemporary(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-directory", "ShoreLogic.loop.pem")

	if err := write0600(missing, []byte("new")); err == nil {
		t.Fatal("write0600 into a directory that is not there returned no error")
	}

	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("the destination exists after a failed write: %v", err)
	}
	if _, err := os.Stat(missing + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the temporary file survived a failed write: %v", err)
	}
}
