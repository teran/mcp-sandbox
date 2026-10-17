package spec

import (
	"fmt"
	"strings"
)

// EnforceHardening re-asserts the server's immutable Isolation policy on a
// built SandboxSpec and gates every host mount source by ALLOW_DIRS (S04).
// It is a pure domain function: no driver, no IO. A spec that a builder/caller
// tried to weaken, or that fails post-build Validate, is REJECTED (fail-closed),
// never silently patched.
//
// The checks run in order: every validation runs BEFORE any mutation, so a
// failing call leaves the spec byte-for-byte unchanged (no partial mutation).
// Only after all checks pass are the 7 immutable hardening fields overwritten
// FROM pol; bounded/network/driver-owned fields are never touched.
func EnforceHardening(pol Isolation, s *SandboxSpec, allowDirs []string) error {
	// 1. Validate first (no mutation yet) — fail-closed on anything a
	//    builder/caller tried to weaken.
	if strings.ContainsAny(s.Shell, "\x00\n") {
		return fmt.Errorf("sandbox: Shell must not contain control characters (NUL or newline)")
	}
	if strings.ContainsRune(s.Sandbox.SBPL, '\x00') {
		return fmt.Errorf("sandbox: Sandbox.SBPL must not contain NUL")
	}
	if !IsValidNetworkMode(s.Isolation.NetworkMode) {
		return fmt.Errorf("sandbox: Isolation.NetworkMode %q is not a valid network mode", s.Isolation.NetworkMode)
	}
	if s.Isolation.PidsLimit <= 0 {
		return fmt.Errorf("sandbox: Isolation.PidsLimit must be bounded (> 0)")
	}
	if s.Isolation.Memory <= 0 {
		return fmt.Errorf("sandbox: Isolation.Memory must be bounded (> 0)")
	}
	if s.Isolation.NanoCPUs <= 0 {
		return fmt.Errorf("sandbox: Isolation.NanoCPUs must be bounded (> 0)")
	}

	// ALLOW_DIRS gate (S04): every mount source must equal an allowed dir or be
	// contained under one at a path-separator boundary. Empty/nil allowDirs
	// rejects all mounts (fail-closed by default).
	for i, m := range s.Mounts {
		if !allowedSource(m.Source, allowDirs) {
			return fmt.Errorf("sandbox: mount %d source %q is outside the allowed dirs", i, m.Source)
		}
	}

	// 2. All checks passed: re-assert the 7 immutable hardening fields FROM pol.
	iso := &s.Isolation
	iso.ReadOnlyRootfs = pol.ReadOnlyRootfs
	iso.CapDrop = pol.CapDrop
	iso.NoNewPrivileges = pol.NoNewPrivileges
	iso.Seccomp = pol.Seccomp
	iso.User = pol.User
	iso.TmpfsWorkdir = pol.TmpfsWorkdir
	iso.AutoRemove = pol.AutoRemove

	// 3. Final guard: post-build Validate (catches empty mount source/target and
	//    re-validates after re-assertion).
	return s.Validate()
}

// allowedSource reports whether source is equal to an allowed dir or is
// contained under one at a path-separator boundary (so "/data" does not admit
// "/database" or "/datax").
func allowedSource(source string, allowDirs []string) bool {
	for _, dir := range allowDirs {
		if source == dir || strings.HasPrefix(source, dir+"/") {
			return true
		}
	}

	return false
}
