package provider

import (
	"fmt"
	"sync"
)

// Registry manages available providers.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", name)
	}
	return p, nil
}

// Default returns the first registered provider (or error if none).
func (r *Registry) Default() (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Prefer "adobe" as default
	if p, ok := r.providers["adobe"]; ok {
		return p, nil
	}
	for _, p := range r.providers {
		return p, nil
	}
	return nil, fmt.Errorf("no providers registered")
}

// List returns all registered provider names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// AllModels returns combined model lists from all providers.
func (r *Registry) AllModels() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var models []ModelInfo
	for _, p := range r.providers {
		models = append(models, p.SupportedModels()...)
	}
	return models
}

// AllVideoModels returns combined video model lists from all providers.
func (r *Registry) AllVideoModels() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var models []ModelInfo
	for _, p := range r.providers {
		models = append(models, p.SupportedVideoModels()...)
	}
	return models
}
