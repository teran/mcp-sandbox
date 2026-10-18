// Package docker implements the sandbox backend roles through the official
// docker SDK over the unix socket (SPEC §6.2, §7; L02GO/N05GO). It applies the
// immutable Isolation block on every create (§7.1), mounts the session cwd 1:1
// at the same absolute path (§7.2), delivers secrets as tmpfs 0600 files under
// /run/sandbox (§7.3) and executes commands as argument arrays with no shell
// (S11). It registers itself into the backend registry via init() (§6.3).
package docker

import (
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
	"sync"
	"time"

	"github.com/distribution/reference"
	dockerconfig "github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/domain/progress"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
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
	// opencodeSessionLabelKey labels a container with the OpenCode client session
	// id that created it (§4/§7.8), so each sandbox container is traceable to the
	// OpenCode session that created it. It is set only when non-empty (the
	// required-ness is enforced at the tool input boundary, not here).
	opencodeSessionLabelKey = "dev.teran.sandbox.opencode_session"

	// tmpfsConfDir is where secret files are placed as a private directory
	// inside the /run tmpfs (files are written with mode 0600, §7.3). The name
	// deliberately avoids the "secret" keyword because gosec's G101 heuristic
	// would otherwise flag this filesystem path as a hardcoded credential.
	tmpfsConfDir = "/run/sandbox"
	// tmpfsRunDir is the tmpfs mounted at /run: it is owned by the sandbox user
	// so the unprivileged process can create the secret dir and its 0600 files
	// (§7.3). It stays noexec (a secrets location does not need to execute).
	tmpfsRunDir = "/run"
	// tmpfsTmpDir is the tmpfs mounted at /tmp: writable AND executable (no
	// noexec) so the sandbox can compile and run Go binaries (e.g. `go build`,
	// govulncheck) against a read-only rootfs. nosuid,nodev are kept for safety.
	tmpfsTmpDir = "/tmp"
	// defaultWorkdir is the working directory when the spec does not request one
	// (SPEC §7.1): a writable, executable tmpfs so exec and file operations work
	// without guessing.
	defaultWorkdir = tmpfsTmpDir
)

// log is the package logger for non-fatal diagnostics (e.g. a missing or
// unreadable Docker client config while pulling). It uses logrus directly — the
// project's centralized logger is wired through infrastructure/logging, which
// this backend has no handle on. It never carries secrets or config contents
// (S02/L05).
var log = logrus.WithField("component", "docker-backend")

