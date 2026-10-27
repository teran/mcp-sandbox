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
	"math/big"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

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
	// defaultKillGrace is the default SIGTERM→SIGKILL escalation grace window
	// granted to a process group before the backend hard-kills it (TODO R-6).
	// It bounds the DoS window a hostile process can hold the backend open.
	defaultKillGrace = 1 * time.Second
	// maxKillGrace is the hard upper bound on the configurable grace window
	// (5s). A configured grace above this is clamped down at config time — the
	// "never weaken" analogue of the immutable isolation block: the DoS window
	// can never be widened beyond the hard bound.
	maxKillGrace = 5 * time.Second
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
	// KillGrace is the SIGTERM→SIGKILL escalation grace window the runner's
	// Wait applies on cancellation before hard-killing the process group (R-6).
	KillGrace time.Duration
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

	// reapMu serializes the group-kill + single Wait/reap of the session's
	// Create child across concurrent Destroys (X05). It guards the reap so that
	// os/exec's cmd.Wait is called exactly once even when two Destroys race —
	// the group-kill itself stays idempotent (ESRCH-tolerant), but the reap must
	// not double-fire. Security condition #2 of TODO R-6.
	reapMu sync.Mutex
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
	// killGrace is the SIGTERM→SIGKILL escalation grace window applied to every
	// process group the backend terminates (Destroy + Exec cancel, R-6).
	killGrace time.Duration
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

