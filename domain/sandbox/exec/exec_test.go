package exec

import "testing"

// Result.PolicyDenied is the field added by the darwin backend's Exec to
// distinguish a sandbox policy denial from a plain non-zero exit (SPEC §7.12).
// The behaviour that SETS it is covered by the darwin backend tests
// (exec_policy_test.go); this file pins the field's presence and zero-value
// contract on the shared domain type, so the API shape the backends rely on is
// explicit (TDD, T01).
func TestResult_PolicyDeniedZeroValue(t *testing.T) {
	var r Result
	if r.PolicyDenied {
		t.Fatal("zero-value Result.PolicyDenied = true, want false")
	}
}

func TestResult_PolicyDeniedCarriesValue(t *testing.T) {
	r := Result{ExitCode: 1, PolicyDenied: true}
	if !r.PolicyDenied {
		t.Fatal("Result.PolicyDenied not carried through construction")
	}
	if r.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1", r.ExitCode)
	}
}
