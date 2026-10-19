//go:build darwin

package darwin

// Tests for the real os/exec runner (runner_darwin.go), which is compiled ONLY
// on darwin (and therefore not exercised by the Linux CI aggregate). This file
// is build-tagged darwin so it runs on the user's macOS host and closes the
// coverage gap that made `make test` report 79.1% for this package.
//
// Unlike the portable backend tests (which inject fakeRunner), these tests use
// the REAL os/exec children (the genuine boundary of the darwin backend) — no
// sandbox-exec, no fake. They spawn short-lived /bin/* children, so they are
// robust and fast. They cover realRunner.Start, osProcess (PGID / Wait /
// KillGroup) and realOps, i.e. everything the fake-based tests deliberately
// leave untested on the real platform path.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNewRunner_Darwin pins that newRunner returns the real os/exec wrapper on
// darwin (the counterpart of runner_other_test.go's TestNewRunner_Other).
func TestNewRunner_Darwin(t *testing.T) {
	r := newRunner()
	if _, ok := r.(realRunner); !ok {
		t.Fatalf("newRunner() = %T, want realRunner", r)
	}
}

// TestRealRunnerStart_EmptyArgv pins the guard against an empty argv: Start
// must fail fast with "darwin: empty argv" and never touch os/exec.
func TestRealRunnerStart_EmptyArgv(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{})
	if err == nil {
		t.Fatal("Start(empty argv) = nil error, want an error")
	}
	if err.Error() != "darwin: empty argv" {
		t.Fatalf("Start(empty argv) err = %q, want %q", err, "darwin: empty argv")
	}
	if p != nil {
		t.Fatalf("Start(empty argv) returned non-nil process %T", p)
	}
}

// TestRealRunnerStart_SpansRealChild spawns a real /bin/echo child and asserts
// the observable contract: a non-nil Process with a valid PGID, and a Wait that
// reports a clean exit with the captured stdout.
func TestRealRunnerStart_SpansRealChild(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{Argv: []string{"/bin/echo", "hello"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p == nil {
		t.Fatal("Start returned nil process")
	}
	if p.PGID() <= 0 {
		t.Fatalf("PGID() = %d, want > 0", p.PGID())
	}

	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	if string(res.Stdout) != "hello\n" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "hello\n")
	}
	if res.Signaled {
		t.Fatal("Signaled = true, want false for a clean exit")
	}
}

