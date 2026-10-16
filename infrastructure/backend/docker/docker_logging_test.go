package docker

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

// newBufferLogger returns a logrus entry writing to buf at trace level with a
// stable, parseable text format.
func newBufferLogger(buf *bytes.Buffer) *logrus.Entry {
	l := logrus.New()
	l.SetOutput(buf)
	l.SetLevel(logrus.TraceLevel)
	l.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})

	return logrus.NewEntry(l)
}

// TestBackend_TraceLogsCarryRequestSessionIDAndNoSecrets drives a full Create
// (including image pull and secret delivery) through a trace-level logger and
// asserts that every trace line carries request_id and session_id from the
// context (L09) and that no secret value leaks into the output (S02/L05).
func TestBackend_TraceLogsCarryRequestSessionIDAndNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	api := newFakeClient()
	api.requirePull = true // exercise the pull trace path
	b := New(api, nil, WithLogger(newBufferLogger(&buf)))

	ctx := domainsession.WithSessionID(
		logging.WithRequestID(context.Background(), "req-xyz"),
		"sess-1",
	)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "super-secret-value"}}
	if _, err := b.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "req-xyz") {
		t.Fatalf("trace output missing request_id:\n%s", out)
	}
	if !strings.Contains(out, "sess-1") {
		t.Fatalf("trace output missing session_id:\n%s", out)
	}
	if !strings.Contains(out, "docker: pull") {
		t.Fatalf("trace output missing pull log:\n%s", out)
	}
	if !strings.Contains(out, "docker: ContainerCreate") {
		t.Fatalf("trace output missing create log:\n%s", out)
	}
	if strings.Contains(out, "super-secret-value") {
		t.Fatalf("secret value leaked into trace output:\n%s", out)
	}
}

// TestBackend_TraceLogsCarrySessionIDArg asserts Exec threads the session id it
// receives as an explicit argument onto its trace lines when the context has no
// session id.
func TestBackend_TraceLogsCarrySessionIDArg(t *testing.T) {
	var buf bytes.Buffer
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hi", exitCode: 0}}
	b := New(api, nil, WithLogger(newBufferLogger(&buf)))

	// Seed a container so Exec can run against a real container id (the session
	// id IS the container id).
	s := baseSpec()
	id, err := b.Create(context.Background(), s)
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	ctx := logging.WithRequestID(context.Background(), "req-exec")
	res, err := b.Exec(ctx, id, exec.Request{Argv: []string{"echo", "hi"}, User: "1000:1000"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d", res.ExitCode)
	}

	out := buf.String()
	if !strings.Contains(out, "req-exec") {
		t.Fatalf("trace output missing request_id:\n%s", out)
	}
	if !strings.Contains(out, id) {
		t.Fatalf("trace output missing session_id %q:\n%s", id, out)
	}
	if !strings.Contains(out, "docker: exec done") {
		t.Fatalf("trace output missing exec done log:\n%s", out)
	}
}
