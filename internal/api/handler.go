package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	shrinkray "github.com/gwlsn/shrinkray"
	"github.com/gwlsn/shrinkray/internal/browse"
	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/jobs"
	"github.com/gwlsn/shrinkray/internal/logger"
	"github.com/gwlsn/shrinkray/internal/pushover"
)

// StatsStore defines the interface for stats-related store operations.
type StatsStore interface {
	ResetSession() error
}

// Handler provides HTTP API handlers
type Handler struct {
	browser    *browse.Browser
	queue      *jobs.Queue
	workerPool *jobs.WorkerPool
	cfg        *config.Config
	cfgPath    string
	pushover   *pushover.Client
	notifyMu   sync.Mutex // Protects notification sending to prevent duplicates
	store      StatsStore // For stats operations (may be nil)
}

// NewHandler creates a new API handler
func NewHandler(browser *browse.Browser, queue *jobs.Queue, workerPool *jobs.WorkerPool, cfg *config.Config, cfgPath string) *Handler {
	return &Handler{
		browser:    browser,
		queue:      queue,
		workerPool: workerPool,
		cfg:        cfg,
		cfgPath:    cfgPath,
		pushover:   pushover.NewClient(cfg.PushoverUserKey, cfg.PushoverAppToken),
	}
}

// SetStore sets the stats store for session/lifetime stats operations.
func (h *Handler) SetStore(store StatsStore) {
	h.store = store
}

// response helpers

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// Validation helpers for config updates

// validateQuality validates a quality/CRF value for the given codec.
// Returns an error message if invalid, empty string if valid.
func validateQuality(value int, codec string) string {
	var min, max int
	switch codec {
	case "hevc":
		min, max = 15, 40
	case "av1":
		min, max = 20, 50
	default:
		return fmt.Sprintf("unknown codec: %s", codec)
	}
	if value < min || value > max {
		return fmt.Sprintf("quality_%s must be between %d and %d", codec, min, max)
	}
	return ""
}

// validateScheduleHour validates a schedule hour value (0-23).
// Returns an error message if invalid, empty string if valid.
func validateScheduleHour(value int, field string) string {
	if value < 0 || value > 23 {
		return fmt.Sprintf("%s must be between 0 and 23", field)
	}
	return ""
}

// Browse handles GET /api/browse?path=...
func (h *Handler) Browse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = h.cfg.MediaPath
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := h.browser.Browse(ctx, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// Presets handles GET /api/presets
func (h *Handler) Presets(w http.ResponseWriter, r *http.Request) {
	presets := ffmpeg.ListPresets()
	writeJSON(w, http.StatusOK, presets)
}

// Encoders handles GET /api/encoders
func (h *Handler) Encoders(w http.ResponseWriter, r *http.Request) {
	encoders := ffmpeg.ListAvailableEncoders()
	best := ffmpeg.GetBestEncoder()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"encoders": encoders,
		"best":     best,
	})
}

// CreateJobsRequest is the request body for creating jobs
type CreateJobsRequest struct {
	Paths    []string `json:"paths"`
	PresetID string   `json:"preset_id"`
}

// CreateJobs handles POST /api/jobs
// Responds immediately and processes files in background to avoid UI freeze
func (h *Handler) CreateJobs(w http.ResponseWriter, r *http.Request) {
	var req CreateJobsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, "no paths provided")
		return
	}

	preset := ffmpeg.GetPreset(req.PresetID)
	if preset == nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown preset: %s", req.PresetID))
		return
	}

	// Respond immediately - jobs will be added in background and appear via SSE
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":  "processing",
		"message": fmt.Sprintf("Processing %d paths in background...", len(req.Paths)),
	})

	// Auto-unpause when adding new jobs (prevents accidental blocking)
	h.workerPool.Unpause()

	// Process in background goroutine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Progress callback broadcasts SSE events (throttled to max 10/sec)
		var lastBroadcast time.Time
		onProgress := func(probed, total int) {
			now := time.Now()
			// Throttle broadcasts, but always send first (0/N) and last (N/N)
			if probed > 0 && probed < total && now.Sub(lastBroadcast) < 100*time.Millisecond {
				return
			}
			lastBroadcast = now
			h.queue.BroadcastProgress(probed, total)
		}

		// Get all video files with progress reporting
		probes, err := h.browser.GetVideoFilesWithProgress(ctx, req.Paths, onProgress)
		if err != nil {
			logger.Error("Error getting video files", "error", err)
			return
		}

		if len(probes) == 0 {
			return
		}

		// Add jobs to queue - SSE will notify frontend of new jobs
		_, _ = h.queue.AddMultiple(probes, req.PresetID)
	}()
}

