// Package docker pins the built-in docker driver builder (Slices 2+3 of the
// driver-registry + opaque options work, SPEC §6.11/§7.11): buildDocker decodes
// the opaque `options` yaml.Node carried in driver.Context into its own typed
// docker options and produces a spec.SandboxSpec equivalent to the pre-migration
// profile.BuildSpec docker branch.
//
// The semantics that were previously in profile.BuildSpec/applyDocker/yamlCgroups
// now live here and are pinned by these tests: friendly-unit cgroup conversion
// (cpu_cores/memory_gb) with raw-value precedence, MemorySwap defaulting to
// Memory, cwd-first mount honouring cwd.mode, non-root user override of the
// policy user, profile-owned resource override, and hardening always from the
// immutable policy. Unknown option keys fail closed.
package docker

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// dockerValidIsolation returns a server-enforced Isolation that satisfies
// spec.Validate (non-root user, bridge network, bounded resources, hardening).
func dockerValidIsolation() spec.Isolation {
	return spec.Isolation{
		ReadOnlyRootfs:  true,
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
		Seccomp:         spec.SeccompDefault,
		User:            spec.UIDGID{UID: 1000, GID: 1000},
		PidsLimit:       512,
		Memory:          512 << 20,
		MemorySwap:      1024 << 20,
		NanoCPUs:        1e9,
		NetworkMode:     spec.NetworkBridge,
		TmpfsWorkdir:    true,
		AutoRemove:      true,
	}
}

// dockerCtx builds a driver.Context for the docker builder from a raw options
// YAML document. The default context uses a read-only cwd, a "/home/user/proj"
// session cwd and a default bash shell.
func dockerCtx(t *testing.T, optsYAML string) driver.Context {
	t.Helper()

	return dockerCtxFull(t, optsYAML, dockerValidIsolation(), domainprofile.CwdConfig{Mode: "ro"})
}

// dockerCtxFull is dockerCtx with the policy and cwd config injectable so tests
// can vary the immutable policy and the cwd mount mode/target.
func dockerCtxFull(t *testing.T, optsYAML string, pol spec.Isolation, cwd domainprofile.CwdConfig) driver.Context {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(optsYAML), &n); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}

	return driver.Context{
		Name:        "default",
		Description: "the default profile",
		Cwd:         cwd,
		Shell:       "bash",
		Options:     driver.NewOptions(&n),
		Policy:      pol,
		Params:      domainprofile.Params{Cwd: "/home/user/proj"},
	}
}

// validDockerOptions is the full docker options document mirroring the old
// `docker:` block used across the migrated fixtures.
const validDockerOptions = `image: docker.io/library/alpine:3.20
env:
  A: "1"
mounts:
  - source: /data
    target: /data
    read_only: true
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  memory_swap: 536870912
  pids_limit: 256
  ulimits:
    - name: nofile
      soft: 1024
      hard: 1024
user:
  uid: 1000
  gid: 1000
sudo: never
`

