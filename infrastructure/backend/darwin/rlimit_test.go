package darwin

// Tests for the setrlimit mapping from spec.Resources.Ulimits to []Rlimit
// (SPEC §7.12: only the RLIMIT-* mappable names are enforced on the darwin
// backend; NanoCPUs/Memory/PidsLimit have no cgroups on macOS and are declared
// unsupported — they can never reach the Rlimits slice). The mapping is pure
// and portable: it uses the stdlib syscall.RLIMIT_* constants, which exist on
// every Unix (darwin and Linux), so the tests run in CI on any platform.

import (
	"reflect"
	"syscall"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

func TestToRlimits_NilOrEmpty(t *testing.T) {
	if got := toRlimits(nil); got != nil {
		t.Fatalf("toRlimits(nil) = %v, want nil", got)
	}
	if got := toRlimits([]spec.Ulimit{}); got != nil {
		t.Fatalf("toRlimits(empty) = %v, want nil", got)
	}
}

func TestToRlimits_MapsKnownNames(t *testing.T) {
	in := []spec.Ulimit{
		{Name: "nofile", Soft: 1024, Hard: 2048},
		{Name: "cpu", Soft: 1, Hard: 2},
		{Name: "as", Soft: 3, Hard: 4},
		{Name: "nproc", Soft: 5, Hard: 6},
		{Name: "fsize", Soft: 7, Hard: 8},
		{Name: "stack", Soft: 9, Hard: 10},
		{Name: "core", Soft: 11, Hard: 12},
	}
	want := []Rlimit{
		{Resource: int(syscall.RLIMIT_NOFILE), Soft: 1024, Hard: 2048},
		{Resource: int(syscall.RLIMIT_CPU), Soft: 1, Hard: 2},
		{Resource: int(syscall.RLIMIT_AS), Soft: 3, Hard: 4},
		{Resource: int(rlimitNPROC), Soft: 5, Hard: 6},
		{Resource: int(syscall.RLIMIT_FSIZE), Soft: 7, Hard: 8},
		{Resource: int(syscall.RLIMIT_STACK), Soft: 9, Hard: 10},
		{Resource: int(syscall.RLIMIT_CORE), Soft: 11, Hard: 12},
	}
	got := toRlimits(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("toRlimits = %+v\nwant %+v", got, want)
	}
}

// TestToRlimits_SkipsUnsupported pins that any name outside the seven mappable
// ones is dropped (honestly not enforced — never silently approximated).
func TestToRlimits_SkipsUnsupported(t *testing.T) {
	in := []spec.Ulimit{
		{Name: "memlock", Soft: 1, Hard: 2},
		{Name: "rtprio", Soft: 3, Hard: 4},
		{Name: "nofile", Soft: 100, Hard: 200},
	}
	got := toRlimits(in)
	if len(got) != 1 {
		t.Fatalf("toRlimits len = %d, want 1 (only mappable kept): %+v", len(got), got)
	}
	if got[0].Resource != int(syscall.RLIMIT_NOFILE) {
		t.Fatalf("Resource = %d, want RLIMIT_NOFILE(%d)", got[0].Resource, syscall.RLIMIT_NOFILE)
	}
	if got[0].Soft != 100 || got[0].Hard != 200 {
		t.Fatalf("soft/hard = %d/%d, want 100/200", got[0].Soft, got[0].Hard)
	}
}

// TestToRlimits_KeepsOrder pins that mapped entries preserve the input order
// (deterministic argv construction).
func TestToRlimits_KeepsOrder(t *testing.T) {
	in := []spec.Ulimit{
		{Name: "core", Soft: 1, Hard: 1},
		{Name: "nofile", Soft: 2, Hard: 2},
		{Name: "stack", Soft: 3, Hard: 3},
	}
	got := toRlimits(in)
	wantResources := []int{
		int(syscall.RLIMIT_CORE),
		int(syscall.RLIMIT_NOFILE),
		int(syscall.RLIMIT_STACK),
	}
	for i, r := range got {
		if r.Resource != wantResources[i] {
			t.Fatalf("entry %d resource = %d, want %d", i, r.Resource, wantResources[i])
		}
	}
}
