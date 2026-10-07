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

	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

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

func TestRun_UnsupportedMode(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
	t.Setenv("LOG_LEVEL", "")
	code := run(context.Background(), []string{"-mode", "http"}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_FlagParseError(t *testing.T) {
	code := run(context.Background(), []string{"-unknown-flag"}, &bytes.Buffer{})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestRun_InvalidAllowDirs(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "/definitely/not/a/real/dir")
	t.Setenv("LOG_LEVEL", "")
	code := run(context.Background(), []string{"-mode", "stdio"}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_ConfigLoadError(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("SANDBOX_IDLE_TIMEOUT", "not-a-duration")
	code := run(context.Background(), []string{"-mode", "stdio"}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_InvalidLogLevel(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
	t.Setenv("LOG_LEVEL", "banana")
	code := run(context.Background(), []string{"-mode", "stdio"}, &bytes.Buffer{})
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRun_StdioServesAndBanner(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
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
	code := run(ctx, []string{"-mode", "stdio"}, &errBuf)
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

func TestRun_StdioDisabledLogging(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
	t.Setenv("LOG_LEVEL", "")
	logFile := filepath.Join(t.TempDir(), "server.log")
	t.Setenv("LOG_FILENAME", logFile)

	orig := newStdioTransport
	tr, closer := newBlockingStdioTransport()
	newStdioTransport = func() mcp.Transport { return tr }
	defer func() { newStdioTransport = orig; closer() }()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	code := run(ctx, []string{"-mode", "stdio"}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if _, err := os.Stat(logFile); err == nil {
		t.Fatal("log file should not be created when logging is disabled")
	}
}

func TestServe_ServerError(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("LOG_FILENAME", filepath.Join(t.TempDir(), "err.log"))
	t.Setenv("LOG_FORMAT", "json")

	logger := newTestLogger(t)
	// A reader that immediately errors makes server.Run return an error.
	pr, pw := io.Pipe()
	pw.CloseWithError(io.ErrClosedPipe)
	tr := &mcp.IOTransport{Reader: io.NopCloser(pr), Writer: discardWriteCloser{}}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if code := serve(ctx, logger, tr); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}
