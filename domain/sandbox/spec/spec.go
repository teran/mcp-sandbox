// Package spec defines the typed SandboxSpec and the immutable Isolation
// policy that the server enforces on every container create (§7.1).
package spec

import (
	"fmt"
	"slices"
	"strings"
)

// Driver identifies the backend driver a sandbox runs on (SPEC §7.12). The
// profile schema and the backend registry both carry a Driver; "auto" (and the
// empty string, its alias) defers the choice to backend selection.
type Driver string

const (
	// DriverAuto defers the backend choice to selection: any available backend
	// covering the required capabilities. An empty Driver is treated as auto.
	DriverAuto Driver = "auto"
	// DriverDocker selects the Docker backend.
	DriverDocker Driver = "docker"
	// DriverSeatbelt selects the macOS sandbox-exec (Seatbelt) backend.
	DriverSeatbelt Driver = "seatbelt"
)

// AllDrivers returns every known driver (including auto).
func AllDrivers() []Driver {
	return []Driver{DriverAuto, DriverDocker, DriverSeatbelt}
}

// IsValidDriver reports whether d is a known driver. The empty string is
// treated as auto and is therefore valid (fail-closed rejects anything else).
func IsValidDriver(d Driver) bool {
	return d == "" || d == DriverAuto || d == DriverDocker || d == DriverSeatbelt
}

// Sandbox carries the darwin/Seatbelt-specific knobs of a sandbox (SPEC §7.12).
// It is ignored by the Docker backend, which emits a zero Sandbox block.
type Sandbox struct {
	// SBPL is the operator Seatbelt (SBPL) template merged after the
	// server-hardening operations. NUL is rejected (it would terminate the
	// SBPL string).
	SBPL string
	// MachLookups are extra (allow mach-lookup ...) services.
	MachLookups []string
	// Sysctls are extra (allow sysctl-read ...) keys.
	Sysctls []string
}

// NetworkMode describes the network isolation of a sandbox.
type NetworkMode string

const (
	// NetworkBridge attaches the sandbox to Docker's default bridged network
	// (safe default).
	NetworkBridge NetworkMode = "bridge"
	// NetworkNone disables networking entirely.
	NetworkNone NetworkMode = "none"
	// NetworkHost exposes the host network namespace. It is a valid known mode
	// (Phase 7); whether an operator offers it is a profile decision, not a
	// server-side gate.
	NetworkHost NetworkMode = "host"
)

// AllNetworkModes returns every network mode the server knows about.
func AllNetworkModes() []NetworkMode {
	return []NetworkMode{NetworkNone, NetworkBridge, NetworkHost}
}

// IsValidNetworkMode reports whether m is one of the known network modes. A
// spec requesting any known mode (host included) validates; the fail-closed
// host opt-in is enforced by the operator-authored profile, not here (Phase 7).
func IsValidNetworkMode(m NetworkMode) bool {
	return slices.Contains(AllNetworkModes(), m)
}

// SudoPolicy controls whether commands run inside the sandbox (via
// sandbox_exec) may use privilege escalation (via sudo).
type SudoPolicy string

const (
	// SudoNever forbids any privilege escalation inside the sandbox.
	SudoNever SudoPolicy = "never"
	// SudoRequire runs sandbox_exec commands under sudo.
	SudoRequire SudoPolicy = "require"
)

// UIDGID is the user identity inside the sandbox. A non-root user is enforced
// by the immutable Isolation policy.
type UIDGID struct {
	UID uint32
	GID uint32
}

// Ulimit is a single resource limit (soft/hard) applied to the sandbox.
type Ulimit struct {
	Name string
	Soft uint64
	Hard uint64
}

// Resources bounds the resource usage of a sandbox.
type Resources struct {
	NanoCPUs   int64
	Memory     int64
	MemorySwap int64
	PidsLimit  int64
	Ulimits    []Ulimit
}

// Network describes the network configuration of a sandbox.
type Network struct {
	Mode NetworkMode
}

// Mount is an immutable host-to-container mount. Mounts cannot be changed
// after the sandbox is created.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// CwdConfig configures the session working-directory mount: the container
// target (defaults to the client-supplied cwd 1:1) and the mount mode ("ro"
// read-only default, "rw" writable). It lives in the leaf `spec` package so
// both the driver registry (driver.Context.Cwd) and the profile model can
// reference it without an import cycle.
type CwdConfig struct {
	Target string
	Mode   string
}

// Params are the per-request, client-supplied parameters merged into a spec by
// a driver builder. The profile/operator owns everything else. It lives in the
// leaf `spec` package so the driver registry can reference it without importing
// the profile package (driver↔profile would be an import cycle).
type Params struct {
	Cwd             string
	Secrets         []SecretRef
	ClientSessionID string
}

// SecretRef references a secret that is delivered into the sandbox as a tmpfs
// file with mode 0600. Value is annotated secret:true and is redacted from all
// output and logs (S02).
type SecretRef struct {
	Name  string `json:"name"`
	Value string `json:"value" secret:"true"`
}

// Isolation is the immutable hardening block (§7.1). It is populated by the
// server policy and applied on every container create; a caller can never
// weaken any of its values.
type Isolation struct {
	ReadOnlyRootfs  bool
	CapDrop         []string
	NoNewPrivileges bool
	Seccomp         SeccompProfile
	User            UIDGID
	PidsLimit       int64
	Memory          int64
	MemorySwap      int64
	NanoCPUs        int64
	Ulimits         []Ulimit
	NetworkMode     NetworkMode
	TmpfsWorkdir    bool
	AutoRemove      bool
}

