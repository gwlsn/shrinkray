package jobs

import (
	"fmt"
	"sync"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/logger"
)

// Store defines the persistence interface for job data.
// This interface is implemented by internal/store.SQLiteStore.
type Store interface {
	SaveJob(job *Job) error
	GetJob(id string) (*Job, error)
	DeleteJob(id string) error
	SaveJobs(jobs []*Job) error
	GetAllJobs() ([]*Job, []string, error)
	GetJobsByStatus(status Status) ([]*Job, error)
	GetNextPendingJob() (*Job, error)
	AppendToOrder(id string) error
	RemoveFromOrder(id string) error
	SetOrder(order []string) error
	ResetRunningJobs() (int, error)
	AddToLifetimeSaved(bytes int64) error
	Close() error
}

// StoreWithStats extends Store with session/lifetime stats support.
// Stores that implement this interface can provide accurate session/lifetime stats.
type StoreWithStats interface {
	Store
	SessionLifetimeStats() (sessionSaved, lifetimeSaved int64, err error)
}

// Queue manages the job queue with persistence
type Queue struct {
	mu    sync.RWMutex
	jobs  map[string]*Job
	order []string // Job IDs in order of creation
	store Store    // Persistence store (nil = in-memory only)

	// Subscribers for job events
	subsMu      sync.RWMutex
	subscribers map[chan JobEvent]struct{}
}

// NewQueue creates a new in-memory job queue (for testing).
// Use NewQueueWithStore for production use with persistence.
func NewQueue() *Queue {
	return &Queue{
		jobs:        make(map[string]*Job),
		order:       make([]string, 0),
		subscribers: make(map[chan JobEvent]struct{}),
	}
}

// NewQueueWithStore creates a job queue backed by a persistent store.
// The store should already be initialized and have running jobs reset.
func NewQueueWithStore(store Store) (*Queue, error) {
	q := &Queue{
		jobs:        make(map[string]*Job),
		order:       make([]string, 0),
		store:       store,
		subscribers: make(map[chan JobEvent]struct{}),
	}

	// Load existing jobs from store into memory cache
	if store != nil {
		jobs, order, err := store.GetAllJobs()
		if err != nil {
			return nil, fmt.Errorf("load jobs from store: %w", err)
		}

		for _, job := range jobs {
			q.jobs[job.ID] = job
		}
		q.order = order
	}

	return q, nil
}

// persist saves a job to the store (if configured).
// Called with lock held.
func (q *Queue) persist(job *Job) {
	if q.store == nil {
		return
	}
	if err := q.store.SaveJob(job); err != nil {
		logger.Warn("Failed to persist job", "job_id", job.ID, "error", err)
	}
}

// persistOrder adds a job ID to the store's order (if configured).
// Called with lock held.
func (q *Queue) persistOrder(id string) {
	if q.store == nil {
		return
	}
	if err := q.store.AppendToOrder(id); err != nil {
		logger.Warn("Failed to persist job order", "job_id", id, "error", err)
	}
}

// persistDelete removes a job from the store (if configured).
// Called with lock held.
func (q *Queue) persistDelete(id string) {
	if q.store == nil {
		return
	}
	if err := q.store.DeleteJob(id); err != nil {
		logger.Warn("Failed to delete job from store", "job_id", id, "error", err)
	}
}

