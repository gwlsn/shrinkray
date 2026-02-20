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

// FileUpdateNotifier is called after a transcode completes to re-probe the
// output file and push updated metadata (codec, resolution, size) to SSE clients.
type FileUpdateNotifier func(path string)

// Worker processes transcoding jobs from the queue
type Worker struct {
	id                 int
	pool               *WorkerPool
	queue              *Queue
	transcoder         *ffmpeg.Transcoder
	prober             *ffmpeg.Prober
	cfg                *config.Config
	invalidateCache    CacheInvalidator
	notifyFileUpdate   FileUpdateNotifier

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
	mu               sync.Mutex
	workers          []*Worker
	queue            *Queue
	cfg              *config.Config
	invalidateCache  CacheInvalidator
	notifyFileUpdate FileUpdateNotifier
	nextWorkerID     int

	ctx    context.Context
	cancel context.CancelFunc

	// Pause state - when true, workers won't pick up new jobs
	paused   bool
	pausedMu sync.RWMutex

	// Semaphore for VMAF analysis - configurable independently of worker count
	// VMAF is CPU-intensive so we limit concurrent analyses to control CPU usage
	analysisMu    sync.Mutex
	analysisCount int // Currently running analyses
	analysisLimit int // Max concurrent analyses (from config, 1-3)
}

// SmartShrink quality thresholds (hardcoded for simplicity)
const (
	vmafAcceptable = 85.0
	vmafGood       = 90.0
	vmafExcellent  = 94.0
)

