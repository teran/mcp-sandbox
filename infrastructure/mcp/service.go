// Package mcp wires the sandbox tools into an MCP server (SPEC §3, §5): it
// builds the go-sdk server (ServerOptions.Logger = slog→logrus, per-tool
// instructions), registers every ToolDef through the shared handler-wrapper, and
// provides the concrete sandbox Service (lazy backend selection + session
// manager) plus the access-log adapter over logrus.
//
// This package is the single adapter edge that connects the application-layer
// tool framework to the infrastructure (backend registry + logging); see
// .go-arch-lint.yml.
package mcp

import (
	"context"
	"sync"

	"github.com/teran/mcp-sandbox/application/session"
	sandboxtool "github.com/teran/mcp-sandbox/application/tools/sandbox"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

// needAll is the capability set required to create and drive a sandbox.
var needAll = role.CapabilitySet{
	Lifecycle:      true,
	Executable:     true,
	Filesystem:     true,
	Introspectable: true,
}

// Service implements the application-layer sandbox.Service port. It lazily
// selects and constructs a backend on the first call that needs one (A02/N35 —
// no startup probing) and drives the in-memory session manager (X01/X05).
type Service struct {
	reg *backend.Registry
	cfg backend.Config

	mu  sync.Mutex
	mgr *session.Manager
	be  role.Backend
}

// NewService returns a sandbox Service backed by the given backend registry.
func NewService(reg *backend.Registry, cfg backend.Config) *Service {
	return &Service{reg: reg, cfg: cfg}
}

// ensure lazily selects and constructs the backend and session manager exactly
// once. Availability is evaluated here, on the first call (A02/N35).
func (s *Service) ensure(ctx context.Context) (*session.Manager, role.Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mgr != nil {
		return s.mgr, s.be, nil
	}
	f, err := s.reg.Select(ctx, needAll)
	if err != nil {
		return nil, nil, err
	}
	be, err := f.New(s.cfg)
	if err != nil {
		return nil, nil, err
	}
	life, ok := be.(role.Lifecycle)
	if !ok {
		return nil, nil, role.ErrCapabilityNotSupported
	}
	s.be = be
	s.mgr = session.NewManager(life)
	return s.mgr, be, nil
}

// Policy returns the server-enforced sandbox policy.
func (s *Service) Policy() sandboxtool.Policy {
	return sandboxtool.Policy{
		DefaultImage: s.cfg.DefaultImage,
		Isolation:    defaultIsolation(),
		Backend:      "docker",
	}
}

// BackendName reports the backend name exposed to sandbox_list. The MVP ships a
// single docker backend, so this is the policy backend name.
func (s *Service) BackendName() string { return s.Policy().Backend }

// Create builds a sandbox from a typed spec (search-before-create, X02).
func (s *Service) Create(ctx context.Context, sp spec.SandboxSpec) (*domainsession.Session, error) {
	mgr, _, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return mgr.Create(ctx, sp)
}

// Get returns the session with the given id.
func (s *Service) Get(id string) (*domainsession.Session, error) {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()
	if mgr == nil {
		return nil, session.ErrSessionNotFound
	}
	return mgr.Get(id)
}

// List returns every live session.
func (s *Service) List() []*domainsession.Session {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.List()
}

// Destroy tears down one session (idempotent, X02).
func (s *Service) Destroy(ctx context.Context, id string) error {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.Destroy(ctx, id)
}

// DestroyAll tears down every session (idempotent).
func (s *Service) DestroyAll(ctx context.Context) error {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.DestroyAll(ctx)
}

// Exec runs a command as an argument array (S11) through the Executable role.
func (s *Service) Exec(ctx context.Context, req exec.Request) (*exec.Result, error) {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	ex, ok := be.(role.Executable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}
	return ex.Exec(ctx, req.SandboxID, req)
}

// ReadFile reads a file through the Filesystem role.
func (s *Service) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	fs, ok := be.(role.Filesystem)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}
	return fs.ReadFile(ctx, id, path)
}

// WriteFile writes a file through the Filesystem role.
func (s *Service) WriteFile(ctx context.Context, id, path string, data []byte) error {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	fs, ok := be.(role.Filesystem)
	if !ok {
		return role.ErrCapabilityNotSupported
	}
	return fs.WriteFile(ctx, id, path, data)
}

// CopyIn copies a host path into a sandbox through the Filesystem role.
func (s *Service) CopyIn(ctx context.Context, id, srcHost, dstSandbox string) error {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	fs, ok := be.(role.Filesystem)
	if !ok {
		return role.ErrCapabilityNotSupported
	}
	return fs.CopyIn(ctx, id, srcHost, dstSandbox)
}

// CopyOut copies a sandbox path out to the host through the Filesystem role.
func (s *Service) CopyOut(ctx context.Context, id, srcSandbox, dstHost string) error {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return err
	}
	fs, ok := be.(role.Filesystem)
	if !ok {
		return role.ErrCapabilityNotSupported
	}
	return fs.CopyOut(ctx, id, srcSandbox, dstHost)
}

// Info returns detailed state through the Introspectable role.
func (s *Service) Info(ctx context.Context, id string) (any, error) {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	ins, ok := be.(role.Introspectable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}
	return ins.Info(ctx, id)
}

// Stats returns live resource usage through the Introspectable role.
func (s *Service) Stats(ctx context.Context, id string) (any, error) {
	_, be, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	ins, ok := be.(role.Introspectable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}
	return ins.Stats(ctx, id)
}

// defaultIsolation is the immutable, server-enforced hardening block (SPEC
// §7.1). sandbox_create applies it to every spec; a caller can never weaken it.
func defaultIsolation() spec.Isolation {
	return spec.Isolation{
		ReadOnlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         spec.SeccompDefault,
		User:            spec.UIDGID{UID: 1000, GID: 1000},
		PidsLimit:       512,
		Memory:          512 * 1024 * 1024,
		MemorySwap:      1024 * 1024 * 1024,
		NanoCPUs:        1_000_000_000,
		Ulimits:         []spec.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}},
		NetworkMode:     spec.NetworkNone,
		TmpfsWorkdir:    true,
		AutoRemove:      true,
	}
}
