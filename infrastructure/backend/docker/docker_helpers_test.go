package docker

import (
	"context"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

func TestBuildHostConfig_NetworkMode(t *testing.T) {
	b := newBackendForDirs(newFakeClient())

	// bridge (the default) must pass through to container.NetworkMode.
	s := baseSpec()
	s.Isolation.NetworkMode = spec.NetworkBridge
	hc := b.buildHostConfig(s, nil, nil)
	if string(hc.NetworkMode) != "bridge" {
		t.Fatalf("bridge NetworkMode = %q, want bridge (default)", hc.NetworkMode)
	}

	// host networking is forcibly downgraded to none when it is not allowed
	// (defense-in-depth, §7.1).
	s2 := baseSpec()
	s2.Isolation.NetworkMode = spec.NetworkHost
	hc2 := b.buildHostConfig(s2, nil, nil)
	if string(hc2.NetworkMode) != "none" {
		t.Fatalf("NetworkMode = %q, want none (host not allowed)", hc2.NetworkMode)
	}

	// host passes through only when the operator has explicitly allowed it.
	bh := New(nil, false, "ro", "img", newFakeClient(), []spec.NetworkMode{spec.NetworkBridge, spec.NetworkHost})
	s5 := baseSpec()
	s5.Isolation.NetworkMode = spec.NetworkHost
	hc5 := bh.buildHostConfig(s5, nil, nil)
	if string(hc5.NetworkMode) != "host" {
		t.Fatalf("NetworkMode = %q, want host (allowed by config)", hc5.NetworkMode)
	}

	// an empty mode is coerced to none.
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

func TestResolveMounts(t *testing.T) {
	dir := t.TempDir()
	b := New([]string{dir}, false, "ro", "img", newFakeClient())

	// Cwd mounted 1:1, read-only by default.
	s := baseSpec()
	s.WorkingDir = dir
	mounts, err := b.resolveMounts(s)
	if err != nil {
		t.Fatalf("resolveMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0].Source != dir || mounts[0].Target != dir || !mounts[0].ReadOnly {
		t.Fatalf("cwd mount = %+v", mounts[0])
	}

	// Writable cwd when mode=rw (opt-in).
	wb := New([]string{dir}, false, "rw", "img", newFakeClient())
	mounts, err = wb.resolveMounts(s)
	if err != nil {
		t.Fatal(err)
	}
	if mounts[0].ReadOnly {
		t.Fatal("cwd should be writable in rw mode")
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

func TestWriteSecrets_InvalidName(t *testing.T) {
	b := newBackendForDirs(newFakeClient())
	for _, n := range []string{"", ".", ".."} {
		if err := b.writeSecrets(context.Background(), "cid", []spec.SecretRef{{Name: n, Value: "v"}}); err == nil {
			t.Fatalf("writeSecrets(%q) should error", n)
		}
	}
}
