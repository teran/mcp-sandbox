package darwin

// This file provides the fakeRunner / fakeProcess in-memory double for the
// package-local `runner` interface. It is the test-only stand-in for os/exec
// (the genuine boundary of the darwin backend: it spawns /usr/bin/sandbox-exec
// children). All backend logic is exercised against this fake, so the tests are
// fully portable and run in CI on any GOOS — they never touch the host shell
// (AGENTS.md security-first).
//
// The fake records every Start (Argv/Env/Dir/Rlimits), every KillGroup signal
// and every Wait, and lets tests preconfigure per-child behavior (canned
// Result/error, or block-until-context-cancelled) so lifecycle and cancellation
// paths are deterministic.

import (
	"context"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeProcess is the in-memory Process returned by fakeRunner.Start. It records
// kills and waits, and honours a canned Result/error or a block-until-cancel
// mode.
type fakeProcess struct {
	mu      sync.Mutex
	pgid    int
	result  *Result
	waitErr error
	block   bool

	// started is closed (once) when Wait begins, so a test can reliably cancel
	// mid-wait without racing Start.
	started     chan struct{}
	startedOnce sync.Once
	waitCalls   int
	kills       []syscall.Signal
	killed      bool
	killErr     error
}

// newFakeProcess returns a process with the started channel ready.
func newFakeProcess(pgid int) *fakeProcess {
	return &fakeProcess{pgid: pgid, started: make(chan struct{})}
}

func (p *fakeProcess) PGID() int { return p.pgid }

// Wait records the call and either returns the canned result/error or blocks
// until ctx is cancelled (returning ctx.Err()).
func (p *fakeProcess) Wait(ctx context.Context) (*Result, error) {
	p.startedOnce.Do(func() { close(p.started) })
	p.mu.Lock()
	p.waitCalls++
	p.mu.Unlock()
	if p.block {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	return p.result, p.waitErr
}

// KillGroup records the signal. The first kill succeeds; any later kill is
// treated as ESRCH (the child is already dead), which models the OS behaviour
// the Destroy idempotency contract relies on.
func (p *fakeProcess) KillGroup(sig syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kills = append(p.kills, sig)
	if p.killed {
		return syscall.ESRCH
	}
	p.killed = true

	return p.killErr
}

// killSignals returns a copy of the recorded signals.
func (p *fakeProcess) killSignals() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]syscall.Signal, len(p.kills))
	copy(out, p.kills)

	return out
}

// hasSignal reports whether sig was among the recorded kills.
func (p *fakeProcess) hasSignal(sig syscall.Signal) bool {
	return slices.Contains(p.killSignals(), sig)
}

func (p *fakeProcess) waitCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.waitCalls
}

// fakeRunner implements the package-local runner interface in memory. It never
// spawns a real process; it records every Start and returns freshly-created
// fakeProcesses whose behaviour is taken from per-call configuration queues.
type fakeRunner struct {
	mu       sync.Mutex
	starts   []ProcessSpec
	procs    []*fakeProcess
	nextPGID int

	startErr error

	// Per-Start configuration queues, consumed in Start order.
	results  []*Result
	waitErrs []error
	blocks   []bool

	// killErr is applied to every produced fakeProcess (nil = kill succeeds).
	killErr error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{}
}

// Start implements runner. It records the ProcessSpec and returns a configured
// fakeProcess.
func (f *fakeRunner) Start(_ context.Context, ps ProcessSpec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.nextPGID++
	p := newFakeProcess(f.nextPGID)
	p.killErr = f.killErr
	if len(f.results) > 0 {
		p.result = f.results[0]
		f.results = f.results[1:]
	}
	if len(f.waitErrs) > 0 {
		p.waitErr = f.waitErrs[0]
		f.waitErrs = f.waitErrs[1:]
	}
	if len(f.blocks) > 0 {
		p.block = f.blocks[0]
		f.blocks = f.blocks[1:]
	}
	f.starts = append(f.starts, ps)
	f.procs = append(f.procs, p)

	return p, nil
}

// recordedStarts returns a copy of the recorded ProcessSpecs.
func (f *fakeRunner) recordedStarts() []ProcessSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ProcessSpec, len(f.starts))
	copy(out, f.starts)

	return out
}

// startCount returns how many Start calls were recorded.
func (f *fakeRunner) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.starts)
}

// proc returns the n-th (1-based) produced process, or nil.
func (f *fakeRunner) proc(n int) *fakeProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n < 1 || n > len(f.procs) {
		return nil
	}

	return f.procs[n-1]
}

// waitForStart polls until at least want processes have been produced and
// returns the want-th one. It is used to grab the child a backgrounded Exec
// spawns before the test cancels its context.
func (f *fakeRunner) waitForStart(t *testing.T, want int) *fakeProcess {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := f.proc(want); p != nil {
			return p
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for start #%d", want)

	return nil
}

// compile-time checks that the fakes satisfy the boundary interfaces.
var (
	_ runner  = (*fakeRunner)(nil)
	_ Process = (*fakeProcess)(nil)
)