// TestBuildDocker_FullOptions pins the full docker projection: image, env,
// cwd-first mount + profile mounts, network.mode, cgroups, non-root user, sudo,
// shell, secrets/client-session passthrough and hardening from the policy.
func TestBuildDocker_FullOptions(t *testing.T) {
	pol := dockerValidIsolation()
	secrets := []spec.SecretRef{{Name: "token", Value: "topsecret"}}
	ctx := dockerCtxFull(t, validDockerOptions, pol, domainprofile.CwdConfig{Mode: "ro"})
	ctx.Params.Secrets = secrets
	ctx.Params.ClientSessionID = "opencode-sess-9"

	sp, err := buildDocker(ctx)
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}

	if sp.Driver != spec.DriverDocker {
		t.Fatalf("Driver = %q, want docker", sp.Driver)
	}
	if sp.Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("Image = %q", sp.Image)
	}
	if sp.Env["A"] != "1" {
		t.Fatalf("Env = %+v, want profile env", sp.Env)
	}
	if sp.WorkingDir != "/home/user/proj" {
		t.Fatalf("WorkingDir = %q, want session cwd 1:1", sp.WorkingDir)
	}
	// Mounts: cwd first, then the profile mount.
	if len(sp.Mounts) != 2 {
		t.Fatalf("Mounts = %+v, want cwd + profile mount", sp.Mounts)
	}
	cwd := sp.Mounts[0]
	if cwd.Source != "/home/user/proj" || cwd.Target != "/home/user/proj" || !cwd.ReadOnly {
		t.Fatalf("cwd mount = %+v, want 1:1 read-only", cwd)
	}
	m := sp.Mounts[1]
	if m.Source != "/data" || m.Target != "/data" || !m.ReadOnly {
		t.Fatalf("profile mount = %+v", m)
	}
	if sp.Network.Mode != spec.NetworkBridge || sp.Isolation.NetworkMode != spec.NetworkBridge {
		t.Fatalf("network = %+v / isolation %q, want bridge", sp.Network, sp.Isolation.NetworkMode)
	}
	assertResourcesNetwork(t, &sp)
	assertIdentityAndPassthrough(t, &sp)
	// Hardening fields all come from the policy.
	assertHardeningFromPolicy(t, &sp, pol)

	if err := sp.Validate(); err != nil {
		t.Fatalf("built spec must validate: %v", err)
	}
	if err := spec.EnforceHardening(pol, &sp, []string{"/home/user/proj", "/data"}); err != nil {
		t.Fatalf("EnforceHardening: %v", err)
	}
}

// assertIdentityAndPassthrough asserts the non-resource fields of the full
// docker projection: non-root user, sudo, shell, secrets/client-session
// passthrough and the zero Sandbox block. Factored out to bound funlen on the
// parent test while preserving every assertion.
func assertIdentityAndPassthrough(t *testing.T, sp *spec.SandboxSpec) {
	t.Helper()
	if sp.UIDGID.UID != 1000 || sp.UIDGID.GID != 1000 {
		t.Fatalf("UIDGID = %+v, want profile 1000:1000", sp.UIDGID)
	}
	if sp.Sudo != spec.SudoNever {
		t.Fatalf("Sudo = %q, want never", sp.Sudo)
	}
	if sp.Shell != "bash" {
		t.Fatalf("Shell = %q, want bash", sp.Shell)
	}
	if len(sp.Secrets) != 1 || sp.Secrets[0].Value != "topsecret" {
		t.Fatalf("Secrets = %+v, want passed through", sp.Secrets)
	}
	if sp.ClientSessionID != "opencode-sess-9" {
		t.Fatalf("ClientSessionID = %q", sp.ClientSessionID)
	}
	// Docker emits a zero Sandbox block.
	if sp.Sandbox.SBPL != "" || len(sp.Sandbox.MachLookups) != 0 || len(sp.Sandbox.Sysctls) != 0 {
		t.Fatalf("Sandbox = %+v, want zero for docker", sp.Sandbox)
	}
}

// assertHardeningFromPolicy verifies every immutable hardening field equals pol.
func assertHardeningFromPolicy(t *testing.T, sp *spec.SandboxSpec, pol spec.Isolation) {
	t.Helper()
	if sp.Isolation.ReadOnlyRootfs != pol.ReadOnlyRootfs {
		t.Errorf("ReadOnlyRootfs = %v, want %v (from pol)", sp.Isolation.ReadOnlyRootfs, pol.ReadOnlyRootfs)
	}
	if sp.Isolation.NoNewPrivileges != pol.NoNewPrivileges {
		t.Errorf("NoNewPrivileges = %v, want %v", sp.Isolation.NoNewPrivileges, pol.NoNewPrivileges)
	}
	if sp.Isolation.Seccomp != pol.Seccomp {
		t.Errorf("Seccomp = %q, want %q", sp.Isolation.Seccomp, pol.Seccomp)
	}
	if sp.Isolation.User != pol.User {
		t.Errorf("Isolation.User = %+v, want pol %+v", sp.Isolation.User, pol.User)
	}
	if sp.Isolation.TmpfsWorkdir != pol.TmpfsWorkdir {
		t.Errorf("TmpfsWorkdir = %v, want %v", sp.Isolation.TmpfsWorkdir, pol.TmpfsWorkdir)
	}
	if sp.Isolation.AutoRemove != pol.AutoRemove {
		t.Errorf("AutoRemove = %v, want %v", sp.Isolation.AutoRemove, pol.AutoRemove)
	}
	if !hasCapDropAll(sp.Isolation.CapDrop) {
		t.Errorf("CapDrop = %v, want ALL from pol", sp.Isolation.CapDrop)
	}
}

