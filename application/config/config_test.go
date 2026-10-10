package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// unsetEnv removes env vars and restores their prior state after the test, to
// exercise envconfig defaults. restoreEnv is a separate, non-*testing.T helper
// so usetesting does not flag the os.Setenv restore — there is no t.Setenv
// equivalent that can "unset" a variable.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		old, had := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			restoreEnv(key, old, had)
		})
	}
}

func restoreEnv(key, value string, had bool) {
	if had {
		os.Setenv(key, value)
	} else {
		os.Unsetenv(key)
	}
}

func TestLoad_DefaultsAndOverrides(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/test.sock")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("SANDBOX_IDLE_TIMEOUT", "1h")
	t.Setenv("SANDBOX_PROFILES_DIR", "/etc/sandbox/profiles")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DockerHost != "unix:///tmp/test.sock" {
		t.Fatalf("DockerHost = %q", c.DockerHost)
	}
	if c.LogLevel != "debug" || c.LogFormat != "json" {
		t.Fatalf("log settings wrong: %+v", c)
	}
	if c.SandboxIdleTimeout != time.Hour {
		t.Fatalf("SandboxIdleTimeout = %v", c.SandboxIdleTimeout)
	}
	if c.SandboxProfilesDir != "/etc/sandbox/profiles" {
		t.Fatalf("SandboxProfilesDir = %q", c.SandboxProfilesDir)
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Truly unset these vars (not empty — envconfig rejects "" for time.Duration)
	// so envconfig defaults are exercised. restoreEnv re-applies the prior state.
	unsetEnv(t, "DOCKER_HOST", "LOG_LEVEL", "LOG_FORMAT", "SANDBOX_IDLE_TIMEOUT", "SANDBOX_PROFILES_DIR")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DockerHost != DefaultDockerHost {
		t.Fatalf("DockerHost default = %q", c.DockerHost)
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
	if c.SandboxProfilesDir != "~/.config/sandbox/profiles" {
		t.Fatalf("SandboxProfilesDir default = %q, want ~/.config/sandbox/profiles", c.SandboxProfilesDir)
	}
}

// TestLoad_RemovedEnvsAreInert pins Phase 7: the trusted-registry / network /
// cwd-mode / cgroup envs are gone. Setting them must not fail startup, and they
// must not appear on the Config (removed fields — verified by absence at compile
// time). ALLOW_DIRS is deliberately NOT listed: it is KEPT (the host-path gate).
func TestLoad_RemovedEnvsAreInert(t *testing.T) {
	t.Setenv("TRUSTED_REGISTRIES", "docker.io,ghcr.io")
	t.Setenv("ALLOW_SYMLINKS", "true")
	t.Setenv("ALLOW_NETWORK_MODES", "bridge,host")
	t.Setenv("SANDBOX_CWD_MODE", "rw")
	t.Setenv("SANDBOX_CGROUP_CPU_LIMIT_CORES", "2")
	t.Setenv("SANDBOX_CGROUP_MEMORY_LIMIT_GB", "8")
	t.Setenv("LOG_LEVEL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load with removed envs must still succeed: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate with removed envs must still succeed: %v", err)
	}
}

// TestValidate_AllowDirsCanonicalizes pins that ALLOW_DIRS is the KEPT host-path
// gate (Phase 7) and every entry is canonicalized with EvalSymlinks on Validate
// so later client-cwd / mount-source containment compares against the resolved
// real roots (S04).
func TestValidate_AllowDirsCanonicalizes(t *testing.T) {
	dir := t.TempDir()
	c := &Config{AllowDirs: []string{dir}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AllowDirs) != 1 || c.AllowDirs[0] != want {
		t.Fatalf("AllowDirs = %v, want canonical [%q]", c.AllowDirs, want)
	}
}

// TestValidate_AllowDirsRejectsMissing confirms a non-existent ALLOW_DIRS entry
// fails Validate (fail-closed), which is the guard that also protects the
// client-supplied cwd and mount sources at runtime.
func TestValidate_AllowDirsRejectsMissing(t *testing.T) {
	c := &Config{AllowDirs: []string{filepath.Join(t.TempDir(), "does-not-exist")}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for a missing ALLOW_DIRS entry")
	}
}

func TestLoad_InvalidIdleTimeout(t *testing.T) {
	t.Setenv("SANDBOX_IDLE_TIMEOUT", "not-a-duration")
	t.Setenv("LOG_LEVEL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid SANDBOX_IDLE_TIMEOUT")
	}
}

func TestValidate_InvalidLogFormat(t *testing.T) {
	c := &Config{LogFormat: "yaml"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for invalid LOG_FORMAT")
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

// TestValidate_ExpandsProfilesDirTilde verifies the default
// ~/.config/sandbox/profiles is expanded to the real home path on Validate
// (Phase 7). It overrides $HOME with a writable temp dir so the test is
// independent of the real home.
func TestValidate_ExpandsProfilesDirTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	c := &Config{SandboxProfilesDir: "~/.config/sandbox/profiles"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := filepath.Join(home, ".config", "sandbox", "profiles")
	if c.SandboxProfilesDir != want {
		t.Fatalf("SandboxProfilesDir = %q, want expanded %q", c.SandboxProfilesDir, want)
	}
}

func TestValidate_ProfilesDirNoTildeUnchanged(t *testing.T) {
	c := &Config{SandboxProfilesDir: "/etc/sandbox/profiles"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.SandboxProfilesDir != "/etc/sandbox/profiles" {
		t.Fatalf("SandboxProfilesDir = %q", c.SandboxProfilesDir)
	}
}

// TestValidate_ProfilesDirBareTilde verifies that a bare "~" SandboxProfilesDir
// expands to the user's home directory itself (expandHome's p == "~" branch).
func TestValidate_ProfilesDirBareTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	c := &Config{SandboxProfilesDir: "~"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.SandboxProfilesDir != home {
		t.Fatalf("SandboxProfilesDir = %q, want expanded home %q", c.SandboxProfilesDir, home)
	}
}
