// Package profile defines the operator-authored, read-only sandbox profile
// model (Phase 7, SPEC §7.11). A profile is DRIVER-AGNOSTIC: it carries a
// required concrete `Driver` token (never auto/empty) plus an opaque
// `Options driver.Options` blob that the selected builder (from the driver
// registry) decodes into driver-specific fields. The profile itself never
// interprets the options content.
//
// A profile fully determines image, env, mounts, network, cgroups, user and
// sudo (via the builder that consumes its Options); the model references a
// profile by name on sandbox_create and can never weaken the server's immutable
// Isolation policy (§7.1).
package profile

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// Sentinel errors returned by the profile store (fail-closed, S04).
var (
	// ErrProfileNotFound reports that no operator-authored profile with the
	// requested name exists in the profiles directory.
	ErrProfileNotFound = errors.New("profile: not found")
	// ErrInvalidName reports that a profile LOOKUP name is not a valid profile
	// key. It is returned before any filesystem access, so the lookup key is
	// never used as a path.
	ErrInvalidName = errors.New("profile: invalid name")
)

// namePattern is the allowed profile-name character set. It mirrors the
// filesystem-safe key used by the store: lowercase alphanumerics plus . _ -
// (and excludes "." and "..", checked separately).
var namePattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

// driverNamePattern is the allowed concrete-driver token character set. It is
// the same charset as the profile name: lowercase alphanumerics plus . _ -
// (with "." and ".." excluded separately).
var driverNamePattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

// ValidName reports whether name is a valid profile key (the same rule as
// Validate's name check). It is used by the store both as the up-front lookup
// guard (returning ErrInvalidName) and to reject a traversal/invalid declared
// name when scanning the profiles directory.
func ValidName(name string) bool {
	if name == "." || name == ".." {
		return false
	}

	return namePattern.MatchString(name)
}

// CwdConfig configures the session working-directory mount. It is an alias of
// spec.CwdConfig (a leaf type shared with the driver registry's Context, so the
// profile can hand it to a builder without an import cycle).
type CwdConfig = spec.CwdConfig

// Params are the per-request, client-supplied parameters a builder merges into
// a spec. It is an alias of spec.Params (shared with driver.Context for the
// same cycle-free reason).
type Params = spec.Params

// Profile is a fully-populated operator-authored profile. It is READ-ONLY by
// construction: there are no create/update/delete profile tools (Phase 7).
// Driver-specific behaviour lives in the builder the Driver token selects; the
// profile carries only the driver-agnostic fields plus the opaque Options.
type Profile struct {
	Name        string
	Description string
	Driver      spec.Driver
	Cwd         CwdConfig
	// Shell is the shell (and optional whitespace-separated args) used to run
	// sandbox_exec commands. Empty means "bash" (defaulted by the builder).
	Shell string
	// Options is the opaque, driver-owned options blob the selected builder
	// decodes. The profile never interprets it.
	Options driver.Options
}

// Validate checks that the profile is well-formed at the driver-agnostic level
// (SPEC §7.11): a valid identity, a non-empty non-auto well-formed concrete
// driver token, and the cwd/shell invariants a profile must never weaken (S11).
//
// Driver-specific validation (image, mounts, network, cgroups, user, sudo,
// seatbelt SBPL) now lives in the builders and is covered by their own tests;
// an unknown but well-formed driver token is ACCEPTED here and fails later at
// USE time when the driver registry Get returns ErrUnknownDriver.
func (p *Profile) Validate() error {
	if err := p.validateIdentity(); err != nil {
		return err
	}
	if err := p.validateDriver(); err != nil {
		return err
	}

	return p.validateCommon()
}

// validateIdentity checks the profile name and description.
func (p *Profile) validateIdentity() error {
	if p.Name == "." || p.Name == ".." {
		return fmt.Errorf("profile: invalid name %q", p.Name)
	}
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("profile: invalid name %q (must match [a-z0-9._-])", p.Name)
	}
	if strings.TrimSpace(p.Description) == "" {
		return fmt.Errorf("profile: description is required")
	}

	return nil
}

// validateDriver checks the concrete driver token (SPEC §7.11). The driver is
// REQUIRED (there is no auto resolution at the profile layer), must not be
// auto, and must be a well-formed token matching the driver-name allowlist. An
// unknown-but-well-formed token is accepted (fail at USE time, not scan time).
func (p *Profile) validateDriver() error {
	d := p.Driver
	if strings.TrimSpace(string(d)) == "" {
		return fmt.Errorf("profile: driver is required")
	}
	if d == spec.DriverAuto {
		return fmt.Errorf("profile: driver %q is not allowed (auto is resolved at build time)", d)
	}
	if d == "." || d == ".." || !driverNamePattern.MatchString(string(d)) {
		return fmt.Errorf("profile: invalid driver %q (must match [a-z0-9._-])", d)
	}

	return nil
}

// validateCommon checks the profile-wide cwd and shell fields.
func (p *Profile) validateCommon() error {
	if p.Cwd.Mode != "" && p.Cwd.Mode != "ro" && p.Cwd.Mode != "rw" {
		return fmt.Errorf("profile: invalid cwd mode %q", p.Cwd.Mode)
	}
	if p.Cwd.Target != "" && !filepath.IsAbs(p.Cwd.Target) {
		return fmt.Errorf("profile: cwd target must be an absolute path")
	}
	if strings.ContainsAny(p.Shell, "\x00\n") {
		return fmt.Errorf("profile: shell must not contain NUL or newline")
	}

	return nil
}
