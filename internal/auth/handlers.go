package auth

import "net/http"

// LoginHandlerProvider allows providers to implement login handling.
type LoginHandlerProvider interface {
	HandleLogin(w http.ResponseWriter, r *http.Request) error
}

// CallbackHandler handles auth provider callbacks.
func CallbackHandler(provider Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			http.NotFound(w, r)
			return
		}
		if err := provider.HandleCallback(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
	}
}

// LoginHandler handles auth login requests.
func LoginHandler(provider Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		loginProvider, ok := provider.(LoginHandlerProvider)
		if !ok || provider == nil {
			http.NotFound(w, r)
			return
		}
		if err := loginProvider.HandleLogin(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
		}
	}
}
