package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// unsetEnv removes an env var and restores it after the test, to exercise
// envconfig defaults.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestLoad_DefaultsAndOverrides(t *testing.T) {
	t.Setenv("ALLOW_DIRS", "")
	t.Setenv("ALLOW_SYMLINKS", "true")
	t.Setenv("DOCKER_HOST", "unix:///tmp/test.sock")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("SANDBOX_IDLE_TIMEOUT", "1h")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DockerHost != "unix:///tmp/test.sock" {
		t.Fatalf("DockerHost = %q", c.DockerHost)
	}
	if !c.AllowSymlinks {
		t.Fatal("AllowSymlinks should be true")
	}
	if c.LogLevel != "debug" || c.LogFormat != "json" {
		t.Fatalf("log settings wrong: %+v", c)
	}
	if c.SandboxIdleTimeout != time.Hour {
		t.Fatalf("SandboxIdleTimeout = %v", c.SandboxIdleTimeout)
	}
	if len(c.AllowDirs) != 0 {
		t.Fatalf("AllowDirs = %v, want empty", c.AllowDirs)
	}
}

func TestLoad_Defaults(t *testing.T) {
	for _, k := range []string{"ALLOW_DIRS", "ALLOW_SYMLINKS", "DOCKER_HOST", "LOG_LEVEL", "LOG_FORMAT", "SANDBOX_IDLE_TIMEOUT", "SANDBOX_CGROUP_CPU_LIMIT_CORES", "SANDBOX_CGROUP_MEMORY_LIMIT_GB"} {
		unsetEnv(t, k)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DockerHost != DefaultDockerHost {
		t.Fatalf("DockerHost default = %q", c.DockerHost)
	}
	if c.AllowSymlinks {
		t.Fatal("AllowSymlinks default should be false")
	}
	if c.LogLevel != "" {
		t.Fatalf("LogLevel default = %q", c.LogLevel)
	}
	if c.LogFormat != "text" {
		t.Fatalf("LogFormat default = %q", c.LogFormat)
	}
	if c.SandboxIdleTimeout != 15*time.Minute {
		t.Fatalf("SandboxIdleTimeout default = %v", c.SandboxIdleTimeout)
	}
	if c.SandboxCwdMode != "ro" {
		t.Fatalf("SandboxCwdMode default = %q", c.SandboxCwdMode)
	}
	if c.SandboxCgroupCPULimitCores != 0 {
		t.Fatalf("SandboxCgroupCPULimitCores default = %d, want 0 (unset)", c.SandboxCgroupCPULimitCores)
	}
	if c.SandboxCgroupMemoryLimitGB != 0 {
		t.Fatalf("SandboxCgroupMemoryLimitGB default = %d, want 0 (unset)", c.SandboxCgroupMemoryLimitGB)
	}
}

func TestLoad_CwdModeOverrides(t *testing.T) {
	unsetEnv(t, "ALLOW_DIRS")
	unsetEnv(t, "SANDBOX_CWD_MODE")
	t.Setenv("SANDBOX_CWD_MODE", "rw")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SandboxCwdMode != "rw" {
		t.Fatalf("SandboxCwdMode = %q, want rw", c.SandboxCwdMode)
	}
}

func TestValidate_InvalidCwdMode(t *testing.T) {
	c := &Config{SandboxCwdMode: "bogus"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for invalid SANDBOX_CWD_MODE")
	}
	if !strings.Contains(err.Error(), "SANDBOX_CWD_MODE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_MultipleAllowDirs(t *testing.T) {
	d1 := t.TempDir()
	d2 := t.TempDir()
	t.Setenv("ALLOW_DIRS", d1+","+d2)
	t.Setenv("LOG_LEVEL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.AllowDirs) != 2 {
		t.Fatalf("AllowDirs = %v, want 2 entries", c.AllowDirs)
	}
}

func TestValidate_CanonicalizesAllowDirs(t *testing.T) {
	dir := t.TempDir()
	c := &Config{AllowDirs: []string{dir}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if c.AllowDirs[0] != want {
		t.Fatalf("AllowDirs[0] = %q, want canonical %q", c.AllowDirs[0], want)
	}
}

func TestValidate_RejectsMissingDir(t *testing.T) {
	c := &Config{AllowDirs: []string{"/definitely/does/not/exist/xyz"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
	if !strings.Contains(err.Error(), "ALLOW_DIRS[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidate_ExpandsTildeInAllowDirs verifies that a leading ~ in an
// ALLOW_DIRS entry is expanded to the user's home directory before
// canonicalization. Regression: the server previously failed to start with
// `config: ALLOW_DIRS[0] "~/...": lstat ~: no such file or directory` because
// the tilde was treated literally by EvalSymlinks.
//
// The test overrides $HOME with a writable temp dir so it is independent of the
// real home (which may be read-only inside a sandbox with a read-only rootfs).
func TestValidate_ExpandsTildeInAllowDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a real subdir under the (overridden) home so EvalSymlinks succeeds.
	dir := filepath.Join(home, ".mcp-sandbox-test-expand-"+t.Name())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	c := &Config{AllowDirs: []string{"~/" + filepath.Base(dir)}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// Validate() canonicalizes via EvalSymlinks, which on macOS resolves the
	// /var -> /private/var symlink. Compare against the canonical form.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if c.AllowDirs[0] != want {
		t.Fatalf("AllowDirs[0] = %q, want expanded %q", c.AllowDirs[0], want)
	}
}

func TestValidate_InvalidLogFormat(t *testing.T) {
	c := &Config{LogFormat: "yaml"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for invalid LOG_FORMAT")
	}
	if !strings.Contains(err.Error(), "LOG_FORMAT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_InvalidIdleTimeout(t *testing.T) {
	t.Setenv("SANDBOX_IDLE_TIMEOUT", "not-a-duration")
	t.Setenv("LOG_LEVEL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid SANDBOX_IDLE_TIMEOUT")
	}
}

func TestValidate_ValidLogFormats(t *testing.T) {
	for _, f := range []string{"", "text", "json"} {
		c := &Config{LogFormat: f}
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate(format=%q): %v", f, err)
		}
	}
}

func TestLoad_AllowNetworkModes(t *testing.T) {
	t.Setenv("ALLOW_NETWORK_MODES", "bridge,host")
	t.Setenv("LOG_LEVEL", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.AllowNetworkModes) != 2 || c.AllowNetworkModes[0] != "bridge" || c.AllowNetworkModes[1] != "host" {
		t.Fatalf("AllowNetworkModes = %v, want [bridge host]", c.AllowNetworkModes)
	}
}

func TestValidate_AllowNetworkModesDefaultsToBridge(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(c.AllowNetworkModes) != 1 || c.AllowNetworkModes[0] != "bridge" {
		t.Fatalf("AllowNetworkModes default = %v, want [bridge]", c.AllowNetworkModes)
	}
}

func TestValidate_InvalidNetworkMode(t *testing.T) {
	c := &Config{AllowNetworkModes: []string{"bridge", "wifi"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for invalid network mode")
	}
	if !strings.Contains(err.Error(), "ALLOW_NETWORK_MODES[1]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_ValidNetworkModes(t *testing.T) {
	for _, modes := range [][]string{{"none"}, {"bridge"}, {"host"}, {"none", "bridge", "host"}} {
		c := &Config{AllowNetworkModes: modes}
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate(%v): %v", modes, err)
		}
	}
}

func TestLoad_UnknownNetworkModeFailsValidation(t *testing.T) {
	t.Setenv("ALLOW_NETWORK_MODES", "host,lan")
	t.Setenv("LOG_LEVEL", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for unknown network mode")
	}
}

func TestLoad_TrustedRegistries(t *testing.T) {
	t.Setenv("TRUSTED_REGISTRIES", "docker.io, registry.homelab.teran.dev, ghcr.io")
	t.Setenv("LOG_LEVEL", "")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := []string{"docker.io", "registry.homelab.teran.dev", "ghcr.io"}
	if len(c.TrustedRegistries) != len(want) {
		t.Fatalf("TrustedRegistries = %v, want %v", c.TrustedRegistries, want)
	}
	for i := range want {
		if c.TrustedRegistries[i] != want[i] {
			t.Fatalf("TrustedRegistries[%d] = %q, want %q (trimmed)", i, c.TrustedRegistries[i], want[i])
		}
	}
}

func TestValidate_TrustedRegistriesDefaults(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(c.TrustedRegistries) != len(DefaultTrustedRegistries) {
		t.Fatalf("TrustedRegistries default = %v, want %v", c.TrustedRegistries, DefaultTrustedRegistries)
	}
	for i := range DefaultTrustedRegistries {
		if c.TrustedRegistries[i] != DefaultTrustedRegistries[i] {
			t.Fatalf("TrustedRegistries[%d] = %q, want %q", i, c.TrustedRegistries[i], DefaultTrustedRegistries[i])
		}
	}
}

func TestValidate_TrustedRegistriesRejectsEmptyEntry(t *testing.T) {
	c := &Config{TrustedRegistries: []string{"docker.io", " ", "ghcr.io"}}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for empty TRUSTED_REGISTRIES entry")
	}
	if !strings.Contains(err.Error(), "TRUSTED_REGISTRIES[1]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_TrustedRegistriesWhitespaceOnlyDefaults(t *testing.T) {
	c := &Config{TrustedRegistries: []string{" ", "  "}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(c.TrustedRegistries) != len(DefaultTrustedRegistries) {
		t.Fatalf("TrustedRegistries = %v, want defaults %v", c.TrustedRegistries, DefaultTrustedRegistries)
	}
	for i := range DefaultTrustedRegistries {
		if c.TrustedRegistries[i] != DefaultTrustedRegistries[i] {
			t.Fatalf("TrustedRegistries[%d] = %q, want %q", i, c.TrustedRegistries[i], DefaultTrustedRegistries[i])
		}
	}
}

func TestValidate_TrustedRegistriesRejectsWildcards(t *testing.T) {
	for _, bad := range []string{"*.example.com", "reg?istry.io", "reg[0]istry.io"} {
		c := &Config{TrustedRegistries: []string{bad}}
		err := c.Validate()
		if err == nil {
			t.Fatalf("expected error for wildcard entry %q", bad)
		}
		if !strings.Contains(err.Error(), "TRUSTED_REGISTRIES[0]") || !strings.Contains(err.Error(), "invalid registry") {
			t.Fatalf("unexpected error for %q: %v", bad, err)
		}
	}
}

func TestValidate_TrustedRegistriesRejectsScheme(t *testing.T) {
	for _, bad := range []string{"https://registry.example.com", "//registry.example.com", "registry.example.com:5000/evil"} {
		c := &Config{TrustedRegistries: []string{bad}}
		if err := c.Validate(); err == nil {
			t.Fatalf("expected error for entry %q", bad)
		}
	}
}

func TestValidate_TrustedRegistriesLowercases(t *testing.T) {
	c := &Config{TrustedRegistries: []string{"DOCKER.IO", "GHCR.IO"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := []string{"docker.io", "ghcr.io"}
	if len(c.TrustedRegistries) != len(want) {
		t.Fatalf("TrustedRegistries = %v, want %v", c.TrustedRegistries, want)
	}
	for i := range want {
		if c.TrustedRegistries[i] != want[i] {
			t.Fatalf("TrustedRegistries[%d] = %q, want lowercased %q", i, c.TrustedRegistries[i], want[i])
		}
	}
}

func TestValidate_TrustedRegistriesDedupes(t *testing.T) {
	c := &Config{TrustedRegistries: []string{"docker.io", "docker.io", "ghcr.io", "DOCKER.IO"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(c.TrustedRegistries) != 2 {
		t.Fatalf("TrustedRegistries = %v, want deduped 2 entries", c.TrustedRegistries)
	}
}

func TestValidate_TrustedRegistriesAcceptsPort(t *testing.T) {
	c := &Config{TrustedRegistries: []string{"registry.homelab.teran.dev:5000"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(c.TrustedRegistries) != 1 || c.TrustedRegistries[0] != "registry.homelab.teran.dev:5000" {
		t.Fatalf("TrustedRegistries = %v, want [registry.homelab.teran.dev:5000]", c.TrustedRegistries)
	}
}

func TestValidate_TrustedRegistriesRejectsColonMisuse(t *testing.T) {
	for _, bad := range []string{"registry:", "a:b:c"} {
		c := &Config{TrustedRegistries: []string{bad}}
		if err := c.Validate(); err == nil {
			t.Fatalf("expected error for colon misuse %q", bad)
		}
	}
}

func TestLoad_CgroupResourceLimits(t *testing.T) {
	unsetEnv(t, "SANDBOX_CGROUP_CPU_LIMIT_CORES")
	unsetEnv(t, "SANDBOX_CGROUP_MEMORY_LIMIT_GB")
	t.Setenv("SANDBOX_CGROUP_CPU_LIMIT_CORES", "2")
	t.Setenv("SANDBOX_CGROUP_MEMORY_LIMIT_GB", "8")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SandboxCgroupCPULimitCores != 2 {
		t.Fatalf("SandboxCgroupCPULimitCores = %d, want 2", c.SandboxCgroupCPULimitCores)
	}
	if c.SandboxCgroupMemoryLimitGB != 8 {
		t.Fatalf("SandboxCgroupMemoryLimitGB = %d, want 8", c.SandboxCgroupMemoryLimitGB)
	}
}

func TestLoad_CgroupResourceLimitsInvalid(t *testing.T) {
	unsetEnv(t, "SANDBOX_CGROUP_CPU_LIMIT_CORES")
	unsetEnv(t, "SANDBOX_CGROUP_MEMORY_LIMIT_GB")
	t.Setenv("SANDBOX_CGROUP_CPU_LIMIT_CORES", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid SANDBOX_CGROUP_CPU_LIMIT_CORES")
	}
}