// TestOSProcessWait_NonZeroExit pins that a child exiting with a non-zero status
// is mapped into Result.ExitCode (via exec.ExitError + syscall.WaitStatus).
func TestOSProcessWait_NonZeroExit(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{Argv: []string{"/bin/sh", "-c", "exit 3"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Signaled {
		t.Fatal("Signaled = true, want false for a normal exit")
	}
}

// TestOSProcessWait_CapturesStderr pins that stderr written by the child is
// captured into Result.Stderr while stdout stays empty.
func TestOSProcessWait_CapturesStderr(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{
		Argv: []string{"/bin/sh", "-c", "echo oops >&2; exit 1"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1", res.ExitCode)
	}
	if !bytes.Contains(res.Stderr, []byte("oops")) {
		t.Fatalf("Stderr = %q, want it to contain %q", res.Stderr, "oops")
	}
	if len(res.Stdout) != 0 {
		t.Fatalf("Stdout = %q, want empty", res.Stdout)
	}
}

// TestOSProcessWait_CtxCancel pins the cancellation path: Wait with an
// already-cancelled context must SIGKILL the process group and return
// ctx.Err() (context.Canceled) rather than the child's result. An
// already-cancelled context makes this deterministic — no timing dependency.
func TestOSProcessWait_CtxCancel(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{Argv: []string{"/bin/sleep", "30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Wait: deterministic.

	res, err := p.Wait(ctx)
	if err == nil {
		t.Fatal("Wait on cancelled ctx = nil error, want ctx.Err()")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}
	if res != nil {
		t.Fatalf("Wait returned a result %+v on cancellation, want nil", res)
	}
}

// TestOSProcessWait_Timeout pins the DeadlineExceeded path using a short
// deadline on a long-lived child. The child is then reaped via KillGroup + Wait
// so no orphan remains.
func TestOSProcessWait_Timeout(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{Argv: []string{"/bin/sleep", "30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res, err := p.Wait(ctx)
	if err == nil {
		t.Fatal("Wait on timed-out ctx = nil error, want ctx.Err()")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait err = %v, want context.DeadlineExceeded", err)
	}
	if res != nil {
		t.Fatalf("Wait returned a result %+v on timeout, want nil", res)
	}

	// Reap the group so the child does not linger.
	_ = p.KillGroup(syscall.SIGKILL)
	_, _ = p.Wait(context.Background())
}

// TestOSProcessKillGroup pins that KillGroup(SIGKILL) kills the whole process
// group: Wait then returns promptly (Signaled=true), and a second KillGroup is
// tolerated (ESRCH is fine — the method just forwards syscall.Kill).
func TestOSProcessKillGroup(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{Argv: []string{"/bin/sleep", "30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := p.KillGroup(syscall.SIGKILL); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}

	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait after kill: %v", err)
	}
	if !res.Signaled {
		t.Fatalf("Signaled = false after SIGKILL; result = %+v", res)
	}

	// A second KillGroup on the (now dead) group must not panic; ESRCH is fine.
	_ = p.KillGroup(syscall.SIGKILL)
}

// TestRealOps_GetSetRlimitRoundTrip pins that the real syscall-backed rlimitOps
// can read RLIMIT_NOFILE and write it back unchanged without error. The limit is
// restored to its own value, so the host's real limits are never permanently
// altered.
func TestRealOps_GetSetRlimitRoundTrip(t *testing.T) {
	ops := realOps{}

	lim, err := ops.Getrlimit(syscall.RLIMIT_NOFILE)
	if err != nil {
		t.Fatalf("Getrlimit(RLIMIT_NOFILE): %v", err)
	}
	if lim == nil {
		t.Fatal("Getrlimit returned nil limit")
	}

	// Write the exact same value back immediately — a safe no-op round-trip.
	if err := ops.Setrlimit(syscall.RLIMIT_NOFILE, lim); err != nil {
		t.Fatalf("Setrlimit(RLIMIT_NOFILE) round-trip: %v", err)
	}

	again, err := ops.Getrlimit(syscall.RLIMIT_NOFILE)
	if err != nil {
		t.Fatalf("second Getrlimit(RLIMIT_NOFILE): %v", err)
	}
	if again.Cur != lim.Cur || again.Max != lim.Max {
		t.Fatalf("limit changed across round-trip: before %+v, after %+v", lim, again)
	}
}

// TestNonNilWriter pins the helper: nil becomes io.Discard, non-nil is passed
// through unchanged, so MultiWriter never panics on a nil writer.
func TestNonNilWriter(t *testing.T) {
	if got := nonNilWriter(nil); got != io.Discard {
		t.Fatalf("nonNilWriter(nil) = %T, want io.Discard", got)
	}

	var b bytes.Buffer
	if got := nonNilWriter(&b); got != io.Writer(&b) {
		t.Fatalf("nonNilWriter(w) = %T, want the same writer", got)
	}
}

// TestRealRunnerStart_MissingBinary pins the cmd.Start error branch: a
// nonexistent argv[0] surfaces as a wrapped "darwin: start" error and no Process
// is returned.
func TestRealRunnerStart_MissingBinary(t *testing.T) {
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{
		Argv: []string{"/nonexistent/definitely-not-a-binary-xyz"},
	})
	if err == nil {
		t.Fatal("Start(missing binary) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "darwin: start") {
		t.Fatalf("Start(missing binary) err = %q, want it wrapped with %q", err, "darwin: start")
	}
	if p != nil {
		t.Fatalf("Start(missing binary) returned non-nil process %T", p)
	}
}

// ---------------------------------------------------------------------------
// SIGTERM-grace (TODO R-6) — real os/exec children
// ---------------------------------------------------------------------------

// TestOSProcessWait_GracefulSendsSIGTERM pins that osProcess.Wait, on ctx
// cancellation, sends SIGTERM (the graceful signal) rather than jumping straight
// to SIGKILL. The child traps SIGTERM and records it to a marker file before
// exiting; if Wait had SIGKILLed first (the current behaviour), the trap would
// never run and the marker would be absent. The context is cancelled before Wait
// so the cancellation is deterministic (no timing dependency). A child that
// honours SIGTERM must be reaped within the grace window — no escalation needed.
//
// The child first signals readiness (a `touch` of the ready marker) after it
// has installed its TERM trap, and the test waits for that before cancelling.
// This removes a genuine OS-level race: a SIGTERM sent too early — before
// /bin/sh has installed the trap — terminates the child by the signal's default
// action without ever running the trap, which would make the marker assertion
// non-deterministic on a real host. The readiness gate and the exit-on-trap busy
// loop keep every assertion below intact while making the test deterministic.
func TestOSProcessWait_GracefulSendsSIGTERM(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gotterm")
	ready := marker + ".ready"
	// The trap is installed first; `touch <ready>` then signals that the child
	// is ready to observe SIGTERM. The trap writes the marker AND exits the
	// shell, and the body is a builtin busy loop (no child wait), so sh
	// processes the SIGTERM promptly and deterministically — a `sleep <n>` body
	// would defer the trap while sh is blocked in waitpid, making the marker
	// write racy on a real host.
	script := fmt.Sprintf("trap 'echo gotterm > %s; exit 0' TERM; touch %s; while :; do :; done", marker, ready)
	r := realRunner{}
	p, err := r.Start(context.Background(), ProcessSpec{
		Argv:      []string{"/bin/sh", "-c", script},
		KillGrace: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait (bounded) until the child has installed its TERM trap and signalled
	// readiness, so the SIGTERM below is observed by the trap rather than racing
	// its installation.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("child never signalled TERM trap readiness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // deterministic: cancel before Wait.

	if _, err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}

	// The SIGTERM trap ran => graceful signal was used, not an immediate SIGKILL.
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("SIGTERM marker %q not written (child was not gracefully SIGTERMed): %v", marker, err)
	}
	// The group was reaped within the grace window (no lingering child).
	if err := syscall.Kill(-p.PGID(), 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process group still alive after graceful cancel (kill err = %v)", err)
	}
}

// TestOSProcessWait_GracefulEscalatesToSIGKILL pins that when the child ignores
// SIGTERM (`trap ” TERM`), the graceful path must escalate to SIGKILL after the
// grace window so the group is still reaped. The child is non-cooperative, so
// only an escalated SIGKILL can terminate it; a short KillGrace keeps the test
// fast and deterministic (the context is cancelled before Wait).
func TestOSProcessWait_GracefulEscalatesToSIGKILL(t *testing.T) {
	r := realRunner{}
	// `trap '' TERM` makes both sh and the inherited sleep ignore SIGTERM, so
	// the group survives SIGTERM and must be SIGKILLed to be reaped.
	p, err := r.Start(context.Background(), ProcessSpec{
		Argv:      []string{"/bin/sh", "-c", `trap "" TERM; sleep 30`},
		KillGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // deterministic: cancel before Wait.

	if _, err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}

	// The group ignored SIGTERM; only SIGKILL escalation could have reaped it.
	// A non-ESRCH result means the child is still alive — escalation failed.
	if err := syscall.Kill(-p.PGID(), 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process group still alive after escalation (kill err = %v)", err)
	}
}
