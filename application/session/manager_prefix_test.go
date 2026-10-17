package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// This file exercises SHORT / PREFIX session-id resolution (SPEC §6.6) under
// the namespaced-sandbox-id design: a session id is `slug:internalID` and a
// session manager owns ONE driver/slug, so resolution is per-driver.
//
// The contract under test:
//   - Get with the exact full namespaced id still resolves (exact match wins).
//   - Get with a namespaced prefix (`slug:<prefix>`) that uniquely matches one
//     live session resolves to it.
//   - Get with a namespaced prefix matching MULTIPLE sessions within the SAME
//     slug returns ErrAmbiguousSessionID.
//   - Get with a namespaced prefix matching ZERO sessions returns ErrSessionNotFound.
//   - A bare id with NO slug prefix never matches (resolution is per-driver and
//     requires the slug), so Get("aaa") is ErrSessionNotFound.
//   - An empty / whitespace-only prefix never panics and never matches.
//   - Destroy mirrors the same namespaced prefix resolution; a zero-match
//     prefix within the valid slug stays an idempotent no-op (X02).
//
// NOTE (TDD red step): this file uses the new namespaced manager contract —
// NewManager now takes a slug, and sessions are keyed `slug:<internalID>`.
// Until @developer wires the slug into NewManager/Create this package will not
// compile (the expected red state for this feature).

// makeIDs returns three deterministic, distinct lowercase-hex internal ids
// designed for prefix-resolution tests:
//
//	id1 = "aaaa...a" (64 'a')
//	id2 = "aabbb..b" (prefix "aa")  — shares the "aa" prefix with id1
//	id3 = "cccc...c" (64 'c')
//
// Thus within a single slug: "aa" is ambiguous (id1+id2), "aaa"/"aab"/"cc" are
// unique, and "dd" matches nothing.
func makeIDs() (id1, id2, id3 string) {
	id1 = strings.Repeat("a", 64)
	id2 = "aa" + strings.Repeat("b", 62)
	id3 = strings.Repeat("c", 64)

	return id1, id2, id3
}

// newManagerWithIDs returns a manager for the given slug whose sessions carry
// exactly the given internal ids (in order), assigned by the lifecycle backend
// (idSeq). Under the namespaced design each session id is `slug:<internalID>`.
// The fake lifecycle is returned too so tests can assert on backend side-effects.
func newManagerWithIDs(t *testing.T, slug string, ids ...string) (*Manager, *fakeLife) {
	t.Helper()
	life := newFakeLife()
	life.idSeq = ids
	m := NewManager(slug, life)
	for range ids {
		if _, err := m.Create(context.Background(), validSpec()); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	return m, life
}

// TestGet_ExactFullNamespacedIDStillResolves pins the exact full-id fast path
// under namespacing.
func TestGet_ExactFullNamespacedIDStillResolves(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1, id2)

	full := "docker:" + id1
	got, err := m.Get(full)
	if err != nil {
		t.Fatalf("Get(full namespaced id): %v", err)
	}
	if got.ID() != full {
		t.Fatalf("got session %q, want %q", got.ID(), full)
	}
}

func TestGet_UniqueNamespacedPrefixResolves(t *testing.T) {
	id1, id2, id3 := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1, id2, id3)

	// "docker:aaa" matches only id1.
	got, err := m.Get("docker:aaa")
	if err != nil {
		t.Fatalf("Get(\"docker:aaa\"): %v", err)
	}
	if got.ID() != "docker:"+id1 {
		t.Fatalf("got session %q, want %q", got.ID(), "docker:"+id1)
	}

	// "docker:aab" matches only id2.
	got, err = m.Get("docker:aab")
	if err != nil {
		t.Fatalf("Get(\"docker:aab\"): %v", err)
	}
	if got.ID() != "docker:"+id2 {
		t.Fatalf("got session %q, want %q", got.ID(), "docker:"+id2)
	}

	// "docker:cc" matches only id3.
	got, err = m.Get("docker:cc")
	if err != nil {
		t.Fatalf("Get(\"docker:cc\"): %v", err)
	}
	if got.ID() != "docker:"+id3 {
		t.Fatalf("got session %q, want %q", got.ID(), "docker:"+id3)
	}
}

