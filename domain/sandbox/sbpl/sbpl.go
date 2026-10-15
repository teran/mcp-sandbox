// Package sbpl renders a deterministic Seatbelt (SBPL) profile from a typed
// SandboxSpec for the darwin / sandbox-exec backend (SPEC §7.12). It is a pure,
// fail-closed function: hostile input yields an error, never a partial profile
// with injected tokens.
package sbpl

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// ErrSBPLMetachar is the sentinel returned when a value contains a prohibited
// SBPL metacharacter that could terminate the profile string and inject a rule
// (SPEC §7.12). Callers detect it with errors.Is.
var ErrSBPLMetachar = errors.New("sbpl: value contains a prohibited SBPL metacharacter")

// sbplMetachars is the exact set of characters that would terminate or corrupt
// an SBPL string literal: double quote, backslash, parentheses, semicolon, and
// the control characters newline, carriage return and NUL.
const sbplMetachars = "\"\\();\n\r\x00"

// ValidatePath reports whether p is safe to embed as an SBPL subpath literal:
// it must be non-empty and absolute and free of every SBPL metacharacter.
// Metacharacters are rejected even in a non-absolute string (they are the
// injection vector, independent of absoluteness).
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("sbpl: empty path")
	}
	if strings.ContainsAny(p, sbplMetachars) {
		return fmt.Errorf("sbpl: path %q contains a prohibited SBPL metacharacter: %w", p, ErrSBPLMetachar)
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("sbpl: path %q is not absolute", p)
	}

	return nil
}

// ValidateMachName reports whether n is a safe Mach service name: it must be
// non-empty and contain only [A-Za-z0-9._-] (reverse-DNS style, no slash,
// space or SBPL metacharacter).
func ValidateMachName(n string) error {
	if n == "" {
		return fmt.Errorf("sbpl: empty mach lookup name")
	}
	for _, r := range n {
		if !isMachNameRune(r) {
			return fmt.Errorf("sbpl: mach lookup name %q contains invalid character %q: %w", n, r, ErrSBPLMetachar)
		}
	}

	return nil
}

// ValidateSysctlName reports whether k is a safe sysctl key: it must be
// non-empty and contain only [A-Za-z0-9._] (no dash, unlike a mach name).
func ValidateSysctlName(k string) error {
	if k == "" {
		return fmt.Errorf("sbpl: empty sysctl name")
	}
	for _, r := range k {
		if !isSysctlNameRune(r) {
			return fmt.Errorf("sbpl: sysctl name %q contains invalid character %q: %w", k, r, ErrSBPLMetachar)
		}
	}

	return nil
}

// Generate renders a deterministic SBPL profile from sp (SPEC §7.12). It is a
// pure, fail-closed function: it validates every literal first and returns an
// error on the first hostile value (never a partial profile). Sections are
// emitted in a fixed order: imports → (deny default) → network → mach → sysctl
// → file → operator seatbelt.sbpl (last, verbatim). The input spec is never
// mutated.
func Generate(sp spec.SandboxSpec, allowDirs []string, scratchDir string) (string, error) {
	if err := validateInputs(sp, scratchDir); err != nil {
		return "", err
	}

	var b strings.Builder

	b.WriteString("(import \"system.sb\")\n")
	b.WriteString("(deny default)\n")

	switch sp.Isolation.NetworkMode {
	case spec.NetworkNone:
		b.WriteString("(deny network*)\n")
	case spec.NetworkBridge, spec.NetworkHost:
		b.WriteString("(allow network-outbound)\n")
	default:
		return "", fmt.Errorf("sbpl: unsupported network mode %q", sp.Isolation.NetworkMode)
	}

	b.WriteString("(deny mach-lookup)\n")
	for _, n := range sortedCopy(sp.Sandbox.MachLookups) {
		fmt.Fprintf(&b, "(allow mach-lookup (global-name %q))\n", n)
	}

	b.WriteString("(deny sysctl*)\n")
	for _, k := range sortedCopy(sp.Sandbox.Sysctls) {
		fmt.Fprintf(&b, "(allow sysctl-read (sysctl-name %q))\n", k)
	}

	emitFileSection(&b, sp, allowDirs, scratchDir)

	if sp.Sandbox.SBPL != "" {
		b.WriteString(sp.Sandbox.SBPL)
		b.WriteString("\n")
	}

	return b.String(), nil
}

