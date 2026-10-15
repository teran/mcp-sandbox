//go:build darwin

package darwin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
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
func (realRunner) Start(ctx context.Context, ps ProcessSpec) (Process, error) {
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
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}

		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// Apply setrlimit before exec: a child process inherits the parent's
	// resource limits, so setting them here bounds the sandboxed child. This is
	// the documented darwin analogue of the immutable resource block — weaker
	// than cgroups and not a hard quota (SPEC §7.12).
	for _, rl := range ps.Rlimits {
		if err := syscall.Setrlimit(rl.Resource, &syscall.Rlimit{Cur: rl.Soft, Max: rl.Hard}); err != nil {
			return nil, fmt.Errorf("darwin: setrlimit(%d): %w", rl.Resource, err)
		}
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("darwin: start %q: %w", ps.Argv[0], err)
	}

	return &osProcess{cmd: cmd, pgid: cmd.Process.Pid, stdout: &stdout, stderr: &stderr}, nil
}

// osProcess is the darwin Process: it tracks the exec.Cmd and its process group
// id (the leader pid), and kills/waits on the group.
type osProcess struct {
	cmd            *exec.Cmd
	pgid           int
	stdout, stderr *bytes.Buffer
}

// PGID returns the process group id (the leader pid).
func (p *osProcess) PGID() int { return p.pgid }

// KillGroup signals the whole process group, idempotently: a missing group
// (ESRCH) is returned as-is so the caller can treat it as a no-op.
func (p *osProcess) KillGroup(sig syscall.Signal) error {
	return syscall.Kill(-p.pgid, sig)
}

// Wait blocks until the child exits or ctx is cancelled. On cancellation it
// SIGKILLs the group and returns ctx.Err(). On a normal exit it returns the
// exit code and captured output.
func (p *osProcess) Wait(ctx context.Context) (*Result, error) {
	errCh := make(chan error, 1)
	go func() { errCh <- p.cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
		<-errCh

		return nil, ctx.Err()
	case err := <-errCh:
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
					return &Result{
						ExitCode: ws.ExitStatus(),
						Stdout:   p.stdout.Bytes(),
						Stderr:   p.stderr.Bytes(),
						Signaled: ws.Signaled(),
					}, nil
				}
			}

			return nil, err
		}

		return &Result{ExitCode: 0, Stdout: p.stdout.Bytes(), Stderr: p.stderr.Bytes()}, nil
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
