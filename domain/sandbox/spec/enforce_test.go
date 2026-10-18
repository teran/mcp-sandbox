package spec

import (
	"maps"
	"reflect"
	"slices"
	"testing"
)

// deepCopy returns a deep copy of s so tests can assert the fail-closed
// no-partial-mutation contract: a failing EnforceHardening call must leave the
// spec byte-for-byte unchanged.
func deepCopy(s *SandboxSpec) *SandboxSpec {
	if s == nil {
		return nil
	}
	c := *s
	if s.Env != nil {
		c.Env = make(map[string]string, len(s.Env))
		maps.Copy(c.Env, s.Env)
	}
	c.Mounts = append([]Mount(nil), s.Mounts...)
	c.Secrets = append([]SecretRef(nil), s.Secrets...)
	c.Isolation.CapDrop = append([]string(nil), s.Isolation.CapDrop...)
	c.Isolation.Ulimits = append([]Ulimit(nil), s.Isolation.Ulimits...)
	c.Resources.Ulimits = append([]Ulimit(nil), s.Resources.Ulimits...)
	c.Sandbox.MachLookups = append([]string(nil), s.Sandbox.MachLookups...)
	c.Sandbox.Sysctls = append([]string(nil), s.Sandbox.Sysctls...)

	return &c
}

// assertHardeningMatchesPol verifies every immutable hardening field of the spec
// was re-asserted from pol. It is the single assertion shared by the immutable
// re-assertion tests (Slice 1 contract, re-assert FROM pol).
func assertHardeningMatchesPol(t *testing.T, s *SandboxSpec, pol Isolation) {
	t.Helper()
	if s.Isolation.ReadOnlyRootfs != pol.ReadOnlyRootfs {
		t.Errorf("Isolation.ReadOnlyRootfs = %v, want %v (from pol)", s.Isolation.ReadOnlyRootfs, pol.ReadOnlyRootfs)
	}
	if !slices.Equal(s.Isolation.CapDrop, pol.CapDrop) {
		t.Errorf("Isolation.CapDrop = %v, want %v (from pol)", s.Isolation.CapDrop, pol.CapDrop)
	}
	if s.Isolation.NoNewPrivileges != pol.NoNewPrivileges {
		t.Errorf("Isolation.NoNewPrivileges = %v, want %v (from pol)", s.Isolation.NoNewPrivileges, pol.NoNewPrivileges)
	}
	if s.Isolation.Seccomp != pol.Seccomp {
		t.Errorf("Isolation.Seccomp = %q, want %q (from pol)", s.Isolation.Seccomp, pol.Seccomp)
	}
	if s.Isolation.User != pol.User {
		t.Errorf("Isolation.User = %+v, want %+v (from pol)", s.Isolation.User, pol.User)
	}
	if s.Isolation.TmpfsWorkdir != pol.TmpfsWorkdir {
		t.Errorf("Isolation.TmpfsWorkdir = %v, want %v (from pol)", s.Isolation.TmpfsWorkdir, pol.TmpfsWorkdir)
	}
	if s.Isolation.AutoRemove != pol.AutoRemove {
		t.Errorf("Isolation.AutoRemove = %v, want %v (from pol)", s.Isolation.AutoRemove, pol.AutoRemove)
	}
}

// TestEnforceHardening_ValidSpec verifies that a fully-hardened spec whose mount
// source is allowed by allowDirs passes EnforceHardening with nil error and
// still validates afterwards.
func TestEnforceHardening_ValidSpec(t *testing.T) {
	s := validSpec()
	if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("post-hardening spec must still validate, got: %v", err)
	}
}

