package darwin

// Tests for Fix #3: setrlimit save/apply/restore. The darwin runner previously
// called syscall.Setrlimit in the parent process and never restored it, so the
// server's own resource limits were permanently lowered and shared across every
// session. The fix extracts the save/apply/restore logic into PORTABLE helpers
// that run on any GOOS and injects the syscall boundary through the rlimitOps
// interface, so the logic is fully testable on Linux without a real setrlimit.
//
// Contract pinned here:
//   - applying lowers then restoring returns the original values (round-trip);
//   - only the resources present in the lowers slice are touched;
//   - an empty lowers slice is a no-op (callable on any GOOS);
//   - a Get error is propagated;
//   - a Set error rolls back the already-applied resources so the parent is
//     never left partially lowered (the exact bug being fixed);
//   - Restore propagates a Set error.
//
// The real syscall-backed rlimitOps lives behind the darwin build tag
// (runner_darwin.go); these portable helpers and their tests compile on every
// platform so coverage is exercised on all CI legs (SPEC §7.12).

import (
	"errors"
	"sync"
	"syscall"
	"testing"
)

// fakeRlimitOps is the in-memory getrlimit/setrlimit double. It records the
// current limits per resource and can be configured to fail a Get or Set for a
// specific resource, making the error and rollback paths deterministic.
type fakeRlimitOps struct {
	mu     sync.Mutex
	limits map[int]syscall.Rlimit
	getErr map[int]error
	setErr map[int]error
}

func newFakeRlimitOps() *fakeRlimitOps {
	return &fakeRlimitOps{
		limits: map[int]syscall.Rlimit{},
		getErr: map[int]error{},
		setErr: map[int]error{},
	}
}

func (f *fakeRlimitOps) Getrlimit(resource int) (*syscall.Rlimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.getErr[resource]; err != nil {
		return nil, err
	}
	lim, ok := f.limits[resource]
	if !ok {
		return nil, errors.New("fake: no limit configured")
	}

	return &lim, nil
}

func (f *fakeRlimitOps) Setrlimit(resource int, lim *syscall.Rlimit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.setErr[resource]; err != nil {
		return err
	}
	f.limits[resource] = *lim

	return nil
}

// get returns the current limit for resource (for assertions).
func (f *fakeRlimitOps) get(resource int) syscall.Rlimit {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.limits[resource]
}

// compile-time check the fake satisfies the injectable boundary.
var _ rlimitOps = (*fakeRlimitOps)(nil)

// TestRlimitSaveApplyRestore_RoundTrip pins the full save→apply→restore cycle:
// after applying the lowers the child would inherit them, and after restore the
// parent's original limits are back.
func TestRlimitSaveApplyRestore_RoundTrip(t *testing.T) {
	ops := newFakeRlimitOps()
	ops.limits[int(syscall.RLIMIT_NOFILE)] = syscall.Rlimit{Cur: 10240, Max: 10240}
	ops.limits[int(syscall.RLIMIT_CPU)] = syscall.Rlimit{Cur: 3600, Max: 3600}

	lowers := []Rlimit{
		{Resource: int(syscall.RLIMIT_NOFILE), Soft: 1024, Hard: 2048},
		{Resource: int(syscall.RLIMIT_CPU), Soft: 1, Hard: 1},
	}
	prev, err := rlimitSaveAndApply(lowers, ops)
	if err != nil {
		t.Fatalf("rlimitSaveAndApply: %v", err)
	}

	// Applied (the child would inherit these at fork).
	if got := ops.get(int(syscall.RLIMIT_NOFILE)); got.Cur != 1024 || got.Max != 2048 {
		t.Fatalf("nofile after apply = %+v, want Cur=1024 Max=2048", got)
	}
	if got := ops.get(int(syscall.RLIMIT_CPU)); got.Cur != 1 || got.Max != 1 {
		t.Fatalf("cpu after apply = %+v, want Cur=1 Max=1", got)
	}

	// Restore returns the original server limits.
	if err := rlimitRestore(prev, ops); err != nil {
		t.Fatalf("rlimitRestore: %v", err)
	}
	if got := ops.get(int(syscall.RLIMIT_NOFILE)); got.Cur != 10240 || got.Max != 10240 {
		t.Fatalf("nofile after restore = %+v, want 10240/10240", got)
	}
	if got := ops.get(int(syscall.RLIMIT_CPU)); got.Cur != 3600 || got.Max != 3600 {
		t.Fatalf("cpu after restore = %+v, want 3600/3600", got)
	}
}

