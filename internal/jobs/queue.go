package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gwlsn/shrinkray/internal/ffmpeg"
)

// Queue manages the job queue with persistence
type Queue struct {
	mu             sync.RWMutex
	jobs           map[string]*Job
	order          []string             // Job IDs in order of creation
	filePath       string               // Path to persistence file
	processedPaths map[string]time.Time // All successfully processed input paths
	totalSaved     int64                // Total bytes saved across completed job history

	// Subscribers for job events
	subsMu      sync.RWMutex
	subscribers map[chan JobEvent]struct{}

	// Rate limiting for hardware fallbacks to prevent queue explosion
	fallbackTimes []time.Time // Timestamps of recent fallback creations
}

// NewQueue creates a new job queue, optionally loading from a persistence file
func NewQueue(filePath string) (*Queue, error) {
	q := &Queue{
		jobs:           make(map[string]*Job),
		order:          make([]string, 0),
		filePath:       filePath,
		processedPaths: make(map[string]time.Time),
		subscribers:    make(map[chan JobEvent]struct{}),
		fallbackTimes:  make([]time.Time, 0),
	}

	// Try to load existing queue
	if filePath != "" {
		if err := q.load(); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load queue: %w", err)
		}
	}

	return q, nil
}

// persistenceData is the structure saved to disk
type persistenceData struct {
	Jobs           []*Job               `json:"jobs"`
	Order          []string             `json:"order"`
	ProcessedPaths map[string]time.Time `json:"processed_paths,omitempty"`
	TotalSaved     *int64               `json:"total_saved,omitempty"`
}

// load reads the queue from disk
func (q *Queue) load() error {
	if q.filePath == "" {
		return nil
	}

	data, err := os.ReadFile(q.filePath)
	if err != nil {
		return err
	}

	var pd persistenceData
	if err := json.Unmarshal(data, &pd); err != nil {
		return err
	}

	q.jobs = make(map[string]*Job)
	for _, job := range pd.Jobs {
		q.jobs[job.ID] = job
	}
	q.order = pd.Order
	if pd.ProcessedPaths != nil {
		q.processedPaths = pd.ProcessedPaths
	} else {
		q.processedPaths = make(map[string]time.Time)
		for _, job := range q.jobs {
			if job.Status == StatusComplete {
				q.recordProcessedPathLocked(job.InputPath, job.CompletedAt)
			}
		}
	}
	if pd.TotalSaved != nil {
		q.totalSaved = *pd.TotalSaved
	} else {
		for _, job := range q.jobs {
			if job.Status == StatusComplete {
				q.totalSaved += job.SpaceSaved
			}
		}
	}

	// Reset any running jobs to pending (they were interrupted)
	for _, job := range q.jobs {
		if job.Status == StatusRunning {
			job.Status = StatusPending
			job.Progress = 0
			job.Speed = 0
			job.ETA = ""
		}
	}

	return nil
}

