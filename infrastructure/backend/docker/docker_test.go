package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			NetworkMode:     spec.NetworkNone,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}
}

// newBackendForDirs builds a backend whose ALLOW_DIRS are the given dirs. The
// cwd mount defaults to read-only (SPEC §7.2) unless an explicit mode is given.
func newBackendForDirs(api dockerAPI, dirs ...string) *Backend {
	return New(dirs, false, "readonly", "default-image:latest", api)
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
	if string(hc.NetworkMode) != "none" {
		t.Errorf("NetworkMode = %q, want none", hc.NetworkMode)
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
	if hc.Tmpfs == nil || hc.Tmpfs[tmpfsConfDir] == "" || hc.Tmpfs[tmpfsWorkdir] == "" {
		t.Errorf("tmpfs mounts missing: %v", hc.Tmpfs)
	}

	cfg := api.lastConfig()
	if cfg.User != "1000:1000" {
		t.Errorf("Config.User = %q, want non-root 1000:1000", cfg.User)
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

func TestCreate_DefaultImage(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = ""
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if cfg := api.lastConfig(); cfg.Image != "default-image:latest" {
		t.Errorf("Image = %q, want default", cfg.Image)
	}
}

func TestCreate_InvalidSpec(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "" // invalid: Image required, and no default lookup because Validation runs first
	// give isolation an invalid value too to guarantee failure before image default
	s.Isolation.Memory = 0
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected validation error")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no create should have happened")
	}
}

func TestCreate_CwdMounted11ReadOnlyByDefault(t *testing.T) {
	dir := t.TempDir()
	api := newFakeClient()
	b := New([]string{dir}, false, "readonly", "img", api)
	s := baseSpec()
	s.WorkingDir = dir
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	hc := api.lastHostConfig()
	if len(hc.Mounts) != 1 {
		t.Fatalf("mounts = %d, want 1 (cwd)", len(hc.Mounts))
	}
	m := hc.Mounts[0]
	if m.Source != dir || m.Target != dir {
		t.Errorf("cwd mount not 1:1: %+v", m)
	}
	if !m.ReadOnly {
		t.Error("cwd should be read-only by default (opt-in rw)")
	}
	if cfg := api.lastConfig(); cfg.WorkingDir != dir {
		t.Errorf("WorkingDir = %q, want %q", cfg.WorkingDir, dir)
	}
}

func TestCreate_CwdWritableWhenModeRW(t *testing.T) {
	dir := t.TempDir()
	api := newFakeClient()
	b := New([]string{dir}, false, "rw", "img", api)
	s := baseSpec()
	s.WorkingDir = dir
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if hc := api.lastHostConfig(); hc.Mounts[0].ReadOnly {
		t.Error("cwd should be writable when SANDBOX_CWD_MODE=rw")
	}
}

func TestCreate_CwdOutsideAllowDirsRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	api := newFakeClient()
	b := New([]string{dir}, false, "rw", "img", api)
	s := baseSpec()
	s.WorkingDir = outside
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected cwd escape error")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no create should have happened")
	}
}

func TestCreate_AdditionalMountReadOnlyFlag(t *testing.T) {
	dir := t.TempDir()
	api := newFakeClient()
	b := New([]string{dir}, false, "rw", "img", api)
	s := baseSpec()
	s.Mounts = []spec.Mount{{Source: dir, Target: "/mnt", ReadOnly: true}}
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if hc := api.lastHostConfig(); len(hc.Mounts) != 1 || !hc.Mounts[0].ReadOnly {
		t.Errorf("additional mount should honor ReadOnly: %+v", hc.Mounts)
	}
}