// Add adds a new job to the queue
func (q *Queue) Add(inputPath string, presetID string, probe *ffmpeg.ProbeResult) (*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Look up preset to get encoder info
	preset := ffmpeg.GetPreset(presetID)
	encoder := string(ffmpeg.HWAccelNone)
	isHardware := false
	if preset != nil {
		encoder = string(preset.Encoder)
		isHardware = preset.Encoder != ffmpeg.HWAccelNone
	}

	// Check if file should be skipped
	var skipReason string
	if preset != nil {
		skipReason = checkSkipReason(probe, preset)
	}

	status := StatusPending
	if skipReason != "" {
		status = StatusSkipped
	}

	job := &Job{
		ID:         generateID(),
		InputPath:  inputPath,
		PresetID:   presetID,
		Encoder:    encoder,
		IsHardware: isHardware,
		Status:     status,
		Error:      skipReason,
		InputSize:  probe.Size,
		Duration:   probe.Duration.Milliseconds(),
		Bitrate:    probe.Bitrate,
		Width:      probe.Width,
		Height:     probe.Height,
		FrameRate:  probe.FrameRate,
		VideoCodec: probe.VideoCodec,
		Profile:    probe.Profile,
		BitDepth:   probe.BitDepth,
		CreatedAt:  time.Now(),
	}

	q.jobs[job.ID] = job
	q.order = append(q.order, job.ID)

	// Persist to store
	q.persist(job)
	q.persistOrder(job.ID)

	// Broadcast appropriate event based on status
	if skipReason != "" {
		q.broadcast(JobEvent{Type: "skipped", Job: job})
	} else {
		q.broadcast(JobEvent{Type: "added", Job: job})
	}

	return job, nil
}

// AddMultiple adds multiple jobs at once with batched persistence and SSE
func (q *Queue) AddMultiple(probes []*ffmpeg.ProbeResult, presetID string) ([]*Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Look up preset once
	preset := ffmpeg.GetPreset(presetID)
	encoder := string(ffmpeg.HWAccelNone)
	isHardware := false
	if preset != nil {
		encoder = string(preset.Encoder)
		isHardware = preset.Encoder != ffmpeg.HWAccelNone
	}

	jobList := make([]*Job, 0, len(probes))
	addedCount := 0
	failedCount := 0

	for _, probe := range probes {
		// Check if file should be skipped
		var skipReason string
		if preset != nil {
			skipReason = checkSkipReason(probe, preset)
		}

		status := StatusPending
		if skipReason != "" {
			status = StatusSkipped
			failedCount++
		} else {
			addedCount++
		}

		job := &Job{
			ID:         generateID(),
			InputPath:  probe.Path,
			PresetID:   presetID,
			Encoder:    encoder,
			IsHardware: isHardware,
			Status:     status,
			Error:      skipReason,
			InputSize:  probe.Size,
			Duration:   probe.Duration.Milliseconds(),
			Bitrate:    probe.Bitrate,
			Width:      probe.Width,
			Height:     probe.Height,
			FrameRate:  probe.FrameRate,
			VideoCodec: probe.VideoCodec,
			Profile:    probe.Profile,
			BitDepth:   probe.BitDepth,
			CreatedAt:  time.Now(),
		}

		q.jobs[job.ID] = job
		q.order = append(q.order, job.ID)
		jobList = append(jobList, job)
	}

	// Batch persist to store
	if q.store != nil && len(jobList) > 0 {
		if err := q.store.SaveJobs(jobList); err != nil {
			logger.Warn("Failed to persist jobs batch", "error", err)
		}
		// Add all to order
		for _, job := range jobList {
			q.persistOrder(job.ID)
		}
	}

	// Broadcast single batch event (frontend will refresh once)
	if addedCount > 0 || failedCount > 0 {
		q.broadcast(JobEvent{Type: "jobs_added", Count: addedCount + failedCount})
	}

	return jobList, nil
}

// Get returns a job by ID
func (q *Queue) Get(id string) *Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.jobs[id]
}