// TestRlimitSaveApply_TouchesOnlyLoweredResources pins that only the resources
// in the lowers slice are modified; any other resource is left untouched.
func TestRlimitSaveApply_TouchesOnlyLoweredResources(t *testing.T) {
	ops := newFakeRlimitOps()
	ops.limits[int(syscall.RLIMIT_NOFILE)] = syscall.Rlimit{Cur: 10240, Max: 10240}
	ops.limits[int(syscall.RLIMIT_CPU)] = syscall.Rlimit{Cur: 3600, Max: 3600}
	ops.limits[int(syscall.RLIMIT_STACK)] = syscall.Rlimit{Cur: 8000, Max: 8000}

	lowers := []Rlimit{{Resource: int(syscall.RLIMIT_NOFILE), Soft: 1024, Hard: 1024}}
	if _, err := rlimitSaveAndApply(lowers, ops); err != nil {
		t.Fatalf("rlimitSaveAndApply: %v", err)
	}

	if got := ops.get(int(syscall.RLIMIT_NOFILE)); got.Cur != 1024 {
		t.Fatalf("nofile = %+v, want lowered to 1024", got)
	}
	if got := ops.get(int(syscall.RLIMIT_CPU)); got.Cur != 3600 {
		t.Fatalf("cpu = %+v, want untouched 3600", got)
	}
	if got := ops.get(int(syscall.RLIMIT_STACK)); got.Cur != 8000 {
		t.Fatalf("stack = %+v, want untouched 8000", got)
	}
}

// TestRlimitSaveApply_EmptyNoOp pins the empty case: no lowers means nothing is
// read, applied or returned — the helper is a safe no-op callable on any GOOS.
func TestRlimitSaveApply_EmptyNoOp(t *testing.T) {
	ops := newFakeRlimitOps()
	ops.limits[int(syscall.RLIMIT_NOFILE)] = syscall.Rlimit{Cur: 10240, Max: 10240}

	prev, err := rlimitSaveAndApply(nil, ops)
	if err != nil {
		t.Fatalf("rlimitSaveAndApply(nil): %v", err)
	}
	if len(prev) != 0 {
		t.Fatalf("prev has %d entries, want 0 for empty lowers", len(prev))
	}
	if got := ops.get(int(syscall.RLIMIT_NOFILE)); got.Cur != 10240 {
		t.Fatalf("nofile modified for empty lowers: %+v", got)
	}
}

// TestRlimitSaveApply_GetErrorPropagates pins that a Get failure is surfaced.
func TestRlimitSaveApply_GetErrorPropagates(t *testing.T) {
	ops := newFakeRlimitOps()
	ops.limits[int(syscall.RLIMIT_NOFILE)] = syscall.Rlimit{Cur: 10240, Max: 10240}
	ops.getErr[int(syscall.RLIMIT_NOFILE)] = errors.New("get denied")

	lowers := []Rlimit{{Resource: int(syscall.RLIMIT_NOFILE), Soft: 1, Hard: 1}}
	if _, err := rlimitSaveAndApply(lowers, ops); err == nil {
		t.Fatal("rlimitSaveAndApply should propagate the get error")
	}
}

// TestRlimitSaveApply_ApplyErrorRollsBackPartiallyApplied pins the core fix: if
// applying the lowers fails partway, the resources already lowered are restored
// so the server is never left with a partially-lowered, permanently-mutated
// limit.
func TestRlimitSaveApply_ApplyErrorRollsBackPartiallyApplied(t *testing.T) {
	ops := newFakeRlimitOps()
	ops.limits[int(syscall.RLIMIT_NOFILE)] = syscall.Rlimit{Cur: 10240, Max: 10240}
	ops.limits[int(syscall.RLIMIT_CPU)] = syscall.Rlimit{Cur: 3600, Max: 3600}
	ops.setErr[int(syscall.RLIMIT_CPU)] = errors.New("cpu set denied")

	lowers := []Rlimit{
		{Resource: int(syscall.RLIMIT_NOFILE), Soft: 1024, Hard: 1024},
		{Resource: int(syscall.RLIMIT_CPU), Soft: 1, Hard: 1},
	}
	if _, err := rlimitSaveAndApply(lowers, ops); err == nil {
		t.Fatal("rlimitSaveAndApply should error when a set fails")
	}

	// nofile was applied first and must have been rolled back.
	if got := ops.get(int(syscall.RLIMIT_NOFILE)); got.Cur != 10240 || got.Max != 10240 {
		t.Fatalf("nofile after failed apply = %+v, want restored to 10240/10240", got)
	}
	// cpu's set never succeeded, so it stays at its original value.
	if got := ops.get(int(syscall.RLIMIT_CPU)); got.Cur != 3600 {
		t.Fatalf("cpu = %+v, want untouched 3600 (set failed)", got)
	}
}

// TestRlimitRestore_PropagatesError pins that a Set error during restore is
// surfaced to the caller.
func TestRlimitRestore_PropagatesError(t *testing.T) {
	ops := newFakeRlimitOps()
	ops.limits[int(syscall.RLIMIT_NOFILE)] = syscall.Rlimit{Cur: 1024, Max: 1024}
	ops.setErr[int(syscall.RLIMIT_NOFILE)] = errors.New("restore denied")

	prev := map[int]syscall.Rlimit{
		int(syscall.RLIMIT_NOFILE): {Cur: 10240, Max: 10240},
	}
	if err := rlimitRestore(prev, ops); err == nil {
		t.Fatal("rlimitRestore should propagate the set error")
	}
}