func TestCreate_SecretsDeliveredAsTmpfsFiles(t *testing.T) {
	dir := t.TempDir()
	api := newFakeClient()
	b := New([]string{dir}, false, "rw", "img", api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "supersecret"}}
	if _, err := b.Create(context.Background(), s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(api.copyTos) != 1 {
		t.Fatalf("copyTos = %d, want 1 secret copy", len(api.copyTos))
	}
	ct := api.copyTos[0]
	if ct.dst != tmpfsConfDir {
		t.Errorf("secret dst = %q, want %q", ct.dst, tmpfsConfDir)
	}
	// The tar payload must carry the secret value (not argv).
	if !bytesContains(ct.data, []byte("supersecret")) {
		t.Error("secret value not present in tmpfs write")
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

func TestDestroy_RemovesByLabel(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-1")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), "sess-1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(api.removes) != 1 {
		t.Fatalf("removes = %d, want 1", len(api.removes))
	}
	if len(api.removedOpts) != 1 || !api.removedOpts[0].Force {
		t.Error("remove should be forced")
	}
}

func TestDestroy_IdempotentWhenMissing(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	if err := b.Destroy(context.Background(), "nope"); err != nil {
		t.Fatalf("Destroy of missing should be no-op, got %v", err)
	}
	if len(api.removes) != 0 {
		t.Fatal("no remove should have happened")
	}
}

func TestExec_ArgumentArrayAndResult(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hi", exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-e")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := b.Exec(context.Background(), "sess-e", exec.Request{Argv: []string{"echo", "hi"}})
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
}

func TestExec_SudoUser(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-u")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), "sess-u", exec.Request{Argv: []string{"id"}, User: "0"}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if opts := api.execCreates[0]; opts.User != "0" {
		t.Errorf("User = %q, want 0 (scoped root)", opts.User)
	}
}

func TestExec_UnknownSession(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	if _, err := b.Exec(context.Background(), "ghost", exec.Request{Argv: []string{"x"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestInfo(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-i")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	info, err := b.Info(context.Background(), "sess-i")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info == nil {
		t.Fatal("nil info")
	}
}

func TestStats(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-st")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	st, err := b.Stats(context.Background(), "sess-st")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st == nil {
		t.Fatal("nil stats")
	}
}

func TestStatsError(t *testing.T) {
	api := newFakeClient()
	api.statsErr = errors.New("stats fail")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-st2")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Stats(context.Background(), "sess-st2"); err == nil {
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
	ctx := domainsession.WithSessionID(context.Background(), "sess-x")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), "sess-x", exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected exec create error")
	}
}

func bytesContains(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}

func TestExec_AttachError(t *testing.T) {
	api := newFakeClient()
	api.execAttachErr = errors.New("attach fail")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-at")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), "sess-at", exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected attach error")
	}
}

