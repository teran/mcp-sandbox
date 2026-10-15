package darwin

// Tests for the portable logic of the darwin / sandbox-exec (Seatbelt) backend
// (SPEC §7.12). The backend is macOS-only in production, but everything here is
// pure Go logic driving the package-local `runner` interface against
// fakeRunner (the test-only stand-in for os/exec). No test touches the real
// sandbox-exec binary, os/exec, the host shell or a live Darwin kernel, so the
// whole suite is portable and runs in CI on any GOOS.

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/teran/mcp-sandbox/domain/progress"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/sbpl"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

// sandboxExec is the fixed /usr/bin/sandbox-exec path the wrapper must use
// (SPEC §7.12).
const sandboxExec = "/usr/bin/sandbox-exec"

// createCommand is the "sleep infinity" analogue the long-lived Create child
// runs under the fixed [<shell>,"-c",cmd] wrapper (SPEC §7.12).
const createCommand = "sleep infinity"

// baseSeatbeltSpec returns a spec valid under spec.Validate (ReadOnlyRootfs,
// CapDrop ALL, no-new-privileges, non-root user, bounded resources, known
// network mode, TmpfsWorkdir, AutoRemove) with the darwin knobs set.
func baseSeatbeltSpec() spec.SandboxSpec {
	return spec.SandboxSpec{
		Driver:     spec.DriverSeatbelt,
		WorkingDir: "/work",
		Env:        map[string]string{"PATH": "/usr/bin:/bin"},
		Isolation: spec.Isolation{
			ReadOnlyRootfs:  true,
			CapDrop:         []string{"ALL"},
			NoNewPrivileges: true,
			Seccomp:         spec.SeccompDefault,
			User:            spec.UIDGID{UID: 1000, GID: 1000},
			PidsLimit:       100,
			Memory:          128 * 1024 * 1024,
			MemorySwap:      128 * 1024 * 1024,
			NanoCPUs:        1_000_000_000,
			NetworkMode:     spec.NetworkBridge,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}
}

// newTestBackend builds a Backend over the given fake runner and options.
func newTestBackend(t *testing.T, r runner, allowDirs []string, opts ...Option) *Backend {
	t.Helper()

	return New(r, allowDirs, opts...)
}

// ---------------------------------------------------------------------------
// Backend identity
// ---------------------------------------------------------------------------

func TestBackendIdentity(t *testing.T) {
	b := newTestBackend(t, newFakeRunner(), nil)
	if b.Name() != "darwin" {
		t.Fatalf("Name = %q, want darwin", b.Name())
	}
	if b.Priority() != 10 {
		t.Fatalf("Priority = %d, want 10", b.Priority())
	}
	if b.Driver() != spec.DriverSeatbelt {
		t.Fatalf("Driver = %q, want seatbelt", b.Driver())
	}
	want := role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: false}
	if got := b.Capabilities(); got != want {
		t.Fatalf("Capabilities = %+v, want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Available (injectable predicates)
// ---------------------------------------------------------------------------

// newAvailableBackend returns a Backend whose available predicates are fully
// injectable via options, so the decision logic is testable without a real OS.
func newAvailableBackend(t *testing.T, goos string, fileExists func(string) bool, euid func() int) *Backend {
	t.Helper()

	return newTestBackend(t, newFakeRunner(), nil,
		WithGOOS(goos),
		WithFileExists(fileExists),
		WithEUID(euid),
	)
}

func alwaysExists(string) bool { return true }

func TestAvailable_AllConditionsMet(t *testing.T) {
	b := newAvailableBackend(t, "darwin", alwaysExists, func() int { return 1000 })
	if !b.Available() {
		t.Fatal("Available() = false, want true (darwin + sandbox-exec + non-root)")
	}
}

func TestAvailable_NotDarwin(t *testing.T) {
	b := newAvailableBackend(t, "linux", alwaysExists, func() int { return 1000 })
	if b.Available() {
		t.Fatal("Available() = true on non-darwin GOOS")
	}
}

func TestAvailable_MissingSandboxExec(t *testing.T) {
	b := newAvailableBackend(t, "darwin", func(string) bool { return false }, func() int { return 1000 })
	if b.Available() {
		t.Fatal("Available() = true when /usr/bin/sandbox-exec is missing")
	}
}

func TestAvailable_RootDenied(t *testing.T) {
	b := newAvailableBackend(t, "darwin", alwaysExists, func() int { return 0 })
	if b.Available() {
		t.Fatal("Available() = true when euid == 0 (self-sandbox refuses root)")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle.Create
// ---------------------------------------------------------------------------

func TestCreate_FixedWrapper(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"}, WithScratchDir("/scratch"))
	s := baseSeatbeltSpec()
	s.Shell = "bash"

	id, err := b.Create(context.Background(), s)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty id")
	}
	if fake.startCount() != 1 {
		t.Fatalf("Start called %d times, want 1", fake.startCount())
	}
	start := fake.recordedStarts()[0]

	// The SBPL in argv must be exactly what sbpl.Generate produces for the same
	// spec/allowDirs/scratchDir (Generate linkage).
	wantSBPL, err := sbpl.Generate(s, []string{"/work"}, "/scratch")
	if err != nil {
		t.Fatalf("sbpl.Generate: %v", err)
	}
	wantArgv := []string{sandboxExec, "-p", wantSBPL, "bash", "-c", createCommand}
	if !reflect.DeepEqual(start.Argv, wantArgv) {
		t.Fatalf("argv = %q\nwant %q", start.Argv, wantArgv)
	}
}

func TestCreate_DefaultShellIsBash(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})
	s := baseSeatbeltSpec()
	s.Shell = "" // default -> bash

	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	argv := fake.recordedStarts()[0].Argv
	if len(argv) < 4 || argv[3] != "bash" {
		t.Fatalf("argv[3] = %q, want bash (default shell)", argv[3])
	}
}

func TestCreate_CustomShell(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})
	s := baseSeatbeltSpec()
	s.Shell = "zsh"

	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	argv := fake.recordedStarts()[0].Argv
	if len(argv) < 4 || argv[3] != "zsh" {
		t.Fatalf("argv[3] = %q, want zsh", argv[3])
	}
}

