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

	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/domain/requestid"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// ErrSessionNotFound is returned by Get when no session has the given id.
var ErrSessionNotFound = errors.New("session: not found")

// IDSizeBytes is the length of the opaque session id in bytes (§7.8). A 32-byte
// id yields 64 hex characters.
const IDSizeBytes = 32

// defaultLog is the manager's base logrus entry for trace diagnostics. The
// session_id and request_id from the context are attached per call (L09).
var defaultLog = logrus.WithField("component", "session-manager")

// Option configures a session manager at construction.
type Option func(*Manager)

// WithLogger sets the base logrus entry used for trace diagnostics. It
// defaults to the package-level session-manager logger. It is primarily for
// tests; production wires no logger and derives request/session id from the
// context.
func WithLogger(e *logrus.Entry) Option { return func(m *Manager) { m.log = e } }

// Manager is the concrete session manager. It is safe for concurrent use; the
// registry map is guarded by a RWMutex and each session carries its own
// per-session mutex (X05).
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*domainsession.Session
	life     role.Lifecycle
	// log is the base logrus entry for trace diagnostics (L09).
	log *logrus.Entry
	// newID generates the opaque session id. It is a field so tests can force
	// deterministic ids or failures.
	newID func() (string, error)
}

// NewManager returns a session manager backed by the given lifecycle backend.
func NewManager(life role.Lifecycle, opts ...Option) *Manager {
	m := &Manager{
		sessions: make(map[string]*domainsession.Session),
		life:     life,
		newID:    newID,
		log:      defaultLog,
	}
	for _, o := range opts {
		if o != nil {
			o(m)
		}
	}
	return m
}

// entryFor returns a logrus entry derived from the manager's base logger with
// the session_id and request_id from ctx attached when available (L09).
func (m *Manager) entryFor(ctx context.Context, sessionID string) *logrus.Entry {
	e := m.log
	if sid, ok := domainsession.IDFromContext(ctx); ok && sid != "" {
		e = e.WithField("session_id", sid)
	} else if sessionID != "" {
		e = e.WithField("session_id", sessionID)
	}
	if id, ok := requestid.IDFromContext(ctx); ok {
		e = e.WithField("request_id", id)
	}
	return e
}

// traceOp logs the start of an operation and returns a func that logs its
// completion with duration. It is trace-level, so it only appears at
// LOG_LEVEL=trace.
func (m *Manager) traceOp(ctx context.Context, op, sessionID string) func() {
	e := m.entryFor(ctx, sessionID).WithField("operation", op)
	e.Trace("session: operation start")
	start := time.Now()
	return func() {
		e.WithField("duration_ms", time.Since(start).Milliseconds()).Trace("session: operation done")
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
	defer m.traceOp(ctx, "create", "")()
	if err := s.Validate(); err != nil {
		return nil, err
	}

	id, err := m.newID()
	if err != nil {
		return nil, err
	}
	m.entryFor(ctx, id).WithField("operation", "create").Trace("session: created")

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
	m.entryFor(context.Background(), id).WithField("operation", "get").Trace("session: got")
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
	m.entryFor(context.Background(), "").WithField("operation", "list").WithField("count", len(out)).Trace("session: listed")
	return out
}

// Destroy tears down one session and removes it from the registry. Destroying
// an unknown id is a no-op (idempotent, X02). The backend destroy is also
// idempotent (X02), so a double destroy does not error.
func (m *Manager) Destroy(ctx context.Context, id string) error {
	defer m.traceOp(ctx, "destroy", id)()
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
	defer m.traceOp(ctx, "destroy_all", "")()
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
	defer m.traceOp(ctx, "reap", "")()
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
