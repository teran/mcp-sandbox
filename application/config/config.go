// Package config loads the env-driven server configuration (via envconfig) and
// validates the filesystem scope (S04).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// DefaultDockerHost is the default unix socket for the Docker daemon.
const DefaultDockerHost = "unix:///var/run/docker.sock"

// DefaultTrustedRegistries is the image-registry allowlist used when the
// operator does not configure TRUSTED_REGISTRIES (SPEC §7.9, C05/T7). Docker
// Hub and GitHub Container Registry cover the common sandbox image sources.
var DefaultTrustedRegistries = []string{"docker.io", "index.docker.io", "registry-1.docker.io", "ghcr.io"}

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
	// TrustedRegistries is the allowlist of image registries the sandbox may
	// pull images from (SPEC §7.9, C05/T7). Empty defaults to
	// DefaultTrustedRegistries in Validate.
	TrustedRegistries []string `envconfig:"TRUSTED_REGISTRIES"`
	// SandboxCgroupCPULimitCores is the per-sandbox CPU limit in cores
	// (SANDBOX_CGROUP_CPU_LIMIT_CORES). 0 means unset — the built-in
	// server default (1 CPU) applies.
	SandboxCgroupCPULimitCores uint64 `envconfig:"SANDBOX_CGROUP_CPU_LIMIT_CORES"`
	// SandboxCgroupMemoryLimitGB is the per-sandbox memory limit in gibibytes
	// (SANDBOX_CGROUP_MEMORY_LIMIT_GB). 0 means unset — the built-in server
	// default (4 GiB) applies. Swap stays disabled.
	SandboxCgroupMemoryLimitGB uint64 `envconfig:"SANDBOX_CGROUP_MEMORY_LIMIT_GB"`
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
	if len(c.TrustedRegistries) == 0 {
		// Default to the known-good registries when the operator does not
		// configure TRUSTED_REGISTRIES (SPEC §7.9). This keeps unqualified
		// references such as "alpine:3.20" (docker.io) working out of the box.
		c.TrustedRegistries = append([]string(nil), DefaultTrustedRegistries...)
	} else {
		// Fail-closed default: if every configured entry is empty/whitespace the
		// allowlist falls back to the known-good defaults (SPEC §7.9) rather than
		// silently allowing an empty allowlist (which would reject every image).
		allEmpty := true
		for _, r := range c.TrustedRegistries {
			if strings.TrimSpace(r) != "" {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			c.TrustedRegistries = append([]string(nil), DefaultTrustedRegistries...)
		} else {
			out := make([]string, 0, len(c.TrustedRegistries))
			seen := make(map[string]struct{}, len(c.TrustedRegistries))
			for i, r := range c.TrustedRegistries {
				r = strings.ToLower(strings.TrimSpace(r))
				if r == "" {
					// A single empty entry among non-empty ones is an error: the
					// operator explicitly configured the list, so a hole in it is
					// a misconfiguration, not a fall-back condition.
					return fmt.Errorf("config: TRUSTED_REGISTRIES[%d] must not be empty", i)
				}
				if err := validateRegistryEntry(r); err != nil {
					return fmt.Errorf("config: TRUSTED_REGISTRIES[%d] %q invalid registry: %w", i, c.TrustedRegistries[i], err)
				}
				if _, dup := seen[r]; dup {
					continue // dedupe (case-insensitive after lowercase)
				}
				seen[r] = struct{}{}
				out = append(out, r)
			}
			c.TrustedRegistries = out
		}
	}
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

// validateRegistryEntry enforces the strict per-entry rules for a single
// TRUSTED_REGISTRIES entry (SPEC §7.9). The entry must already be trimmed and
// lowercased. It rejects wildcard/glob characters, a scheme ("://") or leading
// "//", internal whitespace, any character outside [a-z0-9._:-], and any colon
// misuse (more than one colon, or a bare trailing colon). A colon is allowed
// only as an explicit port, e.g. "registry.homelab.teran.dev:5000".
func validateRegistryEntry(r string) error {
	if strings.ContainsAny(r, "*?[") {
		return errors.New("must not contain wildcard characters")
	}
	if strings.Contains(r, "://") || strings.HasPrefix(r, "//") {
		return errors.New("must not contain a scheme or leading //")
	}
	if strings.ContainsAny(r, " \t\r\n") {
		return errors.New("must not contain whitespace")
	}
	const registryChars = "abcdefghijklmnopqrstuvwxyz0123456789._-:"
	for _, ch := range r {
		if !strings.ContainsRune(registryChars, ch) {
			return errors.New("contains invalid characters")
		}
	}
	colons := 0
	for i, ch := range r {
		if ch != ':' {
			continue
		}
		colons++
		if i == len(r)-1 {
			return errors.New("must not end with a bare colon")
		}
	}
	if colons > 1 {
		return errors.New("must not contain more than one colon")
	}
	return nil
}
