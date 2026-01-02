package auth

import "net/http"

// User represents an authenticated user.
type User struct {
	ID    string
	Email string
	Name  string
}

// Provider authenticates incoming requests and manages login callbacks.
type Provider interface {
	Authenticate(r *http.Request) (*User, error)
	LoginURL(r *http.Request) (string, error)
	HandleCallback(w http.ResponseWriter, r *http.Request) error
}
