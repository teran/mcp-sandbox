package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

// baseSpec returns a valid spec satisfying spec.Validate.
func baseSpec() spec.SandboxSpec {
	return spec.SandboxSpec{
		Image: "img:latest",
		Isolation: spec.Isolation{
			ReadOnlyRootfs:  true,
			CapDrop:         []string{"ALL"},
			NoNewPrivileges: true,
			Seccomp:         spec.SeccompDefault,
			User:            spec.UIDGID{UID: 1000, GID: 1000},
			PidsLimit:       100,
			Memory:          128 * 1024 * 1024,
			MemorySwap:      128 * 1024 * 1024,
			NanoCPUs:        1_000_000_000,
			NetworkMode:     spec.NetworkBridge,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}
}

// newBackendForDirs builds a backend with New(api, dirs): the second argument
// is the ALLOW_DIRS host-path gate (Phase 7), which resolveMounts enforces for
// the cwd mount and every mount source. Pass the allowed roots a scenario needs.
func newBackendForDirs(api dockerAPI, dirs ...string) *Backend {
	return New(api, dirs)
}

func TestNamePriorityCapabilities(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	if b.Name() != "docker" {
		t.Fatalf("Name = %q", b.Name())
	}
	if b.Priority() != 0 {
		t.Fatalf("Priority = %d", b.Priority())
	}
	c := b.Capabilities()
	if !c.Lifecycle || !c.Executable || !c.Introspectable {
		t.Fatalf("capabilities = %+v", c)
	}
}

// TestCreate_AllowsHostNetwork pins Phase 7: with the network-allowlist
// removed and NetworkHost a valid mode, host networking is accepted and applied
// (the operator opt-in for host is enforced at the config/profile layer, not
// here).
func TestCreate_AllowsHostNetwork(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Isolation.NetworkMode = spec.NetworkHost
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("host should be allowed: %v", err)
	}
	hc := api.lastHostConfig()
	if string(hc.NetworkMode) != "host" {
		t.Fatalf("NetworkMode = %q, want host", hc.NetworkMode)
	}
}

func TestCreate_AppliesIsolation(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	hc := api.lastHostConfig()
	if hc == nil {
		t.Fatal("no host config recorded")
	}
	if !hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs must be true (immutable)")
	}
	found := false
	for _, c := range hc.CapDrop {
		if strings.EqualFold(c, "ALL") {
			found = true
		}
	}
	if !found {
		t.Errorf("CapDrop must include ALL: %v", hc.CapDrop)
	}
	hasNoNewPriv := false
	for _, s := range hc.SecurityOpt {
		if s == "no-new-privileges" {
			hasNoNewPriv = true
		}
	}
	if !hasNoNewPriv {
		t.Error("no-new-privileges security opt missing")
	}
	if !hc.AutoRemove {
		t.Error("AutoRemove must be true")
	}
	if string(hc.NetworkMode) != "bridge" {
		t.Errorf("NetworkMode = %q, want bridge", hc.NetworkMode)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != 100 {
		t.Errorf("PidsLimit wrong")
	}
	if hc.Memory != 128*1024*1024 || hc.MemorySwap != 128*1024*1024 {
		t.Errorf("Memory bounds wrong")
	}
	if hc.NanoCPUs != 1_000_000_000 {
		t.Errorf("NanoCPUs wrong")
	}
	if hc.Tmpfs == nil || hc.Tmpfs[tmpfsRunDir] == "" || hc.Tmpfs[tmpfsTmpDir] == "" {
		t.Errorf("tmpfs mounts missing: %v", hc.Tmpfs)
	}

	cfg := api.lastConfig()
	if cfg.User != "1000:1000" {
		t.Errorf("Config.User = %q, want non-root 1000:1000", cfg.User)
	}
}

