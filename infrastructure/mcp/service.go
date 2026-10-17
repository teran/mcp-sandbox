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
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/application/profile"
	"github.com/teran/mcp-sandbox/application/session"
	sandboxtool "github.com/teran/mcp-sandbox/application/tools/sandbox"
	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
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

	// log is the base logrus entry for trace diagnostics (L09).
	log *logrus.Entry

	// profiles is the read-only operator profile store (Phase 7). It serves as
	// both the sandbox-create ProfileResolver and the profile_list Lister.
	profiles *profile.Store

	// drivers maps a profile's concrete driver token to its builder (SPEC
	// §6.11/§7.11). BuildSpec dispatches through it, fail-closed on an unknown
	// driver. It defaults to the process-wide driver.Default (seeded by the
	// built-in docker/seatbelt builders) and can be overridden in tests.
	drivers *driver.Registry
}

// driverRuntime couples a constructed backend with its session manager.
type driverRuntime struct {
	be  role.Backend
	mgr *session.Manager
}

// NewService returns a sandbox Service backed by the given backend registry.
func NewService(reg *backend.Registry, cfg backend.Config, opts ...Option) *Service {
	s := &Service{
		reg:      reg,
		cfg:      cfg,
		log:      serviceLog,
		runtimes: make(map[spec.Driver]*driverRuntime),
		drivers:  driver.Default,
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
	// Fail-closed slug guard (SPEC §7.8): no two runtimes may share a slug —
	// session-id-addressed ops route by slug, so a collision would make routing
	// ambiguous. Built-ins ("docker", "seatbelt") are unique; an external
	// backend that shadows one is rejected here rather than silently corrupting
	// routing.
	slug := be.Slug()
	for _, existing := range s.runtimes {
		if existing.be.Slug() == slug {
			return nil, fmt.Errorf("backend: slug %q already in use", slug)
		}
	}
	rt := &driverRuntime{be: be, mgr: session.NewManager(slug, life)}
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

// resolveSessionID resolves a caller-supplied session id to the owning runtime
// and the FULL namespaced session id (SPEC §6.6/§7.8). It must be called with
// s.mu held (Lock). The id is parsed as `slug:internal` (STRICT, single ':');
// the slug selects the owning runtime (`runtimes[spec.Driver(slug)]`), and the
// runtime's manager resolves a full or unique `slug:<prefix>` id to the full
// namespaced id. An empty, malformed (non-namespaced) or unknown-slug id, and a
// known-slug id with no live match, all resolve to ErrSessionNotFound; a prefix
// matching multiple sessions returns ErrAmbiguousSessionID.
func (s *Service) resolveSessionID(id string) (*driverRuntime, string, error) {
	if id == "" {
		return nil, "", session.ErrSessionNotFound
	}
	n, err := domainsession.ParseNamespacedID(id)
	if err != nil {
		return nil, "", session.ErrSessionNotFound
	}
	runtime, ok := s.runtimes[spec.Driver(n.Slug)]
	if !ok {
		return nil, "", session.ErrSessionNotFound
	}
	full, err := runtime.mgr.Resolve(id)
	if err != nil {
		return nil, "", err
	}

	return runtime, full, nil
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

// BuildSpec assembles a typed SandboxSpec from an operator-authored profile and
// per-request params by dispatching to the profile's concrete driver builder via
// the driver registry (SPEC §6.11/§7.11). It fails closed: an unknown driver is
// rejected with driver.ErrUnknownDriver, never silently falling back. The
// builder receives the immutable defaultIsolation() as Policy, so a profile can
// never weaken hardening.
func (s *Service) BuildSpec(p *domainprofile.Profile, params domainprofile.Params) (spec.SandboxSpec, error) {
	builder, err := s.drivers.Get(p.Driver)
	if err != nil {
		return spec.SandboxSpec{}, err
	}

	return builder.Build(driver.Context{
		Name:        p.Name,
		Description: p.Description,
		Cwd:         p.Cwd,
		Shell:       p.Shell,
		Options:     p.Options,
		Policy:      s.defaultIsolation(),
		Params:      params,
	})
}

// Create builds a sandbox from a typed spec (search-before-create, X02). The
// spec's driver drives backend selection (SPEC §7.12); each driver has its own
// runtime, so docker and seatbelt sandboxes coexist. EnforceHardening runs FIRST
// (before backend selection/creation): it re-asserts the immutable Isolation
// policy on the spec and gates every mount source by cfg.AllowDirs (S04),
// fail-closed, so a weakened or out-of-scope spec never reaches a backend.
func (s *Service) Create(ctx context.Context, sp spec.SandboxSpec) (*domainsession.Session, error) {
	if err := spec.EnforceHardening(s.defaultIsolation(), &sp, s.cfg.AllowDirs); err != nil {
		return nil, err
	}
	d, err := s.resolveDriver(ctx, sp.Driver)
	if err != nil {
		return nil, err
	}
	rt, err := s.ensure(ctx, d)
	if err != nil {
		return nil, err
	}

	return rt.mgr.Create(ctx, sp)
}

// Get returns the session with the given id, routing a unique short/prefix id
// to the owning backend and the full id (SPEC §6.6).
func (s *Service) Get(id string) (*domainsession.Session, error) {
	s.mu.Lock()
	runtime, full, err := s.resolveSessionID(id)
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
// short/prefix id to the owning backend and the full id. An unknown-slug or
// malformed id is an ERROR (ErrSessionNotFound, user decision 1); a valid slug
// whose internal id matches no live session is an idempotent per-driver no-op
// (X02); a service that has never initialized a backend is a safe no-op.
func (s *Service) Destroy(ctx context.Context, id string) error {
	s.mu.Lock()
	runtime, full, err := s.resolveSessionID(id)
	// noOp records the ErrSessionNotFound cases that must stay a no-op: a
	// known-slug id with no live match (per-driver X02), or a service with no
	// initialized runtime at all (nothing to route to).
	noOp := false
	if errors.Is(err, session.ErrSessionNotFound) {
		noOp = len(s.runtimes) == 0 || s.slugKnownLocked(id)
	}
	s.mu.Unlock()
	if err != nil {
		if noOp {
			return nil
		}

		return err
	}

	if err := runtime.mgr.Destroy(ctx, full); err != nil {
		return err
	}

	return nil
}

// slugKnownLocked reports whether id carries the slug of an initialized
// runtime (SPEC §7.8). It must be called with s.mu held.
func (s *Service) slugKnownLocked(id string) bool {
	n, err := domainsession.ParseNamespacedID(id)
	if err != nil {
		return false
	}
	_, ok := s.runtimes[spec.Driver(n.Slug)]

	return ok
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
	runtime, full, err := s.resolveSessionID(req.SandboxID)
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
	runtime, full, err := s.resolveSessionID(id)
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
	runtime, full, err := s.resolveSessionID(id)
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
