package api

import (
	"embed"
	"io/fs"
	"net/http"
)

// NewRouter creates a new HTTP router with all API endpoints
// debugMode determines which UI to serve (true = debug UI, false = production UI)
func NewRouter(h *Handler, staticFS embed.FS, debugMode bool) *http.ServeMux {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/browse", h.Browse)
	mux.HandleFunc("GET /api/presets", h.Presets)
	mux.HandleFunc("GET /api/encoders", h.Encoders)

	mux.HandleFunc("GET /api/jobs", h.ListJobs)
	mux.HandleFunc("POST /api/jobs", h.CreateJobs)
	mux.HandleFunc("GET /api/jobs/stream", h.JobStream)
	mux.HandleFunc("POST /api/jobs/clear", h.ClearQueue)
	mux.HandleFunc("GET /api/jobs/{id}", h.GetJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", h.CancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", h.RetryJob)
	mux.HandleFunc("POST /api/processed/clear", h.ClearProcessedHistory)

	mux.HandleFunc("GET /api/config", h.GetConfig)
	mux.HandleFunc("PUT /api/config", h.UpdateConfig)

	mux.HandleFunc("GET /api/stats", h.Stats)
	mux.HandleFunc("POST /api/cache/clear", h.ClearCache)
	mux.HandleFunc("POST /api/pushover/test", h.TestPushover)
	mux.HandleFunc("POST /api/ntfy/test", h.TestNtfy)

	// Determine which UI to serve
	uiPath := "web/templates"
	if debugMode {
		uiPath = "web/debug"
	}

	// Serve static files from appropriate directory
	staticSubFS, err := fs.Sub(staticFS, uiPath)
	if err != nil {
		// Fall back to empty handler if no static files
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Shrinkray API - No UI available"))
		})
	} else {
		// Serve index.html at root
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			content, err := fs.ReadFile(staticSubFS, "index.html")
			if err != nil {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write(content)
		})

		// Serve logo
		mux.HandleFunc("GET /logo.png", func(w http.ResponseWriter, r *http.Request) {
			content, err := fs.ReadFile(staticSubFS, "logo.png")
			if err != nil {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(content)
		})

		// Serve favicon
		mux.HandleFunc("GET /favicon.png", func(w http.ResponseWriter, r *http.Request) {
			content, err := fs.ReadFile(staticSubFS, "favicon.png")
			if err != nil {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(content)
		})
	}

	return mux
}

// NewRouterWithoutStatic creates a router without static file serving (for testing)
func NewRouterWithoutStatic(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/browse", h.Browse)
	mux.HandleFunc("GET /api/presets", h.Presets)
	mux.HandleFunc("GET /api/encoders", h.Encoders)

	mux.HandleFunc("GET /api/jobs", h.ListJobs)
	mux.HandleFunc("POST /api/jobs", h.CreateJobs)
	mux.HandleFunc("GET /api/jobs/stream", h.JobStream)
	mux.HandleFunc("POST /api/jobs/clear", h.ClearQueue)
	mux.HandleFunc("GET /api/jobs/{id}", h.GetJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", h.CancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", h.RetryJob)
	mux.HandleFunc("POST /api/processed/clear", h.ClearProcessedHistory)

	mux.HandleFunc("GET /api/config", h.GetConfig)
	mux.HandleFunc("PUT /api/config", h.UpdateConfig)

	mux.HandleFunc("GET /api/stats", h.Stats)
	mux.HandleFunc("POST /api/cache/clear", h.ClearCache)
	mux.HandleFunc("POST /api/pushover/test", h.TestPushover)
	mux.HandleFunc("POST /api/ntfy/test", h.TestNtfy)

	return mux
}
