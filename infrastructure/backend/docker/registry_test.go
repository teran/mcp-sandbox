package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
)

func TestPingOK(t *testing.T) {
	if !pingOK(context.Background(), func(context.Context) (types.Ping, error) {
		return types.Ping{}, nil
	}) {
		t.Fatal("pingOK should be true on success")
	}
	if pingOK(context.Background(), func(context.Context) (types.Ping, error) {
		return types.Ping{}, errors.New("down")
	}) {
		t.Fatal("pingOK should be false on error")
	}
}

func TestDockerAvailable_ReturnsBool(_ *testing.T) {
	// Without a live daemon this returns false without panicking; the true
	// branch is covered by TestPingOK.
	_ = dockerAvailable()
}

func TestNewClient(t *testing.T) {
	cli, err := newClient("")
	if err != nil {
		t.Fatalf("newClient(default): %v", err)
	}
	if cli == nil {
		t.Fatal("nil client")
	}
	if _, err := newClient("http://127.0.0.1:1"); err != nil {
		// Host parse/creation should succeed (no network); a malformed host
		// should error.
		t.Logf("newClient(remote) errored (acceptable): %v", err)
	}
}

func TestNewFromConfig(t *testing.T) {
	cfg := backend.Config{
		DockerHost: "unix:///tmp/does-not-exist.sock",
	}
	b, err := newFromConfig(cfg)
	if err != nil {
		t.Fatalf("newFromConfig: %v", err)
	}
	if b == nil {
		t.Fatal("nil backend")
	}
	if b.Name() != "docker" {
		t.Fatalf("Name = %q", b.Name())
	}
}

func TestToEnvSlice(t *testing.T) {
	if toEnvSlice(nil) != nil {
		t.Fatal("nil env should yield nil slice")
	}
	out := toEnvSlice(map[string]string{"B": "2", "A": "1"})
	if len(out) != 2 || out[0] != "A=1" || out[1] != "B=2" {
		t.Fatalf("env slice = %v", out)
	}
}

func TestToDockerUlimits(t *testing.T) {
	if toDockerUlimits(nil) != nil {
		t.Fatal("nil ulimits should yield nil")
	}
	out := toDockerUlimits([]spec.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}})
	if len(out) != 1 || out[0].Name != "nofile" || out[0].Soft != 1024 || out[0].Hard != 2048 {
		t.Fatalf("ulimits = %+v", out)
	}
}

func TestClampUint64(t *testing.T) {
	if clampUint64(0) != 0 {
		t.Fatal("clamp(0) != 0")
	}
	if clampUint64(1024) != 1024 {
		t.Fatal("clamp(1024) != 1024")
	}
	big := ^uint64(0)
	if clampUint64(big) != int64(^uint64(0)>>1) {
		t.Fatalf("clamp(MaxUint64) = %d, want MaxInt64", clampUint64(big))
	}
}

func TestSingleFileTar_InvalidNames(t *testing.T) {
	// writeSecrets rejects dangerous names before building tar.
	b := newBackendForDirs(newFakeClient())
	for _, name := range []string{"", ".", ".."} {
		if err := b.writeSecrets(context.Background(), "cid", []spec.SecretRef{{Name: name, Value: "x"}}); err == nil {
			t.Fatalf("writeSecrets(%q) should error", name)
		}
	}
}
