// Package profile_test pins the operator-authored, read-only sandbox profile
// model (Phase 7): Profile validation and BuildSpec (profile → SandboxSpec).
//
// BuildSpec is the seam where operator intent (image, env, mounts, network,
// cgroups, user, sudo) is merged with the server's immutable Isolation policy.
// The design locks two invariants:
//
//   - profile-owned fields (NanoCPUs/Memory/MemorySwap/PidsLimit/Ulimits/
//     NetworkMode) OVERRIDE the server policy `pol`;
//   - hardening fields (ReadOnlyRootfs, CapDrop, NoNewPrivileges, Seccomp,
//     User, TmpfsWorkdir, AutoRemove) ALWAYS come from `pol` and are NEVER
//     overridden by the profile — a profile can never weaken the sandbox.
package profile

import (
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// validProfile returns a profile that passes Validate.
func validProfile() *Profile {
	return &Profile{
		Name:        "default",
		Description: "the default profile",
		Image:       "docker.io/library/alpine:3.20",
		Env:         map[string]string{"A": "1"},
		Mounts: []Mount{
			{Source: "/data", Target: "/data", ReadOnly: true},
		},
		Cwd:     CwdConfig{Target: "", Mode: "ro"},
		Network: Network{Mode: spec.NetworkBridge},
		Cgroups: Cgroups{
			NanoCPUs:   1e9,
			Memory:     512 << 20,
			MemorySwap: 512 << 20,
			PidsLimit:  256,
			Ulimits:    []spec.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}},
		},
		User: &spec.UIDGID{UID: 1000, GID: 1000},
		Sudo: spec.SudoNever,
	}
}

// validIsolation returns a server-enforced Isolation that satisfies
// spec.Validate.
func validIsolation() spec.Isolation {
	return spec.Isolation{
		ReadOnlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         spec.SeccompDefault,
		User:            spec.UIDGID{UID: 1000, GID: 1000},
		PidsLimit:       256,
		Memory:          512 << 20,
		MemorySwap:      512 << 20,
		NanoCPUs:        1e9,
		Ulimits:         []spec.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}},
		NetworkMode:     spec.NetworkBridge,
		TmpfsWorkdir:    true,
		AutoRemove:      true,
	}
}

