//go:build darwin

package darwin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// realRunner spawns /usr/bin/sandbox-exec children on macOS via os/exec. Each
// child is started in its own process group (Setpgid) so the backend can kill
// the whole sandboxed tree with kill(-pgid, SIGKILL) (SPEC §7.12), and the
// setrlimit-mappable ulimits are applied so the child inherits them.
type realRunner struct{}

// newRunner returns the platform runner: the real os/exec wrapper on darwin.
func newRunner() runner { return realRunner{} }

// rlimitNPROC is not exposed by the syscall package on darwin (it defines
// only AS/CORE/CPU/DATA/FSIZE/NOFILE/STACK). The macOS kernel value for
// RLIMIT_NPROC is 7 (between RLIMIT_MEMLOCK=6 and RLIMIT_NOFILE=8); it is
// pinned here so the portable rlimit mapping compiles on every GOOS. On
// non-darwin the constant is defined in runner_other.go.
const rlimitNPROC = 0x7

// Start builds the exec.Cmd from the ProcessSpec and starts it. Resource
// limits are applied in the parent before exec (the child inherits setrlimit
// limits). Output is captured to buffers while also being written to the spec
// writers, so the Result returned by Wait carries stdout/stderr.
func (realRunner) Start(_ context.Context, ps ProcessSpec) (Process, error) {
	if len(ps.Argv) == 0 {
		return nil, fmt.Errorf("darwin: empty argv")
	}

	cmd := &exec.Cmd{
		Path: ps.Argv[0],
		Args: ps.Argv,
		Env:  ps.Env,
		Dir:  ps.Dir,
		SysProcAttr: &syscall.SysProcAttr{
			// Start the child in its own process group so Destroy/Exec can kill
			// the whole sandboxed tree with kill(-pgid, SIGKILL).
			Setpgid: true,
		},
	}
	if ps.Stdin != nil {
		cmd.Stdin = ps.Stdin
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, nonNilWriter(ps.Stdout))
	cmd.Stderr = io.MultiWriter(&stderr, nonNilWriter(ps.Stderr))

	// Apply the setrlimit-mappable limits, then always restore the parent's own
	// limits afterwards. The child inherits the lowered limits at fork and keeps
	// them across exec, so restoring the parent afterwards is safe for the child.
	// This fixes the previous behaviour, which permanently lowered the server's
	// own limits by never restoring them. This is the documented darwin analogue
	// of the immutable resource block — weaker than cgroups and not a hard quota
	// (SPEC §7.12).
	prev, err := rlimitSaveAndApply(ps.Rlimits, realOps{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rlimitRestore(prev, realOps{}) }()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("darwin: start %q: %w", ps.Argv[0], err)
	}

	return &osProcess{
		cmd:    cmd,
		pgid:   cmd.Process.Pid,
		stdout: &stdout,
		stderr: &stderr,
		grace:  ps.KillGrace,
	}, nil
}

// realOps implements rlimitOps over the syscall package on darwin, providing
// the injectable boundary used by rlimitSaveAndApply/rlimitRestore.
type realOps struct{}

func (realOps) Getrlimit(resource int) (*syscall.Rlimit, error) {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(resource, &lim); err != nil {
		return nil, err
	}

	return &lim, nil
}

func (realOps) Setrlimit(resource int, lim *syscall.Rlimit) error {
	return syscall.Setrlimit(resource, lim)
}

// osProcess is the darwin Process: it tracks the exec.Cmd and its process group
// id (the leader pid), and kills/waits on the group.
type osProcess struct {
	cmd            *exec.Cmd
	pgid           int
	stdout, stderr *bytes.Buffer
	// grace is the SIGTERM→SIGKILL escalation window Wait applies on
	// cancellation before hard-killing the group (R-6). Zero means an immediate
	// hard-kill (the pre-R-6 behaviour).
	grace time.Duration

	// waitOnce ensures the single cmd.Wait goroutine is started exactly once,
	// and `done`/`waitErr`/`result` are shared across all Wait callers. This is
	// the fix for the pre-existing X05 double-Wait bug: os/exec panics with
	// "Wait was already called" if cmd.Wait runs twice, and the backend can call
	// Wait from both Destroy and the Exec-cancel path. `done` closes only after
	// the goroutine has written waitErr/result, establishing the happens-before
	// needed to read them race-free (security condition #2 of R-6).
	waitOnce sync.Once
	done     chan struct{}
	waitErr  error
	result   *Result
}

// PGID returns the process group id (the leader pid).
func (p *osProcess) PGID() int { return p.pgid }

// KillGroup signals the whole process group, idempotently: a missing group
// (ESRCH) is returned as-is so the caller can treat it as a no-op.
func (p *osProcess) KillGroup(sig syscall.Signal) error {
	return syscall.Kill(-p.pgid, sig)
}

// Wait blocks until the child exits or ctx is cancelled. On cancellation it
// sends SIGTERM to the group, waits up to the configured grace for a graceful
// exit, escalates to SIGKILL if the group ignores SIGTERM, and returns
// ctx.Err(). On a normal exit it returns the exit code and captured output.
// Wait is safe to call concurrently: cmd.Wait runs exactly once, shared by all
// callers (security condition #2 of R-6).
func (p *osProcess) Wait(ctx context.Context) (*Result, error) {
	// Start the single cmd.Wait goroutine exactly once. Later Wait callers
	// reuse the shared done/waitErr/result (happens-before via close(done)).
	p.waitOnce.Do(func() {
		p.done = make(chan struct{})
		go func() {
			err := p.cmd.Wait()
			p.waitErr = err
			if err == nil {
				p.result = &Result{ExitCode: 0, Stdout: p.stdout.Bytes(), Stderr: p.stderr.Bytes()}
			} else if ee, ok := errors.AsType[*exec.ExitError](err); ok {
				if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
					p.result = &Result{
						ExitCode: ws.ExitStatus(),
						Stdout:   p.stdout.Bytes(),
						Stderr:   p.stderr.Bytes(),
						Signaled: ws.Signaled(),
					}
				}
			}
			close(p.done)
		}()
	})

	select {
	case <-p.done:
		if p.waitErr != nil && p.result == nil {
			return nil, p.waitErr
		}

		return p.result, nil
	case <-ctx.Done():
		// Graceful termination: SIGTERM the whole group so it can flush/clean
		// up, wait up to the grace window for a graceful exit, then escalate to
		// SIGKILL only if the group ignores SIGTERM. All terminating branches
		// end with <-done, reaping exactly once (cmd.Wait runs once).
		_ = syscall.Kill(-p.pgid, syscall.SIGTERM)
		if p.grace > 0 {
			select {
			case <-p.done:
			case <-time.After(p.grace):
				_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
				<-p.done
			}
		} else {
			_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
			<-p.done
		}

		return nil, ctx.Err()
	}
}

// nonNilWriter returns w if non-nil, else io.Discard, so MultiWriter never
// panics on a nil writer.
func nonNilWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}

	return w
}
