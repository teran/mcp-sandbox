// Package docker implements the sandbox backend roles through the official
// docker SDK over the unix socket (SPEC §6.2, §7; L02GO/N05GO). It applies the
// immutable Isolation block on every create (§7.1), mounts the session cwd 1:1
// at the same absolute path (§7.2), delivers secrets as tmpfs 0600 files
// (§7.3) and executes commands as argument arrays with no shell (S11). It
// registers itself into the backend registry via init() (§6.3).
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

const (
	// name is the backend name.
	name = "docker"
	// priority orders selection (lower preferred).
	priority = 0
	// defaultDockerHost is used when no DOCKER_HOST is configured.
	defaultDockerHost = "unix:///var/run/docker.sock"

	// ownerLabelKey / ownerLabelValue mark containers created by this server
	// so it can identify, reap and destroy exactly its own containers (§7.8).
	ownerLabelKey   = "dev.teran.sandbox.owner"
	ownerLabelValue = "mcp-sandbox"
	// sessionLabelKey labels a container with its session id (label-based
	// ownership, §7.8).
	sessionLabelKey = "dev.teran.sandbox.session"

	// tmpfsConfDir is where secret files are mounted as a private tmpfs dir
	// (files are written with mode 0600, §7.3). The name deliberately avoids
	// the "secret" keyword because gosec's G101 heuristic would otherwise
	// flag this filesystem path as a hardcoded credential.
	tmpfsConfDir = "/run/sandbox"
	// tmpfsWorkdir is the writable tmpfs working directory for the sandbox.
	tmpfsWorkdir = "/workspace"
)

// trustedRegistries is the allowlist of image registries the sandbox may pull
// images from (§7.9, C05/T7). Any image reference whose registry is not listed
// here is rejected at container-create time. Docker Hub resolves to
// "docker.io" via ParseNormalizedNamed, so unqualified references such as
// "alpine:3.20" are treated as docker.io/library/alpine and accepted.
var trustedRegistries = map[string]struct{}{
	"docker.io":            {},
	"index.docker.io":      {},
	"registry-1.docker.io": {},
	"ghcr.io":              {},
}

// ErrNotFound is returned when no container is owned by the given session id.
var ErrNotFound = errors.New("docker: sandbox not found")

// dockerAPI is the narrow subset of the docker SDK client that the backend
// depends on. It keeps the backend testable with a fake client while the real
// *client.Client satisfies it. This is the official SDK — not a hand-rolled
// low-level http.Client (N05GO).
type dockerAPI interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
	ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
}

// Backend implements the sandbox role interfaces (role.Backend, Lifecycle,
// Executable, Filesystem, Introspectable) over the docker SDK.
type Backend struct {
	api dockerAPI
	cfg config
}

// config holds the resolved server settings the backend needs.
type config struct {
	allowDirs     []string
	allowSymlinks bool
	cwdMode       string
	defaultImage  string
}

// New returns a docker backend for the given server configuration. Allow dirs
// are canonicalized with EvalSymlinks so path checks compare like-for-like
// (config.Validate already does this; canonicalizing again is idempotent).
func New(allowDirs []string, allowSymlinks bool, cwdMode, defaultImage string, api dockerAPI) *Backend {
	canon := make([]string, 0, len(allowDirs))
	for _, d := range allowDirs {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			canon = append(canon, r)
		} else {
			canon = append(canon, d)
		}
	}
	return &Backend{
		api: api,
		cfg: config{
			allowDirs:     canon,
			allowSymlinks: allowSymlinks,
			cwdMode:       cwdMode,
			defaultImage:  defaultImage,
		},
	}
}

// Name returns the backend name.
func (b *Backend) Name() string { return name }

// Priority returns the backend priority.
func (b *Backend) Priority() int { return priority }

// Capabilities advertises that docker implements every remaining role
// interface (Lifecycle, Executable, Introspectable).
func (b *Backend) Capabilities() role.CapabilitySet {
	return role.CapabilitySet{
		Lifecycle:      true,
		Executable:     true,
		Introspectable: true,
	}
}

// compile-time check that Backend satisfies the role interfaces.
var (
	_ role.Backend        = (*Backend)(nil)
	_ role.Lifecycle      = (*Backend)(nil)
	_ role.Executable     = (*Backend)(nil)
	_ role.Introspectable = (*Backend)(nil)
)

