package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// TestCreate_ContainerCreateError covers the container-create error branch of
// Create: the fake's Create returns an error and the backend must propagate it.
func TestCreate_ContainerCreateError(t *testing.T) {
	api := newFakeClient()
	api.createErr = errors.New("create fail")
	b := newBackendForDirs(api)
	if _, err := b.Create(context.Background(), baseSpec()); err == nil {
		t.Fatal("expected container create error")
	}
}

// TestExec_StdCopyReadError covers the stdcopy read-error branch of Exec: a
// broken attach stream must surface as an error, not a silent success.
func TestExec_StdCopyReadError(t *testing.T) {
	api := newFakeClient()
	api.execReaderErr = errors.New("stream broken")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-sc")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), "sess-sc", exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected exec read error")
	}
}

// TestReadFile_TarError covers the firstTarEntry error branch of ReadFile: a
// successful CopyFromContainer that yields a malformed tar must error.
func TestReadFile_TarError(t *testing.T) {
	api := newFakeClient()
	// CopyFromContainer succeeds but returns non-tar bytes.
	api.copyOutTar = []byte("this-is-not-a-tar-stream")
	b := newBackendForDirs(api)
	ctx := domainsession.WithSessionID(context.Background(), "sess-rt")
	if _, err := b.Create(ctx, baseSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.ReadFile(context.Background(), "sess-rt", "/workspace/a"); err == nil {
		t.Fatal("expected read file tar error")
	}
}

// TestTarHostPath_OpenRootError covers the os.OpenRoot error branch of
// tarHostPath: a source whose parent is a regular file (not a directory).
func TestTarHostPath_OpenRootError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// filepath.Dir(src) == file, which is not a directory.
	if _, err := tarHostPath(filepath.Join(file, "child")); err == nil {
		t.Fatal("expected open root error")
	}
}

// TestExtractTarTo_CopyError covers the io.CopyN error branch of extractTarTo:
// a tar header declaring more bytes than the stream actually carries makes the
// tar reader return a non-EOF error mid-copy.
func TestExtractTarTo_CopyError(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "big", Mode: 0o600, Size: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not write the declared body and do not Close the writer,
	// so the stream carries the header but a truncated body.
	dst := filepath.Join(dir, "out")
	if err := extractTarTo(bytes.NewReader(buf.Bytes()), dst); err == nil {
		t.Fatal("expected copy error on truncated tar body")
	}
}

// TestExtractTarTo_OpenFileError covers the OpenFile error branch of
// extractTarTo: writing to an existing directory fails with EISDIR.
func TestExtractTarTo_OpenFileError(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "adir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	rc, err := singleFileTar("adir", []byte("x"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractTarTo(rc, sub); err == nil {
		t.Fatal("expected open file error (EISDIR)")
	}
}

// TestTarHostPath_FileOpenError covers the file open error branch of
// tarHostPath: an unreadable source file makes the tar copy fail. Skipped when
// running as root, where the permission bit is not enforced.
func TestTarHostPath_FileOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based; running as root")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(file, 0o600) }()
	if _, err := tarHostPath(file); err == nil {
		t.Fatal("expected file open error")
	}
}

// TestTarHostPath_DirOpenError covers the walkRoot directory-open error branch
// of tarHostPath: a subdirectory the user cannot open stops the walk. Skipped
// when running as root, where the permission bit is not enforced.
func TestTarHostPath_DirOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based; running as root")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o700) }()
	if _, err := tarHostPath(dir); err == nil {
		t.Fatal("expected dir open error")
	}
}
