package browse

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
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

// dirCount holds cached recursive video counts for a directory with
// explicit state tracking. The key invariant: last-known values are
// never deleted during normal operation. Values are only replaced
// when a recompute successfully completes.
type dirCount struct {
	fileCount int
	totalSize int64
	sig       dirSig
	state     dirCountState
	updatedAt time.Time
	err       string
}

// view returns an immutable snapshot of the dirCount.
// Caller must hold countCacheMu (read or write).
func (dc *dirCount) view() dirCountView {
	return dirCountView{
		FileCount: dc.fileCount,
		TotalSize: dc.totalSize,
		State:     dc.state,
		UpdatedAt: dc.updatedAt,
		Err:       dc.err,
	}
}

// DirCountEvent is sent to SSE subscribers when a directory's counts update.
type DirCountEvent struct {
	Path      string    `json:"path"`
	FileCount int       `json:"file_count"`
	TotalSize int64     `json:"total_size"`
	State     string    `json:"counts_state"`
	UpdatedAt time.Time `json:"counts_updated_at"`
	Type      string    `json:"type,omitempty"` // "totals" for ancestor updates
}

// GetDirCount returns an immutable snapshot of the cached directory counts
// with read-through validation. It stats the directory to check the signature
// and enqueues a recompute if the entry is missing, stale, or in error state
// (with the directory now accessible). Last-known values are always preserved.
//
// Returns dirCountView (a value type), not a pointer. This is safe to read
// without holding any lock.
func (b *Browser) GetDirCount(ctx context.Context, dirPath string) dirCountView {
	sig, sigErr := getDirSig(dirPath)

	b.countCacheMu.Lock()
	cached, exists := b.countCache[dirPath]

	if sigErr != nil {
		// Directory does not exist (or can't be stat'd)
		if exists {
			if cached.state != stateError {
				cached.state = stateError
				cached.err = sigErr.Error()
			}
			view := cached.view()
			b.countCacheMu.Unlock()
			return view
		}
		b.countCacheMu.Unlock()
		return dirCountView{State: stateError, Err: sigErr.Error()}
	}

	if !exists {
		// Cache miss: create entry, enqueue recompute
		dc := &dirCount{state: stateUnknown}
		b.countCache[dirPath] = dc
		view := dc.view()
		b.countCacheMu.Unlock()

		b.enqueueRecompute(dirPath)
		return view
	}

	// Cache hit: check signature
	if cached.state == stateReady && cached.sig.differs(sig) {
		cached.state = stateStale
		view := cached.view()
		b.countCacheMu.Unlock()
		b.enqueueRecompute(dirPath)
		return view
	}

	// Error state recovery: directory is accessible again, retry
	if cached.state == stateError {
		cached.state = stateStale
		cached.err = ""
		view := cached.view()
		b.countCacheMu.Unlock()
		b.enqueueRecompute(dirPath)
		return view
	}

	// Retry stale/unknown entries that may have been dropped from the queue
	// (enqueueRecompute is non-blocking and drops if the queue is full).
	// Re-enqueueing on each access ensures these entries eventually converge.
	if cached.state == stateStale || cached.state == stateUnknown {
		view := cached.view()
		b.countCacheMu.Unlock()
		b.enqueueRecompute(dirPath)
		return view
	}

	view := cached.view()
	b.countCacheMu.Unlock()
	return view
}

// enqueueRecompute submits a recompute job to the bounded worker queue.
// Non-blocking: if the queue is full, the directory will be picked up on
// the next access.
func (b *Browser) enqueueRecompute(dirPath string) {
	select {
	case b.recomputeQueue <- dirPath:
	default:
		// Queue full; the directory will be picked up on next access
	}
}

// startRecomputeWorkers launches a fixed pool of goroutines that consume
// from the recompute queue. Called once during Browser initialization.
// The pool size matches countSem capacity (8 workers) since that's the
// actual concurrency limit for filesystem walks.
func (b *Browser) startRecomputeWorkers() {
	for range cap(b.countSem) {
		go func() {
			for dirPath := range b.recomputeQueue {
				_, _, _ = b.countGroup.Do(dirPath, func() (interface{}, error) {
					b.countSem <- struct{}{}
					defer func() { <-b.countSem }()
					b.recomputeDirCount(dirPath)
					return nil, nil
				})
			}
		}()
	}
}