func hasCapDropAll(caps []string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, "ALL") {
			return true
		}
	}

	return false
}

// assertResourcesNetwork asserts the profile-owned resource/network fields of a
// built docker spec. It is factored out of TestBuildDocker_FullOptions to keep
// that test's cyclomatic complexity bounded.
func assertResourcesNetwork(t *testing.T, sp *spec.SandboxSpec) {
	t.Helper()
	if sp.Isolation.NanoCPUs != 1e9 ||
		sp.Isolation.Memory != 536870912 ||
		sp.Isolation.MemorySwap != 536870912 ||
		sp.Isolation.PidsLimit != 256 {
		t.Fatalf("cgroups = NanoCPUs %d Memory %d Swap %d Pids %d",
			sp.Isolation.NanoCPUs, sp.Isolation.Memory, sp.Isolation.MemorySwap, sp.Isolation.PidsLimit)
	}
	if len(sp.Isolation.Ulimits) != 1 || sp.Isolation.Ulimits[0].Soft != 1024 {
		t.Fatalf("ulimits = %+v", sp.Isolation.Ulimits)
	}
}

// TestBuildDocker_FriendlyUnits pins the friendly-unit conversion:
// cpu_cores -> NanoCPUs and memory_gb -> Memory, with MemorySwap defaulted to
// Memory (the old yamlCgroups.toDomain semantics, moved into the builder).
func TestBuildDocker_FriendlyUnits(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  cpu_cores: 4
  memory_gb: 8
  pids_limit: 256
sudo: never
`
	sp, err := buildDocker(dockerCtx(t, yaml))
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if sp.Isolation.NanoCPUs != int64(4*1_000_000_000) {
		t.Fatalf("NanoCPUs = %d, want %d", sp.Isolation.NanoCPUs, int64(4*1_000_000_000))
	}
	if sp.Isolation.Memory != int64(8*1024*1024*1024) {
		t.Fatalf("Memory = %d, want %d", sp.Isolation.Memory, int64(8*1024*1024*1024))
	}
	if sp.Isolation.MemorySwap != sp.Isolation.Memory {
		t.Fatalf("MemorySwap = %d, want defaulted to Memory %d", sp.Isolation.MemorySwap, sp.Isolation.Memory)
	}
	if sp.Isolation.PidsLimit != 256 {
		t.Fatalf("PidsLimit = %d, want 256", sp.Isolation.PidsLimit)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("spec must validate: %v", err)
	}
}

// TestBuildDocker_RawTakesPrecedence pins that the raw cgroup keys
// (nano_cpus/memory) take precedence over the friendly units when both are set.
func TestBuildDocker_RawTakesPrecedence(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  cpu_cores: 4
  memory_gb: 8
  nano_cpus: 2000000000
  memory: 1073741824
  pids_limit: 256
sudo: never
`
	sp, err := buildDocker(dockerCtx(t, yaml))
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if sp.Isolation.NanoCPUs != int64(2_000_000_000) {
		t.Fatalf("NanoCPUs = %d, want %d (raw precedence)", sp.Isolation.NanoCPUs, int64(2_000_000_000))
	}
	if sp.Isolation.Memory != int64(1_073_741_824) {
		t.Fatalf("Memory = %d, want %d (raw precedence)", sp.Isolation.Memory, int64(1_073_741_824))
	}
	if sp.Isolation.MemorySwap != sp.Isolation.Memory {
		t.Fatalf("MemorySwap = %d, want defaulted to Memory %d", sp.Isolation.MemorySwap, sp.Isolation.Memory)
	}
}

