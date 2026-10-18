// Package darwin pins the built-in seatbelt driver builder (Slices 2+3,
// SPEC §6.11/§7.12): buildSeatbelt decodes the opaque `options` yaml.Node into
// its own typed seatbelt options and produces a spec.SandboxSpec equivalent to
// the pre-migration profile.BuildSpec seatbelt branch — no image, the Sandbox
// block (SBPL/mach_lookups/sysctls) and env from the options, Sudo=SudoNever,
// UIDGID/Resources from the immutable policy, only the auto cwd mount, network
// from the policy, hardening from the policy. Unknown option keys fail closed.
package darwin

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// seatbeltValidIsolation returns a server-enforced Isolation that satisfies
// spec.Validate.
func seatbeltValidIsolation() spec.Isolation {
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
		NetworkMode:     spec.NetworkBridge,
		TmpfsWorkdir:    true,
		AutoRemove:      true,
	}
}

// seatbeltCtx builds a driver.Context for the seatbelt builder from a raw
// options YAML document with a read-only "/home/user/proj" cwd.
func seatbeltCtx(t *testing.T, optsYAML string) driver.Context {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(optsYAML), &n); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	return driver.Context{
		Name:        "mac",
		Description: "a seatbelt profile",
		Cwd:         domainprofile.CwdConfig{Mode: "ro"},
		Shell:       "bash",
		Options:     driver.NewOptions(&n),
		Policy:      seatbeltValidIsolation(),
		Params:      domainprofile.Params{Cwd: "/home/user/proj"},
	}
}

// validSeatbeltOptions is the full seatbelt options document mirroring the old
// `seatbelt:` block used across the migrated fixtures.
const validSeatbeltOptions = `sbpl: |
  (import "system.sb")
  (allow process-fork)
mach_lookups:
  - com.apple.system.logger
sysctls:
  - kern.osversion
env:
  MY_VAR: hello-from-profile
`

// TestBuildSeatbelt_FullOptions pins the full seatbelt projection: no image,
// Sandbox block from options, env from options, Sudo=SudoNever, UIDGID/Resources
// from the policy, only the cwd mount, network from the policy and hardening
// from the policy.
func TestBuildSeatbelt_FullOptions(t *testing.T) {
	pol := seatbeltValidIsolation()
	sp, err := buildSeatbelt(seatbeltCtx(t, validSeatbeltOptions))
	if err != nil {
		t.Fatalf("buildSeatbelt: %v", err)
	}

	if sp.Driver != spec.DriverSeatbelt {
		t.Fatalf("Driver = %q, want seatbelt", sp.Driver)
	}
	if sp.Image != "" {
		t.Fatalf("Image = %q, want empty for seatbelt", sp.Image)
	}
	if sp.Sandbox.SBPL != "(import \"system.sb\")\n(allow process-fork)\n" {
		t.Fatalf("Sandbox.SBPL = %q", sp.Sandbox.SBPL)
	}
	if len(sp.Sandbox.MachLookups) != 1 || sp.Sandbox.MachLookups[0] != "com.apple.system.logger" {
		t.Fatalf("MachLookups = %+v", sp.Sandbox.MachLookups)
	}
	if len(sp.Sandbox.Sysctls) != 1 || sp.Sandbox.Sysctls[0] != "kern.osversion" {
		t.Fatalf("Sysctls = %+v", sp.Sandbox.Sysctls)
	}
	if sp.Env["MY_VAR"] != "hello-from-profile" {
		t.Fatalf("Env = %+v, want profile env", sp.Env)
	}
	if sp.Sudo != spec.SudoNever {
		t.Fatalf("Sudo = %q, want never for seatbelt", sp.Sudo)
	}
	if sp.UIDGID != pol.User {
		t.Fatalf("UIDGID = %+v, want policy user %+v", sp.UIDGID, pol.User)
	}
	if sp.Resources.NanoCPUs != pol.NanoCPUs || sp.Resources.Memory != pol.Memory || sp.Resources.PidsLimit != pol.PidsLimit {
		t.Fatalf("Resources = %+v, want policy resources", sp.Resources)
	}
	if sp.Network.Mode != pol.NetworkMode || sp.Isolation.NetworkMode != pol.NetworkMode {
		t.Fatalf("network = %+v / isolation %q, want policy", sp.Network, sp.Isolation.NetworkMode)
	}
	if len(sp.Mounts) != 1 {
		t.Fatalf("Mounts = %+v, want only the cwd mount", sp.Mounts)
	}
	if sp.Mounts[0].Source != "/home/user/proj" || sp.Mounts[0].Target != "/home/user/proj" {
		t.Fatalf("cwd mount = %+v, want 1:1", sp.Mounts[0])
	}
	if sp.WorkingDir != "/home/user/proj" {
		t.Fatalf("WorkingDir = %q", sp.WorkingDir)
	}

	if err := sp.Validate(); err != nil {
		t.Fatalf("built spec must validate: %v", err)
	}
	if err := spec.EnforceHardening(pol, &sp, []string{"/home/user/proj"}); err != nil {
		t.Fatalf("EnforceHardening: %v", err)
	}
}

