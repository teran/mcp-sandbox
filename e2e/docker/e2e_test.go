//go:build e2e

// Package docker hosts the e2e contract suite against the real Docker backend
// (SPEC §4/§7, T02/C09GO). It is build-tagged `e2e` so it is excluded from the
// default unit/coverage pass (make test) and runs only through `make e2e` /
// the e2e CI job (N30/T02). It requires a reachable Docker daemon; when Docker
// is unavailable the suite is skipped rather than failed, so the job can pass
// on runners without Docker.
package docker

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/e2e/contract"
	"github.com/teran/mcp-sandbox/infrastructure/backend/docker"
)

// e2eImage is the base image the suite runs against. It is a small busybox-based
// image with a shell-free toolset (sh, echo, printf, id, pwd, printenv) used by
// the exec scenarios. The container rootfs is mounted read-only and the
// unprivileged user (1000:1000) writes to the writable tmpfs /tmp (executable)
// and the /run tmpfs (owned by the sandbox user, secrets live under /run/sandbox).
const e2eImage = "alpine:3.20"

// dockerAvailable reports whether a Docker daemon is reachable right now. It is
// checked lazily at the start of the e2e test (A02/N35) so the suite can be
// skipped cleanly when no daemon is present.
func dockerAvailable() bool {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return false
	}
	defer cli.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = cli.Ping(ctx)
	return err == nil
}

// newBackend builds the docker backend against the real daemon (official SDK,
// unix socket per DOCKER_HOST). It first pulls the e2e base image so
// ContainerCreate can start from it (pre-provisioned images, SPEC §7.10). The
// optional allowDirs are the ALLOW_DIRS host-path gate (Phase 7) passed to
// New(api, allowDirs) — the cwd and mount sources used by a scenario must lie
// within them.
func newBackend(t *testing.T, allowDirs ...string) *docker.Backend {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rd, err := cli.ImagePull(ctx, e2eImage, image.PullOptions{})
	if err != nil {
		t.Fatalf("pull %s: %v", e2eImage, err)
	}
	if _, err := io.Copy(io.Discard, rd); err != nil {
		_ = rd.Close()
		t.Fatalf("pull %s (read): %v", e2eImage, err)
	}
	_ = rd.Close()

	return docker.New(cli, allowDirs)
}

// mkSpec returns a valid spec satisfying spec.Validate for the docker backend:
// the base image plus the full immutable Isolation hardening (SPEC §7.1).
func mkSpec() spec.SandboxSpec {
	return spec.SandboxSpec{
		Image: e2eImage,
		Isolation: spec.Isolation{
			ReadOnlyRootfs:  true,
			CapDrop:         []string{"ALL"},
			NoNewPrivileges: true,
			Seccomp:         spec.SeccompDefault,
			User:            spec.UIDGID{UID: 1000, GID: 1000},
			PidsLimit:       256,
			Memory:          256 * 1024 * 1024,
			MemorySwap:      256 * 1024 * 1024,
			NanoCPUs:        1_000_000_000,
			NetworkMode:     spec.NetworkBridge,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}
}

// TestContractSuite runs the full backend-independent contract suite against
// the real Docker backend.
func TestContractSuite(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping e2e contract suite")
	}

	allowDir := t.TempDir()
	b := newBackend(t, allowDir)

	contract.RunContractSuite(t, contract.Contract{
		Lifecycle:      b,
		Executable:     b,
		Introspectable: b,
		AllowDir:       allowDir,
	}, mkSpec)
}