// save writes the queue to disk
func (q *Queue) save() error {
	if q.filePath == "" {
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(q.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Build ordered job list
	jobs := make([]*Job, 0, len(q.jobs))
	for _, id := range q.order {
		if job, ok := q.jobs[id]; ok {
			jobs = append(jobs, job)
		}
	}

	totalSaved := q.totalSaved
	pd := persistenceData{
		Jobs:           jobs,
		Order:          q.order,
		ProcessedPaths: q.processedPaths,
		TotalSaved:     &totalSaved,
	}

	data, err := json.MarshalIndent(pd, "", "  ")
	if err != nil {
		return err
	}

	// Write to temp file first, then rename (atomic)
	tmpPath := q.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, q.filePath)
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
		status = StatusFailed
	}

	job := &Job{
		ID:             generateID(),
		InputPath:      inputPath,
		PresetID:       presetID,
		Encoder:        encoder,
		IsHardware:     isHardware,
		Status:         status,
		Error:          skipReason,
		InputSize:      probe.Size,
		Duration:       probe.Duration.Milliseconds(),
		Bitrate:        probe.Bitrate,
		CreatedAt:      time.Now(),
		SubtitleCodecs: probe.SubtitleCodecs,
	}

	q.jobs[job.ID] = job
	q.order = append(q.order, job.ID)

	if err := q.save(); err != nil {
		// Log error but don't fail - queue still works in memory
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	// Broadcast appropriate event based on status
	if skipReason != "" {
		q.broadcast(JobEvent{Type: "failed", Job: job})
	} else {
		q.broadcast(JobEvent{Type: "added", Job: job})
	}

	return job, nil
}

// AddMultiple adds multiple jobs at once with a single batch SSE event.
// This is a performance optimization: instead of broadcasting N individual "added" events,
// we broadcast a single "batch_added" event containing all jobs.
// Jobs that fail skip-reason checks are broadcast separately as "failed" events.
func (q *Queue) AddMultiple(probes []*ffmpeg.ProbeResult, presetID string) ([]*Job, error) {
	q.mu.Lock()

	allJobs := make([]*Job, 0, len(probes))
	addedJobs := make([]*Job, 0, len(probes)) // Jobs successfully added (pending)
	skippedJobs := make([]*Job, 0)            // Jobs that failed skip-reason check

	preset := ffmpeg.GetPreset(presetID)
	encoder := string(ffmpeg.HWAccelNone)
	isHardware := false
	if preset != nil {
		encoder = string(preset.Encoder)
		isHardware = preset.Encoder != ffmpeg.HWAccelNone
	}

	for _, probe := range probes {
		// Check if file should be skipped
		var skipReason string
		if preset != nil {
			skipReason = checkSkipReason(probe, preset)
		}

		status := StatusPending
		if skipReason != "" {
			status = StatusFailed
		}

		job := &Job{
			ID:             generateID(),
			InputPath:      probe.Path,
			PresetID:       presetID,
			Encoder:        encoder,
			IsHardware:     isHardware,
			Status:         status,
			Error:          skipReason,
			InputSize:      probe.Size,
			Duration:       probe.Duration.Milliseconds(),
			Bitrate:        probe.Bitrate,
			CreatedAt:      time.Now(),
			SubtitleCodecs: probe.SubtitleCodecs,
		}

		q.jobs[job.ID] = job
		q.order = append(q.order, job.ID)
		allJobs = append(allJobs, job)

		if skipReason != "" {
			skippedJobs = append(skippedJobs, job)
		} else {
			addedJobs = append(addedJobs, job)
		}
	}

	// Persist once after all jobs are added (not per-job)
	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.mu.Unlock()

	// Broadcast events outside the lock to prevent SSE blocking queue operations
	// Performance: send single batch event instead of N individual events
	if len(addedJobs) > 0 {
		q.broadcast(JobEvent{Type: "batch_added", Jobs: addedJobs})
	}

	// Skipped jobs still get individual "failed" events for proper UI handling
	for _, job := range skippedJobs {
		q.broadcast(JobEvent{Type: "failed", Job: job})
	}

	return allJobs, nil
}

// AddWithoutProbe adds a job in pending_probe status for deferred probing.
// The job will be probed by a worker when picked up for processing.
// This enables streaming discovery: files are added immediately without waiting for ffprobe.
func (q *Queue) AddWithoutProbe(inputPath string, presetID string, fileSize int64) (*Job, error) {
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

	job := &Job{
		ID:         generateID(),
		InputPath:  inputPath,
		PresetID:   presetID,
		Encoder:    encoder,
		IsHardware: isHardware,
		Status:     StatusPendingProbe, // Will be probed when worker picks it up
		InputSize:  fileSize,           // File size is known from directory listing
		Duration:   0,                  // Will be populated after probe
		Bitrate:    0,                  // Will be populated after probe
		CreatedAt:  time.Now(),
	}

	q.jobs[job.ID] = job
	q.order = append(q.order, job.ID)

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "added", Job: job})

	return job, nil
}

