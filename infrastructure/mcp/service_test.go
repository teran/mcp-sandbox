package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/application/profile"
	"github.com/teran/mcp-sandbox/application/session"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

// fakeBackend implements every role so the Service can exercise all paths.
type fakeBackend struct {
	created       bool
	createdSpec   spec.SandboxSpec
	destroyed     int
	execReq       exec.Request
	execRes       *exec.Result
	info          any
	stats         any
	capNotSupport bool
	driver        spec.Driver

	// createIDs, when non-nil, supplies deterministic session ids in order so
	// prefix-resolution tests can force long ids that ShortID truncates and
	// ids that share a common prefix. Nil falls back to the counter ids.
	createIDs     []string
	createIDx     int
	lastExecID    string
	lastInfoID    string
	lastStatsID   string
	lastDestroyID string
}

// fakeBackendCounter supplies a globally unique id per Create call across every
// fakeBackend instance. Under the backend-owned-id design the session id IS the
// backend-returned id, so two creates in one service must never collide on the
// same id (the old constant "cid-1" would break multi-session tests).
var fakeBackendCounter atomic.Int64

func (f *fakeBackend) Name() string  { return "fake" }
func (f *fakeBackend) Priority() int { return 0 }
func (f *fakeBackend) Driver() spec.Driver {
	return f.driver
}

func (f *fakeBackend) Capabilities() role.CapabilitySet {
	return role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true}
}

func (f *fakeBackend) Create(_ context.Context, s spec.SandboxSpec) (string, error) {
	f.created = true
	f.createdSpec = s

	if f.createIDs != nil {
		if f.createIDx >= len(f.createIDs) {
			// Backstop: never hand back an empty id.
			return fmt.Sprintf("cid-%d", fakeBackendCounter.Add(1)), nil
		}
		id := f.createIDs[f.createIDx]
		f.createIDx++

		return id, nil
	}

	return fmt.Sprintf("cid-%d", fakeBackendCounter.Add(1)), nil
}

func (f *fakeBackend) Destroy(_ context.Context, id string) error {
	f.destroyed++
	f.lastDestroyID = id

	return nil
}

func (f *fakeBackend) Exec(_ context.Context, id string, req exec.Request) (*exec.Result, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
	f.lastExecID = id
	f.execReq = req

	return f.execRes, nil
}

func (f *fakeBackend) Info(_ context.Context, id string) (any, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
	f.lastInfoID = id

	return f.info, nil
}

func (f *fakeBackend) Stats(_ context.Context, id string) (any, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
	f.lastStatsID = id

	return f.stats, nil
}

func testSpec() spec.SandboxSpec {
	svc := NewService(backend.NewRegistry(), backend.Config{})
	iso := svc.defaultIsolation()

	return spec.SandboxSpec{
		Image:        "img",
		UIDGID:       iso.User,
		ReadOnlyRoot: true,
		Isolation:    iso,
	}
}

func newTestService(t *testing.T, fb *fakeBackend) *Service {
	t.Helper()
	reg := backend.NewRegistry()
	if err := reg.Register(backend.Factory{
		Name:         "fake",
		Priority:     0,
		Driver:       spec.DriverDocker,
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true},
		Available:    func() bool { return true },
		New:          func(backend.Config) (role.Backend, error) { return fb, nil },
	}); err != nil {
		t.Fatal(err)
	}

	return NewService(reg, backend.Config{})
}

// createSessionID builds a spec via testSpec(), sets a concrete driver, creates
// the session through the service and returns its id. testSpec() has an empty
// Driver (auto); this helper forces a concrete driver so the session is routed
// to a specific backend under the multi-driver design.
func createSessionID(t *testing.T, svc *Service, driver spec.Driver) string {
	t.Helper()
	sp := testSpec()
	sp.Driver = driver
	sess, err := svc.Create(context.Background(), sp)
	if err != nil {
		t.Fatal(err)
	}

	return sess.ID()
}

func TestService_Create(t *testing.T) {
	fb := &fakeBackend{}
	svc := newTestService(t, fb)
	sess, err := svc.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !fb.created {
		t.Fatal("backend Create not called")
	}
	if sess.ID() == "" {
		t.Fatal("empty session id")
	}
}

