// Package config loads the env-driven server configuration (via envconfig) and
// validates the filesystem scope (S04).
package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// DefaultDockerHost is the default unix socket for the Docker daemon.
const DefaultDockerHost = "unix:///var/run/docker.sock"

// Config is the server configuration, read from environment variables. It is
// loaded at startup as local wiring only (A02/N35) — no outbound calls.
type Config struct {
	AllowDirs          []string      `envconfig:"ALLOW_DIRS"`
	AllowSymlinks      bool          `envconfig:"ALLOW_SYMLINKS"`
	AllowNetworkModes  []string      `envconfig:"ALLOW_NETWORK_MODES"`
	DockerHost         string        `envconfig:"DOCKER_HOST" default:"unix:///var/run/docker.sock"`
	LogLevel           string        `envconfig:"LOG_LEVEL"`
	LogFilename        string        `envconfig:"LOG_FILENAME"`
	LogFormat          string        `envconfig:"LOG_FORMAT" default:"text"`
	SandboxIdleTimeout time.Duration `envconfig:"SANDBOX_IDLE_TIMEOUT" default:"15m"`
	SandboxCwdMode     string        `envconfig:"SANDBOX_CWD_MODE" default:"ro"`
}

// Load reads the configuration from the process environment. It does not
// validate; call Validate afterwards.
func Load() (*Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

// Validate verifies configuration invariants. It canonicalizes every entry of
// ALLOW_DIRS with filepath.EvalSymlinks so that later path checks compare
// against resolved real paths (S04). A directory that cannot be resolved is
// rejected. It mutates c.AllowDirs in place to the canonical form.
func (c *Config) Validate() error {
	if c.LogFormat != "" && c.LogFormat != "text" && c.LogFormat != "json" {
		return fmt.Errorf("config: invalid LOG_FORMAT %q (want text or json)", c.LogFormat)
	}
	if c.SandboxCwdMode != "" && c.SandboxCwdMode != "rw" && c.SandboxCwdMode != "ro" {
		return fmt.Errorf("config: invalid SANDBOX_CWD_MODE %q (want ro or rw)", c.SandboxCwdMode)
	}
	if len(c.AllowNetworkModes) == 0 {
		// Default to the safe bridge mode when the operator does not configure
		// ALLOW_NETWORK_MODES. Host networking is never allowed implicitly — it
		// is permitted only when the operator explicitly lists it (SPEC §7.1).
		c.AllowNetworkModes = []string{string(spec.NetworkBridge)}
	}
	for i, m := range c.AllowNetworkModes {
		m = strings.TrimSpace(m)
		if !spec.IsValidNetworkMode(spec.NetworkMode(m)) {
			return fmt.Errorf("config: ALLOW_NETWORK_MODES[%d] %q invalid (want none, bridge or host)", i, m)
		}
		c.AllowNetworkModes[i] = m
	}
	for i, d := range c.AllowDirs {
		resolved, err := filepath.EvalSymlinks(d)
		if err != nil {
			return fmt.Errorf("config: ALLOW_DIRS[%d] %q: %w", i, d, err)
		}
		c.AllowDirs[i] = resolved
	}
	return nil
}
