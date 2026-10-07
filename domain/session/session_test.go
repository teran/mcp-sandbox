package session

import (
	"context"
	"testing"
	"time"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

func validSpec() spec.SandboxSpec {
	return spec.SandboxSpec{
		Image: "img",
		Isolation: spec.Isolation{
			ReadOnlyRootfs:  true,
			CapDrop:         []string{"ALL"},
			NoNewPrivileges: true,
			Seccomp:         spec.SeccompDefault,
			User:            spec.UIDGID{UID: 1000, GID: 1000},
			PidsLimit:       100,
			Memory:          128 * 1024 * 1024,
			MemorySwap:      128 * 1024 * 1024,
			NanoCPUs:        1_000_000_000,
			NetworkMode:     spec.NetworkNone,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := New("s1", validSpec())
	if s.ID() != "s1" {
		t.Fatalf("ID = %q", s.ID())
	}
	if s.Spec().Image != "img" {
		t.Fatal("spec mismatch")
	}
	if s.State() != StateCreating {
		t.Fatalf("state = %q", StateCreating)
	}
	if s.Created().IsZero() {
		t.Fatal("created should be set")
	}
	if s.LastActive().IsZero() {
		t.Fatal("lastActive should be set")
	}

	s.MarkRunning()
	if s.State() != StateRunning {
		t.Fatal("state should be running")
	}

	old := s.LastActive()
	s.Touch(time.Now())
	if !s.LastActive().After(old) {
		t.Fatal("Touch should advance lastActive")
	}
}

func TestSessionExpired(t *testing.T) {
	s := New("s2", validSpec())
	now := time.Now()
	if s.Expired(time.Hour, now) {
		t.Fatal("fresh session should not be expired")
	}
	s.Touch(now.Add(-2 * time.Hour))
	if !s.Expired(time.Hour, now) {
		t.Fatal("aged session should be expired")
	}
	s.MarkDestroyed()
	if s.Expired(time.Minute, now) {
		t.Fatal("destroyed session must never be reaped")
	}
	if s.State() != StateDestroyed {
		t.Fatal("state should be destroyed")
	}
}

func TestSessionMutex(_ *testing.T) {
	s := New("s3", validSpec())
	s.Lock()
	_ = s.State() // do work inside the critical section
	s.Unlock()
}

func TestSessionContextID(t *testing.T) {
	ctx := WithSessionID(context.Background(), "sid")
	id, ok := IDFromContext(ctx)
	if !ok || id != "sid" {
		t.Fatalf("IDFromContext = %q, %v", id, ok)
	}
	if _, ok := IDFromContext(context.Background()); ok {
		t.Fatal("plain context should have no session id")
	}
}