func TestService_SelectError(t *testing.T) {
	reg := backend.NewRegistry() // empty → no backend
	svc := NewService(reg, backend.Config{})
	if _, err := svc.Create(context.Background(), testSpec()); err == nil {
		t.Fatal("expected selection error")
	}
}

func TestService_GetListDestroyBeforeInit(t *testing.T) {
	svc := NewService(backend.NewRegistry(), backend.Config{})
	if _, err := svc.Get("x"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Get err = %v", err)
	}
	if n := svc.List(context.Background()); n != nil {
		t.Fatalf("List = %v, want nil", n)
	}
	if err := svc.Destroy(context.Background(), "x"); err != nil {
		t.Fatalf("Destroy err = %v", err)
	}
	if err := svc.DestroyAll(context.Background()); err != nil {
		t.Fatalf("DestroyAll err = %v", err)
	}
}

func TestService_CreateThenQuery(t *testing.T) {
	fb := &fakeBackend{}
	svc := newTestService(t, fb)
	sess, err := svc.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != sess.ID() {
		t.Fatalf("id = %q", got.ID())
	}
	if len(svc.List(context.Background())) != 1 {
		t.Fatalf("List len = %d", len(svc.List(context.Background())))
	}
}

func TestService_OpsWithoutBackend(t *testing.T) {
	svc := NewService(backend.NewRegistry(), backend.Config{})
	if _, err := svc.Exec(context.Background(), exec.Request{}); err == nil {
		t.Fatal("exec should fail without backend")
	}
	if _, err := svc.Info(context.Background(), "s"); err == nil {
		t.Fatal("info should fail without backend")
	}
	if _, err := svc.Stats(context.Background(), "s"); err == nil {
		t.Fatal("stats should fail without backend")
	}
}

func TestService_Exec(t *testing.T) {
	fb := &fakeBackend{execRes: &exec.Result{ExitCode: 0, Stdout: []byte("hi")}}
	svc := newTestService(t, fb)
	id := createSessionID(t, svc, spec.DriverDocker)
	res, err := svc.Exec(context.Background(), exec.Request{SandboxID: id, Argv: []string{"echo", "hi"}, User: "1000:1000"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "hi" {
		t.Fatalf("result = %+v", res)
	}
	if fb.execReq.SandboxID != id || fb.execReq.User != "1000:1000" {
		t.Fatalf("execReq = %+v", fb.execReq)
	}
}

func TestService_ExecCapabilityNotSupported(t *testing.T) {
	fb := &fakeBackend{capNotSupport: true}
	svc := newTestService(t, fb)
	id := createSessionID(t, svc, spec.DriverDocker)
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: id}); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestService_IntrospectCapabilityNotSupported(t *testing.T) {
	fb := &fakeBackend{capNotSupport: true}
	svc := newTestService(t, fb)
	id := createSessionID(t, svc, spec.DriverDocker)
	if _, err := svc.Info(context.Background(), id); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("info err = %v", err)
	}
	if _, err := svc.Stats(context.Background(), id); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("stats err = %v", err)
	}
}

func TestService_FactoryNewError(t *testing.T) {
	reg := backend.NewRegistry()
	if err := reg.Register(backend.Factory{
		Name:         "bad",
		Priority:     0,
		Capabilities: needAll,
		Available:    func() bool { return true },
		New:          func(backend.Config) (role.Backend, error) { return nil, errors.New("construct failed") },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})
	if _, err := svc.Create(context.Background(), testSpec()); err == nil {
		t.Fatal("expected construction error")
	}
}