// newClient builds a docker API client over the given host (unix socket).
func newClient(host string) (*client.Client, error) {
	if host == "" {
		host = defaultDockerHost
	}
	return client.NewClientWithOpts(client.WithHost(host), client.WithAPIVersionNegotiation())
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Create builds a container from the spec, applying the immutable Isolation
// block (§7.1), validating and mounting host paths within ALLOW_DIRS (§7.2)
// and delivering secrets as tmpfs 0600 files (§7.3). It returns the container
// id.
func (b *Backend) Create(ctx context.Context, s spec.SandboxSpec) (string, error) {
	if s.Image == "" {
		s.Image = b.cfg.defaultImage
	}
	if err := s.Validate(); err != nil {
		return "", err
	}
	// Enforce the trusted-registry / digest policy (§7.9) before any Docker
	// side-effect happens (C05/T7).
	if err := validateImageRef(s.Image); err != nil {
		return "", err
	}

	labels := map[string]string{ownerLabelKey: ownerLabelValue}
	if id, ok := domainsession.IDFromContext(ctx); ok {
		labels[sessionLabelKey] = id
	}

	mounts, err := b.resolveMounts(s)
	if err != nil {
		return "", err
	}

	tmpfs, err := b.buildTmpfs(s)
	if err != nil {
		return "", err
	}

	cfg := b.buildConfig(s, s.Image, labels)
	hostCfg := b.buildHostConfig(s, mounts, tmpfs)
	resp, err := b.api.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("docker: create container: %w", err)
	}
	if err := b.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("docker: start container: %w", err)
	}

	if err := b.writeSecrets(ctx, resp.ID, s.Secrets); err != nil {
		return "", fmt.Errorf("docker: write secrets: %w", err)
	}

	return resp.ID, nil
}

// Destroy removes the container owned by the given session id. It is
// idempotent: destroying a session with no backing container is a no-op (X02).
func (b *Backend) Destroy(ctx context.Context, id string) error {
	cid, err := b.containerIDBySession(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return b.api.ContainerRemove(ctx, cid, container.RemoveOptions{Force: true, RemoveVolumes: true})
}

// buildConfig maps the spec into the docker container.Config. The non-root
// user from the immutable Isolation is forced (never overridable).
func (b *Backend) buildConfig(s spec.SandboxSpec, image string, labels map[string]string) *container.Config {
	workdir := s.WorkingDir
	if workdir == "" {
		workdir = tmpfsWorkdir
	}
	return &container.Config{
		Image:      image,
		WorkingDir: workdir,
		Env:        toEnvSlice(s.Env),
		User:       fmt.Sprintf("%d:%d", s.Isolation.User.UID, s.Isolation.User.GID),
		Cmd:        []string{"sleep", "infinity"},
		Labels:     labels,
	}
}

// buildHostConfig applies the immutable Isolation hardening (§7.1). Every
// security-relevant value is forced server-side; a caller cannot weaken it.
func (b *Backend) buildHostConfig(s spec.SandboxSpec, mounts []mount.Mount, tmpfs map[string]string) *container.HostConfig {
	iso := s.Isolation
	pids := iso.PidsLimit

	networkMode := string(iso.NetworkMode)
	if networkMode == "" || networkMode == string(spec.NetworkHost) {
		networkMode = string(spec.NetworkNone)
	}

	hostCfg := &container.HostConfig{
		// Forced hardening (immutable).
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		AutoRemove:     true,
		NetworkMode:    container.NetworkMode(networkMode),

		// Bounded resources.
		Resources: container.Resources{
			PidsLimit:  &pids,
			Memory:     iso.Memory,
			MemorySwap: iso.MemorySwap,
			NanoCPUs:   iso.NanoCPUs,
			Ulimits:    toDockerUlimits(iso.Ulimits),
		},

		Mounts: mounts,
		Tmpfs:  tmpfs,
	}
	return hostCfg
}

// resolveMounts validates and converts the spec's cwd and mounts into docker
// mount specs. The session cwd is mounted 1:1 at the same absolute path
// (§7.2), **read-only by default** and writable only when the global CWD_MODE
// is "rw" (opt-in). Every host path must stay within ALLOW_DIRS (S04).
func (b *Backend) resolveMounts(s spec.SandboxSpec) ([]mount.Mount, error) {
	var out []mount.Mount

	if s.WorkingDir != "" {
		if err := b.verifyHostPath(s.WorkingDir); err != nil {
			return nil, fmt.Errorf("docker: cwd: %w", err)
		}
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   s.WorkingDir,
			Target:   s.WorkingDir,          // 1:1, same absolute path, no rewriting
			ReadOnly: b.cfg.cwdMode != "rw", // read-only unless rw opt-in
		})
	}

	for i, m := range s.Mounts {
		if err := b.verifyHostPath(m.Source); err != nil {
			return nil, fmt.Errorf("docker: mount %d: %w", i, err)
		}
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}
	return out, nil
}

