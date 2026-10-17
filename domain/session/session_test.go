package session

import (
	"context"
	"strings"
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
			NetworkMode:     spec.NetworkBridge,
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

// TestShortID verifies the docker-style short display id derivation: the first
// 12 hex chars of the full id, never panicking on empty/short input and always
// returning at most 12 characters.
func TestShortID(t *testing.T) {
	full := strings.Repeat("a", 64)
	if got := ShortID(full); got != strings.Repeat("a", 12) {
		t.Fatalf("ShortID(64-hex) = %q, want 12 'a'", got)
	}
	if got := ShortID(""); got != "" {
		t.Fatalf("ShortID(\"\") = %q, want empty", got)
	}
	if got := ShortID("abc"); got != "abc" {
		t.Fatalf("ShortID(short) = %q, want unchanged", got)
	}
	// Exactly 13 chars truncates to the first 12.
	if got := ShortID("1234567890abc"); got != "1234567890ab" {
		t.Fatalf("ShortID(13) = %q, want first 12", got)
	}
	if len(ShortID(full)) > ShortIDLength {
		t.Fatal("ShortID result must be at most 12 chars")
	}
	// Must never panic across a range of inputs.
	for _, s := range []string{"", "x", "abcdef", full} {
		_ = ShortID(s)
	}
}

// TestShortID_Namespaced pins the namespaced-aware short display id (user
// decision 2): the slug is PRESERVED and only the internal id is shortened to
// 12 chars. A namespaced id whose internal part is already <= 12 chars is
// unchanged. A legacy bare id (no slug) keeps the old bare truncation.
func TestShortID_Namespaced(t *testing.T) {
	// docker:<64hex> -> docker:<12> (slug kept, internal shortened).
	hex64 := strings.Repeat("b", 64)
	if got := ShortID("docker:" + hex64); got != "docker:"+strings.Repeat("b", 12) {
		t.Fatalf("ShortID(docker:<64hex>) = %q, want docker:<12>", got)
	}
	// seatbelt:darwin-3 internal len 8 (<= 12) -> unchanged, slug kept.
	if got := ShortID("seatbelt:darwin-3"); got != "seatbelt:darwin-3" {
		t.Fatalf("ShortID(seatbelt:darwin-3) = %q, want unchanged", got)
	}
	// Legacy bare id fallback is unchanged (still the first 12 chars).
	if got := ShortID(hex64); got != strings.Repeat("b", 12) {
		t.Fatalf("ShortID(bare 64hex) = %q, want 12 'b'", got)
	}
	// Must never panic across namespaced inputs.
	for _, s := range []string{"docker:", "docker:a:b", ":x", "docker:" + hex64} {
		_ = ShortID(s)
	}
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