// recomputeDirCount walks a directory, counts video files, captures the
// signature, and updates the cache entry. On walk or stat failure, preserves
// last-known values and transitions to error state. Broadcasts SSE events
// for this directory and all ancestor paths on success.
func (b *Browser) recomputeDirCount(dirPath string) {
	var count int
	var totalSize int64

	walkErr := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // propagate to capture in walkErr
		}
		if d.IsDir() {
			return nil
		}
		if ffmpeg.IsVideoFile(d.Name()) {
			if info, infoErr := d.Info(); infoErr == nil {
				count++
				totalSize += info.Size()
			}
		}
		return nil
	})

	sig, sigErr := getDirSig(dirPath)
	now := time.Now()

	b.countCacheMu.Lock()
	cached, exists := b.countCache[dirPath]

	if walkErr != nil || sigErr != nil {
		// Walk or stat failed: preserve last-known values, set error state
		var errMsg string
		if walkErr != nil {
			errMsg = walkErr.Error()
		} else {
			errMsg = sigErr.Error()
		}
		if exists {
			cached.state = stateError
			cached.err = errMsg
		}
		b.countCacheMu.Unlock()

		// Broadcast error so SSE clients can show the transition
		b.broadcast(DirCountEvent{
			Path:      dirPath,
			FileCount: 0,
			State:     string(stateError),
			UpdatedAt: now,
		})
		return
	}

	if exists {
		cached.fileCount = count
		cached.totalSize = totalSize
		cached.sig = sig
		cached.state = stateReady
		cached.updatedAt = now
		cached.err = ""
	} else {
		b.countCache[dirPath] = &dirCount{
			fileCount: count,
			totalSize: totalSize,
			sig:       sig,
			state:     stateReady,
			updatedAt: now,
		}
	}
	b.countCacheMu.Unlock()

	// Broadcast event for this directory
	b.broadcast(DirCountEvent{
		Path:      dirPath,
		FileCount: count,
		TotalSize: totalSize,
		State:     string(stateReady),
		UpdatedAt: now,
	})

	// Broadcast ancestor totals updates
	b.broadcastAncestorTotals(dirPath)
}

// broadcastAncestorTotals emits "totals" events for every ancestor of dirPath
// up to mediaRoot, reading their current cached values.
func (b *Browser) broadcastAncestorTotals(dirPath string) {
	dir := filepath.Dir(dirPath)
	for b.isUnderRoot(dir) {
		b.countCacheMu.RLock()
		cached, ok := b.countCache[dir]
		var event DirCountEvent
		if ok {
			event = DirCountEvent{
				Path:      dir,
				FileCount: cached.fileCount,
				TotalSize: cached.totalSize,
				State:     string(cached.state),
				UpdatedAt: cached.updatedAt,
				Type:      "totals",
			}
		}
		b.countCacheMu.RUnlock()

		if ok {
			b.broadcast(event)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

// Subscribe returns a buffered channel that receives directory count
// update events. Callers must call Unsubscribe when done.
func (b *Browser) Subscribe() chan DirCountEvent {
	ch := make(chan DirCountEvent, 100)
	b.browseSubsMu.Lock()
	b.browseSubscribers[ch] = struct{}{}
	b.browseSubsMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Browser) Unsubscribe(ch chan DirCountEvent) {
	b.browseSubsMu.Lock()
	delete(b.browseSubscribers, ch)
	b.browseSubsMu.Unlock()
	close(ch)
}

// broadcast sends a directory count event to all subscribers.
// Non-blocking: if a subscriber's channel is full, the event is dropped
// for that subscriber (they will get the next one).
func (b *Browser) broadcast(event DirCountEvent) { //nolint:gocritic // value type matches channel semantics
	b.browseSubsMu.RLock()
	defer b.browseSubsMu.RUnlock()

	for ch := range b.browseSubscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// getDirSig is implemented in build-tagged files:
//   dir_sig_linux.go  - uses syscall.Stat_t for inode/dev/ctime
//   dir_sig_other.go  - mtime-only fallback for non-Linux
