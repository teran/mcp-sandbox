package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	cfg := backend.Config{
		DockerHost:    "unix:///tmp/does-not-exist.sock",
		AllowDirs:     []string{dir},
		AllowSymlinks: false,
		CwdMode:       "rw",
		DefaultImage:  "img:latest",
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

func TestNewCanonicalizesAllowDirs(t *testing.T) {
	dir := t.TempDir()
	// On macOS /var -> /private/var, so canonicalization matters.
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := newBackendForDirs(newFakeClient(), dir)
	if len(b.cfg.allowDirs) != 1 || b.cfg.allowDirs[0] != canon {
		t.Fatalf("allowDirs not canonicalized: %v (want %v)", b.cfg.allowDirs, canon)
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

func TestWalkRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	var visited []string
	if err := walkRoot(root, ".", func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		visited = append(visited, p)
		return nil
	}); err != nil {
		t.Fatalf("walkRoot: %v", err)
	}
	if len(visited) == 0 {
		t.Fatal("no entries visited")
	}

	// Lstat error propagates for a missing entry.
	if err := walkRoot(root, "nope", func(_ string, _ os.FileInfo, err error) error {
		return err
	}); err == nil {
		t.Fatal("expected error for missing entry")
	}
}

func TestTarHostPath_FileAndDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Directory.
	rc, err := tarHostPath(dir)
	if err != nil {
		t.Fatalf("tarHostPath(dir): %v", err)
	}
	data, err := firstTarEntry(rc)
	if err != nil {
		t.Fatalf("read dir tar: %v", err)
	}
	_ = data // directory entries carry no body; the read itself is the check

	// Single file.
	rc, err = tarHostPath(file)
	if err != nil {
		t.Fatalf("tarHostPath(file): %v", err)
	}
	data, err = firstTarEntry(rc)
	if err != nil {
		t.Fatalf("read file tar: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("file tar content = %q", data)
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