// AddMultipleWithoutProbe adds multiple jobs in pending_probe status as a batch.
// Files are added immediately without waiting for ffprobe - probing happens when
// workers pick them up. Returns the created jobs.
func (q *Queue) AddMultipleWithoutProbe(files []FileInfo, presetID string) []*Job {
	q.mu.Lock()

	preset := ffmpeg.GetPreset(presetID)
	encoder := string(ffmpeg.HWAccelNone)
	isHardware := false
	if preset != nil {
		encoder = string(preset.Encoder)
		isHardware = preset.Encoder != ffmpeg.HWAccelNone
	}

	jobs := make([]*Job, 0, len(files))
	for _, f := range files {
		job := &Job{
			ID:         generateID(),
			InputPath:  f.Path,
			PresetID:   presetID,
			Encoder:    encoder,
			IsHardware: isHardware,
			Status:     StatusPendingProbe,
			InputSize:  f.Size,
			Duration:   0,
			Bitrate:    0,
			CreatedAt:  time.Now(),
		}

		q.jobs[job.ID] = job
		q.order = append(q.order, job.ID)
		jobs = append(jobs, job)
	}

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.mu.Unlock()

	// Broadcast batch event
	if len(jobs) > 0 {
		q.broadcast(JobEvent{Type: "batch_added", Jobs: jobs})
	}

	return jobs
}

// FileInfo contains minimal info for deferred probing
type FileInfo struct {
	Path string
	Size int64
}

// UpdateJobAfterProbe updates a pending_probe job with probe results.
// Called by worker after probing the file. Changes status to pending (or failed if skip).
func (q *Queue) UpdateJobAfterProbe(id string, probe *ffmpeg.ProbeResult) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if job.Status != StatusPendingProbe {
		return fmt.Errorf("job not in pending_probe status: %s", job.Status)
	}

	// Update job with probe results
	job.Duration = probe.Duration.Milliseconds()
	job.Bitrate = probe.Bitrate
	job.InputSize = probe.Size
	job.SubtitleCodecs = probe.SubtitleCodecs

	// Check if file should be skipped
	preset := ffmpeg.GetPreset(job.PresetID)
	var skipReason string
	if preset != nil {
		skipReason = checkSkipReason(probe, preset)
	}

	if skipReason != "" {
		job.Status = StatusFailed
		job.Error = skipReason
		job.CompletedAt = time.Now()
	} else {
		job.Status = StatusPending
	}

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	// Broadcast appropriate event
	if skipReason != "" {
		q.broadcast(JobEvent{Type: "failed", Job: job})
	} else {
		// Use a "probed" event type so frontend can update job details
		q.broadcast(JobEvent{Type: "probed", Job: job})
	}

	return nil
}

// Fallback rate limit constants
const (
	fallbackRateLimitWindow = 5 * time.Minute // Time window for rate limiting
	fallbackRateLimitMax    = 5               // Max fallbacks in window before pausing
)

