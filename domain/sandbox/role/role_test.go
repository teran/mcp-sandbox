package role

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

type fakeBackend struct {
	name     string
	priority int
	caps     CapabilitySet
}

func (f *fakeBackend) Name() string                { return f.name }
func (f *fakeBackend) Priority() int               { return f.priority }
func (f *fakeBackend) Capabilities() CapabilitySet { return f.caps }

func TestBackendInterface(t *testing.T) {
	var b Backend = &fakeBackend{name: "docker", priority: 0, caps: CapabilitySet{
		Lifecycle: true, Executable: true, Filesystem: true, Introspectable: true,
	}}
	if b.Name() != "docker" {
		t.Fatalf("Name() = %q", b.Name())
	}
	if b.Priority() != 0 {
		t.Fatalf("Priority() = %d", b.Priority())
	}
	caps := b.Capabilities()
	if !caps.Lifecycle || !caps.Executable || !caps.Filesystem || !caps.Introspectable {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

// Compile-time checks that a backend implementing the roles satisfies the
// role interfaces.
type fullBackend struct{ fakeBackend }

func (f *fullBackend) Create(context.Context, spec.SandboxSpec) (string, error) { return "id", nil }
func (f *fullBackend) Destroy(context.Context, string) error                    { return nil }
func (f *fullBackend) Exec(context.Context, string, exec.Request) (*exec.Result, error) {
	return &exec.Result{}, nil
}
func (f *fullBackend) ReadFile(context.Context, string, string) ([]byte, error) { return nil, nil }
func (f *fullBackend) WriteFile(context.Context, string, string, []byte) error  { return nil }
func (f *fullBackend) CopyIn(context.Context, string, string, string) error     { return nil }
func (f *fullBackend) CopyOut(context.Context, string, string, string) error    { return nil }
func (f *fullBackend) Info(context.Context, string) (any, error)                { return nil, nil }
func (f *fullBackend) Stats(context.Context, string) (any, error)               { return nil, nil }

func TestRoleInterfaces(_ *testing.T) {
	b := &fullBackend{}
	var _ Lifecycle = b
	var _ Executable = b
	var _ Filesystem = b
	var _ Introspectable = b
}

func TestErrCapabilityNotSupported(t *testing.T) {
	if !errors.Is(ErrCapabilityNotSupported, ErrCapabilityNotSupported) {
		t.Fatal("sentinel must compare equal to itself")
	}
	if ErrCapabilityNotSupported.Error() == "" {
		t.Fatal("error message must not be empty")
	}
}
