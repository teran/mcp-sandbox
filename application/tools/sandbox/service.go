// Package sandbox declares the concrete sandbox tool set (SPEC §4) as
// declarative ToolDefs and the application-layer Service interface the tools
// depend on. The tools are grouped read → write → delete (S03) and are always
// invoked through the shared handler-wrapper (SPEC §6.4). The Service is
// implemented by an infrastructure adapter (lazy backend selection + session
// manager); the tools depend only on this interface and on the domain model.
package sandbox

import (
	"context"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// Policy is the server-enforced sandbox policy exposed to the tools (SPEC
// §7.1/§7.2). It carries the base image and the immutable Isolation block that
// sandbox_create applies; a caller can never weaken it.
type Policy struct {
	// DefaultImage is used when a create request does not pin an image.
	DefaultImage string
	// Isolation is the immutable, server-enforced hardening block.
	Isolation spec.Isolation
	// Backend is the backend name reported by sandbox_list (e.g. "docker").
	Backend string
}

// Service is the application-layer port the sandbox tools call. It is
// implemented by an infrastructure adapter that lazily selects a backend and
// drives the session manager; every backend call returns
// role.ErrCapabilityNotSupported when the selected backend lacks a capability.
type Service interface {
	// Policy returns the server-enforced policy.
	Policy() Policy
	// BackendName reports the name of the selected backend (e.g. "docker").
	BackendName() string
	// Create builds a sandbox from a typed spec (search-before-create, X02).
	Create(ctx context.Context, s spec.SandboxSpec) (*domainsession.Session, error)
	// Get returns the session with the given id.
	Get(id string) (*domainsession.Session, error)
	// List returns every live session.
	List() []*domainsession.Session
	// Destroy tears down one session (idempotent, X02).
	Destroy(ctx context.Context, id string) error
	// DestroyAll tears down every session (idempotent).
	DestroyAll(ctx context.Context) error
	// Exec runs a command as an argument array with no shell (S11).
	Exec(ctx context.Context, req exec.Request) (*exec.Result, error)
	// Info returns detailed state of a sandbox.
	Info(ctx context.Context, id string) (any, error)
	// Stats returns live resource usage of a sandbox.
	Stats(ctx context.Context, id string) (any, error)
}
