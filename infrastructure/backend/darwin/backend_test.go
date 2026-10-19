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
	"time"

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

// createCommand is the BSD-portable long sleep the long-lived Create child runs
// under the fixed [<shell>,"-c",cmd] wrapper (SPEC §7.12). macOS BSD /bin/sleep
// does not accept the GNU "infinity" argument, so the command uses the largest
// 31-bit second value (2147483647) — a ~68-year sleep that is effectively
// forever, but accepted by every BSD sleep.
const createCommand = "sleep 2147483647"

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

// TestCreate_ArgvEndsWithBSDSleepCmd pins the BSD-portability fix: the Create
// child must run "sleep 2147483647" (BSD-accepted) under the fixed
// [<shell>,"-c",cmd] wrapper, not the GNU-ism "sleep infinity" (SPEC §7.12).
func TestCreate_ArgvEndsWithBSDSleepCmd(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})

	if _, err := b.Create(context.Background(), baseSeatbeltSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	argv := fake.recordedStarts()[0].Argv
	// argv = [sandbox-exec, -p, <SBPL>, <shell tokens...>, -c, <sleep cmd>].
	if len(argv) < 2 {
		t.Fatalf("Create argv too short: %q", argv)
	}
	tail := argv[len(argv)-2:]
	want := []string{"-c", "sleep 2147483647"}
	if !slices.Equal(tail, want) {
		t.Fatalf("Create argv tail = %q, want %q (BSD-portable long sleep)", tail, want)
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

// TestDestroy_GracefulTermAndWait pins the graceful Destroy contract (TODO
// R-6): the long-lived Create child is first sent SIGTERM so it can flush/clean
// up, and since it honours SIGTERM it exits within the grace window — so NO
// SIGKILL is needed. Destroy still waits on the child.
func TestDestroy_GracefulTermAndWait(t *testing.T) {
	fake := newFakeRunner()
	// Create child: cooperative on SIGTERM (exits within grace), canned result.
	fake.results = []*Result{{ExitCode: 0}}
	fake.termExits = []bool{true}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	p := fake.proc(1)
	if !p.hasSignal(syscall.SIGTERM) {
		t.Fatalf("Create child was not sent SIGTERM; kills = %v", p.killSignals())
	}
	if p.hasSignal(syscall.SIGKILL) {
		t.Fatalf("Create child was SIGKILLed after a graceful SIGTERM exit; kills = %v", p.killSignals())
	}
	if p.waitCount() < 1 {
		t.Fatal("Destroy did not Wait on the child")
	}
}

// TestDestroy_IdempotentOnESRCH pins that Destroy stays idempotent (X02) under
// the graceful signal sequence: a second destroy of an already-dead (cooperative)
// child hits ESRCH on the SIGTERM and must be a no-op (nil).
func TestDestroy_IdempotentOnESRCH(t *testing.T) {
	fake := newFakeRunner()
	// Create child: cooperative on SIGTERM so the first SIGTERM kills it; a
	// second SIGTERM then returns ESRCH (already dead).
	fake.results = []*Result{{ExitCode: 0}}
	fake.termExits = []bool{true}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// First destroy gracefully SIGTERMs the child.
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
		if s != syscall.SIGTERM {
			t.Fatalf("kill signal = %v, want SIGTERM (graceful)", s)
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

// TestExec_TimeoutKillsGroupAndEmitsCancelled pins the cancellation contract
// under the graceful signal sequence (TODO R-6): on ctx cancel (the req.Timeout
// path becomes a context deadline), the fresh child's process group is first
// sent SIGTERM so it can flush, then escalated to SIGKILL only if it ignores the
// graceful signal, and a terminal "cancelled" progress event is emitted. The
// exec child here is cooperative on SIGTERM, so it exits within the grace window
// and NO SIGKILL is required — pinning that the graceful path is used rather than
// an immediate SIGKILL.
func TestExec_TimeoutKillsGroupAndEmitsCancelled(t *testing.T) {
	fake := newFakeRunner()
	// create child: block=false; exec child: block until ctx cancelled, and
	// cooperative on SIGTERM (exits within grace).
	fake.blocks = []bool{false, true}
	fake.termExits = []bool{false, true}
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

	if !p.hasSignal(syscall.SIGTERM) {
		t.Fatalf("exec child was not sent SIGTERM on cancel; kills = %v", p.killSignals())
	}
	if p.hasSignal(syscall.SIGKILL) {
		t.Fatalf("exec child was SIGKILLed despite a graceful SIGTERM exit; kills = %v", p.killSignals())
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

// TestSlug pins the backend's namespacing slug (seatbelt driver -> "seatbelt"),
// which the service uses to compose `seatbelt:<internal>` session ids.
func TestSlug(t *testing.T) {
	b := newTestBackend(t, newFakeRunner(), nil)
	if b.Slug() != "seatbelt" {
		t.Fatalf("Slug() = %q, want seatbelt", b.Slug())
	}
}

// TestDestroy_StripsNamespacedID pins that Destroy strips the "seatbelt:" slug
// and gracefully SIGTERMs the child owned by the raw darwin session id.
func TestDestroy_StripsNamespacedID(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{{ExitCode: 0}}
	fake.termExits = []bool{true}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), "seatbelt:"+id); err != nil {
		t.Fatalf("Destroy(namespaced): %v", err)
	}
	// The slug was stripped and the raw session's Create child was SIGTERMed.
	p := fake.proc(1)
	if !p.hasSignal(syscall.SIGTERM) {
		t.Fatalf("Create child was not SIGTERMed; kills = %v", p.killSignals())
	}
	if p.hasSignal(syscall.SIGKILL) {
		t.Fatalf("Create child was SIGKILLed on graceful exit; kills = %v", p.killSignals())
	}
}

// TestExec_StripsNamespacedID pins that Exec strips the "seatbelt:" slug and
// spawns a fresh child for the raw darwin session id.
func TestExec_StripsNamespacedID(t *testing.T) {
	fake := newFakeRunner()
	// create child (proc1) has no Wait; one exec child (proc2) returns "hi".
	fake.results = []*Result{nil, {ExitCode: 0, Stdout: []byte("hi")}}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := b.Exec(context.Background(), "seatbelt:"+id, exec.Request{Argv: []string{"bash", "-c", "echo hi"}})
	if err != nil {
		t.Fatalf("Exec(namespaced): %v", err)
	}
	if string(res.Stdout) != "hi" {
		t.Fatalf("stdout = %q, want hi", res.Stdout)
	}
	// 1 create child + 1 exec child => the slug-stripped id resolved the session.
	if fake.startCount() != 2 {
		t.Fatalf("Start called %d times, want 2 (1 create + 1 exec)", fake.startCount())
	}
}

// ---------------------------------------------------------------------------
// SIGTERM-grace (TODO R-6)
// ---------------------------------------------------------------------------

// TestDestroy_IgnoresSIGTERM_EscalatesToSIGKILL pins the escalation branch of the
// graceful helper: when the child's process group ignores SIGTERM (a
// non-cooperative process), the backend must wait out the configured grace window
// and THEN escalate to SIGKILL. The kill sequence must be SIGTERM first, SIGKILL
// second — an immediate SIGKILL (the current behaviour) fails this assertion.
func TestDestroy_IgnoresSIGTERM_EscalatesToSIGKILL(t *testing.T) {
	fake := newFakeRunner()
	// Create child blocks its Wait and ignores SIGTERM (non-cooperative), so only
	// an escalated SIGKILL can terminate it. A short grace keeps the test fast.
	fake.blocks = []bool{true}
	b := newTestBackend(t, fake, []string{"/work"}, WithKillGrace(20*time.Millisecond))

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	p := fake.proc(1)
	sigs := p.killSignals()
	if !slices.Equal(sigs, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("kills = %v, want [SIGTERM SIGKILL] (graceful then escalation)", sigs)
	}
}

// TestDestroy_GraceSuccessNoSIGKILL pins the grace-success branch: a child that
// blocks its Wait but exits cooperatively on SIGTERM (within the grace window)
// must NOT be escalated to SIGKILL.
func TestDestroy_GraceSuccessNoSIGKILL(t *testing.T) {
	fake := newFakeRunner()
	// Create child blocks its Wait (so the helper's Wait goroutine is what
	// unblocks on SIGTERM) and is cooperative on SIGTERM (exits within grace).
	fake.blocks = []bool{true}
	fake.termExits = []bool{true}
	b := newTestBackend(t, fake, []string{"/work"}, WithKillGrace(200*time.Millisecond))

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	p := fake.proc(1)
	if !p.hasSignal(syscall.SIGTERM) {
		t.Fatalf("Create child was not sent SIGTERM; kills = %v", p.killSignals())
	}
	if p.hasSignal(syscall.SIGKILL) {
		t.Fatalf("Create child was SIGKILLed despite a graceful exit within grace; kills = %v", p.killSignals())
	}
}

// TestWithKillGrace_ConfigDefaultAndOverride pins that the kill-grace is
// configurable via the WithKillGrace Option and defaults to defaultKillGrace.
func TestWithKillGrace_ConfigDefaultAndOverride(t *testing.T) {
	b := newTestBackend(t, newFakeRunner(), nil)
	if b.cfg.killGrace != defaultKillGrace {
		t.Fatalf("default killGrace = %v, want %v", b.cfg.killGrace, defaultKillGrace)
	}

	override := 250 * time.Millisecond
	b2 := newTestBackend(t, newFakeRunner(), nil, WithKillGrace(override))
	if b2.cfg.killGrace != override {
		t.Fatalf("killGrace = %v, want %v (WithKillGrace honored)", b2.cfg.killGrace, override)
	}
}

// TestDestroy_ThreadsKillGraceToRunnerSpec pins that the backend threads the
// configured kill-grace into the runner's ProcessSpec for the Create child, so
// the real osProcess knows how long to wait before escalating to SIGKILL.
func TestDestroy_ThreadsKillGraceToRunnerSpec(t *testing.T) {
	fake := newFakeRunner()
	want := 300 * time.Millisecond
	b := newTestBackend(t, fake, []string{"/work"}, WithKillGrace(want))

	if _, err := b.Create(context.Background(), baseSeatbeltSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	start := fake.recordedStarts()[0]
	if start.KillGrace != want {
		t.Fatalf("Create ProcessSpec.KillGrace = %v, want %v", start.KillGrace, want)
	}
}

// TestExec_ThreadsKillGraceToRunnerSpec pins that the backend threads the
// configured kill-grace into the runner's ProcessSpec for each Exec child too.
func TestExec_ThreadsKillGraceToRunnerSpec(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{nil, {ExitCode: 0}}
	want := 350 * time.Millisecond
	b := newTestBackend(t, fake, []string{"/work"}, WithKillGrace(want))

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "cmd"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	start := fake.recordedStarts()[1]
	if start.KillGrace != want {
		t.Fatalf("Exec ProcessSpec.KillGrace = %v, want %v", start.KillGrace, want)
	}
}

// TestWithKillGrace_ClampsNonPositiveAndOversized pins the fail-closed clamping
// of WithKillGrace (security condition #1 of R-6): a non-positive value (which
// would mean an immediate, ungraceful hard-kill — weakening the graceful
// shutdown) is reset to defaultKillGrace, and a value above maxKillGrace is
// clamped to it so the DoS window a hostile process can hold the backend open is
// hard-bounded.
func TestWithKillGrace_ClampsNonPositiveAndOversized(t *testing.T) {
	b := newTestBackend(t, newFakeRunner(), nil, WithKillGrace(0))
	if b.cfg.killGrace != defaultKillGrace {
		t.Fatalf("WithKillGrace(0) = %v, want default %v (non-positive weakens graceful shutdown)", b.cfg.killGrace, defaultKillGrace)
	}
	bneg := newTestBackend(t, newFakeRunner(), nil, WithKillGrace(-time.Second))
	if bneg.cfg.killGrace != defaultKillGrace {
		t.Fatalf("WithKillGrace(negative) = %v, want default %v", bneg.cfg.killGrace, defaultKillGrace)
	}
	bover := newTestBackend(t, newFakeRunner(), nil, WithKillGrace(10*time.Second))
	if bover.cfg.killGrace != maxKillGrace {
		t.Fatalf("WithKillGrace(10s) = %v, want max %v (hard-bounded DoS window)", bover.cfg.killGrace, maxKillGrace)
	}
}

// scriptedProcess is a fully controllable Process used to unit-test
// killGroupWithGrace's error/ESRCH branches directly (which the fakeRunner
// scenarios cannot reach because its killErr applies uniformly to every signal).
type scriptedProcess struct {
	mu    sync.Mutex
	pgid  int
	kills []syscall.Signal
	kill  func(sig syscall.Signal) error
	wait  func(ctx context.Context) (*Result, error)
}

func (p *scriptedProcess) PGID() int { return p.pgid }
func (p *scriptedProcess) KillGroup(sig syscall.Signal) error {
	p.mu.Lock()
	p.kills = append(p.kills, sig)
	p.mu.Unlock()

	return p.kill(sig)
}
func (p *scriptedProcess) Wait(ctx context.Context) (*Result, error) { return p.wait(ctx) }
func (p *scriptedProcess) killSignals() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]syscall.Signal, len(p.kills))
	copy(out, p.kills)

	return out
}

// killGroupWithGraceWaitBlocks returns a wait func whose first call blocks until
// ctx.Done (modelling a grace window expiry) and whose subsequent calls return a
// clean result (modelling the post-escalation reap).
func killGroupWithGraceWaitBlocks() func(ctx context.Context) (*Result, error) {
	calls := 0

	return func(ctx context.Context) (*Result, error) {
		calls++
		if calls == 1 {
			<-ctx.Done()

			return nil, ctx.Err()
		}

		return &Result{ExitCode: 0, Signaled: true}, nil
	}
}

// TestKillGroupWithGrace_SIGTERMReturnsError pins that a hard error from the
// initial SIGTERM (non-ESRCH) is surfaced by the helper (no graceful window is
// entered).
func TestKillGroupWithGrace_SIGTERMReturnsError(t *testing.T) {
	p := &scriptedProcess{
		pgid: 1,
		kill: func(syscall.Signal) error { return errors.New("sigterm failed") },
		wait: func(context.Context) (*Result, error) { return &Result{}, nil },
	}
	err := killGroupWithGrace(context.Background(), p, 20*time.Millisecond)
	if err == nil || err.Error() != "sigterm failed" {
		t.Fatalf("killGroupWithGrace err = %v, want sigterm failed", err)
	}
	if got := p.killSignals(); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("kills = %v, want [SIGTERM] only", got)
	}
}

// TestKillGroupWithGrace_SIGKILLReturnsESRCH pins the escalation-ESRCH no-op
// branch: after the grace window expires, an escalated SIGKILL that hits ESRCH
// (the group vanished mid-grace) is treated as success — the helper still reaps
// and returns nil (X02 idempotency preserved).
func TestKillGroupWithGrace_SIGKILLReturnsESRCH(t *testing.T) {
	p := &scriptedProcess{
		pgid: 1,
		kill: func(sig syscall.Signal) error {
			if sig == syscall.SIGTERM {
				return nil
			}

			return syscall.ESRCH // SIGKILL escalation hits ESRCH
		},
		wait: killGroupWithGraceWaitBlocks(),
	}
	if err := killGroupWithGrace(context.Background(), p, 20*time.Millisecond); err != nil {
		t.Fatalf("killGroupWithGrace err = %v, want nil (ESRCH on escalation is no-op)", err)
	}
	if got := p.killSignals(); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("kills = %v, want [SIGTERM SIGKILL]", got)
	}
}

