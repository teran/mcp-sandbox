// Command mcp-sandbox is the composition root of the mcp-sandbox MCP server
// (Local, stdio-only). It performs local wiring only at startup (A02/N35):
// flags, config load, logger, banner, layer composition and transport start.
// No outbound/upstream calls happen here.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/config"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
	mcpserver "github.com/teran/mcp-sandbox/infrastructure/mcp"
)

// Build metadata, embedded via ldflags (B02).
var (
	appName       = "mcp-sandbox"
	appVersion    = "dev"
	appCommitHash = "unknown"
	appTimestamp  = "unknown"
)

// exit is the process-exit function. It is a variable so tests can substitute
// it instead of terminating the test binary.
var exit = os.Exit

func main() {
	exit(run(context.Background(), os.Args[1:], os.Stderr))
}

// run performs the composition-root wiring and starts the stdio transport. It
// returns a process exit code. It is separated from main for testability.
func run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "stdio", "launch mode (stdio)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, "%s: %v\n", appName, err)
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, "%s: %v\n", appName, err)
	}

	logger, err := logging.New(logging.Options{
		Level:    cfg.LogLevel,
		Filename: cfg.LogFilename,
		Format:   cfg.LogFormat,
	})
	if err != nil {
		return fail(stderr, "%s: %v\n", appName, err)
	}
	defer func() { _ = logger.Close() }()

	if logger.Enabled() {
		logger.Banner(appName, appVersion, appCommitHash, appTimestamp)
	}

	switch *mode {
	case "stdio":
		return serve(ctx, logger, cfg, newStdioTransport())
	default:
		return fail(stderr, "%s: unsupported mode %q (only stdio is supported)\n", appName, *mode)
	}
}

// fail writes an error message to stderr and returns a failure exit code. The
// write error is deliberately ignored: there is nothing to do if stderr itself
// fails.
func fail(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, format, args...)
	return 1
}

// newStdioTransport returns the transport used in stdio mode. It is a variable
// so tests can substitute a deterministic transport.
var newStdioTransport = func() mcp.Transport { return &mcp.StdioTransport{} }

// buildServer composes the sandbox service (lazy backend selection + session
// manager), the declarative tool definitions, the handler-wrapper dependencies
// and the go-sdk MCP server. It performs local wiring only — no outbound calls
// happen here (A02/N35); the backend is selected lazily on the first tool call.
func buildServer(cfg *config.Config, logger *logging.Logger) *mcp.Server {
	svc := mcpserver.NewService(backend.Default, backend.Config{
		DockerHost:    cfg.DockerHost,
		AllowDirs:     cfg.AllowDirs,
		AllowSymlinks: cfg.AllowSymlinks,
		CwdMode:       cfg.SandboxCwdMode,
	})
	deps := mcpserver.HandlerDependencies(logger)
	return mcpserver.NewServer(
		&mcp.Implementation{Name: appName, Version: appVersion},
		logger,
		mcpserver.BuildTools(svc),
		deps,
	)
}

// serve starts the MCP server over the given transport and blocks until the
// context is cancelled or the transport ends.
func serve(ctx context.Context, logger *logging.Logger, cfg *config.Config, transport mcp.Transport) int {
	server := buildServer(cfg, logger)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Run(ctx, transport)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			logger.WithField("error", err).Error("server terminated")
			return 1
		}
		return 0
	case <-ctx.Done():
		return 0
	}
}