func TestProfileValidate_Valid(t *testing.T) {
	if err := validProfile().Validate(); err != nil {
		t.Fatalf("expected valid profile, got: %v", err)
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
		{"unparsable image", func(p *Profile) { p.Image = "::::invalid" }, "image"},
		{"empty image", func(p *Profile) { p.Image = "" }, "image"},
		{"relative mount source", func(p *Profile) { p.Mounts[0].Source = "data" }, "mount"},
		{"relative mount target", func(p *Profile) { p.Mounts[0].Target = "data" }, "mount"},
		{"invalid cwd mode", func(p *Profile) { p.Cwd.Mode = "weird" }, "cwd"},
		{"relative cwd target", func(p *Profile) { p.Cwd.Target = "work" }, "cwd"},
		{"invalid network mode", func(p *Profile) { p.Network.Mode = spec.NetworkMode("vlan") }, "network"},
		{"unbounded cpus", func(p *Profile) { p.Cgroups.NanoCPUs = 0 }, "NanoCPUs"},
		{"unbounded memory", func(p *Profile) { p.Cgroups.Memory = 0 }, "Memory"},
		{"unbounded pids", func(p *Profile) { p.Cgroups.PidsLimit = 0 }, "PidsLimit"},
		{"memory_swap less than memory", func(p *Profile) { p.Cgroups.MemorySwap = p.Cgroups.Memory - 1 }, "MemorySwap"},
		{"root user", func(p *Profile) { p.User = &spec.UIDGID{UID: 0, GID: 0} }, "root"},
		{"invalid sudo", func(p *Profile) { p.Sudo = spec.SudoPolicy("maybe") }, "sudo"},
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

// TestBuildSpec_ShellDefaultBash pins that an unset profile shell defaults to
// "bash" in the built SandboxSpec.
func TestBuildSpec_ShellDefaultBash(t *testing.T) {
	p := validProfile()
	p.Shell = "" // unset
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/c"})
	if sp.Shell != "bash" {
		t.Fatalf("Shell = %q, want default \"bash\"", sp.Shell)
	}
}

// TestBuildSpec_ShellPropagated pins that a set profile shell (including args)
// is propagated into the built SandboxSpec.
func TestBuildSpec_ShellPropagated(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "bash -l"} {
		p := validProfile()
		p.Shell = shell
		sp := p.BuildSpec(validIsolation(), Params{Cwd: "/c"})
		if sp.Shell != shell {
			t.Fatalf("Shell = %q, want propagated %q", sp.Shell, shell)
		}
	}
}

// TestProfileValidate_ImageParseOnly pins Phase 7: image validation is
// parse-only — any valid reference (from ANY registry) is accepted, because the
// image source comes from this operator-authored profile and there is no
// registry allowlist (TRUSTED_REGISTRIES was removed). Only empty or unparsable
// references are rejected.
func TestProfileValidate_ImageParseOnly(t *testing.T) {
	for _, img := range []string{
		"docker.io/library/alpine:3.20",
		"ghcr.io/org/app:v1",
		"evil.example.com/team/app:v1", // arbitrary registry accepted
		"localhost:5000/app:v1",        // arbitrary registry accepted
	} {
		p := validProfile()
		p.Image = img
		if err := p.Validate(); err != nil {
			t.Errorf("image %q should validate (parse-only): %v", img, err)
		}
	}
}

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

func TestBuildSpec_CwdMountAlwaysPresent(t *testing.T) {
	// Even with no profile-declared cwd config, the session cwd is mounted 1:1
	// at the same absolute path, read-only by default.
	p := validProfile()
	p.Cwd = CwdConfig{} // no target, no mode
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/home/user/proj"})

	if len(sp.Mounts) != 2 { // cwd + the profile's /data mount
		t.Fatalf("mounts = %+v, want cwd + profile mount", sp.Mounts)
	}
	cwd := sp.Mounts[0]
	if cwd.Source != "/home/user/proj" || cwd.Target != "/home/user/proj" {
		t.Fatalf("cwd mount not 1:1: %+v", cwd)
	}
	if !cwd.ReadOnly {
		t.Fatal("cwd mount must be read-only by default")
	}
	if sp.WorkingDir != "/home/user/proj" {
		t.Fatalf("WorkingDir = %q", sp.WorkingDir)
	}
}

func TestBuildSpec_CwdWritableWhenModeRW(t *testing.T) {
	p := validProfile()
	p.Cwd = CwdConfig{Mode: "rw"}
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/home/user/proj"})
	if sp.Mounts[0].ReadOnly {
		t.Fatal("cwd mount must be writable when profile cwd.mode == rw")
	}
}

func TestBuildSpec_CwdTargetHonored(t *testing.T) {
	p := validProfile()
	p.Cwd = CwdConfig{Target: "/work"}
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/home/user/proj"})
	// The cwd source stays the client path, but the container target is the
	// profile-declared target.
	if sp.Mounts[0].Source != "/home/user/proj" || sp.Mounts[0].Target != "/work" {
		t.Fatalf("cwd mount = %+v, want source=client path, target=/work", sp.Mounts[0])
	}
}

func TestBuildSpec_ProfileMountsAppended(t *testing.T) {
	p := validProfile()
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/home/user/proj"})
	// mounts[0] is the cwd; mounts[1] is the profile mount.
	if len(sp.Mounts) != 2 {
		t.Fatalf("mounts = %+v, want cwd + profile mount", sp.Mounts)
	}
	m := sp.Mounts[1]
	if m.Source != "/data" || m.Target != "/data" || !m.ReadOnly {
		t.Fatalf("profile mount = %+v", m)
	}
}

func TestBuildSpec_ProfileOverridesPolicyResources(t *testing.T) {
	pol := validIsolation()
	pol.NanoCPUs = 1e9
	pol.Memory = 512 << 20
	pol.MemorySwap = 512 << 20
	pol.PidsLimit = 256

	p := validProfile()
	p.Cgroups = Cgroups{
		NanoCPUs:   2e9,
		Memory:     1 << 30,
		MemorySwap: 2 << 30,
		PidsLimit:  512,
		Ulimits:    []spec.Ulimit{{Name: "nofile", Soft: 2048, Hard: 2048}},
	}
	p.Network.Mode = spec.NetworkNone

	sp := p.BuildSpec(pol, Params{Cwd: "/c"})

	if sp.Isolation.NanoCPUs != 2e9 {
		t.Fatalf("NanoCPUs = %d, want profile 2e9", sp.Isolation.NanoCPUs)
	}
	if sp.Isolation.Memory != 1<<30 {
		t.Fatalf("Memory = %d, want profile 1GiB", sp.Isolation.Memory)
	}
	if sp.Isolation.MemorySwap != 2<<30 {
		t.Fatalf("MemorySwap = %d, want profile 2GiB", sp.Isolation.MemorySwap)
	}
	if sp.Isolation.PidsLimit != 512 {
		t.Fatalf("PidsLimit = %d, want profile 512", sp.Isolation.PidsLimit)
	}
	if len(sp.Isolation.Ulimits) != 1 || sp.Isolation.Ulimits[0].Soft != 2048 {
		t.Fatalf("Ulimits = %+v, want profile ulimits", sp.Isolation.Ulimits)
	}
	if sp.Isolation.NetworkMode != spec.NetworkNone {
		t.Fatalf("NetworkMode = %q, want profile none", sp.Isolation.NetworkMode)
	}
	if sp.Network.Mode != spec.NetworkNone {
		t.Fatalf("spec.Network.Mode = %q, want none", sp.Network.Mode)
	}
	if sp.Resources.NanoCPUs != 2e9 || sp.Resources.Memory != 1<<30 || sp.Resources.PidsLimit != 512 {
		t.Fatalf("Resources = %+v, want profile values", sp.Resources)
	}
}

func TestBuildSpec_HardeningNeverOverridden(t *testing.T) {
	// A hostile profile attempts to weaken every hardening field; all of them
	// must come from `pol` unchanged.
	pol := validIsolation()
	pol.User = spec.UIDGID{UID: 1000, GID: 1000}

	p := validProfile()
	p.User = &spec.UIDGID{UID: 0, GID: 0} // hostile: root
	// There is no profile field for the remaining hardening flags, so the only
	// way they can be weakened is if the implementation copies from the wrong
	// source. Assert they all match `pol` exactly.

	sp := p.BuildSpec(pol, Params{Cwd: "/c"})

	if !sp.Isolation.ReadOnlyRootfs {
		t.Error("ReadOnlyRootfs must come from pol (true)")
	}
	if !sp.Isolation.NoNewPrivileges {
		t.Error("NoNewPrivileges must come from pol (true)")
	}
	found := false
	for _, c := range sp.Isolation.CapDrop {
		if strings.EqualFold(c, "ALL") {
			found = true
		}
	}
	if !found {
		t.Error("CapDrop must come from pol (ALL)")
	}
	if sp.Isolation.Seccomp != spec.SeccompDefault {
		t.Error("Seccomp must come from pol")
	}
	if !sp.Isolation.TmpfsWorkdir {
		t.Error("TmpfsWorkdir must come from pol (true)")
	}
	if !sp.Isolation.AutoRemove {
		t.Error("AutoRemove must come from pol (true)")
	}
	// The profile cannot override the enforced user even though it asked for
	// root — the sandbox must run non-root.
	if sp.Isolation.User.UID == 0 {
		t.Error("Isolation.User must remain non-root from pol, never overridden by profile")
	}
	if sp.UIDGID.UID != pol.User.UID || sp.UIDGID.GID != pol.User.GID {
		t.Fatalf("UIDGID = %+v, want pol user %+v", sp.UIDGID, pol.User)
	}
}

func TestBuildSpec_DefaultUserNonRootWhenProfileUserNil(t *testing.T) {
	p := validProfile()
	p.User = nil
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/c"})
	if sp.UIDGID.UID == 0 {
		t.Fatal("default user must be non-root when profile user is nil")
	}
	if sp.UIDGID.UID != 1000 || sp.UIDGID.GID != 1000 {
		t.Fatalf("UIDGID = %+v, want pol non-root user", sp.UIDGID)
	}
}

func TestBuildSpec_ProfileUserHonored(t *testing.T) {
	p := validProfile()
	p.User = &spec.UIDGID{UID: 2000, GID: 2000}
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/c"})
	if sp.UIDGID.UID != 2000 || sp.UIDGID.GID != 2000 {
		t.Fatalf("UIDGID = %+v, want profile user 2000:2000", sp.UIDGID)
	}
	// Isolation.User (the enforced non-root for tmpfs ownership) still comes
	// from pol.
	if sp.Isolation.User.UID != 1000 {
		t.Fatalf("Isolation.User.UID = %d, want pol 1000", sp.Isolation.User.UID)
	}
}

func TestBuildSpec_SecretsAndClientSessionIDPassedThrough(t *testing.T) {
	p := validProfile()
	secrets := []spec.SecretRef{{Name: "token", Value: "topsecret"}}
	sp := p.BuildSpec(validIsolation(), Params{
		Cwd:             "/c",
		Secrets:         secrets,
		ClientSessionID: "opencode-sess-9",
	})
	if len(sp.Secrets) != 1 || sp.Secrets[0].Value != "topsecret" {
		t.Fatalf("Secrets = %+v, want passed through", sp.Secrets)
	}
	if sp.ClientSessionID != "opencode-sess-9" {
		t.Fatalf("ClientSessionID = %q, want opencode-sess-9", sp.ClientSessionID)
	}
}

func TestBuildSpec_ImageAndEnvFromProfile(t *testing.T) {
	p := validProfile()
	p.Image = "ghcr.io/org/app:v1"
	p.Env = map[string]string{"K": "V"}
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/c"})
	if sp.Image != "ghcr.io/org/app:v1" {
		t.Fatalf("Image = %q, want profile image", sp.Image)
	}
	if sp.Env["K"] != "V" {
		t.Fatalf("Env = %+v, want profile env", sp.Env)
	}
}

func TestBuildSpec_ResultValidates(t *testing.T) {
	p := validProfile()
	sp := p.BuildSpec(validIsolation(), Params{Cwd: "/home/user/proj"})
	if err := sp.Validate(); err != nil {
		t.Fatalf("built spec must validate: %v", err)
	}
}
