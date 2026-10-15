package darwin

// Tests for Fix #2: the in-band policy-denial classifier is wired into
// Backend.Exec (darwin.go). A command whose child stderr carries a Seatbelt
// deny marker or an in-band EPERM ("Operation not permitted") must be
// represented on the returned domain/sandbox/exec.Result.PolicyDenied — so a
// policy-denied command is distinguishable from a plain non-zero exit.
//
// The classifier predicate itself (IsPolicyDenial) is unit-tested in
// deny_test.go; these tests pin that Exec actually USES it and maps the outcome
// onto the shared exec.Result (TDD, T01). They run on any GOOS via the fake
// runner: the fake's canned child Result carries the stderr, and Exec must
// classify it.
//
// NOTE: these tests reference the NEW exec.Result.PolicyDenied field, which does
// not exist yet — they will not compile until @developer adds it. That is the
// intended failing-first state for the TDD workflow.

import (
	"context"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
)

// execOne creates a session and runs a single Exec call. The caller must have
// preconfigured fake.results so the second produced process (the exec child)
// carries the canned Result; the first (create child) is unused.
func execOne(t *testing.T, fake *fakeRunner) *exec.Result {
	t.Helper()
	b := newTestBackend(t, fake, []string{"/work"})
	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "cmd"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	return res
}

// TestExec_PolicyDenied_EPERM pins that an in-band EPERM on stderr is surfaced
// as PolicyDenied on the returned exec.Result.
func TestExec_PolicyDenied_EPERM(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{
		nil, // create child (unused)
		{ExitCode: 1, Stderr: []byte("open /etc/passwd: Operation not permitted")},
	}
	res := execOne(t, fake)
	if !res.PolicyDenied {
		t.Fatalf("PolicyDenied = false for stderr with EPERM, want true; result=%+v", res)
	}
	if res.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1", res.ExitCode)
	}
}

// TestExec_PolicyDenied_DenyMarker pins that a Seatbelt deny marker on stderr is
// surfaced as PolicyDenied.
func TestExec_PolicyDenied_DenyMarker(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{
		nil, // create child (unused)
		{ExitCode: 1, Stderr: []byte("Sandbox: deny file-read /etc/passwd")},
	}
	res := execOne(t, fake)
	if !res.PolicyDenied {
		t.Fatalf("PolicyDenied = false for stderr with deny marker, want true; result=%+v", res)
	}
}

// TestExec_NotPolicyDenied_NormalNonZero pins that a plain non-zero exit with no
// deny/EPERM marker is NOT marked as a policy denial (fail-closed, no false
// positives).
func TestExec_NotPolicyDenied_NormalNonZero(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{
		nil, // create child (unused)
		{ExitCode: 3, Stderr: []byte("command not found")},
	}
	res := execOne(t, fake)
	if res.PolicyDenied {
		t.Fatalf("PolicyDenied = true for plain non-zero exit, want false; result=%+v", res)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}
}

// TestExec_NotPolicyDenied_Clean pins that a clean exit 0 is not a policy
// denial.
func TestExec_NotPolicyDenied_Clean(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{
		nil, // create child (unused)
		{ExitCode: 0, Stdout: []byte("ok")},
	}
	res := execOne(t, fake)
	if res.PolicyDenied {
		t.Fatalf("PolicyDenied = true for clean exit, want false; result=%+v", res)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
}

// TestExec_PolicyDenied_DistinguishableFromPlainNonZero pins the whole point of
// the field: two children both exiting 1 — one policy-denied, one a plain
// error — must be distinguishable via PolicyDenied even though their exit codes
// are identical.
func TestExec_PolicyDenied_DistinguishableFromPlainNonZero(t *testing.T) {
	fake := newFakeRunner()
	fake.results = []*Result{
		nil, // create child (unused)
		{ExitCode: 1, Stderr: []byte("Sandbox: deny file-read /etc/passwd")},
		{ExitCode: 1, Stderr: []byte("command not found")},
	}
	b := newTestBackend(t, fake, []string{"/work"})
	id, err := b.Create(context.Background(), baseSeatbeltSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	denied, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "a"}})
	if err != nil {
		t.Fatalf("Exec (denied): %v", err)
	}
	plain, err := b.Exec(context.Background(), id, exec.Request{Argv: []string{"bash", "-c", "b"}})
	if err != nil {
		t.Fatalf("Exec (plain): %v", err)
	}

	if !denied.PolicyDenied {
		t.Fatalf("denied child PolicyDenied = false, want true; result=%+v", denied)
	}
	if plain.PolicyDenied {
		t.Fatalf("plain child PolicyDenied = true, want false; result=%+v", plain)
	}
	// Both exit 1 — the exit code alone cannot discriminate, PolicyDenied must.
	if denied.ExitCode != 1 || plain.ExitCode != 1 {
		t.Fatalf("exit codes = %d/%d, want 1/1 (identical) so PolicyDenied is the discriminator",
			denied.ExitCode, plain.ExitCode)
	}
}