// SeccompProfile identifies the seccomp profile applied to the sandbox. The
// default profile is used for the MVP; a custom profile is mandatory roadmap.
type SeccompProfile string

const (
	// SeccompDefault applies the runtime's default seccomp profile.
	SeccompDefault SeccompProfile = "default"
)

// IsValidSeccompProfile reports whether p is a known seccomp profile. For the
// MVP SeccompDefault is the ONLY valid value (R-3): a custom/unconfined
// profile is mandatory roadmap, so anything else — including the empty string —
// is rejected fail-closed rather than silently defaulted or mapped to a
// weakening docker option.
func IsValidSeccompProfile(p SeccompProfile) bool {
	return p == SeccompDefault
}

// SandboxSpec is the typed specification used to create a sandbox (§6.5). It
// is never a free-form blob; every field has an explicit type.
type SandboxSpec struct {
	// Driver is the backend driver this spec targets. It is never "auto" after
	// BuildSpec resolves it (SPEC §7.12); a direct caller may leave it empty to
	// defer selection.
	Driver       Driver
	Image        string
	WorkingDir   string
	Env          map[string]string
	Mounts       []Mount
	Resources    Resources
	Network      Network
	UIDGID       UIDGID
	Sudo         SudoPolicy
	Secrets      []SecretRef
	ReadOnlyRoot bool
	Isolation    Isolation
	// Sandbox carries the darwin/Seatbelt knobs (SPEC §7.12); it is zero for
	// the Docker backend.
	Sandbox Sandbox
	// Shell is the profile-configured shell (with optional whitespace-separated
	// args) used to build the sandbox_exec wrapper. Empty means "bash".
	Shell string

	// ClientSessionID is the OpenCode client session id that created the
	// sandbox (§4/§7.8). It is passed as a required sandbox_create parameter and
	// recorded as a container label for traceability. It is OPTIONAL at the
	// domain layer — the required-ness is enforced at the tool input boundary —
	// so internal callers and e2e specs that build a SandboxSpec directly can
	// omit it.
	ClientSessionID string
}

// Validate checks that the sandbox spec is well-formed and that the immutable
// Isolation hardening cannot be weakened by a caller (§7.1). Any known network
// mode (bridge, none, host) is accepted — the network source is the
// operator-authored profile (Phase 7), not a server allowlist.
func (s *SandboxSpec) Validate() error {
	// An image is required ONLY for the Docker driver. The Seatbelt driver runs
	// on host libraries and has no image (SPEC §7.12); a direct caller leaves
	// Driver empty to defer to backend selection, so no image is demanded.
	if s.Driver == DriverDocker && strings.TrimSpace(s.Image) == "" {
		return fmt.Errorf("sandbox: Image is required")
	}
	// A NUL byte in the Seatbelt SBPL would terminate the string (SPEC §7.12).
	if s.Driver == DriverSeatbelt && strings.ContainsRune(s.Sandbox.SBPL, '\x00') {
		return fmt.Errorf("sandbox: Sandbox.SBPL must not contain NUL")
	}
	if s.WorkingDir != "" && !strings.HasPrefix(s.WorkingDir, "/") {
		return fmt.Errorf("sandbox: WorkingDir must be an absolute path")
	}
	if s.Sudo != "" && s.Sudo != SudoNever && s.Sudo != SudoRequire {
		return fmt.Errorf("sandbox: invalid Sudo policy %q", s.Sudo)
	}

	iso := s.Isolation
	if !iso.ReadOnlyRootfs {
		return fmt.Errorf("sandbox: Isolation.ReadOnlyRootfs must be true (server-enforced)")
	}
	if !iso.NoNewPrivileges {
		return fmt.Errorf("sandbox: Isolation.NoNewPrivileges must be true (server-enforced)")
	}
	if !hasCapDropAll(iso.CapDrop) {
		return fmt.Errorf("sandbox: Isolation.CapDrop must drop ALL (server-enforced)")
	}
	if !IsValidNetworkMode(iso.NetworkMode) {
		return fmt.Errorf("sandbox: Isolation.NetworkMode %q is not a valid network mode", iso.NetworkMode)
	}
	if !IsValidSeccompProfile(iso.Seccomp) {
		return fmt.Errorf("sandbox: Isolation.Seccomp %q is not a valid seccomp profile", iso.Seccomp)
	}
	if iso.User.UID == 0 {
		return fmt.Errorf("sandbox: Isolation.User must be non-root (server-enforced)")
	}
	if !iso.TmpfsWorkdir {
		return fmt.Errorf("sandbox: Isolation.TmpfsWorkdir must be true (server-enforced)")
	}
	if !iso.AutoRemove {
		return fmt.Errorf("sandbox: Isolation.AutoRemove must be true (server-enforced)")
	}
	if iso.PidsLimit <= 0 {
		return fmt.Errorf("sandbox: Isolation.PidsLimit must be bounded (> 0)")
	}
	if iso.Memory <= 0 {
		return fmt.Errorf("sandbox: Isolation.Memory must be bounded (> 0)")
	}
	if iso.NanoCPUs <= 0 {
		return fmt.Errorf("sandbox: Isolation.NanoCPUs must be bounded (> 0)")
	}

	for i, m := range s.Mounts {
		if strings.TrimSpace(m.Source) == "" || strings.TrimSpace(m.Target) == "" {
			return fmt.Errorf("sandbox: mount %d requires Source and Target", i)
		}
	}

	return nil
}

func hasCapDropAll(caps []string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, "ALL") {
			return true
		}
	}

	return false
}