func TestGet_AmbiguousNamespacedPrefixErrors(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1, id2)

	// "docker:aa" is a prefix of both -> ambiguous within the same slug, never
	// silently pick one.
	if _, err := m.Get("docker:aa"); !errors.Is(err, ErrAmbiguousSessionID) {
		t.Fatalf("err = %v, want ErrAmbiguousSessionID", err)
	}
}

func TestGet_NamespacedPrefixNoMatchReturnsNotFound(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1, id2)

	if _, err := m.Get("docker:dd"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

// TestGet_BareIDWithoutSlugDoesNotMatch pins that resolution is per-driver: a
// bare internal id with NO slug prefix never matches the `slug:<internal>`
// keys, so it is ErrSessionNotFound (never an accidental cross-slug match).
func TestGet_BareIDWithoutSlugDoesNotMatch(t *testing.T) {
	id1, _, _ := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1)

	if _, err := m.Get("aaa"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(\"aaa\") err = %v, want ErrSessionNotFound (bare id, no slug)", err)
	}
	if _, err := m.Get(id1); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(bare full id) err = %v, want ErrSessionNotFound (no slug)", err)
	}
}

func TestGet_EmptyAndWhitespacePrefixDoNotPanicAndDoNotMatch(t *testing.T) {
	id1, _, _ := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1)

	if _, err := m.Get(""); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(\"\") err = %v, want ErrSessionNotFound", err)
	}
	if _, err := m.Get("   "); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(\"   \") err = %v, want ErrSessionNotFound", err)
	}
}

func TestDestroy_UniqueNamespacedPrefixTearsDownRightSession(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, life := newManagerWithIDs(t, "docker", id1, id2)

	if err := m.Destroy(context.Background(), "docker:aaa"); err != nil {
		t.Fatalf("Destroy(unique prefix): %v", err)
	}
	// The backend must be called with the RESOLVED full namespaced id.
	wantFull := "docker:" + id1
	if len(life.destroyed) != 1 || life.destroyed[0] != wantFull {
		t.Fatalf("destroyed = %v, want [%s]", life.destroyed, wantFull)
	}
	// id1 is gone, id2 untouched.
	if _, err := m.Get("docker:" + id1); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("id1 should be gone after prefix destroy")
	}
	if _, err := m.Get("docker:" + id2); err != nil {
		t.Fatalf("id2 should remain untouched: %v", err)
	}
}

func TestDestroy_AmbiguousNamespacedPrefixErrors(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, life := newManagerWithIDs(t, "docker", id1, id2)

	if err := m.Destroy(context.Background(), "docker:aa"); !errors.Is(err, ErrAmbiguousSessionID) {
		t.Fatalf("err = %v, want ErrAmbiguousSessionID", err)
	}
	if len(life.destroyed) != 0 {
		t.Fatal("backend must not be called for an ambiguous destroy")
	}
}

func TestDestroy_ZeroMatchNamespacedPrefixIsIdempotentNoOp(t *testing.T) {
	id1, _, _ := makeIDs()
	m, life := newManagerWithIDs(t, "docker", id1)

	if err := m.Destroy(context.Background(), "docker:dd"); err != nil {
		t.Fatalf("Destroy(no-match) = %v, want nil (idempotent no-op, X02)", err)
	}
	if len(life.destroyed) != 0 {
		t.Fatal("backend must not be called for a zero-match destroy")
	}
	// The session must remain live.
	if _, err := m.Get("docker:" + id1); err != nil {
		t.Fatalf("id1 should remain after no-op destroy: %v", err)
	}
}

// TestGet_ConcurrentFullAndPrefix exercises concurrent reads of the full id and
// a unique prefix through the RWMutex under -race (X05).
func TestGet_ConcurrentFullAndPrefix(t *testing.T) {
	id1, _, id3 := makeIDs()
	m, _ := newManagerWithIDs(t, "docker", id1, id3)

	full1 := "docker:" + id1
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if got, err := m.Get(full1); err != nil || got.ID() != full1 {
				t.Errorf("Get(full) = %v, %v", got, err)
			}
		}()
		go func() {
			defer wg.Done()
			if got, err := m.Get("docker:cc"); err != nil || got.ID() != "docker:"+id3 {
				t.Errorf("Get(prefix) = %v, %v", got, err)
			}
		}()
	}
	wg.Wait()
}