func TestCreate_PassesEnvDirRlimits(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})
	s := baseSeatbeltSpec()
	s.Env = map[string]string{"A": "1", "B": "2"}
	s.WorkingDir = "/work"
	s.Resources.Ulimits = []spec.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}}

	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	start := fake.recordedStarts()[0]

	if start.Dir != "/work" {
		t.Fatalf("Dir = %q, want /work", start.Dir)
	}
	if !contains(start.Env, "A=1") || !contains(start.Env, "B=2") {
		t.Fatalf("Env = %v, want to contain A=1 and B=2", start.Env)
	}
	// The env values must never contain a stray "=" or leaked secrets.
	for _, kv := range start.Env {
		if strings.Count(kv, "=") != 1 {
			t.Fatalf("Env entry %q is not a literal KEY=VALUE", kv)
		}
	}
	wantRlimits := []Rlimit{{Resource: int(syscall.RLIMIT_NOFILE), Soft: 1024, Hard: 2048}}
	if !reflect.DeepEqual(start.Rlimits, wantRlimits) {
		t.Fatalf("Rlimits = %+v, want %+v", start.Rlimits, wantRlimits)
	}
}

// TestCreate_RlimitsExcludeNonMappable pins that only setrlimit-mappable
// ulimits reach the child: NanoCPUs/Memory/PidsLimit (no cgroups on macOS) and
// unsupported ulimit names are never in the Rlimits slice (SPEC §7.12).
func TestCreate_RlimitsExcludeNonMappable(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})
	s := baseSeatbeltSpec()
	s.Isolation.NanoCPUs = 2_000_000_000
	s.Isolation.Memory = 256 * 1024 * 1024
	s.Isolation.PidsLimit = 200
	s.Resources.Ulimits = []spec.Ulimit{{Name: "nofile", Soft: 64, Hard: 64}}

	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rlimits := fake.recordedStarts()[0].Rlimits
	if len(rlimits) != 1 {
		t.Fatalf("Rlimits len = %d, want 1 (only nofile mapped): %+v", len(rlimits), rlimits)
	}
	if rlimits[0].Resource != int(syscall.RLIMIT_NOFILE) {
		t.Fatalf("Rlimits[0] = %+v, want RLIMIT_NOFILE", rlimits[0])
	}
}

// TestCreate_SecretsNotInArgvOrEnv pins S02: a secret value must never appear
// in the child argv or env.
func TestCreate_SecretsNotInArgvOrEnv(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})
	s := baseSeatbeltSpec()
	secretValue := "super-secret-token-abc123"
	s.Secrets = []spec.SecretRef{{Name: "TOKEN", Value: secretValue}}

	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	start := fake.recordedStarts()[0]
	for _, a := range start.Argv {
		if strings.Contains(a, secretValue) {
			t.Fatalf("secret value leaked into argv element %q", a)
		}
	}
	for _, e := range start.Env {
		if strings.Contains(e, secretValue) {
			t.Fatalf("secret value leaked into env element %q", e)
		}
	}
}