// runningJob tracks a job being processed by a worker.
// Used by Resize and Pause to collect and manage running jobs.
type runningJob struct {
	worker *Worker
	jobID  string
}

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
func NewWorkerPool(queue *Queue, cfg *config.Config, invalidateCache CacheInvalidator, notifyFileUpdate FileUpdateNotifier) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	// Use configured limit for concurrent VMAF analyses.
	// VMAF scoring is CPU-intensive (libvmaf cannot be hardware accelerated).
	// Each analysis uses ~50% of CPU cores, so multiple concurrent analyses
	// can saturate the CPU. Default is 1 for media server friendliness.
	analysisLimit := ClampAnalysisCount(cfg.MaxConcurrentAnalyses)

	pool := &WorkerPool{
		workers:          make([]*Worker, 0, cfg.Workers),
		queue:            queue,
		cfg:              cfg,
		invalidateCache:  invalidateCache,
		notifyFileUpdate: notifyFileUpdate,
		nextWorkerID:     0,
		ctx:              ctx,
		cancel:           cancel,
		analysisLimit:    analysisLimit, // Allow concurrent analysis matching worker count
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
		id:               p.nextWorkerID,
		pool:             p,
		queue:            p.queue,
		transcoder:       ffmpeg.NewTranscoder(p.cfg.FFmpegPath),
		prober:           ffmpeg.NewProber(p.cfg.FFprobePath),
		cfg:              p.cfg,
		invalidateCache:  p.invalidateCache,
		notifyFileUpdate: p.notifyFileUpdate,
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
	n = ClampWorkerCount(n)

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

// SetAnalysisLimit updates the maximum concurrent VMAF analyses.
// This is independent of worker count since VMAF is CPU-intensive.
// Unlike worker resize, running analyses are NOT cancelled - they complete
// and the new limit takes effect for subsequent analyses.
func (p *WorkerPool) SetAnalysisLimit(n int) {
	n = ClampAnalysisCount(n)

	p.analysisMu.Lock()
	old := p.analysisLimit
	p.analysisLimit = n
	current := p.analysisCount
	p.analysisMu.Unlock()

	// Update config
	p.cfg.MaxConcurrentAnalyses = n

	if old != n {
		logger.Info("Analysis limit changed",
			"old", old,
			"new", n,
			"currently_running", current)
	}
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

// tryEncoderFallbacks attempts to transcode using fallback encoders after the primary encoder failed.
// It tries each fallback encoder with HW decode (if appropriate), then SW decode, before moving to the next.
// Returns the successful result, or priorError if all fallbacks fail.
func (w *Worker) tryEncoderFallbacks(
	jobCtx context.Context,
	job *Job,
	opts ffmpeg.TranscodeOptions, //nolint:gocritic // by value: fallback loop copies and mutates per encoder
	tempPath string,
	priorError error,
) (*ffmpeg.TranscodeResult, error) {
	currentEncoder := opts.Preset.Encoder
	lastError := priorError

	for {
		// Check for cancellation before trying next fallback
		if jobCtx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		fallback := ffmpeg.GetFallbackEncoder(currentEncoder, opts.Preset.Codec)
		if fallback == nil {
			// No more fallbacks available - return the last error we saw
			return nil, lastError
		}

		logger.Warn("Encoder failed, trying fallback",
			"job_id", job.ID,
			"failed_encoder", currentEncoder,
			"fallback_encoder", fallback.Accel)

		// Build fallback opts: swap encoder, recompute SW decode requirement.
		// Value copy ensures the caller's opts is unmodified.
		fallbackOpts := opts
		fallbackOpts.Preset = opts.Preset.WithEncoder(fallback.Accel)

		// Recompute whether this fallback encoder needs software decode
		// (each encoder has different decode capabilities)
		fallbackNeedsSWDecode := ffmpeg.RequiresSoftwareDecode(
			job.VideoCodec, job.Profile, job.BitDepth, fallback.Accel)

		// Try with HW decode first (unless this encoder requires SW decode)
		if !fallbackNeedsSWDecode {
			fallbackOpts.SoftwareDecode = false
			result, err := w.attemptTranscode(jobCtx, job, fallbackOpts, tempPath)

			if err == nil {
				logger.Info("Fallback encoder succeeded", "job_id", job.ID, "encoder", fallback.Accel)
				return result, nil
			}

			if jobCtx.Err() == context.Canceled {
				return nil, context.Canceled
			}

			lastError = err
		}

		// Try SW decode with fallback encoder (unless it's software encoder - no point)
		if shouldRetryWithSoftwareDecode(fallback.Accel) {
			fallbackOpts.SoftwareDecode = true
			result, err := w.attemptTranscode(jobCtx, job, fallbackOpts, tempPath)

			if err == nil {
				logger.Info("Fallback encoder succeeded with SW decode", "job_id", job.ID, "encoder", fallback.Accel)
				return result, nil
			}

			if jobCtx.Err() == context.Canceled {
				return nil, context.Canceled
			}

			lastError = err
		}

		// This fallback also failed, try next
		currentEncoder = fallback.Accel
	}
}

// attemptTranscode runs a single transcode attempt with a fresh progress channel.
// This helper centralizes progress channel creation and forwarding.
func (w *Worker) attemptTranscode(
	jobCtx context.Context,
	job *Job,
	opts ffmpeg.TranscodeOptions, //nolint:gocritic // by value: forwarded to Transcode which also takes by value
	tempPath string,
) (*ffmpeg.TranscodeResult, error) {
	// Create fresh progress channel (Transcode closes it when done)
	progressCh := make(chan ffmpeg.Progress, 10)
	go func() {
		for progress := range progressCh {
			eta := util.FormatDuration(progress.ETA)
			w.queue.UpdateProgress(job.ID, progress.Percent, progress.Speed, eta)
		}
	}()

	return w.transcoder.Transcode(jobCtx, job.InputPath, tempPath, opts, progressCh)
}

// processJob handles a single transcoding job by orchestrating four phases:
// prepare, build options, execute transcode, and finalize results.
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

	// Phase 1: Validate preset, build temp path, mark job started
	preset, tempPath, err := w.prepareJob(job)
	if err != nil {
		return
	}

	// Phase 2: Run SmartShrink analysis (if applicable), resolve quality/decode/HDR/subtitle params
	opts, err := w.buildTranscodeOpts(jobCtx, job, preset)
	if err != nil {
		return
	}

	// Phase 3: Execute transcode with recovery strategies (SW decode, encoder fallbacks)
	result, transcodeErr := w.executeTranscode(jobCtx, job, opts, tempPath)

	// Phase 4: Handle result (cancellation, failure, size check, file move, completion)
	w.finalizeJob(jobCtx, job, result, transcodeErr, tempPath, startTime)
}

// prepareJob validates the preset, builds the temp output path, and marks the job as started.
// Returns the preset and temp path on success. On error, the job status is already updated
// (failed or claimed by another worker) and the caller should return without further action.
func (w *Worker) prepareJob(job *Job) (*ffmpeg.Preset, string, error) {
	preset := ffmpeg.GetPreset(job.PresetID)
	if preset == nil {
		logger.Error("Job failed", "job_id", job.ID, "error", "unknown preset", "preset", job.PresetID)
		if err := w.queue.FailJob(job.ID, fmt.Sprintf("unknown preset: %s", job.PresetID)); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "FailJob", "error", err)
		}
		return nil, "", fmt.Errorf("unknown preset: %s", job.PresetID)
	}

	tempDir := w.cfg.GetTempDir()
	tempPath := ffmpeg.BuildTempPath(job.InputPath, tempDir, w.cfg.OutputFormat)

	// Mark job as started (first worker to call this wins)
	if err := w.queue.StartJob(job.ID, tempPath); err != nil {
		// Another worker claimed this job, or it was cancelled
		return nil, "", err
	}

	logger.Info("Job started", "job_id", job.ID, "file", job.InputPath, "preset", job.PresetID)
	return preset, tempPath, nil
}

