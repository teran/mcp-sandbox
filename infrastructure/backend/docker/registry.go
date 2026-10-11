package docker

import (
	"context"
	"time"

	"github.com/docker/docker/api/types"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

// init registers the docker backend into the process-wide backend registry
// (SPEC §6.3). Availability is NOT probed here — it is evaluated lazily by the
// registry Select during the first sandbox_create (A02/N35); no outbound call
// happens at startup.
func init() {
	_ = backend.Register(backend.Factory{
		Name:         name,
		Priority:     priority,
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true},
		Available:    dockerAvailable,
		New:          newFromConfig,
	})
}

// dockerAvailable reports whether the docker daemon is reachable. It is
// evaluated lazily inside Select/sandbox_create — never at startup (A02/N35).
// dockerPingTimeout bounds the startup laziness probe (SPEC §7.9). Evaluated
// lazily inside Select/sandbox_create — never at startup (A02/N35).
const dockerPingTimeout = 2 * time.Second

func dockerAvailable() bool {
	cli, err := newClient("")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerPingTimeout)
	defer cancel()

	return pingOK(ctx, cli.Ping)
}

// pingOK reports whether the given ping succeeds. It is separated so the
// availability decision is directly testable without a real daemon.
func pingOK(ctx context.Context, ping func(context.Context) (types.Ping, error)) bool {
	_, err := ping(ctx)

	return err == nil
}

// newFromConfig constructs the docker backend for the given server config,
// wiring a real SDK client over the configured unix socket.
func newFromConfig(cfg backend.Config) (role.Backend, error) {
	cli, err := newClient(cfg.DockerHost)
	if err != nil {
		return nil, err
	}

	return New(cli, cfg.AllowDirs, WithAllowSymlinks(cfg.AllowSymlinks)), nil
}