// TestCreate_TmpfsLayout locks the reworked tmpfs layout (SPEC §7.1):
//   - the two separate tmpfs mounts (/run/sandbox and /workspace) are gone;
//   - /run is a tmpfs, owned by the sandbox user, and stays noexec (a secrets
//     location does not need to execute);
//   - /tmp is a tmpfs, writable AND executable (no noexec), so the sandbox can
//     compile and run Go binaries against a read-only rootfs; nosuid,nodev kept.
func TestCreate_TmpfsLayout(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	if _, err := b.Create(context.Background(), baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	hc := api.lastHostConfig()
	tm := hc.Tmpfs
	if tm == nil {
		t.Fatal("no tmpfs mounts")
	}
	// The old separate mounts must not exist.
	if _, ok := tm["/run/sandbox"]; ok {
		t.Error("/run/sandbox must not be a separate tmpfs mount")
	}
	if _, ok := tm["/workspace"]; ok {
		t.Error("/workspace must not be a separate tmpfs mount")
	}
	// /run is a tmpfs, owned by the sandbox user, and stays noexec.
	run, ok := tm["/run"]
	if !ok {
		t.Fatal("/run must be a tmpfs")
	}
	if !strings.Contains(run, "uid=1000") || !strings.Contains(run, "gid=1000") {
		t.Errorf("/run tmpfs must be owned by sandbox user 1000:1000: %q", run)
	}
	if !strings.Contains(run, "noexec") {
		t.Errorf("/run tmpfs must keep noexec: %q", run)
	}
	// /tmp is a tmpfs, writable and executable (no noexec), nosuid,nodev kept.
	tmp, ok := tm["/tmp"]
	if !ok {
		t.Fatal("/tmp must be a tmpfs")
	}
	if strings.Contains(tmp, "noexec") {
		t.Errorf("/tmp tmpfs must NOT be noexec (must be executable): %q", tmp)
	}
	// Explicit "exec" guards against Docker Desktop / some daemons forcing
	// noexec on tmpfs, which would prevent running compiled Go binaries.
	if !strings.Contains(tmp, "exec") {
		t.Errorf("/tmp tmpfs must explicitly enable exec: %q", tmp)
	}
	if !strings.Contains(tmp, "nosuid") || !strings.Contains(tmp, "nodev") {
		t.Errorf("/tmp tmpfs must keep nosuid,nodev: %q", tmp)
	}
}

func TestCreate_LabelsAndSessionID(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-123")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cfg := api.lastConfig()
	if cfg.Labels[ownerLabelKey] != ownerLabelValue {
		t.Errorf("owner label missing: %v", cfg.Labels)
	}
	if cfg.Labels[sessionLabelKey] != "sess-123" {
		t.Errorf("session label = %q, want sess-123", cfg.Labels[sessionLabelKey])
	}
}

func TestCreate_OpenCodeSessionLabel(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.ClientSessionID = "opencode-sess-1"
	ctx := domainsession.WithSessionID(context.Background(), "sess-123")
	if _, err := b.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cfg := api.lastConfig()
	if cfg.Labels[opencodeSessionLabelKey] != "opencode-sess-1" {
		t.Errorf("opencode session label = %q, want opencode-sess-1", cfg.Labels[opencodeSessionLabelKey])
	}
	// The existing owner and sandbox-session labels must remain intact.
	if cfg.Labels[ownerLabelKey] != ownerLabelValue {
		t.Errorf("owner label missing: %v", cfg.Labels)
	}
	if cfg.Labels[sessionLabelKey] != "sess-123" {
		t.Errorf("session label = %q, want sess-123", cfg.Labels[sessionLabelKey])
	}
}

func TestCreate_NoOpenCodeSessionLabelWhenEmpty(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	if _, err := b.Create(context.Background(), baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cfg := api.lastConfig()
	if _, ok := cfg.Labels[opencodeSessionLabelKey]; ok {
		t.Errorf("opencode session label must not be set when empty: %v", cfg.Labels)
	}
}

func TestCreate_InvalidSpec(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "" // invalid: Image required
	s.Isolation.Memory = 0
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected validation error")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no create should have happened")
	}
}