// TestBuildDocker_MemorySwapDefaultsToMemory pins that when only raw Memory is
// set (no memory_swap) MemorySwap defaults to Memory so the
// memory_swap >= memory invariant holds with swap disabled.
func TestBuildDocker_MemorySwapDefaultsToMemory(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
sudo: never
`
	sp, err := buildDocker(dockerCtx(t, yaml))
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if sp.Isolation.MemorySwap != sp.Isolation.Memory {
		t.Fatalf("MemorySwap = %d, want Memory %d", sp.Isolation.MemorySwap, sp.Isolation.Memory)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("spec must validate: %v", err)
	}
}

// TestBuildDocker_ExplicitMemorySwapPreserved pins that an explicit memory_swap
// larger than memory survives the defaulting.
func TestBuildDocker_ExplicitMemorySwapPreserved(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  memory_swap: 1073741824
  pids_limit: 256
sudo: never
`
	sp, err := buildDocker(dockerCtx(t, yaml))
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if want := int64(1_073_741_824); sp.Isolation.MemorySwap != want {
		t.Fatalf("MemorySwap = %d, want preserved %d", sp.Isolation.MemorySwap, want)
	}
}

// TestBuildDocker_ProfileOverridesPolicyResources pins that profile-owned
// resources (NanoCPUs/Memory/MemorySwap/PidsLimit/Ulimits/NetworkMode) override
// the immutable policy, while hardening stays from the policy.
func TestBuildDocker_ProfileOverridesPolicyResources(t *testing.T) {
	pol := dockerValidIsolation()
	pol.NanoCPUs = 1e9
	pol.Memory = 512 << 20
	pol.MemorySwap = 512 << 20
	pol.PidsLimit = 256
	pol.Ulimits = []spec.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}

	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: none
cgroups:
  nano_cpus: 2000000000
  memory: 1073741824
  memory_swap: 2147483648
  pids_limit: 512
  ulimits:
    - name: nofile
      soft: 2048
      hard: 2048
sudo: never
`
	ctx := dockerCtxFull(t, yaml, pol, domainprofile.CwdConfig{Mode: "ro"})
	sp, err := buildDocker(ctx)
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
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
	if sp.Isolation.NetworkMode != spec.NetworkNone || sp.Network.Mode != spec.NetworkNone {
		t.Fatalf("network = %+v / isolation %q, want none", sp.Network, sp.Isolation.NetworkMode)
	}
	if sp.Resources.NanoCPUs != 2e9 || sp.Resources.Memory != 1<<30 || sp.Resources.PidsLimit != 512 {
		t.Fatalf("Resources = %+v, want profile values", sp.Resources)
	}
	assertHardeningFromPolicy(t, &sp, pol)
}

// TestBuildDocker_HardeningNeverOverridden pins that a hostile options doc
// attempting to set a root user cannot weaken the enforced policy user, and the
// resulting spec passes EnforceHardening (which re-asserts hardening from pol).
func TestBuildDocker_HardeningNeverOverridden(t *testing.T) {
	pol := dockerValidIsolation()
	pol.User = spec.UIDGID{UID: 1000, GID: 1000}

	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
user:
  uid: 0
  gid: 0
sudo: never
`
	ctx := dockerCtxFull(t, yaml, pol, domainprofile.CwdConfig{Mode: "ro"})
	sp, err := buildDocker(ctx)
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	// The root profile user must NOT override the policy's non-root user.
	if sp.UIDGID.UID == 0 {
		t.Fatalf("UIDGID = %+v, want non-root from policy (profile root rejected)", sp.UIDGID)
	}
	if sp.UIDGID.UID != pol.User.UID || sp.UIDGID.GID != pol.User.GID {
		t.Fatalf("UIDGID = %+v, want pol user %+v", sp.UIDGID, pol.User)
	}
	if err := spec.EnforceHardening(pol, &sp, []string{"/home/user/proj"}); err != nil {
		t.Fatalf("EnforceHardening: %v", err)
	}
	assertHardeningFromPolicy(t, &sp, pol)
}

