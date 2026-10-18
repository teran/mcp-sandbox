package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types/registry"
)

// writeDockerConfig creates a temp Docker client config directory containing a
// config.json with the given auths (registry -> "user:pass"), sets $DOCKER_CONFIG
// to it, and returns the directory path.
func writeDockerConfig(t *testing.T, auths map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	authsJSON := make(map[string]any, len(auths))
	for reg, userPass := range auths {
		authsJSON[reg] = map[string]string{
			"auth": base64.StdEncoding.EncodeToString([]byte(userPass)),
		}
	}
	data, err := json.Marshal(map[string]any{"auths": authsJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dockerconfig.ConfigFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dockerconfig.EnvOverrideConfigDir, dir)

	return dir
}

// TestPullImage_RegistryAuthFromConfig verifies that when the operator's Docker
// client config has credentials for the image's registry, pullImage attaches the
// encoded RegistryAuth and the daemon can authenticate against a private
// registry (§7.11).
func TestPullImage_RegistryAuthFromConfig(t *testing.T) {
	writeDockerConfig(t, map[string]string{"ghcr.io": "user:pass"})
	api := newFakeClient()
	b := newBackend(api)

	if err := b.pullImage(context.Background(), "ghcr.io/org/app:v1"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 1 {
		t.Fatalf("pulls = %d, want 1", len(opts))
	}
	if opts[0].RegistryAuth == "" {
		t.Fatal("RegistryAuth empty, want credentials attached for a private registry")
	}
	auth, err := registry.DecodeAuthConfig(opts[0].RegistryAuth)
	if err != nil {
		t.Fatalf("decode RegistryAuth: %v", err)
	}
	if auth.Username != "user" || auth.Password != "pass" {
		t.Fatalf("decoded auth = user:%q pass:%q, want user:pass", auth.Username, auth.Password)
	}
}

// TestPullImage_RegistryAuth_DockerHubNormalized verifies that an unqualified
// image (docker.io) looks up the Docker Hub auth entry, which the Docker client
// config stores under the historical index address, via GetAuthConfig.
func TestPullImage_RegistryAuth_DockerHubNormalized(t *testing.T) {
	writeDockerConfig(t, map[string]string{"https://index.docker.io/v1/": "hub:token"})
	api := newFakeClient()
	b := newBackend(api)

	if err := b.pullImage(context.Background(), "alpine:3.20"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 1 {
		t.Fatalf("pulls = %d, want 1", len(opts))
	}
	if opts[0].RegistryAuth == "" {
		t.Fatal("RegistryAuth empty, want Docker Hub credentials attached")
	}
	auth, err := registry.DecodeAuthConfig(opts[0].RegistryAuth)
	if err != nil {
		t.Fatalf("decode RegistryAuth: %v", err)
	}
	if auth.Username != "hub" {
		t.Fatalf("decoded username = %q, want hub", auth.Username)
	}
}

// TestPullImage_RegistryWithoutCredentials_EmptyAuth verifies that a config file
// without an entry for the pulled registry leaves RegistryAuth empty and the
// pull still proceeds (public image behaviour is unchanged).
func TestPullImage_RegistryWithoutCredentials_EmptyAuth(t *testing.T) {
	// Config exists but only has credentials for a different registry.
	writeDockerConfig(t, map[string]string{"docker.io": "u:p"})
	api := newFakeClient()
	b := newBackend(api)

	if err := b.pullImage(context.Background(), "ghcr.io/org/app:v1"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 1 {
		t.Fatalf("pulls = %d, want 1", len(opts))
	}
	if opts[0].RegistryAuth != "" {
		t.Fatalf("RegistryAuth = %q, want empty for a registry without credentials", opts[0].RegistryAuth)
	}
}

// TestPullImage_NoConfigFile_EmptyAuth verifies that a missing client config
// file does not fail the pull: RegistryAuth stays empty and the pull proceeds.
func TestPullImage_NoConfigFile_EmptyAuth(t *testing.T) {
	dir := t.TempDir() // no config.json inside
	t.Setenv(dockerconfig.EnvOverrideConfigDir, dir)
	api := newFakeClient()
	b := newBackend(api)

	if err := b.pullImage(context.Background(), "ghcr.io/org/app:v1"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 1 {
		t.Fatalf("pulls = %d, want 1", len(opts))
	}
	if opts[0].RegistryAuth != "" {
		t.Fatalf("RegistryAuth = %q, want empty when no config file exists", opts[0].RegistryAuth)
	}
}

// TestPullImage_DockerConfigEnvHonoured verifies $DOCKER_CONFIG is honoured: the
// credentials are read from the directory the env var points at, and a registry
// absent from that directory is not authenticated.
func TestPullImage_DockerConfigEnvHonoured(t *testing.T) {
	writeDockerConfig(t, map[string]string{"registry.example.com": "svc:secret"})
	api := newFakeClient()
	b := newBackend(api)

	if err := b.pullImage(context.Background(), "registry.example.com/team/app:v1"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	if err := b.pullImage(context.Background(), "ghcr.io/org/app:v1"); err != nil {
		t.Fatalf("pullImage (absent registry): %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 2 {
		t.Fatalf("pulls = %d, want 2", len(opts))
	}
	// The registry listed in the $DOCKER_CONFIG dir must be authenticated.
	if opts[0].RegistryAuth == "" {
		t.Fatal("registry listed in $DOCKER_CONFIG must be authenticated")
	}
	auth, err := registry.DecodeAuthConfig(opts[0].RegistryAuth)
	if err != nil {
		t.Fatalf("decode RegistryAuth: %v", err)
	}
	if auth.Username != "svc" || auth.Password != "secret" {
		t.Fatalf("decoded auth = user:%q pass:%q, want svc:secret", auth.Username, auth.Password)
	}
	// A registry absent from that config must stay unauthenticated.
	if opts[1].RegistryAuth != "" {
		t.Fatalf("RegistryAuth = %q, want empty for registry absent from $DOCKER_CONFIG", opts[1].RegistryAuth)
	}
}

// TestRegistryAuth_InvalidRef_EmptyAuth verifies the registryAuth early-return:
// an image reference that cannot be parsed yields no registry domain, so the
// helper degrades to an empty (unauthenticated) auth. It exercises the
// registryHost parse-failure branch.
func TestRegistryAuth_InvalidRef_EmptyAuth(t *testing.T) {
	// A reference with illegal characters cannot be normalized → no domain.
	if got := registryAuth("!!!not-a-valid-ref!!!"); got != "" {
		t.Fatalf("registryAuth(invalid ref) = %q, want empty", got)
	}
}

// TestRegistryAuth_ConfigLoadError_EmptyAuth verifies that a client config that
// cannot be loaded (invalid JSON) degrades to an unauthenticated pull instead of
// failing: RegistryAuth stays empty and pullImage still reaches ImagePull. It
// exercises the dockerconfig.Load error branch.
func TestRegistryAuth_ConfigLoadError_EmptyAuth(t *testing.T) {
	dir := t.TempDir()
	// An unparsable config.json forces dockerconfig.Load to return an error.
	if err := os.WriteFile(filepath.Join(dir, dockerconfig.ConfigFileName), []byte("{ this is not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dockerconfig.EnvOverrideConfigDir, dir)

	api := newFakeClient()
	b := newBackend(api)
	if err := b.pullImage(context.Background(), "ghcr.io/org/app:v1"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 1 {
		t.Fatalf("pulls = %d, want 1", len(opts))
	}
	if opts[0].RegistryAuth != "" {
		t.Fatalf("RegistryAuth = %q, want empty when the client config fails to load", opts[0].RegistryAuth)
	}
}

// TestRegistryAuth_ConfigEntryWithEmptyAuth verifies the "empty credentials"
// branch: a config that lists the registry but holds no username/password/token
// yields an empty RegistryAuth, and the pull still proceeds (public-image
// behaviour). It exercises the auth-all-empty early return independently of a
// missing registry entry.
func TestRegistryAuth_ConfigEntryWithEmptyAuth(t *testing.T) {
	dir := t.TempDir()
	data, err := json.Marshal(map[string]any{
		"auths": map[string]any{"ghcr.io": map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, dockerconfig.ConfigFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dockerconfig.EnvOverrideConfigDir, dir)

	api := newFakeClient()
	b := newBackend(api)
	if err := b.pullImage(context.Background(), "ghcr.io/org/app:v1"); err != nil {
		t.Fatalf("pullImage: %v", err)
	}
	opts := api.pullOpts()
	if len(opts) != 1 {
		t.Fatalf("pulls = %d, want 1", len(opts))
	}
	if opts[0].RegistryAuth != "" {
		t.Fatalf("RegistryAuth = %q, want empty for an empty credentials entry", opts[0].RegistryAuth)
	}
}

// TestDockerConfigDir_DefaultHomeDir verifies the default branch of
// dockerConfigDir: when $DOCKER_CONFIG is unset it falls back to the user's
// home directory + ".docker". It exercises the UserHomeDir/Join path.
func TestDockerConfigDir_DefaultHomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Ensure the default branch is reached regardless of the ambient env:
	// dockerConfigDir treats an empty DOCKER_CONFIG as unset, and t.Setenv
	// restores the original value on cleanup.
	t.Setenv(dockerconfig.EnvOverrideConfigDir, "")
	got := dockerConfigDir()
	want := filepath.Join(home, ".docker")
	if got != want {
		t.Fatalf("dockerConfigDir() = %q, want %q", got, want)
	}
}

// TestRegistryHost_DerivesDomain verifies the registry domain derivation for a
// private registry (Phase 7 private-registry pull): a fully-qualified reference
// yields its registry host, and a success is returned for a parseable reference.
func TestRegistryHost_DerivesDomain(t *testing.T) {
	dom, err := registryHost("registry.example.com/team/app:v1")
	if err != nil {
		t.Fatalf("registryHost: %v", err)
	}
	if dom != "registry.example.com" {
		t.Fatalf("domain = %q, want registry.example.com", dom)
	}
}