// AddSoftwareFallback creates a new job using software encoding after a hardware
// encoder failure. The new job references the original failed job and is marked
// as a software fallback for visibility. Returns nil if rate limited.
func (q *Queue) AddSoftwareFallback(originalJob *Job, fallbackReason string) *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Rate limit: clean up old timestamps and check limit
	now := time.Now()
	cutoff := now.Add(-fallbackRateLimitWindow)
	validTimes := make([]time.Time, 0, len(q.fallbackTimes))
	for _, t := range q.fallbackTimes {
		if t.After(cutoff) {
			validTimes = append(validTimes, t)
		}
	}
	q.fallbackTimes = validTimes

	// If too many fallbacks recently, refuse to create another
	if len(q.fallbackTimes) >= fallbackRateLimitMax {
		fmt.Printf("Warning: hardware fallback rate limit reached (%d in %v), skipping auto-retry\n",
			fallbackRateLimitMax, fallbackRateLimitWindow)
		return nil
	}

	// Get the preset to determine the software encoder for this codec
	preset := ffmpeg.GetPreset(originalJob.PresetID)
	if preset == nil {
		return nil
	}

	// Create a modified preset that uses software encoding
	softwareEncoder := string(ffmpeg.HWAccelNone)

	job := &Job{
		ID:                 generateID(),
		InputPath:          originalJob.InputPath,
		PresetID:           originalJob.PresetID,
		Encoder:            softwareEncoder,
		IsHardware:         false,
		Status:             StatusPending,
		InputSize:          originalJob.InputSize,
		Duration:           originalJob.Duration,
		Bitrate:            originalJob.Bitrate,
		CreatedAt:          time.Now(),
		IsSoftwareFallback: true,
		OriginalJobID:      originalJob.ID,
		FallbackReason:     fallbackReason,
	}

	q.jobs[job.ID] = job
	q.order = append(q.order, job.ID)

	// Record this fallback for rate limiting
	q.fallbackTimes = append(q.fallbackTimes, now)

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "added", Job: job})

	return job
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

// GetNext returns the next workable job (pending_probe or pending) for workers to pick up.
// Jobs with pending_probe status need to be probed first by the worker.
func (q *Queue) GetNext() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, id := range q.order {
		if job, ok := q.jobs[id]; ok && job.IsWorkable() {
			return job
		}
	}
	return nil
}

// StartJob marks a job as running.
// Accepts jobs in pending or pending_probe status.
func (q *Queue) StartJob(id string, tempPath string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if !job.IsWorkable() {
		return fmt.Errorf("job not workable: %s", job.Status)
	}

	job.Status = StatusRunning
	job.TempPath = tempPath
	job.StartedAt = time.Now()

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

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

	// Performance: Use delta update instead of full Job struct
	// This reduces SSE payload from ~500+ bytes to ~80 bytes per progress event
	q.broadcast(JobEvent{
		Type: "progress",
		ProgressUpdate: &ProgressUpdate{
			ID:       id,
			Progress: progress,
			Speed:    speed,
			ETA:      eta,
		},
	})
}

// CompleteJob marks a job as complete
func (q *Queue) CompleteJob(id string, outputPath string, outputSize int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	wasComplete := job.Status == StatusComplete
	previousSaved := job.SpaceSaved

	job.Status = StatusComplete
	job.Progress = 100
	job.OutputPath = outputPath
	job.OutputSize = outputSize
	job.SpaceSaved = job.InputSize - outputSize
	job.CompletedAt = time.Now()
	job.TranscodeTime = int64(job.CompletedAt.Sub(job.StartedAt).Seconds())
	job.TempPath = "" // Clear temp path
	q.recordProcessedPathLocked(job.InputPath, job.CompletedAt)
	if outputPath != "" {
		q.recordProcessedPathLocked(outputPath, job.CompletedAt)
	}

	if wasComplete {
		q.totalSaved += job.SpaceSaved - previousSaved
	} else {
		q.totalSaved += job.SpaceSaved
	}

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "complete", Job: job})

	return nil
}

// ProcessedPaths returns a copy of processed input paths.
func (q *Queue) ProcessedPaths() map[string]struct{} {
	q.mu.RLock()
	defer q.mu.RUnlock()

	paths := make(map[string]struct{}, len(q.processedPaths))
	for path := range q.processedPaths {
		paths[path] = struct{}{}
	}
	return paths
}

// MarkProcessedPaths records input paths as processed.
// Returns the number of new entries added.
func (q *Queue) MarkProcessedPaths(paths []string) int {
	if len(paths) == 0 {
		return 0
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	added := 0
	now := time.Now()
	for _, path := range paths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			absPath = path
		}
		if _, ok := q.processedPaths[absPath]; !ok {
			added++
		}
		q.processedPaths[absPath] = now
	}

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	return added
}