func TestService_LifecycleMissing(t *testing.T) {
	// A backend that implements only the base Backend role, not Lifecycle.
	reg := backend.NewRegistry()
	if err := reg.Register(backend.Factory{
		Name:         "nolife",
		Priority:     0,
		Driver:       spec.DriverDocker,
		Capabilities: needAll,
		Available:    func() bool { return true },
		New: func(backend.Config) (role.Backend, error) {
			return roleOnlyBackend{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})
	if _, err := svc.Create(context.Background(), testSpec()); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

// roleOnlyBackend implements only the base Backend role.
type roleOnlyBackend struct{}

func (roleOnlyBackend) Name() string                     { return "only" }
func (roleOnlyBackend) Priority() int                    { return 0 }
func (roleOnlyBackend) Capabilities() role.CapabilitySet { return needAll }
func (roleOnlyBackend) Driver() spec.Driver              { return spec.DriverDocker }

func TestService_Introspect(t *testing.T) {
	fb := &fakeBackend{info: "info", stats: "stats"}
	svc := newTestService(t, fb)
	id := createSessionID(t, svc, spec.DriverDocker)
	if v, err := svc.Info(context.Background(), id); err != nil || v != "info" {
		t.Fatalf("info = %v, %v", v, err)
	}
	if v, err := svc.Stats(context.Background(), id); err != nil || v != "stats" {
		t.Fatalf("stats = %v, %v", v, err)
	}
}

func TestService_PolicyAndBackendName(t *testing.T) {
	fb := &fakeBackend{}
	svc := newTestService(t, fb)
	pol := svc.Policy()
	if pol.Backend != "docker" || pol.DefaultImage != "" {
		t.Fatalf("policy = %+v", pol)
	}
	if svc.BackendName() != "docker" {
		t.Fatalf("backend name = %q", svc.BackendName())
	}
	if pol.Isolation.NetworkMode != spec.NetworkBridge {
		t.Fatalf("isolation = %+v", pol.Isolation)
	}
}

// TestService_PolicyWithProfileStore pins Phase 7: once SetProfileStore is wired,
// Policy exposes the store as the ProfileResolver so sandbox_create can resolve
// operator-authored profiles by name on demand (fail-closed: unknown name →
// ErrProfileNotFound).
func TestService_PolicyWithProfileStore(t *testing.T) {
	fb := &fakeBackend{}
	svc := newTestService(t, fb)
	// Before wiring: Profiles resolver is nil.
	if pol := svc.Policy(); pol.Profiles != nil {
		t.Fatalf("Profiles = %v, want nil before SetProfileStore", pol.Profiles)
	}
	// After wiring an empty-dir store, Policy must expose it as the resolver.
	st := profile.New(t.TempDir())
	svc.SetProfileStore(st)
	pol := svc.Policy()
	if pol.Profiles == nil {
		t.Fatal("Profiles = nil, want non-nil store after SetProfileStore")
	}
	// The wired store is actually used for resolution (fail-closed).
	if _, err := pol.Profiles.Get("anything"); !errors.Is(err, domainprofile.ErrProfileNotFound) {
		t.Fatalf("Get(anything) err = %v, want ErrProfileNotFound", err)
	}
}

func TestService_DefaultIsolationIsBridgeAndAppliedOnCreate(t *testing.T) {
	// The server-enforced default isolation must use a bridged network.
	defSvc := NewService(backend.NewRegistry(), backend.Config{})
	if got := defSvc.defaultIsolation().NetworkMode; got != spec.NetworkBridge {
		t.Fatalf("defaultIsolation().NetworkMode = %q, want bridge", got)
	}

	// sandbox_create (via the service) applies the bridge default to every spec.
	fb := &fakeBackend{}
	svc := newTestService(t, fb)
	if _, err := svc.Create(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	if !fb.created {
		t.Fatal("backend Create not called")
	}
	if fb.createdSpec.Isolation.NetworkMode != spec.NetworkBridge {
		t.Fatalf("created spec Isolation.NetworkMode = %q, want bridge", fb.createdSpec.Isolation.NetworkMode)
	}
	if err := fb.createdSpec.Validate(); err != nil {
		t.Fatalf("created spec must validate: %v", err)
	}
}

func TestService_NetworkHostAllowedAndApplied(t *testing.T) {
	// Phase 7: the network-allowlist is removed from the service; host is a
	// valid mode and passes straight through to the backend (fail-closed host
	// opt-in is enforced at the profile/config layer, not here).
	mkService := func(cfg backend.Config) (*Service, *fakeBackend) {
		fb := &fakeBackend{}
		reg := backend.NewRegistry()
		if err := reg.Register(backend.Factory{
			Name:         "fake",
			Priority:     0,
			Driver:       spec.DriverDocker,
			Capabilities: needAll,
			Available:    func() bool { return true },
			New:          func(backend.Config) (role.Backend, error) { return fb, nil },
		}); err != nil {
			t.Fatal(err)
		}

		return NewService(reg, cfg), fb
	}

	hostSpec := testSpec()
	hostSpec.Isolation.NetworkMode = spec.NetworkHost

	// Host is accepted by default and reaches the backend unchanged.
	svc, fb := mkService(backend.Config{})
	if _, err := svc.Create(context.Background(), hostSpec); err != nil {
		t.Fatalf("host should be allowed: %v", err)
	}
	if !fb.created || fb.createdSpec.Isolation.NetworkMode != spec.NetworkHost {
		t.Fatal("host spec should reach the backend")
	}
}

func TestService_CreateThenDestroy(t *testing.T) {
	fb := &fakeBackend{}
	svc := newTestService(t, fb)
	if _, err := svc.Create(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Destroy(context.Background(), sess.ID()); err != nil {
		t.Fatal(err)
	}
	if fb.destroyed != 1 {
		t.Fatalf("destroyed = %d", fb.destroyed)
	}
}

func TestService_PolicyBoundedResourceDefaults(t *testing.T) {
	// Phase 7: cgroup overrides via config are removed; resources come from
	// profiles. The server-enforced default Isolation must still be bounded
	// (fail-closed, spec.Validate requires > 0) as a profile-independent base.
	reg := backend.NewRegistry()
	svc := NewService(reg, backend.Config{})
	iso := svc.Policy().Isolation

	if iso.NanoCPUs <= 0 {
		t.Fatalf("NanoCPUs = %d, want bounded (> 0)", iso.NanoCPUs)
	}
	if iso.Memory <= 0 || iso.MemorySwap < iso.Memory {
		t.Fatalf("Memory/MemorySwap = %d/%d, want bounded with swap >= memory", iso.Memory, iso.MemorySwap)
	}
	if iso.PidsLimit <= 0 {
		t.Fatalf("PidsLimit = %d, want bounded (> 0)", iso.PidsLimit)
	}
	s := spec.SandboxSpec{Image: "img", Isolation: iso}
	if err := s.Validate(); err != nil {
		t.Fatalf("default isolation must validate: %v", err)
	}
}

// TestService_TraceLogsCarryRequestID asserts the service threads request_id
// from the context onto its trace lines for exec/info/stats (L09).
// TestService_Create_UsesRequestedDriver pins that Create drives ensure with the
// spec's driver: a spec requesting docker selects and caches a docker backend.
func TestService_Create_UsesRequestedDriver(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})
	sp := testSpec()
	sp.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	if !dockerFB.created {
		t.Fatal("docker backend Create not called")
	}
}

// TestService_Create_SelectsSeatbeltOnFreshService pins that a seatbelt spec on
// a fresh service selects the seatbelt backend (driver-aware selection), even
// though docker has a lower priority.
func TestService_Create_SelectsSeatbeltOnFreshService(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker}
	seatbeltFB := &fakeBackend{driver: spec.DriverSeatbelt}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(backend.Factory{
		Name: "seatbelt", Priority: 1, Driver: spec.DriverSeatbelt,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return seatbeltFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})
	sp := testSpec()
	sp.Driver = spec.DriverSeatbelt
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	if !seatbeltFB.created {
		t.Fatal("seatbelt backend Create not called")
	}
	if dockerFB.created {
		t.Fatal("docker backend should not be selected for a seatbelt spec")
	}
}

// TestService_Create_SelectsSeatbeltWithoutIntrospectable pins the bug fix: a
// seatbelt backend that — like the real darwin backend — does NOT implement
// Introspectable (macOS has no cgroups; Info/Stats return
// ErrCapabilityNotSupported, SPEC §6.2 "no stubs") must still be selectable for
// create/exec/destroy. Regression: needAll used to require Introspectable, so
// the darwin backend never passed covers() and was unselectable everywhere.
func TestService_Create_SelectsSeatbeltWithoutIntrospectable(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker}
	seatbeltFB := &fakeBackend{driver: spec.DriverSeatbelt}
	// Real darwin capabilities: Lifecycle + Executable only (Introspectable=false).
	darwinCaps := role.CapabilitySet{Lifecycle: true, Executable: true}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true},
		Available:    func() bool { return true },
		New:          func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(backend.Factory{
		Name: "seatbelt", Priority: 1, Driver: spec.DriverSeatbelt,
		Capabilities: darwinCaps, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return seatbeltFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})
	sp := testSpec()
	sp.Driver = spec.DriverSeatbelt
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatalf("seatbelt (no Introspectable) must be selectable on a fresh service: %v", err)
	}
	if !seatbeltFB.created {
		t.Fatal("seatbelt backend Create not called")
	}
}

