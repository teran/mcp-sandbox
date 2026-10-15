package darwin

// Tests for the portable policy-denial detection of the darwin / sandbox-exec
// (Seatbelt) backend (SPEC §7.12). macOS records policy denials in the unified
// log (subsystem == "com.apple.sandbox"); sandbox-exec does not log them
// itself. The backend tails that log, classifies a `deny` as a policy failure
// (not a normal exit code), and logs it as an L08-style `sandbox_deny
// {session_id, pid, ...}` event with no secrets (S02).
//
// Everything here is pure, portable Go: it parses log lines, maps a deny onto
// a structured log record, classifies a backend Result, and consumes lines
// from the `logTailer` boundary abstraction. No test touches `log stream`/`log
// show`, /usr/bin/sandbox-exec, os/exec or a live Darwin kernel, so the whole
// suite runs in CI on any GOOS. The real unified-log tail is macOS-specific and
// lives behind the darwin build tag (_darwin.go) — outside this file's scope.
//
// These tests define the intended API (TDD, T01):
//
//	type DenyEvent struct {
//	    Subsystem string // "com.apple.sandbox" for a classified deny
//	    Operation string // e.g. "file-read", "mach-lookup", "network-outbound"
//	    Target    string // denied path or Mach service, may be empty
//	    Pid       int    // 0 when the line carried no pid
//	    Message   string // SHORT synthesized summary, never the raw line (S02)
//	}
//	func ClassifyDeny(line string) (DenyEvent, bool)
//
//	type SandboxDenyLog struct {
//	    SessionID string
//	    Subsystem string
//	    Operation string
//	    Target    string
//	    Pid       int
//	    Message   string
//	}
//	func ToSandboxDeny(sessionID string, ev DenyEvent) SandboxDenyLog
//
//	func IsPolicyDenial(res *Result) bool
//
//	type logTailer interface {
//	    Lines(ctx context.Context) (<-chan string, error)
//	}
//	func ConsumeDenials(ctx context.Context, t logTailer, sessionID string, emit func(SandboxDenyLog)) error

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// ClassifyDeny — deny classification of a single unified-log line
// ---------------------------------------------------------------------------

func TestClassifyDeny_FileReadWithPath(t *testing.T) {
	line := "2024-05-01 10:00:00.000+0000 sandboxd[123] Sandbox: deny file-read /private/etc/passwd for PID 789 (com.apple.sandbox)"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true for a sandbox deny line")
	}
	if ev.Subsystem != "com.apple.sandbox" {
		t.Fatalf("Subsystem = %q, want com.apple.sandbox", ev.Subsystem)
	}
	if ev.Operation != "file-read" {
		t.Fatalf("Operation = %q, want file-read", ev.Operation)
	}
	if ev.Target != "/private/etc/passwd" {
		t.Fatalf("Target = %q, want /private/etc/passwd", ev.Target)
	}
	if ev.Pid != 789 {
		t.Fatalf("Pid = %d, want 789", ev.Pid)
	}
}

func TestClassifyDeny_MachLookup(t *testing.T) {
	line := "sandboxd: deny mach-lookup com.apple.system.logger (com.apple.sandbox) pid 456"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true for a mach-lookup deny")
	}
	if ev.Operation != "mach-lookup" {
		t.Fatalf("Operation = %q, want mach-lookup", ev.Operation)
	}
	if ev.Target != "com.apple.system.logger" {
		t.Fatalf("Target = %q, want com.apple.system.logger", ev.Target)
	}
	if ev.Pid != 456 {
		t.Fatalf("Pid = %d, want 456", ev.Pid)
	}
}

func TestClassifyDeny_NetworkOutbound(t *testing.T) {
	line := "10:00:00 sandboxd deny network-outbound (com.apple.sandbox) pid 999"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true for a network-outbound deny")
	}
	if ev.Operation != "network-outbound" {
		t.Fatalf("Operation = %q, want network-outbound", ev.Operation)
	}
	if ev.Pid != 999 {
		t.Fatalf("Pid = %d, want 999", ev.Pid)
	}
}

