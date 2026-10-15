package darwin

// Complementary unit tests for the portable core of the darwin backend. These
// cover the production branches not exercised by backend_test.go / rlimit_test.go
// (the real file-exists predicate, the empty-env path of toEnvSlice, the
// factory.New constructor and the req.Timeout execution branch) so the package
// meets the ≥95% coverage gate. They never weaken or duplicate the assertions
// in the @qa-authored tests.

import (
	"context"
	"reflect"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

func TestFileExistsReal(t *testing.T) {
	if !fileExistsReal("/") {
		t.Fatal("fileExistsReal(/) = false, want true (root always exists)")
	}
	if fileExistsReal("/definitely/not/a/real/path-xyz") {
		t.Fatal("fileExistsReal(nonexistent) = true, want false")
	}
}

func TestToEnvSlice_EmptyReturnsNil(t *testing.T) {
	if got := toEnvSlice(nil); got != nil {
		t.Fatalf("toEnvSlice(nil) = %v, want nil", got)
	}
	if got := toEnvSlice(map[string]string{}); got != nil {
		t.Fatalf("toEnvSlice(empty) = %v, want nil", got)
	}
}

func TestToEnvSlice_SortedKeyValue(t *testing.T) {
	got := toEnvSlice(map[string]string{"b": "2", "a": "1"})
	want := []string{"a=1", "b=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toEnvSlice = %v, want %v", got, want)
	}
}

// TestFactoryNew constructs a backend through the registered factory's New to
// cover the constructor closure (SPEC §6.3).
func TestFactoryNew(t *testing.T) {
	f := factory()
	b, err := f.New(backend.Config{AllowDirs: []string{"/work"}})
	if err != nil {
		t.Fatalf("factory.New: %v", err)
	}
	if b.Name() != "darwin" {
		t.Fatalf("factory backend Name = %q, want darwin", b.Name())
	}
	be, ok := b.(*Backend)
	if !ok {
		t.Fatalf("factory backend = %T, want *Backend", b)
	}
	if !be.Capabilities().Lifecycle || !be.Capabilities().Executable {
		t.Fatal("factory backend must advertise Lifecycle and Executable")
	}
}

// TestExec_WithTimeout covers the req.Timeout>0 context-deadline branch: a fast
// command that finishes before the deadline returns its result normally.
func TestExec_WithTimeout(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{nil, {ExitCode: 0, Stdout: []byte("ok")}}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := b.Exec(context.Background(), id, exec.Request{
		Argv:    []string{"bash", "-c", "echo ok"},
		Timeout: 10,
	})
	if err != nil {
		t.Fatalf("Exec with timeout: %v", err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "ok" {
		t.Fatalf("result = %+v, want exit 0 / stdout ok", res)
	}
}

// compile-time check that factory.New returns a Backend satisfying role.Backend.
var _ role.Backend = (*Backend)(nil)
