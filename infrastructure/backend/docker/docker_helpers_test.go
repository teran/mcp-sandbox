package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// TestBuildHostConfig_NetworkMode pins Phase 7: the network-allowlist coercion
// is removed, so buildHostConfig maps the spec's network mode straight through
// (host included). It still applies the immutable Isolation hardening and
// bounded-resource fields.
func TestBuildHostConfig_NetworkMode(t *testing.T) {
	b := newBackend(newFakeClient())

	// bridge (the default) passes through.
	s := baseSpec()
	s.Isolation.NetworkMode = spec.NetworkBridge
	hc := b.buildHostConfig(s, nil, nil)
	if string(hc.NetworkMode) != "bridge" {
		t.Fatalf("bridge NetworkMode = %q, want bridge", hc.NetworkMode)
	}

	// host passes through unchanged (no allowlist to coerce it to none).
	s2 := baseSpec()
	s2.Isolation.NetworkMode = spec.NetworkHost
	hc2 := b.buildHostConfig(s2, nil, nil)
	if string(hc2.NetworkMode) != "host" {
		t.Fatalf("host NetworkMode = %q, want host", hc2.NetworkMode)
	}

	// an empty mode is still coerced to none.
	s3 := baseSpec()
	s3.Isolation.NetworkMode = ""
	hc3 := b.buildHostConfig(s3, nil, nil)
	if string(hc3.NetworkMode) != "none" {
		t.Fatalf("empty NetworkMode = %q, want none", hc3.NetworkMode)
	}

	// Ulimits passthrough.
	s4 := baseSpec()
	s4.Isolation.Ulimits = []spec.Ulimit{{Name: "nofile", Soft: 100, Hard: 200}}
	hc4 := b.buildHostConfig(s4, nil, nil)
	if len(hc4.Ulimits) != 1 || hc4.Ulimits[0].Name != "nofile" {
		t.Fatalf("ulimits = %+v", hc4.Ulimits)
	}
}

// TestBuildHostConfig_AppliesImmutableHardening confirms that removing the
// network coercion did not weaken the immutable Isolation hardening applied by
// buildHostConfig.
func TestBuildHostConfig_AppliesImmutableHardening(t *testing.T) {
	b := newBackend(newFakeClient())
	hc := b.buildHostConfig(baseSpec(), nil, nil)
	if !hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs must be true")
	}
	found := false
	for _, c := range hc.CapDrop {
		if c == "ALL" {
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
	if hc.PidsLimit == nil || *hc.PidsLimit != 100 {
		t.Errorf("PidsLimit = %v, want bounded 100", hc.PidsLimit)
	}
	if hc.Memory != 128*1024*1024 {
		t.Errorf("Memory = %d", hc.Memory)
	}
}

// TestBuildHostConfig_NoSeccompEmission pins R-3: the Docker backend must NEVER
// emit a seccomp SecurityOpt. The daemon's default seccomp profile applies
// automatically, Docker rejects `seccomp=default`, and emitting
// `seccomp=unconfined` would weaken security (forbidden). The Seccomp field is an
// honest marker enforced by the closed-enum invariant in spec.Validate /
// EnforceHardening — it is not a docker SecurityOpt and must not be mapped into
// one, so this guards against a future weakening in the backend itself.
func TestBuildHostConfig_NoSeccompEmission(t *testing.T) {
	b := newBackend(newFakeClient())
	hc := b.buildHostConfig(baseSpec(), nil, nil)
	for _, s := range hc.SecurityOpt {
		if s == "seccomp=unconfined" || strings.HasPrefix(s, "seccomp=") {
			t.Fatalf("buildHostConfig must not emit a seccomp SecurityOpt, got %q", s)
		}
	}
}

// TestResolveMounts pins Slice 4: resolveMounts passes every existing host path
// through 1:1 with its ReadOnly flag honoured, and no longer enforces an
// ALLOW_DIRS containment set of its own — that decision moved to
// EnforceHardening at the Service level (SPEC §6.11 G). The backend retains
// TOCTOU integrity: it still EvalSymlinks the path and rejects one that does not
// resolve (open-then-verify is pinned in docker_verify_test.go).
func TestResolveMounts(t *testing.T) {
	allowDir := t.TempDir()
	proj := filepath.Join(allowDir, "proj")
	w := filepath.Join(allowDir, "w")
	for _, d := range []string{proj, w} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := newBackend(newFakeClient())

	// A read-only cwd entry passes through 1:1.
	s := baseSpec()
	s.WorkingDir = proj
	mounts, err := b.resolveMounts(s)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].Source != proj || mounts[0].Target != proj {
		t.Fatalf("cwd mount = %+v", mounts)
	}

	// A writable mount stays writable (ReadOnly honoured).
	s2 := baseSpec()
	s2.Mounts = []spec.Mount{{Source: w, Target: "/w", ReadOnly: false}}
	mounts, err = b.resolveMounts(s2)
	if err != nil {
		t.Fatal(err)
	}
	if mounts[0].ReadOnly {
		t.Fatal("writable mount must not be forced read-only")
	}

	// Containment is lifted: an existing source outside any allowed root is now
	// ACCEPTED (the backend no longer rejects on its own; EnforceHardening owns
	// the gate at the Service level).
	s3 := baseSpec()
	s3.WorkingDir = t.TempDir() // exists, outside any (now-absent) allow set
	if _, err := b.resolveMounts(s3); err != nil {
		t.Fatalf("cwd outside a former allow set must be accepted by the backend: %v", err)
	}

	// TOCTOU integrity retained: a source that does not exist fails EvalSymlinks
	// and is rejected before any mount is produced.
	s4 := baseSpec()
	s4.Mounts = []spec.Mount{{Source: filepath.Join(allowDir, "does-not-exist"), Target: "/mnt", ReadOnly: true}}
	if _, err := b.resolveMounts(s4); err == nil {
		t.Fatal("non-existent mount source must be rejected (EvalSymlinks)")
	}
}