// TestClassifyDeny_OperationWithoutTarget pins that an operation with no path
// or service (e.g. process-fork) still classifies, leaving Target empty.
func TestClassifyDeny_OperationWithoutTarget(t *testing.T) {
	line := "sandboxd Sandbox: deny process-fork (com.apple.sandbox) pid 5"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true for a process-fork deny")
	}
	if ev.Operation != "process-fork" {
		t.Fatalf("Operation = %q, want process-fork", ev.Operation)
	}
	if ev.Target != "" {
		t.Fatalf("Target = %q, want empty", ev.Target)
	}
	if ev.Pid != 5 {
		t.Fatalf("Pid = %d, want 5", ev.Pid)
	}
}

// TestClassifyDeny_DenyWithoutPid pins that a deny with no pid still classifies
// (Pid stays 0) rather than being dropped — fail-open on the *parse* of an
// optional field, fail-closed on whether it is a deny at all.
func TestClassifyDeny_DenyWithoutPid(t *testing.T) {
	line := "Sandbox: deny file-read /etc/passwd (com.apple.sandbox)"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true for a deny without pid")
	}
	if ev.Pid != 0 {
		t.Fatalf("Pid = %d, want 0", ev.Pid)
	}
}

// TestClassifyDeny_NotDeny_AllowLine pins that an *allow* line from the sandbox
// subsystem is not classified as a deny.
func TestClassifyDeny_NotDeny_AllowLine(t *testing.T) {
	line := "sandboxd[5] Sandbox: allow file-read /etc/passwd (com.apple.sandbox) pid 3"
	if _, ok := ClassifyDeny(line); ok {
		t.Fatal("ClassifyDeny = true for an allow line, want false")
	}
}

// TestClassifyDeny_NotDeny_OtherSubsystem pins that a deny from a *different*
// subsystem (no com.apple.sandbox marker) is not classified — we only report
// sandbox policy denials (SPEC §7.12).
func TestClassifyDeny_NotDeny_OtherSubsystem(t *testing.T) {
	line := "com.apple.coreduetd deny something for pid 7"
	if _, ok := ClassifyDeny(line); ok {
		t.Fatal("ClassifyDeny = true for a non-sandbox subsystem deny, want false")
	}
}

// TestClassifyDeny_NotDeny_PlainLogLine pins that ordinary log/info noise is
// not classified even when it mentions the word "deny".
func TestClassifyDeny_NotDeny_PlainLogLine(t *testing.T) {
	line := "some daemon reported it would deny future requests (no sandbox marker)"
	if _, ok := ClassifyDeny(line); ok {
		t.Fatal("ClassifyDeny = true for a plain log line, want false")
	}
}

// TestClassifyDeny_FailClosed pins fail-closed behaviour (SPEC §7.12 / S02):
// empty and malformed input is never a deny and never panics.
func TestClassifyDeny_FailClosed(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"!!!####",
		"deny",                       // "deny" alone has no sandbox subsystem marker
		"com.apple.sandbox",          // subsystem marker alone has no deny
		"Sandbox:",                   // marker without deny
		"deny file-read /etc/passwd", // no com.apple.sandbox / sandbox marker
	}
	for _, c := range cases {
		if _, ok := ClassifyDeny(c); ok {
			t.Fatalf("ClassifyDeny(%q) = true, want false (fail-closed)", c)
		}
	}
}

// TestClassifyDeny_NoPanicOnGarbage pins that odd input never panics (a parser
// must be total over any string).
func TestClassifyDeny_NoPanicOnGarbage(t *testing.T) {
	cases := []string{
		"\x00\x01\x02",
		"pid pid pid",  //nolint:dupword // deliberate malformed input: repeated "pid"
		"deny \n deny", //nolint:dupword // deliberate malformed input: repeated "deny"
		strings.Repeat("x", 10_000),
	}
	for _, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ClassifyDeny(%q) panicked: %v", c, r)
				}
			}()
			_, _ = ClassifyDeny(c)
		}()
	}
}

// TestClassifyDeny_MessageIsSynthesized pins the S02 guarantee that the parsed
// Message is a short summary built from the parsed fields, never the raw line:
// a secret-like value embedded elsewhere in the line must not appear in the
// Message.
func TestClassifyDeny_MessageIsSynthesized(t *testing.T) {
	secret := "super-secret-token-abc123"
	line := "sandboxd Sandbox: deny file-read /safe (reason=" + secret + ") (com.apple.sandbox) pid 111"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true")
	}
	if strings.Contains(ev.Message, secret) {
		t.Fatalf("Message %q leaked the secret %q (S02)", ev.Message, secret)
	}
	if !strings.Contains(ev.Message, ev.Operation) || !strings.Contains(ev.Message, ev.Target) {
		t.Fatalf("Message %q should summarize operation %q and target %q", ev.Message, ev.Operation, ev.Target)
	}
}

