//go:build darwin

package darwin

// Tests for the macOS unified-log tailer (tailer_darwin.go), which is compiled
// ONLY on darwin (and therefore not exercised by the Linux CI aggregate). This
// file is build-tagged darwin so it runs on the user's macOS host and closes
// the coverage gap of the real `log stream` wrapper, exactly like
// runner_darwin_test.go covers runner_darwin.go.
//
// These tests inject execCommand to return a REAL os/exec child (the genuine
// boundary of the tailer) — no fake reader. They spawn short-lived /bin/sh
// children that emit canned lines, so they are robust and fast, and they verify
// the line-yield, context-cancellation and error paths of the tailer.

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestNewUnifiedLogTailer_Default pins that the constructor wires the default
// exec.Command so a production tailer spawns the real `log` binary.
func TestNewUnifiedLogTailer_Default(t *testing.T) {
	tl := newUnifiedLogTailer()
	if tl == nil {
		t.Fatal("newUnifiedLogTailer() = nil")
	}
	if tl.execCommand == nil {
		t.Fatal("execCommand not wired to a default constructor")
	}
}

// TestUnifiedLogTailer_LinesYieldsCanned spawns a real /bin/sh child that emits
// a sandbox deny-looking line and an ordinary line, and asserts Lines yields
// both then closes the channel on child exit.
func TestUnifiedLogTailer_LinesYieldsCanned(t *testing.T) {
	tl := &unifiedLogTailer{
		execCommand: func(_ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(context.Background(), "/bin/sh", "-c",
				"echo 'Sandbox: deny file-read /etc/passwd (com.apple.sandbox) pid 7'; echo 'some ordinary line'")
		},
	}

	ch, err := tl.Lines(context.Background())
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}

	got := drainTailerLines(t, ch)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(got), got)
	}
	if !strings.Contains(got[0], "com.apple.sandbox") {
		t.Fatalf("line 0 = %q, want a sandbox deny line", got[0])
	}
	if got[1] != "some ordinary line" {
		t.Fatalf("line 1 = %q, want %q", got[1], "some ordinary line")
	}
}

// TestUnifiedLogTailer_LinesCancelCloses pins the cancellation path: once the
// first line is received, cancelling the context must kill the child's process
// group and close the channel promptly.
func TestUnifiedLogTailer_LinesCancelCloses(t *testing.T) {
	tl := &unifiedLogTailer{
		execCommand: func(_ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(context.Background(), "/bin/sh", "-c", "echo 'first line'; sleep 30")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := tl.Lines(ctx)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}

	select {
	case line, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before any line")
		}
		if line != "first line" {
			t.Fatalf("first line = %q, want %q", line, "first line")
		}
	case <-time.After(tailerCancelDeadline):
		t.Fatal("timed out waiting for the first line")
	}

	cancel()

	waitTailerClosed(t, ch, tailerCancelDeadline)
}

// TestUnifiedLogTailer_CancelWhileBlocked pins the ctx.Done branch of the send
// select: cancelling while the goroutine is blocked sending (no receiver) must
// still close the channel promptly rather than leak.
func TestUnifiedLogTailer_CancelWhileBlocked(t *testing.T) {
	tl := &unifiedLogTailer{
		execCommand: func(_ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(context.Background(), "/bin/sh", "-c", "echo 'line one'; echo 'line two'; sleep 30")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := tl.Lines(ctx)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}

	// Cancel without ever reading; the goroutine blocks sending and must pick
	// the ctx.Done branch and close the channel.
	cancel()

	waitTailerClosed(t, ch, tailerCancelDeadline)
}

// TestUnifiedLogTailer_StdoutPipeError pins the StdoutPipe failure branch: a
// command whose stdout is already wired cannot take a pipe, and Lines must
// return a wrapped error and no channel.
func TestUnifiedLogTailer_StdoutPipeError(t *testing.T) {
	tl := &unifiedLogTailer{
		execCommand: func(name string, arg ...string) *exec.Cmd {
			cmd := exec.CommandContext(context.Background(), name, arg...)
			cmd.Stdout = io.Discard // preset → StdoutPipe must fail

			return cmd
		},
	}

	ch, err := tl.Lines(context.Background())
	if err == nil {
		t.Fatal("Lines = nil error, want a StdoutPipe error")
	}
	if !strings.Contains(err.Error(), "stdout pipe") {
		t.Fatalf("err = %q, want it to mention the stdout pipe", err)
	}
	if ch != nil {
		t.Fatalf("Lines returned non-nil channel %T on error", ch)
	}
}

// TestUnifiedLogTailer_MissingBinary pins the cmd.Start failure branch: a
// nonexistent binary surfaces as a wrapped "darwin: log stream: start" error
// and no channel.
func TestUnifiedLogTailer_MissingBinary(t *testing.T) {
	tl := &unifiedLogTailer{
		execCommand: func(_ string, arg ...string) *exec.Cmd {
			return exec.CommandContext(context.Background(), "/nonexistent/definitely-not-log", arg...)
		},
	}

	ch, err := tl.Lines(context.Background())
	if err == nil {
		t.Fatal("Lines(missing binary) = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "darwin: log stream: start") {
		t.Fatalf("err = %q, want it wrapped with %q", err, "darwin: log stream: start")
	}
	if ch != nil {
		t.Fatalf("Lines returned non-nil channel %T on error", ch)
	}
}

// drainTailerLines collects every line from ch until it is closed.
func drainTailerLines(t *testing.T, ch <-chan string) []string {
	t.Helper()
	var got []string
	for line := range ch {
		got = append(got, line)
	}

	return got
}

// tailerCancelDeadline bounds how long a cancellation may take to close the
// channel. Cancellation→close is an inherently asynchronous chain: SIGKILL the
// child's process group → its write-end closes → EOF wakes the scanner → the
// child is reaped → the channel closes. CI runs `go test -race` on shared,
// loaded macOS runners, which inflates each hop's latency by a large,
// machine-dependent factor, so a small fixed timeout (e.g. 2s) flakes. The
// deadline is generous to absorb scheduling noise — it does NOT weaken the
// assertion: a broken cancel path that never closes the channel still fails
// within it, while a correct tailer completes in milliseconds.
const tailerCancelDeadline = 10 * time.Second

// waitTailerClosed blocks until ch is closed, or fails the test after
// deadline. It returns the moment the channel closes, so a correct tailer
// finishes promptly; the generous deadline only tolerates -race/CI scheduling
// noise on the process-kill→EOF→close chain.
func waitTailerClosed(t *testing.T, ch <-chan string, deadline time.Duration) {
	t.Helper()
	timer := time.NewTimer(deadline)
	defer timer.Stop()

	for {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("channel still open, want closed")
			}

			return
		case <-timer.C:
			t.Fatal("channel did not close within deadline")
		}
	}
}