// TestResolveMounts_CwdWritableHonorsProfileMode pins the fix for the profile's
// cwd.mode: rw being ignored: when the spec's first mount is the cwd entry with
// ReadOnly:false (as BuildSpec produces for cwd.mode: rw), the WorkingDir bind
// mount is writable and the cwd is not double-mounted.
func TestResolveMounts_CwdWritableHonorsProfileMode(t *testing.T) {
	allowDir := t.TempDir()
	proj := filepath.Join(allowDir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	b := newBackend(newFakeClient())

	s := baseSpec()
	s.WorkingDir = proj
	s.Mounts = []spec.Mount{
		{Source: proj, Target: proj, ReadOnly: false},  // cwd entry (cwd.mode: rw)
		{Source: proj, Target: "/mnt", ReadOnly: true}, // extra mount, same source
	}
	mounts, err := b.resolveMounts(s)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	// Exactly two mounts: the cwd bind + the extra mount (no double-mount).
	if len(mounts) != 2 {
		t.Fatalf("mounts = %d, want 2 (cwd + extra, no double-mount): %+v", len(mounts), mounts)
	}
	if mounts[0].Source != proj || mounts[0].Target != proj {
		t.Fatalf("cwd mount = %+v", mounts[0])
	}
	if mounts[0].ReadOnly {
		t.Fatal("cwd bind mount must be writable when cwd.mode: rw")
	}
	// The extra mount's own ReadOnly flag is still honoured.
	if !mounts[1].ReadOnly {
		t.Fatal("extra mount should keep its own ReadOnly flag")
	}
}

// TestResolveMounts_CwdReadOnlyByDefault pins the unchanged default: with no
// cwd entry (or an explicit ro cwd entry) the WorkingDir bind mount stays
// read-only.
func TestResolveMounts_CwdReadOnlyByDefault(t *testing.T) {
	allowDir := t.TempDir()
	proj := filepath.Join(allowDir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	b := newBackend(newFakeClient())

	// No cwd entry in Mounts → default read-only.
	s := baseSpec()
	s.WorkingDir = proj
	mounts, err := b.resolveMounts(s)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].Source != proj || mounts[0].Target != proj {
		t.Fatalf("cwd mount = %+v", mounts)
	}
	if !mounts[0].ReadOnly {
		t.Fatal("cwd bind mount must default to read-only")
	}

	// Explicit ro cwd entry → read-only and single mount.
	s2 := baseSpec()
	s2.WorkingDir = proj
	s2.Mounts = []spec.Mount{{Source: proj, Target: proj, ReadOnly: true}}
	mounts, err = b.resolveMounts(s2)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	if len(mounts) != 1 || !mounts[0].ReadOnly {
		t.Fatalf("ro cwd mount = %+v, want single read-only", mounts)
	}
}

func TestBuildConfig_WorkingDirDefaults(t *testing.T) {
	b := newBackend(newFakeClient())
	s := baseSpec()
	cfg := b.buildConfig(s, "img", nil)
	if cfg.WorkingDir != defaultWorkdir {
		t.Fatalf("WorkingDir = %q, want %q", cfg.WorkingDir, defaultWorkdir)
	}
	s.WorkingDir = "/work"
	cfg = b.buildConfig(s, "img", nil)
	if cfg.WorkingDir != "/work" {
		t.Fatalf("WorkingDir = %q", cfg.WorkingDir)
	}
	if cfg.Env != nil {
		t.Fatalf("Env should be nil for empty env, got %v", cfg.Env)
	}
	s.Env = map[string]string{"A": "1"}
	cfg = b.buildConfig(s, "img", nil)
	if len(cfg.Env) != 1 || cfg.Env[0] != "A=1" {
		t.Fatalf("Env = %v", cfg.Env)
	}
}

func TestWriteSecrets_InvalidName(t *testing.T) {
	b := newBackend(newFakeClient())
	for _, n := range []string{"", ".", ".."} {
		if err := b.writeSecrets(context.Background(), "cid", []spec.SecretRef{{Name: n, Value: "v"}}); err == nil {
			t.Fatalf("writeSecrets(%q) should error", n)
		}
	}
}