// ---------------------------------------------------------------------------
// ToSandboxDeny — mapping a DenyEvent onto an L08-style sandbox_deny record
// ---------------------------------------------------------------------------

func TestToSandboxDeny_MapsFields(t *testing.T) {
	ev := DenyEvent{
		Subsystem: "com.apple.sandbox",
		Operation: "file-read",
		Target:    "/etc/passwd",
		Pid:       789,
		Message:   "deny file-read /etc/passwd",
	}
	got := ToSandboxDeny("darwin-7", ev)
	if got.SessionID != "darwin-7" {
		t.Fatalf("SessionID = %q, want darwin-7", got.SessionID)
	}
	if got.Pid != 789 {
		t.Fatalf("Pid = %d, want 789", got.Pid)
	}
	if got.Operation != "file-read" || got.Target != "/etc/passwd" {
		t.Fatalf("Operation/Target = %q/%q, want file-read//etc/passwd", got.Operation, got.Target)
	}
	if got.Subsystem != "com.apple.sandbox" {
		t.Fatalf("Subsystem = %q, want com.apple.sandbox", got.Subsystem)
	}
}

// TestSandboxDeny_SecretNeverLeaks is the end-to-end S02 assertion: classify a
// deny line that carries a secret-like value in its noise, map it to a log
// record, and assert the secret is absent from every field of the record.
func TestSandboxDeny_SecretNeverLeaks(t *testing.T) {
	secret := "super-secret-token-abc123"
	line := "sandboxd Sandbox: deny file-read /safe (reason=" + secret + ") (com.apple.sandbox) pid 111"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false, want true")
	}
	got := ToSandboxDeny("darwin-1", ev)

	for name, value := range map[string]string{
		"SessionID": got.SessionID,
		"Subsystem": got.Subsystem,
		"Operation": got.Operation,
		"Target":    got.Target,
		"Message":   got.Message,
	} {
		if strings.Contains(value, secret) {
			t.Fatalf("field %s leaked the secret %q (S02): %q", name, secret, value)
		}
	}
}

// ---------------------------------------------------------------------------
// IsPolicyDenial — classify a backend Result (in-band EPERM / deny marker)
// ---------------------------------------------------------------------------

// TestIsPolicyDenial_DenyMarkerInStderr pins that a deny marker on stderr is a
// policy failure, not a normal exit code.
func TestIsPolicyDenial_DenyMarkerInStderr(t *testing.T) {
	res := &Result{ExitCode: 1, Stderr: []byte("Sandbox: deny file-read /etc/passwd")}
	if !IsPolicyDenial(res) {
		t.Fatal("IsPolicyDenial = false for stderr with deny marker, want true")
	}
}

// TestIsPolicyDenial_EPERMInStderr pins that an in-band EPERM surfaces as a
// policy denial (SPEC §7.12: a denied syscall also surfaces in-band as EPERM).
func TestIsPolicyDenial_EPERMInStderr(t *testing.T) {
	res := &Result{ExitCode: 1, Stderr: []byte("open /etc/passwd: Operation not permitted")}
	if !IsPolicyDenial(res) {
		t.Fatal("IsPolicyDenial = false for stderr with EPERM, want true")
	}
}

// TestIsPolicyDenial_NormalZeroExit pins that a clean run is not a denial.
func TestIsPolicyDenial_NormalZeroExit(t *testing.T) {
	res := &Result{ExitCode: 0, Stderr: []byte("hello")}
	if IsPolicyDenial(res) {
		t.Fatal("IsPolicyDenial = true for a clean run, want false")
	}
}

// TestIsPolicyDenial_NormalNonZeroExit pins that a plain non-zero exit without
// any deny/EPERM marker is NOT a policy denial — a high exit code alone must
// not be misclassified as a deny (fail-closed, no false positives).
func TestIsPolicyDenial_NormalNonZeroExit(t *testing.T) {
	res := &Result{ExitCode: 3, Stderr: []byte("command not found")}
	if IsPolicyDenial(res) {
		t.Fatal("IsPolicyDenial = true for a normal non-zero exit, want false")
	}
}

