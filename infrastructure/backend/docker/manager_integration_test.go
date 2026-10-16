package docker

// Integrated manager → backend contract tests (SPEC §6.6 / §7.8). These wire a
// REAL docker backend (over the in-memory fake client) into a REAL session
// manager and drive the exact flow the MCP service performs:
//
//	Manager.Create -> backend.Create (returns the container id)
//	Manager.Destroy / backend.Exec / backend.Info / backend.Stats (session id)
//
// Because the session id IS the container id, every follow-up operation must use
// the returned id DIRECTLY as the container id — the obsolete
// dev.teran.sandbox.session label lookup must NOT be relied upon. These tests
// are the regression guard for the "session id = backend id" refactor: before
// the fix, the manager passes no session id into backend.Create, the container
// carries no session label, and Destroy/Exec/Info/Stats resolve nothing and
// return ErrNotFound.

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/application/session"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
)

// TestManagerBackend_IntegratedDestroy pins that Destroy through the manager
// actually removes the container owned by the created session. Before the fix
// the label-based lookup matched nothing and Destroy was a silent no-op.
func TestManagerBackend_IntegratedDestroy(t *testing.T) {
	api := newFakeClient()
	be := New(api, nil)
	m := session.NewManager(be)

	sess, err := m.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := sess.ID()
	if id == "" {
		t.Fatal("empty session id")
	}

	if err := m.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy via manager must succeed for a created session: %v", err)
	}

	// The session id IS the container id: the backing container must actually
	// have been removed (regression: the label lookup used to make this a no-op).
	if len(api.removes) != 1 || api.removes[0] != id {
		t.Fatalf("removes = %v, want exactly [%q] (container id == session id)", api.removes, id)
	}

	if _, err := m.Get(id); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("session should be gone after destroy, got err = %v", err)
	}
}

// TestManagerBackend_IntegratedExec pins that Exec with the manager-returned
// session id does not error (regression: used to return ErrNotFound) and reaches
// the container identified by that id.
func TestManagerBackend_IntegratedExec(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hi", exitCode: 0}}
	be := New(api, nil)
	m := session.NewManager(be)

	sess, err := m.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := sess.ID()

	res, err := be.Exec(context.Background(), id, exec.Request{Argv: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("Exec with manager-returned session id must not error (regression): %v", err)
	}
	if string(res.Stdout) != "hi" {
		t.Fatalf("stdout = %q, want hi", res.Stdout)
	}
	if len(api.execContainerIDs) != 1 || api.execContainerIDs[0] != id {
		t.Fatalf("exec container id = %v, want exactly [%q] (container id == session id)", api.execContainerIDs, id)
	}
}

// TestManagerBackend_IntegratedInfo pins that Info with the manager-returned
// session id does not error (regression: used to return ErrNotFound) and inspects
// the container identified by that id.
func TestManagerBackend_IntegratedInfo(t *testing.T) {
	api := newFakeClient()
	be := New(api, nil)
	m := session.NewManager(be)

	sess, err := m.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := sess.ID()

	info, err := be.Info(context.Background(), id)
	if err != nil {
		t.Fatalf("Info with manager-returned session id must not error (regression): %v", err)
	}
	if info == nil {
		t.Fatal("nil info")
	}
	if len(api.inspects) != 1 || api.inspects[0] != id {
		t.Fatalf("inspected container id = %v, want exactly [%q] (container id == session id)", api.inspects, id)
	}
}

// TestManagerBackend_IntegratedStats pins that Stats with the manager-returned
// session id does not error (regression: used to return ErrNotFound) and reads
// the container identified by that id.
func TestManagerBackend_IntegratedStats(t *testing.T) {
	api := newFakeClient()
	be := New(api, nil)
	m := session.NewManager(be)

	sess, err := m.Create(context.Background(), baseSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := sess.ID()

	st, err := be.Stats(context.Background(), id)
	if err != nil {
		t.Fatalf("Stats with manager-returned session id must not error (regression): %v", err)
	}
	if st == nil {
		t.Fatal("nil stats")
	}
	if len(api.statsCalls) != 1 || api.statsCalls[0] != id {
		t.Fatalf("stats container id = %v, want exactly [%q] (container id == session id)", api.statsCalls, id)
	}
}
