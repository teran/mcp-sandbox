package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/application/session"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
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
}

func (f *fakeBackend) Name() string  { return "fake" }
func (f *fakeBackend) Priority() int { return 0 }
func (f *fakeBackend) Capabilities() role.CapabilitySet {
	return role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true}
}

func (f *fakeBackend) Create(_ context.Context, s spec.SandboxSpec) (string, error) {
	f.created = true
	f.createdSpec = s
	return "cid-1", nil
}

func (f *fakeBackend) Destroy(_ context.Context, _ string) error { f.destroyed++; return nil }

func (f *fakeBackend) Exec(_ context.Context, _ string, req exec.Request) (*exec.Result, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
	f.execReq = req
	return f.execRes, nil
}

func (f *fakeBackend) Info(_ context.Context, _ string) (any, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
	return f.info, nil
}

func (f *fakeBackend) Stats(_ context.Context, _ string) (any, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
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
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true},
		Available:    func() bool { return true },
		New:          func(backend.Config) (role.Backend, error) { return fb, nil },
	}); err != nil {
		t.Fatal(err)
	}
	return NewService(reg, backend.Config{})
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
	if n := svc.List(); n != nil {
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
	if len(svc.List()) != 1 {
		t.Fatalf("List len = %d", len(svc.List()))
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
	res, err := svc.Exec(context.Background(), exec.Request{SandboxID: "s", Argv: []string{"echo", "hi"}, User: "1000:1000"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "hi" {
		t.Fatalf("result = %+v", res)
	}
	if fb.execReq.SandboxID != "s" || fb.execReq.User != "1000:1000" {
		t.Fatalf("execReq = %+v", fb.execReq)
	}
}

func TestService_ExecCapabilityNotSupported(t *testing.T) {
	fb := &fakeBackend{capNotSupport: true}
	svc := newTestService(t, fb)
	if _, err := svc.Exec(context.Background(), exec.Request{}); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestService_IntrospectCapabilityNotSupported(t *testing.T) {
	fb := &fakeBackend{capNotSupport: true}
	svc := newTestService(t, fb)
	if _, err := svc.Info(context.Background(), "s"); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("info err = %v", err)
	}
	if _, err := svc.Stats(context.Background(), "s"); !errors.Is(err, role.ErrCapabilityNotSupported) {
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

func TestService_Introspect(t *testing.T) {
	fb := &fakeBackend{info: "info", stats: "stats"}
	svc := newTestService(t, fb)
	if v, err := svc.Info(context.Background(), "s"); err != nil || v != "info" {
		t.Fatalf("info = %v, %v", v, err)
	}
	if v, err := svc.Stats(context.Background(), "s"); err != nil || v != "stats" {
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

func TestService_CreateNetworkModeAllowlist(t *testing.T) {
	mkService := func(cfg backend.Config) (*Service, *fakeBackend) {
		fb := &fakeBackend{}
		reg := backend.NewRegistry()
		if err := reg.Register(backend.Factory{
			Name:         "fake",
			Priority:     0,
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

	// Default config (no allowlist → bridge only) rejects host.
	svc, _ := mkService(backend.Config{})
	if _, err := svc.Create(context.Background(), hostSpec); err == nil {
		t.Fatal("expected host to be rejected by default")
	}

	// Config listing host passes the host spec through to the backend.
	svc2, fb2 := mkService(backend.Config{AllowNetworkModes: []string{"bridge", "host"}})
	if _, err := svc2.Create(context.Background(), hostSpec); err != nil {
		t.Fatalf("host should be allowed when listed: %v", err)
	}
	if !fb2.created || fb2.createdSpec.Isolation.NetworkMode != spec.NetworkHost {
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

func TestService_PolicyCgroupResourceOverrides(t *testing.T) {
	reg := backend.NewRegistry()
	svc := NewService(reg, backend.Config{CgroupCPULimitCores: 2, CgroupMemoryLimitGB: 8})
	iso := svc.Policy().Isolation

	if iso.NanoCPUs != 2_000_000_000 {
		t.Fatalf("NanoCPUs = %d, want 2_000_000_000", iso.NanoCPUs)
	}
	wantMem := int64(8) * 1024 * 1024 * 1024
	if iso.Memory != wantMem {
		t.Fatalf("Memory = %d, want %d", iso.Memory, wantMem)
	}
	if iso.MemorySwap != wantMem {
		t.Fatalf("MemorySwap = %d, want %d (swap disabled)", iso.MemorySwap, wantMem)
	}
}

func TestService_PolicyCgroupResourceDefaults(t *testing.T) {
	reg := backend.NewRegistry()
	svc := NewService(reg, backend.Config{})
	iso := svc.Policy().Isolation

	if iso.NanoCPUs != 1_000_000_000 {
		t.Fatalf("NanoCPUs = %d, want 1_000_000_000 (default)", iso.NanoCPUs)
	}
	defaultMem := int64(4) * 1024 * 1024 * 1024
	if iso.Memory != defaultMem || iso.MemorySwap != defaultMem {
		t.Fatalf("Memory/MemorySwap = %d/%d, want default %d (swap disabled)", iso.Memory, iso.MemorySwap, defaultMem)
	}
}

func TestService_PolicyCgroupCPUOnlyOverrides(t *testing.T) {
	reg := backend.NewRegistry()
	svc := NewService(reg, backend.Config{CgroupCPULimitCores: 4})
	iso := svc.Policy().Isolation

	if iso.NanoCPUs != 4_000_000_000 {
		t.Fatalf("NanoCPUs = %d, want 4_000_000_000", iso.NanoCPUs)
	}
	defaultMem := int64(4) * 1024 * 1024 * 1024
	if iso.Memory != defaultMem || iso.MemorySwap != defaultMem {
		t.Fatalf("memory should keep defaults, got %d/%d", iso.Memory, iso.MemorySwap)
	}
}
