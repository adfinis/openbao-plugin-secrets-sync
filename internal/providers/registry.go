package providers

import "fmt"

// Registry resolves provider implementations by stable provider type.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds a provider registry and rejects ambiguous provider types.
func NewRegistry(providerList ...Provider) (*Registry, error) {
	registry := &Registry{
		providers: make(map[string]Provider, len(providerList)),
	}
	for _, provider := range providerList {
		if provider == nil {
			return nil, fmt.Errorf("provider registry contains nil provider")
		}
		providerType := provider.Type()
		if providerType == "" {
			return nil, fmt.Errorf("provider registry contains provider with empty type")
		}
		if _, exists := registry.providers[providerType]; exists {
			return nil, fmt.Errorf("provider type %q registered more than once", providerType)
		}
		registry.providers[providerType] = provider
	}
	return registry, nil
}

// MustNewRegistry builds a registry for static provider sets.
func MustNewRegistry(providerList ...Provider) *Registry {
	registry, err := NewRegistry(providerList...)
	if err != nil {
		panic(err)
	}
	return registry
}

// Get returns a provider implementation by type.
func (r *Registry) Get(providerType string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[providerType]
	return provider, ok
}

// MustGet returns a provider implementation or a stable unsupported-provider error.
func (r *Registry) MustGet(providerType string) (Provider, error) {
	provider, ok := r.Get(providerType)
	if !ok {
		return nil, fmt.Errorf("unsupported destination type %q", providerType)
	}
	return provider, nil
}
