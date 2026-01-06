package jobs

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/logger"
)

// CacheInvalidator is called when a file is transcoded to invalidate cached probe data
type CacheInvalidator func(path string)

// Worker processes transcoding jobs from the queue
type Worker struct {
	id              int
	queue           *Queue
	transcoder      *ffmpeg.Transcoder
	prober          *ffmpeg.Prober
	cfg             *config.Config
	invalidateCache CacheInvalidator

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Currently running job (for cancellation)
	currentJobMu sync.Mutex
	currentJob   *Job
	jobCancel    context.CancelFunc
}

// WorkerPool manages multiple workers
type WorkerPool struct {
	mu              sync.Mutex
	workers         []*Worker
	queue           *Queue
	cfg             *config.Config
	invalidateCache CacheInvalidator
	nextWorkerID    int

	ctx    context.Context
	cancel context.CancelFunc
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(queue *Queue, cfg *config.Config, invalidateCache CacheInvalidator) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	pool := &WorkerPool{
		workers:         make([]*Worker, 0, cfg.Workers),
		queue:           queue,
		cfg:             cfg,
		invalidateCache: invalidateCache,
		nextWorkerID:    0,
		ctx:             ctx,
		cancel:          cancel,
	}

	// Create workers
	for i := 0; i < cfg.Workers; i++ {
		pool.workers = append(pool.workers, pool.createWorker())
	}

	return pool
}

// createWorker creates a new worker with the next available ID
func (p *WorkerPool) createWorker() *Worker {
	worker := &Worker{
		id:              p.nextWorkerID,
		queue:           p.queue,
		transcoder:      ffmpeg.NewTranscoder(p.cfg.FFmpegPath),
		prober:          ffmpeg.NewProber(p.cfg.FFprobePath),
		cfg:             p.cfg,
		invalidateCache: p.invalidateCache,
	}
	p.nextWorkerID++
	return worker
}

// Start starts all workers
func (p *WorkerPool) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.workers {
		w.Start(p.ctx)
	}
}

// Stop stops all workers gracefully
func (p *WorkerPool) Stop() {
	p.cancel()

	p.mu.Lock()
	workers := make([]*Worker, len(p.workers))
	copy(workers, p.workers)
	p.mu.Unlock()

	for _, w := range workers {
		w.Stop()
	}
}

// CancelJob cancels a specific job if it's currently running
func (p *WorkerPool) CancelJob(jobID string) bool {
	p.mu.Lock()
	workers := make([]*Worker, len(p.workers))
	copy(workers, p.workers)
	p.mu.Unlock()

	for _, w := range workers {
		if w.CancelCurrentJob(jobID) {
			return true
		}
	}
	return false
}

// Resize changes the number of workers in the pool
// If n > current, new workers are started immediately
// If n < current, excess workers are stopped immediately
// Jobs are cancelled in reverse order (most recently added jobs cancelled first)
func (p *WorkerPool) Resize(n int) {
	if n < 1 {
		n = 1
	}
	if n > 6 {
		n = 6 // reasonable upper limit
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	current := len(p.workers)

	if n > current {
		// Add workers
		for i := current; i < n; i++ {
			worker := p.createWorker()
			worker.Start(p.ctx)
			p.workers = append(p.workers, worker)
		}
	} else if n < current {
		// Remove excess workers immediately
		// Cancel jobs in reverse order (most recently added jobs first)
		workersToStop := current - n

		// First, collect all running jobs and their workers
		type runningJob struct {
			worker *Worker
			jobID  string
		}
		var runningJobs []runningJob

		for _, w := range p.workers {
			w.currentJobMu.Lock()
			if w.currentJob != nil {
				runningJobs = append(runningJobs, runningJob{
					worker: w,
					jobID:  w.currentJob.ID,
				})
			}
			w.currentJobMu.Unlock()
		}

		// Sort running jobs by job ID descending (newest first)
		// Job IDs are timestamp-based, so lexicographically larger = more recent
		sort.Slice(runningJobs, func(i, j int) bool {
			return runningJobs[i].jobID > runningJobs[j].jobID
		})

		// Cancel jobs starting from most recent
		cancelled := 0
		for _, rj := range runningJobs {
			if cancelled >= workersToStop {
				break
			}
			rj.worker.CancelAndStop()

			// Remove this worker from the pool
			for j, w := range p.workers {
				if w == rj.worker {
					p.workers = append(p.workers[:j], p.workers[j+1:]...)
					break
				}
			}
			cancelled++
		}

		// If we still need to remove more workers (idle ones), remove from end
		for len(p.workers) > n {
			w := p.workers[len(p.workers)-1]
			p.workers = p.workers[:len(p.workers)-1]
			w.CancelAndStop()
		}
	}

	// Update config
	p.cfg.Workers = n
}

// WorkerCount returns the current number of workers
func (p *WorkerPool) WorkerCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.workers)
}

// Start starts the worker's processing loop
func (w *Worker) Start(parentCtx context.Context) {
	w.ctx, w.cancel = context.WithCancel(parentCtx)
	w.wg.Add(1)

	go w.run()
}

// Stop stops the worker
func (w *Worker) Stop() {
	w.cancel()
	w.wg.Wait()
}