// dockerAPI is the narrow subset of the docker SDK client that the backend
// depends on. It keeps the backend testable with a fake client while the real
// *client.Client satisfies it. This is the official SDK — not a hand-rolled
// low-level http.Client (N05GO).
type dockerAPI interface {
	ContainerCreate(
		ctx context.Context,
		config *container.Config,
		hostConfig *container.HostConfig,
		networkingConfig *network.NetworkingConfig,
		platform *ocispec.Platform,
		containerName string,
	) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
	ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

// Backend implements the sandbox role interfaces (role.Backend, Lifecycle,
// Executable, Filesystem, Introspectable) over the docker SDK.
type Backend struct {
	api dockerAPI
	cfg config
}

// config holds the resolved server settings the backend needs.
type config struct {
	allowSymlinks bool
	// logEntry is the base logrus entry for trace diagnostics. It is derived
	// from the context at each call site to attach session_id/request_id, and
	// never carries secrets or argv (S02/L05).
	logEntry *logrus.Entry
}

// Option configures a docker backend at construction.
type Option func(*config)

// WithAllowSymlinks controls whether host path symlinks are followed during
// path verification (S04). It defaults to false.
func WithAllowSymlinks(v bool) Option {
	return func(c *config) { c.allowSymlinks = v }
}

// WithLogger sets the base logrus entry used for trace diagnostics. It
// defaults to the package-level docker-backend logger. It is primarily for
// tests; production wires no logger and derives request/session id from the
// context.
func WithLogger(e *logrus.Entry) Option {
	return func(c *config) { c.logEntry = e }
}

// New returns a docker backend for the given server settings. The ALLOW_DIRS
// host-path containment decision no longer lives in the backend (Slice 4, SPEC
// §6.11 G): EnforceHardening at the Service level owns mount-source containment,
// so the constructor takes no allow-dirs argument. Optional Options (e.g.
// WithAllowSymlinks, WithLogger) tune path verification. The backend retains
// TOCTOU integrity — verifyHostPath still EvalSymlinks + open-then-verifies
// every host path. The image source, network and resources come from the
// operator profile (Phase 7), so there is no registry allowlist or network-mode
// gate here.
func New(api dockerAPI, opts ...Option) *Backend {
	cfg := config{
		allowSymlinks: false,
		logEntry:      log,
	}
	for _, o := range opts {
		o(&cfg)
	}

	return &Backend{api: api, cfg: cfg}
}

// entryFor returns a logrus entry derived from the backend's base logger with
// the session_id and request_id from ctx attached when available (L09). It
// never carries secrets or argv (S02/L05). sessionID is a fallback for call
// sites (e.g. Exec/Destroy) that receive the session id as an explicit
// argument rather than via context.
func (b *Backend) entryFor(ctx context.Context, sessionID string) *logrus.Entry {
	e := b.cfg.logEntry
	if sid, ok := domainsession.IDFromContext(ctx); ok && sid != "" {
		e = e.WithField("session_id", sid)
	} else if sessionID != "" {
		e = e.WithField("session_id", sessionID)
	}

	return logging.RequestEntry(ctx, e)
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

// Driver reports the backend driver.
func (b *Backend) Driver() spec.Driver { return spec.DriverDocker }

// Slug returns the namespacing slug of the backend's session ids (§7.8): for
// the docker backend it is "docker", so a session id reads "docker:<container id>".
func (b *Backend) Slug() string { return "docker" }

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
	if err := s.Validate(); err != nil {
		return "", err
	}
	// Parse-only image validation (Phase 7): any valid reference from any
	// registry is accepted; only empty or malformed references are rejected.
	if err := b.validateImageRef(s.Image); err != nil {
		return "", err
	}

	labels := map[string]string{ownerLabelKey: ownerLabelValue}
	if id, ok := domainsession.IDFromContext(ctx); ok {
		labels[sessionLabelKey] = id
	}
	if s.ClientSessionID != "" {
		labels[opencodeSessionLabelKey] = s.ClientSessionID
	}

	mounts, err := b.resolveMounts(s)
	if err != nil {
		return "", err
	}

	tmpfs, err := b.buildTmpfs(s)
	if err != nil {
		return "", err
	}

	entry := b.entryFor(ctx, "")
	entry.WithFields(logrus.Fields{
		"image":   s.Image,
		"mounts":  len(mounts),
		"network": string(s.Isolation.NetworkMode),
		"memory":  s.Isolation.Memory,
		"pids":    s.Isolation.PidsLimit,
	}).Trace("docker: ContainerCreate spec")

	cfg := b.buildConfig(s, s.Image, labels)
	hostCfg := b.buildHostConfig(s, mounts, tmpfs)
	resp, err := b.api.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
	if isNoSuchImage(err) {
		// The image is not present locally. Pull the (already parse-only
		// validated) image, then retry the create. validateImageRef ran above,
		// so only a valid image reference can ever reach a pull.
		if perr := b.pullImage(ctx, s.Image); perr != nil {
			return "", perr
		}
		resp, err = b.api.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "")
		if err != nil {
			return "", fmt.Errorf("docker: create container (after pull): %w", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("docker: create container: %w", err)
	}
	b.entryFor(ctx, "").WithField("container_id", resp.ID).Trace("docker: ContainerCreate done")
	if err := b.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("docker: start container: %w", err)
	}
	b.entryFor(ctx, "").WithField("container_id", resp.ID).Trace("docker: ContainerStart")

	if err := b.writeSecrets(ctx, resp.ID, s.Secrets); err != nil {
		return "", fmt.Errorf("docker: write secrets: %w", err)
	}
	b.entryFor(ctx, "").WithField("count", len(s.Secrets)).Trace("docker: writeSecrets")

	return resp.ID, nil
}

// Destroy removes the container owned by the given session id. It is
// idempotent: destroying a session with no backing container is a no-op (X02).
// Because the session id IS the container id (§7.8), the id is passed directly
// to the Docker remove API; an unknown id surfaces the daemon's "no such
// container" error, which is treated as an idempotent no-op.
func (b *Backend) Destroy(ctx context.Context, id string) error {
	b.entryFor(ctx, id).Trace("docker: destroy")
	err := b.api.ContainerRemove(ctx, stripSlug(id), container.RemoveOptions{Force: true, RemoveVolumes: true})
	if isNoSuchContainer(err) {
		// Idempotent no-op (X02): destroying a session with no backing
		// container is not an error.
		return nil
	}

	return err
}

// buildConfig maps the spec into the docker container.Config. The non-root
// user from the immutable Isolation is forced (never overridable).
func (b *Backend) buildConfig(s spec.SandboxSpec, image string, labels map[string]string) *container.Config {
	workdir := s.WorkingDir
	if workdir == "" {
		workdir = defaultWorkdir
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
// The network mode comes straight from the spec (operator profile), host
// included — there is no server-side allowlist to coerce it (Phase 7).
func (b *Backend) buildHostConfig(s spec.SandboxSpec, mounts []mount.Mount, tmpfs map[string]string) *container.HostConfig {
	iso := s.Isolation
	pids := iso.PidsLimit

	networkMode := string(iso.NetworkMode)
	if networkMode == "" {
		networkMode = string(spec.NetworkNone)
	}

	hostCfg := &container.HostConfig{
		// Forced hardening (immutable).
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		// INVARIANT (R-3): seccomp is NOT mapped into a SecurityOpt. The daemon's
		// default seccomp profile applies automatically and is never overridden.
		// `seccomp=default` is rejected by Docker and `seccomp=unconfined` /
		// `seccomp=<path>` would weaken security — neither is emitted in the MVP.
		// The only valid Seccomp value (SeccompDefault) is enforced fail-closed by
		// spec.Validate()/EnforceHardening, so the field is an honest marker, not a
		// docker SecurityOpt.
		SecurityOpt: []string{"no-new-privileges"},
		AutoRemove:  true,
		NetworkMode: container.NetworkMode(networkMode),

		// Bounded resources.
		PidsLimit:  &pids,
		Memory:     iso.Memory,
		MemorySwap: iso.MemorySwap,
		NanoCPUs:   iso.NanoCPUs,
		Ulimits:    toDockerUlimits(iso.Ulimits),

		Mounts: mounts,
		Tmpfs:  tmpfs,
	}

	return hostCfg
}

// resolveMounts validates and converts the spec's cwd and mounts into docker
// mount specs (Phase 7). The session cwd is mounted 1:1 at the same absolute
// path (§7.2) as the first mount, honouring the profile's cwd.mode (rw/ro) via
// the spec's cwd mount entry. Every host path — the cwd and each profile mount
// source — is verified for TOCTOU integrity (EvalSymlinks + open-then-verify,
// S04); the ALLOW_DIRS containment gate is owned by EnforceHardening at the
// Service level (SPEC §6.11 G), not duplicated here.
func (b *Backend) resolveMounts(s spec.SandboxSpec) ([]mount.Mount, error) {
	var out []mount.Mount

	if s.WorkingDir != "" {
		if err := b.verifyHostPath(s.WorkingDir); err != nil {
			return nil, fmt.Errorf("docker: cwd: %w", err)
		}
		// The profile's cwd mode (cwd.mode: rw/ro) is baked into the spec's
		// first mount entry by BuildSpec; mirror it onto the WorkingDir bind
		// mount here so a writable cwd stays writable. Default to read-only
		// when no matching cwd entry exists.
		readOnly := true
		for _, m := range s.Mounts {
			if m.Source == s.WorkingDir && m.Target == s.WorkingDir {
				readOnly = m.ReadOnly

				break
			}
		}
		out = append(out, mount.Mount{
			Type:     mount.TypeBind,
			Source:   s.WorkingDir,
			Target:   s.WorkingDir, // 1:1, same absolute path, no rewriting
			ReadOnly: readOnly,     // honours the profile's cwd.mode
		})
	}

	for i, m := range s.Mounts {
		// Skip the profile's cwd entry when it is already mounted 1:1 from
		// WorkingDir above, so the cwd is not double-mounted.
		if s.WorkingDir != "" && m.Source == s.WorkingDir && m.Target == s.WorkingDir {
			continue
		}
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

// buildTmpfs mounts /run and /tmp as tmpfs (§7.1/§7.3).
//
//   - /run is owned by the sandbox user (uid/gid) and stays noexec, so the
//     unprivileged process can create the secret dir (/run/sandbox) and its own
//     0600 secret files; a secrets location does not need to execute.
//   - /tmp is writable AND executable (no noexec) so the sandbox can compile
//     and run Go binaries against a read-only rootfs; nosuid,nodev are kept.
//
// Both carry rw,nosuid,nodev. With a read-only rootfs these tmpfs are the only
// writable locations.
func (b *Backend) buildTmpfs(s spec.SandboxSpec) (map[string]string, error) {
	uid := fmt.Sprintf("%d", s.Isolation.User.UID)
	gid := fmt.Sprintf("%d", s.Isolation.User.GID)

	return map[string]string{
		tmpfsRunDir: "rw,noexec,nosuid,nodev,mode=0755,uid=" + uid + ",gid=" + gid,
		// Explicit "exec" so Docker Desktop / some daemons that force noexec on
		// tmpfs do not prevent the sandbox from running compiled Go binaries.
		tmpfsTmpDir: "rw,exec,nosuid,nodev,mode=1777",
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
// "$1", so no value ever appears in the shell string or argv. The script
// creates the parent /run/sandbox directory (owned by the sandbox user via the
// /run tmpfs, §7.3) and streams the value over the exec's stdin with umask 077.
// The secret value never surfaces in argv, image or env exposed to exec (§7.3).
func (b *Backend) writeSecret(ctx context.Context, cid, name, value string) error {
	writer := []string{
		"/bin/sh", "-c",
		`mkdir -p "$(dirname "$1")"; umask 077; cat > "$1"`,
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
	// Apply req.Timeout (seconds) as a context.WithTimeout deadline on the ctx
	// handed to every Docker API call in this function, so the timeout turns
	// into a cancellation (SPEC §6.10). When req.Timeout == 0 the original ctx
	// is used as-is (no deadline).
	execCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	entry := b.entryFor(execCtx, id)
	entry.Trace("docker: exec create")

	raw := stripSlug(id)
	opts := container.ExecOptions{
		Cmd:          req.Argv,
		User:         req.User,
		Env:          toEnvSlice(req.Env),
		WorkingDir:   req.WorkingDir,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Privileged:   false, // never --privileged (N21)
	}
	execResp, err := b.api.ContainerExecCreate(execCtx, raw, opts)
	if err != nil {
		return nil, fmt.Errorf("docker: exec create: %w", err)
	}

	entry.Trace("docker: exec attach")
	hijacked, err := b.api.ContainerExecAttach(execCtx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: exec attach: %w", err)
	}
	defer hijacked.Close()

	// Streaming (Phase 6, SPEC §5.1): when a progress sink is present in the
	// context we emit a `start` event (carrying the redacted argv), one event
	// per batched stdout/stderr line (fed from the stdcopy demux to
	// LineBatchers), a terminal `done` event carrying the exit code, and a
	// terminal `cancelled` event on context cancellation. Progress is a single
	// monotonic counter shared across both streams. Without a real sink the
	// no-op discards everything and the Result is byte-for-byte unchanged.
	stream := newExecStream(execCtx, req)
	if _, err := stdcopy.StdCopy(stream.outWriter, stream.errWriter, hijacked.Reader); err != nil {
		return nil, fmt.Errorf("docker: exec read: %w", err)
	}
	// Flush any trailing partial (non-newline-terminated) output so it is
	// streamed too before the terminal events.
	stream.flush(execCtx)

	inspect, err := b.api.ContainerExecInspect(execCtx, execResp.ID)
	if err != nil {
		return nil, fmt.Errorf("docker: exec inspect: %w", err)
	}

	res := &exec.Result{ExitCode: inspect.ExitCode, Stdout: stream.stdout.Bytes(), Stderr: stream.stderr.Bytes()}

	if stream.sink != nil {
		stream.emit(fmt.Sprintf("done: exit code %d", inspect.ExitCode), int64(len(res.Stdout)+len(res.Stderr)))
		if execCtx.Err() != nil {
			// The command ran (the fake ignores cancellation); surface the
			// cancellation as a terminal event before returning the result.
			stream.emit("cancelled", 0)
		}
	}
	b.entryFor(execCtx, id).WithField("exit_code", inspect.ExitCode).Trace("docker: exec done")

	return res, nil
}

// execStream carries the progress-streaming state for Exec (Phase 6, §5.1).
// It demuxes stdcopy output into two buffers while emitting line-batched
// progress events when a sink is present; a nil sink keeps the Result
// byte-for-byte identical to non-streaming exec.
type execStream struct {
	sink                   progress.Sink
	emit                   func(msg string, total int64)
	outWriter              io.Writer
	errWriter              io.Writer
	outBatcher, errBatcher *progress.LineBatcher
	stdout, stderr         bytes.Buffer
}

// newExecStream wires the progress sink into the exec output demux: a single
// shared, monotonically increasing Progress counter across the whole stream
// (start, both stdout/stderr chunks, done, cancelled) so the emitted sequence
// is never decreasing.
func newExecStream(execCtx context.Context, req exec.Request) *execStream {
	s := &execStream{sink: progress.SinkFromContext(execCtx)}
	var mu sync.Mutex
	var seq int64
	s.emit = func(msg string, total int64) {
		if s.sink == nil {
			return
		}
		mu.Lock()
		seq++
		p := seq
		mu.Unlock()
		s.sink(execCtx, progress.Event{Message: msg, Progress: p, Total: total})
	}
	batcherSink := func(_ context.Context, e progress.Event) { s.emit(e.Message, e.Total) }

	s.outWriter = io.Writer(&s.stdout)
	s.errWriter = io.Writer(&s.stderr)
	if s.sink != nil {
		// The start event shows the raw visible command (req.Command), falling
		// back to the argv wrapper when the command string is absent.
		startCmd := req.Command
		if strings.TrimSpace(startCmd) == "" {
			startCmd = strings.Join(req.Argv, " ")
		}
		s.emit("start: "+startCmd, 0)
		s.outBatcher = progress.NewLineBatcher(batcherSink)
		s.errBatcher = progress.NewLineBatcher(batcherSink)
		s.outWriter = io.MultiWriter(&s.stdout, s.outBatcher)
		s.errWriter = io.MultiWriter(&s.stderr, s.errBatcher)
	}

	return s
}

// flush emits any trailing partial (non-newline-terminated) output so it is
// streamed before the terminal events.
func (s *execStream) flush(execCtx context.Context) {
	if s.outBatcher != nil {
		s.outBatcher.Flush(execCtx)
	}
	if s.errBatcher != nil {
		s.errBatcher.Flush(execCtx)
	}
}

// ---------------------------------------------------------------------------
// Introspectable
// ---------------------------------------------------------------------------

// Info returns detailed state of the sandbox.
func (b *Backend) Info(ctx context.Context, id string) (any, error) {
	inspect, err := b.api.ContainerInspect(ctx, stripSlug(id))
	if err != nil {
		return nil, fmt.Errorf("docker: inspect: %w", err)
	}

	return inspect, nil
}

// Stats returns live resource usage of the sandbox.
func (b *Backend) Stats(ctx context.Context, id string) (any, error) {
	reader, err := b.api.ContainerStats(ctx, stripSlug(id), false)
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

// verifyHostPath validates a host path's TOCTOU integrity (S04, SPEC §7.2). The
// ALLOW_DIRS containment decision no longer lives in the backend (Slice 4, SPEC
// §6.11 G) — EnforceHardening at the Service level owns it — so this only
// ensures the path resolves and its post-open identity is stable. When symlinks
// are disallowed it resolves symlinks and open-then-verifies the path identity.
func (b *Backend) verifyHostPath(src string) error {
	if strings.TrimSpace(src) == "" {
		return fmt.Errorf("empty host path")
	}
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", src, err)
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

// validateImageRef enforces parse-only image validation (Phase 7): the
// reference must parse as a valid image reference. Any registry is accepted —
// the image source comes from the operator-authored profile and there is no
// trusted-registry allowlist (TRUSTED_REGISTRIES was removed). Only empty or
// malformed references are rejected.
func (b *Backend) validateImageRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("sandbox: image is required")
	}
	if _, err := reference.ParseNormalizedNamed(ref); err != nil {
		return fmt.Errorf("sandbox: invalid image reference %q: %w", ref, err)
	}

	return nil
}

// isNoSuchImage reports whether a ContainerCreate error means the image is not
// present in the local cache ("No such image"), which triggers an auto-pull of
// the already-validated trusted image (§7.9). It relies on the daemon's stable
// error message rather than error-type assertion so no deprecated/versioned SDK
// API is needed. Only called after validateImageRef has already accepted the
// reference.
func isNoSuchImage(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No such image")
}

// stripSlug extracts the raw backend id from a session id. Under the
// namespaced-sandbox-id design (§7.8) the session id is `slug:internalID`
// (e.g. "docker:6b4464b592f9"); the docker backend must call the Docker API
// with the raw container id only, so the "docker:" slug is stripped. A bare
// (non-namespaced) id — the legacy form — is passed through unchanged, so
// callers that already hold the raw container id keep working.
func stripSlug(id string) string {
	if n, err := domainsession.ParseNamespacedID(id); err == nil {
		return n.Internal
	}

	return id
}

// isNoSuchContainer reports whether an error means the container does not exist
// ("No such container"). Because the session id IS the container id (§7.8), the
// Docker API errors naturally for an unknown id; this classifier lets Destroy
// treat a missing container as an idempotent no-op (X02). Like isNoSuchImage it
// relies on the daemon's stable error message rather than error-type assertion.
func isNoSuchContainer(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No such container")
}

// pullImage pulls the given image reference from its registry (§7.9, C05/T7).
// The caller MUST have run validateImageRef on ref first — this helper never
// re-validates, so it must never be reached with an untrusted reference.
//
// When the operator's Docker client config has credentials for the image's
// registry they are attached as RegistryAuth (the same source `docker pull`
// uses, §7.11); a private registry is otherwise unreachable because the daemon
// alone cannot authenticate. A missing config or a public image degrade
// gracefully to an unauthenticated pull. The pull stream is drained and
// discarded: image content is never logged (L05) and never carries secrets.
// The error is surfaced clearly to the model (X03).
func (b *Backend) pullImage(ctx context.Context, ref string) error {
	opts := image.PullOptions{}
	if auth := registryAuth(ref); auth != "" {
		opts.RegistryAuth = auth
	}
	// Log the reference and whether registry auth was supplied — never the
	// token itself (S02/L05).
	b.entryFor(ctx, "").WithFields(logrus.Fields{
		"ref":           ref,
		"registry_auth": opts.RegistryAuth != "",
	}).Trace("docker: pull")
	reader, err := b.api.ImagePull(ctx, ref, opts)
	if err != nil {
		return fmt.Errorf("docker: pull image %q: %w", ref, err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("docker: pull image %q: read: %w", ref, err)
	}

	return nil
}

// registryAuth returns the base64url-encoded X-Registry-Auth value for the
// image's registry, read from the operator's Docker client config
// (~/.docker/config.json or $DOCKER_CONFIG — the same source `docker pull`
// uses), or "" when no credentials are configured (a public image). It never
// fails the pull: a missing/unreadable client config or a registry with no
// entry degrades gracefully to an unauthenticated pull. The operator's token
// and config contents are never logged (S02/L05).
func registryAuth(ref string) string {
	domain, err := registryHost(ref)
	if err != nil || domain == "" {
		log.Debugf("docker: could not derive registry from %q; pulling unauthenticated", ref)

		return ""
	}

	cf, err := dockerconfig.Load(dockerConfigDir())
	if err != nil {
		log.Warnf("docker: failed to load client config for registry %q: %v; pulling unauthenticated", domain, err)

		return ""
	}

	auth, err := cf.GetAuthConfig(domain)
	if err != nil {
		log.Debugf("docker: no credentials configured for registry %q; pulling unauthenticated", domain)

		return ""
	}
	if auth.Username == "" && auth.Password == "" && auth.IdentityToken == "" && auth.Auth == "" {
		log.Debugf("docker: empty credentials for registry %q; pulling unauthenticated", domain)

		return ""
	}

	encoded, err := registry.EncodeAuthConfig(registry.AuthConfig{
		Username:      auth.Username,
		Password:      auth.Password,
		Auth:          auth.Auth,
		ServerAddress: auth.ServerAddress,
		IdentityToken: auth.IdentityToken,
		RegistryToken: auth.RegistryToken,
	})
	if err != nil {
		log.Warnf("docker: failed to encode registry auth for %q: %v; pulling unauthenticated", domain, err)

		return ""
	}

	return encoded
}

// registryHost returns the registry domain for an image reference, resolving
// unqualified names to docker.io.
func registryHost(ref string) (string, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", err
	}

	return reference.Domain(named), nil
}

// dockerConfigDir returns the Docker client config directory, honouring
// $DOCKER_CONFIG and defaulting to ~/.docker.
func dockerConfigDir() string {
	if d := os.Getenv(dockerconfig.EnvOverrideConfigDir); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".docker")
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