// TestService_Create_DriversCoexistAfterDocker pins the multi-backend design:
// once a docker session exists, creating a seatbelt session must SUCCEED (the
// drivers coexist; routing is by session id, not by a single process-wide
// cached backend).
func TestService_Create_DriversCoexistAfterDocker(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker}
	seatbeltFB := &fakeBackend{driver: spec.DriverSeatbelt}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(backend.Factory{
		Name: "seatbelt", Priority: 1, Driver: spec.DriverSeatbelt,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return seatbeltFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})

	dockerSpec := testSpec()
	dockerSpec.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), dockerSpec); err != nil {
		t.Fatal(err)
	}

	// A seatbelt session must succeed even after docker is already in use.
	seatSpec := testSpec()
	seatSpec.Driver = spec.DriverSeatbelt
	if _, err := svc.Create(context.Background(), seatSpec); err != nil {
		t.Fatalf("seatbelt create after docker should succeed: %v", err)
	}
	if !seatbeltFB.created {
		t.Fatal("seatbelt backend Create not called")
	}
}

// TestService_Create_DriversCoexist pins that two concrete drivers (docker and
// seatbelt) can coexist in one Service: creating a session on each succeeds,
// both backends are driven, and both sessions are listable/gettable.
func TestService_Create_DriversCoexist(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker}
	seatbeltFB := &fakeBackend{driver: spec.DriverSeatbelt}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(backend.Factory{
		Name: "seatbelt", Priority: 1, Driver: spec.DriverSeatbelt,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return seatbeltFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})

	dockerID := createSessionID(t, svc, spec.DriverDocker)
	seatbeltID := createSessionID(t, svc, spec.DriverSeatbelt)

	if !dockerFB.created {
		t.Fatal("docker backend Create not called")
	}
	if !seatbeltFB.created {
		t.Fatal("seatbelt backend Create not called")
	}
	if n := len(svc.List(context.Background())); n != 2 {
		t.Fatalf("List len = %d, want 2", n)
	}
	if _, err := svc.Get(dockerID); err != nil {
		t.Fatalf("Get(docker) err = %v", err)
	}
	if _, err := svc.Get(seatbeltID); err != nil {
		t.Fatalf("Get(seatbelt) err = %v", err)
	}
}

