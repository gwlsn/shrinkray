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
	mu       sync.RWMutex
	jobs     map[string]*Job
	order    []string // Job IDs in order of creation
	filePath string   // Path to persistence file

	// Subscribers for job events
	subsMu      sync.RWMutex
	subscribers map[chan JobEvent]struct{}
}

// NewQueue creates a new job queue, optionally loading from a persistence file
func NewQueue(filePath string) (*Queue, error) {
	q := &Queue{
		jobs:        make(map[string]*Job),
		order:       make([]string, 0),
		filePath:    filePath,
		subscribers: make(map[chan JobEvent]struct{}),
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
	Jobs  []*Job `json:"jobs"`
	Order []string `json:"order"`
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

	pd := persistenceData{
		Jobs:  jobs,
		Order: q.order,
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
		CreatedAt:  time.Now(),
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

// AddMultiple adds multiple jobs at once
func (q *Queue) AddMultiple(probes []*ffmpeg.ProbeResult, presetID string) ([]*Job, error) {
	jobs := make([]*Job, 0, len(probes))

	for _, probe := range probes {
		job, err := q.Add(probe.Path, presetID, probe)
		if err != nil {
			return jobs, err
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// AddSoftwareFallback creates a new job using software encoding after a hardware
// encoder failure. The new job references the original failed job and is marked
// as a software fallback for visibility.
func (q *Queue) AddSoftwareFallback(originalJob *Job, fallbackReason string) *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

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

// GetNext returns the next pending job (for workers to pick up)
func (q *Queue) GetNext() *Job {
	q.mu.Lock()
	defer q.mu.Unlock()

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

	if err := q.save(); err != nil {
		fmt.Printf("Warning: failed to persist queue: %v\n", err)
	}

	q.broadcast(JobEvent{Type: "complete", Job: job})

	return nil
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

// Clear removes all non-running jobs from the queue (pending, completed, failed, cancelled)
// Only running jobs are kept.
func (q *Queue) Clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	count := 0
	newOrder := make([]string, 0, len(q.order))
	for _, id := range q.order {
		job, ok := q.jobs[id]
		if !ok {
			continue
		}
		if job.Status == StatusRunning {
			// Keep only running jobs
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

// Remove removes a single job from the queue (for retry functionality)
func (q *Queue) Remove(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()

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
	Pending   int   `json:"pending"`
	Running   int   `json:"running"`
	Complete  int   `json:"complete"`
	Failed    int   `json:"failed"`
	Cancelled int   `json:"cancelled"`
	Total     int   `json:"total"`
	TotalSaved int64 `json:"total_saved"` // Total bytes saved by completed jobs
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