// buildTranscodeOpts resolves quality settings (including SmartShrink analysis if applicable),
// detects hardware/software decode requirements, sets up HDR tonemapping, filters subtitles,
// and constructs the TranscodeOptions struct. On error, the job status is already updated
// (SmartShrink skip/cancel/fail) and the caller should return without further action.
func (w *Worker) buildTranscodeOpts(jobCtx context.Context, job *Job, preset *ffmpeg.Preset) (ffmpeg.TranscodeOptions, error) {
	var zero ffmpeg.TranscodeOptions

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
				if w.ctx.Err() == nil {
					logger.Info("Job cancelled during analysis", "job_id", job.ID)
					if err := w.queue.CancelJob(job.ID); err != nil {
						logger.Warn("Failed to update job state", "job_id", job.ID, "op", "CancelJob", "error", err)
					}
				} else {
					logger.Info("Job interrupted by shutdown during analysis", "job_id", job.ID)
				}
				return zero, fmt.Errorf("analysis interrupted")
			}
			logger.Error("SmartShrink analysis failed", "job_id", job.ID, "error", err.Error())
			if err := w.queue.FailJob(job.ID, err.Error()); err != nil {
				logger.Warn("Failed to update job state", "job_id", job.ID, "op", "FailJob", "error", err)
			}
			return zero, fmt.Errorf("analysis failed")
		}

		if shouldSkip {
			logger.Info("Job skipped by SmartShrink", "job_id", job.ID, "reason", skipReason)
			if err := w.queue.SkipJob(job.ID, skipReason); err != nil {
				logger.Warn("Failed to update job state", "job_id", job.ID, "op", "SkipJob", "error", err)
			}
			return zero, fmt.Errorf("job skipped")
		}

		// Store VMAF results
		if err := w.queue.UpdateJobVMAFResult(job.ID, vmafScore, selectedCRF, qualityMod); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "UpdateJobVMAFResult", "error", err)
		}

		// Update phase to encoding
		if err := w.queue.UpdateJobPhase(job.ID, PhaseEncoding); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "UpdateJobPhase", "error", err)
		}

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

	// Compute transcode parameters
	duration := time.Duration(job.Duration) * time.Millisecond
	// Calculate total frames for frame-based progress fallback (VAAPI reports N/A for time)
	totalFrames := int64(float64(job.Duration) / 1000.0 * job.FrameRate)

	// Proactive check: does this file require software decode?
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

	// For MKV output, filter incompatible subtitle codecs to avoid muxing failures.
	var subtitleIndices []int // nil = map all (default)
	if w.cfg.OutputFormat == "mkv" {
		probeCtx, probeCancel := context.WithTimeout(jobCtx, 10*time.Second)
		subtitleStreams, err := w.prober.ProbeSubtitles(probeCtx, job.InputPath)
		probeCancel()

		if err != nil {
			logger.Warn("Failed to probe subtitles, using default mapping",
				"job_id", job.ID, "error", err)
		} else if len(subtitleStreams) > 0 {
			compatible, dropped := ffmpeg.FilterMKVCompatible(subtitleStreams)
			if len(dropped) > 0 {
				logger.Warn("Dropping incompatible subtitle streams",
					"job_id", job.ID,
					"dropped", dropped,
					"reason", "not supported in MKV container")
			}
			subtitleIndices = compatible
		}
	}

	return ffmpeg.TranscodeOptions{
		Preset:          preset,
		SourceBitrate:   job.Bitrate,
		SourceWidth:     job.Width,
		SourceHeight:    job.Height,
		Duration:        duration,
		TotalFrames:     totalFrames,
		QualityHEVC:     qualityHEVC,
		QualityAV1:      qualityAV1,
		QualityMod:      qualityMod,
		SoftwareDecode:  useSoftwareDecode,
		OutputFormat:    w.cfg.OutputFormat,
		Tonemap:         tonemapParams,
		SubtitleIndices: subtitleIndices,
	}, nil
}

