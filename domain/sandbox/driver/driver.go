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
	"regexp"
	"strings"
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

// APIGroup is the DNS group that every profile `apiVersion` belongs to. The
// first DNS label of a profile's apiVersion names the driver that owns its
// `spec` (e.g. `docker.profiles.sandbox.teran.dev/v1` -> driver `docker`).
const APIGroup = "profiles.sandbox.teran.dev"

// APISuffix is the fully-qualified suffix a profile apiVersion must end with
// (`.profiles.sandbox.teran.dev/<version>`).
const APISuffix = ".profiles.sandbox.teran.dev"

// Sentinel errors returned by the apiVersion contract (fail-closed).
var (
	// ErrInvalidAPIVersion reports that an apiVersion string does not conform
	// to the `<driver>.profiles.sandbox.teran.dev/v<N>` shape (empty, no match,
	// or a `.`/`..` driver label).
	ErrInvalidAPIVersion = errors.New("driver: invalid apiVersion")
	// ErrAPIVersionMismatch reports that a profile's declared schema apiVersion
	// is not the version its driver builder declares (or the builder does not
	// declare one at all).
	ErrAPIVersionMismatch = errors.New("driver: apiVersion mismatch")
)

// apiVersionPattern matches the envelope apiVersion form:
// `<driver>.profiles.sandbox.teran.dev/v<N>`, where `<driver>` is the FIRST DNS
// label (the driver that owns `spec`) and `<N>` is the schema-version number
// (the LAST segment after the `/`). This is the profile SCHEMA version, not the
// driver implementation version.
var apiVersionPattern = regexp.MustCompile(`^([a-z0-9._-]+)\.profiles\.sandbox\.teran\.dev/(v[0-9]+)$`)

// ParseAPIVersion parses an envelope apiVersion into the owning driver (the
// first DNS label) and the schema version (the segment after the `/`). An empty,
// non-matching, or `.`/`..` driver label returns ErrInvalidAPIVersion.
func ParseAPIVersion(s string) (spec.Driver, string, error) {
	m := apiVersionPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", "", ErrInvalidAPIVersion
	}
	drv := m[1]
	if drv == "." || drv == ".." {
		return "", "", ErrInvalidAPIVersion
	}

	return spec.Driver(drv), m[2], nil
}

// Common option keys that the SERVER interprets from a driver's `spec` and
// strips before handing the remainder to the builder (SPEC §7.11). These are
// driver-agnostic and never appear in the builder-owned options.
const (
	CommonCwdKey   = "cwd"
	CommonShellKey = "shell"
)

// SplitCommon returns a NEW mapping node containing every key of node EXCEPT
// the server-interpreted common keys (cwd/shell). The input node is never
// mutated. A nil or non-mapping node is returned as-is.
func SplitCommon(node *yaml.Node) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return node
	}
	out := &yaml.Node{
		Kind:    yaml.MappingNode,
		Tag:     node.Tag,
		Value:   node.Value,
		Anchor:  node.Anchor,
		Alias:   node.Alias,
		Style:   node.Style,
		Content: make([]*yaml.Node, 0, len(node.Content)),
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode && (key.Value == CommonCwdKey || key.Value == CommonShellKey) {
			continue
		}
		out.Content = append(out.Content, key, node.Content[i+1])
	}

	return out
}

// APIVersioner is the optional capability a Builder may implement to declare
// the profile schema apiVersion it supports (SPEC §7.11). It is dispatched by
// type-assertion — a Builder that does not implement it is a legacy builder
// that does not participate in apiVersion reconciliation.
type APIVersioner interface {
	APIVersion() string
}

// VersionedBuildFunc is a Builder that also declares a supported schema
// apiVersion (satisfies both Builder and APIVersioner). It is the versioned
// counterpart of BuildFunc.
type VersionedBuildFunc struct {
	// BuildFn is the wrapped build function.
	BuildFn func(ctx Context) (spec.SandboxSpec, error)
	// Version is the declared schema apiVersion, e.g.
	// "docker.profiles.sandbox.teran.dev/v1".
	Version string
}

// Build invokes the wrapped build function.
func (v VersionedBuildFunc) Build(ctx Context) (spec.SandboxSpec, error) {
	return v.BuildFn(ctx)
}

// APIVersion returns the declared schema apiVersion.
func (v VersionedBuildFunc) APIVersion() string { return v.Version }

// BuildFuncWithVersion wraps a build function and a declared schema apiVersion
// into a Builder that also satisfies APIVersioner.
func BuildFuncWithVersion(fn func(ctx Context) (spec.SandboxSpec, error), apiVersion string) Builder {
	return VersionedBuildFunc{BuildFn: fn, Version: apiVersion}
}

// Reconcile checks that the declared profile apiVersion is supported by the
// builder b. An empty declared version (legacy driver+options form) is a no-op
// returning nil. A builder that does not declare a schema apiVersion (does not
// implement APIVersioner) fails closed with ErrAPIVersionMismatch, as does a
// declared version that does not equal the builder's declared version.
func Reconcile(declared string, b Builder) error {
	if strings.TrimSpace(declared) == "" {
		return nil
	}
	ver, ok := b.(APIVersioner)
	if !ok {
		return fmt.Errorf("%w: driver does not declare a schema apiVersion (profile declares %q)",
			ErrAPIVersionMismatch, declared)
	}
	if strings.TrimSpace(ver.APIVersion()) != strings.TrimSpace(declared) {
		return fmt.Errorf("%w: profile apiVersion %q is not supported (driver declares %q)",
			ErrAPIVersionMismatch, declared, ver.APIVersion())
	}

	return nil
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