// TestSecretsDelivery reproduces the read-only-rootfs secret bug: the container
// is created with a read-only rootfs (§7.1), so secrets cannot be written with
// a `docker cp` (CopyToContainer) call. Secrets must instead be delivered as
// tmpfs 0600 files at /run/sandbox/<name> (§7.3) via a `docker exec` that
// streams the value over stdin. This test asserts the secret file exists with
// the right content and mode 0600 against the real Docker daemon.
func TestSecretsDelivery(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping secrets delivery e2e")
	}

	b := newBackend(t)

	const secretName = "token"
	const secretValue = "s3cr3t-value-42"

	s := mkSpec()
	s.Secrets = []spec.SecretRef{{Name: secretName, Value: secretValue}}

	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("create sandbox with secret: %v", err)
	}
	t.Cleanup(func() { _ = b.Destroy(context.Background(), id) })

	// Read the secret file back as the sandbox user and check its content.
	res, err := b.Exec(ctx, id, exec.Request{Argv: []string{"/bin/cat", "/run/sandbox/" + secretName}})
	if err != nil {
		t.Fatalf("exec cat secret: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("cat secret exit code = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	if got := string(res.Stdout); got != secretValue {
		t.Fatalf("secret content = %q, want %q", got, secretValue)
	}

	// The secret file must be mode 0600 (§7.3).
	res, err = b.Exec(ctx, id, exec.Request{Argv: []string{"/bin/stat", "-c", "%a", "/run/sandbox/" + secretName}})
	if err != nil {
		t.Fatalf("exec stat secret: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "600" {
		t.Fatalf("secret file mode = %q, want 600", got)
	}
}

// TestTmpIsExecutable verifies the /tmp tmpfs is writable AND executable (no
// noexec, SPEC §7.1) so the sandbox can compile and run binaries (e.g. Go
// builds, govulncheck) against a read-only rootfs. A binary copied into /tmp
// must be runnable as the unprivileged sandbox user.
func TestTmpIsExecutable(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping /tmp executability e2e")
	}

	b := newBackend(t)

	s := mkSpec()
	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() { _ = b.Destroy(context.Background(), id) })

	// Copy a real executable into /tmp and run it: under the old noexec layout
	// this would fail with "Permission denied".
	res, err := b.Exec(ctx, id, exec.Request{Argv: []string{"/bin/cp", "/bin/true", "/tmp/sandbox-true"}})
	if err != nil {
		t.Fatalf("exec cp into /tmp: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("cp into /tmp exit = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
	res, err = b.Exec(ctx, id, exec.Request{Argv: []string{"/tmp/sandbox-true"}})
	if err != nil {
		t.Fatalf("exec /tmp binary: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("/tmp must be executable: exit = %d, stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestNetworkBridgeConnectivity verifies the default bridged network: a sandbox
// created without an explicit network reaches the network over Docker's default
// bridge, i.e. it can resolve and fetch a remote resource. This guards the
// default-network-mode change from `none` to `bridge`.
func TestNetworkBridgeConnectivity(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping bridge connectivity e2e")
	}

	b := newBackend(t)

	s := mkSpec()
	s.Isolation.NetworkMode = spec.NetworkBridge

	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("create sandbox with bridge network: %v", err)
	}
	t.Cleanup(func() { _ = b.Destroy(context.Background(), id) })

	res, err := b.Exec(ctx, id, exec.Request{Argv: []string{"/usr/bin/wget", "-q", "-O", "/dev/null", "http://example.com"}})
	if err != nil {
		t.Fatalf("exec wget: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("bridge network fetch failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestNetworkHostAllowedAndApplied verifies Phase 7: with the network-allowlist
// removed, host networking is a valid mode and the backend applies it (the
// fail-closed host opt-in is enforced at the profile/config layer, not here).
func TestNetworkHostAllowedAndApplied(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping host e2e")
	}

	b := newBackend(t)

	s := mkSpec()
	s.Isolation.NetworkMode = spec.NetworkHost

	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("host should be allowed: %v", err)
	}
	t.Cleanup(func() { _ = b.Destroy(context.Background(), id) })

	ins, err := b.Info(context.Background(), id)
	if err != nil {
		t.Fatalf("inspect sandbox: %v", err)
	}
	resp, ok := ins.(container.InspectResponse)
	if !ok {
		t.Fatalf("Info returned %T, want container.InspectResponse", ins)
	}
	if resp.HostConfig == nil || string(resp.HostConfig.NetworkMode) != "host" {
		t.Fatalf("container NetworkMode = %v, want host", resp.HostConfig.NetworkMode)
	}
}

// TestAutoPullMissingImage verifies the backend auto-pulls a trusted image that
// is not present in the local cache before creating the container (SPEC §7.9).
// It picks a small trusted image, confirms it is absent locally, then creates a
// sandbox without pre-pulling it — success proves the backend pulled it on
// demand from a trusted registry (and, as a bonus, that the image became
// available locally afterward).
func TestAutoPullMissingImage(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping auto-pull e2e")
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	// A specific patch tag is unlikely to be pre-cached; if it happens to be,
	// we skip rather than create a container without exercising the pull path.
	const pullImage = "alpine:3.20.6"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if imagePresent(ctx, cli, pullImage) {
		t.Skipf("image %s already present locally; cannot exercise auto-pull", pullImage)
	}

	// New signature: New(api, allowDirs). nil allowDirs is fine here — this
	// scenario declares no host mounts/cwd.
	b := docker.New(cli, nil)

	s := mkSpec()
	s.Image = pullImage
	id := uuid.NewString()
	if _, err := b.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("create sandbox with non-local image (should auto-pull): %v", err)
	}
	t.Cleanup(func() { _ = b.Destroy(context.Background(), id) })

	if !imagePresent(ctx, cli, pullImage) {
		t.Fatalf("image %s not present after create; expected the backend to pull it", pullImage)
	}
}

// TestCreate_OpenCodeSessionLabel verifies that a container created with a
// client (OpenCode) session id carries the dedicated traceability label, so
// each sandbox container is traceable to the OpenCode session that created it.
func TestCreate_OpenCodeSessionLabel(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker daemon not available; skipping opencode label e2e")
	}

	b := newBackend(t)

	s := mkSpec()
	s.ClientSessionID = "opencode-e2e-session"

	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() { _ = b.Destroy(context.Background(), id) })

	ins, err := b.Info(context.Background(), id)
	if err != nil {
		t.Fatalf("inspect sandbox: %v", err)
	}
	resp, ok := ins.(container.InspectResponse)
	if !ok {
		t.Fatalf("Info returned %T, want container.InspectResponse", ins)
	}
	if resp.Config == nil || resp.Config.Labels["dev.teran.sandbox.opencode_session"] != "opencode-e2e-session" {
		t.Fatalf("opencode session label missing on container: %v", resp.Config.Labels)
	}
}

// imagePresent reports whether the given image reference is present in the
// local daemon's image cache (matched against its repository tags).
func imagePresent(ctx context.Context, cli *client.Client, ref string) bool {
	summaries, err := cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return false
	}
	for _, s := range summaries {
		for _, tag := range s.RepoTags {
			if tag == ref {
				return true
			}
		}
	}
	return false
}
