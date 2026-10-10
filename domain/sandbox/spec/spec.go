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
	for _, n := range AllNetworkModes() {
		if m == n {
			return true
		}
	}
	return false
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
	if !IsValidNetworkMode(iso.NetworkMode) {
		return fmt.Errorf("sandbox: Isolation.NetworkMode %q is not a valid network mode", iso.NetworkMode)
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