// ListJobs handles GET /api/jobs
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	allJobs := h.queue.GetAll()
	stats := h.queue.Stats()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"jobs":  allJobs,
		"stats": stats,
	})
}

// GetJob handles GET /api/jobs/:id
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path - expects /api/jobs/{id}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// CancelJob handles DELETE /api/jobs/:id
func (h *Handler) CancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	// If job is running, cancel it via worker pool
	if job.Status == jobs.StatusRunning {
		h.workerPool.CancelJob(id)
	}

	// Cancel in queue
	if err := h.queue.CancelJob(id); err != nil {
		// Might already be cancelled/completed
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// ClearQueue handles POST /api/jobs/clear
// Optional query param: ?status=pending|complete|failed|skipped|cancelled
// If status is provided, only jobs matching that status are cleared.
// Running jobs are never cleared.
func (h *Handler) ClearQueue(w http.ResponseWriter, r *http.Request) {
	status := jobs.Status(r.URL.Query().Get("status"))
	count := h.queue.Clear(status)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cleared": count,
		"message": fmt.Sprintf("Cleared %d jobs", count),
	})
}

// PauseQueue handles POST /api/queue/pause
// Stops all running jobs and prevents new jobs from starting
func (h *Handler) PauseQueue(w http.ResponseWriter, r *http.Request) {
	count := h.workerPool.Pause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"paused":   true,
		"requeued": count,
	})
}

// ResumeQueue handles POST /api/queue/resume
// Allows workers to pick up jobs again
func (h *Handler) ResumeQueue(w http.ResponseWriter, r *http.Request) {
	h.workerPool.Unpause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"paused": false,
	})
}

// GetConfig handles GET /api/config
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	// Get encoder-specific defaults
	bestEncoder := ffmpeg.GetBestEncoder()
	defaultHEVC, defaultAV1 := ffmpeg.GetEncoderDefaults(bestEncoder.Accel)
	// Fall back to software defaults for bitrate-based encoders (VideoToolbox)
	if defaultHEVC == 0 {
		defaultHEVC = 26
	}
	if defaultAV1 == 0 {
		defaultAV1 = 35
	}

	// Return a sanitized config (no sensitive paths exposed)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":               shrinkray.Version,
		"media_path":            h.cfg.MediaPath,
		"original_handling":     h.cfg.OriginalHandling,
		"workers":               h.cfg.Workers,
		"has_temp_path":         h.cfg.TempPath != "",
		"pushover_user_key":     h.cfg.PushoverUserKey,
		"pushover_app_token":    h.cfg.PushoverAppToken,
		"pushover_configured":   h.pushover.IsConfigured(),
		"notify_on_complete":    h.cfg.NotifyOnComplete,
		"quality_hevc":          h.cfg.QualityHEVC,
		"quality_av1":           h.cfg.QualityAV1,
		"default_quality_hevc":  defaultHEVC,
		"default_quality_av1":   defaultAV1,
		"schedule_enabled":      h.cfg.ScheduleEnabled,
		"schedule_start_hour":   h.cfg.ScheduleStartHour,
		"schedule_end_hour":     h.cfg.ScheduleEndHour,
		"output_format":         h.cfg.OutputFormat,
		"tonemap_hdr":           h.cfg.TonemapHDR,
		"tonemap_algorithm":     h.cfg.TonemapAlgorithm,
	})
}

// UpdateConfigRequest is the request body for updating config
type UpdateConfigRequest struct {
	OriginalHandling  *string `json:"original_handling,omitempty"`
	Workers           *int    `json:"workers,omitempty"`
	PushoverUserKey   *string `json:"pushover_user_key,omitempty"`
	PushoverAppToken  *string `json:"pushover_app_token,omitempty"`
	NotifyOnComplete  *bool   `json:"notify_on_complete,omitempty"`
	QualityHEVC       *int    `json:"quality_hevc,omitempty"`
	QualityAV1        *int    `json:"quality_av1,omitempty"`
	ScheduleEnabled   *bool   `json:"schedule_enabled,omitempty"`
	ScheduleStartHour *int    `json:"schedule_start_hour,omitempty"`
	ScheduleEndHour   *int    `json:"schedule_end_hour,omitempty"`
	OutputFormat      *string `json:"output_format,omitempty"`
	TonemapHDR        *bool   `json:"tonemap_hdr,omitempty"`
	TonemapAlgorithm  *string `json:"tonemap_algorithm,omitempty"`
}