// TestEnforceHardening_ReassertsImmutableFields pins the core choke-point: a
// caller/driver that set any immutable hardening field to a weak value must end
// up with the pol value after a successful EnforceHardening.
func TestEnforceHardening_ReassertsImmutableFields(t *testing.T) {
	pol := validIsolation()
	tests := []struct {
		name   string
		weaken func(*SandboxSpec)
	}{
		{"readonly rootfs false", func(s *SandboxSpec) { s.Isolation.ReadOnlyRootfs = false }},
		{"cap drop empty", func(s *SandboxSpec) { s.Isolation.CapDrop = nil }},
		{"cap drop non-ALL", func(s *SandboxSpec) { s.Isolation.CapDrop = []string{"NET_ADMIN"} }},
		{"no new privileges false", func(s *SandboxSpec) { s.Isolation.NoNewPrivileges = false }},
		{"seccomp empty", func(s *SandboxSpec) { s.Isolation.Seccomp = "" }},
		{"user root", func(s *SandboxSpec) { s.Isolation.User = UIDGID{} }},
		{"tmpfs workdir false", func(s *SandboxSpec) { s.Isolation.TmpfsWorkdir = false }},
		{"auto remove false", func(s *SandboxSpec) { s.Isolation.AutoRemove = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.weaken(s)
			if err := EnforceHardening(pol, s, []string{"/host"}); err != nil {
				t.Fatalf("expected nil error, got: %v", err)
			}
			assertHardeningMatchesPol(t, s, pol)
		})
	}
}

// TestEnforceHardening_DoesNotClobberDriverOwned verifies EnforceHardening does
// not overwrite driver/profile-owned fields (UIDGID, Resources, Mounts, Image,
// Env, Network, Sudo, Shell, WorkingDir, Sandbox).
func TestEnforceHardening_DoesNotClobberDriverOwned(t *testing.T) {
	s := validSpec()
	before := deepCopy(s)
	pol := validIsolation()

	if err := EnforceHardening(pol, s, []string{"/host"}); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Driver-owned fields must be preserved exactly (not overwritten from pol).
	if s.Driver != before.Driver {
		t.Errorf("Driver clobbered: %v -> %v", before.Driver, s.Driver)
	}
	if s.Image != before.Image {
		t.Errorf("Image clobbered: %q -> %q", before.Image, s.Image)
	}
	if s.WorkingDir != before.WorkingDir {
		t.Errorf("WorkingDir clobbered: %q -> %q", before.WorkingDir, s.WorkingDir)
	}
	if !reflect.DeepEqual(s.Env, before.Env) {
		t.Errorf("Env clobbered: %v -> %v", before.Env, s.Env)
	}
	if !reflect.DeepEqual(s.Mounts, before.Mounts) {
		t.Errorf("Mounts clobbered: %+v -> %+v", before.Mounts, s.Mounts)
	}
	if !reflect.DeepEqual(s.Resources, before.Resources) {
		t.Errorf("Resources clobbered: %+v -> %+v", before.Resources, s.Resources)
	}
	if s.Network != before.Network {
		t.Errorf("Network clobbered: %+v -> %+v", before.Network, s.Network)
	}
	if s.UIDGID != before.UIDGID {
		t.Errorf("UIDGID clobbered: %+v -> %+v", before.UIDGID, s.UIDGID)
	}
	if s.Sudo != before.Sudo {
		t.Errorf("Sudo clobbered: %q -> %q", before.Sudo, s.Sudo)
	}
	if s.Shell != before.Shell {
		t.Errorf("Shell clobbered: %q -> %q", before.Shell, s.Shell)
	}
	if !reflect.DeepEqual(s.Secrets, before.Secrets) {
		t.Errorf("Secrets clobbered")
	}
	if !reflect.DeepEqual(s.Sandbox, before.Sandbox) {
		t.Errorf("Sandbox clobbered: %+v -> %+v", before.Sandbox, s.Sandbox)
	}
}

// TestEnforceHardening_MountOutsideAllowDirs verifies a mount source not equal
// to nor contained under any allowDir is rejected (fail-closed, S04).
func TestEnforceHardening_MountOutsideAllowDirs(t *testing.T) {
	s := validSpec() // source "/host"
	if err := EnforceHardening(validIsolation(), s, []string{"/other"}); err == nil {
		t.Fatal("expected error for mount source outside allowDirs")
	}
}

