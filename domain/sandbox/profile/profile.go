// Package profile defines the operator-authored, read-only sandbox profile
// model (Phase 7, SPEC §7.11). A profile fully determines image, env, mounts,
// network, cgroups, user and sudo for a sandbox; the model references a profile
// by name on sandbox_create and can never weaken the server's immutable
// Isolation policy (§7.1).
package profile

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/distribution/reference"

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

// Mount is an operator-authored host-to-container mount. The source is gated
// by ALLOW_DIRS at the backend (S04); the profile is the trusted party for the
// target and read-only flag.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// CwdConfig configures the session working-directory mount: the container
// target (defaults to the client-supplied cwd 1:1) and the mount mode
// ("ro" read-only default, "rw" writable).
type CwdConfig struct {
	Target string
	Mode   string
}

// Network carries the network mode for the sandbox.
type Network struct {
	Mode spec.NetworkMode
}

// Cgroups bounds the sandbox resources. Every value is profile-owned and
// overrides the server policy default at BuildSpec time.
type Cgroups struct {
	NanoCPUs   int64
	Memory     int64
	MemorySwap int64
	PidsLimit  int64
	Ulimits    []spec.Ulimit
}

// DockerProfile carries the Docker-backend-specific fields of a profile. It is
// mutually exclusive with SeatbeltProfile: a profile declares exactly one
// driver block.
type DockerProfile struct {
	Image   string
	Env     map[string]string
	Mounts  []Mount
	Network Network
	Cgroups Cgroups
	User    *spec.UIDGID
	Sudo    spec.SudoPolicy
}

// SeatbeltProfile carries the darwin/sandbox-exec (Seatbelt) fields of a
// profile (SPEC §7.12). It is mutually exclusive with DockerProfile.
type SeatbeltProfile struct {
	SBPL        string
	MachLookups []string
	Sysctls     []string
}

// Profile is a fully-populated operator-authored profile. It is READ-ONLY by
// construction: there are no create/update/delete profile tools (Phase 7).
// The hybrid schema makes the driver explicit: a profile carries a Driver
// (auto/docker/seatbelt) and exactly one of a Docker or Seatbelt block.
type Profile struct {
	Name        string
	Description string
	Driver      spec.Driver
	Cwd         CwdConfig
	// Shell is the shell (and optional whitespace-separated args) used to run
	// sandbox_exec commands. Empty means "bash" (defaulted at BuildSpec).
	Shell string
	// Docker is the Docker-backend block; mutually exclusive with Seatbelt.
	Docker *DockerProfile
	// Seatbelt is the darwin/Seatbelt block; mutually exclusive with Docker.
	Seatbelt *SeatbeltProfile
}

// Params are the per-request, client-supplied parameters merged into the spec
// by BuildSpec. The profile owns everything else.
type Params struct {
	Cwd             string
	Secrets         []spec.SecretRef
	ClientSessionID string
}

