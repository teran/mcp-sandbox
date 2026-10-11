package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// This file exercises SHORT / PREFIX session-id resolution (SPEC §6.6).
//
// The contract under test:
//   - Get with a full exact id still resolves (exact match wins).
//   - Get with a prefix that uniquely matches one live session resolves to it.
//   - Get with a prefix matching MULTIPLE sessions returns ErrAmbiguousSessionID.
//   - Get with a prefix matching ZERO sessions returns ErrSessionNotFound.
//   - An empty / whitespace-only prefix never panics and never accidentally
//     matches (zero matches unless a real empty id exists, which it shouldn't).
//   - Destroy mirrors the same prefix resolution, and a zero-match prefix stays
//     an idempotent no-op (X02).
//
// NOTE (TDD red step): the tests reference ErrAmbiguousSessionID which is NOT
// yet defined. @developer must add it to manager.go, e.g.:
//
//	var ErrAmbiguousSessionID = errors.New("session: ambiguous id prefix")
//
// until that symbol exists the package will not compile (that is the expected
// red state for this feature).

// makeIDs returns three deterministic, distinct lowercase-hex ids designed for
// prefix-resolution tests:
//
//	id1 = "aaaa...a" (64 'a')
//	id2 = "aabbb..b" (prefix "aa")  — shares the "aa" prefix with id1
//	id3 = "cccc...c" (64 'c')
//
// Thus: "aa" is ambiguous (id1+id2), "aaa" / "aab" / "cc" are unique, and
// "dd" matches nothing.
func makeIDs() (id1, id2, id3 string) {
	id1 = strings.Repeat("a", 64)
	id2 = "aa" + strings.Repeat("b", 62)
	id3 = strings.Repeat("c", 64)

	return id1, id2, id3
}

// newManagerWithIDs returns a manager whose sessions carry exactly the given
// ids (in order), created via a deterministic newID. The fake lifecycle is
// returned too so tests can assert on backend side-effects.
func newManagerWithIDs(t *testing.T, ids ...string) (*Manager, *fakeLife) {
	t.Helper()
	life := newFakeLife()
	m := NewManager(life)
	var i int
	m.newID = func() (string, error) {
		if i >= len(ids) {
			return "", errors.New("no more ids")
		}
		id := ids[i]
		i++

		return id, nil
	}
	for range ids {
		if _, err := m.Create(context.Background(), validSpec()); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	return m, life
}

func TestGet_ExactFullIDStillResolves(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, _ := newManagerWithIDs(t, id1, id2)

	got, err := m.Get(id1)
	if err != nil {
		t.Fatalf("Get(full id): %v", err)
	}
	if got.ID() != id1 {
		t.Fatalf("got session %q, want %q", got.ID(), id1)
	}
}

func TestGet_UniquePrefixResolves(t *testing.T) {
	id1, id2, id3 := makeIDs()
	m, _ := newManagerWithIDs(t, id1, id2, id3)

	// "aaa" matches only id1.
	got, err := m.Get("aaa")
	if err != nil {
		t.Fatalf("Get(\"aaa\"): %v", err)
	}
	if got.ID() != id1 {
		t.Fatalf("got session %q, want %q", got.ID(), id1)
	}

	// "aab" matches only id2.
	got, err = m.Get("aab")
	if err != nil {
		t.Fatalf("Get(\"aab\"): %v", err)
	}
	if got.ID() != id2 {
		t.Fatalf("got session %q, want %q", got.ID(), id2)
	}

	// "cc" matches only id3.
	got, err = m.Get("cc")
	if err != nil {
		t.Fatalf("Get(\"cc\"): %v", err)
	}
	if got.ID() != id3 {
		t.Fatalf("got session %q, want %q", got.ID(), id3)
	}
}

func TestGet_AmbiguousPrefixErrors(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, _ := newManagerWithIDs(t, id1, id2)

	// "aa" is a prefix of both id1 and id2 -> ambiguous, never silently pick one.
	if _, err := m.Get("aa"); !errors.Is(err, ErrAmbiguousSessionID) {
		t.Fatalf("err = %v, want ErrAmbiguousSessionID", err)
	}
}

func TestGet_PrefixNoMatchReturnsNotFound(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, _ := newManagerWithIDs(t, id1, id2)

	if _, err := m.Get("dd"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestGet_EmptyAndWhitespacePrefixDoNotPanicAndDoNotMatch(t *testing.T) {
	id1, _, _ := makeIDs()
	m, _ := newManagerWithIDs(t, id1)

	// Empty prefix must not panic and must not "accidentally match" (a naive
	// HasPrefix("") would match every session).
	if _, err := m.Get(""); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(\"\") err = %v, want ErrSessionNotFound", err)
	}
	// Whitespace-only prefix is not valid lowercase-hex and must not match.
	if _, err := m.Get("   "); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Get(\"   \") err = %v, want ErrSessionNotFound", err)
	}
}

func TestDestroy_UniquePrefixTearsDownRightSession(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, life := newManagerWithIDs(t, id1, id2)

	if err := m.Destroy(context.Background(), "aaa"); err != nil {
		t.Fatalf("Destroy(unique prefix): %v", err)
	}
	// The backend must be called with the RESOLVED full id.
	if len(life.destroyed) != 1 || life.destroyed[0] != id1 {
		t.Fatalf("destroyed = %v, want [%s]", life.destroyed, id1)
	}
	// id1 is gone, id2 untouched.
	if _, err := m.Get(id1); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("id1 should be gone after prefix destroy")
	}
	if _, err := m.Get(id2); err != nil {
		t.Fatalf("id2 should remain untouched: %v", err)
	}
}

func TestDestroy_AmbiguousPrefixErrors(t *testing.T) {
	id1, id2, _ := makeIDs()
	m, life := newManagerWithIDs(t, id1, id2)

	if err := m.Destroy(context.Background(), "aa"); !errors.Is(err, ErrAmbiguousSessionID) {
		t.Fatalf("err = %v, want ErrAmbiguousSessionID", err)
	}
	if len(life.destroyed) != 0 {
		t.Fatal("backend must not be called for an ambiguous destroy")
	}
}

func TestDestroy_ZeroMatchPrefixIsIdempotentNoOp(t *testing.T) {
	id1, _, _ := makeIDs()
	m, life := newManagerWithIDs(t, id1)

	if err := m.Destroy(context.Background(), "dd"); err != nil {
		t.Fatalf("Destroy(no-match) = %v, want nil (idempotent no-op, X02)", err)
	}
	if len(life.destroyed) != 0 {
		t.Fatal("backend must not be called for a zero-match destroy")
	}
	// The session must remain live.
	if _, err := m.Get(id1); err != nil {
		t.Fatalf("id1 should remain after no-op destroy: %v", err)
	}
}

// TestGet_ConcurrentFullAndPrefix exercises concurrent reads of the full id and
// a unique prefix through the RWMutex under -race (X05).
func TestGet_ConcurrentFullAndPrefix(t *testing.T) {
	id1, _, id3 := makeIDs()
	m, _ := newManagerWithIDs(t, id1, id3)

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if got, err := m.Get(id1); err != nil || got.ID() != id1 {
				t.Errorf("Get(full) = %v, %v", got, err)
			}
		}()
		go func() {
			defer wg.Done()
			if got, err := m.Get("cc"); err != nil || got.ID() != id3 {
				t.Errorf("Get(prefix) = %v, %v", got, err)
			}
		}()
	}
	wg.Wait()
}
