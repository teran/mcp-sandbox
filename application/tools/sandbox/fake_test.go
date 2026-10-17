package sandbox

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
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

// ---------------------------------------------------------------------------
// Driver-registry test doubles
//
// The tools layer tests the buildSpec WIRING (profile -> builder -> spec), not
// the builders themselves (those are covered by the built-in builder tests in
// infrastructure/backend/docker and darwin). To keep the application layer
// architecture-clean (application may depend only on domain), we seed the
// domain `driver.Default` registry with faithful test-double builders here
// instead of importing the infrastructure packages.
// ---------------------------------------------------------------------------

// fakeDockerMount is the docker options mount mirror.
type fakeDockerMount struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}

// fakeNetworkOpts is the docker options network mirror.
type fakeNetworkOpts struct {
	Mode string `yaml:"mode"`
}

// fakeCgroups is the docker options cgroups mirror (raw + friendly units).
type fakeCgroups struct {
	NanoCPUs   int64         `yaml:"nano_cpus,omitempty"`
	Memory     int64         `yaml:"memory,omitempty"`
	MemorySwap int64         `yaml:"memory_swap,omitempty"`
	PidsLimit  int64         `yaml:"pids_limit,omitempty"`
	CPUCores   float64       `yaml:"cpu_cores,omitempty"`
	MemoryGB   int64         `yaml:"memory_gb,omitempty"`
	Ulimits    []spec.Ulimit `yaml:"ulimits,omitempty"`
}

// fakeDockerOpts mirrors the docker builder's option keys.
type fakeDockerOpts struct {
	Image   string            `yaml:"image"`
	Env     map[string]string `yaml:"env,omitempty"`
	Mounts  []fakeDockerMount `yaml:"mounts,omitempty"`
	Network fakeNetworkOpts   `yaml:"network"`
	Cgroups fakeCgroups       `yaml:"cgroups"`
	User    *spec.UIDGID      `yaml:"user,omitempty"`
	Sudo    string            `yaml:"sudo,omitempty"`
}

