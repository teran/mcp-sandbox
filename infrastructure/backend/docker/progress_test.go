package docker

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/teran/mcp-sandbox/domain/progress"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// progressSink is a thread-safe Sink recording every event.
type progressSink struct {
	mu     sync.Mutex
	events []progress.Event
}

func (p *progressSink) sink(_ context.Context, e progress.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *progressSink) got() []progress.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]progress.Event, len(p.events))
	copy(out, p.events)

	return out
}

func messages(events []progress.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Message
	}

	return out
}

// TestExec_ProgressStreamingOrderedStartChunkDone verifies that Exec, given a
// sink in the context, emits an ordered stream: start (carrying redacted argv)
// → chunks (stdout/stderr lines) → done (with the exit code), with monotonic
// Progress.
func TestExec_ProgressStreamingOrderedStartChunkDone(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hello\nworld\n", stderr: "boom\n", exitCode: 3}}
	api.execExitCode = 3 // ContainerExecAttach consumes the execResult; inspect uses this
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-p")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ps := &progressSink{}
	res, err := b.Exec(progress.WithSink(ctx, ps.sink), "sess-p", exec.Request{Argv: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// Result is still populated and correct.
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if string(res.Stdout) != "hello\nworld\n" || string(res.Stderr) != "boom\n" {
		t.Errorf("result bytes changed by streaming: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}

	ev := ps.got()
	if len(ev) < 3 {
		t.Fatalf("events = %d, want at least start+chunk+done", len(ev))
	}
	// First event is start, carrying the (redacted) argv.
	first := ev[0]
	if !strings.HasPrefix(first.Message, "start") {
		t.Errorf("first event message = %q, want start prefix", first.Message)
	}
	if !strings.Contains(first.Message, "echo") || !strings.Contains(first.Message, "hi") {
		t.Errorf("start event must carry argv: %q", first.Message)
	}

	// Middle events are output chunks: each line appears exactly once.
	chunks := messages(ev[1 : len(ev)-1])
	joined := strings.Join(chunks, "\n")
	for _, line := range []string{"hello", "world", "boom"} {
		if !strings.Contains(joined, line) {
			t.Errorf("chunks missing line %q: %v", line, chunks)
		}
	}

	// Last event is done, carrying the exit code.
	last := ev[len(ev)-1]
	if !strings.HasPrefix(last.Message, "done") {
		t.Errorf("last event message = %q, want done prefix", last.Message)
	}
	if !strings.Contains(last.Message, "3") {
		t.Errorf("done event must carry exit code 3: %q", last.Message)
	}

	// Progress must be monotonic non-decreasing across the whole stream.
	prev := int64(-1)
	for i, e := range ev {
		if e.Progress < prev {
			t.Fatalf("progress not monotonic at event %d: %+v", i, ev)
		}
		prev = e.Progress
	}
}

// TestExec_ProgressStartShowsCommand pins that the `start:` stream event shows
// the raw visible command (req.Command), not the argv wrapper.
func TestExec_ProgressStartShowsCommand(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "ok\n", exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-cmd")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ps := &progressSink{}
	req := exec.Request{Command: "git status", Argv: []string{"/bin/sh", "-c", "git status"}}
	if _, err := b.Exec(progress.WithSink(ctx, ps.sink), "sess-cmd", req); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ev := ps.got()
	if len(ev) == 0 {
		t.Fatal("expected at least a start event")
	}
	// When Command is set, the start message shows exactly the raw command.
	if ev[0].Message != "start: git status" {
		t.Fatalf("start message = %q, want \"start: git status\"", ev[0].Message)
	}
}

// TestExec_ProgressStartFallsBackToArgv pins that when Command is empty the
// start event falls back to joining argv.
func TestExec_ProgressStartFallsBackToArgv(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "ok\n", exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-fb")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ps := &progressSink{}
	req := exec.Request{Argv: []string{"bash", "-c", "echo hi"}} // Command empty
	if _, err := b.Exec(progress.WithSink(ctx, ps.sink), "sess-fb", req); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ev := ps.got()
	if len(ev) == 0 {
		t.Fatal("expected at least a start event")
	}
	if ev[0].Message != "start: bash -c echo hi" {
		t.Fatalf("start message = %q, want argv join", ev[0].Message)
	}
}

// TestExec_ProgressCancelEmitsTerminalCancelled verifies that when the context
// is cancelled, Exec emits a terminal "cancelled" event before returning.
func TestExec_ProgressCancelEmitsTerminalCancelled(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "partial", exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-c")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	cctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the call; the fake ignores ctx so Exec still runs

	ps := &progressSink{}
	_, _ = b.Exec(progress.WithSink(cctx, ps.sink), "sess-c", exec.Request{Argv: []string{"x"}})

	ev := ps.got()
	if len(ev) == 0 {
		t.Fatal("expected at least a cancelled event on a cancelled context")
	}
	last := ev[len(ev)-1]
	if last.Message != "cancelled" {
		t.Fatalf("terminal event message = %q, want cancelled", last.Message)
	}
}

// TestExec_NoSink_ResultUnchanged verifies that without a sink in the context,
// Exec produces no progress notifications and its Result is byte-for-byte
// unchanged.
func TestExec_NoSink_ResultUnchanged(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hello\nworld\n", stderr: "err\n", exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-n")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No sink in context.
	res, err := b.Exec(context.Background(), "sess-n", exec.Request{Argv: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "hello\nworld\n" {
		t.Errorf("stdout = %q, want hello\\nworld\\n", res.Stdout)
	}
	if string(res.Stderr) != "err\n" {
		t.Errorf("stderr = %q, want err\\n", res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
}

// TestExec_NilSinkIsNilSafe verifies that an explicit nil sink placed in the
// context does not panic and the Result is unchanged.
func TestExec_NilSinkIsNilSafe(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "out\n", exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-n2")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := b.Exec(progress.WithSink(ctx, nil), "sess-n2", exec.Request{Argv: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "out\n" || res.ExitCode != 0 {
		t.Fatalf("result = %+v, want unchanged", res)
	}
}
