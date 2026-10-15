package darwin

// Complementary unit tests closing the remaining uncovered branches of the
// portable darwin core (darwin.go / deny.go) so the package comfortably clears
// the ≥95% gate and survives mutation testing. Each test pins observable
// behaviour of an error/edge path that backend_test.go / darwin_extra_test.go
// do not drive directly:
//
//   - Destroy surfaces a non-ESRCH KillGroup error;
//   - Exec honours a per-request WorkingDir override and Env override;
//   - Exec surfaces a runner.Start error (the create succeeded, exec spawn fails);
//   - parseDenyClause is fail-closed when the line carries no "deny" keyword.
//
// Everything here is behaviour-focused, uses the same fakeRunner boundary, and
// never touches the real sandbox-exec binary, os/exec or the host shell, so it
// runs in CI on any GOOS.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
)

// TestDestroy_KillGroupErrorReturned pins that a KillGroup failure other than
// ESRCH is surfaced to the caller (the ESRCH idempotency case is covered
// separately in backend_test.go).
func TestDestroy_KillGroupErrorReturned(t *testing.T) {
	fake := newFakeRunner()
	fake.killErr = errors.New("kill failed")
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := b.Destroy(context.Background(), id); err == nil {
		t.Fatal("Destroy should surface the KillGroup error")
	} else if !strings.Contains(err.Error(), "kill failed") {
		t.Fatalf("Destroy err = %v, want it to mention the kill failure", err)
	}
}

// TestExec_WorkingDirOverride pins that a per-request WorkingDir overrides the
// session's Create-time working dir for that Exec call.
func TestExec_WorkingDirOverride(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{nil, {ExitCode: 0}}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{
		Argv:       []string{"bash", "-c", "pwd"},
		WorkingDir: "/elsewhere",
	}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// Start #1 = create child, Start #2 = exec child.
	start := fake.recordedStarts()[1]
	if start.Dir != "/elsewhere" {
		t.Fatalf("exec Dir = %q, want /elsewhere (per-request override)", start.Dir)
	}
}

// TestExec_EnvOverride pins that a per-request Env replaces the session env for
// that Exec call.
func TestExec_EnvOverride(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{nil, {ExitCode: 0}}
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := b.Exec(context.Background(), id, exec.Request{
		Argv: []string{"bash", "-c", "env"},
		Env:  map[string]string{"K": "V"},
	}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	start := fake.recordedStarts()[1]
	if !slices.Contains(start.Env, "K=V") {
		t.Fatalf("exec Env = %v, want it to contain K=V (per-request override)", start.Env)
	}
}

// TestExec_StartError pins that a runner.Start failure while spawning the exec
// child is surfaced. The session (Create) has already succeeded; only the exec
// spawn fails, so the error must carry the "exec start" marker.
func TestExec_StartError(t *testing.T) {
	fake := newFakeRunner()
	b := newTestBackend(t, fake, []string{"/work"})

	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Fail only the exec spawn, after Create has succeeded.
	fake.startErr = errors.New("exec spawn failed")
	if _, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "x"}}); err == nil {
		t.Fatal("Exec should surface the runner.Start error")
	} else if !strings.Contains(err.Error(), "exec start") {
		t.Fatalf("Exec err = %v, want it to mention 'exec start'", err)
	}
}

// TestParseDenyClause_NoDenyKeyword pins the fail-closed early return: a line
// with no "deny" keyword yields no clause (operation/target empty, ok=false).
// This is exercised directly because ClassifyDeny gates on isDenyText first, so
// the parse-level early return is only reachable through a direct call.
func TestParseDenyClause_NoDenyKeyword(t *testing.T) {
	op, target, ok := parseDenyClause("sandboxd allowed something for pid 7")
	if ok {
		t.Fatal("parseDenyClause ok = true for a line with no 'deny' keyword, want false")
	}
	if op != "" || target != "" {
		t.Fatalf("parseDenyClause op/target = %q/%q, want empty/empty", op, target)
	}
}

// TestParseDenyClause_EmptyAfterKeyword pins the second fail-closed path: a line
// whose clause after "deny" has no fields yields no clause.
func TestParseDenyClause_EmptyAfterKeyword(t *testing.T) {
	_, _, ok := parseDenyClause("com.apple.sandbox deny   ")
	if ok {
		t.Fatal("parseDenyClause ok = true when nothing follows 'deny', want false")
	}
}