// GetAll returns all jobs in order
func (q *Queue) GetAll() []*Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	jobs := make([]*Job, 0, len(q.order))
	for _, id := range q.order {
		if job, ok := q.jobs[id]; ok {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// GetNext returns the next pending job (for workers to pick up)
func (q *Queue) GetNext() *Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	for _, id := range q.order {
		if job, ok := q.jobs[id]; ok && job.Status == StatusPending {
			return job
		}
	}
	return nil
}

// StartJob marks a job as running
func (q *Queue) StartJob(id string, tempPath string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if job.Status != StatusPending {
		return fmt.Errorf("job not pending: %s", job.Status)
	}

	job.Status = StatusRunning
	job.TempPath = tempPath
	job.StartedAt = time.Now()

	q.persist(job)
	q.broadcast(JobEvent{Type: "started", Job: job})

	return nil
}

// UpdateProgress updates a job's progress
func (q *Queue) UpdateProgress(id string, progress float64, speed float64, eta string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok || job.Status != StatusRunning {
		return
	}

	job.Progress = progress
	job.Speed = speed
	job.ETA = eta

	// Don't persist on every progress update (too expensive)
	// Just broadcast to subscribers

	q.broadcast(JobEvent{Type: "progress", Job: job})
}

// CompleteJob marks a job as complete
func (q *Queue) CompleteJob(id string, outputPath string, outputSize int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	job.Status = StatusComplete
	job.Progress = 100
	job.OutputPath = outputPath
	job.OutputSize = outputSize
	job.SpaceSaved = job.InputSize - outputSize
	job.CompletedAt = time.Now()
	job.TranscodeTime = int64(job.CompletedAt.Sub(job.StartedAt).Seconds())
	job.TempPath = "" // Clear temp path

	q.persist(job)

	// Update session/lifetime saved counters
	if q.store != nil && job.SpaceSaved > 0 {
		if err := q.store.AddToLifetimeSaved(job.SpaceSaved); err != nil {
			logger.Warn("Failed to update saved stats", "error", err)
		}
	}

	q.broadcast(JobEvent{Type: "complete", Job: job})

	return nil
}

// FailJob marks a job as failed
func (q *Queue) FailJob(id string, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	job.Status = StatusFailed
	job.Error = errMsg
	job.CompletedAt = time.Now()
	job.TempPath = "" // Clear temp path

	q.persist(job)
	q.broadcast(JobEvent{Type: "failed", Job: job})

	return nil
}

// CancelJob cancels a job
func (q *Queue) CancelJob(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if job.IsTerminal() {
		return fmt.Errorf("job already in terminal state: %s", job.Status)
	}

	job.Status = StatusCancelled
	job.CompletedAt = time.Now()

	q.persist(job)
	q.broadcast(JobEvent{Type: "cancelled", Job: job})

	return nil
}

// Requeue resets a running job back to pending and moves it to the front of the queue.
// Used when reducing worker count to return jobs to the queue.
func (q *Queue) Requeue(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if job.Status != StatusRunning {
		return fmt.Errorf("can only requeue running jobs, got: %s", job.Status)
	}

	// Reset job state
	job.Status = StatusPending
	job.Progress = 0
	job.Speed = 0
	job.ETA = ""
	job.TempPath = ""
	job.StartedAt = time.Time{}

	// Move to front of order (in memory)
	newOrder := []string{id}
	for _, oid := range q.order {
		if oid != id {
			newOrder = append(newOrder, oid)
		}
	}
	q.order = newOrder

	// Persist job and order
	q.persist(job)
	if q.store != nil {
		if err := q.store.SetOrder(q.order); err != nil {
			logger.Warn("Failed to persist job order", "error", err)
		}
	}

	q.broadcast(JobEvent{Type: "requeued", Job: job})

	return nil
}

// Clear removes jobs from the queue. If filterStatus is empty, clears all
// non-running jobs. If specified, clears only jobs matching that status.
// Running jobs are never cleared.
func (q *Queue) Clear(filterStatus Status) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	count := 0
	newOrder := make([]string, 0, len(q.order))
	for _, id := range q.order {
		job, ok := q.jobs[id]
		if !ok {
			continue
		}
		// Never clear running jobs
		if job.Status == StatusRunning {
			newOrder = append(newOrder, id)
			continue
		}
		// If filtering by status, only clear matching jobs
		if filterStatus != "" && job.Status != filterStatus {
			newOrder = append(newOrder, id)
			continue
		}
		// Clear this job
		q.persistDelete(id)
		delete(q.jobs, id)
		count++
	}
	q.order = newOrder

	return count
}

// Remove removes a single job from the queue (for retry functionality)
func (q *Queue) Remove(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.persistDelete(id)
	delete(q.jobs, id)

	// Remove from order slice
	newOrder := make([]string, 0, len(q.order))
	for _, jid := range q.order {
		if jid != id {
			newOrder = append(newOrder, jid)
		}
	}
	q.order = newOrder

	// Broadcast removal event
	q.broadcast(JobEvent{Type: "removed", Job: &Job{ID: id}})
}