// executeTranscode runs the primary transcode attempt and, if the hardware encoder fails,
// tries recovery strategies: software decode fallback, then encoder fallback chain.
// Returns the result and any error. Does NOT update job status (the caller handles that).
func (w *Worker) executeTranscode(
	jobCtx context.Context,
	job *Job,
	opts ffmpeg.TranscodeOptions, //nolint:gocritic // by value: recovery strategies copy and mutate opts
	tempPath string,
) (*ffmpeg.TranscodeResult, error) {
	// Create progress channel for the primary transcode attempt
	progressCh := make(chan ffmpeg.Progress, 10)

	// Start progress forwarding
	go func() {
		for progress := range progressCh {
			eta := util.FormatDuration(progress.ETA)
			w.queue.UpdateProgress(job.ID, progress.Percent, progress.Speed, eta)
		}
	}()

	result, err := w.transcoder.Transcode(jobCtx, job.InputPath, tempPath, opts, progressCh)

	// Recovery strategies for hardware encoder failures
	if err != nil && jobCtx.Err() != context.Canceled && opts.Preset.Encoder != ffmpeg.HWAccelNone {
		// Strategy 1: If we used HW decode, retry with SW decode (same encoder)
		if !opts.SoftwareDecode && shouldRetryWithSoftwareDecode(opts.Preset.Encoder) {
			logger.Warn("Hardware transcode failed, retrying with software decode",
				"job_id", job.ID, "error", err.Error())

			retryOpts := opts
			retryOpts.SoftwareDecode = true
			result, err = w.attemptTranscode(jobCtx, job, retryOpts, tempPath)

			if err == nil {
				logger.Info("Software decode fallback succeeded", "job_id", job.ID)
			}
		}

		// Strategy 2: Try fallback encoders (only if still failing)
		if err != nil && jobCtx.Err() != context.Canceled {
			logger.Warn("Primary encoder failed, trying fallback encoders",
				"job_id", job.ID, "encoder", opts.Preset.Encoder, "error", err.Error())

			result, err = w.tryEncoderFallbacks(jobCtx, job, opts, tempPath, err)
		}
	}

	return result, err
}

