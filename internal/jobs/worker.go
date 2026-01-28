package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/ffmpeg/vmaf"
	"github.com/gwlsn/shrinkray/internal/logger"
	"github.com/gwlsn/shrinkray/internal/util"
)

// CacheInvalidator is called when a file is transcoded to invalidate cached probe data
type CacheInvalidator func(path string)

// Worker processes transcoding jobs from the queue
type Worker struct {
	id              int
	pool            *WorkerPool
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
	jobDone      chan struct{} // Closed when current job finishes
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

	// Pause state - when true, workers won't pick up new jobs
	paused   bool
	pausedMu sync.RWMutex

	analysisSem chan struct{} // Limits concurrent VMAF analysis
}

// SmartShrink quality thresholds (hardcoded for simplicity)
const (
	vmafAcceptable = 85.0
	vmafGood       = 93.0
	vmafExcellent  = 96.0
)

// getSmartShrinkThreshold returns the VMAF threshold for a quality tier
func getSmartShrinkThreshold(quality string) float64 {
	switch quality {
	case "acceptable":
		return vmafAcceptable
	case "excellent":
		return vmafExcellent
	default:
		return vmafGood
	}
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(queue *Queue, cfg *config.Config, invalidateCache CacheInvalidator) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	analysisSem := make(chan struct{}, 1) // Only 1 concurrent VMAF analysis

	pool := &WorkerPool{
		workers:         make([]*Worker, 0, cfg.Workers),
		queue:           queue,
		cfg:             cfg,
		invalidateCache: invalidateCache,
		nextWorkerID:    0,
		ctx:             ctx,
		cancel:          cancel,
		analysisSem:     analysisSem,
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
		pool:            p,
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
		if done := w.CancelCurrentJob(jobID); done != nil {
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

			// CancelAndStop waits for worker to finish (wg.Wait)
			rj.worker.CancelAndStop()

			// Worker is done. Job left as "running" by shutdown path.
			// Safe to requeue now - moves to front of pending queue.
			if err := p.queue.Requeue(rj.jobID); err != nil {
				logger.Warn("Failed to requeue job during resize", "job_id", rj.jobID, "error", err)
			}

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

// IsPaused returns whether job processing is paused
func (p *WorkerPool) IsPaused() bool {
	p.pausedMu.RLock()
	defer p.pausedMu.RUnlock()
	return p.paused
}

// Pause stops all running jobs and prevents new jobs from starting.
// Returns the number of jobs that were requeued.
func (p *WorkerPool) Pause() int {
	p.pausedMu.Lock()
	p.paused = true
	p.pausedMu.Unlock()

	// Collect all running jobs
	p.mu.Lock()
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
	p.mu.Unlock()

	// Sort running jobs by ID ascending (oldest first) so we can requeue in correct order
	sort.Slice(runningJobs, func(i, j int) bool {
		return runningJobs[i].jobID < runningJobs[j].jobID
	})

	// Requeue in REVERSE order (newest first) so oldest ends up at front of queue
	// Requeue adds to front, so: requeue(3), requeue(2), requeue(1) → [1, 2, 3, ...]
	count := 0
	for i := len(runningJobs) - 1; i >= 0; i-- {
		rj := runningJobs[i]
		// Requeue FIRST while job is still "running" - this changes status to "pending"
		if err := p.queue.Requeue(rj.jobID); err != nil {
			logger.Warn("Failed to requeue job during pause", "job_id", rj.jobID, "error", err)
			continue
		}
		count++

		// Now cancel the job and wait for it to finish
		done := rj.worker.CancelCurrentJob(rj.jobID)
		if done != nil {
			<-done
		}
	}

	return count
}

// Unpause allows workers to pick up jobs again
func (p *WorkerPool) Unpause() {
	p.pausedMu.Lock()
	p.paused = false
	p.pausedMu.Unlock()
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
			// Check if paused (user clicked Stop)
			if w.pool.IsPaused() {
				select {
				case <-w.ctx.Done():
					return
				case <-time.After(500 * time.Millisecond):
					continue
				}
			}

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

	// Create a cancellable context for this job
	jobCtx, jobCancel := context.WithCancel(w.ctx)
	defer jobCancel()

	w.currentJobMu.Lock()
	w.currentJob = job
	w.jobCancel = jobCancel
	w.jobDone = make(chan struct{})
	w.currentJobMu.Unlock()

	defer func() {
		w.currentJobMu.Lock()
		w.currentJob = nil
		w.jobCancel = nil
		if w.jobDone != nil {
			close(w.jobDone)
			w.jobDone = nil
		}
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
	tempPath := ffmpeg.BuildTempPath(job.InputPath, tempDir, w.cfg.OutputFormat)

	// Mark job as started (first worker to call this wins)
	if err := w.queue.StartJob(job.ID, tempPath); err != nil {
		// Another worker claimed this job, or it was cancelled
		return
	}

	logger.Info("Job started", "job_id", job.ID, "file", job.InputPath, "preset", job.PresetID)

	// Initialize quality settings (may be overridden by SmartShrink analysis)
	qualityHEVC := w.cfg.QualityHEVC
	qualityAV1 := w.cfg.QualityAV1
	var qualityMod float64

	// Check if this is a SmartShrink preset
	if preset.IsSmartShrink {
		var shouldSkip bool
		var skipReason string
		var selectedCRF int
		var vmafScore float64
		var err error
		shouldSkip, skipReason, selectedCRF, qualityMod, vmafScore, err = w.pool.runSmartShrinkAnalysis(jobCtx, job, preset)
		if err != nil {
			// Check if context was cancelled (user cancel or shutdown)
			if jobCtx.Err() != nil {
				// Job will be handled appropriately:
				// - User cancel: jobCtx cancelled but w.ctx still active
				// - Shutdown: w.ctx also cancelled, job left as running for restart
				if w.ctx.Err() == nil {
					logger.Info("Job cancelled during analysis", "job_id", job.ID)
					_ = w.queue.CancelJob(job.ID)
				} else {
					logger.Info("Job interrupted by shutdown during analysis", "job_id", job.ID)
				}
				return
			}
			logger.Error("SmartShrink analysis failed", "job_id", job.ID, "error", err.Error())
			_ = w.queue.FailJob(job.ID, err.Error())
			return
		}

		if shouldSkip {
			logger.Info("Job skipped by SmartShrink", "job_id", job.ID, "reason", skipReason)
			_ = w.queue.SkipJob(job.ID, skipReason)
			return
		}

		// Store VMAF results
		_ = w.queue.UpdateJobVMAFResult(job.ID, vmafScore, selectedCRF, qualityMod)

		// Update phase to encoding
		_ = w.queue.UpdateJobPhase(job.ID, PhaseEncoding)

		// Set quality overrides for transcode
		if selectedCRF > 0 {
			qualityHEVC = selectedCRF
			qualityAV1 = selectedCRF
		}
		if qualityMod > 0 {
			logger.Info("SmartShrink selected quality modifier",
				"job_id", job.ID,
				"quality_mod", qualityMod,
			)
		}

		logger.Info("SmartShrink analysis complete",
			"job_id", job.ID,
			"vmaf_score", vmafScore,
			"selected_crf", selectedCRF,
		)
	}

	// Create progress channel
	progressCh := make(chan ffmpeg.Progress, 10)

	// Start progress forwarding
	go func() {
		for progress := range progressCh {
			eta := util.FormatDuration(progress.ETA)
			w.queue.UpdateProgress(job.ID, progress.Percent, progress.Speed, eta)
		}
	}()

	// Run the transcode
	duration := time.Duration(job.Duration) * time.Millisecond
	// Calculate total frames for frame-based progress fallback (VAAPI reports N/A for time)
	totalFrames := int64(float64(job.Duration) / 1000.0 * job.FrameRate)

	// Proactive check: does this file require software decode?
	// This detects known-unsupported formats (e.g., H.264 10-bit) before wasting time on a failed attempt
	useSoftwareDecode := ffmpeg.RequiresSoftwareDecode(job.VideoCodec, job.Profile, job.BitDepth, preset.Encoder)
	if useSoftwareDecode {
		logger.Debug("Using software decode for unsupported codec/profile",
			"job_id", job.ID,
			"codec", job.VideoCodec,
			"profile", job.Profile,
			"bit_depth", job.BitDepth,
		)
	}

	// Set up HDR tonemapping if enabled and source is HDR
	var tonemapParams *ffmpeg.TonemapParams
	if job.IsHDR && w.cfg.TonemapHDR {
		tonemapParams = &ffmpeg.TonemapParams{
			IsHDR:         true,
			EnableTonemap: true,
			Algorithm:     w.cfg.TonemapAlgorithm,
		}
		logger.Debug("HDR tonemapping enabled",
			"job_id", job.ID,
			"algorithm", w.cfg.TonemapAlgorithm,
		)
	}

	result, err := w.transcoder.Transcode(jobCtx, job.InputPath, tempPath, preset, duration, job.Bitrate, job.Width, job.Height, qualityHEVC, qualityAV1, qualityMod, totalFrames, progressCh, useSoftwareDecode, w.cfg.OutputFormat, tonemapParams)

	if err != nil {
		// Check if it was cancelled
		if jobCtx.Err() == context.Canceled {
			os.Remove(tempPath)
			// Only mark as cancelled if user-initiated (jobCtx cancelled but w.ctx still active)
			// If w.ctx is also cancelled, this is a shutdown - leave job as "running"
			// so it will be reset to pending on restart
			// Also skip if job was requeued by Pause() (status changed to pending)
			if w.ctx.Err() != context.Canceled && job.Status == StatusRunning {
				logger.Info("Job cancelled", "job_id", job.ID)
				_ = w.queue.CancelJob(job.ID)
			} else if w.ctx.Err() == context.Canceled {
				logger.Info("Job interrupted by shutdown", "job_id", job.ID)
			}
			return
		}

		// If using hardware encoder and it failed, retry with software decode
		// Skip if we already used software decode
		if !useSoftwareDecode && shouldRetryWithSoftwareDecode(preset.Encoder) {
			logger.Warn("Hardware transcode failed, retrying with software decode", "job_id", job.ID, "error", err.Error())

			// Create new progress channel for retry
			retryProgressCh := make(chan ffmpeg.Progress, 10)
			go func() {
				for progress := range retryProgressCh {
					eta := util.FormatDuration(progress.ETA)
					w.queue.UpdateProgress(job.ID, progress.Percent, progress.Speed, eta)
				}
			}()

			// Retry with software decode
			result, err = w.transcoder.Transcode(jobCtx, job.InputPath, tempPath, preset, duration, job.Bitrate, job.Width, job.Height, qualityHEVC, qualityAV1, qualityMod, totalFrames, retryProgressCh, true, w.cfg.OutputFormat, tonemapParams)

			if err != nil {
				// Check if cancelled during retry
				if jobCtx.Err() == context.Canceled {
					os.Remove(tempPath)
					// Only mark as cancelled if user-initiated, not shutdown
					// Also skip if job was requeued by Pause() (status changed to pending)
					if w.ctx.Err() != context.Canceled && job.Status == StatusRunning {
						logger.Info("Job cancelled during software decode retry", "job_id", job.ID)
						_ = w.queue.CancelJob(job.ID)
					} else if w.ctx.Err() == context.Canceled {
						logger.Info("Job interrupted by shutdown during retry", "job_id", job.ID)
					}
					return
				}

				// Retry also failed
				os.Remove(tempPath)
				logger.Error("Job failed after software decode retry", "job_id", job.ID, "error", err.Error())
				_ = w.queue.FailJob(job.ID, err.Error())
				return
			}

			logger.Info("Software decode fallback succeeded", "job_id", job.ID)
		} else {
			// Already used software decode or not using hardware encoder, fail the job
			os.Remove(tempPath)
			logger.Error("Job failed", "job_id", job.ID, "error", err.Error())
			_ = w.queue.FailJob(job.ID, err.Error())
			return
		}
	}

	// Check if transcoded file is larger than original
	if result.OutputSize >= job.InputSize && !w.cfg.KeepLargerFiles {
		// Delete the temp file and skip the job (not fail - this is expected behavior)
		os.Remove(tempPath)
		logger.Warn("Job skipped - output larger than input", "job_id", job.ID, "input_size", util.FormatBytes(job.InputSize), "output_size", util.FormatBytes(result.OutputSize))
		_ = w.queue.SkipJob(job.ID, fmt.Sprintf("Output larger than original (%s > %s)",
			util.FormatBytes(result.OutputSize), util.FormatBytes(job.InputSize)))
		return
	} else if result.OutputSize >= job.InputSize {
		logger.Warn("Output larger than input but keeping (keep_larger_files enabled)", "job_id", job.ID, "input_size", util.FormatBytes(job.InputSize), "output_size", util.FormatBytes(result.OutputSize))
	}

	// Finalize the transcode (handle original file)
	replace := w.cfg.OriginalHandling == "replace"
	finalPath, err := ffmpeg.FinalizeTranscode(job.InputPath, tempPath, w.cfg.OutputFormat, replace)
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

	logger.Info("Job complete", "job_id", job.ID, "duration", util.FormatDuration(elapsed), "saved", util.FormatBytes(saved))

	// Mark job complete
	_ = w.queue.CompleteJob(job.ID, finalPath, result.OutputSize)
}

// CancelCurrentJob cancels the job if it matches the given ID.
// Returns a channel that will be closed when the job finishes, or nil if job not found.
func (w *Worker) CancelCurrentJob(jobID string) <-chan struct{} {
	w.currentJobMu.Lock()
	defer w.currentJobMu.Unlock()

	if w.currentJob != nil && w.currentJob.ID == jobID && w.jobCancel != nil {
		w.jobCancel()
		return w.jobDone
	}
	return nil
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

// shouldRetryWithSoftwareDecode returns true if we should retry with software decode.
// Simple rule: if using a hardware encoder and the transcode failed, try software decode once.
// This catches all hardware failures (initialization, mid-stream, EOF issues) without
// fragile pattern matching on error messages.
//
// Note: Jellyfin doesn't even have automatic retry (Issue #2314 was closed without implementation).
// Our approach is more robust - we always try software decode fallback once.
func shouldRetryWithSoftwareDecode(encoder ffmpeg.HWAccel) bool {
	// Software encoder has no hardware decode to fall back from
	return encoder != ffmpeg.HWAccelNone
}

// runSmartShrinkAnalysis performs VMAF analysis and returns the optimal quality settings.
// Returns (shouldSkip, skipReason, selectedCRF, qualityMod, vmafScore, error)
func (wp *WorkerPool) runSmartShrinkAnalysis(ctx context.Context, job *Job, preset *ffmpeg.Preset) (bool, string, int, float64, float64, error) {
	// Acquire analysis semaphore
	select {
	case wp.analysisSem <- struct{}{}:
		defer func() { <-wp.analysisSem }()
	case <-ctx.Done():
		return false, "", 0, 0, 0, ctx.Err()
	}

	// Update phase
	_ = wp.queue.UpdateJobPhase(job.ID, PhaseAnalyzing)

	// Check HDR without tonemap
	if job.IsHDR && !wp.cfg.TonemapHDR {
		return true, "HDR requires tonemapping for SmartShrink", 0, 0, 0, nil
	}

	// Check HDR detected via fallback (missing color_transfer metadata)
	// Tonemapping requires proper transfer function (smpte2084, arib-std-b67, etc.)
	if job.IsHDR && wp.cfg.TonemapHDR && job.ColorTransfer == "" {
		return true, "HDR tonemapping requires color transfer metadata (poorly-tagged HDR)", 0, 0, 0, nil
	}

	// Check video duration
	duration := time.Duration(job.Duration) * time.Millisecond
	if duration < 5*time.Second {
		return true, "Video too short for analysis", 0, 0, 0, nil
	}

	// Get quality range for this encoder
	qRange := ffmpeg.GetQualityRange(preset.Encoder, preset.Codec)

	// Get temp directory for analysis
	tempDir := wp.cfg.GetTempDir(job.InputPath)

	// Get threshold from job's quality tier
	threshold := getSmartShrinkThreshold(job.SmartShrinkQuality)

	// Create analyzer
	analyzer := vmaf.NewAnalyzer(wp.cfg.FFmpegPath, tempDir)

	// Set up tonemapping for HDR content with tonemapping enabled.
	var encodeTonemapParams *ffmpeg.TonemapParams
	if job.IsHDR && wp.cfg.TonemapHDR {
		analyzer.WithTonemap(true, wp.cfg.TonemapAlgorithm)
	}

	// Create encode callback
	encodeSample := func(ctx context.Context, samplePath string, quality int, modifier float64) (string, error) {
		inputArgs, outputArgs := ffmpeg.BuildSampleEncodeArgs(
			preset, job.Width, job.Height,
			quality, modifier,
			true, // Force software decode for FFV1 samples
			encodeTonemapParams,
		)

		outputPath := samplePath + ".encoded.mkv"

		args := make([]string, 0, len(inputArgs)+len(outputArgs)+4)
		args = append(args, inputArgs...)
		args = append(args, "-i", samplePath)
		args = append(args, outputArgs...)
		args = append(args, "-y", outputPath)

		cmd := exec.CommandContext(ctx, wp.cfg.FFmpegPath, args...)
		if err := cmd.Run(); err != nil {
			return "", err
		}

		return outputPath, nil
	}

	// Run analysis with threshold
	result, err := analyzer.Analyze(ctx, job.InputPath, duration, job.Height, qRange, threshold, encodeSample)
	if err != nil {
		return false, "", 0, 0, 0, fmt.Errorf("VMAF analysis failed: %w", err)
	}

	if result.ShouldSkip {
		return true, result.SkipReason, 0, 0, 0, nil
	}

	return false, "", result.OptimalCRF, result.QualityMod, result.VMafScore, nil
}
