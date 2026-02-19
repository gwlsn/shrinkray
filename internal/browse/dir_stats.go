package browse

import (
	"time"
)

// dirCountState represents the lifecycle state of a directory's cached counts.
type dirCountState string

const (
	stateUnknown dirCountState = "unknown" // never computed
	statePending dirCountState = "pending" // first compute running
	stateReady   dirCountState = "ready"   // up to date w.r.t. signature
	stateStale   dirCountState = "stale"   // signature mismatch, recompute queued
	stateError   dirCountState = "error"   // last recompute failed
)

// dirSig is a lightweight directory signature for detecting structural changes.
// Uses inode/dev/mtime/ctime on Linux. Falls back to mtime-only comparison
// when inode or ctime are zero (common on NFS/SMB).
type dirSig struct {
	inode uint64
	dev   uint64
	mtime time.Time
	ctime time.Time
}

// differs reports whether two signatures indicate a directory has changed.
// If both signatures have non-zero inode (local filesystem), all four fields
// are compared. Otherwise falls back to mtime-only (NFS/SMB safe).
func (s dirSig) differs(other dirSig) bool {
	if s.inode != 0 && other.inode != 0 {
		return s.inode != other.inode ||
			s.dev != other.dev ||
			!s.mtime.Equal(other.mtime) ||
			!s.ctime.Equal(other.ctime)
	}
	// Fallback: mtime-only for network filesystems where inode/ctime
	// may be synthetic or absent.
	return !s.mtime.Equal(other.mtime)
}

// dirCountView is an immutable snapshot of a directory's cached counts.
// Returned by GetDirCount so callers can safely read without holding locks.
type dirCountView struct {
	FileCount int
	TotalSize int64
	State     dirCountState
	UpdatedAt time.Time
	Err       string
}

// getDirSig is implemented in build-tagged files:
//   dir_sig_linux.go  - uses syscall.Stat_t for inode/dev/ctime
//   dir_sig_other.go  - mtime-only fallback for non-Linux