// Validate checks that the profile is well-formed and that its driver/block
// combination is coherent. It fail-closes (with the "profile:" prefix) on every
// hardening-relevant invariant a profile must not weaken: an unknown driver,
// conflicting driver blocks, non-root user, bounded resources, known network
// mode, and control characters that would break the Shell or Seatbelt SBPL
// strings (S11/S04, SPEC §7.11/§7.12).
func (p *Profile) Validate() error {
	if err := p.validateIdentity(); err != nil {
		return err
	}
	if err := p.validateDriver(); err != nil {
		return err
	}
	if err := p.validateCommon(); err != nil {
		return err
	}
	if err := p.validateDocker(); err != nil {
		return err
	}

	return p.validateSeatbelt()
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

// validateDriver checks the driver/block coherence rules (SPEC §7.11/§7.12).
func (p *Profile) validateDriver() error {
	// Rule 1: unknown driver is rejected (empty string = auto).
	if !spec.IsValidDriver(p.Driver) {
		return fmt.Errorf("profile: invalid driver %q", p.Driver)
	}

	// Rule 2: both driver blocks set is incoherent.
	if p.Docker != nil && p.Seatbelt != nil {
		return fmt.Errorf("profile: cannot set both docker and seatbelt blocks")
	}

	// Rules 3 & 4: an explicit driver demands its matching block and forbids the
	// other. Rule 5: auto/empty demands exactly one block.
	switch p.Driver {
	case spec.DriverDocker:
		if p.Docker == nil {
			return fmt.Errorf("profile: docker driver requires a docker block")
		}
		if p.Seatbelt != nil {
			return fmt.Errorf("profile: docker driver must not set a seatbelt block")
		}
	case spec.DriverSeatbelt:
		if p.Seatbelt == nil {
			return fmt.Errorf("profile: seatbelt driver requires a seatbelt block")
		}
		if p.Docker != nil {
			return fmt.Errorf("profile: seatbelt driver must not set a docker block")
		}
	case spec.DriverAuto, "":
		if (p.Docker == nil) == (p.Seatbelt == nil) {
			return fmt.Errorf("profile: auto driver requires exactly one of docker or seatbelt")
		}
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

// validateDocker checks the Docker-block invariants (rule 7). Image validation
// is parse-only: any valid reference from any registry is accepted (Phase 7 —
// TRUSTED_REGISTRIES was removed); only empty or malformed references are
// rejected.
func (p *Profile) validateDocker() error {
	if p.Docker == nil {
		return nil
	}
	if _, err := reference.ParseNormalizedNamed(strings.TrimSpace(p.Docker.Image)); err != nil {
		return fmt.Errorf("profile: invalid image %q: %w", p.Docker.Image, err)
	}
	for _, m := range p.Docker.Mounts {
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Target) {
			return fmt.Errorf("profile: mount source and target must be absolute: %+v", m)
		}
	}
	if !spec.IsValidNetworkMode(p.Docker.Network.Mode) {
		return fmt.Errorf("profile: invalid network mode %q", p.Docker.Network.Mode)
	}
	cg := p.Docker.Cgroups
	if cg.NanoCPUs <= 0 {
		return fmt.Errorf("profile: NanoCPUs must be bounded (> 0)")
	}
	if cg.Memory <= 0 {
		return fmt.Errorf("profile: Memory must be bounded (> 0)")
	}
	if cg.PidsLimit <= 0 {
		return fmt.Errorf("profile: PidsLimit must be bounded (> 0)")
	}
	if cg.MemorySwap < cg.Memory {
		return fmt.Errorf("profile: MemorySwap must be >= Memory")
	}
	if p.Docker.User != nil && p.Docker.User.UID == 0 {
		return fmt.Errorf("profile: user must be non-root")
	}
	if p.Docker.Sudo != spec.SudoNever && p.Docker.Sudo != spec.SudoRequire {
		return fmt.Errorf("profile: invalid sudo policy %q", p.Docker.Sudo)
	}

	return nil
}

// validateSeatbelt checks the Seatbelt-block invariants (rule 8): control
// characters would break the SBPL / mach / sysctl strings (SPEC §7.12).
func (p *Profile) validateSeatbelt() error {
	if p.Seatbelt == nil {
		return nil
	}
	if strings.ContainsRune(p.Seatbelt.SBPL, '\x00') {
		return fmt.Errorf("profile: seatbelt SBPL must not contain NUL")
	}
	for _, m := range p.Seatbelt.MachLookups {
		if strings.ContainsAny(m, "\x00\n") {
			return fmt.Errorf("profile: mach_lookup must not contain NUL or newline")
		}
	}
	for _, s := range p.Seatbelt.Sysctls {
		if strings.ContainsAny(s, "\x00\n") {
			return fmt.Errorf("profile: sysctl must not contain NUL or newline")
		}
	}

	return nil
}

// BuildSpec merges the operator's intent with the server's immutable Isolation
// policy `pol` into a SandboxSpec. The hybrid schema resolves the driver first:
// auto (or empty) resolves to the single present block's driver (docker →
// DriverDocker, seatbelt → DriverSeatbelt); an explicit driver is preserved.
// The resulting spec's Driver is NEVER auto (SPEC §7.12).
//
// Two invariants are locked:
//
//   - profile-owned fields (NanoCPUs/Memory/MemorySwap/PidsLimit/Ulimits/
//     NetworkMode) OVERRIDE `pol` (docker branch only);
//   - hardening fields (ReadOnlyRootfs, CapDrop, NoNewPrivileges, Seccomp,
//     User, TmpfsWorkdir, AutoRemove) ALWAYS come from `pol` and are NEVER
//     overridden by the profile — a profile can never weaken the sandbox.
//
// The session cwd is always the first mount, 1:1 at the same absolute path
// (or at the profile-declared cwd target), read-only unless the profile sets
// cwd.mode=rw. Secrets and the client session id come from `params`.
func (p *Profile) BuildSpec(pol spec.Isolation, params Params) spec.SandboxSpec {
	driver := p.resolveDriver()
	cwdTarget := p.cwdTarget(params.Cwd)
	mounts := []spec.Mount{{
		Source:   params.Cwd,
		Target:   cwdTarget,
		ReadOnly: p.Cwd.Mode != "rw",
	}}

	sp := spec.SandboxSpec{
		Driver:          driver,
		WorkingDir:      cwdTarget,
		Mounts:          mounts,
		Shell:           defaultShell(p.Shell),
		Secrets:         params.Secrets,
		ReadOnlyRoot:    true,
		Isolation:       pol,
		ClientSessionID: params.ClientSessionID,
	}

	if driver == spec.DriverDocker {
		p.applyDocker(&sp, pol)
	} else {
		p.applySeatbelt(&sp)
	}

	return sp
}

// resolveDriver returns the concrete driver for this profile: auto/empty
// resolves to the single present block's driver; an explicit driver is kept.
func (p *Profile) resolveDriver() spec.Driver {
	if p.Driver != "" && p.Driver != spec.DriverAuto {
		return p.Driver
	}
	if p.Docker != nil {
		return spec.DriverDocker
	}

	return spec.DriverSeatbelt
}

// cwdTarget returns the container target for the session cwd mount: the
// profile-declared target, or the client path 1:1 when unset.
func (p *Profile) cwdTarget(client string) string {
	if p.Cwd.Target != "" {
		return p.Cwd.Target
	}

	return client
}

// defaultShell returns the shell to build the sandbox_exec wrapper, defaulting
// to "bash" when unset (SPEC §7.10).
func defaultShell(shell string) string {
	if strings.TrimSpace(shell) == "" {
		return "bash"
	}

	return shell
}

// applyDocker fills the Docker branch of the spec: profile-owned resources,
// network, image, env, mounts (appended after the cwd), user and sudo. The
// immutable Isolation hardening always comes from `pol`; only resource/network
// fields are overridden. The Sandbox block stays zero (Seatbelt knobs ignored).
func (p *Profile) applyDocker(sp *spec.SandboxSpec, pol spec.Isolation) {
	d := p.Docker

	iso := sp.Isolation
	iso.NanoCPUs = d.Cgroups.NanoCPUs
	iso.Memory = d.Cgroups.Memory
	iso.MemorySwap = d.Cgroups.MemorySwap
	iso.PidsLimit = d.Cgroups.PidsLimit
	iso.Ulimits = d.Cgroups.Ulimits
	iso.NetworkMode = d.Network.Mode

	// User: the enforced non-root user from `pol` is never weakened. A profile
	// may substitute a different non-root user; a nil or root profile user
	// falls back to the policy user.
	uidgid := pol.User
	if d.User != nil && d.User.UID != 0 {
		uidgid = *d.User
	}

	mounts := sp.Mounts
	for _, m := range d.Mounts {
		mounts = append(mounts, spec.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}

	sp.Image = d.Image
	sp.Env = d.Env
	sp.Mounts = mounts
	sp.Resources = spec.Resources{
		NanoCPUs:   iso.NanoCPUs,
		Memory:     iso.Memory,
		MemorySwap: iso.MemorySwap,
		PidsLimit:  iso.PidsLimit,
		Ulimits:    iso.Ulimits,
	}
	sp.Network = spec.Network{Mode: iso.NetworkMode}
	sp.UIDGID = uidgid
	sp.Sudo = d.Sudo
	sp.Isolation = iso
}

// applySeatbelt fills the Seatbelt branch of the spec: no image; the Sandbox
// block carried from the Seatbelt block; Sudo=SudoNever; UIDGID/Resources from
// the server policy (there is no profile resource block); only the auto cwd
// mount. Isolation hardening stays from `pol`.
func (p *Profile) applySeatbelt(sp *spec.SandboxSpec) {
	seatbelt := p.Seatbelt

	sp.Sandbox = spec.Sandbox{
		SBPL:        seatbelt.SBPL,
		MachLookups: seatbelt.MachLookups,
		Sysctls:     seatbelt.Sysctls,
	}
	sp.UIDGID = sp.Isolation.User
	sp.Sudo = spec.SudoNever
	sp.Resources = spec.Resources{
		NanoCPUs:   sp.Isolation.NanoCPUs,
		Memory:     sp.Isolation.Memory,
		MemorySwap: sp.Isolation.MemorySwap,
		PidsLimit:  sp.Isolation.PidsLimit,
		Ulimits:    sp.Isolation.Ulimits,
	}
	sp.Network = spec.Network{Mode: sp.Isolation.NetworkMode}
}