// TestCreate_MountsWithinAllowDirsPassThrough pins Phase 7: resolveMounts keeps
// the ALLOW_DIRS host-path gate. The cwd and every mount source within an
// allowed root pass through 1:1 with their ReadOnly flag honoured.
func TestCreate_MountsWithinAllowDirsPassThrough(t *testing.T) {
	api := newFakeClient()
	allowDir := t.TempDir()
	proj := filepath.Join(allowDir, "proj")
	other := filepath.Join(allowDir, "other")
	for _, d := range []string{proj, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := newBackendForDirs(api, allowDir)
	s := baseSpec()
	s.WorkingDir = proj
	s.Mounts = []spec.Mount{
		{Source: proj, Target: proj, ReadOnly: true}, // cwd entry
		{Source: other, Target: "/mnt", ReadOnly: false},
	}
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	hc := api.lastHostConfig()
	if len(hc.Mounts) != 2 {
		t.Fatalf("mounts = %d, want 2", len(hc.Mounts))
	}
	if hc.Mounts[0].Source != proj || hc.Mounts[0].Target != proj || !hc.Mounts[0].ReadOnly {
		t.Fatalf("cwd mount not passed through: %+v", hc.Mounts[0])
	}
	if hc.Mounts[1].Source != other || hc.Mounts[1].Target != "/mnt" || hc.Mounts[1].ReadOnly {
		t.Fatalf("mount not passed through: %+v", hc.Mounts[1])
	}
	if cfg := api.lastConfig(); cfg.WorkingDir != proj {
		t.Errorf("WorkingDir = %q", cfg.WorkingDir)
	}
}

// TestCreate_CwdOutsideAllowDirsRejected verifies a cwd whose host path is not
// within ANY allowed root is rejected before a container is created.
func TestCreate_CwdOutsideAllowDirsRejected(t *testing.T) {
	api := newFakeClient()
	allowDir := t.TempDir()
	outside := t.TempDir() // exists, but is not under allowDir
	b := newBackendForDirs(api, allowDir)
	s := baseSpec()
	s.WorkingDir = outside
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected cwd outside ALLOW_DIRS to be rejected")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no container create should have happened")
	}
}

// TestCreate_MountOutsideAllowDirsRejected verifies a profile mount source
// outside ALLOW_DIRS is rejected (the same gate guards the cwd and every mount).
func TestCreate_MountOutsideAllowDirsRejected(t *testing.T) {
	api := newFakeClient()
	allowDir := t.TempDir()
	outside := t.TempDir() // exists, but is not under allowDir
	b := newBackendForDirs(api, allowDir)
	s := baseSpec()
	s.Mounts = []spec.Mount{{Source: outside, Target: "/mnt", ReadOnly: true}}
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected mount source outside ALLOW_DIRS to be rejected")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no container create should have happened")
	}
}

func TestCreate_AdditionalMountReadOnlyFlag(t *testing.T) {
	api := newFakeClient()
	allowDir := t.TempDir()
	src := filepath.Join(allowDir, "host")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	b := newBackendForDirs(api, allowDir)
	s := baseSpec()
	s.Mounts = []spec.Mount{{Source: src, Target: "/mnt", ReadOnly: true}}
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if hc := api.lastHostConfig(); len(hc.Mounts) != 1 || !hc.Mounts[0].ReadOnly {
		t.Errorf("additional mount should honor ReadOnly: %+v", hc.Mounts)
	}
}

func TestCreate_SecretsDeliveredViaExecOnTmpfs(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "supersecret"}}
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The /run tmpfs must be owned by the sandbox user so the unprivileged
	// process can create the /run/sandbox secret dir and its own 0600 secret
	// files (§7.3).
	hc := api.lastHostConfig()
	if !strings.Contains(hc.Tmpfs[tmpfsRunDir], "uid=1000") || !strings.Contains(hc.Tmpfs[tmpfsRunDir], "gid=1000") {
		t.Errorf("secrets tmpfs %q must be owned by sandbox user 1000:1000", hc.Tmpfs[tmpfsRunDir])
	}
	// Secrets must NOT be delivered via CopyToContainer (`docker cp`), which is
	// incompatible with a read-only rootfs (§7.1); they are written by a docker
	// exec instead.
	if len(api.execCreates) != 1 {
		t.Fatalf("exec creates = %d, want 1 (secret writer)", len(api.execCreates))
	}
	opts := api.execCreates[0]
	// The writer argv is a fixed script; the secret value must never appear in
	// argv, image or env exposed to exec (§7.3).
	if strings.Contains(strings.Join(opts.Cmd, "\x00"), "supersecret") {
		t.Fatal("secret value leaked into exec argv")
	}
	// The writer must target /run/sandbox/token inside the sandbox.
	last := opts.Cmd[len(opts.Cmd)-1]
	if last != filepath.Join(tmpfsConfDir, "token") {
		t.Fatalf("secret writer target = %q, want %q", last, filepath.Join(tmpfsConfDir, "token"))
	}
	// The secret value must be streamed over the exec's stdin (never argv).
	if len(api.execStdin) != 1 || !bytes.Contains(api.execStdin[0], []byte("supersecret")) {
		t.Fatalf("secret value not delivered on exec stdin, got %d payloads", len(api.execStdin))
	}
}

