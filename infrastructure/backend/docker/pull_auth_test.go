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
	b := newBackendForDirs(api)

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
	b := newBackendForDirs(api)

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
	b := newBackendForDirs(api)

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
	b := newBackendForDirs(api)

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
	b := newBackendForDirs(api)

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