// TestCreate_PropagatesSBPLGenerationError pins fail-closed: a hostile path
// that sbpl.Generate rejects aborts Create before any child is spawned.
func TestCreate_PropagatesSBPLGenerationError(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})
	s := baseSeatbeltSpec()
	// ';' is an SBPL metacharacter that sbpl.Generate rejects (S11-analog).
	s.WorkingDir = "/work;oops"

	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("Create with hostile WorkingDir should error")
	}
	if fake.startCount() != 0 {
		t.Fatalf("Start called %d times, want 0 (fail-closed before spawn)", fake.startCount())
	}
}

// TestCreate_StartError surfaces the runner error.
func TestCreate_StartError(t *testing.T) {
	fake := newFakeRunner()
	fake.startErr = errors.New("spawn failed")
	b := newTestBackend(t, fake, []string{"/work"})

	if _, err := b.Create(context.Background(), baseSeatbeltSpec()); err == nil {
		t.Fatal("Create should propagate runner.Start error")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle.Destroy
// ---------------------------------------------------------------------------

func TestDestroy_KillGroupSIGKILLAndWait(t *testing.T) {
	fake := newFakeRunner()
	// The Create child's Wait returns a canned result.
	fake.results = []*Result{{ExitCode: 0}}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	p := fake.proc(1)
	if !p.hasSignal(syscall.SIGKILL) {
		t.Fatalf("Create child was not SIGKILLed; kills = %v", p.killSignals())
	}
	if p.waitCount() < 1 {
		t.Fatal("Destroy did not Wait on the child")
	}
}

func TestDestroy_IdempotentOnESRCH(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{{ExitCode: 0}}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// First destroy kills the child.
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("first Destroy: %v", err)
	}
	// Second destroy hits ESRCH (already dead) and must be a no-op (nil).
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("second Destroy (ESRCH) should be nil no-op, got %v", err)
	}
	p := fake.proc(1)
	sigs := p.killSignals()
	if len(sigs) != 2 {
		t.Fatalf("kills = %v, want 2 kill attempts", sigs)
	}
	for _, s := range sigs {
		if s != syscall.SIGKILL {
			t.Fatalf("kill signal = %v, want SIGKILL", s)
		}
	}
}

// TestDestroy_UnknownIDIsNoOp pins that destroying a session id with no backing
// child is a no-op (mirrors docker's idempotent Destroy).
func TestDestroy_UnknownIDIsNoOp(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})

	if err := b.Destroy(context.Background(), "no-such-id"); err != nil {
		t.Fatalf("Destroy(unknown) should be nil no-op, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Executable.Exec
// ---------------------------------------------------------------------------

func TestExec_SpawnsFreshChildPerCall(t *testing.T) {
	fake := newFakeRunner()
	// create child (proc1) has no Wait; two exec children (proc2, proc3).
	fake.results = []*Result{nil, {ExitCode: 0, Stdout: []byte("one")}, {ExitCode: 3, Stdout: []byte("two")}}
	b := newTestBackend(t, fake, []string{"/work"}, WithScratchDir("/scratch"))

	// Capture the SBPL the Create child used.
	s := baseSeatbeltSpec()
	id, err := b.Create(context.Background(), s)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	createSBPL := fake.recordedStarts()[0].Argv[2]

	req1 := exec.Request{Argv: []string{"bash", "-c", "echo one"}}
	if _, err := b.Exec(context.Background(), id, req1); err != nil {
		t.Fatalf("Exec #1: %v", err)
	}
	req2 := exec.Request{Argv: []string{"bash", "-c", "echo two"}}
	res2, err := b.Exec(context.Background(), id, req2)
	if err != nil {
		t.Fatalf("Exec #2: %v", err)
	}

	if fake.startCount() != 3 {
		t.Fatalf("Start called %d times, want 3 (1 create + 2 exec)", fake.startCount())
	}
	starts := fake.recordedStarts()
	// Each exec is a fresh child wrapped as [sandbox-exec, -p, <same SBPL>, <req argv...>].
	for _, n := range []int{2, 3} {
		argv := starts[n-1].Argv
		if argv[0] != sandboxExec || argv[1] != "-p" || argv[2] != createSBPL {
			t.Fatalf("exec child %d argv wrapper = %q, want [%s -p <sbpl> ...]", n, argv[:3], sandboxExec)
		}
	}
	// Result #2 mapped from the fake child's Result.
	if res2.ExitCode != 3 || string(res2.Stdout) != "two" {
		t.Fatalf("Exec #2 result = %+v, want exit 3 / stdout two", res2)
	}
}

func TestExec_CollectsOutput(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{
		nil, // create child unused
		{ExitCode: 7, Stdout: []byte("out"), Stderr: []byte("err")},
	}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "cmd"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 7 || string(res.Stdout) != "out" || string(res.Stderr) != "err" {
		t.Fatalf("result = %+v, want exit 7 / out / err", res)
	}
	// The exec child must have had output sinks wired so output is collected.
	start := fake.recordedStarts()[1]
	if start.Stdout == nil || start.Stderr == nil {
		t.Fatalf("exec child Stdout/Stderr not wired: %+v", start)
	}
}

func TestExec_UnknownSession(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})

	if _, err := b.Exec(context.Background(), "nope", exec.Request{Argv: []string{"bash", "-c", "x"}}); err == nil {
		t.Fatal("Exec for an unknown session id should error")
	}
}

