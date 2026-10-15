//go:build !darwin

package darwin

// Tests for the non-darwin runner stand-in (runner_other.go). This file is
// build-tagged !darwin because unavailableRunner only exists there; on darwin
// the real os/exec runner is compiled instead. These cover the runner boundary
// on Linux so the package meets the coverage gate.

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
)

func TestNewRunner_Other(t *testing.T) {
	r := newRunner()
	if r == nil {
		t.Fatal("newRunner() = nil")
	}
}

func TestUnavailableRunner_Start(t *testing.T) {
	r := unavailableRunner{}
	_, err := r.Start(context.Background(), ProcessSpec{Argv: []string{"/usr/bin/sandbox-exec"}})
	if !errors.Is(err, role.ErrCapabilityNotSupported) {
		t.Fatalf("Start err = %v, want ErrCapabilityNotSupported", err)
	}
}