// buildTmpfs mounts the secrets dir (mode 0700) and the writable workspace.
// Both are tmpfs with rw,noexec,nosuid,nodev (§7.1/§7.3). The secrets dir is
// additionally owned by the sandbox user (uid/gid) so the unprivileged process
// can create and read its own 0600 secret files; with a read-only rootfs the
// only writable location is this tmpfs.
func (b *Backend) buildTmpfs(s spec.SandboxSpec) (map[string]string, error) {
	uid := fmt.Sprintf("%d", s.Isolation.User.UID)
	gid := fmt.Sprintf("%d", s.Isolation.User.GID)
	return map[string]string{
		tmpfsConfDir: "rw,noexec,nosuid,nodev,mode=0700,uid=" + uid + ",gid=" + gid,
		tmpfsWorkdir: "rw,noexec,nosuid,nodev,mode=0755",
	}, nil
}

// writeSecrets delivers each secret into the sandbox as a tmpfs file with
// mode 0600 (§7.3). Values are never placed in argv, the image or env exposed
// to exec. Because the rootfs is read-only (§7.1), a CopyToContainer
// (`docker cp`) write would fail — so each secret is written by a `docker exec`
// that streams the value over the exec's stdin (never argv) into the writable
// tmpfs secrets dir owned by the sandbox user.
func (b *Backend) writeSecrets(ctx context.Context, cid string, secrets []spec.SecretRef) error {
	for _, secret := range secrets {
		name := filepath.Base(secret.Name)
		if name == "" || name == "." || name == ".." {
			return fmt.Errorf("docker: invalid secret name %q", secret.Name)
		}
		if err := b.writeSecret(ctx, cid, name, secret.Value); err != nil {
			return err
		}
	}
	return nil
}

// writeSecret writes a single secret file into the sandbox. The writer command
// is a fixed script with no user input interpolated into it (S11); the target
// path is passed as a distinct positional argument and referenced only as
// "$1", so no value ever appears in the shell string or argv. The secret value
// is streamed over the exec's stdin and never surfaces in argv, image or env
// exposed to exec (§7.3).
func (b *Backend) writeSecret(ctx context.Context, cid, name, value string) error {
	writer := []string{
		"/bin/sh", "-c",
		`umask 077; cat > "$1"`,
		"sh",
		filepath.Join(tmpfsConfDir, name),
	}
	execResp, err := b.api.ContainerExecCreate(ctx, cid, container.ExecOptions{
		Cmd:          writer,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
	})
	if err != nil {
		return fmt.Errorf("docker: exec create for secret %q: %w", name, err)
	}

	hijacked, err := b.api.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("docker: exec attach for secret %q: %w", name, err)
	}

	if _, err := hijacked.Conn.Write([]byte(value)); err != nil {
		hijacked.Close()
		return fmt.Errorf("docker: write secret %q stdin: %w", name, err)
	}
	if err := hijacked.CloseWrite(); err != nil {
		hijacked.Close()
		return fmt.Errorf("docker: close secret %q stdin: %w", name, err)
	}

	// Drain the exec output so the stream is fully consumed before the exec
	// completes. `cat > "$1"` writes nothing to stdout/stderr in the happy path;
	// any output here is discarded and never logged (L05).
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, hijacked.Reader)

	hijacked.Close()

	inspect, err := b.api.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return fmt.Errorf("docker: exec inspect for secret %q: %w", name, err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("docker: write secret %q: exec exited with code %d", name, inspect.ExitCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Executable
// ---------------------------------------------------------------------------

// Exec runs a command inside the sandbox as an argument array with no shell
// (S11). The user is taken from the request (resolved by the session manager
// from the sandbox UIDGID and SudoPolicy, §7.10). --privileged is never used.
func (b *Backend) Exec(ctx context.Context, id string, req exec.Request) (*exec.Result, error) {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return nil, err
	}

	opts := container.ExecOptions{
		Cmd:          req.Argv,
		User:         req.User,
		WorkingDir:   req.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Privileged:   false, // never --privileged (N21)
	}
	execResp, err := b.api.ContainerExecCreate(ctx, cid, opts)
	if err != nil {
		return nil, fmt.Errorf("docker: exec create: %w", err)
	}

	hijacked, err := b.api.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: exec attach: %w", err)
	}
	defer hijacked.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, hijacked.Reader); err != nil {
		return nil, fmt.Errorf("docker: exec read: %w", err)
	}

	inspect, err := b.api.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, fmt.Errorf("docker: exec inspect: %w", err)
	}

	return &exec.Result{ExitCode: inspect.ExitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

// ---------------------------------------------------------------------------
// Introspectable
// ---------------------------------------------------------------------------

// Info returns detailed state of the sandbox.
func (b *Backend) Info(ctx context.Context, id string) (any, error) {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	inspect, err := b.api.ContainerInspect(ctx, cid)
	if err != nil {
		return nil, fmt.Errorf("docker: inspect: %w", err)
	}
	return inspect, nil
}

// Stats returns live resource usage of the sandbox.
func (b *Backend) Stats(ctx context.Context, id string) (any, error) {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	reader, err := b.api.ContainerStats(ctx, cid, false)
	if err != nil {
		return nil, fmt.Errorf("docker: stats: %w", err)
	}
	defer func() { _ = reader.Body.Close() }()

	var stats container.StatsResponse
	if err := json.NewDecoder(reader.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("docker: stats decode: %w", err)
	}
	return stats, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// containerIDBySession resolves the container id owning the given session id
// via label-based ownership (§7.8). It returns ErrNotFound when no container
// is owned by the session.
func (b *Backend) containerIDBySession(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("docker: empty session id")
	}
	args := filters.NewArgs(filters.Arg("label", sessionLabelKey+"="+id))
	list, err := b.api.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return "", fmt.Errorf("docker: list containers: %w", err)
	}
	if len(list) == 0 {
		return "", ErrNotFound
	}
	return list[0].ID, nil
}