// TestService_Exec_RoutesToCorrectBackend pins that Exec routes by session id to
// the backend that owns the session — not to a single process-wide backend.
func TestService_Exec_RoutesToCorrectBackend(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker, execRes: &exec.Result{ExitCode: 0}}
	seatbeltFB := &fakeBackend{driver: spec.DriverSeatbelt, execRes: &exec.Result{ExitCode: 0}}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(backend.Factory{
		Name: "seatbelt", Priority: 1, Driver: spec.DriverSeatbelt,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return seatbeltFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})

	dockerID := createSessionID(t, svc, spec.DriverDocker)
	seatbeltID := createSessionID(t, svc, spec.DriverSeatbelt)

	// Exec on the seatbelt session must reach the seatbelt backend only.
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: seatbeltID}); err != nil {
		t.Fatal(err)
	}
	if seatbeltFB.execReq.SandboxID != seatbeltID {
		t.Fatalf("seatbelt execReq.SandboxID = %q, want %q", seatbeltFB.execReq.SandboxID, seatbeltID)
	}
	if dockerFB.execReq.SandboxID != "" {
		t.Fatalf("docker backend must not receive the seatbelt exec, got %q", dockerFB.execReq.SandboxID)
	}

	// Exec on the docker session must reach the docker backend only.
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: dockerID}); err != nil {
		t.Fatal(err)
	}
	if dockerFB.execReq.SandboxID != dockerID {
		t.Fatalf("docker execReq.SandboxID = %q, want %q", dockerFB.execReq.SandboxID, dockerID)
	}
	if seatbeltFB.execReq.SandboxID != seatbeltID {
		t.Fatalf("seatbelt execReq.SandboxID = %q, must remain %q", seatbeltFB.execReq.SandboxID, seatbeltID)
	}
}