func TestCreate_StartError(t *testing.T) {
	api := newFakeClient()
	api.startErr = errors.New("start failed")
	b := newBackendForDirs(api)
	if _, err := b.Create(context.Background(), baseSpec()); err == nil {
		t.Fatal("expected start error")
	}
}

// TestDestroy_RemovesContainerByID pins the §7.8 contract: the session id IS the
// container id, so Destroy uses the passed id directly as the container id (no
// label lookup). The returned id from Create is what a caller passes back.
func TestDestroy_RemovesContainerByID(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("empty container id")
	}
	if err := b.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(api.removes) != 1 {
		t.Fatalf("removes = %d, want 1", len(api.removes))
	}
	if api.removes[0] != id {
		t.Fatalf("removed container id = %q, want %q (container id == session id)", api.removes[0], id)
	}
	if len(api.removedOpts) != 1 || !api.removedOpts[0].Force {
		t.Error("remove should be forced")
	}
}

// TestDestroy_IdempotentWhenMissing pins X02: destroying a session with no
// backing container is a no-op. Because the session id IS the container id, the
// backend attempts a docker remove of the unknown id and must treat the daemon's
// "no such container" error as an idempotent no-op.
func TestDestroy_IdempotentWhenMissing(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	if err := b.Destroy(context.Background(), "nope"); err != nil {
		t.Fatalf("Destroy of missing should be no-op, got %v", err)
	}
}

func TestExec_ArgumentArrayAndResult(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hi", exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "hi" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	opts := api.execCreates[0]
	if len(opts.Cmd) != 2 || opts.Cmd[0] != "echo" || opts.Cmd[1] != "hi" {
		t.Errorf("argv not passed as array: %v", opts.Cmd)
	}
	if opts.Privileged {
		t.Error("--privileged must never be used")
	}
	// The container id used by Exec must equal the id returned from Create
	// (session id == container id, §7.8).
	if len(api.execContainerIDs) != 1 || api.execContainerIDs[0] != id {
		t.Fatalf("exec container id = %v, want exactly [%q]", api.execContainerIDs, id)
	}
}

// TestExec_TimeoutIsAppliedAsContextCancellation verifies that req.Timeout
// (seconds) is applied as a context.WithTimeout deadline on the ctx handed to
// the Docker exec API calls, so the timeout turns into a cancellation. The fake
// client records the deadline of the ctx each exec call receives; the recorded
// deadline must land at (time of the call) + req.Timeout seconds.
func TestExec_TimeoutIsAppliedAsContextCancellation(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const timeout uint64 = 42
	before := time.Now()
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"echo", "hi"}, Timeout: timeout}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	after := time.Now()

	dl := api.lastExecDeadline()
	if dl.IsZero() {
		t.Fatal("expected a context deadline derived from req.Timeout, got none")
	}
	// The deadline is set inside Exec to (now at exec time) + timeout. The exec
	// ran between before and after, so the deadline must fall within
	// [before+timeout, after+timeout].
	wantMin := before.Add(time.Duration(timeout) * time.Second)
	wantMax := after.Add(time.Duration(timeout) * time.Second)
	if dl.Before(wantMin) || dl.After(wantMax) {
		t.Fatalf("deadline = %v, want within [%v, %v] (= now + %ds)", dl, wantMin, wantMax, timeout)
	}
}