// ClearProcessedHistory removes all recorded processed paths.
func (q *Queue) ClearProcessedHistory() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	count := len(q.processedPaths)
	q.processedPaths = make(map[string]time.Time)
	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}
	return count
}

func (q *Queue) recordProcessedPathLocked(inputPath string, completedAt time.Time) {
	absPath, err := filepath.Abs(inputPath)
	if err != nil {
		absPath = inputPath
	}
	q.processedPaths[absPath] = completedAt
}

// FailJobDetails contains optional diagnostic information for failed jobs
type FailJobDetails struct {
	Stderr     string   // Bounded stderr output from ffmpeg
	ExitCode   int      // FFmpeg exit code
	FFmpegArgs []string // FFmpeg command arguments used
}

// FailJob marks a job as failed
func (q *Queue) FailJob(id string, errMsg string) error {
	return q.FailJobWithDetails(id, errMsg, nil)
}

// FailJobWithDetails marks a job as failed with additional diagnostic info
func (q *Queue) FailJobWithDetails(id string, errMsg string, details *FailJobDetails) error {
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

	// Add diagnostic details if provided
	if details != nil {
		job.Stderr = details.Stderr
		job.ExitCode = details.ExitCode
		job.FFmpegArgs = details.FFmpegArgs
	}

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

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

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "cancelled", Job: job})

	return nil
}

// Clear removes all non-running jobs from the queue.
// If includeCompleted is false, completed jobs are kept.
// Only running jobs are always kept.
func (q *Queue) Clear(includeCompleted bool) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	count := 0
	newOrder := make([]string, 0, len(q.order))
	for _, id := range q.order {
		job, ok := q.jobs[id]
		if !ok {
			continue
		}
		if job.Status == StatusRunning || (!includeCompleted && job.Status == StatusComplete) {
			// Keep running jobs (and completed if requested)
			newOrder = append(newOrder, id)
		} else {
			delete(q.jobs, id)
			count++
		}
	}
	q.order = newOrder

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	return count
}

// Remove removes a single job from the queue.
func (q *Queue) Remove(id string) (*Job, error) {
	q.mu.Lock()

	job, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return nil, fmt.Errorf("job not found: %s", id)
	}

	delete(q.jobs, id)

	// Remove from order slice
	newOrder := make([]string, 0, len(q.order))
	for _, jid := range q.order {
		if jid != id {
			newOrder = append(newOrder, jid)
		}
	}
	q.order = newOrder

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.mu.Unlock()

	q.broadcast(JobEvent{Type: "removed", Job: job})

	return job, nil
}

// ReorderPending moves a pending job up or down within the pending queue order.
// Only pending or pending_probe jobs can be reordered.
// Returns true if the order changed.
func (q *Queue) ReorderPending(id string, direction string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return false, fmt.Errorf("job not found: %s", id)
	}
	if job.Status != StatusPending && job.Status != StatusPendingProbe {
		return false, fmt.Errorf("job not pending: %s", job.Status)
	}

	if direction != "up" && direction != "down" {
		return false, fmt.Errorf("invalid direction: %s", direction)
	}

	pendingIDs := make([]string, 0)
	for _, jid := range q.order {
		if queued, ok := q.jobs[jid]; ok && (queued.Status == StatusPending || queued.Status == StatusPendingProbe) {
			pendingIDs = append(pendingIDs, jid)
		}
	}

	currentIdx := -1
	for idx, jid := range pendingIDs {
		if jid == id {
			currentIdx = idx
			break
		}
	}
	if currentIdx == -1 {
		return false, fmt.Errorf("job not found in pending order: %s", id)
	}

	targetIdx := currentIdx
	if direction == "up" && currentIdx > 0 {
		targetIdx = currentIdx - 1
	}
	if direction == "down" && currentIdx < len(pendingIDs)-1 {
		targetIdx = currentIdx + 1
	}
	if targetIdx == currentIdx {
		return false, nil
	}

	pendingIDs[currentIdx], pendingIDs[targetIdx] = pendingIDs[targetIdx], pendingIDs[currentIdx]

	newOrder := make([]string, 0, len(q.order))
	pendingPos := 0
	for _, jid := range q.order {
		if queued, ok := q.jobs[jid]; ok && (queued.Status == StatusPending || queued.Status == StatusPendingProbe) {
			newOrder = append(newOrder, pendingIDs[pendingPos])
			pendingPos++
			continue
		}
		newOrder = append(newOrder, jid)
	}
	q.order = newOrder

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "reordered"})
	return true, nil
}

