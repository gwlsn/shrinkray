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
	"github.com/gwlsn/shrinkray/internal/pushover"
)

// Handler provides HTTP API handlers
type Handler struct {
	browser    *browse.Browser
	queue      *jobs.Queue
	workerPool *jobs.WorkerPool
	cfg        *config.Config
	cfgPath    string
	pushover   *pushover.Client
	notifyMu   sync.Mutex // Protects notification sending to prevent duplicates
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

// response helpers

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
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
	Paths             []string `json:"paths"`
	PresetID          string   `json:"preset_id"`
	IncludeSubfolders *bool    `json:"include_subfolders,omitempty"` // Default: true (for backwards compatibility)
	MaxDepth          *int     `json:"max_depth,omitempty"`          // nil = unlimited, 0 = current dir only, 1 = one level, etc.
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

	// Process in background goroutine
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// Build options for video file discovery
		// Default to recursive for backwards compatibility
		opts := browse.GetVideoFilesOptions{
			Recursive: true,
			MaxDepth:  req.MaxDepth,
		}
		if req.IncludeSubfolders != nil {
			opts.Recursive = *req.IncludeSubfolders
		}

		// Get all video files (this is the slow part - probing with ffprobe)
		probes, err := h.browser.GetVideoFilesWithOptions(ctx, req.Paths, opts)
		if err != nil {
			fmt.Printf("Error getting video files: %v\n", err)
			return
		}

		if len(probes) == 0 {
			return
		}

		// Add jobs to queue - SSE will notify frontend of new jobs
		h.queue.AddMultiple(probes, req.PresetID)
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
func (h *Handler) ClearQueue(w http.ResponseWriter, r *http.Request) {
	count := h.queue.Clear()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cleared": count,
		"message": fmt.Sprintf("Cleared %d jobs", count),
	})
}

// GetConfig handles GET /api/config
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
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
	})
}

// UpdateConfigRequest is the request body for updating config
type UpdateConfigRequest struct {
	OriginalHandling *string `json:"original_handling,omitempty"`
	Workers          *int    `json:"workers,omitempty"`
	PushoverUserKey  *string `json:"pushover_user_key,omitempty"`
	PushoverAppToken *string `json:"pushover_app_token,omitempty"`
	NotifyOnComplete *bool   `json:"notify_on_complete,omitempty"`
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

// GetPushover returns the Pushover client (for SSE handler)
func (h *Handler) GetPushover() *pushover.Client {
	return h.pushover
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
