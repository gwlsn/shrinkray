package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/graysonwilson/shrinkray/internal/jobs"
)

// JobStream handles GET /api/jobs/stream (SSE endpoint)
func (h *Handler) JobStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

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

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return
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
		}
	}
}

// SSEEvent represents a Server-Sent Event
type SSEEvent struct {
	Type string      `json:"type"`
	Job  *jobs.Job   `json:"job,omitempty"`
	Jobs []*jobs.Job `json:"jobs,omitempty"`
}