// TestService_Create_AutoUsesCached pins that once a docker backend is cached,
// an auto-driver Create reuses it (auto never triggers a mismatch).
func TestService_Create_AutoUsesCached(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})

	dockerSpec := testSpec()
	dockerSpec.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), dockerSpec); err != nil {
		t.Fatal(err)
	}
	// Auto spec reuses the cached docker backend instead of erroring.
	before := dockerFB.destroyed
	if _, err := svc.Create(context.Background(), testSpec()); err != nil {
		t.Fatalf("auto create should reuse cached backend: %v", err)
	}
	if dockerFB.destroyed != before {
		t.Fatal("cached backend must not be torn down on auto create")
	}
}

// TestService_OpsRouteToSessionBackend pins that Exec/Info/Stats route by the
// session id to the backend that owns the session (not "auto" selection of a
// process-wide backend).
func TestService_OpsRouteToSessionBackend(t *testing.T) {
	reg := backend.NewRegistry()
	dockerFB := &fakeBackend{driver: spec.DriverDocker, info: "info", stats: "stats", execRes: &exec.Result{ExitCode: 0}}
	if err := reg.Register(backend.Factory{
		Name: "docker", Priority: 0, Driver: spec.DriverDocker,
		Capabilities: needAll, Available: func() bool { return true },
		New: func(backend.Config) (role.Backend, error) { return dockerFB, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{})

	id := createSessionID(t, svc, spec.DriverDocker)
	// Exec/Info/Stats on the docker session route to the docker backend.
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: id, Argv: []string{"echo"}}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if v, err := svc.Info(context.Background(), id); err != nil || v != "info" {
		t.Fatalf("info = %v, %v", v, err)
	}
	if v, err := svc.Stats(context.Background(), id); err != nil || v != "stats" {
		t.Fatalf("stats = %v, %v", v, err)
	}
}

func TestService_TraceLogsCarryRequestID(t *testing.T) {
	var buf bytes.Buffer
	l := logrus.New()
	l.SetOutput(&buf)
	l.SetLevel(logrus.TraceLevel)
	l.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})

	reg := backend.NewRegistry()
	if err := reg.Register(backend.Factory{
		Name:         "fake",
		Priority:     0,
		Driver:       spec.DriverDocker,
		Capabilities: needAll,
		Available:    func() bool { return true },
		New:          func(backend.Config) (role.Backend, error) { return &fakeBackend{}, nil },
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(reg, backend.Config{}, WithLogger(logrus.NewEntry(l)))
	ctx := logging.WithRequestID(context.Background(), "req-svc")

	// Create a real session so Exec/Info/Stats have a session id to route by.
	sp := testSpec()
	sp.Driver = spec.DriverDocker
	sess, err := svc.Create(ctx, sp)
	if err != nil {
		t.Fatal(err)
	}
	id := sess.ID()

	if _, err := svc.Exec(ctx, exec.Request{SandboxID: id, Argv: []string{"echo", "hi"}, User: "1000:1000"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Info(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stats(ctx, id); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "req-svc") {
		t.Fatalf("trace output missing request_id:\n%s", out)
	}
	for _, op := range []string{"exec", "info", "stats"} {
		if !strings.Contains(out, op+"=") && !strings.Contains(out, "operation="+op) {
			t.Fatalf("trace output missing %q operation:\n%s", op, out)
		}
	}
}

// longIDs are deterministic 64-hex session ids used to exercise prefix
// resolution: ShortID truncates them to 12 chars. The first two share the same
// first-12-char prefix ("deadbeef1234") but differ at position 13, so that
// prefix is ambiguous; the third has a unique prefix. The 12-char ShortIDs are
// the realistic ids a client echoes from sandbox_create/sandbox_list/sandbox_info.
var longIDs = []string{
	"deadbeef1234000000000000000000000000000000000000000000000000000000",
	"deadbeef1234ffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	"feedf00d9999cccccccccccccccccccccccccccccccccccccccccccccccccccccc",
}

// newPrefixService builds a Service backed by a fakeBackend that yields the
// given deterministic long ids on successive Create calls.
func newPrefixService(t *testing.T, ids []string) (*Service, *fakeBackend) {
	t.Helper()
	fb := &fakeBackend{createIDs: ids, info: "info", stats: "stats", execRes: &exec.Result{ExitCode: 0}}
	reg := backend.NewRegistry()
	if err := reg.Register(backend.Factory{
		Name:         "fake",
		Priority:     0,
		Driver:       spec.DriverDocker,
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true},
		Available:    func() bool { return true },
		New:          func(backend.Config) (role.Backend, error) { return fb, nil },
	}); err != nil {
		t.Fatal(err)
	}

	return NewService(reg, backend.Config{}), fb
}

// TestService_ShortID_RoutesAndPassesFullID pins the core bug fix: a client
// echoes the short (ShortID) id returned by sandbox_create / sandbox_list /
// sandbox_info, so Get/Exec/Info/Stats/Destroy must resolve that prefix to the
// owning runtime AND pass the resolved FULL id down to the backend (docker uses
// the id directly as the container id — a 12-char prefix would be "No such
// container"). On the current code every one of these returns
// ErrSessionNotFound, so this test fails until the fix lands.
func TestService_ShortID_RoutesAndPassesFullID(t *testing.T) {
	svc, fb := newPrefixService(t, []string{longIDs[0]})
	full := longIDs[0]
	short := domainsession.ShortID(full)
	if short == full {
		t.Fatalf("test requires a real truncation: ShortID(%q) = %q", full, short)
	}

	sp := testSpec()
	sp.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}

	// Get resolves the short prefix.
	got, err := svc.Get(short)
	if err != nil {
		t.Fatalf("Get(short) err = %v (want resolved to %q)", err, full)
	}
	if got.ID() != full {
		t.Fatalf("Get(short).ID() = %q, want full %q", got.ID(), full)
	}

	// Exec routes to the backend with the FULL id.
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: short, Argv: []string{"echo"}}); err != nil {
		t.Fatalf("Exec(short) err = %v", err)
	}
	if fb.lastExecID != full {
		t.Fatalf("backend Exec got id %q, want full %q (short prefix %q must be resolved)", fb.lastExecID, full, short)
	}

	// Info routes to the backend with the FULL id.
	if _, err := svc.Info(context.Background(), short); err != nil {
		t.Fatalf("Info(short) err = %v", err)
	}
	if fb.lastInfoID != full {
		t.Fatalf("backend Info got id %q, want full %q", fb.lastInfoID, full)
	}

	// Stats routes to the backend with the FULL id.
	if _, err := svc.Stats(context.Background(), short); err != nil {
		t.Fatalf("Stats(short) err = %v", err)
	}
	if fb.lastStatsID != full {
		t.Fatalf("backend Stats got id %q, want full %q", fb.lastStatsID, full)
	}

	// Destroy of the short id resolves and tears down the session.
	if err := svc.Destroy(context.Background(), short); err != nil {
		t.Fatalf("Destroy(short) err = %v", err)
	}
	if fb.lastDestroyID != full {
		t.Fatalf("backend Destroy got id %q, want full %q", fb.lastDestroyID, full)
	}
	if fb.destroyed != 1 {
		t.Fatalf("backend destroyed = %d, want 1", fb.destroyed)
	}
}

