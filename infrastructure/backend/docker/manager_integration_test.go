package docker

// Integrated manager → backend contract tests (SPEC §6.6 / §7.8). These wire a
// REAL docker backend (over the in-memory fake client) into a REAL session
// manager and drive the exact flow the MCP service performs:
//
//	Manager.Create -> backend.Create (returns the container id)
//	Manager.Destroy / backend.Exec / backend.Info / backend.Stats (session id)
//
// Under the namespaced-sandbox-id design the session id is `slug:internalID`
// (e.g. "docker:cid-1"): the manager stores it namespaced, but the docker
// backend STRIPS the "docker:" slug before calling the Docker API, so every
// ContainerRemove / exec / inspect / stats call receives the raw container id
// ("cid-N"). These tests are the regression guard for the "session id =
// namespaced backend id" refactor: before the fix the backend passed the whole
// "docker:cid-1" string as the container id and the daemon lookups resolved
// nothing.

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/application/session"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// TestManagerBackend_IntegratedDestroy pins that Destroy through the manager
// actually removes the container owned by the created session, passing the
// STRIPPED raw container id (without the "docker:" slug) to the Docker API.
func TestManagerBackend_IntegratedDestroy(t *testing.T) {
	api := newFakeClient()
	be := New(api, nil)
	m := session.NewManager(be.Slug(), be)

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

	// The session id is namespaced ("docker:cid-1"); the backend must strip the
	// slug and remove the raw container id.
	raw := namespacedInternal(t, id)
	if len(api.removes) != 1 || api.removes[0] != raw {
		t.Fatalf("removes = %v, want exactly [%q] (slug stripped)", api.removes, raw)
	}

	if _, err := m.Get(id); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("session should be gone after destroy, got err = %v", err)
	}
}

// TestManagerBackend_IntegratedExec pins that Exec with the manager-returned
// namespaced session id does not error and reaches the container identified by
// the STRIPPED raw id.
func TestManagerBackend_IntegratedExec(t *testing.T) {
	api := newFakeClient()
	api.execResults = []execResult{{stdout: "hi", exitCode: 0}}
	be := New(api, nil)
	m := session.NewManager(be.Slug(), be)

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
	raw := namespacedInternal(t, id)
	if len(api.execContainerIDs) != 1 || api.execContainerIDs[0] != raw {
		t.Fatalf("exec container id = %v, want exactly [%q] (slug stripped)", api.execContainerIDs, raw)
	}
}

// TestManagerBackend_IntegratedInfo pins that Info with the manager-returned
// namespaced session id does not error and inspects the STRIPPED raw id.
func TestManagerBackend_IntegratedInfo(t *testing.T) {
	api := newFakeClient()
	be := New(api, nil)
	m := session.NewManager(be.Slug(), be)

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
	raw := namespacedInternal(t, id)
	if len(api.inspects) != 1 || api.inspects[0] != raw {
		t.Fatalf("inspected container id = %v, want exactly [%q] (slug stripped)", api.inspects, raw)
	}
}

// TestManagerBackend_IntegratedStats pins that Stats with the manager-returned
// namespaced session id does not error and reads the STRIPPED raw id.
func TestManagerBackend_IntegratedStats(t *testing.T) {
	api := newFakeClient()
	be := New(api, nil)
	m := session.NewManager(be.Slug(), be)

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
	raw := namespacedInternal(t, id)
	if len(api.statsCalls) != 1 || api.statsCalls[0] != raw {
		t.Fatalf("stats container id = %v, want exactly [%q] (slug stripped)", api.statsCalls, raw)
	}
}

// namespacedInternal extracts the raw (slug-stripped) backend id from a
// namespaced session id. It fails the test if the id is not namespaced.
func namespacedInternal(t *testing.T, id string) string {
	t.Helper()
	n, err := domainsession.ParseNamespacedID(id)
	if err != nil {
		t.Fatalf("session id %q must be namespaced: %v", id, err)
	}

	return n.Internal
}
