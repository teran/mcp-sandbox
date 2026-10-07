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
		NetworkMode:     NetworkNone,
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
		Network:   Network{Mode: NetworkNone},
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
		{"host network", func(s *SandboxSpec) { s.Isolation.NetworkMode = NetworkHost }, "host networking"},
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

func TestHasCapDropAll_CaseInsensitive(t *testing.T) {
	if !hasCapDropAll([]string{"all"}) {
		t.Fatal("expected case-insensitive match")
	}
}
