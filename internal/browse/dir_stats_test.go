package browse

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetDirSig(t *testing.T) {
	dir := t.TempDir()

	sig, err := getDirSig(dir)
	if err != nil {
		t.Fatalf("getDirSig failed: %v", err)
	}

	// mtime should be set (directory was just created)
	if sig.mtime.IsZero() {
		t.Error("expected non-zero mtime")
	}
}

func TestGetDirSig_NonExistent(t *testing.T) {
	_, err := getDirSig("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestDirSigDiffers_MtimeChange(t *testing.T) {
	dir := t.TempDir()

	sig1, err := getDirSig(dir)
	if err != nil {
		t.Fatalf("getDirSig failed: %v", err)
	}

	// Sleep to ensure the filesystem timestamp granularity is exceeded.
	// Some filesystems (ext4, tmpfs) have 1-second mtime resolution,
	// so operations within the same second produce identical timestamps.
	time.Sleep(1100 * time.Millisecond)

	// Create a file to change the directory mtime
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}

	sig2, err := getDirSig(dir)
	if err != nil {
		t.Fatalf("getDirSig failed: %v", err)
	}

	if !sig1.differs(sig2) {
		t.Error("expected signatures to differ after adding a file")
	}
}

func TestDirSigDiffers_MtimeOnlyFallback(t *testing.T) {
	// Simulate NFS/SMB where inode and ctime are zero
	sig1 := dirSig{mtime: time.Now()}
	sig2 := dirSig{mtime: sig1.mtime.Add(1 * time.Second)}

	if !sig1.differs(sig2) {
		t.Error("expected mtime-only signatures to differ")
	}

	sig3 := dirSig{mtime: sig1.mtime}
	if sig1.differs(sig3) {
		t.Error("expected identical mtime-only signatures to match")
	}
}

func TestDirSigDiffers_SameDir(t *testing.T) {
	dir := t.TempDir()

	sig1, _ := getDirSig(dir)
	sig2, _ := getDirSig(dir)

	if sig1.differs(sig2) {
		t.Error("expected identical signatures for same unchanged directory")
	}
}

func TestDirCountStates(t *testing.T) {
	// Verify state constants are distinct and non-empty
	states := []dirCountState{stateUnknown, statePending, stateReady, stateStale, stateError}
	seen := make(map[dirCountState]bool)
	for _, s := range states {
		if s == "" {
			t.Error("state constant must not be empty")
		}
		if seen[s] {
			t.Errorf("duplicate state constant: %s", s)
		}
		seen[s] = true
	}
}
