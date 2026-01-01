package auth

import (
	"context"
	"net/http"
	"strings"
)

type contextKey struct{}

// Middleware enforces authentication for incoming requests.
type Middleware struct {
	Provider    Provider
	BypassPaths []string
}

// DefaultBypassPaths returns default unauthenticated endpoints.
func DefaultBypassPaths() []string {
	return []string{"/healthz", "/auth/callback", "/auth/login"}
}

// NewMiddleware creates an auth middleware.
func NewMiddleware(provider Provider, bypassPaths []string) *Middleware {
	return &Middleware{Provider: provider, BypassPaths: bypassPaths}
}

// Wrap wraps an HTTP handler with auth enforcement.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	if m == nil || m.Provider == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.shouldBypass(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		user, err := m.Provider.Authenticate(r)
		if err == nil && user != nil {
			ctx := context.WithValue(r.Context(), contextKey{}, user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if isAPIRequest(r.URL.Path) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		loginURL, err := m.Provider.LoginURL(r)
		if err != nil || loginURL == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		http.Redirect(w, r, loginURL, http.StatusFound)
	})
}

// UserFromContext returns the authenticated user if present.
func UserFromContext(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(contextKey{}).(*User)
	return user, ok
}

func (m *Middleware) shouldBypass(path string) bool {
	for _, bypass := range m.BypassPaths {
		if bypass == path {
			return true
		}
		if strings.HasSuffix(bypass, "*") {
			prefix := strings.TrimSuffix(bypass, "*")
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
	}
	return false
}

func isAPIRequest(path string) bool {
	if path == "/api" {
		return true
	}
	return strings.HasPrefix(path, "/api/")
}