// TestBuildDocker_CwdWritableWhenModeRW pins the cwd mount honours cwd.mode rw.
func TestBuildDocker_CwdWritableWhenModeRW(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
sudo: never
`
	ctx := dockerCtxFull(t, yaml, dockerValidIsolation(), domainprofile.CwdConfig{Mode: "rw"})
	sp, err := buildDocker(ctx)
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if sp.Mounts[0].ReadOnly {
		t.Fatal("cwd mount must be writable when cwd.mode == rw")
	}
}

// TestBuildDocker_CwdTargetHonored pins the cwd mount honours the declared
// cwd target (source stays the client path, target is the profile target).
func TestBuildDocker_CwdTargetHonored(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
sudo: never
`
	ctx := dockerCtxFull(t, yaml, dockerValidIsolation(), domainprofile.CwdConfig{Target: "/work", Mode: "ro"})
	sp, err := buildDocker(ctx)
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if sp.Mounts[0].Source != "/home/user/proj" || sp.Mounts[0].Target != "/work" {
		t.Fatalf("cwd mount = %+v, want source=client path, target=/work", sp.Mounts[0])
	}
	if sp.WorkingDir != "/work" {
		t.Fatalf("WorkingDir = %q, want /work", sp.WorkingDir)
	}
}

// TestBuildDocker_ShellDefaultBash pins the shell defaulting to bash.
func TestBuildDocker_ShellDefaultBash(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
sudo: never
`
	ctx := dockerCtx(t, yaml)
	ctx.Shell = ""
	sp, err := buildDocker(ctx)
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if sp.Shell != "bash" {
		t.Fatalf("Shell = %q, want default bash", sp.Shell)
	}
}

// TestBuildDocker_UnknownOptionKey_Error pins that an unknown key in the opaque
// options fails closed at build time.
func TestBuildDocker_UnknownOptionKey_Error(t *testing.T) {
	const yaml = `image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
sudo: never
totally_bogus_key: true
`
	if _, err := buildDocker(dockerCtx(t, yaml)); err == nil {
		t.Fatal("expected error for unknown option key")
	}
}

// TestBuildDocker_InvalidImage_Error pins that a malformed image is rejected at
// build time (parse-only validation).
func TestBuildDocker_InvalidImage_Error(t *testing.T) {
	const yaml = `image: "::::invalid"
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  pids_limit: 256
sudo: never
`
	if _, err := buildDocker(dockerCtx(t, yaml)); err == nil {
		t.Fatal("expected error for malformed image")
	}
}

// TestBuildDocker_ResultEnforceHardening pins the choke-point contract: a
// built spec, even one whose profile tries to weaken hardening, ends up with
// the policy's hardening after EnforceHardening and validates.
func TestBuildDocker_ResultEnforceHardening(t *testing.T) {
	pol := dockerValidIsolation()
	const yaml = validDockerOptions
	sp, err := buildDocker(dockerCtxFull(t, yaml, pol, domainprofile.CwdConfig{Mode: "ro"}))
	if err != nil {
		t.Fatalf("buildDocker: %v", err)
	}
	if err := spec.EnforceHardening(pol, &sp, []string{"/home/user/proj", "/data"}); err != nil {
		t.Fatalf("EnforceHardening: %v", err)
	}
	assertHardeningFromPolicy(t, &sp, pol)
	if err := sp.Validate(); err != nil {
		t.Fatalf("post-hardening spec must validate: %v", err)
	}
}