// TestIsPolicyDenial_SignaledPins that a signal-terminated process without a
// deny marker is not a policy denial.
func TestIsPolicyDenial_Signaled(t *testing.T) {
	res := &Result{ExitCode: 137, Signaled: true, Stderr: nil}
	if IsPolicyDenial(res) {
		t.Fatal("IsPolicyDenial = true for a signal-killed child, want false")
	}
}

// TestIsPolicyDenial_Nil pins fail-closed: a nil Result is never a denial and
// never panics.
func TestIsPolicyDenial_Nil(t *testing.T) {
	if IsPolicyDenial(nil) {
		t.Fatal("IsPolicyDenial(nil) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// logTailer interface + ConsumeDenials — feed the classifier from a line source
// ---------------------------------------------------------------------------

// fakeTailer is the in-memory stand-in for the macOS unified-log tailer. It
// yields a fixed set of lines over a channel (or an error from Lines).
type fakeTailer struct {
	lines []string
	err   error
}

func (f *fakeTailer) Lines(ctx context.Context) (<-chan string, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range f.lines {
			select {
			case ch <- l:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// channelTailer is a tailer over a caller-owned channel, used to drive
// context-cancellation deterministically.
type channelTailer struct{ ch chan string }

func (c *channelTailer) Lines(_ context.Context) (<-chan string, error) {
	return c.ch, nil
}

// compile-time check that the fakes satisfy the boundary interface.
var (
	_ logTailer = (*fakeTailer)(nil)
	_ logTailer = (*channelTailer)(nil)
)

// TestConsumeDenials_EmitsOnlyForDenyLines pins the integration: deny lines
// from the tailer become sandbox_deny records bound to the session id, while
// allow/ordinary lines are skipped.
func TestConsumeDenials_EmitsOnlyForDenyLines(t *testing.T) {
	tl := &fakeTailer{lines: []string{
		"Sandbox: deny file-read /etc/passwd (com.apple.sandbox) pid 1",
		"some ordinary line",
		"Sandbox: allow file-read /x (com.apple.sandbox) pid 2",
		"sandboxd deny mach-lookup com.apple.test (com.apple.sandbox) pid 3",
	}}

	var got []SandboxDenyLog
	if err := ConsumeDenials(context.Background(), tl, "darwin-1", func(l SandboxDenyLog) {
		got = append(got, l)
	}); err != nil {
		t.Fatalf("ConsumeDenials: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("emitted %d records, want 2 (only the deny lines)", len(got))
	}
	for i, lg := range got {
		if lg.SessionID != "darwin-1" {
			t.Fatalf("record %d SessionID = %q, want darwin-1", i, lg.SessionID)
		}
	}
	if got[0].Operation != "file-read" || got[0].Pid != 1 {
		t.Fatalf("record 0 = %+v, want file-read / pid 1", got[0])
	}
	if got[1].Operation != "mach-lookup" || got[1].Pid != 3 {
		t.Fatalf("record 1 = %+v, want mach-lookup / pid 3", got[1])
	}
}

// TestConsumeDenials_TailerError propagates a Lines error.
func TestConsumeDenials_TailerError(t *testing.T) {
	tl := &fakeTailer{err: errors.New("log stream failed")}
	err := ConsumeDenials(context.Background(), tl, "darwin-1", func(SandboxDenyLog) {})
	if err == nil {
		t.Fatal("ConsumeDenials should propagate the tailer error")
	}
	if !strings.Contains(err.Error(), "log stream failed") {
		t.Fatalf("err = %v, want it to mention the tailer failure", err)
	}
}

// TestConsumeDenials_StopsOnContextCancel pins that ConsumeDenials exits on ctx
// cancellation, returning context.Canceled — no goroutine leak.
func TestConsumeDenials_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tl := &channelTailer{ch: make(chan string)}

	done := make(chan error, 1)
	go func() {
		done <- ConsumeDenials(ctx, tl, "darwin-1", func(SandboxDenyLog) {})
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ConsumeDenials did not stop on context cancellation")
	}
}
