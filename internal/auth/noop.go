package auth

import "net/http"

// NoopProvider authenticates every request without enforcing login.
type NoopProvider struct {
	user *User
}

// NewNoopProvider returns a provider that always authenticates.
func NewNoopProvider() *NoopProvider {
	return &NoopProvider{user: &User{ID: "noop", Name: "Shrinkray"}}
}

// Authenticate always succeeds.
func (p *NoopProvider) Authenticate(_ *http.Request) (*User, error) {
	return p.user, nil
}

// LoginURL returns the root path.
func (p *NoopProvider) LoginURL(_ *http.Request) (string, error) {
	return "/", nil
}

// HandleCallback is a no-op for the noop provider.
func (p *NoopProvider) HandleCallback(w http.ResponseWriter, _ *http.Request) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}
