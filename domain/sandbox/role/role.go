// Package role defines the ISP role interfaces that a backend implements and
// the capability set used to dispatch them via type-assertion (§6.2).
package role

import (
	"context"
	"errors"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// ErrCapabilityNotSupported is returned when a backend does not implement a
// role interface that a tool requires. It is never a silent no-op stub.
var ErrCapabilityNotSupported = errors.New("capability not supported")

// Backend is the base role every backend implements (§6.2). Name identifies
// the backend (e.g. "docker"), Priority orders backend selection,
// Capabilities advertises which role interfaces the backend implements, and
// Driver reports the backend driver (SPEC §7.12) so the service can enforce
// one-backend-per-process driver coherence. Slug is the namespacing prefix of
// the backend's session ids (SPEC §7.8): the service composes every session id
// as `slug:internalID` and routes session-id-addressed ops by it. For the
// built-ins the slug equals the driver string (docker -> "docker",
// seatbelt -> "seatbelt"), but it is a distinct, backend-authored value so a
// driver could in principle expose a different slug.
type Backend interface {
	Name() string
	Priority() int
	Capabilities() CapabilitySet
	Driver() spec.Driver
	Slug() string
}

// Lifecycle creates and destroys sandboxes.
type Lifecycle interface {
	Create(ctx context.Context, s spec.SandboxSpec) (string, error)
	Destroy(ctx context.Context, id string) error
}

// Executable runs commands inside an existing sandbox.
type Executable interface {
	Exec(ctx context.Context, id string, req exec.Request) (*exec.Result, error)
}

// Introspectable exposes detailed state and live resource usage of a sandbox.
type Introspectable interface {
	Info(ctx context.Context, id string) (any, error)
	Stats(ctx context.Context, id string) (any, error)
}

// CapabilitySet advertises which role interfaces a backend implements. The
// tool layer uses it to select backends and to short-circuit before calling
// into a backend that does not support a capability.
type CapabilitySet struct {
	Lifecycle      bool
	Executable     bool
	Introspectable bool
}
