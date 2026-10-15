package spec

import (
	"slices"
	"strings"
	"testing"
)

// TestDriverConstants pins the Driver enum string values used across the
// profile schema, the backend factory and the service (SPEC §7.12).
func TestDriverConstants(t *testing.T) {
	if DriverAuto != "auto" {
		t.Fatalf("DriverAuto = %q, want \"auto\"", DriverAuto)
	}
	if DriverDocker != "docker" {
		t.Fatalf("DriverDocker = %q, want \"docker\"", DriverDocker)
	}
	if DriverSeatbelt != "seatbelt" {
		t.Fatalf("DriverSeatbelt = %q, want \"seatbelt\"", DriverSeatbelt)
	}
}

// TestAllDrivers pins that AllDrivers enumerates every known driver exactly
// once, without duplicates.
func TestAllDrivers(t *testing.T) {
	got := AllDrivers()
	want := []Driver{DriverAuto, DriverDocker, DriverSeatbelt}
	if len(got) != len(want) {
		t.Fatalf("AllDrivers() = %v, want %d entries", got, len(want))
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("AllDrivers() = %v missing %q", got, w)
		}
	}
}

// TestIsValidDriver pins the fail-closed driver validation: the empty string is
// equivalent to auto (valid), each known driver is valid, and anything else is
// rejected.
func TestIsValidDriver(t *testing.T) {
	valid := []Driver{"", DriverAuto, DriverDocker, DriverSeatbelt}
	for _, d := range valid {
		if !IsValidDriver(d) {
			t.Errorf("IsValidDriver(%q) = false, want true", d)
		}
	}
	invalid := []Driver{"bwrap", "docker2", "SEATBELT", "seat belt", "Docker"}
	for _, d := range invalid {
		if IsValidDriver(d) {
			t.Errorf("IsValidDriver(%q) = true, want false", d)
		}
	}
}

// TestSandboxSpecValidate_DockerImageRequired pins that Image is mandatory ONLY
// for the docker driver.
func TestSandboxSpecValidate_DockerImageRequired(t *testing.T) {
	s := validSpec() // Driver = DriverDocker
	s.Image = " "
	if err := s.Validate(); err == nil {
		t.Fatal("docker spec with empty image must fail validation")
	} else if !strings.Contains(err.Error(), "Image is required") {
		t.Fatalf("err = %v, want Image-is-required", err)
	}
}

// TestSandboxSpecValidate_SeatbeltImageNotRequired pins that a seatbelt spec
// needs no image (there is no image on macOS; SBPL runs on host libs).
func TestSandboxSpecValidate_SeatbeltImageNotRequired(t *testing.T) {
	s := validSpec()
	s.Driver = DriverSeatbelt
	s.Image = ""
	if err := s.Validate(); err != nil {
		t.Fatalf("seatbelt spec with empty image should validate: %v", err)
	}
}

// TestSandboxSpecValidate_SeatbeltImageOptional pins that a seatbelt spec still
// validates when an image is (unusually) present.
func TestSandboxSpecValidate_SeatbeltImageOptional(t *testing.T) {
	s := validSpec()
	s.Driver = DriverSeatbelt
	s.Image = "docker.io/library/alpine:3.20"
	if err := s.Validate(); err != nil {
		t.Fatalf("seatbelt spec with an image should still validate: %v", err)
	}
}

// TestSandboxSpecValidate_SeatbeltSBPLNulRejected pins the fail-closed NUL guard
// on the seatbelt SBPL (SPEC §7.12): a NUL byte would terminate the SBPL string.
func TestSandboxSpecValidate_SeatbeltSBPLNulRejected(t *testing.T) {
	s := validSpec()
	s.Driver = DriverSeatbelt
	s.Image = ""
	s.Sandbox = Sandbox{SBPL: "(allow process-fork)\x00"}
	if err := s.Validate(); err == nil {
		t.Fatal("seatbelt SBPL containing NUL must fail validation")
	} else if !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("err = %v, want NUL mention", err)
	}
}

// TestSandboxSpecValidate_SeatbeltSBPLValid pins a well-formed seatbelt spec
// with a full Sandbox block validates.
func TestSandboxSpecValidate_SeatbeltSBPLValid(t *testing.T) {
	s := validSpec()
	s.Driver = DriverSeatbelt
	s.Image = ""
	s.Sandbox = Sandbox{
		SBPL:        "(import \"system.sb\")\n(allow process-fork)",
		MachLookups: []string{"com.apple.system.logger"},
		Sysctls:     []string{"kern.osversion"},
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid seatbelt spec should validate: %v", err)
	}
}

// TestSandboxSpecValidate_SeatbeltKeepsIsolationInvariants pins that the
// driver switch does NOT weaken the immutable Isolation hardening: even a
// seatbelt spec still fail-closes on the server-enforced invariants.
func TestSandboxSpecValidate_SeatbeltKeepsIsolationInvariants(t *testing.T) {
	s := validSpec()
	s.Driver = DriverSeatbelt
	s.Image = ""
	s.Isolation.PidsLimit = 0
	if err := s.Validate(); err == nil {
		t.Fatal("seatbelt spec with unbounded pids must fail validation")
	} else if !strings.Contains(err.Error(), "PidsLimit") {
		t.Fatalf("err = %v, want PidsLimit mention", err)
	}
}
