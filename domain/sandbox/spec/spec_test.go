package spec

import (
	"strings"
	"testing"
)

func validIsolation() Isolation {
	return Isolation{
		ReadOnlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         SeccompDefault,
		User:            UIDGID{UID: 1000, GID: 1000},
		PidsLimit:       256,
		Memory:          512 << 20,
		MemorySwap:      512 << 20,
		NanoCPUs:        1e9,
		Ulimits:         []Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}},
		NetworkMode:     NetworkBridge,
		TmpfsWorkdir:    true,
		AutoRemove:      true,
	}
}

func validSpec() *SandboxSpec {
	return &SandboxSpec{
		Image:      "docker.io/library/alpine@sha256:abc",
		WorkingDir: "/work",
		Env:        map[string]string{"A": "1"},
		Mounts: []Mount{
			{Source: "/host", Target: "/mnt", ReadOnly: true},
		},
		Resources: Resources{NanoCPUs: 1e9, Memory: 512 << 20, PidsLimit: 256},
		Network:   Network{Mode: NetworkBridge},
		UIDGID:    UIDGID{UID: 1000, GID: 1000},
		Sudo:      SudoNever,
		Secrets: []SecretRef{
			{Name: "token", Value: "supersecret"},
		},
		ReadOnlyRoot: true,
		Isolation:    validIsolation(),
	}
}

func TestSandboxSpecValidate_Valid(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("expected valid spec, got: %v", err)
	}
}

func TestSandboxSpecValidate_Errors(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*SandboxSpec)
		want string
	}{
		{"image required", func(s *SandboxSpec) { s.Image = " " }, "Image is required"},
		{"working dir relative", func(s *SandboxSpec) { s.WorkingDir = "work" }, "absolute path"},
		{"invalid sudo", func(s *SandboxSpec) { s.Sudo = SudoPolicy("maybe") }, "invalid Sudo"},
		{"readonly false", func(s *SandboxSpec) { s.Isolation.ReadOnlyRootfs = false }, "ReadOnlyRootfs"},
		{"no new priv false", func(s *SandboxSpec) { s.Isolation.NoNewPrivileges = false }, "NoNewPrivileges"},
		{"capdrop missing all", func(s *SandboxSpec) { s.Isolation.CapDrop = []string{"NET_ADMIN"} }, "CapDrop"},
		{"network unset", func(s *SandboxSpec) { s.Isolation.NetworkMode = "" }, "NetworkMode"},
		{"root user", func(s *SandboxSpec) { s.Isolation.User = UIDGID{UID: 0} }, "non-root"},
		{"tmpfs workdir false", func(s *SandboxSpec) { s.Isolation.TmpfsWorkdir = false }, "TmpfsWorkdir"},
		{"autoremove false", func(s *SandboxSpec) { s.Isolation.AutoRemove = false }, "AutoRemove"},
		{"pids unbounded", func(s *SandboxSpec) { s.Isolation.PidsLimit = 0 }, "PidsLimit"},
		{"memory unbounded", func(s *SandboxSpec) { s.Isolation.Memory = 0 }, "Memory"},
		{"nacpus unbounded", func(s *SandboxSpec) { s.Isolation.NanoCPUs = 0 }, "NanoCPUs"},
		{"bad mount", func(s *SandboxSpec) { s.Mounts = []Mount{{Source: "", Target: "/mnt"}} }, "mount"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.mut(s)
			err := s.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got: %v", tt.want, err)
			}
		})
	}
}

// TestValidate_ImageParseOnly pins Phase 7: any valid image reference (from ANY
// registry) is acceptable — there is no registry allowlist (TRUSTED_REGISTRIES
// was removed); image validation is parse-only.
func TestValidate_ImageParseOnly(t *testing.T) {
	for _, img := range []string{
		"docker.io/library/alpine@sha256:abc",
		"ghcr.io/org/app:v1",
		"evil.example.com/team/app:v1", // arbitrary registry accepted
		"localhost:5000/app:v1",        // arbitrary registry accepted
	} {
		s := validSpec()
		s.Image = img
		if err := s.Validate(); err != nil {
			t.Errorf("image %q should validate (parse-only): %v", img, err)
		}
	}
}

func TestNetworkBridgeIsAllowedDefault(t *testing.T) {
	s := validSpec()
	if s.Isolation.NetworkMode != NetworkBridge {
		t.Fatalf("validSpec Isolation.NetworkMode = %q, want bridge (default)", s.Isolation.NetworkMode)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("bridge spec must validate, got: %v", err)
	}
}

func TestHasCapDropAll_CaseInsensitive(t *testing.T) {
	if !hasCapDropAll([]string{"all"}) {
		t.Fatal("expected case-insensitive match")
	}
}

// TestNetworkHostIsValidMode pins Phase 7: with the network-allowlist removed,
// host is a valid known network mode and a spec requesting it validates. The
// fail-closed host default is enforced at the config/tool layer (operator
// opt-in), not by spec.Validate.
func TestNetworkHostIsValidMode(t *testing.T) {
	if !IsValidNetworkMode(NetworkHost) {
		t.Fatal("NetworkHost must be a valid known network mode")
	}
	for _, mode := range AllNetworkModes() {
		if !IsValidNetworkMode(mode) {
			t.Fatalf("IsValidNetworkMode(%q) = false, want true", mode)
		}
	}
	if IsValidNetworkMode(NetworkMode("vlan")) {
		t.Fatal("unknown mode should be invalid")
	}

	// A spec with host networking validates (no allowlist to reject it).
	s := validSpec()
	s.Isolation.NetworkMode = NetworkHost
	if err := s.Validate(); err != nil {
		t.Fatalf("host network spec must validate with no allowlist: %v", err)
	}
}

// TestValidate_KeepsHardeningAndBoundedResourceChecks confirms that even with
// the network-allowlist gone, Validate still fail-closes on every hardening and
// resource-bounding invariant.
func TestValidate_KeepsHardeningAndBoundedResourceChecks(t *testing.T) {
	mut := map[string]func(*SandboxSpec) string{
		"readonly false":   func(s *SandboxSpec) string { s.Isolation.ReadOnlyRootfs = false; return "ReadOnlyRootfs" },
		"no new priv":      func(s *SandboxSpec) string { s.Isolation.NoNewPrivileges = false; return "NoNewPrivileges" },
		"capdrop":          func(s *SandboxSpec) string { s.Isolation.CapDrop = []string{"NET_ADMIN"}; return "CapDrop" },
		"root user":        func(s *SandboxSpec) string { s.Isolation.User = UIDGID{UID: 0}; return "non-root" },
		"tmpfs workdir":    func(s *SandboxSpec) string { s.Isolation.TmpfsWorkdir = false; return "TmpfsWorkdir" },
		"autoremove":       func(s *SandboxSpec) string { s.Isolation.AutoRemove = false; return "AutoRemove" },
		"pids unbounded":   func(s *SandboxSpec) string { s.Isolation.PidsLimit = 0; return "PidsLimit" },
		"memory unbounded": func(s *SandboxSpec) string { s.Isolation.Memory = 0; return "Memory" },
		"nacpus unbounded": func(s *SandboxSpec) string { s.Isolation.NanoCPUs = 0; return "NanoCPUs" },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			s := validSpec()
			want := m(s)
			err := s.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error containing %q, got: %v", want, err)
			}
		})
	}
}