// run is the main worker loop
func (w *Worker) run() {
	defer w.wg.Done()

	for {
		select {
		case <-w.ctx.Done():
			return
		default:
			// Check if schedule allows transcoding
			if !w.isScheduleAllowed() {
				select {
				case <-w.ctx.Done():
					return
				case <-time.After(30 * time.Second):
					continue
				}
			}

			// Try to get next job
			job := w.queue.GetNext()
			if job == nil {
				// No jobs available, wait a bit
				select {
				case <-w.ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}

			// Process the job
			w.processJob(job)
		}
	}
}

// isScheduleAllowed checks if the current time is within the allowed schedule
func (w *Worker) isScheduleAllowed() bool {
	if !w.cfg.ScheduleEnabled {
		return true
	}

	hour := time.Now().Hour()
	start := w.cfg.ScheduleStartHour
	end := w.cfg.ScheduleEndHour

	// Handle overnight windows (e.g., 22:00 to 06:00)
	if start > end {
		return hour >= start || hour < end
	}

	// Handle daytime windows (e.g., 09:00 to 17:00)
	return hour >= start && hour < end
}

// processJob handles a single transcoding job
func (w *Worker) processJob(job *Job) {
	startTime := time.Now()
	logger.Info("Job started", "job_id", job.ID, "file", job.InputPath, "preset", job.PresetID)

	// Create a cancellable context for this job
	jobCtx, jobCancel := context.WithCancel(w.ctx)
	defer jobCancel()

	w.currentJobMu.Lock()
	w.currentJob = job
	w.jobCancel = jobCancel
	w.currentJobMu.Unlock()

	defer func() {
		w.currentJobMu.Lock()
		w.currentJob = nil
		w.jobCancel = nil
		w.currentJobMu.Unlock()
	}()

	// Get the preset
	preset := ffmpeg.GetPreset(job.PresetID)
	if preset == nil {
		logger.Error("Job failed", "job_id", job.ID, "error", "unknown preset", "preset", job.PresetID)
		_ = w.queue.FailJob(job.ID, fmt.Sprintf("unknown preset: %s", job.PresetID))
		return
	}

	// Build temp output path
	tempDir := w.cfg.GetTempDir(job.InputPath)
	tempPath := ffmpeg.BuildTempPath(job.InputPath, tempDir)

	// Mark job as started
	if err := w.queue.StartJob(job.ID, tempPath); err != nil {
		// Job might have been cancelled or already started
		return
	}

	// Create progress channel
	progressCh := make(chan ffmpeg.Progress, 10)

	// Start progress forwarding
	go func() {
		for progress := range progressCh {
			eta := formatDuration(progress.ETA)
			w.queue.UpdateProgress(job.ID, progress.Percent, progress.Speed, eta)
		}
	}()

	// Run the transcode
	duration := time.Duration(job.Duration) * time.Millisecond
	result, err := w.transcoder.Transcode(jobCtx, job.InputPath, tempPath, preset, duration, job.Bitrate, w.cfg.QualityHEVC, w.cfg.QualityAV1, progressCh)

	if err != nil {
		// Check if it was cancelled
		if jobCtx.Err() == context.Canceled {
			// Clean up temp file
			os.Remove(tempPath)
			logger.Info("Job cancelled", "job_id", job.ID)
			_ = w.queue.CancelJob(job.ID)
			return
		}

		// Clean up temp file on failure
		os.Remove(tempPath)
		logger.Error("Job failed", "job_id", job.ID, "error", err.Error())
		_ = w.queue.FailJob(job.ID, err.Error())
		return
	}

	// Check if transcoded file is larger than original
	if result.OutputSize >= job.InputSize {
		// Delete the temp file and fail the job
		os.Remove(tempPath)
		logger.Warn("Job skipped - output larger than input", "job_id", job.ID, "input_size", formatBytes(job.InputSize), "output_size", formatBytes(result.OutputSize))
		_ = w.queue.FailJob(job.ID, fmt.Sprintf("Transcoded file (%s) is larger than original (%s). File skipped.",
			formatBytes(result.OutputSize), formatBytes(job.InputSize)))
		return
	}

	// Finalize the transcode (handle original file)
	replace := w.cfg.OriginalHandling == "replace"
	finalPath, err := ffmpeg.FinalizeTranscode(job.InputPath, tempPath, replace)
	if err != nil {
		// Try to clean up
		os.Remove(tempPath)
		logger.Error("Job failed - finalization error", "job_id", job.ID, "error", err.Error())
		_ = w.queue.FailJob(job.ID, fmt.Sprintf("failed to finalize: %v", err))
		return
	}

	// Invalidate cache for the output file so browser shows updated metadata
	if w.invalidateCache != nil {
		w.invalidateCache(finalPath)
		// Also invalidate the original path in case it was cached
		w.invalidateCache(job.InputPath)
	}

	// Calculate stats
	elapsed := time.Since(startTime)
	saved := job.InputSize - result.OutputSize

	logger.Info("Job complete", "job_id", job.ID, "duration", formatDuration(elapsed), "saved", formatBytes(saved))

	// Mark job complete
	_ = w.queue.CompleteJob(job.ID, finalPath, result.OutputSize)
}

// CancelCurrentJob cancels the job if it matches the given ID
func (w *Worker) CancelCurrentJob(jobID string) bool {
	w.currentJobMu.Lock()
	defer w.currentJobMu.Unlock()

	if w.currentJob != nil && w.currentJob.ID == jobID && w.jobCancel != nil {
		w.jobCancel()
		return true
	}
	return false
}

// CancelAndStop cancels any current job and stops the worker immediately
func (w *Worker) CancelAndStop() {
	// First cancel any running job
	w.currentJobMu.Lock()
	if w.jobCancel != nil {
		w.jobCancel()
	}
	w.currentJobMu.Unlock()

	// Then stop the worker
	w.Stop()
}

// formatDuration formats a duration as a human-readable string
func formatDuration(d time.Duration) string {
	if d < 0 {
		return ""
	}

	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// formatBytes formats bytes as a human-readable string
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