// TestKillGroupWithGrace_SIGKILLReturnsError pins the escalation-error branch: a
// non-ESRCH error from the escalated SIGKILL is surfaced.
func TestKillGroupWithGrace_SIGKILLReturnsError(t *testing.T) {
	p := &scriptedProcess{
		pgid: 1,
		kill: func(sig syscall.Signal) error {
			if sig == syscall.SIGTERM {
				return nil
			}

			return errors.New("kill failed")
		},
		wait: killGroupWithGraceWaitBlocks(),
	}
	err := killGroupWithGrace(context.Background(), p, 20*time.Millisecond)
	if err == nil || err.Error() != "kill failed" {
		t.Fatalf("killGroupWithGrace err = %v, want kill failed", err)
	}
	if got := p.killSignals(); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("kills = %v, want [SIGTERM SIGKILL]", got)
	}
}

// TestKillGroupWithGrace_ReapsAfterEscalation pins that the helper ALWAYS reaps
// (Waits) the process after an escalated SIGKILL: the final `proc.Wait(ctx)`
// after the grace window must run, so a group that ignores SIGTERM is still
// reaped exactly once per the kill+escalate+reap sequence. A mutation that drops
// that final reap (leaving the child un-reaped) leaves waitCalls==1; the correct
// sequence needs two Waits (the grace-window wait, then the post-escalation
// reap). Deterministic: the scripted Wait's first call blocks until the grace
// ctx expires, then returns; no real sleeps.
func TestKillGroupWithGrace_ReapsAfterEscalation(t *testing.T) {
	var mu sync.Mutex
	waitCalls := 0
	p := &scriptedProcess{
		pgid: 1,
		kill: func(syscall.Signal) error { return nil },
		wait: func(ctx context.Context) (*Result, error) {
			mu.Lock()
			waitCalls++
			n := waitCalls
			mu.Unlock()
			if n == 1 {
				<-ctx.Done() // first call blocks until the grace window expires
				return nil, ctx.Err()
			}

			return &Result{ExitCode: 0, Signaled: true}, nil
		},
	}
	if err := killGroupWithGrace(context.Background(), p, 20*time.Millisecond); err != nil {
		t.Fatalf("killGroupWithGrace err = %v, want nil", err)
	}
	if got := p.killSignals(); !slices.Equal(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("kills = %v, want [SIGTERM SIGKILL]", got)
	}
	mu.Lock()
	n := waitCalls
	mu.Unlock()
	if n != 2 {
		t.Fatalf("Wait called %d times, want 2 (grace-window wait + post-escalation reap)", n)
	}
}

