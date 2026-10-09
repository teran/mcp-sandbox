package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// TestBuildHostConfig_NetworkMode pins Phase 7: the network-allowlist coercion
// is removed, so buildHostConfig maps the spec's network mode straight through
// (host included). It still applies the immutable Isolation hardening and
// bounded-resource fields.
func TestBuildHostConfig_NetworkMode(t *testing.T) {
	b := newBackendForDirs(newFakeClient())

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
	b := newBackendForDirs(newFakeClient())
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

// TestResolveMounts pins Phase 7: resolveMounts RETAINS the ALLOW_DIRS gate for
// both the cwd mount and every mount source (S04). Sources within an allowed
// root pass through 1:1 with their ReadOnly flag honoured; sources outside any
// allowed root are rejected.
func TestResolveMounts(t *testing.T) {
	allowDir := t.TempDir()
	proj := filepath.Join(allowDir, "proj")
	w := filepath.Join(allowDir, "w")
	for _, d := range []string{proj, w} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := newBackendForDirs(newFakeClient(), allowDir)

	// A read-only cwd entry within ALLOW_DIRS passes through 1:1.
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

	// A cwd outside ALLOW_DIRS is rejected.
	s3 := baseSpec()
	s3.WorkingDir = t.TempDir() // exists, but not under allowDir
	if _, err := b.resolveMounts(s3); err == nil {
		t.Fatal("cwd outside ALLOW_DIRS must be rejected")
	}

	// A mount source outside ALLOW_DIRS is rejected.
	s4 := baseSpec()
	s4.Mounts = []spec.Mount{{Source: t.TempDir(), Target: "/mnt", ReadOnly: true}}
	if _, err := b.resolveMounts(s4); err == nil {
		t.Fatal("mount source outside ALLOW_DIRS must be rejected")
	}
}

func TestBuildConfig_WorkingDirDefaults(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
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
	b := newBackendForDirs(newFakeClient())
	for _, n := range []string{"", ".", ".."} {
		if err := b.writeSecrets(context.Background(), "cid", []spec.SecretRef{{Name: n, Value: "v"}}); err == nil {
			t.Fatalf("writeSecrets(%q) should error", n)
		}
	}
}
