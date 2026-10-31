package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/config"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

// TestDockerBackend_Registered guards the composition root (SPEC §6.3): the
// concrete docker backend must be blank-imported so its init() registers it into
// backend.Default. Regression: without the blank import in main.go the registry
// has zero backends and every capability dispatch fails with
// ErrNoAvailableBackend ("no available backend covers the required capabilities")
// regardless of Docker availability.
func TestDockerBackend_Registered(t *testing.T) {
	f, ok := backend.Default.Get("docker")
	if !ok {
		t.Fatal("docker backend is not registered in backend.Default; missing blank import in main.go")
	}
	if !f.Capabilities.Lifecycle || !f.Capabilities.Executable || !f.Capabilities.Introspectable {
		t.Fatalf("docker backend must advertise Lifecycle+Executable+Introspectable, got %+v", f.Capabilities)
	}
}

func newTestLogger(t *testing.T) *logging.Logger {
	t.Helper()
	l, err := logging.New(logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	return l
}

// discardWriteCloser is an io.WriteCloser that discards all writes.
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// newBlockingStdioTransport returns an IOTransport whose reader blocks until
// the context is cancelled, making serve() deterministic in tests. The writer
// side is held open for the lifetime of the test via the returned closer.
func newBlockingStdioTransport() (mcp.Transport, func()) {
	pr, pw := io.Pipe()

	return &mcp.IOTransport{Reader: io.NopCloser(pr), Writer: discardWriteCloser{}}, func() { pw.Close() }
}

func TestRun_FlagParseError(t *testing.T) {
	code := run(context.Background(), []string{"-unknown-flag"}, &bytes.Buffer{})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRun_InvalidAllowDir(t *testing.T) {
	// ALLOW_DIRS is the KEPT host-path gate (Phase 7): a missing entry fails
	// config.Validate and the server refuses to start.
	t.Setenv("ALLOW_DIRS", "/definitely/not/a/real/allowdir")
	t.Setenv("LOG_LEVEL", "")
	code := run(context.Background(), nil, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_ConfigLoadError(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("SANDBOX_IDLE_TIMEOUT", "not-a-duration")
	code := run(context.Background(), nil, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_InvalidLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "banana")
	code := run(context.Background(), nil, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_StdioServesAndBanner(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("LOG_FORMAT", "text")
	logFile := filepath.Join(t.TempDir(), "server.log")
	t.Setenv("LOG_FILENAME", logFile)

	orig := newStdioTransport
	tr, closer := newBlockingStdioTransport()
	newStdioTransport = func() mcp.Transport { return tr }
	defer func() { newStdioTransport = orig; closer() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var errBuf bytes.Buffer
	code := run(ctx, nil, &errBuf)
	if code != 0 {
		t.Fatalf("code = %d, want 0; stderr=%s", code, errBuf.String())
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile log: %v", err)
	}
	if !strings.Contains(string(data), "Starting "+appName+"/") {
		t.Fatalf("banner not emitted:\n%s", data)
	}
}

// TestRun_StdioLogsToolSurface pins L10/L11 at the composition root: when
// logging is enabled, the startup wiring (local only, A02/N35) emits one
// per-tool line for every statically-registered tool, each carrying
// always_present=true (all tools in this server are static/always present).
func TestRun_StdioLogsToolSurface(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")
	t.Setenv("LOG_FORMAT", "text")
	logFile := filepath.Join(t.TempDir(), "server.log")
	t.Setenv("LOG_FILENAME", logFile)

	orig := newStdioTransport
	tr, closer := newBlockingStdioTransport()
	newStdioTransport = func() mcp.Transport { return tr }
	defer func() { newStdioTransport = orig; closer() }()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if code := run(ctx, nil, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("ReadFile log: %v", err)
	}
	s := string(data)

	// Every statically-registered tool must appear with the always_present=true
	// marker. profile_list is always present because buildServer always wires a
	// read-on-demand profile store.
	for _, name := range []string{
		"sandbox_list", "sandbox_info", "sandbox_stats",
		"sandbox_create", "sandbox_exec", "sandbox_destroy", "sandbox_destroy_all",
		"profile_list",
	} {
		if !strings.Contains(s, "tool="+name) {
			t.Fatalf("missing tool-surface line for %s:\n%s", name, s)
		}
	}
	if !strings.Contains(s, "always_present=true") {
		t.Fatalf("expected always_present=true marker:\n%s", s)
	}
}

// TestRun_StdioDisabledLoggingNoToolSurface pins L10/L11 at the composition
// root: when LOG_LEVEL is unset (logging disabled), no tool-surface lines are
// emitted — the log file is not even created.
func TestRun_StdioDisabledLoggingNoToolSurface(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	logFile := filepath.Join(t.TempDir(), "server.log")
	t.Setenv("LOG_FILENAME", logFile)

	orig := newStdioTransport
	tr, closer := newBlockingStdioTransport()
	newStdioTransport = func() mcp.Transport { return tr }
	defer func() { newStdioTransport = orig; closer() }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if code := run(ctx, nil, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if _, err := os.Stat(logFile); err == nil {
		t.Fatal("log file should not be created when logging is disabled; no tool-surface lines allowed")
	}
}

func TestRun_StdioDisabledLogging(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	logFile := filepath.Join(t.TempDir(), "server.log")
	t.Setenv("LOG_FILENAME", logFile)

	orig := newStdioTransport
	tr, closer := newBlockingStdioTransport()
	newStdioTransport = func() mcp.Transport { return tr }
	defer func() { newStdioTransport = orig; closer() }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	code := run(ctx, nil, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if _, err := os.Stat(logFile); err == nil {
		t.Fatal("log file should not be created when logging is disabled")
	}
}

func TestServe_ServerError(t *testing.T) {
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("LOG_FILENAME", filepath.Join(t.TempDir(), "err.log"))
	t.Setenv("LOG_FORMAT", "json")

	logger := newTestLogger(t)
	cfg := &config.Config{LogLevel: "error"}
	// A reader that immediately errors makes server.Run return an error.
	pr, pw := io.Pipe()
	pw.CloseWithError(io.ErrClosedPipe)
	tr := &mcp.IOTransport{Reader: io.NopCloser(pr), Writer: discardWriteCloser{}}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if code := serve(ctx, logger, cfg, tr); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestServe_CleanExit(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	logger := newTestLogger(t)
	cfg := &config.Config{}
	// A reader that returns a clean EOF makes server.Run return nil, so serve
	// exits 0 through the errCh-nil branch.
	pr, pw := io.Pipe()
	pw.Close()
	tr := &mcp.IOTransport{Reader: io.NopCloser(pr), Writer: discardWriteCloser{}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if code := serve(ctx, logger, cfg, tr); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
}

func TestMain_CallsExit(t *testing.T) { // Capture the exit code instead of terminating the process.
	code := -999
	oldExit := exit
	exit = func(c int) { code = c }
	defer func() { exit = oldExit }()

	oldArgs := os.Args
	os.Args = []string{"mcp-sandbox", "-unknown-flag"}
	defer func() { os.Args = oldArgs }()

	main()
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (flag parse error)", code)
	}
}

// TestBuildServer_DoesNotLoadAtStartup pins the read-on-demand contract at the
// composition root: buildServer must NOT preload/parse profiles at startup, so
// even a broken/unparsable profiles dir still composes a working server. Errors
// surface lazily, on the first Get/List call.
func TestBuildServer_DoesNotLoadAtStartup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yml"), []byte("name: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SandboxProfilesDir: dir}
	if server := buildServer(cfg, newTestLogger(t)); server == nil {
		t.Fatal("buildServer returned nil server on a broken profiles dir (must not load at startup)")
	}
}

// TestBuildServer_WiresProfileStore pins the happy-path wiring: buildServer
// composes a server with a read-on-demand profile store rooted at the operator
// profiles dir.
func TestBuildServer_WiresProfileStore(t *testing.T) {
	dir := t.TempDir()
	prof := "name: default\nimage: docker.io/library/alpine:3.20\n" +
		"network:\n  mode: bridge\ncgroups:\n  pids_limit: 256\nsudo: never\n"
	if err := os.WriteFile(filepath.Join(dir, "default.yml"), []byte(prof), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SandboxProfilesDir: dir}
	if server := buildServer(cfg, newTestLogger(t)); server == nil {
		t.Fatal("buildServer returned nil server with a valid profiles dir")
	}
}
