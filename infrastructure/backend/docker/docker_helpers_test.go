package docker

import (
	"context"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

func TestBuildHostConfig_NetworkDefaultsToNone(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	// host networking is forcibly downgraded to none (policy, §7.1).
	s := baseSpec()
	s.Isolation.NetworkMode = spec.NetworkHost
	hc := b.buildHostConfig(s, nil, nil)
	if string(hc.NetworkMode) != "none" {
		t.Fatalf("NetworkMode = %q, want none (host forbidden)", hc.NetworkMode)
	}

	s2 := baseSpec()
	s2.Isolation.NetworkMode = ""
	hc2 := b.buildHostConfig(s2, nil, nil)
	if string(hc2.NetworkMode) != "none" {
		t.Fatalf("empty NetworkMode = %q, want none", hc2.NetworkMode)
	}

	// Ulimits passthrough.
	s3 := baseSpec()
	s3.Isolation.Ulimits = []spec.Ulimit{{Name: "nofile", Soft: 100, Hard: 200}}
	hc3 := b.buildHostConfig(s3, nil, nil)
	if len(hc3.Ulimits) != 1 || hc3.Ulimits[0].Name != "nofile" {
		t.Fatalf("ulimits = %+v", hc3.Ulimits)
	}
}

func TestResolveMounts(t *testing.T) {
	dir := t.TempDir()
	b := New([]string{dir}, false, "rw", "img", newFakeClient())

	// Cwd mounted 1:1, writable by default.
	s := baseSpec()
	s.WorkingDir = dir
	mounts, err := b.resolveMounts(s)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].Source != dir || mounts[0].Target != dir || mounts[0].ReadOnly {
		t.Fatalf("cwd mount = %+v", mounts[0])
	}

	// Read-only cwd when mode=readonly.
	rb := New([]string{dir}, false, "readonly", "img", newFakeClient())
	mounts, err = rb.resolveMounts(s)
	if err != nil {
		t.Fatal(err)
	}
	if !mounts[0].ReadOnly {
		t.Fatal("cwd should be read-only in readonly mode")
	}

	// No cwd, one additional mount.
	s2 := baseSpec()
	s2.Mounts = []spec.Mount{{Source: dir, Target: "/mnt", ReadOnly: true}}
	mounts, err = b.resolveMounts(s2)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != "/mnt" || !mounts[0].ReadOnly {
		t.Fatalf("additional mount = %+v", mounts)
	}

	// A mount escaping ALLOW_DIRS errors.
	outside := t.TempDir()
	s3 := baseSpec()
	s3.Mounts = []spec.Mount{{Source: outside, Target: "/mnt"}}
	if _, err := b.resolveMounts(s3); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestBuildConfig_WorkingDirDefaults(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	s := baseSpec()
	cfg := b.buildConfig(s, "img", nil)
	if cfg.WorkingDir != tmpfsWorkdir {
		t.Fatalf("WorkingDir = %q, want %q", cfg.WorkingDir, tmpfsWorkdir)
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

func TestFirstTarEntry_Error(t *testing.T) {
	// A reader that errors mid-stream.
	rc, err := singleFileTar("x", []byte("y"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstTarEntry(rc); err != nil {
		t.Fatalf("firstTarEntry on valid tar: %v", err)
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
