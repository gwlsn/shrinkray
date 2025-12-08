package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/graysonwilson/shrinkray/internal/browse"
	"github.com/graysonwilson/shrinkray/internal/config"
	"github.com/graysonwilson/shrinkray/internal/ffmpeg"
	"github.com/graysonwilson/shrinkray/internal/jobs"
)

// Handler provides HTTP API handlers
type Handler struct {
	browser    *browse.Browser
	queue      *jobs.Queue
	workerPool *jobs.WorkerPool
	cfg        *config.Config
}

// NewHandler creates a new API handler
func NewHandler(browser *browse.Browser, queue *jobs.Queue, workerPool *jobs.WorkerPool, cfg *config.Config) *Handler {
	return &Handler{
		browser:    browser,
		queue:      queue,
		workerPool: workerPool,
		cfg:        cfg,
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

// EstimateRequest is the request body for the estimate endpoint
type EstimateRequest struct {
	Paths    []string `json:"paths"`
	PresetID string   `json:"preset_id"`
}

// EstimateResponse contains size and time estimates
type EstimateResponse struct {
	Files            []*FileEstimate  `json:"files"`
	Total            *ffmpeg.Estimate `json:"total"`
	PresetID         string           `json:"preset_id"`
	PresetName       string           `json:"preset_name"`
}

// FileEstimate contains estimate for a single file
type FileEstimate struct {
	Path     string           `json:"path"`
	Estimate *ffmpeg.Estimate `json:"estimate"`
	Warning  string           `json:"warning,omitempty"`
}

// Estimate handles POST /api/estimate
func (h *Handler) Estimate(w http.ResponseWriter, r *http.Request) {
	var req EstimateRequest
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

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Get all video files
	probes, err := h.browser.GetVideoFiles(ctx, req.Paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(probes) == 0 {
		writeError(w, http.StatusBadRequest, "no video files found in selection")
		return
	}

	// Build response
	resp := &EstimateResponse{
		Files:      make([]*FileEstimate, 0, len(probes)),
		PresetID:   preset.ID,
		PresetName: preset.Name,
	}

	for _, probe := range probes {
		est := ffmpeg.EstimateTranscode(probe, preset)
		resp.Files = append(resp.Files, &FileEstimate{
			Path:     probe.Path,
			Estimate: est,
			Warning:  est.Warning,
		})
	}

	resp.Total = ffmpeg.EstimateMultiple(probes, preset)

	writeJSON(w, http.StatusOK, resp)
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

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Get all video files
	probes, err := h.browser.GetVideoFiles(ctx, req.Paths)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(probes) == 0 {
		writeError(w, http.StatusBadRequest, "no video files found in selection")
		return
	}

	// Add jobs to queue
	createdJobs, err := h.queue.AddMultiple(probes, req.PresetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"jobs":    createdJobs,
		"count":   len(createdJobs),
		"message": fmt.Sprintf("Created %d jobs", len(createdJobs)),
	})
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

// ClearCompleted handles POST /api/jobs/clear
func (h *Handler) ClearCompleted(w http.ResponseWriter, r *http.Request) {
	count := h.queue.ClearCompleted()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cleared": count,
		"message": fmt.Sprintf("Cleared %d completed jobs", count),
	})
}

// GetConfig handles GET /api/config
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	// Return a sanitized config (no sensitive paths exposed)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"media_path":        h.cfg.MediaPath,
		"original_handling": h.cfg.OriginalHandling,
		"workers":           h.cfg.Workers,
		"has_temp_path":     h.cfg.TempPath != "",
	})
}

// UpdateConfigRequest is the request body for updating config
type UpdateConfigRequest struct {
	OriginalHandling string `json:"original_handling,omitempty"`
	Workers          int    `json:"workers,omitempty"`
}

// UpdateConfig handles PUT /api/config
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Only allow updating certain fields
	if req.OriginalHandling != "" {
		if req.OriginalHandling != "replace" && req.OriginalHandling != "keep" {
			writeError(w, http.StatusBadRequest, "original_handling must be 'replace' or 'keep'")
			return
		}
		h.cfg.OriginalHandling = req.OriginalHandling
	}

	if req.Workers > 0 {
		h.cfg.Workers = req.Workers
		// Note: changing workers doesn't restart the worker pool - would need app restart
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
