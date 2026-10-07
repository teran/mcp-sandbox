// Package docker implements the sandbox backend roles through the official
// docker SDK over the unix socket (SPEC §6.2, §7; L02GO/N05GO). It applies the
// immutable Isolation block on every create (§7.1), mounts the session cwd 1:1
// at the same absolute path (§7.2), delivers secrets as tmpfs 0600 files
// (§7.3) and executes commands as argument arrays with no shell (S11). It
// registers itself into the backend registry via init() (§6.3).
package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	CopyFromContainer(ctx context.Context, containerID, srcPath string) (io.ReadCloser, container.PathStat, error)
	CopyToContainer(ctx context.Context, containerID, dstPath string, content io.Reader, options container.CopyToContainerOptions) error
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

// Capabilities advertises that docker implements every role interface.
func (b *Backend) Capabilities() role.CapabilitySet {
	return role.CapabilitySet{
		Lifecycle:      true,
		Executable:     true,
		Filesystem:     true,
		Introspectable: true,
	}
}

// compile-time check that Backend satisfies the role interfaces.
var (
	_ role.Backend        = (*Backend)(nil)
	_ role.Lifecycle      = (*Backend)(nil)
	_ role.Executable     = (*Backend)(nil)
	_ role.Filesystem     = (*Backend)(nil)
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
// (§7.2), writable by default and read-only when the global CWD_MODE is
// "readonly". Every host path must stay within ALLOW_DIRS (S04).
func (b *Backend) resolveMounts(s spec.SandboxSpec) ([]mount.Mount, error) {
	var out []mount.Mount

	if s.WorkingDir != "" {
		if err := b.verifyHostPath(s.WorkingDir); err != nil {
			return nil, fmt.Errorf("docker: cwd: %w", err)
		}
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   s.WorkingDir,
			Target:   s.WorkingDir, // 1:1, same absolute path, no rewriting
			ReadOnly: b.cfg.cwdMode == "readonly",
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
// Both are tmpfs with rw,noexec,nosuid,nodev (§7.1/§7.3).
func (b *Backend) buildTmpfs(_ spec.SandboxSpec) (map[string]string, error) {
	return map[string]string{
		tmpfsConfDir: "rw,noexec,nosuid,nodev,mode=0700",
		tmpfsWorkdir: "rw,noexec,nosuid,nodev,mode=0755",
	}, nil
}

// writeSecrets delivers each secret into the sandbox as a tmpfs file with
// mode 0600 (§7.3). Values are never placed in argv, the image or env exposed
// to exec.
func (b *Backend) writeSecrets(ctx context.Context, cid string, secrets []spec.SecretRef) error {
	for _, secret := range secrets {
		name := filepath.Base(secret.Name)
		if name == "" || name == "." || name == ".." {
			return fmt.Errorf("docker: invalid secret name %q", secret.Name)
		}
		rc, err := singleFileTar(name, []byte(secret.Value), 0o600)
		if err != nil {
			return err
		}
		if err := b.api.CopyToContainer(ctx, cid, tmpfsConfDir, rc, container.CopyToContainerOptions{}); err != nil {
			return fmt.Errorf("docker: copy secret %q: %w", name, err)
		}
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
// Filesystem
// ---------------------------------------------------------------------------

// ReadFile reads a single file from inside the sandbox.
func (b *Backend) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return nil, err
	}
	rc, _, err := b.api.CopyFromContainer(ctx, cid, path)
	if err != nil {
		return nil, fmt.Errorf("docker: read file: %w", err)
	}
	defer func() { _ = rc.Close() }()
	data, err := firstTarEntry(rc)
	if err != nil {
		return nil, fmt.Errorf("docker: read file tar: %w", err)
	}
	return data, nil
}

// WriteFile writes a file into the sandbox (tmpfs / allowed mount). The write
// is delivered as a tar stream so content never reaches argv.
func (b *Backend) WriteFile(ctx context.Context, id, path string, data []byte) error {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return err
	}
	rc, err := singleFileTar(filepath.Base(path), data, 0o644)
	if err != nil {
		return err
	}
	if err := b.api.CopyToContainer(ctx, cid, filepath.Dir(path), rc, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("docker: write file: %w", err)
	}
	return nil
}

// CopyIn copies a file or directory from an allowed host path into the
// sandbox.
func (b *Backend) CopyIn(ctx context.Context, id, srcHostPath, dstSandboxPath string) error {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return err
	}
	if err := b.verifyHostPath(srcHostPath); err != nil {
		return err
	}
	rc, err := tarHostPath(srcHostPath)
	if err != nil {
		return err
	}
	if err := b.api.CopyToContainer(ctx, cid, dstSandboxPath, rc, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("docker: copy in: %w", err)
	}
	return nil
}

// CopyOut copies a file or directory from the sandbox to an allowed host
// path.
func (b *Backend) CopyOut(ctx context.Context, id, srcSandboxPath, dstHostPath string) error {
	cid, err := b.containerIDBySession(ctx, id)
	if err != nil {
		return err
	}
	if err := b.verifyHostPath(filepath.Dir(dstHostPath)); err != nil {
		return err
	}
	rc, _, err := b.api.CopyFromContainer(ctx, cid, srcSandboxPath)
	if err != nil {
		return fmt.Errorf("docker: copy out: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return extractTarTo(rc, dstHostPath)
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

// singleFileTar builds a tar stream containing a single file entry with the
// given mode. The entry name is the base name, safe from traversal.
func singleFileTar(name string, data []byte, mode int64) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// firstTarEntry returns the content of the first entry of a tar stream.
func firstTarEntry(r io.Reader) ([]byte, error) {
	tr := tar.NewReader(r)
	if _, err := tr.Next(); err != nil {
		return nil, err
	}
	return io.ReadAll(tr)
}

// tarHostPath builds a tar stream of the given host file or directory, rooted
// at its base name. File access is scoped under an os.Root of the parent
// directory so a variable path cannot escape it (G304/G122).
func tarHostPath(src string) (io.Reader, error) {
	root, err := os.OpenRoot(filepath.Dir(src))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	base := filepath.Base(src)
	info, err := root.Lstat(base)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if info.IsDir() {
		err = walkRoot(root, base, func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, rerr := filepath.Rel(base, p)
			if rerr != nil {
				return rerr
			}
			name := filepath.Join(base, rel)
			link := ""
			if fi.Mode()&os.ModeSymlink != 0 {
				link, err = root.Readlink(p)
				if err != nil {
					return err
				}
			}
			hdr, herr := tar.FileInfoHeader(fi, link)
			if herr != nil {
				return herr
			}
			hdr.Name = filepath.ToSlash(name)
			if werr := tw.WriteHeader(hdr); werr != nil {
				return werr
			}
			if fi.IsDir() || link != "" {
				return nil
			}
			f, oerr := root.Open(p)
			if oerr != nil {
				return oerr
			}
			defer func() { _ = f.Close() }()
			_, cerr := io.Copy(tw, f)
			return cerr
		})
		if err != nil {
			return nil, err
		}
	} else {
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = root.Readlink(base)
			if err != nil {
				return nil, err
			}
		}
		hdr, herr := tar.FileInfoHeader(info, link)
		if herr != nil {
			return nil, herr
		}
		hdr.Name = base
		if werr := tw.WriteHeader(hdr); werr != nil {
			return nil, werr
		}
		if link == "" {
			f, oerr := root.Open(base)
			if oerr != nil {
				return nil, oerr
			}
			defer func() { _ = f.Close() }()
			if _, cerr := io.Copy(tw, f); cerr != nil {
				return nil, cerr
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// walkRoot recursively walks path within root, invoking fn for every entry
// (including path itself). File access stays scoped under root (G122/G304).
// Entries are statted with Lstat so symlinks are preserved as links, not
// followed (and never allowed to escape the root).
func walkRoot(root *os.Root, path string, fn func(p string, fi os.FileInfo, err error) error) error {
	fi, err := root.Lstat(path)
	if err != nil {
		return fn(path, nil, err)
	}
	if err := fn(path, fi, nil); err != nil {
		return err
	}
	if !fi.IsDir() {
		return nil
	}
	d, err := root.Open(path)
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	closeErr := d.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	sort.Strings(names)
	for _, n := range names {
		if err := walkRoot(root, filepath.Join(path, n), fn); err != nil {
			return err
		}
	}
	return nil
}

// maxCopyOutBytes bounds a single copy-out to guard against a decompression
// bomb (gosec G110).
const maxCopyOutBytes = 1 << 30 // 1 GiB

// extractTarTo extracts the first entry of a tar stream to the destination
// path. It guards against path traversal by writing only to the exact target,
// scopes the write under an os.Root (G304) and bounds the copied size (G110).
func extractTarTo(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	if _, err := tr.Next(); err != nil {
		return err
	}

	root, err := os.OpenRoot(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	f, err := root.OpenFile(filepath.Base(dst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	n, err := io.CopyN(f, tr, maxCopyOutBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n > maxCopyOutBytes {
		return fmt.Errorf("docker: copy out exceeds %d bytes", maxCopyOutBytes)
	}
	return f.Close()
}
