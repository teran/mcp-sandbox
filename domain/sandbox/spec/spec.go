// Package spec defines the typed SandboxSpec and the immutable Isolation
// policy that the server enforces on every container create (§7.1).
package spec

import (
	"fmt"
	"strings"
)

// NetworkMode describes the network isolation of a sandbox.
type NetworkMode string

const (
	// NetworkBridge attaches the sandbox to Docker's default bridged network
	// (safe default).
	NetworkBridge NetworkMode = "bridge"
	// NetworkNone disables networking entirely.
	NetworkNone NetworkMode = "none"
	// NetworkHost exposes the host network namespace. It is never allowed
	// implicitly: a sandbox may use it only when the operator explicitly lists
	// it in ALLOW_NETWORK_MODES (SPEC §7.1).
	NetworkHost NetworkMode = "host"
)

// AllNetworkModes returns every network mode the server knows about.
func AllNetworkModes() []NetworkMode {
	return []NetworkMode{NetworkNone, NetworkBridge, NetworkHost}
}

// DefaultNetworkModes returns the default allowlist of network modes when the
// operator does not configure ALLOW_NETWORK_MODES: only the safe bridge mode.
// Host networking is never allowed implicitly (SPEC §7.1).
func DefaultNetworkModes() []NetworkMode {
	return []NetworkMode{NetworkBridge}
}

// IsValidNetworkMode reports whether m is one of the known network modes.
func IsValidNetworkMode(m NetworkMode) bool {
	for _, n := range AllNetworkModes() {
		if m == n {
			return true
		}
	}
	return false
}

func containsNetworkMode(allowed []NetworkMode, m NetworkMode) bool {
	for _, n := range allowed {
		if n == m {
			return true
		}
	}
	return false
}

// SudoPolicy controls whether sandbox_install_package may use privilege
// escalation (via sudo).
type SudoPolicy string

const (
	// SudoNever forbids any privilege escalation inside the sandbox.
	SudoNever SudoPolicy = "never"
	// SudoRequire runs package installation under sudo.
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

// SandboxSpec is the typed specification used to create a sandbox (§6.5). It
// is never a free-form blob; every field has an explicit type.
type SandboxSpec struct {
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
}

// Validate checks that the sandbox spec is well-formed and that the immutable
// Isolation hardening cannot be weakened by a caller (§7.1). It permits only
// the default network-mode allowlist (DefaultNetworkModes).
func (s *SandboxSpec) Validate() error {
	return s.ValidateWithNetworkModes(DefaultNetworkModes())
}

// ValidateWithNetworkModes is like Validate but permits only the given network
// modes for the immutable Isolation (§7.1). A requested mode not in the allowed
// set — including host when the operator has not listed it — is rejected.
// allowed must be non-empty; an empty allowlist rejects every spec. It returns
// a descriptive error identifying the first problem found.
func (s *SandboxSpec) ValidateWithNetworkModes(allowed []NetworkMode) error {
	if len(allowed) == 0 {
		return fmt.Errorf("sandbox: no network modes are allowed")
	}
	if strings.TrimSpace(s.Image) == "" {
		return fmt.Errorf("sandbox: Image is required")
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
	if iso.NetworkMode == "" {
		return fmt.Errorf("sandbox: Isolation.NetworkMode must be set (default bridge)")
	}
	if !containsNetworkMode(allowed, iso.NetworkMode) {
		return fmt.Errorf("sandbox: network mode %q is not in the allowed set %v", iso.NetworkMode, allowed)
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