// TestService_ShortID_ExactFullIDFastPathStillWorks pins that resolving a full
// id (the exact fast path, SPEC §6.6) still routes and still passes the full id
// to the backend — the prefix scan must not regress exact lookups.
func TestService_ShortID_ExactFullIDFastPathStillWorks(t *testing.T) {
	svc, fb := newPrefixService(t, []string{longIDs[0]})
	full := longIDs[0]
	sp := testSpec()
	sp.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: full}); err != nil {
		t.Fatalf("Exec(full) err = %v", err)
	}
	if fb.lastExecID != full {
		t.Fatalf("backend Exec got id %q, want %q", fb.lastExecID, full)
	}
	if _, err := svc.Get(full); err != nil {
		t.Fatalf("Get(full) err = %v", err)
	}
}

// TestService_PrefixAmbiguous pins SPEC §6.6: a prefix matching two distinct
// live sessions must be rejected with ErrAmbiguousSessionID rather than
// resolving to an arbitrary one. On the current code the short prefix matches
// nothing in sessionDriver, so Get/Exec/Info/Stats return ErrSessionNotFound —
// the test fails until the fix routes through a prefix-resolving lookup.
func TestService_PrefixAmbiguous(t *testing.T) {
	svc, _ := newPrefixService(t, []string{longIDs[0], longIDs[1]})
	ambiguous := longIDs[0][:12] // shared by both sessions
	sp := testSpec()
	sp.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Get(ambiguous); !errors.Is(err, session.ErrAmbiguousSessionID) {
		t.Fatalf("Get(ambiguous %q) err = %v, want ErrAmbiguousSessionID", ambiguous, err)
	}
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: ambiguous}); !errors.Is(err, session.ErrAmbiguousSessionID) {
		t.Fatalf("Exec(ambiguous %q) err = %v, want ErrAmbiguousSessionID", ambiguous, err)
	}
	if _, err := svc.Info(context.Background(), ambiguous); !errors.Is(err, session.ErrAmbiguousSessionID) {
		t.Fatalf("Info(ambiguous %q) err = %v, want ErrAmbiguousSessionID", ambiguous, err)
	}
	if _, err := svc.Stats(context.Background(), ambiguous); !errors.Is(err, session.ErrAmbiguousSessionID) {
		t.Fatalf("Stats(ambiguous %q) err = %v, want ErrAmbiguousSessionID", ambiguous, err)
	}
	// Destroy must also refuse an ambiguous prefix, not silently destroy one.
	if err := svc.Destroy(context.Background(), ambiguous); !errors.Is(err, session.ErrAmbiguousSessionID) {
		t.Fatalf("Destroy(ambiguous %q) err = %v, want ErrAmbiguousSessionID", ambiguous, err)
	}
}

