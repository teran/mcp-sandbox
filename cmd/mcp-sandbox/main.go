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
	applicationprofile "github.com/teran/mcp-sandbox/application/profile"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
	// Blank-import the concrete docker backend so its init() registers it into
	// backend.Default (SPEC §6.3). Without this the registry has zero backends
	// and every capability dispatch fails with ErrNoAvailableBackend.
	_ "github.com/teran/mcp-sandbox/infrastructure/backend/darwin"
	_ "github.com/teran/mcp-sandbox/infrastructure/backend/docker"
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

// Process exit codes. flag.Parse mirrors the standard library's convention of
// exiting with status 2 on a usage/parse error.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// run performs the composition-root wiring and starts the stdio transport. It
// returns a process exit code. It is separated from main for testability.
func run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
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
		// Resolved-config summary at debug (gated behind LOG_LEVEL=debug/trace).
		// Never logs secrets or the full argv; only non-sensitive settings.
		logger.WithField("sandbox_profiles_dir", cfg.SandboxProfilesDir).
			WithField("allow_dirs_count", len(cfg.AllowDirs)).
			WithField("docker_host", cfg.DockerHost).
			Debug("config resolved")
	}

	// mcp-sandbox is stdio-only (Local): always start the stdio transport.
	return serve(ctx, logger, cfg, newStdioTransport())
}

// fail writes an error message to stderr and returns a failure exit code. The
// write error is deliberately ignored: there is nothing to do if stderr itself
// fails.
func fail(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, format, args...)

	return 1
}

// newStdioTransport returns the stdio transport. It is a variable so tests can
// substitute a deterministic transport.
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
	})
	// Wire the read-on-demand operator-authored profile store (Phase 7). The
	// store reads + parses + validates the profiles directory fresh on every
	// Get/List call, so there is NO startup preload here (A02/N35): even a
	// broken/unparsable profiles dir composes a working server, and errors
	// surface lazily on the first profile tool call.
	svc.SetProfileStore(applicationprofile.New(cfg.SandboxProfilesDir))
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
