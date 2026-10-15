package darwin

// Additional coverage for the portable policy-denial detection (SPEC §7.12).
// These tests pin edge cases of deny.go that the primary deny_test.go suite
// does not exercise directly, complementing rather than replacing it.

import "testing"

// TestClassifyDeny_MalformedMarkerWithoutOperation pins that a line carrying
// both the deny word and the sandbox subsystem marker but no operation (no
// clause after "deny") is still fail-closed: it is not classified as a deny.
func TestClassifyDeny_MalformedMarkerWithoutOperation(t *testing.T) {
	line := "com.apple.sandbox deny"
	if _, ok := ClassifyDeny(line); ok {
		t.Fatal("ClassifyDeny = true for a marker+deny line with no operation, want false")
	}
}

// TestClassifyDeny_SandboxPrefixMarker pins that the "Sandbox:" prefix alone
// (without the "com.apple.sandbox" suffix) is recognized as a sandbox marker
// and classified as a deny when the deny word is present.
func TestClassifyDeny_SandboxPrefixMarker(t *testing.T) {
	line := "Sandbox: deny file-read /etc/passwd"
	ev, ok := ClassifyDeny(line)
	if !ok {
		t.Fatal("ClassifyDeny = false for a deny with the Sandbox: prefix marker")
	}
	if ev.Operation != "file-read" || ev.Target != "/etc/passwd" {
		t.Fatalf("Operation/Target = %q/%q, want file-read//etc/passwd", ev.Operation, ev.Target)
	}
	if ev.Pid != 0 {
		t.Fatalf("Pid = %d, want 0 (no pid in line)", ev.Pid)
	}
}

// TestClassifyDeny_MessageEmptyTarget pins that a deny with no target still
// synthesizes a non-empty summary built from the operation.
func TestClassifyDeny_MessageEmptyTarget(t *testing.T) {
	ev, ok := ClassifyDeny("sandboxd Sandbox: deny process-fork (com.apple.sandbox) pid 5")
	if !ok {
		t.Fatal("ClassifyDeny = false for a process-fork deny")
	}
	if ev.Message != "deny process-fork" {
		t.Fatalf("Message = %q, want %q", ev.Message, "deny process-fork")
	}
}