// TestExec_TimeoutZeroNoDeadline verifies the edge case: with req.Timeout == 0
// no context.WithTimeout deadline is applied, so the ctx passed to the Docker
// exec API carries no deadline.
func TestExec_TimeoutZeroNoDeadline(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"echo", "hi"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if dl := api.lastExecDeadline(); !dl.IsZero() {
		t.Fatalf("deadline = %v, want none when req.Timeout == 0", dl)
	}
}

func TestExec_SudoUser(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"id"}, User: "0"}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if opts := api.execCreates[0]; opts.User != "0" {
		t.Errorf("User = %q, want 0 (scoped root)", opts.User)
	}
}

func TestExec_UnknownSession(t *testing.T) {
	// Because the session id IS the container id, Exec of an unknown id reaches
	// the Docker API directly and fails naturally ("no such container") rather
	// than through a session-existence pre-check.
	b := newBackendForDirs(newFakeClient())
	if _, err := b.Exec(context.Background(), "ghost", exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected an error for an unknown container id")
	}
}

// TestExec_EnvSetSorted pins that the per-exec env map is converted to a
// sorted KEY=VALUE slice on ExecOptions.Env.
func TestExec_EnvSetSorted(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	req := exec.Request{Argv: []string{"bash", "-c", "env"}, Env: map[string]string{"Z": "last", "A": "first", "B": "mid"}}
	if _, err := b.Exec(context.Background(), id, req); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	opts := api.execCreates[0]
	want := []string{"A=first", "B=mid", "Z=last"} // sorted by key
	if !reflect.DeepEqual(opts.Env, want) {
		t.Fatalf("Env = %v, want %v", opts.Env, want)
	}
}

// TestExec_EnvNilWhenEmpty pins that a nil/empty env map produces a nil
// ExecOptions.Env (no override).
func TestExec_EnvNilWhenEmpty(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "env"}}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if opts := api.execCreates[0]; opts.Env != nil {
		t.Fatalf("Env = %v, want nil when env empty", opts.Env)
	}
}

// TestExec_CmdWrapperPassThrough pins that the argv wrapper (built by the tool
// layer) is passed through to ExecOptions.Cmd untouched.
func TestExec_CmdWrapperPassThrough(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wrapper := []string{"bash", "-l", "-c", "git status"}
	if _, err := b.Exec(context.Background(), id, exec.Request{Command: "git status", Argv: wrapper}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	opts := api.execCreates[0]
	if !reflect.DeepEqual(opts.Cmd, wrapper) {
		t.Fatalf("Cmd = %v, want wrapper %v", opts.Cmd, wrapper)
	}
}

func TestInfo(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := b.Info(context.Background(), id)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info == nil {
		t.Fatal("nil info")
	}
	// The container id used by Info must equal the id returned from Create.
	if len(api.inspects) != 1 || api.inspects[0] != id {
		t.Fatalf("inspected container id = %v, want exactly [%q]", api.inspects, id)
	}
}

func TestStats(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	st, err := b.Stats(context.Background(), id)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st == nil {
		t.Fatal("nil stats")
	}
	// The container id used by Stats must equal the id returned from Create.
	if len(api.statsCalls) != 1 || api.statsCalls[0] != id {
		t.Fatalf("stats container id = %v, want exactly [%q]", api.statsCalls, id)
	}
}

func TestStatsError(t *testing.T) {
	api := newFakeClient()
	api.statsErr = errors.New("stats fail")
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Stats(context.Background(), id); err == nil {
		t.Fatal("expected stats error")
	}
}

func TestCreate_StartErrorNoPartialState(t *testing.T) {
	api := newFakeClient()
	api.startErr = errors.New("boom")
	b := newBackendForDirs(api)
	if _, err := b.Create(context.Background(), baseSpec()); err == nil {
		t.Fatal("expected error")
	}
}

func TestExec_ErrFromCreate(t *testing.T) {
	api := newFakeClient()
	api.execErr = errors.New("exec create fail")
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected exec create error")
	}
}

func TestExec_AttachError(t *testing.T) {
	api := newFakeClient()
	api.execAttachErr = errors.New("attach fail")
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected attach error")
	}
}

