package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/domain/requestid"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// fakeLife implements role.Lifecycle with recording and injectable errors.
// Its Create returns a backend-assigned id (the source of the session id under
// the backend-owned-id design): by default a unique "ctr-<letter>" per call,
// or a configurable fixed id / empty id / fixed id-sequence when a test needs
// those scenarios.
type fakeLife struct {
	mu         sync.Mutex
	nextID     int
	created    []spec.SandboxSpec
	destroyed  []string
	createErr  error
	destroyErr error
	createdIDs map[string]bool

	// fixedID, when non-empty, is returned verbatim on every Create (used to
	// simulate a backend that assigns the same container id to two requests,
	// which the manager must surface as a duplicate-id error).
	fixedID string
	// emptyID, when true, makes Create return ("", nil), simulating a backend
	// that supplies no id and thus triggering the manager's generated-id
	// fallback (m.newID).
	emptyID bool
	// idSeq, when non-nil, is a fixed list of ids returned in order on each
	// Create (used to drive deterministic prefix-resolution tests from
	// backend-returned ids rather than a forced m.newID).
	idSeq  []string
	idSeqN int
}

func newFakeLife() *fakeLife {
	return &fakeLife{createdIDs: make(map[string]bool)}
}

func (f *fakeLife) Create(_ context.Context, s spec.SandboxSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.emptyID {
		return "", nil
	}
	if f.fixedID != "" {
		f.created = append(f.created, s)
		f.createdIDs[f.fixedID] = true

		return f.fixedID, nil
	}
	if f.idSeq != nil {
		if f.idSeqN >= len(f.idSeq) {
			return "", errors.New("no more ids")
		}
		id := f.idSeq[f.idSeqN]
		f.idSeqN++
		f.created = append(f.created, s)
		f.createdIDs[id] = true

		return id, nil
	}
	f.nextID++
	id := "ctr-" + string(rune('a'+f.nextID-1))
	f.created = append(f.created, s)
	f.createdIDs[id] = true

	return id, nil
}

func (f *fakeLife) Destroy(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.destroyed = append(f.destroyed, id)
	delete(f.createdIDs, id)

	return nil
}

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

// TestCreate_SessionIDIsBackendID pins the backend-owned-id design: the session
// id MUST be the id returned by the lifecycle backend (the docker container id
// / darwin "darwin-N"), not a manager-generated opaque id. fakeLife returns
// "ctr-a", "ctr-b", ... so the first and second creates must carry exactly
// those ids.
func TestCreate_SessionIDIsBackendID(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)

	sess1, err := m.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if sess1.ID() != "ctr-a" {
		t.Fatalf("first session id = %q, want backend-returned \"ctr-a\"", sess1.ID())
	}

	sess2, err := m.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if sess2.ID() != "ctr-b" {
		t.Fatalf("second session id = %q, want backend-returned \"ctr-b\"", sess2.ID())
	}

	if len(life.created) != 2 {
		t.Fatalf("created = %d, want 2", len(life.created))
	}
}

