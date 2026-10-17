// Package darwin implements the sandbox backend roles on Apple macOS through
// the external /usr/bin/sandbox-exec (Seatbelt) binary (SPEC §7.12). It is an
// experimental, policy-based containment driver — strictly weaker than the
// Docker MVP — that runs as a non-root self-sandboxed child process, with no
// cgo, no root and no cgroups.
//
// The package is split into a portable core (this file, compiled on every
// GOOS and exercised in CI against an in-memory runner) and a platform runner
// (runner_darwin.go / runner_other.go). The portable core drives the package-
// local `runner` interface; tests substitute a fakeRunner for os/exec, so all
// backend logic is verifiable on any platform while the real os/exec wrapper
// lives behind the darwin build tag.
package darwin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/teran/mcp-sandbox/domain/progress"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/sbpl"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

const (
	// name is the backend name.
	name = "darwin"
	// priority orders selection (lower preferred; docker=0 is preferred).
	priority = 10
	// sandboxExecPath is the fixed /usr/bin/sandbox-exec binary (SPEC §7.12).
	sandboxExecPath = "/usr/bin/sandbox-exec"
	// createCommand is the long-lived Create child command run under the fixed
	// [<shell>,"-c",cmd] wrapper (SPEC §7.12). It uses an explicit integer second
	// value (2147483647, a ~68-year sleep) rather than the GNU-ism "sleep
	// infinity", because macOS's BSD /bin/sleep accepts only integer seconds and
	// not the GNU "infinity" keyword (SPEC §7.12).
	createSleepCmd = "sleep 2147483647"
	// defaultShell is used when the spec requests no shell.
	defaultShell = "bash"
)

// runner is the boundary between the portable backend logic and the OS: it
// spawns a /usr/bin/sandbox-exec child. Tests substitute fakeRunner; on darwin
// the real implementation wraps os/exec with Setpgid + setrlimit + kill(-pgid).
type runner interface {
	Start(ctx context.Context, ps ProcessSpec) (Process, error)
}

// ProcessSpec describes a child process to start.
type ProcessSpec struct {
	Argv    []string
	Env     []string
	Dir     string
	Rlimits []Rlimit
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// Rlimit is a single setrlimit resource (soft/hard) applied to the child.
type Rlimit struct {
	Resource int
	Soft     uint64
	Hard     uint64
}

// Process is a started child. PGID identifies the process group so Destroy can
// kill the whole sandboxed tree (SPEC §7.12); Wait blocks for completion and
// KillGroup sends a signal to the group.
type Process interface {
	PGID() int
	Wait(ctx context.Context) (*Result, error)
	KillGroup(sig syscall.Signal) error
}

// Result is the outcome of a Wait: the exit code, captured stdout/stderr and
// whether the process was terminated by a signal.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Signaled bool
}

// session is the in-memory record backing a sandbox (X01): the long-lived
// Create child and the parameters re-used by each fresh Exec child.
type session struct {
	proc Process
	// sbpl is the rendered Seatbelt profile; Exec re-uses it verbatim.
	sbpl    string
	dir     string
	env     []string
	rlimits []Rlimit
}

// Backend implements the sandbox role interfaces (role.Backend, Lifecycle,
// Executable) over /usr/bin/sandbox-exec (SPEC §7.12). Introspection is not
// supported (no cgroups on macOS): Info/Stats return ErrCapabilityNotSupported.
type Backend struct {
	runner runner
	cfg    config

	mu       sync.Mutex
	sessions map[string]*session
}

// config holds the resolved backend settings.
type config struct {
	goos       string
	fileExists func(string) bool
	euid       func() int
	allowDirs  []string
	scratchDir string
}

// Option configures a darwin backend at construction.
type Option func(*config)

// WithGOOS overrides the GOOS used by Available (runtime.GOOS by default). It
// exists so the availability decision is testable without a real OS.
func WithGOOS(goos string) Option {
	return func(c *config) { c.goos = goos }
}

