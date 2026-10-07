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
	destroyed     int
	execReq       exec.Request
	execRes       *exec.Result
	readData      []byte
	readErr       error
	info          any
	stats         any
	capNotSupport bool
}

func (f *fakeBackend) Name() string  { return "fake" }
func (f *fakeBackend) Priority() int { return 0 }
func (f *fakeBackend) Capabilities() role.CapabilitySet {
	return role.CapabilitySet{Lifecycle: true, Executable: true, Filesystem: true, Introspectable: true}
}

func (f *fakeBackend) Create(_ context.Context, _ spec.SandboxSpec) (string, error) {
	f.created = true
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

func (f *fakeBackend) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	if f.capNotSupport {
		return nil, role.ErrCapabilityNotSupported
	}
	return f.readData, f.readErr
}

func (f *fakeBackend) WriteFile(_ context.Context, _, _ string, _ []byte) error {
	if f.capNotSupport {
		return role.ErrCapabilityNotSupported
	}
	return nil
}

func (f *fakeBackend) CopyIn(_ context.Context, _, _, _ string) error {
	if f.capNotSupport {
		return role.ErrCapabilityNotSupported
	}
	return nil
}

func (f *fakeBackend) CopyOut(_ context.Context, _, _, _ string) error {
	if f.capNotSupport {
		return role.ErrCapabilityNotSupported
	}
	return nil
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
	iso := defaultIsolation()
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
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Filesystem: true, Introspectable: true},
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
	if _, err := svc.ReadFile(context.Background(), "s", "/p"); err == nil {
		t.Fatal("read should fail without backend")
	}
	if err := svc.WriteFile(context.Background(), "s", "/p", []byte("d")); err == nil {
		t.Fatal("write should fail without backend")
	}
	if err := svc.CopyIn(context.Background(), "s", "/h", "/c"); err == nil {
		t.Fatal("copyin should fail without backend")
	}
	if err := svc.CopyOut(context.Background(), "s", "/c", "/h"); err == nil {
		t.Fatal("copyout should fail without backend")
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

func TestService_FilesystemCapabilityNotSupported(t *testing.T) {
	fb := &fakeBackend{capNotSupport: true}
	svc := newTestService(t, fb)
	if _, err := svc.ReadFile(context.Background(), "s", "/p"); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("read err = %v", err)
	}
	if err := svc.WriteFile(context.Background(), "s", "/p", []byte("d")); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("write err = %v", err)
	}
	if err := svc.CopyIn(context.Background(), "s", "/h", "/c"); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("copyin err = %v", err)
	}
	if err := svc.CopyOut(context.Background(), "s", "/c", "/h"); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("copyout err = %v", err)
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

func TestService_ReadWriteCopy(t *testing.T) {
	fb := &fakeBackend{readData: []byte("data")}
	svc := newTestService(t, fb)
	data, err := svc.ReadFile(context.Background(), "s", "/p")
	if err != nil || string(data) != "data" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if err := svc.WriteFile(context.Background(), "s", "/p", []byte("d")); err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyIn(context.Background(), "s", "/h", "/c"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CopyOut(context.Background(), "s", "/c", "/h"); err != nil {
		t.Fatal(err)
	}
}

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
	if pol.Isolation.NetworkMode != spec.NetworkNone {
		t.Fatalf("isolation = %+v", pol.Isolation)
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