// TestEnforceHardening_MountAllowDirBoundaries pins the exact allowDir
// containment semantics: equal to an allowDir or nested under it passes; a
// string prefix without a path-separator boundary ("/data" vs "/database") is
// rejected.
func TestEnforceHardening_MountAllowDirBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		allowDirs []string
		source    string
		wantErr   bool
	}{
		{"equal to allow dir", []string{"/data"}, "/data", false},
		{"nested directly under", []string{"/data"}, "/data/x", false},
		{"nested deep under", []string{"/data"}, "/data/a/b/c", false},
		{"separator boundary sibling", []string{"/data"}, "/database", true},
		{"prefix without separator", []string{"/data"}, "/datax", true},
		{"unrelated", []string{"/data"}, "/other", true},
		{"multi allow dirs picks second", []string{"/a", "/b"}, "/b/c", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSpec()
			s.Mounts = []Mount{{Source: c.source, Target: "/mnt"}}
			err := EnforceHardening(validIsolation(), s, c.allowDirs)
			if c.wantErr && err == nil {
				t.Fatalf("source %q with allowDirs %v: expected error, got nil", c.source, c.allowDirs)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("source %q with allowDirs %v: expected nil, got: %v", c.source, c.allowDirs, err)
			}
		})
	}
}

// TestEnforceHardening_EmptyAllowDirsRejectsMount verifies an empty allowDirs
// list rejects any mount source (fail-closed by default).
func TestEnforceHardening_EmptyAllowDirsRejectsMount(t *testing.T) {
	for _, allowDirs := range [][]string{nil, {}} {
		s := validSpec() // source "/host"
		if err := EnforceHardening(validIsolation(), s, allowDirs); err == nil {
			t.Fatalf("expected error with allowDirs %v (empty must reject any mount)", allowDirs)
		}
	}
}

// TestEnforceHardening_InvalidNetworkMode verifies an unknown network mode is
// rejected (validate, not overwrite).
func TestEnforceHardening_InvalidNetworkMode(t *testing.T) {
	s := validSpec()
	s.Isolation.NetworkMode = NetworkMode("vlan")
	if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err == nil {
		t.Fatal("expected error for invalid network mode")
	}
}

// TestEnforceHardening_InvalidSeccompProfile pins R-3: an unknown seccomp profile
// on the incoming spec is REJECTED (validate, not overwrite) — even though
// EnforceHardening would otherwise re-assert Seccomp from pol. A builder/driver
// that tried to set an invalid profile must fail closed rather than be silently
// patched to the default.
func TestEnforceHardening_InvalidSeccompProfile(t *testing.T) {
	s := validSpec()
	s.Isolation.Seccomp = SeccompProfile("unconfined")
	if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err == nil {
		t.Fatal("expected error for invalid seccomp profile")
	}
}

