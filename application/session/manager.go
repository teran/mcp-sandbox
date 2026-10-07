// Package session implements the in-memory session manager (SPEC §6.6). It
// owns the registry of live sessions, drives their lifecycle through a
// role.Lifecycle backend, provides idempotent destroy (X02), an idle reaper
// and per-session serialization (X05). State is in-memory only (X01).
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// ErrSessionNotFound is returned by Get when no session has the given id.
var ErrSessionNotFound = errors.New("session: not found")

// IDSizeBytes is the length of the opaque session id in bytes (§7.8). A 32-byte
// id yields 64 hex characters.
const IDSizeBytes = 32

// Manager is the concrete session manager. It is safe for concurrent use; the
// registry map is guarded by a RWMutex and each session carries its own
// per-session mutex (X05).
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*domainsession.Session
	life     role.Lifecycle
	// newID generates the opaque session id. It is a field so tests can force
	// deterministic ids or failures.
	newID func() (string, error)
}

// NewManager returns a session manager backed by the given lifecycle backend.
func NewManager(life role.Lifecycle) *Manager {
	return &Manager{
		sessions: make(map[string]*domainsession.Session),
		life:     life,
		newID:    newID,
	}
}

// newID returns an opaque, unpredictable session id from crypto/rand (§7.8).
func newID() (string, error) {
	return newIDFrom(rand.Reader)
}

// newIDFrom generates the id from the given reader. It is separated so the
// error path is directly testable.
func newIDFrom(r io.Reader) (string, error) {
	buf := make([]byte, IDSizeBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("session: generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Create validates the spec, builds the sandbox through the backend and
// registers the resulting session. It surfaces duplicate ids as an error
// (search-before-create, X02).
func (m *Manager) Create(ctx context.Context, s spec.SandboxSpec) (*domainsession.Session, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	id, err := m.newID()
	if err != nil {
		return nil, err
	}

	// search-before-create (X02): a duplicate id is surfaced as an error
	// before any backend side-effect happens.
	m.mu.RLock()
	_, exists := m.sessions[id]
	m.mu.RUnlock()
	if exists {
		return nil, fmt.Errorf("session: duplicate id %q", id)
	}

	// The backend labels the sandbox with the session id (label-based
	// ownership, §7.8). We key the session by the generated id so all
	// subsequent role calls reference the same sandbox.
	if _, err := m.life.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		return nil, fmt.Errorf("session: create sandbox: %w", err)
	}

	sess := domainsession.New(id, s)
	sess.MarkRunning()

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	return sess, nil
}

// Get returns the session with the given id.
func (m *Manager) Get(id string) (*domainsession.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

// List returns every live session.
func (m *Manager) List() []*domainsession.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*domainsession.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// Destroy tears down one session and removes it from the registry. Destroying
// an unknown id is a no-op (idempotent, X02). The backend destroy is also
// idempotent (X02), so a double destroy does not error.
func (m *Manager) Destroy(ctx context.Context, id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	// Serialize the destroy with any in-flight per-session operation (X05).
	s.Lock()
	defer s.Unlock()

	if err := m.life.Destroy(ctx, id); err != nil {
		return fmt.Errorf("session: destroy %q: %w", id, err)
	}
	s.MarkDestroyed()

	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

// DestroyAll tears down every live session (shutdown-teardown guarantee).
func (m *Manager) DestroyAll(ctx context.Context) error {
	ids := make([]string, 0)
	m.mu.RLock()
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, id := range ids {
		if err := m.Destroy(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Reap destroys every live session that has been idle longer than the given
// timeout (idle reaper).
func (m *Manager) Reap(ctx context.Context, timeout time.Duration, now time.Time) error {
	sessions := m.List()
	for _, s := range sessions {
		if !s.Expired(timeout, now) {
			continue
		}
		if err := m.Destroy(ctx, s.ID()); err != nil {
			return err
		}
	}
	return nil
}
