package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
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

// BrowseStream handles GET /api/browse/stream (SSE for directory count updates)
func (h *Handler) BrowseStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	browsePath := r.URL.Query().Get("path")
	if browsePath == "" {
		browsePath = h.cfg.MediaPath
	}

	ch := h.browser.Subscribe()
	defer h.browser.Unsubscribe(ch)

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Periodic keepalive to prevent proxy/load balancer timeouts.
	// Without this, reverse proxies (nginx, Cloudflare) may close the
	// connection after 60-120 seconds of silence.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}

			// Filter: only send events relevant to the current browse path.
			// Relevant means: the event path is an immediate child of browsePath,
			// or the event path IS browsePath (for header totals).
			if !isRelevantEvent(browsePath, event.Path) {
				continue
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

// isRelevantEvent checks if an event path is relevant to the current browse path.
// An event is relevant if:
//   - it IS the browse path (totals update for the current directory)
//   - it is an immediate child directory of the browse path (row update)
func isRelevantEvent(browsePath, eventPath string) bool {
	if eventPath == browsePath {
		return true
	}
	// Check if eventPath is under browsePath
	if !strings.HasPrefix(eventPath, browsePath+string(os.PathSeparator)) {
		return false
	}
	// Immediate child: no more separators after browsePath prefix
	rel := eventPath[len(browsePath)+1:]
	return !strings.Contains(rel, string(os.PathSeparator))
}