// Subscribe returns a channel that receives job events
func (q *Queue) Subscribe() chan JobEvent {
	ch := make(chan JobEvent, 100)

	q.subsMu.Lock()
	q.subscribers[ch] = struct{}{}
	q.subsMu.Unlock()

	return ch
}

// Unsubscribe removes a subscription
func (q *Queue) Unsubscribe(ch chan JobEvent) {
	q.subsMu.Lock()
	delete(q.subscribers, ch)
	q.subsMu.Unlock()

	close(ch)
}

// broadcast sends an event to all subscribers
func (q *Queue) broadcast(event JobEvent) {
	q.subsMu.RLock()
	defer q.subsMu.RUnlock()

	for ch := range q.subscribers {
		select {
		case ch <- event:
		default:
			// Channel full, skip this subscriber
		}
	}
}

// BroadcastProgress sends a discovery progress event to all subscribers
func (q *Queue) BroadcastProgress(probed, total int) {
	q.broadcast(JobEvent{
		Type:   "discovery_progress",
		Probed: probed,
		Total:  total,
	})
}

// Stats returns queue statistics
type Stats struct {
	Pending       int   `json:"pending"`
	Running       int   `json:"running"`
	Complete      int   `json:"complete"`
	Failed        int   `json:"failed"`
	Cancelled     int   `json:"cancelled"`
	Skipped       int   `json:"skipped"`
	Total         int   `json:"total"`
	TotalSaved    int64 `json:"total_saved"`    // For API compatibility (= session_saved)
	SessionSaved  int64 `json:"session_saved"`  // Bytes saved this session
	LifetimeSaved int64 `json:"lifetime_saved"` // All-time bytes saved
}

func (q *Queue) Stats() Stats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var stats Stats
	for _, job := range q.jobs {
		stats.Total++
		switch job.Status {
		case StatusPending:
			stats.Pending++
		case StatusRunning:
			stats.Running++
		case StatusComplete:
			stats.Complete++
			stats.TotalSaved += job.SpaceSaved
		case StatusFailed:
			stats.Failed++
		case StatusCancelled:
			stats.Cancelled++
		case StatusSkipped:
			stats.Skipped++
		}
	}

	// Get session/lifetime stats from store if available
	if sws, ok := q.store.(StoreWithStats); ok {
		sessionSaved, lifetimeSaved, err := sws.SessionLifetimeStats()
		if err == nil {
			stats.SessionSaved = sessionSaved
			stats.LifetimeSaved = lifetimeSaved
			stats.TotalSaved = sessionSaved // Header shows session saved
		}
	}

	return stats
}

// idCounter ensures unique IDs even when called in quick succession
var idCounter int64
var idMu sync.Mutex

// generateID creates a unique job ID
func generateID() string {
	idMu.Lock()
	defer idMu.Unlock()
	idCounter++
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), idCounter)
}

// checkSkipReason returns an error message if the file should be skipped, empty string otherwise.
func checkSkipReason(probe *ffmpeg.ProbeResult, preset *ffmpeg.Preset) string {
	// For downscale presets, only check resolution (codec doesn't matter)
	if preset.MaxHeight > 0 {
		if probe.Height <= preset.MaxHeight {
			return fmt.Sprintf("File is already %dp or smaller", preset.MaxHeight)
		}
		return "" // Needs downscaling, proceed regardless of codec
	}

	// For compression presets (no downscaling), check codec
	var isAlreadyTarget bool
	var codecName string

	switch preset.Codec {
	case ffmpeg.CodecHEVC:
		isAlreadyTarget = probe.IsHEVC
		codecName = "HEVC"
	case ffmpeg.CodecAV1:
		isAlreadyTarget = probe.IsAV1
		codecName = "AV1"
	}

	if isAlreadyTarget {
		return fmt.Sprintf("File is already encoded in %s", codecName)
	}

	return "" // Proceed with transcode
}