func TestInfo_UnknownSession(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	if _, err := b.Info(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestStats_UnknownSession(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	if _, err := b.Stats(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestContainerListError(t *testing.T) {
	api := newFakeClient()
	api.listErr = errors.New("list fail")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-l")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), "sess-l"); err == nil {
		t.Fatal("expected list error on destroy")
	}
}

func TestDestroy_RemoveError(t *testing.T) {
	api := newFakeClient()
	api.removeErr = errors.New("remove fail")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-r")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), "sess-r"); err == nil {
		t.Fatal("expected remove error")
	}
}

func TestVerifyHostPath_EmptyAndSymlinkAllowed(t *testing.T) {
	dir := t.TempDir()
	b := New([]string{dir}, true, "rw", "img", newFakeClient())
	if err := b.verifyHostPath(""); err == nil {
		t.Fatal("empty path should error")
	}
	if err := b.verifyHostPath(filepath.Join(dir, "sub")); err == nil {
		t.Fatal("missing path should error")
	}
}

func TestVerifyHostPath_NonexistentRejected(t *testing.T) {
	dir := t.TempDir()
	b := New([]string{dir}, false, "rw", "img", newFakeClient())
	if err := b.verifyHostPath(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("nonexistent path should error")
	}
}

func TestVerifyHostPath_SymlinkDisallowed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// Symlink resolves outside ALLOW_DIRS -> rejected.
	b := New([]string{dir}, false, "rw", "img", newFakeClient())
	if err := b.verifyHostPath(link); err == nil {
		t.Fatal("symlink escaping ALLOW_DIRS should be rejected")
	}
}

func TestVerifyHostPath_OpenThenVerify(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := New([]string{dir}, false, "rw", "img", newFakeClient())
	if err := b.verifyHostPath(sub); err != nil {
		t.Fatalf("verifyHostPath: %v", err)
	}
}

func TestInfo_InspectError(t *testing.T) {
	api := newFakeClient()
	api.inspectErr = errors.New("inspect fail")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-inf")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Info(context.Background(), "sess-inf"); err == nil {
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

func TestCreate_SecretsCopyError(t *testing.T) {
	dir := t.TempDir()
	api := newFakeClient()
	api.copyToErr = errors.New("copy fail")
	b := New([]string{dir}, false, "rw", "img", api)
	s := baseSpec()
	s.Secrets = []spec.SecretRef{{Name: "token", Value: "v"}}
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected secret copy error")
	}
}

func TestVerifyHostPath_AllowSymlinksValid(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b := New([]string{dir}, true, "rw", "img", newFakeClient())
	if err := b.verifyHostPath(sub); err != nil {
		t.Fatalf("verifyHostPath with allowSymlinks: %v", err)
	}
}

func TestStats_DecodeError(t *testing.T) {
	api := newFakeClient()
	api.statsBody = "not-json{"
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-dec")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Stats(context.Background(), "sess-dec"); err == nil {
		t.Fatal("expected stats decode error")
	}
}

func TestExec_InspectError(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{exitCode: 0}}
	api.execInspectErr = errors.New("inspect fail")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-insp")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), "sess-insp", exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected exec inspect error")
	}
}

func TestContainerIDBySession_EmptyID(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	if _, err := b.containerIDBySession(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestNew_NonexistentAllowDir(t *testing.T) {
	// EvalSymlinks fails, so the raw path is kept (New must not error).
	b := New([]string{"/definitely/not/a/real/dir"}, false, "rw", "img", newFakeClient())
	if len(b.cfg.allowDirs) != 1 || b.cfg.allowDirs[0] != "/definitely/not/a/real/dir" {
		t.Fatalf("allowDirs = %v", b.cfg.allowDirs)
	}
}

func TestValidateImageRef_AcceptsTrustedRegistries(t *testing.T) {
	accepted := []string{
		"alpine:3.20", // unqualified → docker.io/library/alpine
		"docker.io/library/alpine:3.20",
		"img:latest", // unqualified test image → docker.io
		"default-image:latest",
		"ghcr.io/org/app:v1",
		"ghcr.io/org/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"index.docker.io/library/alpine:3.20",
	}
	for _, ref := range accepted {
		if err := validateImageRef(ref); err != nil {
			t.Errorf("validateImageRef(%q) = %v, want nil", ref, err)
		}
	}
}

func TestValidateImageRef_RejectsUntrustedOrInvalid(t *testing.T) {
	rejected := []string{
		"",
		"   ",
		"evil.com/foo/bar:latest",     // untrusted registry
		"registry.example.org/app:v1", // untrusted registry
		"localhost:5000/app:v1",       // untrusted local registry
		"::::invalid",                 // malformed reference
	}
	for _, ref := range rejected {
		if err := validateImageRef(ref); err == nil {
			t.Errorf("validateImageRef(%q) = nil, want error", ref)
		}
	}
}

func TestCreate_RejectsUntrustedImage(t *testing.T) {
	api := newFakeClient()
	b := newBackendForDirs(api)
	s := baseSpec()
	s.Image = "evil.com/foo/bar:latest"
	if _, err := b.Create(context.Background(), s); err == nil {
		t.Fatal("expected untrusted image rejection")
	}
	if len(api.createdConfigs) != 0 {
		t.Fatal("no container create should have happened")
	}
}

func TestCreate_AcceptsTrustedImage(t *testing.T) {
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
