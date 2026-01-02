package api

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gwlsn/shrinkray/internal/auth"
)

// NewRouter creates a new HTTP router with all API endpoints
// debugMode determines which UI to serve (true = debug UI, false = production UI)
func NewRouter(h *Handler, staticFS embed.FS, debugMode bool, authMiddleware *auth.Middleware) *http.ServeMux {
	mux := http.NewServeMux()

	provider := auth.Provider(nil)
	if authMiddleware != nil {
		provider = authMiddleware.Provider
	}

	wrap := func(handler http.Handler) http.Handler {
		if authMiddleware == nil {
			return handler
		}
		return authMiddleware.Wrap(handler)
	}

	// Health check
	mux.Handle("GET /healthz", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})))

	// Auth callbacks
	mux.Handle("GET /auth/callback", wrap(auth.CallbackHandler(provider)))
	mux.Handle("GET /auth/login", wrap(auth.LoginHandler(provider)))
	mux.Handle("POST /auth/login", wrap(auth.LoginHandler(provider)))

	// API routes
	mux.Handle("GET /api/browse", wrap(http.HandlerFunc(h.Browse)))
	mux.Handle("GET /api/presets", wrap(http.HandlerFunc(h.Presets)))
	mux.Handle("GET /api/encoders", wrap(http.HandlerFunc(h.Encoders)))

  mux.Handle("GET /api/jobs", wrap(http.HandlerFunc(h.ListJobs)))
  mux.Handle("POST /api/jobs", wrap(http.HandlerFunc(h.CreateJobs)))
  mux.Handle("GET /api/jobs/stream", wrap(http.HandlerFunc(h.JobStream)))
  mux.Handle("POST /api/jobs/clear", wrap(http.HandlerFunc(h.ClearQueue)))
  mux.Handle("GET /api/jobs/{id}", wrap(http.HandlerFunc(h.GetJob)))
  mux.Handle("DELETE /api/jobs/{id}", wrap(http.HandlerFunc(h.CancelJob)))
  mux.Handle("POST /api/jobs/{id}/retry", wrap(http.HandlerFunc(h.RetryJob)))
  mux.Handle("POST /api/jobs/{id}/reorder", wrap(http.HandlerFunc(h.ReorderJob)))
  mux.Handle("POST /api/jobs/{id}/move", wrap(http.HandlerFunc(h.MoveJob)))
  mux.Handle("POST /api/processed/clear", wrap(http.HandlerFunc(h.ClearProcessedHistory)))
  mux.Handle("POST /api/processed/mark", wrap(http.HandlerFunc(h.MarkProcessed)))
  
	mux.Handle("GET /api/config", wrap(http.HandlerFunc(h.GetConfig)))
	mux.Handle("PUT /api/config", wrap(http.HandlerFunc(h.UpdateConfig)))

	mux.Handle("GET /api/stats", wrap(http.HandlerFunc(h.Stats)))
	mux.Handle("POST /api/cache/clear", wrap(http.HandlerFunc(h.ClearCache)))
	mux.Handle("POST /api/pushover/test", wrap(http.HandlerFunc(h.TestPushover)))
	mux.Handle("POST /api/ntfy/test", wrap(http.HandlerFunc(h.TestNtfy)))


	// Determine which UI to serve
	uiPath := "web/templates"
	if debugMode {
		uiPath = "web/debug"
	}

	// Serve static files from appropriate directory
	staticSubFS, err := fs.Sub(staticFS, uiPath)
	if err != nil {
		// Fall back to empty handler if no static files
		mux.Handle("GET /", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Shrinkray API - No UI available"))
		})))
	} else {
		// Serve index.html at root
		mux.Handle("GET /", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		})))

		// Serve logo
		mux.Handle("GET /logo.png", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content, err := fs.ReadFile(staticSubFS, "logo.png")
			if err != nil {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(content)
		})))

		// Serve favicon
		mux.Handle("GET /favicon.png", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content, err := fs.ReadFile(staticSubFS, "favicon.png")
			if err != nil {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(content)
		})))
	}

	return mux
}

// NewRouterWithoutStatic creates a router without static file serving (for testing)
func NewRouterWithoutStatic(h *Handler, authMiddleware *auth.Middleware) *http.ServeMux {
	mux := http.NewServeMux()

	provider := auth.Provider(nil)
	if authMiddleware != nil {
		provider = authMiddleware.Provider
	}

	wrap := func(handler http.Handler) http.Handler {
		if authMiddleware == nil {
			return handler
		}
		return authMiddleware.Wrap(handler)
	}

	// Health check
	mux.Handle("GET /healthz", wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})))

	// Auth callbacks
	mux.Handle("GET /auth/callback", wrap(auth.CallbackHandler(provider)))
	mux.Handle("GET /auth/login", wrap(auth.LoginHandler(provider)))
	mux.Handle("POST /auth/login", wrap(auth.LoginHandler(provider)))

	// API routes
	mux.Handle("GET /api/browse", wrap(http.HandlerFunc(h.Browse)))
	mux.Handle("GET /api/presets", wrap(http.HandlerFunc(h.Presets)))
	mux.Handle("GET /api/encoders", wrap(http.HandlerFunc(h.Encoders)))

	mux.Handle("GET /api/jobs", wrap(http.HandlerFunc(h.ListJobs)))
	mux.Handle("POST /api/jobs", wrap(http.HandlerFunc(h.CreateJobs)))
	mux.Handle("GET /api/jobs/stream", wrap(http.HandlerFunc(h.JobStream)))
	mux.Handle("POST /api/jobs/clear", wrap(http.HandlerFunc(h.ClearQueue)))
	mux.Handle("GET /api/jobs/{id}", wrap(http.HandlerFunc(h.GetJob)))
	mux.Handle("DELETE /api/jobs/{id}", wrap(http.HandlerFunc(h.CancelJob)))
	mux.Handle("POST /api/jobs/{id}/retry", wrap(http.HandlerFunc(h.RetryJob)))
	mux.Handle("POST /api/jobs/{id}/reorder", wrap(http.HandlerFunc(h.ReorderJob)))
  mux.Handle("POST /api/jobs/{id}/move", wrap(http.HandlerFunc(h.MoveJob)))
	mux.Handle("POST /api/processed/clear", wrap(http.HandlerFunc(h.ClearProcessedHistory)))
	mux.Handle("POST /api/processed/mark", wrap(http.HandlerFunc(h.MarkProcessed)))

	mux.Handle("GET /api/config", wrap(http.HandlerFunc(h.GetConfig)))
	mux.Handle("PUT /api/config", wrap(http.HandlerFunc(h.UpdateConfig)))

	mux.Handle("GET /api/stats", wrap(http.HandlerFunc(h.Stats)))
	mux.Handle("POST /api/cache/clear", wrap(http.HandlerFunc(h.ClearCache)))
	mux.Handle("POST /api/pushover/test", wrap(http.HandlerFunc(h.TestPushover)))
	mux.Handle("POST /api/ntfy/test", wrap(http.HandlerFunc(h.TestNtfy)))

	return mux
}