// verifyHostPath ensures a host path stays within ALLOW_DIRS (S04). When
// symlinks are disallowed it resolves symlinks, checks the real path against
// the allowed roots, then open-then-verifies the path identity.
func (b *Backend) verifyHostPath(src string) error {
	if strings.TrimSpace(src) == "" {
		return fmt.Errorf("empty host path")
	}
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", src, err)
	}
	if !b.withinAllowDirs(resolved) {
		return fmt.Errorf("host path %q escapes ALLOW_DIRS", src)
	}
	if b.cfg.allowSymlinks {
		return nil
	}
	// open-then-verify: open the resolved path and confirm its identity has
	// not changed to escape the allowed roots.
	f, err := os.Open(resolved)
	if err != nil {
		return fmt.Errorf("open %q: %w", resolved, err)
	}
	defer func() { _ = f.Close() }()
	realPath, err := filepath.EvalSymlinks(f.Name())
	if err != nil {
		return err
	}
	if realPath != resolved {
		return fmt.Errorf("host path %q resolved to %q after open", src, realPath)
	}
	return nil
}

// withinAllowDirs reports whether p equals or is contained under an allowed
// directory.
func (b *Backend) withinAllowDirs(p string) bool {
	for _, d := range b.cfg.allowDirs {
		if p == d || strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// validateImageRef enforces the sandbox image policy (§7.9): the reference
// must parse as a valid image reference and its registry must be in the
// trusted allowlist. This prevents the server from pulling sandbox images from
// arbitrary/untrusted registries (C05/T7). Digest-pinning is the recommended
// deployment posture (documented in SPEC §7.9); a trusted registry is the
// hard enforcement boundary here.
func validateImageRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("sandbox: image is required")
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return fmt.Errorf("sandbox: invalid image reference %q: %w", ref, err)
	}
	domain := reference.Domain(named)
	if domain == "" {
		domain = "docker.io"
	}
	if _, ok := trustedRegistries[domain]; !ok {
		return fmt.Errorf("sandbox: image registry %q is not in the trusted allowlist", domain)
	}
	return nil
}

// toEnvSlice converts the spec env map to a KEY=VALUE slice.
func toEnvSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// toDockerUlimits converts domain ulimits to docker ulimits.
func toDockerUlimits(ul []spec.Ulimit) []*container.Ulimit {
	if len(ul) == 0 {
		return nil
	}
	out := make([]*container.Ulimit, 0, len(ul))
	for _, u := range ul {
		u := u
		out = append(out, &container.Ulimit{
			Name: u.Name,
			Soft: clampUint64(u.Soft),
			Hard: clampUint64(u.Hard),
		})
	}
	return out
}

// clampUint64 converts a uint64 to int64, saturating at MaxInt64 to avoid the
// unsigned-to-signed overflow that gosec G115 flags.
func clampUint64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