// WithKillGrace overrides the SIGTERM→SIGKILL escalation grace window applied
// when terminating a process group (Destroy and Exec cancel, R-6). The value is
// clamped fail-closed at config time: a non-positive value (which would mean an
// immediate, ungraceful hard-kill — weakening the graceful shutdown) is reset
// to defaultKillGrace, and a value above maxKillGrace is clamped to it so the
// DoS window a hostile process can hold the backend open is hard-bounded.
func WithKillGrace(d time.Duration) Option {
	return func(c *config) {
		switch {
		case d <= 0:
			c.killGrace = defaultKillGrace
		case d > maxKillGrace:
			c.killGrace = maxKillGrace
		default:
			c.killGrace = d
		}
	}
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
		killGrace:  defaultKillGrace,
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
// "seatbelt:<25 base36 chars>".
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

// seatbeltNamespace is the fixed UUIDv5 (SHA-1 name-based) namespace for
// seatbelt session ids (§7.8). It MUST remain stable across releases: it is
// part of the deterministic session-id derivation, and changing it would change
// every existing seatbelt session id, breaking prefix resolution of in-flight
// ids.
var seatbeltNamespace = uuid.MustParse("6f6b2f9a-2c0e-4f0a-9b0a-1a2b3c4d5e6f")

// base36IDLength is the fixed length of a seatbelt internal session id: a
// 128-bit value rendered in base36 (0-9a-z) is always 25 chars (§7.8).
const base36IDLength = 25

// base36Radix is the radix for base36 encoding (alphabet 0-9a-z).
const base36Radix = 36

// identityParts enumerates the identity components of a seatbelt sandbox in
// their canonical join order — [profile, session_id, cwd] (§7.8).
func identityParts(s spec.SandboxSpec) []string {
	return []string{s.Profile, s.ClientSessionID, s.WorkingDir}
}

// sessionIdentity joins the non-empty parts of a seatbelt sandbox's identity —
// [profile, session_id, cwd] — with "/". It is deterministic: the same
// profile/session/cwd always yields the same identity string.
func sessionIdentity(s spec.SandboxSpec) string {
	identity := identityParts(s)
	parts := make([]string, 0, len(identity))
	for _, p := range identity {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}

	return strings.Join(parts, "/")
}

// sessionIDFromSpec derives a seatbelt session's internal id deterministically
// from the sandbox's identity: a UUIDv5 (SHA-1 name-based) of
// sessionIdentity(s) under the fixed seatbeltNamespace, encoded as base36
// (0-9a-z, no special characters, left-padded to exactly 25 chars) (§7.8).
// Determinism is intentional: the same identity → the same id, so a repeat
// sandbox_create with the same identity surfaces as a duplicate via
// search-before-create (X02); different profile/session/cwd → different,
// distinguishable ids. base36 (rather than base64url) keeps the id free of
// URL/shell special characters.
func sessionIDFromSpec(s spec.SandboxSpec) string {
	id := uuid.NewSHA1(seatbeltNamespace, []byte(sessionIdentity(s)))

	return encodeBase36(id)
}

// encodeBase36 renders a 128-bit (16-byte) value as a lowercase base36 string
// (alphabet 0-9a-z, no special characters), left-padded with '0' to a fixed 25
// chars. 128 bits always need 25 base36 digits; the padding keeps the id length
// stable for every identity.
func encodeBase36(id uuid.UUID) string {
	s := new(big.Int).SetBytes(id[:]).Text(base36Radix)
	if len(s) < base36IDLength {
		s = strings.Repeat("0", base36IDLength-len(s)) + s
	}

	return s
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
		Argv:      argv,
		Env:       toEnvSlice(s.Env),
		Dir:       s.WorkingDir,
		Rlimits:   rlimits,
		KillGrace: b.cfg.killGrace,
	}

	proc, err := b.runner.Start(ctx, ps)
	if err != nil {
		return "", fmt.Errorf("darwin: create: %w", err)
	}

	id := sessionIDFromSpec(s)
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

// Destroy terminates the whole process group of the session's Create child,
// gracefully: it first sends SIGTERM so the sandboxed tree can flush/clean up,
// then waits up to the configured grace window, escalating to SIGKILL only if
// the group ignores SIGTERM (TODO R-6). It is idempotent (X02): an unknown
// session id is a no-op, and a kill on an already-dead group (ESRCH) is treated
// as a no-op. The session record is retained so repeated destroys stay no-ops.
// The per-session reapMu serializes the kill+reap so two racing Destroys never
// double-call cmd.Wait (X05 / security condition #2 of R-6).
func (b *Backend) Destroy(ctx context.Context, id string) error {
	b.mu.Lock()
	s, ok := b.sessions[stripSlug(id)]
	b.mu.Unlock()
	if !ok {
		return nil
	}

	s.reapMu.Lock()
	defer s.reapMu.Unlock()

	if err := killGroupWithGrace(ctx, s.proc, b.cfg.killGrace); err != nil {
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
		execCtx, cancel = context.WithTimeout(ctx, exec.TimeoutDuration(req.Timeout))
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
		Argv:      argv,
		Env:       env,
		Dir:       dir,
		Rlimits:   s.rlimits,
		Stdout:    &stdout,
		Stderr:    &stderr,
		KillGrace: b.cfg.killGrace,
	}

	proc, err := b.runner.Start(execCtx, ps)
	if err != nil {
		return nil, fmt.Errorf("darwin: exec start: %w", err)
	}

	res, err := proc.Wait(execCtx)
	if err != nil {
		// Cancellation (e.g. req.Timeout): terminate the child's process group
		// gracefully (SIGTERM, escalate to SIGKILL after the grace window) so it
		// can flush/clean up, then emit a terminal "cancelled" progress event
		// AFTER the group has been killed/reaped (SPEC §5.1/§6.10, R-6). The
		// event ordering keeps the S02/S09 redaction contract intact.
		_ = killGroupWithGrace(ctx, proc, b.cfg.killGrace)
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

// killGroupWithGrace terminates a process group gracefully (TODO R-6): it sends
// SIGTERM first so the sandboxed tree can flush/clean up, waits up to `grace`
// for a graceful exit, and escalates to SIGKILL only if the group ignores
// SIGTERM. It always reaps (Waits) the group exactly once; a group that is
// already dead (ESRCH on the SIGTERM) is a no-op, preserving Destroy
// idempotency (X02). The grace window is derived from ctx via
// context.WithoutCancel, so a cancelled caller context (e.g. the Exec cancel
// path, where the ctx is already done) neither truncates the grace window —
// forcing an unnecessary escalation — nor skips the reap. It is driven entirely
// through the `Process` interface, so it is fully testable with fakeRunner; the
// real osProcess.Wait is idempotent, so this cannot double-reap (security
// condition #2 of R-6).
func killGroupWithGrace(ctx context.Context, proc Process, grace time.Duration) error {
	err := proc.KillGroup(syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		// Group already gone — idempotent no-op (X02).
		return nil
	}
	if err != nil {
		return err
	}

	graceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if _, werr := proc.Wait(graceCtx); werr == nil {
		// Exited within the grace window — no escalation needed.
		return nil
	}

	// The group ignored SIGTERM (or the wait errored): escalate to SIGKILL and
	// reap exactly once. osProcess.Wait is idempotent, so this cannot double-reap.
	if kerr := proc.KillGroup(syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
		return kerr
	}
	_, _ = proc.Wait(ctx)

	return nil
}

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
// (e.g. "seatbelt:<25 base36 chars>"); the darwin backend keys its in-memory
// sessions by the raw id (the 25-char derived id), so the "seatbelt:" slug is
// stripped before the lookup. A bare (non-namespaced) id is passed through
// unchanged.
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