// WithFileExists overrides the file-existence predicate used by Available
// (os.Stat by default).
func WithFileExists(f func(string) bool) Option {
	return func(c *config) { c.fileExists = f }
}

// WithEUID overrides the effective-uid predicate used by Available (os.Geteuid
// by default).
func WithEUID(f func() int) Option {
	return func(c *config) { c.euid = f }
}

// WithScratchDir sets the sanctioned per-session scratch directory projected
// read-write into the Seatbelt profile (SPEC §7.12). It is passed through to
// sbpl.Generate. Empty (default) projects no scratch rule.
func WithScratchDir(dir string) Option {
	return func(c *config) { c.scratchDir = dir }
}

// New returns a darwin backend for the given server configuration and runner.
// Allow dirs gate the host paths the Seatbelt profile may grant file access to
// (S04). Optional Options (WithGOOS/WithFileExists/WithEUID/WithScratchDir)
// tune availability and the SBPL projection; the runner is injectable for tests.
func New(r runner, allowDirs []string, opts ...Option) *Backend {
	cfg := config{
		goos:       runtime.GOOS,
		fileExists: fileExistsReal,
		euid:       os.Geteuid,
		allowDirs:  allowDirs,
	}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}

	return &Backend{
		runner:   r,
		cfg:      cfg,
		sessions: make(map[string]*session),
	}
}

