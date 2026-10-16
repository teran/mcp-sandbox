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
	"errors"
	"fmt"
	"strings"
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

// needAll is the capability set required to create and drive a sandbox
// (create/exec/destroy): Lifecycle + Executable. Introspectable is deliberately
// NOT required — the darwin (sandbox-exec) backend legitimately lacks it (no
// cgroups on macOS; Info/Stats return ErrCapabilityNotSupported, SPEC §6.2 "no
// stubs"), and it must still be selectable. Requiring Introspectable here made
// darwin never pass covers() in registry.Select, so it was unselectable
// everywhere.
var needAll = role.CapabilitySet{
	Lifecycle:  true,
	Executable: true,
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
// selects and constructs a backend per concrete driver (A02/N35 — no startup
// probing) and drives an in-memory session manager per backend (X01/X05).
// Multiple drivers (e.g. docker + seatbelt) coexist in one Service; a session
// is routed to the backend that owns it by its id.
type Service struct {
	reg *backend.Registry
	cfg backend.Config

	mu sync.Mutex
	// runtimes holds one backend + session manager per concrete driver, created
	// lazily on first use (A02/N35). Multiple drivers coexist.
	runtimes map[spec.Driver]*driverRuntime
	// sessionDriver maps a live session id to the driver that owns it, so
	// session-id-addressed ops (Exec/Info/Stats/Destroy/Get) route to the
	// correct backend.
	sessionDriver map[string]spec.Driver

	// log is the base logrus entry for trace diagnostics (L09).
	log *logrus.Entry

	// profiles is the read-only operator profile store (Phase 7). It serves as
	// both the sandbox-create ProfileResolver and the profile_list Lister.
	profiles *profile.Store
}

// driverRuntime couples a constructed backend with its session manager.
type driverRuntime struct {
	be  role.Backend
	mgr *session.Manager
}

// NewService returns a sandbox Service backed by the given backend registry.
func NewService(reg *backend.Registry, cfg backend.Config, opts ...Option) *Service {
	s := &Service{
		reg:           reg,
		cfg:           cfg,
		log:           serviceLog,
		runtimes:      make(map[spec.Driver]*driverRuntime),
		sessionDriver: make(map[string]spec.Driver),
	}
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

// ensure returns the runtime (backend + session manager) for a concrete driver,
// lazily constructing it on first use (A02/N35). Multiple drivers coexist, each
// with its own runtime. `want` must be a concrete driver (auto is resolved by
// resolveDriver before Create).
func (s *Service) ensure(ctx context.Context, want spec.Driver) (*driverRuntime, error) {
	if isAuto(want) {
		return nil, fmt.Errorf("backend: auto driver must be resolved to a concrete driver")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt, ok := s.runtimes[want]; ok {
		return rt, nil
	}
	f, err := s.reg.Select(ctx, needAll, want)
	if err != nil {
		return nil, err
	}
	be, err := f.New(s.cfg)
	if err != nil {
		return nil, err
	}
	life, ok := be.(role.Lifecycle)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}
	rt := &driverRuntime{be: be, mgr: session.NewManager(life)}
	s.runtimes[want] = rt

	return rt, nil
}

// resolveDriver resolves an auto (or empty) driver to a concrete one by
// selecting the best available backend; a concrete driver passes through
// unchanged. It is used by Create, since the resulting spec's driver is never
// auto (SPEC §7.12) but callers/tests may pass an empty driver.
func (s *Service) resolveDriver(ctx context.Context, want spec.Driver) (spec.Driver, error) {
	if !isAuto(want) {
		return want, nil
	}
	f, err := s.reg.Select(ctx, needAll, spec.DriverAuto)
	if err != nil {
		return "", err
	}

	return f.Driver, nil
}

// resolveID resolves a caller-supplied id to the owning runtime and the full
// session id (SPEC §6.6). It must be called with s.mu held (Lock). An exact
// full id fast-paths O(1) via sessionDriver; otherwise every live session id
// (the sessionDriver keys) is scanned for a unique prefix match, mirroring
// Manager.resolveID. Zero matches returns ErrSessionNotFound, more than one
// match returns ErrAmbiguousSessionID, and an empty id is never a valid
// resolution (a real session id is always non-empty, §7.8).
func (s *Service) resolveID(prefix string) (*driverRuntime, string, error) {
	if prefix == "" {
		return nil, "", session.ErrSessionNotFound
	}
	if d, ok := s.sessionDriver[prefix]; ok {
		rt, ok := s.runtimes[d]
		if !ok {
			return nil, "", session.ErrSessionNotFound
		}

		return rt, prefix, nil
	}

	var matchDriver spec.Driver
	var match string
	for id, d := range s.sessionDriver {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if match != "" {
			return nil, "", session.ErrAmbiguousSessionID
		}
		match = id
		matchDriver = d
	}
	if match == "" {
		return nil, "", session.ErrSessionNotFound
	}
	rt, ok := s.runtimes[matchDriver]
	if !ok {
		return nil, "", session.ErrSessionNotFound
	}

	return rt, match, nil
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
// spec's driver drives backend selection (SPEC §7.12); each driver has its own
// runtime, so docker and seatbelt sandboxes coexist.
func (s *Service) Create(ctx context.Context, sp spec.SandboxSpec) (*domainsession.Session, error) {
	d, err := s.resolveDriver(ctx, sp.Driver)
	if err != nil {
		return nil, err
	}
	rt, err := s.ensure(ctx, d)
	if err != nil {
		return nil, err
	}
	sess, err := rt.mgr.Create(ctx, sp)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sessionDriver[sess.ID()] = d
	s.mu.Unlock()

	return sess, nil
}

// Get returns the session with the given id, routing a unique short/prefix id
// to the owning backend and the full id (SPEC §6.6).
func (s *Service) Get(id string) (*domainsession.Session, error) {
	s.mu.Lock()
	runtime, full, err := s.resolveID(id)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	return runtime.mgr.Get(full)
}

// List returns every live session across all backends.
func (s *Service) List(ctx context.Context) []*domainsession.Session {
	s.mu.Lock()
	rts := make([]*driverRuntime, 0, len(s.runtimes))
	for _, rt := range s.runtimes {
		rts = append(rts, rt)
	}
	s.mu.Unlock()

	var out []*domainsession.Session
	for _, rt := range rts {
		out = append(out, rt.mgr.List(ctx)...)
	}

	return out
}

// Destroy tears down one session (idempotent, X02), routing a unique
// short/prefix id to the owning backend and the full id. An unknown id is a
// no-op; an ambiguous prefix is propagated as ErrAmbiguousSessionID.
func (s *Service) Destroy(ctx context.Context, id string) error {
	s.mu.Lock()
	runtime, full, err := s.resolveID(id)
	s.mu.Unlock()
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			// Idempotent no-op (X02): a zero-match id/prefix is not an error.
			return nil
		}

		return err
	}

	if err := runtime.mgr.Destroy(ctx, full); err != nil {
		return err
	}

	// The session is gone; drop its id from the routing table so a subsequent
	// short/prefix lookup no longer resolves to the destroyed backend session.
	s.mu.Lock()
	delete(s.sessionDriver, full)
	s.mu.Unlock()

	return nil
}

// DestroyAll tears down every session across all backends (idempotent).
func (s *Service) DestroyAll(ctx context.Context) error {
	s.mu.Lock()
	rts := make([]*driverRuntime, 0, len(s.runtimes))
	for _, rt := range s.runtimes {
		rts = append(rts, rt)
	}
	s.mu.Unlock()

	for _, rt := range rts {
		if err := rt.mgr.DestroyAll(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Exec runs a command as an argument array (S11) through the Executable role of
// the backend that owns the session (routed by session id; a unique short id is
// resolved to the full id before it reaches the backend).
func (s *Service) Exec(ctx context.Context, req exec.Request) (*exec.Result, error) {
	s.entryFor(ctx, req.SandboxID).WithField("operation", "exec").Trace("sandbox: exec")
	s.mu.Lock()
	runtime, full, err := s.resolveID(req.SandboxID)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	executor, ok := runtime.be.(role.Executable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}
	req.SandboxID = full

	return executor.Exec(ctx, full, req)
}

// Info returns detailed state through the Introspectable role of the backend
// that owns the session (a unique short id is resolved to the full id).
func (s *Service) Info(ctx context.Context, id string) (any, error) {
	s.entryFor(ctx, id).WithField("operation", "info").Trace("sandbox: info")
	s.mu.Lock()
	runtime, full, err := s.resolveID(id)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ins, ok := runtime.be.(role.Introspectable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}

	return ins.Info(ctx, full)
}

// Stats returns live resource usage through the Introspectable role of the
// backend that owns the session (a unique short id is resolved to the full id).
func (s *Service) Stats(ctx context.Context, id string) (any, error) {
	s.entryFor(ctx, id).WithField("operation", "stats").Trace("sandbox: stats")
	s.mu.Lock()
	runtime, full, err := s.resolveID(id)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ins, ok := runtime.be.(role.Introspectable)
	if !ok {
		return nil, role.ErrCapabilityNotSupported
	}

	return ins.Stats(ctx, full)
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