// TestEnforceHardening_UnboundedResources verifies bounded cgroups are validated
// (not forced): PidsLimit/Memory/NanoCPUs must be > 0.
func TestEnforceHardening_UnboundedResources(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*SandboxSpec)
	}{
		{"pids limit zero", func(s *SandboxSpec) { s.Isolation.PidsLimit = 0 }},
		{"memory zero", func(s *SandboxSpec) { s.Isolation.Memory = 0 }},
		{"nano cpus zero", func(s *SandboxSpec) { s.Isolation.NanoCPUs = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSpec()
			tt.mut(s)
			if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}
}

// TestEnforceHardening_ShellControlChars verifies the Shell rejects NUL and
// newline (S11, mirroring profile.validateCommon).
func TestEnforceHardening_ShellControlChars(t *testing.T) {
	for _, shell := range []string{"bash\x00arg", "bash\narg"} {
		s := validSpec()
		s.Shell = shell
		if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err == nil {
			t.Fatalf("expected error for shell %q", shell)
		}
	}
}

// TestEnforceHardening_SBPLNUL verifies the Seatbelt SBPL rejects a NUL byte
// (SPEC §7.12, mirroring spec.Validate).
func TestEnforceHardening_SBPLNUL(t *testing.T) {
	s := validSpec()
	s.Sandbox.SBPL = "(version 1)\x00junk"
	if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err == nil {
		t.Fatal("expected error for SBPL containing NUL")
	}
}

// TestEnforceHardening_EmptyMountSourceTarget verifies a mount with an empty
// (or blank) source or target is rejected (post-build Validate).
func TestEnforceHardening_EmptyMountSourceTarget(t *testing.T) {
	badMounts := []Mount{
		{Source: "", Target: "/mnt"},
		{Source: "   ", Target: "/mnt"},
		{Source: "/host", Target: ""},
		{Source: "/host", Target: "  "},
	}
	for _, m := range badMounts {
		s := validSpec()
		s.Mounts = []Mount{m}
		if err := EnforceHardening(validIsolation(), s, []string{"/host"}); err == nil {
			t.Fatalf("expected error for mount %+v", m)
		}
	}
}

// TestEnforceHardening_TraversalRejected (F1, HIGH) pins the fail-closed
// traversal defense in allowedSource: a mount source (and every allowDir) must
// be an ABSOLUTE path with NO "." or ".." path component. Without this, a
// hostile model-host can pass cwd=/allowed/../.ssh or a mount source like
// /allowed/sub/../../etc, which lexically passes the ALLOW_DIRS gate, defeats
// the Docker backend's TOCTOU-only verifyHostPath, and bind-mounts a host path
// OUTSIDE ALLOW_DIRS into the sandbox (confidentiality/integrity breach of host
// files, defeats S04/N03). Mirrors the darwin backend (hasDotComponent +
// isSubpath in domain/sandbox/sbpl/sbpl.go).
func TestEnforceHardening_TraversalRejected(t *testing.T) {
	cases := []struct {
		name      string
		allowDirs []string
		source    string
		wantErr   bool
	}{
		// The reported exploit: ".." escapes an allowed root into .ssh.
		{"dotdot into hidden dir", []string{"/allowed"}, "/allowed/../.ssh", true},
		// Deep escape through multiple ".." components.
		{"deep traversal escape", []string{"/allowed"}, "/allowed/sub/../../etc", true},
		// Bare ".." as the final component escapes to the parent of the root.
		{"dotdot at end", []string{"/allowed"}, "/allowed/..", true},
		// Relative source with a ".." component must never be admitted.
		{"relative dotdot", []string{"/allowed"}, "..", true},
		// A relative "." source is ambiguous and must be rejected.
		{"dot source", []string{"/allowed"}, ".", true},
		// Non-absolute sources must always be rejected (require absolute path).
		{"relative non-absolute", []string{"/allowed"}, "relative/x", true},
		// An allowDir that itself contains a ".." component is malformed and must
		// never admit anything — not even a source that lexically nests under it.
		{"malformed allowDir with dotdot", []string{"/allowed/.."}, "/allowed/../x", true},

		// --- Negative controls (regression; must STILL pass) ---
		{"equal to allow dir", []string{"/allowed"}, "/allowed", false},
		{"nested directly under", []string{"/allowed"}, "/allowed/x", false},
		{"nested deep under", []string{"/allowed"}, "/allowed/a/b", false},
		// Trailing slash is normalized away and remains a valid, in-scope source.
		{"trailing slash normalized", []string{"/allowed"}, "/allowed/", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSpec()
			s.Mounts = []Mount{{Source: c.source, Target: "/mnt"}}
			err := EnforceHardening(validIsolation(), s, c.allowDirs)
			if c.wantErr && err == nil {
				t.Fatalf("source %q with allowDirs %v: expected error (traversal/ambiguous path), got nil", c.source, c.allowDirs)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("source %q with allowDirs %v: expected nil, got: %v", c.source, c.allowDirs, err)
			}
		})
	}
}

// TestEnforceHardening_NoPartialMutationOnError pins the fail-closed contract:
// on a failing call EnforceHardening must NOT mutate the spec at all — not even
// the immutable hardening fields it would otherwise re-assert.
func TestEnforceHardening_NoPartialMutationOnError(t *testing.T) {
	s := validSpec()
	// Weaken a hardening field; on failure it must NOT be patched back to pol.
	s.Isolation.ReadOnlyRootfs = false
	before := deepCopy(s)

	// Failing call: mount source "/host" is outside allowDirs.
	if err := EnforceHardening(validIsolation(), s, []string{"/other"}); err == nil {
		t.Fatal("expected error (mount outside allowDirs)")
	}
	if !reflect.DeepEqual(s, before) {
		t.Fatal("EnforceHardening mutated the spec on a failing call")
	}
}
