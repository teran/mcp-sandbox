// Package config loads the env-driven server configuration (via envconfig) and
// validates the filesystem scope (S04).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// DefaultDockerHost is the default unix socket for the Docker daemon.
const DefaultDockerHost = "unix:///var/run/docker.sock"

// DefaultSandboxProfilesDir is the default operator-authored profiles
// directory. The "~" prefix is expanded to the user's home directory on
// Validate.
const DefaultSandboxProfilesDir = "~/.config/sandbox/profiles"

// Config is the server configuration, read from environment variables. It is
// loaded at startup as local wiring only (A02/N35) — no outbound calls.
type Config struct {
	AllowDirs          []string      `envconfig:"ALLOW_DIRS"`
	AllowSymlinks      bool          `envconfig:"ALLOW_SYMLINKS"`
	DockerHost         string        `envconfig:"DOCKER_HOST" default:"unix:///var/run/docker.sock"`
	LogLevel           string        `envconfig:"LOG_LEVEL"`
	LogFilename        string        `envconfig:"LOG_FILENAME"`
	LogFormat          string        `envconfig:"LOG_FORMAT" default:"text"`
	SandboxIdleTimeout time.Duration `envconfig:"SANDBOX_IDLE_TIMEOUT" default:"15m"`
	// SandboxProfilesDir is the operator-authored profiles directory (Phase 7,
	// SPEC §7.11). A leading "~" is expanded to the user's home on Validate.
	// The directory is scanned read-only for <name>.yml profiles.
	SandboxProfilesDir string `envconfig:"SANDBOX_PROFILES_DIR" default:"~/.config/sandbox/profiles"`
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
// against resolved real paths (S04), and expands a leading "~" in
// SandboxProfilesDir to the user's home. A directory that cannot be resolved
// is rejected. It mutates c.AllowDirs in place to the canonical form.
func (c *Config) Validate() error {
	if c.LogFormat != "" && c.LogFormat != "text" && c.LogFormat != "json" {
		return fmt.Errorf("config: invalid LOG_FORMAT %q (want text or json)", c.LogFormat)
	}
	expanded, err := expandHome(c.SandboxProfilesDir)
	if err != nil {
		return fmt.Errorf("config: SANDBOX_PROFILES_DIR %q: %w", c.SandboxProfilesDir, err)
	}
	c.SandboxProfilesDir = expanded
	for i, d := range c.AllowDirs {
		expanded, err := expandHome(d)
		if err != nil {
			return fmt.Errorf("config: ALLOW_DIRS[%d] %q: %w", i, d, err)
		}
		resolved, err := filepath.EvalSymlinks(expanded)
		if err != nil {
			return fmt.Errorf("config: ALLOW_DIRS[%d] %q: %w", i, d, err)
		}
		c.AllowDirs[i] = resolved
	}
	return nil
}

// expandHome expands a leading "~" or "~/" in p to the current user's home
// directory. It uses os.UserHomeDir (respecting $HOME). A bare "~" resolves to
// the home directory itself. Paths not starting with "~" are returned
// unchanged. This runs before EvalSymlinks, which does not expand tilde.
func expandHome(p string) (string, error) {
	if p == "~" {
		return os.UserHomeDir()
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
