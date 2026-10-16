package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
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
	id, err := b.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"x"}}); err == nil {
		t.Fatal("expected exec read error")
	}
}
