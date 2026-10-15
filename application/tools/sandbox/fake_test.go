package sandbox

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
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

	// sessions, when set, backs Get/List with prefix resolution mirroring the
	// real session manager (SPEC §6.6): a unique prefix resolves to the full
	// id, an ambiguous prefix errors, an unknown one reports not-found.
	sessions map[string]*domainsession.Session

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
	if f.sessions != nil {
		full, err := resolveFakePrefix(f.sessions, id)
		if err != nil {
			return nil, err
		}

		return f.sessions[full], nil
	}
	if f.session != nil {
		return f.session, nil
	}

	return domainsession.New(id, f.createdSpec), nil
}

func (f *fakeService) List(_ context.Context) []*domainsession.Session {
	if f.list != nil {
		return f.list
	}
	if f.sessions != nil {
		out := make([]*domainsession.Session, 0, len(f.sessions))
		for _, s := range f.sessions {
			out = append(out, s)
		}

		return out
	}

	return nil
}

// resolveFakePrefix resolves a caller-supplied id to the full session id,
// mirroring Manager.resolveID (SPEC §6.6): an exact full id wins, otherwise a
// unique prefix is scanned for. Zero matches and empty input report not-found;
// more than one match reports ambiguous.
func resolveFakePrefix(sessions map[string]*domainsession.Session, prefix string) (string, error) {
	if prefix == "" {
		return "", errFakeNotFound
	}
	if _, ok := sessions[prefix]; ok {
		return prefix, nil
	}
	var match string
	for id := range sessions {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if match != "" {
			return "", errFakeAmbiguous
		}
		match = id
	}
	if match == "" {
		return "", errFakeNotFound
	}

	return match, nil
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

// errFakeNotFound / errFakeAmbiguous are the fake's prefix-resolution errors,
// mirroring the real manager (ErrSessionNotFound / ErrAmbiguousSessionID).
var (
	errFakeNotFound  = errors.New("fake: session not found")
	errFakeAmbiguous = errors.New("fake: ambiguous session id")
)

// policy returns a server-enforced policy whose Isolation satisfies
// spec.Validate (non-root user, network bridge, bounded resources, tmpfs,
// auto-remove, cap-drop ALL, read-only root, no-new-privileges).
func defaultPolicy() Policy {
	return Policy{
		DefaultImage: "sandbox:latest",
		Backend:      "docker",
		Profiles: fakeResolver{profiles: map[string]*domainprofile.Profile{
			"default": defaultProfile(),
		}},
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

// defaultProfile is the fake's built-in operator profile used by the sandbox
// tools under test. It is a valid, complete profile.
func defaultProfile() *domainprofile.Profile {
	return &domainprofile.Profile{
		Name:        "default",
		Description: "the default test profile",
		Driver:      spec.DriverDocker,
		Cwd:         domainprofile.CwdConfig{Mode: "ro"},
		Docker: &domainprofile.DockerProfile{
			Image:   "docker.io/library/alpine:3.20",
			Env:     map[string]string{},
			Network: domainprofile.Network{Mode: spec.NetworkBridge},
			Cgroups: domainprofile.Cgroups{
				NanoCPUs:   1_000_000_000,
				Memory:     512 * 1024 * 1024,
				MemorySwap: 1024 * 1024 * 1024,
				PidsLimit:  512,
			},
			User: &spec.UIDGID{UID: 1000, GID: 1000},
			Sudo: spec.SudoNever,
		},
	}
}

// fakeResolver is a ProfileResolver double keyed by profile name. It resolves
// on demand (new API): missing names yield ErrProfileNotFound; an injected
// err simulates a fail-closed store read error.
type fakeResolver struct {
	profiles map[string]*domainprofile.Profile
	err      error
}

func (f fakeResolver) Get(name string) (*domainprofile.Profile, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.profiles[name]
	if !ok {
		return nil, domainprofile.ErrProfileNotFound
	}

	return p, nil
}

func newFake() *fakeService {
	return &fakeService{policy: defaultPolicy()}
}

func runningSession(id string, s spec.SandboxSpec) *domainsession.Session {
	sess := domainsession.New(id, s)
	sess.MarkRunning()

	return sess
}