// waitGroupTimeout waits for a WaitGroup to drain, failing the test if it does
// not complete within the deadline. This keeps the concurrency tests fail-fast
// under mutation: a mutated reaping path must FAIL (so the mutant is detected)
// rather than hang and time out the whole suite.
func waitGroupTimeout(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out waiting for concurrent goroutines (possible hang under mutation)")
	}
}

// TestDestroy_ConcurrentReapsOnce pins security condition #2 of R-6: two
// concurrent Destroys on the same session must reap (Wait) the Create child
// exactly once — never a double cmd.Wait ("Wait was already called" panic). The
// per-session reapMu serializes the kill+reap; the second Destroy is an
// idempotent ESRCH no-op.
func TestDestroy_ConcurrentReapsOnce(t *testing.T) {
	fake := newFakeRunner()
	// Create child: blocking Wait, cooperative on SIGTERM (dies within grace).
	fake.blocks = []bool{true}
	fake.termExits = []bool{true}
	b := newTestBackend(t, fake, []string{"/work"}, WithKillGrace(50*time.Millisecond))

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = b.Destroy(context.Background(), id)
		}(i)
	}
	waitGroupTimeout(t, &wg, 5*time.Second)

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent Destroy #%d err = %v, want nil", i, e)
		}
	}
	p := fake.proc(1)
	if n := p.waitCount(); n != 1 {
		t.Fatalf("Wait called %d times across concurrent Destroys, want exactly 1 (single reap)", n)
	}
	if !p.hasSignal(syscall.SIGTERM) {
		t.Fatalf("Create child was not SIGTERMed; kills = %v", p.killSignals())
	}
}

