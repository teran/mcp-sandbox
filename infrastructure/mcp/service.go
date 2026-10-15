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
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/application/profile"
	"github.com/teran/mcp-sandbox/application/session"
	sandboxtool "github.com/teran/mcp-sandbox/application/tools/sandbox"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

// needAll is the capability set required to create and drive a sandbox.
var needAll = role.CapabilitySet{
	Lifecycle:      true,
	Executable:     true,
	Introspectable: true,
}

// isAuto reports whether a requested driver means "defer to selection": the
// DriverAuto value or its empty-string alias (SPEC §7.12).
func isAuto(d spec.Driver) bool { return d == "" || d == spec.DriverAuto }

// serviceLog is the base logrus entry for trace diagnostics. The session_id
// and request_id from the context are attached per call (L09).
var serviceLog = logrus.WithField("component", "sandbox-service")

// Option configures a Service at construction.
type Option func(*Service)

// WithLogger sets the base logrus entry used for trace diagnostics. It
// defaults to the package-level sandbox-service logger. It is primarily for
// tests; production wires no logger and derives request/session id from the
// context.
func WithLogger(e *logrus.Entry) Option { return func(s *Service) { s.log = e } }

// Service implements the application-layer sandbox.Service port. It lazily
// selects and constructs a backend on the first call that needs one (A02/N35 —
// no startup probing) and drives the in-memory session manager (X01/X05).
type Service struct {
	reg *backend.Registry
	cfg backend.Config

	mu  sync.Mutex
	mgr *session.Manager
	be  role.Backend

	// log is the base logrus entry for trace diagnostics (L09).
	log *logrus.Entry

	// profiles is the read-only operator profile store (Phase 7). It serves as
	// both the sandbox-create ProfileResolver and the profile_list Lister.
	profiles *profile.Store
}

// NewService returns a sandbox Service backed by the given backend registry.
func NewService(reg *backend.Registry, cfg backend.Config, opts ...Option) *Service {
	s := &Service{reg: reg, cfg: cfg, log: serviceLog}
	for _, o := range opts {
		if o != nil {
			o(s)
		}
	}

	return s
}

// entryFor returns a logrus entry derived from the service's base logger with
// the session_id and request_id from ctx attached when available (L09).
func (s *Service) entryFor(ctx context.Context, sessionID string) *logrus.Entry {
	e := s.log
	if sessionID != "" {
		e = e.WithField("session_id", sessionID)
	}

	return logging.RequestEntry(ctx, e)
}

// SetProfileStore attaches the read-only operator profile store (Phase 7).
func (s *Service) SetProfileStore(st *profile.Store) { s.profiles = st }

// ensure lazily selects and constructs the backend and session manager exactly
// once (one backend per process). Availability is evaluated here, on the first
// call (A02/N35). When a backend is already cached:
//
//   - want == auto → the cached backend is reused as-is (auto never triggers a
//     mismatch);
//   - want is a concrete driver → the cached backend's driver must match, else
//     the request is rejected (one backend per process, SPEC §7.12).
func (s *Service) ensure(ctx context.Context, want spec.Driver) (*session.Manager, role.Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mgr != nil {
		if !isAuto(want) && s.be.Driver() != want {
			return nil, nil, fmt.Errorf("backend driver %q does not match requested driver %q", s.be.Driver(), want)
		}

		return s.mgr, s.be, nil
	}
	f, err := s.reg.Select(ctx, needAll, want)
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
	var res sandboxtool.ProfileResolver
	if s.profiles != nil {
		res = s.profiles
	}

	return sandboxtool.Policy{
		DefaultImage: "", // retained on the struct but unused (image comes from the profile)
		Isolation:    s.defaultIsolation(),
		Backend:      "docker",
		Profiles:     res,
	}
}

// BackendName reports the backend name exposed to sandbox_list. The MVP ships a
// single docker backend, so this is the policy backend name.
func (s *Service) BackendName() string { return s.Policy().Backend }

// Create builds a sandbox from a typed spec (search-before-create, X02). The
// spec's driver drives backend selection (SPEC §7.12).
func (s *Service) Create(ctx context.Context, sp spec.SandboxSpec) (*domainsession.Session, error) {
	mgr, _, err := s.ensure(ctx, sp.Driver)
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
func (s *Service) List(ctx context.Context) []*domainsession.Session {
	s.mu.Lock()
	mgr := s.mgr
	s.mu.Unlock()
	if mgr == nil {
		return nil
	}

	return mgr.List(ctx)
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
	s.entryFor(ctx, req.SandboxID).WithField("operation", "exec").Trace("sandbox: exec")
	_, be, err := s.ensure(ctx, spec.DriverAuto)
	if err != nil {
		return nil, err
	}
	ex, ok := be.(role.Executable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}

	return ex.Exec(ctx, req.SandboxID, req)
}

// Info returns detailed state through the Introspectable role.
func (s *Service) Info(ctx context.Context, id string) (any, error) {
	s.entryFor(ctx, id).WithField("operation", "info").Trace("sandbox: info")
	_, be, err := s.ensure(ctx, spec.DriverAuto)
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
	s.entryFor(ctx, id).WithField("operation", "stats").Trace("sandbox: stats")
	_, be, err := s.ensure(ctx, spec.DriverAuto)
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
// Resources/network are overridden by the operator profile at BuildSpec time;
// the built-in bounded values here are a fail-closed, profile-independent base.
//
// Hardening constants: default non-root user, bounded pids/memory/cpu and an
// explicit nofile rlimit ceiling for the fail-closed base (SPEC §7.1).
const (
	defaultSandboxUID  = 1000
	defaultSandboxGID  = 1000
	defaultPidsLimit   = 512
	defaultMemoryBytes = 4 * 1024 * 1024 * 1024 // 4 GiB, in-RAM only
	defaultNanoCPUs    = 1_000_000_000          // 1 CPU
	defaultNofileSoft  = 1024
	defaultNofileHard  = 2048
)

func (s *Service) defaultIsolation() spec.Isolation {
	return spec.Isolation{
		ReadOnlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         spec.SeccompDefault,
		User:            spec.UIDGID{UID: defaultSandboxUID, GID: defaultSandboxGID},
		PidsLimit:       defaultPidsLimit,
		Memory:          defaultMemoryBytes,
		// MemorySwap equal to Memory disables swap entirely (memory + swap limit
		// == memory => no swap), keeping the sandbox in-RAM only.
		MemorySwap:   defaultMemoryBytes,
		NanoCPUs:     defaultNanoCPUs,
		Ulimits:      []spec.Ulimit{{Name: "nofile", Soft: defaultNofileSoft, Hard: defaultNofileHard}},
		NetworkMode:  spec.NetworkBridge,
		TmpfsWorkdir: true,
		AutoRemove:   true,
	}
}