func TestInfo_UnknownSession(t *testing.T) {
	// The session id IS the container id: Info of an unknown id reaches the
	// Docker inspect API and fails naturally rather than via a session-existence
	// pre-check.
	b := newBackendForDirs(newFakeClient())
	if _, err := b.Info(context.Background(), "ghost"); err == nil {
		t.Fatal("expected an error for an unknown container id")
	}
}

func TestStats_UnknownSession(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	if _, err := b.Stats(context.Background(), "ghost"); err == nil {
		t.Fatal("expected an error for an unknown container id")
	}
}

func TestDestroy_RemoveError(t *testing.T) {
	api := newFakeClient()
	api.removeErr = errors.New("remove fail")
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), id); err == nil {
		t.Fatal("expected remove error")
	}
}

func TestInfo_InspectError(t *testing.T) {
	api := newFakeClient()
	api.inspectErr = errors.New("inspect fail")
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Info(context.Background(), id); err == nil {
		t.Fatal("expected inspect error")
	}
}

func TestNewFromConfig_Error(t *testing.T) {
	// A host without "://" fails ParseHostURL in WithHost.
	cfg := backend.Config{DockerHost: "not-a-url"}
	if _, err := newFromConfig(cfg); err == nil {
		t.Fatal("expected newClient error")
	}
}

func TestCreate_SecretsExecCreateError(t *testing.T) {
	api := newFakeClient()
	api.execErr = errors.New("exec create fail")
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "v"}}
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected secret exec create error")
	}
}

func TestCreate_SecretsExecNonZeroExit(t *testing.T) {
	api := newFakeClient()
	api.execExitCode = 1
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "v"}}
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected secret exec non-zero exit error")
	}
}

func TestCreate_SecretsExecAttachError(t *testing.T) {
	api := newFakeClient()
	api.execAttachErr = errors.New("attach fail")
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "v"}}
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected secret exec attach error")
	}
}

func TestCreate_SecretsExecInspectError(t *testing.T) {
	api := newFakeClient()
	api.execInspectErr = errors.New("inspect fail")
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "v"}}
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected secret exec inspect error")
	}
}

func TestStats_DecodeError(t *testing.T) {
	api := newFakeClient()
	api.statsBody = "not-json{"
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Stats(context.Background(), id); err == nil {
		t.Fatal("expected stats decode error")
	}
}

func TestExec_InspectError(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	api.execInspectErr = errors.New("inspect fail")
	b := newBackendForDirs(api)
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected exec inspect error")
	}
}

// TestValidateImageRef_ParseOnly pins Phase 7: image validation is parse-only.
// Any valid image reference — including one from an arbitrary/untrusted registry
// — is accepted, because TRUSTED_REGISTRIES was removed and the image source now
// comes from the operator-authored profile. Only empty or unparsable references
// are rejected.
func TestValidateImageRef_ParseOnly(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	accepted := []string{
		"alpine:3.20", // unqualified → docker.io/library/alpine
		"img:latest",
		"docker.io/library/alpine:3.20",
		"ghcr.io/org/app:v1",
		"ghcr.io/org/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"evil.com/foo/bar:latest", // arbitrary registry accepted (parse-only)
		"localhost:5000/app:v1",   // arbitrary registry accepted (parse-only)
	}
	for _, ref := range accepted {
		if err := b.validateImageRef(ref); err != nil {
			t.Errorf("validateImageRef(%q) = %v, want nil", ref, err)
		}
	}
}

// TestValidateImageRef_RejectsEmptyOrInvalid pins the parse-only rejection side:
// empty or malformed references are rejected, regardless of registry.
func TestValidateImageRef_RejectsEmptyOrInvalid(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	rejected := []string{
		"",
		"   ",
		"::::invalid", // malformed reference
		"a b",         // internal whitespace is not a valid reference
	}
	for _, ref := range rejected {
		if err := b.validateImageRef(ref); err == nil {
			t.Errorf("validateImageRef(%q) = nil, want error", ref)
		}
	}
}

// TestCreate_AcceptsImageFromAnyRegistry verifies a valid image reference from an
// arbitrary registry is accepted and created (parse-only policy; no registry
// allowlist at create time).
func TestCreate_AcceptsImageFromAnyRegistry(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "evil.com/foo/bar:latest"
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("create with arbitrary-registry image should succeed (parse-only): %v", err)
	}
	if len(api.createdConfigs) != 1 {
		t.Fatalf("created = %d, want 1", len(api.createdConfigs))
	}
}

