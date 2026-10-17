// Package driver defines the driver-registry + opaque options model (SPEC
// §6.11 / §7.11). A `Driver` (a token from spec) is mapped to a `Builder` by a
// `Registry`; each builder turns an opaque `Options` blob plus the driver-
// agnostic profile fields and the immutable `Policy` into a `spec.SandboxSpec`.
//
// The registry is fail-closed: an unknown driver fails at USE time with
// ErrUnknownDriver, and a duplicate/empty registration is rejected with
// ErrDuplicateDriver. The reserved built-in drivers (`docker`, `seatbelt`) can
// never be shadowed — the duplicate check makes a re-registration impossible.
//
// The package depends only on the leaf `spec` package (for Context's Cwd/
// Params/Isolation and the returned SandboxSpec), so it can be imported by the
// profile package (for `Options`) without creating an import cycle.
package driver

import (
	"errors"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// Sentinel errors returned by the driver registry (fail-closed, SPEC §6.3).
var (
	// ErrUnknownDriver reports that Get was asked for a driver that is not
	// registered. This is the fail-at-USE-time error for an unknown driver.
	ErrUnknownDriver = errors.New("driver: unknown driver")
	// ErrDuplicateDriver reports that Register was given a driver that is
	// already registered, or an empty driver name. It also prevents shadowing
	// the reserved built-in drivers.
	ErrDuplicateDriver = errors.New("driver: duplicate driver")
)

// Options is an opaque, driver-owned options blob wrapped around a *yaml.Node.
// The concrete builder decodes it into its own typed struct; the registry and
// profile never interpret its content (SPEC §7.11).
type Options struct {
	node *yaml.Node
}

// NewOptions wraps n in an Options. A nil node is stored as nil (Node() returns
// nil), so callers can represent "no options".
func NewOptions(n *yaml.Node) Options {
	return Options{node: n}
}

// Node returns the wrapped yaml.Node, or nil if none was set.
func (o Options) Node() *yaml.Node {
	return o.node
}

// Context is the driver-agnostic input a Builder consumes. It carries the
// operator-authored profile fields (Name, Description, Cwd, Shell), the opaque
// Options, the immutable Isolation Policy (SPEC §7.1) and the per-request
// Params. Hardening fields in the built spec must come from Policy, never from
// the options.
type Context struct {
	Name        string
	Description string
	Cwd         spec.CwdConfig
	Shell       string
	Options     Options
	Policy      spec.Isolation
	Params      spec.Params
}

// Builder turns a Context into a spec.SandboxSpec. It is the seam an external
// driver implements (SPEC §6.11); the reserved built-ins are BuildFuncs.
type Builder interface {
	Build(ctx Context) (spec.SandboxSpec, error)
}

// BuildFunc adapts a plain function to the Builder interface.
type BuildFunc func(ctx Context) (spec.SandboxSpec, error)

// Build invokes the wrapped function.
func (f BuildFunc) Build(ctx Context) (spec.SandboxSpec, error) {
	return f(ctx)
}

// Registry maps a spec.Driver to its Builder. It is concurrency-safe (X05): a
// process-wide Default registry is seeded by the built-in builders' init() and
// read concurrently by every tool call.
type Registry struct {
	mu       sync.RWMutex
	builders map[spec.Driver]Builder
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{builders: make(map[spec.Driver]Builder)}
}

// Register files b under d. It fail-closes on an empty driver name and on a
// duplicate (already-registered) driver — which also guarantees the reserved
// built-in drivers can never be shadowed. The first registration of a name on a
// fresh registry is always allowed.
func (r *Registry) Register(d spec.Driver, b Builder) error {
	if d == "" {
		return ErrDuplicateDriver
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.builders[d]; ok {
		return ErrDuplicateDriver
	}
	r.builders[d] = b

	return nil
}

// Get returns the Builder for d, or ErrUnknownDriver if d is not registered
// (fail-closed at USE time, SPEC §6.3). A Builder error is never swallowed —
// it is propagated verbatim by the caller of Build.
func (r *Registry) Get(d spec.Driver) (Builder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.builders[d]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, d)
	}

	return b, nil
}

// List returns every registered driver name. Order is unspecified; each driver
// appears exactly once.
func (r *Registry) List() []spec.Driver {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]spec.Driver, 0, len(r.builders))
	for d := range r.builders {
		out = append(out, d)
	}

	return out
}

// Default is the process-wide registry. It is seeded by the reserved built-in
// builders (infrastructure/backend/docker and darwin) at startup and, in the
// application tests, by the test-double builders.
var Default = New()

// Register files a builder into the Default registry.
func Register(d spec.Driver, b Builder) error {
	return Default.Register(d, b)
}
