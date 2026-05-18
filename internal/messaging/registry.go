package messaging

import (
	"fmt"
	"sort"
	"sync"

	"github.com/akmatori/akmatori/internal/database"
)

// Registry is the default ProviderRegistry implementation. It holds a Provider
// per database.MessagingProvider identifier and is safe for concurrent reads
// and writes — providers are typically registered once at startup but may be
// reloaded as part of integration CRUD flows.
type Registry struct {
	mu        sync.RWMutex
	providers map[database.MessagingProvider]Provider
}

// NewRegistry returns an empty registry. Callers register providers via
// Register before the registry is consulted by handlers.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[database.MessagingProvider]Provider),
	}
}

// Register adds p to the registry under its declared Name. Re-registering the
// same provider name replaces the existing entry — useful for hot-reloads
// (e.g. when Slack credentials change and a fresh slack-go client is built).
func (r *Registry) Register(p Provider) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Unregister removes the provider for name. No-op if absent.
func (r *Registry) Unregister(name database.MessagingProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
}

// Get returns the provider registered under name. Returns
// ErrProviderNotRegistered (wrapped with the requested name) when the
// provider is absent so the caller can degrade gracefully without panicking.
func (r *Registry) Get(name database.MessagingProvider) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotRegistered, name)
	}
	return p, nil
}

// List returns the set of registered provider names in sorted order so call
// sites (UI listings, debug logs) get a deterministic enumeration.
func (r *Registry) List() []database.MessagingProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]database.MessagingProvider, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