// fakeSeatbeltOpts mirrors the seatbelt builder's option keys.
type fakeSeatbeltOpts struct {
	SBPL        string            `yaml:"sbpl"`
	MachLookups []string          `yaml:"mach_lookups,omitempty"`
	Sysctls     []string          `yaml:"sysctls,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
}

// setOptions wraps a struct as an opaque driver.Options yaml.Node on a profile.
func setOptions(t *testing.T, p *domainprofile.Profile, v any) {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	p.Options = driver.NewOptions(&n)
}

// dockerOptsFixture returns a fully-populated docker options double matching the
// default profile.
func dockerOptsFixture() fakeDockerOpts {
	return fakeDockerOpts{
		Image:   "docker.io/library/alpine:3.20",
		Network: fakeNetworkOpts{Mode: string(spec.NetworkBridge)},
		Cgroups: fakeCgroups{
			NanoCPUs:   1_000_000_000,
			Memory:     512 * 1024 * 1024,
			MemorySwap: 1024 * 1024 * 1024,
			PidsLimit:  512,
		},
		User: &spec.UIDGID{UID: 1000, GID: 1000},
		Sudo: string(spec.SudoNever),
	}
}

// fakeDockerBuild is the faithful docker builder double: it decodes the opaque
// options and produces the spec the docker builder would (cwd-first mount,
// friendly-unit resources, non-root user, hardening from policy).
func fakeDockerBuild(ctx driver.Context) (spec.SandboxSpec, error) {
	var o fakeDockerOpts
	if ctx.Options.Node() != nil {
		if err := ctx.Options.Node().Decode(&o); err != nil {
			return spec.SandboxSpec{}, err
		}
	}

	pol := ctx.Policy
	nano, mem, swap, pids, ulims := fakeCgroupValues(o.Cgroups)

	iso := pol
	iso.NanoCPUs = nano
	iso.Memory = mem
	iso.MemorySwap = swap
	iso.PidsLimit = pids
	iso.Ulimits = ulims
	iso.NetworkMode = spec.NetworkMode(o.Network.Mode)

	uidgid := pol.User
	if o.User != nil && o.User.UID != 0 {
		uidgid = *o.User
	}

	cwdTarget := ctx.Cwd.Target
	if cwdTarget == "" {
		cwdTarget = ctx.Params.Cwd
	}
	mounts := []spec.Mount{{Source: ctx.Params.Cwd, Target: cwdTarget, ReadOnly: ctx.Cwd.Mode != "rw"}}
	for _, m := range o.Mounts {
		mounts = append(mounts, spec.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	shell := strings.TrimSpace(ctx.Shell)
	if shell == "" {
		shell = "bash"
	}

	return spec.SandboxSpec{
		Driver:     spec.DriverDocker,
		Image:      o.Image,
		WorkingDir: cwdTarget,
		Env:        o.Env,
		Mounts:     mounts,
		Shell:      shell,
		Resources: spec.Resources{
			NanoCPUs:   nano,
			Memory:     mem,
			MemorySwap: swap,
			PidsLimit:  pids,
			Ulimits:    ulims,
		},
		Network:         spec.Network{Mode: iso.NetworkMode},
		UIDGID:          uidgid,
		Sudo:            spec.SudoPolicy(o.Sudo),
		Secrets:         ctx.Params.Secrets,
		ReadOnlyRoot:    true,
		Isolation:       iso,
		ClientSessionID: ctx.Params.ClientSessionID,
	}, nil
}

// fakeCgroupValues applies the friendly-unit cgroup conversion with raw-value
// precedence and MemorySwap defaulting to Memory (mirrors the docker builder's
// resolveResources).
func fakeCgroupValues(cg fakeCgroups) (nano, mem, swap, pids int64, ulims []spec.Ulimit) {
	nano = cg.NanoCPUs
	if nano == 0 && cg.CPUCores > 0 {
		nano = int64(cg.CPUCores * 1_000_000_000)
	}
	mem = cg.Memory
	if mem == 0 && cg.MemoryGB > 0 {
		mem = cg.MemoryGB * (1 << 30)
	}
	swap = cg.MemorySwap
	if mem > 0 && swap == 0 {
		swap = mem
	}
	pids = cg.PidsLimit
	ulims = cg.Ulimits

	return
}

// fakeSeatbeltBuild is the faithful seatbelt builder double: no image, Sandbox
// block from options, Sudo never, resources/UIDGID from policy, only the cwd
// mount.
func fakeSeatbeltBuild(ctx driver.Context) (spec.SandboxSpec, error) {
	var o fakeSeatbeltOpts
	if ctx.Options.Node() != nil {
		if err := ctx.Options.Node().Decode(&o); err != nil {
			return spec.SandboxSpec{}, err
		}
	}
	pol := ctx.Policy

	cwdTarget := ctx.Cwd.Target
	if cwdTarget == "" {
		cwdTarget = ctx.Params.Cwd
	}
	shell := strings.TrimSpace(ctx.Shell)
	if shell == "" {
		shell = "bash"
	}

	return spec.SandboxSpec{
		Driver:     spec.DriverSeatbelt,
		WorkingDir: cwdTarget,
		Env:        o.Env,
		Mounts:     []spec.Mount{{Source: ctx.Params.Cwd, Target: cwdTarget, ReadOnly: ctx.Cwd.Mode != "rw"}},
		Shell:      shell,
		Resources: spec.Resources{
			NanoCPUs:   pol.NanoCPUs,
			Memory:     pol.Memory,
			MemorySwap: pol.MemorySwap,
			PidsLimit:  pol.PidsLimit,
			Ulimits:    pol.Ulimits,
		},
		Network:         spec.Network{Mode: pol.NetworkMode},
		UIDGID:          pol.User,
		Sudo:            spec.SudoNever,
		Secrets:         ctx.Params.Secrets,
		ReadOnlyRoot:    true,
		Isolation:       pol,
		Sandbox:         spec.Sandbox{SBPL: o.SBPL, MachLookups: o.MachLookups, Sysctls: o.Sysctls},
		ClientSessionID: ctx.Params.ClientSessionID,
	}, nil
}

// init seeds the domain driver.Default registry with the docker + seatbelt
// test-double builders. This mirrors the production wiring where the
// infrastructure packages register the real builders at startup; the tools
// tests seed the domain registry directly to stay architecture-clean.
func init() {
	_ = driver.Register(spec.DriverDocker, driver.BuildFunc(fakeDockerBuild))
	_ = driver.Register(spec.DriverSeatbelt, driver.BuildFunc(fakeSeatbeltBuild))
}

// defaultProfile is the fake's built-in operator profile used by the sandbox
// tools under test. It is a valid, complete profile in the driver+options
// schema.
func defaultProfile() *domainprofile.Profile {
	p := &domainprofile.Profile{
		Name:        "default",
		Description: "the default test profile",
		Driver:      spec.DriverDocker,
		Cwd:         domainprofile.CwdConfig{Mode: "ro"},
	}
	setOptionsNoT(p, dockerOptsFixture())

	return p
}

// setOptionsNoT is setOptions without a *testing.T for use in non-test helpers
// (the fixture is static and always valid).
func setOptionsNoT(p *domainprofile.Profile, v any) {
	b, _ := yaml.Marshal(v)
	var n yaml.Node
	_ = yaml.Unmarshal(b, &n)
	p.Options = driver.NewOptions(&n)
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