// TestService_EmptyAndUnknownID pins that an empty id and an unknown prefix are
// both ErrSessionNotFound (never a wildcard match / never a panic), and that
// Destroy is idempotent on unknown ids.
func TestService_EmptyAndUnknownID(t *testing.T) {
	svc, _ := newPrefixService(t, []string{longIDs[0]})
	sp := testSpec()
	sp.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}

	// Empty id.
	if _, err := svc.Get(""); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Get(\"\") err = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: ""}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Exec(\"\") err = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.Info(context.Background(), ""); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Info(\"\") err = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.Stats(context.Background(), ""); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Stats(\"\") err = %v, want ErrSessionNotFound", err)
	}

	// Unknown non-empty prefix.
	if _, err := svc.Get("zzzz"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Get(unknown) err = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.Exec(context.Background(), exec.Request{SandboxID: "zzzz"}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Exec(unknown) err = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.Info(context.Background(), "zzzz"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Info(unknown) err = %v, want ErrSessionNotFound", err)
	}
	if _, err := svc.Stats(context.Background(), "zzzz"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("Stats(unknown) err = %v, want ErrSessionNotFound", err)
	}

	// Destroy is idempotent on unknown and empty ids (no-op, no error).
	if err := svc.Destroy(context.Background(), "zzzz"); err != nil {
		t.Fatalf("Destroy(unknown) err = %v, want nil", err)
	}
	if err := svc.Destroy(context.Background(), ""); err != nil {
		t.Fatalf("Destroy(\"\") err = %v, want nil", err)
	}
}

// TestService_ShortID_DestroyIsIdempotent pins that destroying a short id of a
// live session works, and a repeat destroy of the now-missing short id is a
// silent no-op (X02 idempotency) rather than an error.
func TestService_ShortID_DestroyIsIdempotent(t *testing.T) {
	svc, fb := newPrefixService(t, []string{longIDs[0]})
	full := longIDs[0]
	short := domainsession.ShortID(full)
	sp := testSpec()
	sp.Driver = spec.DriverDocker
	if _, err := svc.Create(context.Background(), sp); err != nil {
		t.Fatal(err)
	}

	if err := svc.Destroy(context.Background(), short); err != nil {
		t.Fatalf("first Destroy(short) err = %v", err)
	}
	if fb.destroyed != 1 {
		t.Fatalf("destroyed = %d, want 1", fb.destroyed)
	}
	if fb.lastDestroyID != full {
		t.Fatalf("backend Destroy got id %q, want full %q", fb.lastDestroyID, full)
	}

	// Second destroy of the same short id is a silent no-op.
	if err := svc.Destroy(context.Background(), short); err != nil {
		t.Fatalf("second Destroy(short) err = %v, want nil", err)
	}
	if fb.destroyed != 1 {
		t.Fatalf("destroyed = %d, want still 1 after idempotent second destroy", fb.destroyed)
	}
}
