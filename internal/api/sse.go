package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// JobStream handles GET /api/jobs/stream (SSE endpoint)
func (h *Handler) JobStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Encoding", "identity")

	// Get flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe to job events
	eventCh := h.queue.Subscribe()
	defer h.queue.Unsubscribe(eventCh)

	// Send initial state
	initialJobs := h.queue.GetAll()
	initialData, _ := json.Marshal(map[string]interface{}{
		"type":  "init",
		"jobs":  initialJobs,
		"stats": h.queue.Stats(),
	})
	fmt.Fprintf(w, "data: %s\n\n", initialData)
	flusher.Flush()

	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case event, ok := <-eventCh:
			if !ok {
				return
			}

			data, err := json.Marshal(event)
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

			// Check if we should send a Pushover notification
			// This happens when a job completes/fails/cancels and the queue is empty
			if event.Type == "complete" || event.Type == "failed" || event.Type == "cancelled" {
				h.checkAndSendNotification(w, flusher)
			}
		}
	}
}

// checkAndSendNotification checks if all jobs are done and sends a Pushover notification if enabled
func (h *Handler) checkAndSendNotification(w http.ResponseWriter, flusher http.Flusher) {
	// Lock to prevent multiple concurrent notifications when multiple jobs finish simultaneously
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()

	// Check if notification is enabled and at least one provider is configured
	if !h.cfg.NotifyOnComplete || (!h.pushover.IsConfigured() && !h.ntfy.IsConfigured()) {
		return
	}

	// Check if queue is empty (no pending or running jobs)
	stats := h.queue.Stats()
	if stats.Pending > 0 || stats.Running > 0 {
		return
	}

	// Queue is empty, send notification
	message := fmt.Sprintf("%d jobs complete, %d failed\nSaved %s",
		stats.Complete, stats.Failed, formatBytes(stats.TotalSaved))

	allSent := true
	if h.pushover.IsConfigured() {
		if err := h.pushover.Send("Shrinkray Complete", message); err != nil {
			// Log error but don't crash - leave checkbox checked for retry
			fmt.Printf("Failed to send Pushover notification: %v\n", err)
			allSent = false
		}
	}
	if h.ntfy.IsConfigured() {
		if err := h.ntfy.Send("Shrinkray Complete", message); err != nil {
			// Log error but don't crash - leave checkbox checked for retry
			fmt.Printf("Failed to send ntfy notification: %v\n", err)
			allSent = false
		}
	}

	if !allSent {
		return
	}

	// Notification sent successfully, disable the checkbox
	h.cfg.NotifyOnComplete = false
	if h.cfgPath != "" {
		h.cfg.Save(h.cfgPath)
	}

	// Broadcast notify_sent event so UI can update the checkbox
	notifyData, _ := json.Marshal(map[string]interface{}{
		"type": "notify_sent",
	})
	fmt.Fprintf(w, "data: %s\n\n", notifyData)
	flusher.Flush()
}

// formatBytes formats bytes into a human-readable string
func formatBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