// TestExec_TimeoutKillsGroupAndEmitsCancelled pins the cancellation contract:
// on ctx cancel (the req.Timeout path becomes a context deadline), the fresh
// child is SIGKILLed as a process group and a terminal "cancelled" progress
// event is emitted.
func TestExec_TimeoutKillsGroupAndEmitsCancelled(t *testing.T) {
	fake := newFakeRunner()
	// create child: block=false; exec child: block until ctx cancelled.
	fake.blocks = []bool{false, true}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var mu sync.Mutex
	var events []progress.Event
	sink := func(_ context.Context, e progress.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}
	execCtx, cancel := context.WithCancel(progress.WithSink(context.Background(), sink))

	done := make(chan struct{})
	var res *exec.Result
	var execErr error
	go func() {
		res, execErr = b.Exec(execCtx, id, exec.Request{Argv: []string{"bash", "-c", "sleep 999"}})
		close(done)
	}()

	// Grab the exec child (2nd start) and wait until its Wait is blocking, then
	// cancel — deterministic, no sleeps.
	p := fake.waitForStart(t, 2)
	<-p.started
	cancel()
	<-done

	if !p.hasSignal(syscall.SIGKILL) {
		t.Fatalf("exec child was not SIGKILLed on cancel; kills = %v", p.killSignals())
	}
	if !hasCancelledEvent(events) {
		t.Fatalf("no cancelled progress event emitted; events = %+v", events)
	}
	if execErr == nil {
		t.Fatal("Exec on cancellation should return an error")
	}
	_ = res
}

func hasCancelledEvent(events []progress.Event) bool {
	for _, e := range events {
		if strings.Contains(e.Message, "cancelled") {
			return true
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Introspectable
// ---------------------------------------------------------------------------

func TestIntrospectable_NotSupported(t *testing.T) {
	b := newTestBackend(t, newFakeRunner(), []string{"/work"})

	if _, err := b.Info(context.Background(), "id"); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("Info err = %v, want ErrCapabilityNotSupported", err)
	}
	if _, err := b.Stats(context.Background(), "id"); !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("Stats err = %v, want ErrCapabilityNotSupported", err)
	}
}

// ---------------------------------------------------------------------------
// Registration (SPEC §6.3) — via a fresh registry so the shared Default is
// untouched.
// ---------------------------------------------------------------------------

func TestFactoryMetadata(t *testing.T) {
	f := factory()
	if f.Name != "darwin" {
		t.Fatalf("factory Name = %q, want darwin", f.Name)
	}
	if f.Priority != 10 {
		t.Fatalf("factory Priority = %d, want 10", f.Priority)
	}
	if f.Driver != spec.DriverSeatbelt {
		t.Fatalf("factory Driver = %q, want seatbelt", f.Driver)
	}
	want := role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: false}
	if f.Capabilities != want {
		t.Fatalf("factory Capabilities = %+v, want %+v", f.Capabilities, want)
	}
	if f.Available == nil {
		t.Fatal("factory Available must be non-nil")
	}
	if f.New == nil {
		t.Fatal("factory New must be non-nil")
	}
	// The real (non-injectable) predicate must be callable without panicking.
	_ = f.Available()
}

func TestSelect_DarwinBackend(t *testing.T) {
	reg := backend.NewRegistry()
	f := factory()
	// Override availability so selection is deterministic on any GOOS (the real
	// predicate is platform-dependent).
	f.Available = func() bool { return true }
	if err := reg.Register(f); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := reg.Select(context.Background(), role.CapabilitySet{Lifecycle: true, Executable: true}, spec.DriverSeatbelt)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Name != "darwin" {
		t.Fatalf("Select = %q, want darwin", got.Name)
	}
}

func TestSelect_DarwinExposesNoIntrospect(t *testing.T) {
	reg := backend.NewRegistry()
	f := factory()
	f.Available = func() bool { return true }
	if err := reg.Register(f); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := reg.Select(context.Background(), role.CapabilitySet{Introspectable: true}, spec.DriverSeatbelt)
	if !errors.Is(err, backend.ErrNoAvailableBackend) {
		t.Fatalf("Select(introspectable) err = %v, want ErrNoAvailableBackend", err)
	}
}

// contains reports whether hay contains needle.
func contains(hay []string, needle string) bool {
	return slices.Contains(hay, needle)
}