// TestBuildSeatbelt_HardeningFromPolicy pins that the seatbelt builder never
// weakens the immutable Isolation hardening.
func TestBuildSeatbelt_HardeningFromPolicy(t *testing.T) {
	pol := seatbeltValidIsolation()
	sp, err := buildSeatbelt(seatbeltCtx(t, validSeatbeltOptions))
	if err != nil {
		t.Fatalf("buildSeatbelt: %v", err)
	}
	if !sp.Isolation.ReadOnlyRootfs || !sp.Isolation.NoNewPrivileges || !sp.Isolation.TmpfsWorkdir || !sp.Isolation.AutoRemove {
		t.Fatalf("seatbelt must inherit hardening from policy: %+v", sp.Isolation)
	}
	if sp.Isolation.User.UID == 0 {
		t.Fatal("seatbelt isolation user must stay non-root")
	}
	if err := spec.EnforceHardening(pol, &sp, []string{"/home/user/proj"}); err != nil {
		t.Fatalf("EnforceHardening: %v", err)
	}
}

// TestBuildSeatbelt_UnknownOptionKey_Error pins that an unknown key in the
// opaque seatbelt options fails closed at build time.
func TestBuildSeatbelt_UnknownOptionKey_Error(t *testing.T) {
	const yaml = `sbpl: "(allow process-fork)"
totally_bogus_key: true
`
	if _, err := buildSeatbelt(seatbeltCtx(t, yaml)); err == nil {
		t.Fatal("expected error for unknown option key")
	}
}

// TestBuildSeatbelt_ResultEnforceHardening pins the choke-point contract: a
// built seatbelt spec re-asserts the policy hardening through EnforceHardening
// and validates.
func TestBuildSeatbelt_ResultEnforceHardening(t *testing.T) {
	pol := seatbeltValidIsolation()
	sp, err := buildSeatbelt(seatbeltCtx(t, validSeatbeltOptions))
	if err != nil {
		t.Fatalf("buildSeatbelt: %v", err)
	}
	if err := spec.EnforceHardening(pol, &sp, []string{"/home/user/proj"}); err != nil {
		t.Fatalf("EnforceHardening: %v", err)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("post-hardening spec must validate: %v", err)
	}
}

// TestBuildSeatbelt_CwdMode_ReadOnlyFlag pins the cwd mount read-only flag for
// both a "ro" (read-only mount) and "rw" (writable mount) profile cwd mode.
func TestBuildSeatbelt_CwdMode_ReadOnlyFlag(t *testing.T) {
	for _, testCase := range []struct {
		mode     string
		readOnly bool
	}{
		{mode: "ro", readOnly: true},
		{mode: "rw", readOnly: false},
	} {
		t.Run(testCase.mode, func(t *testing.T) {
			ctx := seatbeltCtx(t, validSeatbeltOptions)
			ctx.Cwd.Mode = testCase.mode
			sp, err := buildSeatbelt(ctx)
			if err != nil {
				t.Fatalf("buildSeatbelt: %v", err)
			}
			if len(sp.Mounts) != 1 {
				t.Fatalf("Mounts = %+v, want only the cwd mount", sp.Mounts)
			}
			if sp.Mounts[0].ReadOnly != testCase.readOnly {
				t.Fatalf("cwd mount ReadOnly = %v, want %v (mode %q)", sp.Mounts[0].ReadOnly, testCase.readOnly, testCase.mode)
			}
		})
	}
}

// TestBuildSeatbelt_EmptyShell_DefaultsToBash pins that an empty profile shell
// falls back to "bash" (SPEC §7.10).
func TestBuildSeatbelt_EmptyShell_DefaultsToBash(t *testing.T) {
	ctx := seatbeltCtx(t, validSeatbeltOptions)
	ctx.Shell = ""
	sp, err := buildSeatbelt(ctx)
	if err != nil {
		t.Fatalf("buildSeatbelt: %v", err)
	}
	if sp.Shell != "bash" {
		t.Fatalf("Shell = %q, want \"bash\" default", sp.Shell)
	}
}

// TestSeatbeltBuilder_DeclaresSchemaVersion pins that the seatbelt builder,
// registered in init() into the process-wide driver registry, declares the
// seatbelt profile schema apiVersion it supports (SPEC §7.11).
func TestSeatbeltBuilder_DeclaresSchemaVersion(t *testing.T) {
	b, err := driver.Default.Get(spec.DriverSeatbelt)
	if err != nil {
		t.Fatalf("Get(seatbelt): %v", err)
	}
	ver, ok := b.(driver.APIVersioner)
	if !ok {
		t.Fatal("seatbelt builder must implement APIVersioner")
	}
	if want := "seatbelt.profiles.sandbox.teran.dev/v1"; ver.APIVersion() != want {
		t.Fatalf("APIVersion() = %q, want %q", ver.APIVersion(), want)
	}
	if err := driver.Reconcile(ver.APIVersion(), b); err != nil {
		t.Fatalf("Reconcile(own) = %v, want nil", err)
	}
	if err := driver.Reconcile("seatbelt.profiles.sandbox.teran.dev/v99", b); err == nil {
		t.Fatal("Reconcile(wrong version) must fail")
	}
}