// validateInputs runs every literal through its validator before any SBPL is
// generated (fail-closed, fail-fast): the first hostile value aborts the whole
// projection. Paths are validated even when they fall outside ALLOW_DIRS —
// validation is primary, the ALLOW_DIRS filter is applied afterwards.
func validateInputs(sp spec.SandboxSpec, scratchDir string) error {
	for _, m := range sp.Mounts {
		if err := ValidatePath(m.Source); err != nil {
			return err
		}
	}
	if sp.WorkingDir != "" {
		if err := ValidatePath(sp.WorkingDir); err != nil {
			return err
		}
	}
	if scratchDir != "" {
		if err := ValidatePath(scratchDir); err != nil {
			return err
		}
	}
	for _, n := range sp.Sandbox.MachLookups {
		if err := ValidateMachName(n); err != nil {
			return err
		}
	}
	for _, k := range sp.Sandbox.Sysctls {
		if err := ValidateSysctlName(k); err != nil {
			return err
		}
	}

	return nil
}

// emitFileSection writes the file projection: a file-read* rule for every path
// (mount source / working dir) inside ALLOW_DIRS or equal to the scratch dir,
// and a file-write* rule for read-write mounts, a writable working dir and the
// scratch dir. The working dir is read-only by default (least-privilege, SPEC
// §7.2/§7.12); it becomes writable only when the matching cwd mount entry is
// read-write (cwd.mode=rw). Non-ALLOW_DIRS paths are silently skipped
// (deny-by-default), never an error. The scratch dir is always projected
// read-write.
func emitFileSection(b *strings.Builder, sp spec.SandboxSpec, allowDirs []string, scratchDir string) {
	allowed := sortedCopy(allowDirs)

	var writable []string
	emit := func(p string, w bool) {
		fmt.Fprintf(b, "(allow file-read* (subpath %q))\n", p)
		if w {
			writable = append(writable, p)
		}
	}

	for _, m := range sortedMounts(sp.Mounts) {
		if inAnyDir(m.Source, allowed) || (scratchDir != "" && m.Source == scratchDir) {
			emit(m.Source, !m.ReadOnly)
		}
	}

	if sp.WorkingDir != "" && (inAnyDir(sp.WorkingDir, allowed) || sp.WorkingDir == scratchDir) {
		// cwd read-only by default (SPEC §7.2/§7.12); honour cwd.mode=rw via
		// the matching cwd mount entry (mirrors docker.go resolveMounts). This
		// block also serves as the fallback for direct callers that set
		// WorkingDir without a corresponding cwd mount — those stay read-only.
		cwdWritable := false
		for _, m := range sp.Mounts {
			if m.Target == sp.WorkingDir {
				cwdWritable = !m.ReadOnly

				break
			}
		}
		emit(sp.WorkingDir, cwdWritable)
	}

	if scratchDir != "" {
		// The sanctioned per-session scratch dir is always writable.
		emit(scratchDir, true)
	}

	for _, p := range dedupe(writable) {
		fmt.Fprintf(b, "(allow file-write* (subpath %q))\n", p)
	}
}

func inAnyDir(path string, dirs []string) bool {
	for _, d := range dirs {
		if isSubpath(path, d) {
			return true
		}
	}

	return false
}

// isSubpath reports whether path is root itself or a true descendant of root
// (a sibling sharing only a string prefix is not a subpath). It is pure string
// logic with no filesystem access and is deliberately fail-closed: it never
// over-approximates containment. The generator does NOT trust non-canonicalized
// paths (S04) — if either argument carries a "." or ".." component the canonical
// containment is ambiguous (EvalSymlinks is the backend's responsibility), so it
// returns false and the path is skipped (deny-by-default). A single trailing
// slash on root (other than "/") is normalized away, and the root "/" is treated
// as containing every absolute path.
func isSubpath(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	if !strings.HasPrefix(path, "/") || !strings.HasPrefix(root, "/") {
		return false
	}
	if hasDotComponent(path) || hasDotComponent(root) {
		return false
	}
	if root == "/" {
		return strings.HasPrefix(path, "/")
	}
	root = strings.TrimSuffix(root, "/")

	return path == root || strings.HasPrefix(path, root+"/")
}

// hasDotComponent reports whether p contains a "." or ".." path component, the
// markers of a non-canonicalized (ambiguous) path that the pure generator cannot
// resolve without filesystem access.
func hasDotComponent(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}

	return false
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)

	return out
}

func sortedMounts(in []spec.Mount) []spec.Mount {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b spec.Mount) int {
		return strings.Compare(a.Source, b.Source)
	})

	return out
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	return out
}

func isMachNameRune(r rune) bool {
	return r == '.' || r == '_' || r == '-' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isSysctlNameRune(r rune) bool {
	return r == '.' || r == '_' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