// MovePending moves a pending job before another pending job ID.
// If beforeID is empty, the job is moved to the end of pending jobs.
// Returns true if the order changed.
func (q *Queue) MovePending(id string, beforeID string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	job, ok := q.jobs[id]
	if !ok {
		return false, fmt.Errorf("job not found: %s", id)
	}
	if job.Status != StatusPending && job.Status != StatusPendingProbe {
		return false, fmt.Errorf("job not pending: %s", job.Status)
	}

	pendingIDs := make([]string, 0)
	for _, jid := range q.order {
		if queued, ok := q.jobs[jid]; ok && (queued.Status == StatusPending || queued.Status == StatusPendingProbe) {
			pendingIDs = append(pendingIDs, jid)
		}
	}

	currentIdx := -1
	for idx, jid := range pendingIDs {
		if jid == id {
			currentIdx = idx
			break
		}
	}
	if currentIdx == -1 {
		return false, fmt.Errorf("job not found in pending order: %s", id)
	}

	targetIdx := len(pendingIDs)
	if beforeID != "" {
		found := false
		for idx, jid := range pendingIDs {
			if jid == beforeID {
				targetIdx = idx
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("before job not found in pending order: %s", beforeID)
		}
	}

	if currentIdx == targetIdx || currentIdx+1 == targetIdx {
		return false, nil
	}

	updated := make([]string, 0, len(pendingIDs))
	for idx, jid := range pendingIDs {
		if idx == currentIdx {
			continue
		}
		if idx == targetIdx {
			updated = append(updated, id)
		}
		updated = append(updated, jid)
	}
	if targetIdx == len(pendingIDs) {
		updated = append(updated, id)
	}

	newOrder := make([]string, 0, len(q.order))
	pendingPos := 0
	for _, jid := range q.order {
		if queued, ok := q.jobs[jid]; ok && (queued.Status == StatusPending || queued.Status == StatusPendingProbe) {
			newOrder = append(newOrder, updated[pendingPos])
			pendingPos++
			continue
		}
		newOrder = append(newOrder, jid)
	}
	q.order = newOrder

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "reordered"})
	return true, nil
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

// Stats returns queue statistics
type Stats struct {
	PendingProbe int   `json:"pending_probe"` // Awaiting probe (deferred probing)
	Pending      int   `json:"pending"`
	Running      int   `json:"running"`
	Complete     int   `json:"complete"`
	Failed       int   `json:"failed"`
	Cancelled    int   `json:"cancelled"`
	Total        int   `json:"total"`
	TotalSaved   int64 `json:"total_saved"` // Total bytes saved by completed jobs
}

func (q *Queue) Stats() Stats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	stats := Stats{TotalSaved: q.totalSaved}
	for _, job := range q.jobs {
		stats.Total++
		switch job.Status {
		case StatusPendingProbe:
			stats.PendingProbe++
		case StatusPending:
			stats.Pending++
		case StatusRunning:
			stats.Running++
		case StatusComplete:
			stats.Complete++
		case StatusFailed:
			stats.Failed++
		case StatusCancelled:
			stats.Cancelled++
		}
	}
	stats.TotalSaved = q.totalSaved
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
	// For downscale presets, check if file already meets resolution target
	if preset.MaxHeight > 0 && probe.Height <= preset.MaxHeight {
		return fmt.Sprintf("File is already %dp or smaller", preset.MaxHeight)
	}

	// Check if file is already in target codec
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
