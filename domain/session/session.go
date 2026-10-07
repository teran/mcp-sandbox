// Package session defines the sandbox session aggregate and the session
// manager interface (SPEC §6.6, §7.8). A session owns one sandbox and its
// state; it is stateful and in-memory (X01), identified by an opaque
// crypto/rand id and protected by a per-session mutex (X05).
package session

import (
	"context"
	"time"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

type sessionIDKey struct{}

// WithSessionID returns a context carrying the given session id. The session
// manager passes it to the backend during Create so the backend can label the
// sandbox with the session id (label-based ownership, §7.8).
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

// IDFromContext returns the session id stored in the context, if any.
func IDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(sessionIDKey{}).(string)
	return id, ok
}

// State describes the lifecycle state of a session.
type State string

const (
	// StateCreating is set while a session is being created.
	StateCreating State = "creating"
	// StateRunning is set once the underlying sandbox exists.
	StateRunning State = "running"
	// StateDestroyed is set after the sandbox has been destroyed. Destroyed
	// sessions are removed from the manager and never resurrected.
	StateDestroyed State = "destroyed"
)

// Session is the per-sandbox aggregate. It carries an opaque id, the immutable
// spec it was created from, lifecycle state and last-activity timestamps used
// by the idle reaper. The per-session mutex serializes reads and writes so no
// partial or corrupt state results under concurrency (X05).
type Session struct {
	id         string
	spec       spec.SandboxSpec
	state      State
	created    time.Time
	lastActive time.Time

	// mu is the per-session mutex (X05).
	mu chan struct{}
}

// New creates a session aggregate in the creating state.
func New(id string, s spec.SandboxSpec) *Session {
	return &Session{
		id:         id,
		spec:       s,
		state:      StateCreating,
		created:    time.Now(),
		lastActive: time.Now(),
		mu:         make(chan struct{}, 1),
	}
}

// ID returns the opaque session id.
func (s *Session) ID() string { return s.id }

// Spec returns the immutable spec the session was created from.
func (s *Session) Spec() spec.SandboxSpec { return s.spec }

// State returns the current lifecycle state.
func (s *Session) State() State { return s.state }

// Created returns the session creation time.
func (s *Session) Created() time.Time { return s.created }

// LastActive returns the last recorded activity time.
func (s *Session) LastActive() time.Time { return s.lastActive }

// MarkRunning transitions the session to the running state and records the
// current time as the last activity.
func (s *Session) MarkRunning() {
	s.state = StateRunning
	s.lastActive = time.Now()
}

// MarkDestroyed transitions the session to the destroyed state.
func (s *Session) MarkDestroyed() {
	s.state = StateDestroyed
	s.lastActive = time.Now()
}

// Touch records activity at the given time (used by the reaper and by tool
// calls to extend the idle window).
func (s *Session) Touch(at time.Time) { s.lastActive = at }

// Expired reports whether the session has been idle longer than the given
// timeout.
func (s *Session) Expired(timeout time.Duration, now time.Time) bool {
	return s.state != StateDestroyed && now.Sub(s.lastActive) > timeout
}

// Lock acquires the per-session mutex.
func (s *Session) Lock() { s.mu <- struct{}{} }

// Unlock releases the per-session mutex.
func (s *Session) Unlock() { <-s.mu }

// Manager is the session manager interface implemented by the application
// layer (SPEC §6.6). It owns the registry of live sessions and their lifecycle.
type Manager interface {
	// Create builds a sandbox from the spec and registers the resulting
	// session. A duplicate id is surfaced as an error (search-before-create,
	// X02).
	Create(ctx context.Context, s spec.SandboxSpec) (*Session, error)
	// Get returns the session with the given id, or an error if it is unknown.
	Get(id string) (*Session, error)
	// List returns all known live sessions.
	List() []*Session
	// Destroy tears down one session. Destroying an unknown id is a no-op
	// (idempotent, X02).
	Destroy(ctx context.Context, id string) error
	// DestroyAll tears down every live session.
	DestroyAll(ctx context.Context) error
	// Reap destroys every session idle longer than timeout.
	Reap(ctx context.Context, timeout time.Duration, now time.Time) error
}
