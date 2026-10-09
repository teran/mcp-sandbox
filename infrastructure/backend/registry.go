// Package backend provides the constructor-register for sandbox backends and
// the capability-based selection logic (SPEC §6.3). Concrete backends register
// themselves through init(); availability is evaluated lazily, never at
// startup (A02/N35).
package backend

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
)

// ErrNoAvailableBackend is returned by Select when no registered backend both
// reports itself available and covers the required capability set.
var ErrNoAvailableBackend = fmt.Errorf("backend: no available backend covers the required capabilities")

// Config carries the server-level settings a backend needs at construction.
// It is assembled at startup from the application config and passed to every
// Factory.New.
type Config struct {
	// DockerHost is the unix socket used by the docker backend
	// (default unix:///var/run/docker.sock).
	DockerHost string
	// AllowDirs are the only host directories reachable by the sandbox
	// (S04), already canonicalized with EvalSymlinks by config.Validate.
	AllowDirs []string
	// AllowSymlinks controls whether host path symlinks are followed (S04).
	AllowSymlinks bool
}

// Factory describes how to construct a backend. Backends are registered by
// name; a duplicate name is rejected at registration.
type Factory struct {
	// Name uniquely identifies the backend (e.g. "docker").
	Name string
	// Priority orders backend selection: lower values are preferred.
	Priority int
	// Capabilities advertises which role interfaces the backend implements.
	Capabilities role.CapabilitySet
	// Available reports whether the backend is usable right now. It is
	// evaluated lazily during Select, never at startup (A02/N35).
	Available func() bool
	// New constructs the backend for the given server config.
	New func(cfg Config) (role.Backend, error)
}

// Registry is a thread-safe, constructor-register map of backend factories.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// NewRegistry returns an empty backend registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register adds a factory to the registry. It returns an error if a factory
// with the same name is already registered.
func (r *Registry) Register(f Factory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f.Name == "" {
		return fmt.Errorf("backend: factory name is required")
	}
	if _, ok := r.factories[f.Name]; ok {
		return fmt.Errorf("backend: factory %q already registered", f.Name)
	}
	r.factories[f.Name] = f
	return nil
}

// Get returns the factory registered under the given name.
func (r *Registry) Get(name string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.factories[name]
	return f, ok
}

// List returns every registered factory in an unspecified order.
func (r *Registry) List() []Factory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Factory, 0, len(r.factories))
	for _, f := range r.factories {
		out = append(out, f)
	}
	return out
}

// Select chooses the best factory that is currently available and covers the
// required capabilities. It filters by Available() and capability coverage,
// then orders by Priority (lower preferred) with the name as a deterministic
// tiebreaker. Availability is evaluated lazily here, on the first call that
// needs a backend (A02/N35).
func (r *Registry) Select(_ context.Context, need role.CapabilitySet) (Factory, error) {
	candidates := make([]Factory, 0)
	for _, f := range r.List() {
		if !covers(f.Capabilities, need) {
			continue
		}
		if f.Available != nil && !f.Available() {
			continue
		}
		candidates = append(candidates, f)
	}
	if len(candidates) == 0 {
		return Factory{}, ErrNoAvailableBackend
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].Name < candidates[j].Name
	})
	return candidates[0], nil
}

// covers reports whether the factory's capability set satisfies every
// capability required by need.
func covers(factory, need role.CapabilitySet) bool {
	return (!need.Lifecycle || factory.Lifecycle) &&
		(!need.Executable || factory.Executable) &&
		(!need.Introspectable || factory.Introspectable)
}

// Default is the process-wide backend registry. Concrete backends register
// themselves into it via init() (SPEC §6.3), and the tool layer selects from
// it lazily on the first call that needs a backend (A02/N35).
var Default = NewRegistry()

// Register adds a factory to the Default registry.
func Register(f Factory) error { return Default.Register(f) }

// Select picks the best available factory covering the required capabilities
// from the Default registry.
func Select(ctx context.Context, need role.CapabilitySet) (Factory, error) {
	return Default.Select(ctx, need)
}
