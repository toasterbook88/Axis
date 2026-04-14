package llmrouter

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Registry holds the set of known inference providers and supports health
// checking. It is safe for concurrent use.
//
// Providers are keyed by their Name() value. Registering two providers with
// the same name is an error. The registry never contacts any endpoint itself —
// that is delegated to individual Provider implementations.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds p to the registry. It returns an error if a provider with the
// same name is already registered. Name matching is case-sensitive.
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return fmt.Errorf("registry: cannot register nil provider")
	}
	name := p.Name()
	if name == "" {
		return fmt.Errorf("registry: provider name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("registry: provider %q already registered", name)
	}
	r.providers[name] = p
	return nil
}

// MustRegister calls Register and panics on error.
// Intended for use during program initialization where duplicate registration
// is a programming error.
func (r *Registry) MustRegister(p Provider) {
	if err := r.Register(p); err != nil {
		panic(err)
	}
}

// Lookup returns the provider registered under name, or (nil, false) if no
// such provider exists.
func (r *Registry) Lookup(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// List returns all registered providers sorted by provider name.
// The returned slice is a snapshot; mutations to the registry after the call
// do not affect it.
func (r *Registry) List() []Provider {
	return r.snapshotProviders("")
}

// ListByType returns all providers of the given type sorted by provider name.
// The returned slice is a snapshot.
func (r *Registry) ListByType(t ProviderType) []Provider {
	return r.snapshotProviders(t)
}

func (r *Registry) snapshotProviders(providerType ProviderType) []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Provider, 0, len(names))
	for _, name := range names {
		provider := r.providers[name]
		if providerType != "" && provider.Type() != providerType {
			continue
		}
		out = append(out, provider)
	}
	return out
}

// Len returns the number of registered providers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

// CheckHealth probes every registered provider concurrently and returns a map
// of provider name → HealthStatus. The ctx deadline applies to each individual
// probe (not to all probes combined — they run in parallel).
//
// CheckHealth never returns an error; probe failures are captured inside the
// HealthStatus.Message field with OK=false.
func (r *Registry) CheckHealth(ctx context.Context) map[string]HealthStatus {
	r.mu.RLock()
	snapshot := make(map[string]Provider, len(r.providers))
	for name, p := range r.providers {
		snapshot[name] = p
	}
	r.mu.RUnlock()

	type result struct {
		name   string
		status HealthStatus
	}

	ch := make(chan result, len(snapshot))
	var wg sync.WaitGroup

	for name, p := range snapshot {
		wg.Add(1)
		go func(n string, prov Provider) {
			defer wg.Done()
			status, err := prov.Health(ctx)
			if err != nil {
				status = HealthStatus{OK: false, Message: err.Error()}
			}
			ch <- result{name: n, status: status}
		}(name, p)
	}

	wg.Wait()
	close(ch)

	out := make(map[string]HealthStatus, len(snapshot))
	for res := range ch {
		out[res.name] = res.status
	}
	return out
}

// Healthy returns providers that report OK health, probed concurrently.
// Providers that return an error or OK=false are silently excluded.
func (r *Registry) Healthy(ctx context.Context) []Provider {
	statuses := r.CheckHealth(ctx)

	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Provider
	for name, status := range statuses {
		if status.OK {
			if p, ok := r.providers[name]; ok {
				out = append(out, p)
			}
		}
	}
	return out
}