// UpdateConfig handles PUT /api/config
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Only allow updating certain fields
	if req.OriginalHandling != nil {
		if *req.OriginalHandling != "replace" && *req.OriginalHandling != "keep" {
			writeError(w, http.StatusBadRequest, "original_handling must be 'replace' or 'keep'")
			return
		}
		h.cfg.OriginalHandling = *req.OriginalHandling
	}

	if req.Workers != nil && *req.Workers > 0 {
		workers := *req.Workers
		if workers > 6 {
			workers = 6 // Cap at 6 workers
		}
		// Dynamically resize the worker pool
		h.workerPool.Resize(workers)
	}

	// Handle Pushover settings
	if req.PushoverUserKey != nil {
		h.cfg.PushoverUserKey = *req.PushoverUserKey
		h.pushover.UserKey = *req.PushoverUserKey
	}
	if req.PushoverAppToken != nil {
		h.cfg.PushoverAppToken = *req.PushoverAppToken
		h.pushover.AppToken = *req.PushoverAppToken
	}
	if req.NotifyOnComplete != nil {
		h.cfg.NotifyOnComplete = *req.NotifyOnComplete
	}

	// Handle quality settings
	if req.QualityHEVC != nil {
		if errMsg := validateQuality(*req.QualityHEVC, "hevc"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.QualityHEVC = *req.QualityHEVC
	}
	if req.QualityAV1 != nil {
		if errMsg := validateQuality(*req.QualityAV1, "av1"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.QualityAV1 = *req.QualityAV1
	}

	// Handle schedule settings
	if req.ScheduleEnabled != nil {
		h.cfg.ScheduleEnabled = *req.ScheduleEnabled
	}
	if req.ScheduleStartHour != nil {
		if errMsg := validateScheduleHour(*req.ScheduleStartHour, "schedule_start_hour"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.ScheduleStartHour = *req.ScheduleStartHour
	}
	if req.ScheduleEndHour != nil {
		if errMsg := validateScheduleHour(*req.ScheduleEndHour, "schedule_end_hour"); errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		h.cfg.ScheduleEndHour = *req.ScheduleEndHour
	}

	// Handle output format
	if req.OutputFormat != nil {
		if *req.OutputFormat != "mkv" && *req.OutputFormat != "mp4" {
			writeError(w, http.StatusBadRequest, "output_format must be 'mkv' or 'mp4'")
			return
		}
		h.cfg.OutputFormat = *req.OutputFormat
	}

	// Handle HDR tonemapping settings
	if req.TonemapHDR != nil {
		h.cfg.TonemapHDR = *req.TonemapHDR
	}
	if req.TonemapAlgorithm != nil {
		// Validate algorithm
		switch *req.TonemapAlgorithm {
		case "hable", "bt2390", "reinhard", "mobius", "clip", "linear", "gamma":
			h.cfg.TonemapAlgorithm = *req.TonemapAlgorithm
		default:
			writeError(w, http.StatusBadRequest, "tonemap_algorithm must be one of: hable, bt2390, reinhard, mobius, clip, linear, gamma")
			return
		}
	}

	// Persist config to disk
	if h.cfgPath != "" {
		if err := h.cfg.Save(h.cfgPath); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// Stats handles GET /api/stats
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	stats := h.queue.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// ResetSession handles POST /api/stats/reset-session
func (h *Handler) ResetSession(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusInternalServerError, "stats store not configured")
		return
	}

	if err := h.store.ResetSession(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "session reset"})
}

// ClearCache handles POST /api/cache/clear
func (h *Handler) ClearCache(w http.ResponseWriter, r *http.Request) {
	h.browser.ClearCache()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cache cleared"})
}

// TestPushover handles POST /api/pushover/test
func (h *Handler) TestPushover(w http.ResponseWriter, r *http.Request) {
	if !h.pushover.IsConfigured() {
		writeError(w, http.StatusBadRequest, "Pushover credentials not configured")
		return
	}

	if err := h.pushover.Test(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "Test notification sent"})
}

// RetryJob handles POST /api/jobs/:id/retry
func (h *Handler) RetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "job ID required")
		return
	}

	job := h.queue.Get(id)
	if job == nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if job.Status != jobs.StatusFailed {
		writeError(w, http.StatusBadRequest, "can only retry failed jobs")
		return
	}

	// Re-probe the file and create a new job
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	probe, err := h.browser.ProbeFile(ctx, job.InputPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to probe file: %v", err))
		return
	}

	// Add new job with same preset
	newJob, err := h.queue.Add(job.InputPath, job.PresetID, probe)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create job: %v", err))
		return
	}

	// Remove the failed job
	h.queue.Remove(id)

	writeJSON(w, http.StatusOK, newJob)
}
