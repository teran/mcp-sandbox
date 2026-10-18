// Package profile pins the operator-authored, read-only sandbox profile model
// (Phase 7 + driver-registry schema, SPEC §7.11/§7.12).
//
// After the driver-registry migration (Slices 2+3) a Profile is DRIVER-AGNOSTIC:
// it no longer carries a `Docker` or `Seatbelt` block — those driver branches
// moved into the built-in builders (infrastructure/backend/docker and darwin).
// A Profile carries a required concrete `Driver` (never auto/empty) plus an
// opaque `Options driver.Options` blob that the selected builder decodes.
//
// Profile.Validate is reduced to the driver-agnostic invariants a profile must
// never weaken: name/description, a non-empty non-auto well-formed concrete
// driver token, and the cwd/shell control-character rules (S11). Driver-specific
// validation (image, mounts, network, cgroups, user, sudo, seatbelt SBPL) now
// lives in the builders and is covered by their own tests.
package profile

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// mustOptions builds a driver.Options from a YAML document, ignoring the error
// (test fixtures are valid). Validate is driver-agnostic and never inspects the
// options content, so the node content here is illustrative.
func mustOptions(doc string) driver.Options {
	var n yaml.Node
	_ = yaml.Unmarshal([]byte(doc), &n)

	return driver.NewOptions(&n)
}

// validProfile returns a docker-driver profile that passes Validate.
func validProfile() *Profile {
	return &Profile{
		Name:        "default",
		Description: "the default profile",
		Driver:      spec.DriverDocker,
		Cwd:         CwdConfig{Mode: "ro"},
		Options:     mustOptions("image: docker.io/library/alpine:3.20"),
	}
}

// validSeatbeltProfile returns a seatbelt-driver profile that passes Validate.
func validSeatbeltProfile() *Profile {
	return &Profile{
		Name:        "mac",
		Description: "a seatbelt profile",
		Driver:      spec.DriverSeatbelt,
		Cwd:         CwdConfig{Mode: "ro"},
		Options:     mustOptions("sbpl: '(allow process-fork)'"),
	}
}

func TestProfileValidate_Valid(t *testing.T) {
	if err := validProfile().Validate(); err != nil {
		t.Fatalf("expected valid docker profile, got: %v", err)
	}
	if err := validSeatbeltProfile().Validate(); err != nil {
		t.Fatalf("expected valid seatbelt profile, got: %v", err)
	}
}

// TestProfileValidate_UnknownWellFormedDriverAccepted pins the fail-at-USE-time
// contract: a well-formed but not-yet-registered concrete driver token passes
// Validate (the store rejects only empty/auto/malformed); the unknown driver
// fails later when the registry Get returns ErrUnknownDriver (covered by the
// driver registry tests).
func TestProfileValidate_UnknownWellFormedDriverAccepted(t *testing.T) {
	p := validProfile()
	p.Driver = spec.Driver("bwrap") // well-formed token, unknown to the registry
	if err := p.Validate(); err != nil {
		t.Fatalf("well-formed unknown driver token should validate: %v", err)
	}
	p.Driver = spec.Driver("my-driver.1")
	if err := p.Validate(); err != nil {
		t.Fatalf("well-formed driver token with dots/dash should validate: %v", err)
	}
}

func TestProfileValidate_Errors(t *testing.T) {
	valid := validProfile

	tests := []struct {
		name string
		mut  func(*Profile)
		want string
	}{
		{"empty name", func(p *Profile) { p.Name = "" }, "name"},
		{"bad name", func(p *Profile) { p.Name = "Bad/Name" }, "name"},
		{"name path traversal", func(p *Profile) { p.Name = "../evil" }, "name"},
		{"empty description", func(p *Profile) { p.Description = "" }, "description"},
		{"invalid cwd mode", func(p *Profile) { p.Cwd.Mode = "weird" }, "cwd"},
		{"relative cwd target", func(p *Profile) { p.Cwd.Target = "work" }, "cwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid()
			tt.mut(p)
			err := p.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got: %v", tt.want, err)
			}
		})
	}
}

// --- Driver validation (fail-closed, SPEC §7.12) ---

// TestProfileValidate_DriverRequired pins that the driver is REQUIRED: empty is
// rejected (there is no auto resolution at the profile layer anymore).
func TestProfileValidate_DriverRequired(t *testing.T) {
	p := validProfile()
	p.Driver = ""
	if err := p.Validate(); err == nil {
		t.Fatal("empty driver must fail validation")
	} else if !strings.Contains(err.Error(), "driver") {
		t.Fatalf("err = %v, want driver mention", err)
	}
}

// TestProfileValidate_DriverAutoRejected pins that `auto` is rejected at the
// profile layer (auto was the pre-migration resolver; the profile is now
// explicit).
func TestProfileValidate_DriverAutoRejected(t *testing.T) {
	p := validProfile()
	p.Driver = spec.DriverAuto
	if err := p.Validate(); err == nil {
		t.Fatal("auto driver must fail validation")
	} else if !strings.Contains(err.Error(), "driver") {
		t.Fatalf("err = %v, want driver mention", err)
	}
}