func TestCreateAndGet(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	sess, err := m.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID() == "" {
		t.Fatal("empty session id")
	}
	if sess.State() != domainsession.StateRunning {
		t.Fatalf("state = %q", sess.State())
	}
	got, err := m.Get(sess.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID() != sess.ID() {
		t.Fatalf("Get returned wrong session")
	}
	if len(life.created) != 1 {
		t.Fatalf("created = %d", len(life.created))
	}
}

func TestGet_NotFound(t *testing.T) {
	m := NewManager(newFakeLife())
	if _, err := m.Get("ghost"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCreate_InvalidSpecRejected(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	s := validSpec()
	s.Image = "" // required
	s.Isolation.Memory = 0
	if _, err := m.Create(context.Background(), s); err == nil {
		t.Fatal("expected validation error")
	}
	if len(life.created) != 0 {
		t.Fatal("backend should not be called for invalid spec")
	}
}

func TestCreate_BackendError(t *testing.T) {
	life := newFakeLife()
	life.createErr = errors.New("daemon down")
	m := NewManager(life)
	if _, err := m.Create(context.Background(), validSpec()); err == nil {
		t.Fatal("expected backend error")
	}
	if len(m.List(context.Background())) != 0 {
		t.Fatal("no session should be registered on backend failure")
	}
}

func TestCreate_NetworkHostAllowed(t *testing.T) {
	// Phase 7: the network-allowlist is removed from the manager; Validate no
	// longer rejects host networking. Host is a valid mode.
	life := newFakeLife()
	m := NewManager(life)
	s := validSpec()
	s.Isolation.NetworkMode = spec.NetworkHost
	if _, err := m.Create(context.Background(), s); err != nil {
		t.Fatalf("host networking must be allowed: %v", err)
	}
	if len(life.created) != 1 {
		t.Fatal("backend should be called")
	}
}

func TestList(t *testing.T) {
	m := NewManager(newFakeLife())
	if _, err := m.Create(context.Background(), validSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), validSpec()); err != nil {
		t.Fatal(err)
	}
	if got := m.List(context.Background()); len(got) != 2 {
		t.Fatalf("List len = %d", len(got))
	}
}

func TestDestroy(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	sess, err := m.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Destroy(context.Background(), sess.ID()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(life.destroyed) != 1 || life.destroyed[0] != sess.ID() {
		t.Fatalf("destroyed = %v", life.destroyed)
	}
	if _, err := m.Get(sess.ID()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("session should be gone after destroy")
	}
}

func TestDestroy_UnknownIsNoOp(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	if err := m.Destroy(context.Background(), "nope"); err != nil {
		t.Fatalf("destroy unknown should be no-op, got %v", err)
	}
	if len(life.destroyed) != 0 {
		t.Fatal("backend should not be called")
	}
}

func TestDestroy_BackendErrorKeepsSession(t *testing.T) {
	life := newFakeLife()
	life.destroyErr = errors.New("fail")
	m := NewManager(life)
	sess, err := m.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Destroy(context.Background(), sess.ID()); err == nil {
		t.Fatal("expected destroy error")
	}
	if _, err := m.Get(sess.ID()); err != nil {
		t.Fatal("session should remain after failed destroy")
	}
}

func TestDestroyAll(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	for range 3 {
		if _, err := m.Create(context.Background(), validSpec()); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DestroyAll(context.Background()); err != nil {
		t.Fatalf("DestroyAll: %v", err)
	}
	if len(m.List(context.Background())) != 0 {
		t.Fatalf("sessions remain after DestroyAll: %d", len(m.List(context.Background())))
	}
	if len(life.destroyed) != 3 {
		t.Fatalf("destroyed = %d, want 3", len(life.destroyed))
	}
}

func TestReap_IdleOnly(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	for range 2 {
		if _, err := m.Create(context.Background(), validSpec()); err != nil {
			t.Fatal(err)
		}
	}
	// Fresh sessions are not idle.
	if err := m.Reap(context.Background(), 15*time.Minute, time.Now()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(m.List(context.Background())) != 2 {
		t.Fatalf("List after fresh reap = %d, want 2", len(m.List(context.Background())))
	}
	// Age every session beyond the idle window.
	for _, s := range m.List(context.Background()) {
		s.Touch(time.Now().Add(-30 * time.Minute))
	}
	if err := m.Reap(context.Background(), 15*time.Minute, time.Now()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(m.List(context.Background())) != 0 {
		t.Fatalf("List after aged reap = %d, want 0", len(m.List(context.Background())))
	}
	if len(life.destroyed) != 2 {
		t.Fatalf("destroyed = %d, want 2", len(life.destroyed))
	}
}

// Ensure the session aggregate methods are covered.
func TestSessionAggregate(t *testing.T) {
	sess := domainsession.New("abc", validSpec())
	if sess.ID() != "abc" {
		t.Fatal("id mismatch")
	}
	if sess.State() != domainsession.StateCreating {
		t.Fatalf("initial state = %q", sess.State())
	}
	sess.MarkRunning()
	if sess.State() != domainsession.StateRunning {
		t.Fatal("should be running")
	}
	now := time.Now()
	sess.Touch(now.Add(-time.Hour))
	if !sess.Expired(30*time.Minute, time.Now()) {
		t.Fatal("session should be expired")
	}
	if sess.Expired(2*time.Hour, time.Now()) {
		t.Fatal("session should not be expired")
	}
	sess.MarkDestroyed()
	if sess.Expired(time.Minute, time.Now()) {
		t.Fatal("destroyed sessions are never expired/reaped")
	}
	if sess.State() != domainsession.StateDestroyed {
		t.Fatal("should be destroyed")
	}
}

func TestSessionMutex(_ *testing.T) {
	sess := domainsession.New("m", validSpec())
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			sess.Lock()
			defer sess.Unlock()
			sess.Touch(time.Now())
		})
	}
	wg.Wait()
}

func TestSessionContextID(t *testing.T) {
	ctx := domainsession.WithSessionID(context.Background(), "sid")
	id, ok := domainsession.IDFromContext(ctx)
	if !ok || id != "sid" {
		t.Fatalf("IDFromContext = %q, %v", id, ok)
	}
	if _, ok := domainsession.IDFromContext(context.Background()); ok {
		t.Fatal("plain context should have no session id")
	}
}

// Compile-time: Manager implements the domain Manager interface and the fake
// satisfies role.Lifecycle.
var (
	_ domainsession.Manager = (*Manager)(nil)
	_ role.Lifecycle        = (*fakeLife)(nil)
	_                       = exec.Request{}
)

func TestNewID_IsHex(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != IDSizeBytes*2 {
		t.Fatalf("id length = %d", len(id))
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("non-hex char %q", r)
		}
	}
}

func TestReap_DestroyError(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	if _, err := m.Create(context.Background(), validSpec()); err != nil {
		t.Fatal(err)
	}
	life.destroyErr = errors.New("fail")
	for _, s := range m.List(context.Background()) {
		s.Touch(time.Now().Add(-time.Hour))
	}
	if err := m.Reap(context.Background(), 15*time.Minute, time.Now()); err == nil {
		t.Fatal("expected reap destroy error")
	}
}

func TestDestroyAll_Error(t *testing.T) {
	life := newFakeLife()
	m := NewManager(life)
	for range 2 {
		if _, err := m.Create(context.Background(), validSpec()); err != nil {
			t.Fatal(err)
		}
	}
	life.destroyErr = errors.New("fail")
	if err := m.DestroyAll(context.Background()); err == nil {
		t.Fatal("expected DestroyAll error")
	}
}

// TestCreate_NewIDError pins that the manager's generated-id fallback (m.newID)
// runs ONLY when the backend returns an empty id: a backend returning ("", nil)
// delegates id generation to newID, and a newID failure surfaces as an error.
func TestCreate_NewIDError(t *testing.T) {
	life := newFakeLife()
	life.emptyID = true // backend supplies no id -> fall back to m.newID
	m := NewManager(life)
	m.newID = func() (string, error) { return "", errors.New("rand fail") }
	if _, err := m.Create(context.Background(), validSpec()); err == nil {
		t.Fatal("expected id generation error")
	}
	if len(m.List(context.Background())) != 0 {
		t.Fatal("no session should be registered")
	}
}

// TestCreate_BackendIDBypassesNewID pins that when the backend returns a
// NON-empty id, the generated-id fallback is never consulted: even if newID
// would fail, Create must succeed using the backend-returned id. This guards
// the design against a regression that always calls newID (or discards the
// backend id).
func TestCreate_BackendIDBypassesNewID(t *testing.T) {
	life := newFakeLife() // returns "ctr-a"
	m := NewManager(life)
	m.newID = func() (string, error) { return "", errors.New("rand fail") }
	sess, err := m.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("Create must succeed via the backend-returned id even when newID fails: %v", err)
	}
	if sess.ID() != "ctr-a" {
		t.Fatalf("session id = %q, want backend-returned \"ctr-a\"", sess.ID())
	}
}

// TestCreate_DuplicateIDSurfaced pins the post-create duplicate check (X02)
// under the backend-owned-id design: when the backend returns the SAME id for
// two creates, the second must be surfaced as a duplicate-id error and must
// NOT register a second session.
func TestCreate_DuplicateIDSurfaced(t *testing.T) {
	life := newFakeLife()
	life.fixedID = "fixed" // backend returns the same container id every time
	m := NewManager(life)
	if _, err := m.Create(context.Background(), validSpec()); err != nil {
		t.Fatal(err)
	}
	// Second create returns the same backend id -> duplicate error, and only
	// one session is registered.
	if _, err := m.Create(context.Background(), validSpec()); err == nil {
		t.Fatal("expected duplicate id error")
	}
	if len(m.List(context.Background())) != 1 {
		t.Fatalf("registered sessions = %d, want 1", len(m.List(context.Background())))
	}
}

func TestNewIDFrom_Error(t *testing.T) {
	if _, err := newIDFrom(strings.NewReader("short")); err == nil {
		t.Fatal("expected error from short reader")
	}
}

// TestManager_TraceLogsCarryRequestIDAndSession asserts the manager threads
// request_id (from the context) and session_id onto its trace lines (L09).
func TestManager_TraceLogsCarryRequestIDAndSession(t *testing.T) {
	var buf bytes.Buffer
	l := logrus.New()
	l.SetOutput(&buf)
	l.SetLevel(logrus.TraceLevel)
	l.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})

	m := NewManager(newFakeLife(), WithLogger(logrus.NewEntry(l)))
	ctx := requestid.WithID(context.Background(), "req-abc")
	if _, err := m.Create(ctx, validSpec()); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "req-abc") {
		t.Fatalf("trace output missing request_id:\n%s", out)
	}
	if !strings.Contains(out, `operation=create`) {
		t.Fatalf("trace output missing operation field:\n%s", out)
	}
	if !strings.Contains(out, "session: operation done") {
		t.Fatalf("trace output missing duration log:\n%s", out)
	}
}