// fileExistsReal reports whether a path exists on disk (default predicate).
func fileExistsReal(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// Name returns the backend name.
func (b *Backend) Name() string { return name }

// Priority returns the backend priority.
func (b *Backend) Priority() int { return priority }

// Capabilities advertises that darwin implements Lifecycle and Executable but
// not Introspectable (no cgroups on macOS, SPEC §7.12).
func (b *Backend) Capabilities() role.CapabilitySet {
	return role.CapabilitySet{
		Lifecycle:      true,
		Executable:     true,
		Introspectable: false,
	}
}

// Driver reports the backend driver.
func (b *Backend) Driver() spec.Driver { return spec.DriverSeatbelt }

// Slug returns the namespacing slug of the backend's session ids (§7.8): for
// the darwin/seatbelt backend it is "seatbelt", so a session id reads
// "seatbelt:darwin-N".
func (b *Backend) Slug() string { return "seatbelt" }

// Available reports whether the darwin backend is usable right now: GOOS must
// be darwin, /usr/bin/sandbox-exec must exist, and the process must not run as
// root (self-sandbox refuses uid 0, SPEC §7.12). It is evaluated lazily during
// backend selection, never at startup (A02/N35).
func (b *Backend) Available() bool {
	return b.cfg.goos == "darwin" && b.cfg.fileExists(sandboxExecPath) && b.cfg.euid() != 0
}

// compile-time checks that Backend satisfies the role interfaces it advertises.
var (
	_ role.Backend        = (*Backend)(nil)
	_ role.Lifecycle      = (*Backend)(nil)
	_ role.Executable     = (*Backend)(nil)
	_ role.Introspectable = (*Backend)(nil)
)

// nextSessionID is a monotonic source of session ids. A darwin session is an
// in-memory record keyed by this id; uniqueness within the process is enough
// (X01) — the id is never exposed as a filesystem path or command argument.
var nextSessionID atomic.Int64

func newSessionID() string {
	return fmt.Sprintf("darwin-%d", nextSessionID.Add(1))
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Create renders the Seatbelt profile and spawns a long-lived sandboxed child
// running "sleep infinity" under the fixed [<shell>,"-c",cmd] wrapper (SPEC
// §7.12). The SBPL is rendered fail-closed BEFORE any child is spawned — a
// hostile path aborts Create with no side effect. The session id is returned
// as the sandbox handle. Secrets are never placed in argv or env (S02).
func (b *Backend) Create(ctx context.Context, s spec.SandboxSpec) (string, error) {
	rendered, err := sbpl.Generate(s, b.cfg.allowDirs, b.cfg.scratchDir)
	if err != nil {
		return "", err
	}

	shell := defaultShell
	if strings.TrimSpace(s.Shell) != "" {
		shell = s.Shell
	}
	shellTokens := strings.Fields(shell)
	if len(shellTokens) == 0 {
		shellTokens = []string{defaultShell}
	}

	// argv: [sandbox-exec, -p, <SBPL>, <shell tokens...>, -c, "sleep infinity"].
	argv := []string{sandboxExecPath, "-p", rendered}
	argv = append(argv, shellTokens...)
	argv = append(argv, "-c", createSleepCmd)

	rlimits := toRlimits(s.Resources.Ulimits)
	ps := ProcessSpec{
		Argv:    argv,
		Env:     toEnvSlice(s.Env),
		Dir:     s.WorkingDir,
		Rlimits: rlimits,
	}

	proc, err := b.runner.Start(ctx, ps)
	if err != nil {
		return "", fmt.Errorf("darwin: create: %w", err)
	}

	id := newSessionID()
	b.mu.Lock()
	b.sessions[id] = &session{
		proc:    proc,
		sbpl:    rendered,
		dir:     s.WorkingDir,
		env:     toEnvSlice(s.Env),
		rlimits: rlimits,
	}
	b.mu.Unlock()

	return id, nil
}

// Destroy kills the whole process group of the session's Create child with
// SIGKILL and waits for it. It is idempotent (X02): an unknown session id is a
// no-op, and a second kill on an already-dead child (ESRCH) is treated as a
// no-op. The session record is retained so repeated destroys stay no-ops.
func (b *Backend) Destroy(ctx context.Context, id string) error {
	b.mu.Lock()
	s, ok := b.sessions[stripSlug(id)]
	b.mu.Unlock()
	if !ok {
		return nil
	}

	err := s.proc.KillGroup(syscall.SIGKILL)
	_, _ = s.proc.Wait(ctx)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("darwin: destroy: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Executable
// ---------------------------------------------------------------------------

// Exec runs a command inside the sandbox by spawning a fresh sandbox-exec
// child on every call, wrapped as [sandbox-exec, -p, <session SBPL>, <req
// argv...>] (SPEC §7.12). It collects stdout/stderr and maps the child's exit
// status into the result. req.Timeout (seconds) is applied as a context
// deadline (SPEC §6.10); on cancellation the child's process group is SIGKILLed
// and a terminal "cancelled" progress event is emitted before the error returns.
func (b *Backend) Exec(ctx context.Context, id string, req exec.Request) (*exec.Result, error) {
	execCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	b.mu.Lock()
	s, ok := b.sessions[stripSlug(id)]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("darwin: unknown session %q", id)
	}

	argv := []string{sandboxExecPath, "-p", s.sbpl}
	argv = append(argv, req.Argv...)

	dir := s.dir
	if req.WorkingDir != "" {
		dir = req.WorkingDir
	}
	env := s.env
	if len(req.Env) > 0 {
		env = toEnvSlice(req.Env)
	}

	var stdout, stderr bytes.Buffer
	ps := ProcessSpec{
		Argv:    argv,
		Env:     env,
		Dir:     dir,
		Rlimits: s.rlimits,
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	proc, err := b.runner.Start(execCtx, ps)
	if err != nil {
		return nil, fmt.Errorf("darwin: exec start: %w", err)
	}

	res, err := proc.Wait(execCtx)
	if err != nil {
		// Cancellation (e.g. req.Timeout): SIGKILL the group and surface a
		// terminal "cancelled" progress event before returning (SPEC §5.1/§6.10).
		_ = proc.KillGroup(syscall.SIGKILL)
		progress.SinkFromContext(execCtx)(execCtx, progress.Event{Message: "cancelled"})

		return nil, fmt.Errorf("darwin: exec: %w", err)
	}

	return &exec.Result{
		ExitCode:     res.ExitCode,
		Stdout:       res.Stdout,
		Stderr:       res.Stderr,
		PolicyDenied: IsPolicyDenial(res),
	}, nil
}

// ---------------------------------------------------------------------------
// Introspectable (not supported)
// ---------------------------------------------------------------------------

// Info returns ErrCapabilityNotSupported — macOS has no cgroups and the darwin
// backend does not implement live introspection (SPEC §7.12). Never a stub.
func (b *Backend) Info(_ context.Context, _ string) (any, error) {
	return nil, role.ErrCapabilityNotSupported
}

// Stats returns ErrCapabilityNotSupported — same rationale as Info.
func (b *Backend) Stats(_ context.Context, _ string) (any, error) {
	return nil, role.ErrCapabilityNotSupported
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// toEnvSlice converts an env map to a KEY=VALUE slice, sorted by key for
// determinism. Values are literal — never shell-evaluated.
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

// stripSlug extracts the raw backend id from a session id. Under the
// namespaced-sandbox-id design (§7.8) the session id is `slug:internalID`
// (e.g. "seatbelt:darwin-3"); the darwin backend keys its in-memory sessions
// by the raw id ("darwin-N"), so the "seatbelt:" slug is stripped before the
// lookup. A bare (non-namespaced) id is passed through unchanged.
func stripSlug(id string) string {
	if n, err := domainsession.ParseNamespacedID(id); err == nil {
		return n.Internal
	}

	return id
}

// rlimitNames maps the setrlimit-mappable ulimit names to their RLIMIT_*
// resources (SPEC §7.12). Any other name is honestly not enforced and dropped —
// never silently approximated.
var rlimitNames = map[string]int{
	"nofile": syscall.RLIMIT_NOFILE,
	"cpu":    syscall.RLIMIT_CPU,
	"as":     syscall.RLIMIT_AS,
	"nproc":  rlimitNPROC,
	"fsize":  syscall.RLIMIT_FSIZE,
	"stack":  syscall.RLIMIT_STACK,
	"core":   syscall.RLIMIT_CORE,
}

// toRlimits maps spec.Resources.Ulimits to the Rlimit slice applied to a child
// via setrlimit. Only the seven mappable names are kept, in input order; a nil
// or empty input yields nil. NanoCPUs/Memory/PidsLimit have no cgroup analogue
// on macOS and never reach this slice.
func toRlimits(ul []spec.Ulimit) []Rlimit {
	if len(ul) == 0 {
		return nil
	}
	out := make([]Rlimit, 0, len(ul))
	for _, u := range ul {
		res, ok := rlimitNames[u.Name]
		if !ok {
			continue
		}
		out = append(out, Rlimit{Resource: res, Soft: u.Soft, Hard: u.Hard})
	}

	return out
}

// ---------------------------------------------------------------------------
// Registration (SPEC §6.3)
// ---------------------------------------------------------------------------

// platformAvailable is the real, non-injectable availability predicate used by
// the factory. It is evaluated lazily during backend selection, never at
// startup (A02/N35).
func platformAvailable() bool {
	return runtime.GOOS == "darwin" && fileExistsReal(sandboxExecPath) && os.Geteuid() != 0
}

// factory returns the backend.Factory for the darwin backend. Available uses
// the real platform predicate; New constructs a backend over the platform
// runner (newRunner, build-tag-selected) with the server's allow dirs.
func factory() backend.Factory {
	return backend.Factory{
		Name:         name,
		Priority:     priority,
		Driver:       spec.DriverSeatbelt,
		Capabilities: role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: false},
		Available:    platformAvailable,
		New: func(cfg backend.Config) (role.Backend, error) {
			return New(newRunner(), cfg.AllowDirs), nil
		},
	}
}

// init registers the darwin backend into the shared registry (SPEC §6.3). The
// backend is blank-imported in cmd/mcp-sandbox so this runs on startup.
func init() {
	_ = backend.Register(factory())
}
