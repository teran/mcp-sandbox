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
	// R-3: reject an invalid seccomp profile BEFORE re-asserting Seccomp from
	// pol (line 54 would otherwise overwrite it before the final Validate). A
	// non-empty unknown profile ("unconfined", "custom", ...) is an explicit
	// request that fails closed — never silently patched to the default. An
	// EMPTY seccomp is a weak/unspecified value that is re-asserted from pol,
	// exactly like the other immutable hardening fields (see
	// TestEnforceHardening_ReassertsImmutableFields); Validate() alone still
	// rejects empty via IsValidSeccompProfile.
	if s.Isolation.Seccomp != "" && !IsValidSeccompProfile(s.Isolation.Seccomp) {
		return fmt.Errorf("sandbox: Isolation.Seccomp %q is not a valid seccomp profile", s.Isolation.Seccomp)
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
// "/database" or "/datax"). It is pure string logic with no filesystem access
// and is deliberately fail-closed (S04/N03): it never over-approximates
// containment.
//
// A source that is not absolute, or that carries a "." or ".." path component,
// is ambiguous (a non-canonicalized path has no reliable containment without
// filesystem access) and is REJECTED — mirroring hasDotComponent/isSubpath in
// the darwin backend (S04). A malformed allowDir (non-absolute or with a "." /
// ".." component) is skipped and can never admit anything (a hostile/malformed
// allowDir must never widen the gate). A single trailing slash on an allowDir
// (other than "/") is normalized away so "/allowed/" matches "/allowed", and
// the root "/" is treated as containing every absolute path.
func allowedSource(source string, allowDirs []string) bool {
	if !strings.HasPrefix(source, "/") || hasDotComponent(source) {
		return false
	}

	for _, dir := range allowDirs {
		// Skip malformed allowDirs: they must never admit anything.
		if !strings.HasPrefix(dir, "/") || hasDotComponent(dir) {
			continue
		}
		// The root contains every absolute path (consistent with isSubpath).
		if dir == "/" {
			return true
		}
		dir = strings.TrimSuffix(dir, "/")

		if source == dir || strings.HasPrefix(source, dir+"/") {
			return true
		}
	}

	return false
}

// hasDotComponent reports whether p contains a "." or ".." path component, the
// markers of a non-canonicalized (ambiguous) path that the pure gate cannot
// resolve without filesystem access. Mirrors domain/sandbox/sbpl/sbpl.go.
func hasDotComponent(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}

	return false
}
