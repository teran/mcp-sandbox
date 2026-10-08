package sandbox

import (
	"context"
	"errors"
	"sync"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// fakeService is a configurable Service double used to exercise the tools
// without a real backend.
type fakeService struct {
	mu sync.Mutex

	policy  Policy
	backend string

	createErr   error
	createdSpec spec.SandboxSpec
	session     *domainsession.Session

	getErr error
	list   []*domainsession.Session

	execRes *exec.Result
	execErr error
	execReq exec.Request

	info    any
	infoErr error

	stats    any
	statsErr error

	destroyErr    error
	destroyAllErr error
}

func (f *fakeService) Policy() Policy { return f.policy }

func (f *fakeService) BackendName() string {
	if f.backend != "" {
		return f.backend
	}
	return "docker"
}

func (f *fakeService) Create(_ context.Context, s spec.SandboxSpec) (*domainsession.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdSpec = s
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.session != nil {
		return f.session, nil
	}
	return domainsession.New("sess-1", s), nil
}

func (f *fakeService) Get(id string) (*domainsession.Session, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.session != nil {
		return f.session, nil
	}
	return domainsession.New(id, f.createdSpec), nil
}

func (f *fakeService) List() []*domainsession.Session {
	if f.list != nil {
		return f.list
	}
	return nil
}

func (f *fakeService) Destroy(_ context.Context, _ string) error { return f.destroyErr }

func (f *fakeService) DestroyAll(_ context.Context) error { return f.destroyAllErr }

func (f *fakeService) Exec(_ context.Context, req exec.Request) (*exec.Result, error) {
	f.mu.Lock()
	f.execReq = req
	f.mu.Unlock()
	return f.execRes, f.execErr
}

func (f *fakeService) Info(_ context.Context, _ string) (any, error) {
	return f.info, f.infoErr
}

func (f *fakeService) Stats(_ context.Context, _ string) (any, error) {
	return f.stats, f.statsErr
}

// errFake is a sentinel for error-path tests.
var errFake = errors.New("boom")

// policy returns a server-enforced policy whose Isolation satisfies
// spec.Validate (non-root user, network bridge, bounded resources, tmpfs,
// auto-remove, cap-drop ALL, read-only root, no-new-privileges).
func defaultPolicy() Policy {
	return Policy{
		DefaultImage: "sandbox:latest",
		Backend:      "docker",
		Isolation: spec.Isolation{
			ReadOnlyRootfs:  true,
			CapDrop:         []string{"ALL"},
			NoNewPrivileges: true,
			User:            spec.UIDGID{UID: 1000, GID: 1000},
			PidsLimit:       512,
			Memory:          512 * 1024 * 1024,
			MemorySwap:      1024 * 1024 * 1024,
			NanoCPUs:        1_000_000_000,
			NetworkMode:     spec.NetworkBridge,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}
}

func newFake() *fakeService {
	return &fakeService{policy: defaultPolicy()}
}

func runningSession(id string, s spec.SandboxSpec) *domainsession.Session {
	sess := domainsession.New(id, s)
	sess.MarkRunning()
	return sess
}
