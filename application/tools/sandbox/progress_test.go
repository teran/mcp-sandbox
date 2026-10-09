package sandbox

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/teran/mcp-sandbox/domain/progress"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// progressEmitService wraps fakeService and additionally emits the raw exec
// stdout as a progress event through the sink present in the context. It models
// a backend that streams command output so the test can observe how the tool
// layer's sink wrapper (redactSecrets + sanitizeANSI, S02) transforms it.
type progressEmitService struct {
	*fakeService
}

func (p *progressEmitService) Exec(ctx context.Context, req exec.Request) (*exec.Result, error) {
	res, err := p.fakeService.Exec(ctx, req)
	if err != nil {
		return nil, err
	}
	// Emit the raw output through whatever sink the tool layer placed in ctx.
	// In production this sink is the wrapped one; emitting a secret here must
	// be redacted before it reaches the caller-visible sink.
	sink := progress.SinkFromContext(ctx)
	sink(ctx, progress.Event{Message: string(res.Stdout), Progress: int64(len(res.Stdout))})
	return res, nil
}

// collectEvents returns a Sink appending to dst under mu.
func collectEvents(mu *sync.Mutex, dst *[]progress.Event) progress.Sink {
	return func(_ context.Context, e progress.Event) {
		mu.Lock()
		defer mu.Unlock()
		*dst = append(*dst, e)
	}
}

func marshalProgressArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSandboxExec_ProgressStreamRedactsSecretsAndSanitizesANSI verifies that
// the sandboxExecDef wraps the context sink so a secret value leaked into the
// progress stream is redacted (S02) and ANSI escapes are stripped (S09) before
// reaching the caller.
func TestSandboxExec_ProgressStreamRedactsSecretsAndSanitizesANSI(t *testing.T) {
	svc := &progressEmitService{fakeService: newFake()}
	svc.session = runningSession("abc", spec.SandboxSpec{
		UIDGID:    spec.UIDGID{UID: 1000, GID: 1000},
		Secrets:   []spec.SecretRef{{Name: "token", Value: "supersecret"}},
		Env:       map[string]string{"SECRET": "envsecret"},
		Isolation: defaultPolicy().Isolation,
	})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("\x1b[31mvalue=supersecret env=envsecret\x1b[0m\n")}

	d := sandboxExecDef(builder{svc: svc})

	var mu sync.Mutex
	var events []progress.Event
	outer := collectEvents(&mu, &events)
	ctx := progress.WithSink(context.Background(), outer)

	out, err := d.Handler(ctx, marshalProgressArgs(t, map[string]any{"id": "abc", "argv": []string{"echo", "$SECRET"}}))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	eo, ok := out.(ExecOutput)
	if !ok {
		t.Fatalf("output type = %T, want ExecOutput", out)
	}
	// The final structured output must also be redacted/sanitized (existing
	// contract), confirming the tool still redacts the return value.
	if strings.Contains(eo.Stdout, "supersecret") || strings.Contains(eo.Stdout, "envsecret") {
		t.Fatalf("final stdout leaked a secret: %q", eo.Stdout)
	}
	if strings.Contains(eo.Stdout, "\x1b") {
		t.Fatalf("final stdout retained ANSI escapes: %q", eo.Stdout)
	}

	mu.Lock()
	ev := append([]progress.Event(nil), events...)
	mu.Unlock()

	if len(ev) != 1 {
		t.Fatalf("progress events = %d, want 1", len(ev))
	}
	msg := ev[0].Message
	// ANSI sequences must be gone.
	if strings.Contains(msg, "\x1b") {
		t.Fatalf("progress message retained ANSI: %q", msg)
	}
	// Secrets must be redacted.
	if strings.Contains(msg, "supersecret") {
		t.Fatalf("progress message leaked secret value: %q", msg)
	}
	if strings.Contains(msg, "envsecret") {
		t.Fatalf("progress message leaked env secret: %q", msg)
	}
	// The redaction marker must be present and the plain text preserved.
	if !strings.Contains(msg, redactedMarker) {
		t.Fatalf("progress message missing redaction marker: %q", msg)
	}
	if !strings.Contains(msg, "value=") || !strings.Contains(msg, "env=") {
		t.Fatalf("progress message lost plain text: %q", msg)
	}
}

// TestSandboxExec_ProgressStream_NoSecretsUnchanged verifies that without any
// secret, the progress message passes through redaction/sanitization unchanged
// (no spurious redaction).
func TestSandboxExec_ProgressStream_NoSecretsUnchanged(t *testing.T) {
	svc := &progressEmitService{fakeService: newFake()}
	svc.session = runningSession("abc", spec.SandboxSpec{
		UIDGID:    spec.UIDGID{UID: 1000, GID: 1000},
		Isolation: defaultPolicy().Isolation,
	})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("plain output\n")}

	d := sandboxExecDef(builder{svc: svc})

	var mu sync.Mutex
	var events []progress.Event
	ctx := progress.WithSink(context.Background(), collectEvents(&mu, &events))

	if _, err := d.Handler(ctx, marshalProgressArgs(t, map[string]any{"id": "abc", "argv": []string{"echo", "hi"}})); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Message != "plain output\n" {
		t.Fatalf("events = %+v, want unchanged [plain output\\n]", events)
	}
}

// TestSandboxExec_ProgressStream_NoSinkNilSafe verifies that when no sink is in
// the context the tool handler still runs and returns normally (nil-safe).
func TestSandboxExec_ProgressStream_NoSinkNilSafe(t *testing.T) {
	svc := &progressEmitService{fakeService: newFake()}
	svc.session = runningSession("abc", spec.SandboxSpec{
		UIDGID:    spec.UIDGID{UID: 1000, GID: 1000},
		Isolation: defaultPolicy().Isolation,
	})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("ok\n")}

	d := sandboxExecDef(builder{svc: svc})
	// No sink in ctx: progressEmitService.Exec will emit via a no-op sink.
	out, err := d.Handler(context.Background(), marshalProgressArgs(t, map[string]any{"id": "abc", "argv": []string{"echo", "hi"}}))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if eo := out.(ExecOutput); eo.Stdout != "ok\n" {
		t.Fatalf("stdout = %q, want ok\\n", eo.Stdout)
	}
}
