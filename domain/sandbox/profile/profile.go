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

// Profile is a fully-populated operator-authored profile. It is READ-ONLY by
// construction: there are no create/update/delete profile tools (Phase 7).
type Profile struct {
	Name        string
	Description string
	Image       string
	Env         map[string]string
	Mounts      []Mount
	Cwd         CwdConfig
	Network     Network
	Cgroups     Cgroups
	User        *spec.UIDGID
	Sudo        spec.SudoPolicy
}

// Params are the per-request, client-supplied parameters merged into the spec
// by BuildSpec. The profile owns everything else.
type Params struct {
	Cwd             string
	Secrets         []spec.SecretRef
	ClientSessionID string
}

// Validate checks that the profile is well-formed. Image validation is
// parse-only: any valid reference from any registry is accepted (Phase 7 —
// TRUSTED_REGISTRIES was removed); only empty or malformed references are
// rejected. It fail-closes on every hardening-relevant invariant a profile
// must not weaken: non-root user, bounded resources, known network mode.
func (p *Profile) Validate() error {
	if p.Name == "." || p.Name == ".." {
		return fmt.Errorf("profile: invalid name %q", p.Name)
	}
	if !namePattern.MatchString(p.Name) {
		return fmt.Errorf("profile: invalid name %q (must match [a-z0-9._-])", p.Name)
	}
	if strings.TrimSpace(p.Description) == "" {
		return fmt.Errorf("profile: description is required")
	}
	if _, err := reference.ParseNormalizedNamed(strings.TrimSpace(p.Image)); err != nil {
		return fmt.Errorf("profile: invalid image %q: %w", p.Image, err)
	}
	for _, m := range p.Mounts {
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Target) {
			return fmt.Errorf("profile: mount source and target must be absolute: %+v", m)
		}
	}
	if p.Cwd.Mode != "" && p.Cwd.Mode != "ro" && p.Cwd.Mode != "rw" {
		return fmt.Errorf("profile: invalid cwd mode %q", p.Cwd.Mode)
	}
	if p.Cwd.Target != "" && !filepath.IsAbs(p.Cwd.Target) {
		return fmt.Errorf("profile: cwd target must be an absolute path")
	}
	if !spec.IsValidNetworkMode(p.Network.Mode) {
		return fmt.Errorf("profile: invalid network mode %q", p.Network.Mode)
	}
	cg := p.Cgroups
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
	if p.User != nil && p.User.UID == 0 {
		return fmt.Errorf("profile: user must be non-root")
	}
	if p.Sudo != spec.SudoNever && p.Sudo != spec.SudoRequire {
		return fmt.Errorf("profile: invalid sudo policy %q", p.Sudo)
	}
	return nil
}

// BuildSpec merges the operator's intent (image, env, mounts, network, cgroups,
// user, sudo) with the server's immutable Isolation policy `pol` into a
// SandboxSpec. Two invariants are locked:
//
//   - profile-owned fields (NanoCPUs/Memory/MemorySwap/PidsLimit/Ulimits/
//     NetworkMode) OVERRIDE `pol`;
//   - hardening fields (ReadOnlyRootfs, CapDrop, NoNewPrivileges, Seccomp,
//     User, TmpfsWorkdir, AutoRemove) ALWAYS come from `pol` and are NEVER
//     overridden by the profile — a profile can never weaken the sandbox.
//
// The session cwd is always the first mount, 1:1 at the same absolute path
// (or at the profile-declared cwd target), read-only unless the profile sets
// cwd.mode=rw.
func (p *Profile) BuildSpec(pol spec.Isolation, params Params) spec.SandboxSpec {
	// Start from the server policy; only profile-owned resource/network fields
	// may override it.
	iso := pol
	iso.NanoCPUs = p.Cgroups.NanoCPUs
	iso.Memory = p.Cgroups.Memory
	iso.MemorySwap = p.Cgroups.MemorySwap
	iso.PidsLimit = p.Cgroups.PidsLimit
	iso.Ulimits = p.Cgroups.Ulimits
	iso.NetworkMode = p.Network.Mode

	// User: the enforced non-root user from `pol` is never weakened. A profile
	// may substitute a different non-root user; a nil or root profile user
	// falls back to the policy user.
	uidgid := pol.User
	if p.User != nil && p.User.UID != 0 {
		uidgid = *p.User
	}

	// The cwd mount always comes first. Source is the client path 1:1; the
	// container target is the profile-declared cwd target (or the source).
	cwdTarget := p.Cwd.Target
	if cwdTarget == "" {
		cwdTarget = params.Cwd
	}
	mounts := []spec.Mount{{
		Source:   params.Cwd,
		Target:   cwdTarget,
		ReadOnly: p.Cwd.Mode != "rw",
	}}
	for _, m := range p.Mounts {
		mounts = append(mounts, spec.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}

	return spec.SandboxSpec{
		Image:           p.Image,
		WorkingDir:      cwdTarget,
		Env:             p.Env,
		Mounts:          mounts,
		Resources:       spec.Resources{NanoCPUs: iso.NanoCPUs, Memory: iso.Memory, MemorySwap: iso.MemorySwap, PidsLimit: iso.PidsLimit, Ulimits: iso.Ulimits},
		Network:         spec.Network{Mode: iso.NetworkMode},
		UIDGID:          uidgid,
		Sudo:            p.Sudo,
		Secrets:         params.Secrets,
		ReadOnlyRoot:    true,
		Isolation:       iso,
		ClientSessionID: params.ClientSessionID,
	}
}
