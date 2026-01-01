package auth

// Registry stores configured auth providers.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a registry for auth providers.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds a provider under a name.
func (r *Registry) Register(name string, provider Provider) {
	r.providers[name] = provider
}

// Provider returns the provider registered for name.
func (r *Registry) Provider(name string) (Provider, bool) {
	provider, ok := r.providers[name]
	return provider, ok
}
