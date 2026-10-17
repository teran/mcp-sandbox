package docker

// TOCTOU-integrity tests for the docker backend's host-path verification
// (Slice 4, SPEC §6.11 G / §7.2 / S04). The ALLOW_DIRS *containment* decision
// moved up to EnforceHardening at the Service level, but the backend RETAINS
// its EvalSymlinks + open-then-verify TOCTOU integrity: a host path must still
// resolve, and its post-open identity must match what EvalSymlinks reported.
// These tests pin that retained behaviour so a future refactor cannot strip it.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyHostPath_NonExistentRejected pins that EvalSymlinks still runs: a
// source that does not exist cannot resolve and is rejected before any mount is
// produced.
func TestVerifyHostPath_NonExistentRejected(t *testing.T) {
	b := newBackend(newFakeClient())
	if err := b.verifyHostPath(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("non-existent host path must be rejected (EvalSymlinks)")
	}
}

// TestVerifyHostPath_EmptyRejected pins the empty-path guard is retained.
func TestVerifyHostPath_EmptyRejected(t *testing.T) {
	b := newBackend(newFakeClient())
	if err := b.verifyHostPath("   "); err == nil {
		t.Fatal("empty/blank host path must be rejected")
	}
}

// TestVerifyHostPath_ExistingDirAccepted pins that an existing host directory
// passes verification even though the backend no longer enforces an ALLOW_DIRS
// root set (containment is at the Service level now).
func TestVerifyHostPath_ExistingDirAccepted(t *testing.T) {
	b := newBackend(newFakeClient())
	dir := t.TempDir()
	if err := b.verifyHostPath(dir); err != nil {
		t.Fatalf("existing dir must pass host-path verification: %v", err)
	}
}

// TestVerifyHostPath_SymlinkOpenThenVerify pins the open-then-verify step: a
// symlink resolving to an existing target passes the identity check (the opened
// file re-resolves to the same real path EvalSymlinks reported).
func TestVerifyHostPath_SymlinkOpenThenVerify(t *testing.T) {
	b := newBackend(newFakeClient())

	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	if err := b.verifyHostPath(link); err != nil {
		t.Fatalf("symlink resolving to an existing target must pass open-then-verify: %v", err)
	}
}