// TestCreate_RejectsInvalidImage verifies a malformed image reference is rejected
// before any Docker side-effect (parse-only validation).
func TestCreate_RejectsInvalidImage(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "::::invalid"
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected invalid image rejection")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no container create should have happened")
	}
	if pulls := api.pulledRefs(); len(pulls) != 0 {
		t.Fatalf("no image pull should happen for an invalid image: %v", pulls)
	}
}

func TestCreate_AcceptsValidImage(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "alpine:3.20"
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(api.createdConfigs) != 1 {
		t.Fatalf("created = %d, want 1", len(api.createdConfigs))
	}
}

// TestCreate_ImageAlreadyPresent_NoPull verifies that when the image is already
// in the local cache the create succeeds without issuing an image pull — pulls
// are only a fallback for a missing image (parse-only validation; no registry
// allowlist).
func TestCreate_ImageAlreadyPresent_NoPull(t *testing.T) {
	api := newFakeClient()
	api.requirePull = false // image is already local
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "alpine:3.20"
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(api.createdConfigs) != 1 {
		t.Fatalf("created = %d, want 1", len(api.createdConfigs))
	}
	if pulls := api.pulledRefs(); len(pulls) != 0 {
		t.Fatalf("unexpected image pulls for a local image: %v", pulls)
	}
}

// TestCreate_MissingImage_PullsThenCreates verifies the auto-pull path: when the
// image is not present locally the first ContainerCreate fails with "No such
// image", the backend pulls the (validated) image and retries the create, which
// then succeeds.
func TestCreate_MissingImage_PullsThenCreates(t *testing.T) {
	api := newFakeClient()
	api.requirePull = true // image is not in the local cache
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "alpine:3.20"
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pulls := api.pulledRefs(); len(pulls) != 1 || pulls[0] != "alpine:3.20" {
		t.Fatalf("pulled = %v, want exactly [alpine:3.20]", pulls)
	}
	if len(api.createdConfigs) != 1 {
		t.Fatalf("created = %d, want 1 (the retried create)", len(api.createdConfigs))
	}
}

// TestCreate_PullError verifies a failed pull is surfaced as a clear error and
// no container is created (no partial state).
func TestCreate_PullError(t *testing.T) {
	api := newFakeClient()
	api.requirePull = true
	api.pullErr = errors.New("pull exploded")
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "alpine:3.20"
	_, err := b.Create(context.Background(), s)
	if err == nil {
		t.Fatal("expected pull error")
	}
	if !strings.Contains(err.Error(), "pull exploded") {
		t.Fatalf("error = %v, want pull error surfaced", err)
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no container should be created when the pull fails")
	}
}

// TestCreate_PullReadError verifies a failure while draining the pull stream is
// surfaced (image content is never logged, but a broken stream must not be
// silently swallowed).
func TestCreate_PullReadError(t *testing.T) {
	api := newFakeClient()
	api.requirePull = true
	api.pullBodyErr = errors.New("stream broken")
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "alpine:3.20"
	_, err := b.Create(context.Background(), s)
	if err == nil {
		t.Fatal("expected pull stream error")
	}
	if !strings.Contains(err.Error(), "stream broken") {
		t.Fatalf("error = %v, want pull stream error surfaced", err)
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no container should be created when the pull stream fails")
	}
}

// TestCreate_CreateError verifies a generic (non-"No such image") create error
// is surfaced unchanged and does not trigger an image pull.
func TestCreate_CreateError(t *testing.T) {
	api := newFakeClient()
	api.createErr = errors.New("daemon exploded")
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "alpine:3.20"
	_, err := b.Create(context.Background(), s)
	if err == nil {
		t.Fatal("expected create error")
	}
	if !strings.Contains(err.Error(), "daemon exploded") {
		t.Fatalf("error = %v, want create error surfaced", err)
	}
	if pulls := api.pulledRefs(); len(pulls) != 0 {
		t.Fatalf("no image pull should happen for a generic create error: %v", pulls)
	}
}