// TestDestroy_RacesWithExecCancel pins that a Destroy on the session's Create
// child racing a concurrently-cancelled Exec (which kills a fresh child) neither
// double-reaps a shared process nor panics: the two paths operate on independent
// processes and both complete cleanly (X05 / security condition #2 & #7 of R-6).
func TestDestroy_RacesWithExecCancel(t *testing.T) {
	fake := newFakeRunner()
	// create child: non-blocking, cooperative; exec child: blocking, cooperative.
	fake.results = []*Result{{ExitCode: 0}}
	fake.blocks = []bool{false, true}
	fake.termExits = []bool{true, true}
	b := newTestBackend(t, fake, []string{"/work"}, WithKillGrace(50*time.Millisecond))

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

	var destroyErr, execErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); destroyErr = b.Destroy(context.Background(), id) }()
	go func() {
		defer wg.Done()
		// Start Exec first so the exec child (start #2) is actually spawned.
		_, execErr = b.Exec(execCtx, id, exec.Request{Argv: []string{"bash", "-c", "cmd"}})
	}()

	// Grab the exec child once it is spawned, wait until its Wait is blocking,
	// then cancel — the graceful cancel path races the in-flight Destroy.
	p := fake.waitForStart(t, 2)
	<-p.started
	cancel()
	waitGroupTimeout(t, &wg, 5*time.Second)

	if destroyErr != nil {
		t.Fatalf("Destroy err = %v, want nil", destroyErr)
	}
	if execErr == nil {
		t.Fatal("Exec on cancellation should return an error")
	}
	if !hasCancelledEvent(events) {
		t.Fatalf("no cancelled progress event emitted; events = %+v", events)
	}
}
