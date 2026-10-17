//go:build darwin

package darwin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

// logBinaryPath is the fixed path to the macOS unified-log CLI used to stream
// sandbox subsystem entries (SPEC §7.12).
const logBinaryPath = "/usr/bin/log"

// logStreamArgs are the argv elements (beyond the binary) for a live
// unified-log stream filtered to the sandbox subsystem. The predicate is a
// single argv element whose inner double quotes are part of the predicate
// expression — it is passed as one argument, never shell-interpolated (S11).
var logStreamArgs = []string{
	"stream",
	"--style", "compact",
	"--predicate", `subsystem == "com.apple.sandbox"`,
}

const (
	// scannerInitialBufferSize is the initial bufio.Scanner buffer size and
	// scannerMaxBufferSize its hard cap — both well above the 64 KiB default so
	// an over-long unified-log line ends the stream with a real error rather
	// than a silent stop. Named constants keep the mnd (magic numbers) linter
	// quiet.
	scannerInitialBufferSize = 65536
	scannerMaxBufferSize     = 1048576
)

// unifiedLogTailer streams unified-log entries for the sandbox subsystem via
// the macOS `log` CLI (SPEC §7.12). It satisfies the package-local logTailer
// boundary; deny.go turns each yielded line into a structured deny record. The
// child runs in its own process group so a cancelled Lines can kill the whole
// tree without leaking a goroutine.
type unifiedLogTailer struct {
	// execCommand builds the exec.Cmd for a binary. It is injectable so tests
	// substitute a canned command; the default is exec.Command.
	execCommand func(name string, arg ...string) *exec.Cmd
}

// newUnifiedLogTailer returns a tailer over the real macOS `log` binary.
func newUnifiedLogTailer() *unifiedLogTailer {
	return &unifiedLogTailer{execCommand: exec.Command}
}

// Lines starts a `log stream` child and returns a channel of raw unified-log
// lines. The caller must consume the channel (or cancel the context) to avoid
// blocking. On ctx cancellation the child's process group is SIGKILLed and the
// channel closes; a failure to start the binary surfaces a wrapped error. The
// channel is closed exactly once (EOF/exit/cancel), so no goroutine leaks.
func (t *unifiedLogTailer) Lines(ctx context.Context) (<-chan string, error) {
	argv := append([]string{logBinaryPath}, logStreamArgs...)
	cmd := t.execCommand(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("darwin: log stream: stdout pipe: %w", err)
	}

	// Drain `log`'s stderr so a chatty child never blocks on a full pipe; the
	// tailer feeds stdout lines only (deny.go classifies those).
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("darwin: log stream: start %q: %w", logBinaryPath, err)
	}

	ch := make(chan string)
	go streamTailer(ctx, cmd, stdout, ch)

	return ch, nil
}

// streamTailer reads lines from the child's stdout into ch, stopping the child
// on context cancellation or EOF, and closes ch exactly once. On cancellation
// cancelWatcher SIGKILLs the child's process group so the child (and its write
// end of the pipe) actually dies; the reader goroutine then unblocks via EOF.
// Crucially, closing ch is driven by a select on ctx.Done in the forwarding
// loop and never depends on a Read on stdout unblocking — macOS does not
// guarantee that closing a pipe read end (or even the write end after a kill)
// will promptly wake a goroutine blocked in read, so tying close(ch) to the
// scanner would let cancellation hang. The child is SIGKILLed (no-op once it
// has exited) and then reaped with cmd.Wait, so no zombie process is left
// behind on any exit path.
func streamTailer(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, ch chan<- string) {
	// Defers run LIFO, and the order matters here. close(ch) is registered last
	// so it runs FIRST, before cmd.Wait(): os/exec.Wait waits for its internal
	// stderr-copy goroutine (spawned for cmd.Stderr = io.Discard) to hit EOF,
	// which macOS does not guarantee promptly after the group kill. Gating
	// close(ch) behind Wait would let cancellation hang, so the channel must
	// close before reaping.
	defer func() { _ = cmd.Wait() }() // reap the child; registered first, runs last
	defer killLogGroup(cmd)           // registered second
	defer close(ch)                   // registered last, runs first

	stop := make(chan struct{})
	defer close(stop)
	go cancelWatcher(ctx, cmd, stop)

	// lines feeds raw scanner output from a dedicated reader goroutine. The
	// reader may stay blocked in a Read on stdout after cancellation until the
	// child is SIGKILLed, so it must never be the component that decides when
	// ch closes. The forwarding loop below closes ch on ctx.Done regardless.
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		// Raise the token limit well above the 64 KiB default so an over-long
		// unified-log line ends the stream with a real error instead of
		// silently stopping the denial feed (scanner.Err() was previously
		// ignored).
		scanner.Buffer(make([]byte, scannerInitialBufferSize), scannerMaxBufferSize)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	// Forward lines to ch, closing ch exactly once on cancellation or when the
	// reader finishes (lines is closed after the scanner hits EOF/error). The
	// reader selects ctx.Done before/while sending, so it never sends to ch.
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			select {
			case ch <- line:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// cancelWatcher kills the child's process group when ctx is done, or exits when
// stop is closed (the stream goroutine is already finishing). Killing the group
// terminates the child, whose death closes the pipe's write end so the blocked
// reader goroutine eventually unblocks via EOF (SIGKILL cannot be ignored, so
// the child always dies). It never outlives the stream goroutine, so no
// goroutine leaks.
func cancelWatcher(ctx context.Context, cmd *exec.Cmd, stop <-chan struct{}) {
	select {
	case <-ctx.Done():
		killLogGroup(cmd)
	case <-stop:
	}
}

// killLogGroup sends SIGKILL to the child's process group. A missing group
// (ESRCH) is ignored — the child already exited.
func killLogGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