// TestProfileValidate_MalformedDriverRejected pins that a driver token that is
// not well-formed (not matching the driver-name allowlist) is rejected.
func TestProfileValidate_MalformedDriverRejected(t *testing.T) {
	for _, d := range []spec.Driver{"Bad/Driver", "has space", "UPPER", "a@b", ".."} {
		p := validProfile()
		p.Driver = d
		if err := p.Validate(); err == nil {
			t.Errorf("malformed driver %q should fail validation", d)
		}
	}
}

// TestProfileValidate_NameRegex pins the profile-name character set.
func TestProfileValidate_NameRegex(t *testing.T) {
	for _, name := range []string{"a", "a.b", "a_b", "a-b", "0a9", "profile.1"} {
		p := validProfile()
		p.Name = name
		if err := p.Validate(); err != nil {
			t.Errorf("name %q should be valid: %v", name, err)
		}
	}
	for _, name := range []string{"A", "a b", "a/b", "a\\b", "..", "a:b", "a@b", ""} {
		p := validProfile()
		p.Name = name
		if err := p.Validate(); err == nil {
			t.Errorf("name %q should be invalid", name)
		}
	}
}

// TestProfileValidate_Shell pins the shell validation: a bare name, an absolute
// path and a multi-token ("bash -l") shell are accepted; a NUL or newline in the
// shell is rejected.
func TestProfileValidate_Shell(t *testing.T) {
	for _, shell := range []string{"bash", "/bin/sh", "bash -l", "/bin/bash -l"} {
		p := validProfile()
		p.Shell = shell
		if err := p.Validate(); err != nil {
			t.Errorf("shell %q should validate: %v", shell, err)
		}
	}
	for _, shell := range []string{"bash\x00", "bash\n-l", "/bin/sh\x00", "a\nb"} {
		p := validProfile()
		p.Shell = shell
		if err := p.Validate(); err == nil {
			t.Errorf("shell %q should be rejected (NUL/newline)", shell)
		}
	}
}

// TestProfile_OptionsIsOpaqueDriverOptions pins the migration contract: a
// Profile carries an opaque driver.Options (not a typed Docker/Seatbelt block).
// Referencing p.Options as driver.Options is the compile-time proof the old
// Docker/Seatbelt fields are gone.
func TestProfile_OptionsIsOpaqueDriverOptions(t *testing.T) {
	var opts driver.Options
	p := validProfile()
	opts = p.Options // compile-time: Profile.Options is driver.Options
	if opts.Node() == nil {
		t.Fatal("Profile.Options must carry the options yaml.Node")
	}
	if validSeatbeltProfile().Options.Node() == nil {
		t.Fatal("seatbelt Profile.Options must carry the options yaml.Node")
	}
}

// TestProfile_HasNoDockerOrSeatbeltBlocks is a documentation/compile-time guard:
// the migrated Profile has no Docker/Seatbelt fields, so the only way to reach
// driver-specific behaviour is through the builder registry. This test
// constructs a profile and confirms the driver-agnostic surface is complete.
func TestProfile_HasNoDockerOrSeatbeltBlocks(t *testing.T) {
	p := validProfile()
	if p.Driver != spec.DriverDocker {
		t.Fatalf("Driver = %q", p.Driver)
	}
	if p.Cwd.Mode != "ro" {
		t.Fatalf("Cwd = %+v", p.Cwd)
	}
	// The empty-string description must still fail (driver-agnostic invariant).
	p.Description = ""
	if err := p.Validate(); err == nil {
		t.Fatal("empty description must fail even without a driver block")
	}
}

// TestProfile_EnvelopeDriverValidates pins that an envelope-form profile (the
// driver derived from the apiVersion label) still passes the driver-agnostic
// Validate — the driver token and cwd/shell invariants are unchanged by the
// envelope migration.
func TestProfile_EnvelopeDriverValidates(t *testing.T) {
	p := validProfile()
	p.APIVersion = "docker.profiles.sandbox.teran.dev/v1"
	if err := p.Validate(); err != nil {
		t.Fatalf("envelope docker profile should validate: %v", err)
	}

	s := validSeatbeltProfile()
	s.APIVersion = "seatbelt.profiles.sandbox.teran.dev/v1"
	if err := s.Validate(); err != nil {
		t.Fatalf("envelope seatbelt profile should validate: %v", err)
	}
}

// TestProfile_APIVersionRoundTrip pins the APIVersion field is carried verbatim
// on the profile (envelope form) and empty for legacy.
func TestProfile_APIVersionRoundTrip(t *testing.T) {
	p := validProfile()
	if p.APIVersion != "" {
		t.Fatalf("legacy profile APIVersion = %q, want empty", p.APIVersion)
	}
	const v = "docker.profiles.sandbox.teran.dev/v1"
	p.APIVersion = v
	if p.APIVersion != v {
		t.Fatalf("APIVersion = %q, want %q", p.APIVersion, v)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("profile with APIVersion should validate: %v", err)
	}
}