// finalizeJob handles the transcode outcome: cancellation cleanup, failure reporting,
// output size check, file finalization (move/rename), cache invalidation, and job completion.
func (w *Worker) finalizeJob(
	jobCtx context.Context,
	job *Job,
	result *ffmpeg.TranscodeResult,
	transcodeErr error,
	tempPath string,
	startTime time.Time,
) {
	// Handle cancellation (could happen at any point during transcode)
	if jobCtx.Err() == context.Canceled {
		if removeErr := os.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
			logger.Warn("Failed to remove temp file", "path", tempPath, "error", removeErr)
		}
		if w.ctx.Err() != context.Canceled && job.Status == StatusRunning {
			logger.Info("Job cancelled", "job_id", job.ID)
			if err := w.queue.CancelJob(job.ID); err != nil {
				logger.Warn("Failed to update job state", "job_id", job.ID, "op", "CancelJob", "error", err)
			}
		} else if w.ctx.Err() == context.Canceled {
			logger.Info("Job interrupted by shutdown", "job_id", job.ID)
		}
		return
	}

	// Handle final failure (after all recovery strategies exhausted)
	if transcodeErr != nil {
		if removeErr := os.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
			logger.Warn("Failed to remove temp file", "path", tempPath, "error", removeErr)
		}
		logger.Error("Job failed", "job_id", job.ID, "error", transcodeErr.Error())
		if err := w.queue.FailJob(job.ID, transcodeErr.Error()); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "FailJob", "error", err)
		}
		return
	}

	// Check if transcoded file is larger than original
	if result.OutputSize >= job.InputSize && !w.cfg.KeepLargerFiles {
		// Delete the temp file and skip the job (not fail, this is expected behavior)
		if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("Failed to remove temp file", "path", tempPath, "error", err)
		}
		logger.Warn("Job skipped - output larger than input", "job_id", job.ID, "input_size", util.FormatBytes(job.InputSize), "output_size", util.FormatBytes(result.OutputSize))
		if err := w.queue.SkipJob(job.ID, fmt.Sprintf("Output larger than original (%s > %s)",
			util.FormatBytes(result.OutputSize), util.FormatBytes(job.InputSize))); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "SkipJob", "error", err)
		}
		return
	} else if result.OutputSize >= job.InputSize {
		logger.Warn("Output larger than input but keeping (keep_larger_files enabled)", "job_id", job.ID, "input_size", util.FormatBytes(job.InputSize), "output_size", util.FormatBytes(result.OutputSize))
	}

	// Finalize the transcode (handle original file)
	replace := w.cfg.OriginalHandling == "replace"
	finalPath, err := ffmpeg.FinalizeTranscode(job.InputPath, tempPath, w.cfg.OutputFormat, replace)
	if err != nil {
		// Try to clean up
		if removeErr := os.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
			logger.Warn("Failed to remove temp file", "path", tempPath, "error", removeErr)
		}
		logger.Error("Job failed - finalization error", "job_id", job.ID, "error", err.Error())
		if err := w.queue.FailJob(job.ID, fmt.Sprintf("failed to finalize: %v", err)); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "FailJob", "error", err)
		}
		return
	}

	// Invalidate cache for the output file so browser shows updated metadata
	if w.invalidateCache != nil {
		w.invalidateCache(finalPath)
		// Also invalidate the original path in case it was cached
		w.invalidateCache(job.InputPath)
	}

	// Asynchronously re-probe and push updated file metadata to SSE clients.
	if w.notifyFileUpdate != nil {
		go func() {
			w.notifyFileUpdate(finalPath)
			if finalPath != job.InputPath {
				w.notifyFileUpdate(job.InputPath)
			}
		}()
	}

	// Calculate stats
	elapsed := time.Since(startTime)
	saved := job.InputSize - result.OutputSize

	logger.Info("Job complete", "job_id", job.ID, "duration", util.FormatDuration(elapsed), "saved", util.FormatBytes(saved))

	// Mark job complete
	if err := w.queue.CompleteJob(job.ID, finalPath, result.OutputSize); err != nil {
		logger.Warn("Failed to update job state", "job_id", job.ID, "op", "CompleteJob", "error", err)
	}
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
	// SmartShrink does not support HDR content — VMAF is validated for SDR only.
	// Check before acquiring analysis slot so HDR jobs don't block SDR analyses.
	if job.IsHDR {
		return true, "SmartShrink does not support HDR content. Use a Compress preset or tonemap to SDR first", 0, 0, 0, nil
	}

	// TryAcquire: if a slot is immediately available, go straight to analyzing (no flicker).
	// If contended, show waiting state until a slot is released.
	wp.analysisMu.Lock()
	if wp.analysisCount < wp.analysisLimit {
		wp.analysisCount++
		wp.analysisMu.Unlock()
		if err := wp.queue.UpdateJobPhase(job.ID, PhaseAnalyzing); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "UpdateJobPhase", "error", err)
		}
	} else {
		wp.analysisMu.Unlock()
		// Slot contended - show waiting state, then spin until one is free
		if err := wp.queue.UpdateJobPhase(job.ID, PhaseWaitingAnalysis); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "UpdateJobPhase", "error", err)
		}
		for {
			wp.analysisMu.Lock()
			if wp.analysisCount < wp.analysisLimit {
				wp.analysisCount++
				wp.analysisMu.Unlock()
				break
			}
			wp.analysisMu.Unlock()

			select {
			case <-ctx.Done():
				return false, "", 0, 0, 0, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err := wp.queue.UpdateJobPhase(job.ID, PhaseAnalyzing); err != nil {
			logger.Warn("Failed to update job state", "job_id", job.ID, "op", "UpdateJobPhase", "error", err)
		}
	}

	defer func() {
		wp.analysisMu.Lock()
		wp.analysisCount--
		wp.analysisMu.Unlock()
	}()

	// Check video duration
	duration := time.Duration(job.Duration) * time.Millisecond
	if duration < 5*time.Second {
		return true, "Video too short for analysis", 0, 0, 0, nil
	}

	// Get quality range for this encoder
	qRange := ffmpeg.GetQualityRange(preset.Encoder, preset.Codec)

	tempDir := wp.cfg.GetTempDir()

	// Get threshold from job's quality tier
	threshold := getSmartShrinkThreshold(job.SmartShrinkQuality)

	// Create analyzer
	analyzer := vmaf.NewAnalyzer(wp.cfg.FFmpegPath, tempDir)

	// Create encode callback
	encodeSample := func(ctx context.Context, samplePath string, quality int, modifier float64) (string, error) {
		sampleOpts := ffmpeg.TranscodeOptions{
			Preset:         preset,
			SourceWidth:    job.Width,
			SourceHeight:   job.Height,
			QualityHEVC:    quality,
			QualityAV1:     quality,
			QualityMod:     modifier,
			SoftwareDecode: true, // Force software decode for FFV1 samples
		}
		inputArgs, outputArgs := ffmpeg.BuildSampleEncodeArgs(sampleOpts)

		outputPath := samplePath + ".encoded.mkv"

		// Use full CPU for encoding since sample encoding is sequential
		numThreads := vmaf.GetEncodingThreads()
		threadStr := fmt.Sprintf("%d", numThreads)
		args := make([]string, 0, len(inputArgs)+len(outputArgs)+8)
		args = append(args, "-threads", threadStr, "-filter_threads", threadStr)
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
