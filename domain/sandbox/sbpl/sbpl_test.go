// Package sbpl_test defines the public contract of the Seatbelt (SBPL)
// projection generator (SPEC §7.12) via TDD (T01): the package does not exist
// yet — these tests pin its exact API and fail-closed security invariants.
//
// All tests are pure Go and fully portable: they run in CI on any OS (the SBPL
// generator is a deterministic pure function SandboxSpec → SBPL, no
// sandbox-exec binary is invoked).
package sbpl_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/sbpl"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// validSpec builds a minimal valid SandboxSpec for sbpl.Generate. Only the
// fields Generate reads are populated (SPEC §7.12); the rest are zero values.
// sbpl.Generate is called directly, so the stricter spec.Validate invariants
// (non-root Isolation etc.) are irrelevant here.
func validSpec() spec.SandboxSpec {
	return spec.SandboxSpec{
		Isolation: spec.Isolation{NetworkMode: spec.NetworkNone},
	}
}

// fullSpec builds a spec where every projected section carries content, used by
// the section-order and deny-default placement tests.
func fullSpec() spec.SandboxSpec {
	sp := validSpec()
	sp.Isolation.NetworkMode = spec.NetworkBridge
	sp.WorkingDir = "/work"
	sp.Sandbox = spec.Sandbox{
		MachLookups: []string{"com.apple.system.logger"},
		Sysctls:     []string{"kern.osversion"},
		SBPL:        "(allow process-fork)",
	}
	sp.Mounts = []spec.Mount{{Source: "/data", Target: "/mnt", ReadOnly: false}}

	return sp
}

// --- ValidatePath ------------------------------------------------------------

// TestValidatePath_RejectEachMetachar pins the fail-closed metacharacter
// rejection of ValidatePath: every SBPL metacharacter (" \ ( ) ; \n \r \0) is
// rejected individually with the ErrSBPLMetachar sentinel.
func TestValidatePath_RejectEachMetachar(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"double quote", "/a\"b"},
		{"backslash", `/a\b`},
		{"open paren", "/a(b"},
		{"close paren", "/a)b"},
		{"semicolon", "/a;b"},
		{"newline", "/a\nb"},
		{"carriage return", "/a\rb"},
		{"nul", "/a\x00b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sbpl.ValidatePath(tt.in)
			if !errors.Is(err, sbpl.ErrSBPLMetachar) {
				t.Fatalf("ValidatePath(%q) err = %v, want ErrSBPLMetachar", tt.in, err)
			}
		})
	}
}

// TestValidatePath_RejectNonAbsolute pins that a relative path and the empty
// string are rejected (fail-closed): the generator only ever emits absolute
// subpaths.
func TestValidatePath_RejectNonAbsolute(t *testing.T) {
	for _, in := range []string{"", "relative/path", "Users/foo"} {
		if err := sbpl.ValidatePath(in); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want error", in)
		}
	}
}

// TestValidatePath_RejectHostileInjection pins the SPEC §7.12 hostile example:
// a crafted value that would terminate the SBPL string and inject an
// (allow file-write*) token must be rejected.
func TestValidatePath_RejectHostileInjection(t *testing.T) {
	const hostile = `") (allow file-write*) ;"`
	if err := sbpl.ValidatePath(hostile); !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("ValidatePath(%q) err = %v, want ErrSBPLMetachar", hostile, err)
	}
}

// TestValidatePath_RejectBackslashEscapes pins that backslash-prefixed quotes
// do NOT smuggle past the validator: a string containing " or \ is still
// rejected.
func TestValidatePath_RejectBackslashEscapes(t *testing.T) {
	for _, in := range []string{`a\"b`, `a\\b`} {
		if err := sbpl.ValidatePath(in); !errors.Is(err, sbpl.ErrSBPLMetachar) {
			t.Errorf("ValidatePath(%q) err = %v, want ErrSBPLMetachar", in, err)
		}
	}
}

// TestValidatePath_AcceptValid pins that legitimate absolute paths — including
// spaces and non-ASCII (unicode) characters that are not SBPL metacharacters —
// are accepted.
func TestValidatePath_AcceptValid(t *testing.T) {
	for _, in := range []string{
		"/Users/foo/bar",
		"/Users/foo bar/baz", // space is not an SBPL metacharacter
		"/Users/foo/бар",     // unicode is allowed
		"/café/menu",
	} {
		if err := sbpl.ValidatePath(in); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", in, err)
		}
	}
}

// --- ValidateMachName --------------------------------------------------------

// TestValidateMachName_Reject pins the [A-Za-z0-9._-] charset: `/` and space
// are rejected as invalid characters, and the SBPL metacharacters are rejected
// with ErrSBPLMetachar.
func TestValidateMachName_Reject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"slash", "com/apple", nil}, // invalid char, not a metachar
		{"space", "com apple", nil}, // invalid char, not a metachar
		{"open paren", "com.apple(a", sbpl.ErrSBPLMetachar},
		{"close paren", "com.apple)a", sbpl.ErrSBPLMetachar},
		{"double quote", `com.apple"a`, sbpl.ErrSBPLMetachar},
		{"semicolon", "com.apple;a", sbpl.ErrSBPLMetachar},
		{"newline", "com.apple\n", sbpl.ErrSBPLMetachar},
		{"nul", "com.apple\x00", sbpl.ErrSBPLMetachar},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sbpl.ValidateMachName(tt.in)
			if err == nil {
				t.Fatalf("ValidateMachName(%q) = nil, want error", tt.in)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("ValidateMachName(%q) err = %v, want %v", tt.in, err, tt.want)
			}
		})
	}
}

// TestValidateMachName_Accept pins a valid mach service name.
func TestValidateMachName_Accept(t *testing.T) {
	for _, in := range []string{"com.apple.system.logger", "com.apple.audio", "x.y-z.1"} {
		if err := sbpl.ValidateMachName(in); err != nil {
			t.Errorf("ValidateMachName(%q) = %v, want nil", in, err)
		}
	}
}

// --- ValidateSysctlName ------------------------------------------------------

// TestValidateSysctlName_Reject pins the [A-Za-z0-9._] charset: the dash (which
// IS allowed in a mach name) and `/` are rejected as invalid characters, and
// the SBPL metacharacters are rejected with ErrSBPLMetachar.
func TestValidateSysctlName_Reject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"dash", "kern-osversion", nil}, // dash invalid in sysctl charset
		{"slash", "kern/osversion", nil},
		{"open paren", "kern.os(a", sbpl.ErrSBPLMetachar},
		{"close paren", "kern.os)a", sbpl.ErrSBPLMetachar},
		{"double quote", `kern.os"a`, sbpl.ErrSBPLMetachar},
		{"semicolon", "kern.os;a", sbpl.ErrSBPLMetachar},
		{"newline", "kern.os\n", sbpl.ErrSBPLMetachar},
		{"nul", "kern.os\x00", sbpl.ErrSBPLMetachar},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := sbpl.ValidateSysctlName(tt.in)
			if err == nil {
				t.Fatalf("ValidateSysctlName(%q) = nil, want error", tt.in)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("ValidateSysctlName(%q) err = %v, want %v", tt.in, err, tt.want)
			}
		})
	}
}

// TestValidateSysctlName_Accept pins a valid sysctl key.
func TestValidateSysctlName_Accept(t *testing.T) {
	for _, in := range []string{"kern.osversion", "net.inet.ip.ttl"} {
		if err := sbpl.ValidateSysctlName(in); err != nil {
			t.Errorf("ValidateSysctlName(%q) = %v, want nil", in, err)
		}
	}
}

// --- Generate ----------------------------------------------------------------

// TestGenerate_DenyDefault_EmptySandbox pins that even a completely empty
// sandbox (no mounts, no mach/sysctl, no operator SBPL) yields a profile that
// contains exactly one (deny default), placed right after the imports, and no
// (allow ...) at all — the deny-by-default posture is unstrippable.
func TestGenerate_DenyDefault_EmptySandbox(t *testing.T) {
	out, err := sbpl.Generate(validSpec(), nil, "")
	if err != nil {
		t.Fatalf("Generate(empty) err = %v, want nil", err)
	}

	if n := strings.Count(out, "(deny default)"); n != 1 {
		t.Fatalf("(deny default) count = %d, want exactly 1 in:\n%s", n, out)
	}
	idxImport := strings.Index(out, "(import \"system.sb\")")
	idxDeny := strings.Index(out, "(deny default)")
	if idxImport == -1 || idxDeny == -1 || idxImport > idxDeny {
		t.Fatalf("(deny default) must follow imports; import@%d deny@%d in:\n%s", idxImport, idxDeny, out)
	}
	if strings.Contains(out, "(allow") {
		t.Fatalf("empty sandbox must emit no (allow ...) rule, got:\n%s", out)
	}
}

// TestGenerate_DenyDefault_BeforeFirstAllow pins that (deny default) appears
// exactly once and strictly before the first (allow ...) in a fully-populated
// sandbox.
func TestGenerate_DenyDefault_BeforeFirstAllow(t *testing.T) {
	sp := fullSpec()
	out, err := sbpl.Generate(sp, []string{"/data"}, "/scratch")
	if err != nil {
		t.Fatalf("Generate(full) err = %v, want nil", err)
	}

	if n := strings.Count(out, "(deny default)"); n != 1 {
		t.Fatalf("(deny default) count = %d, want exactly 1 in:\n%s", n, out)
	}
	idxDeny := strings.Index(out, "(deny default)")
	idxAllow := strings.Index(out, "(allow ")
	if idxDeny == -1 || idxAllow == -1 {
		t.Fatalf("missing deny or allow in:\n%s", out)
	}
	if idxDeny > idxAllow {
		t.Fatalf("(deny default)@%d must precede first (allow @%d in:\n%s", idxDeny, idxAllow, out)
	}
}

// TestGenerate_SectionOrder pins the fixed projection order via positional
// asserts: imports → (deny default) → network → mach → sysctl → file →
// operator seatbelt.sbpl (last).
func TestGenerate_SectionOrder(t *testing.T) {
	sp := fullSpec()
	out, err := sbpl.Generate(sp, []string{"/data"}, "/scratch")
	if err != nil {
		t.Fatalf("Generate(full) err = %v, want nil", err)
	}

	markers := []string{
		`(import "system.sb")`,
		"(deny default)",
		"(allow network-outbound)", // bridge network
		"(deny mach-lookup)",       // mach section header
		"(deny sysctl*)",           // sysctl section header
		"(allow file-read*",        // file section
		"(allow process-fork)",     // operator seatbelt.sbpl, last
	}
	prev := -1
	for _, m := range markers {
		idx := strings.Index(out, m)
		if idx == -1 {
			t.Fatalf("marker %q not found in:\n%s", m, out)
		}
		if idx <= prev {
			t.Fatalf("marker %q@%d not strictly after previous@%d in:\n%s", m, idx, prev, out)
		}
		prev = idx
	}
}

// TestGenerate_HostileMountPath_Error pins fail-closed: a mount source inside
// ALLOW_DIRS containing an SBPL metacharacter yields an error, never a partial
// SBPL with injected tokens.
func TestGenerate_HostileMountPath_Error(t *testing.T) {
	sp := validSpec()
	// Absolute path inside allowDirs but carrying an SBPL metacharacter.
	sp.Mounts = []spec.Mount{{Source: `/host") (allow file-write*) ;`, Target: "/mnt", ReadOnly: false}}

	_, err := sbpl.Generate(sp, []string{"/host"}, "")
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(hostile mount) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_HostileMachLookup_Error pins fail-closed on a hostile mach
// service name.
func TestGenerate_HostileMachLookup_Error(t *testing.T) {
	sp := validSpec()
	sp.Sandbox.MachLookups = []string{`com.apple") (allow file-write*) ;`}

	_, err := sbpl.Generate(sp, nil, "")
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(hostile mach) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_HostileSysctl_Error pins fail-closed on a hostile sysctl key.
func TestGenerate_HostileSysctl_Error(t *testing.T) {
	sp := validSpec()
	sp.Sandbox.Sysctls = []string{`kern.os) (allow file-write*) ;`}

	_, err := sbpl.Generate(sp, nil, "")
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(hostile sysctl) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_NulInPath_Error pins that a NUL byte in a mount source is
// rejected (it would terminate the SBPL string).
func TestGenerate_NulInPath_Error(t *testing.T) {
	sp := validSpec()
	sp.Mounts = []spec.Mount{{Source: "/host/a\x00b", Target: "/mnt", ReadOnly: false}}

	_, err := sbpl.Generate(sp, []string{"/host"}, "")
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(NUL mount) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_NonAllowedDirMount_Skipped pins that a mount whose source is not
// inside ALLOW_DIRS is silently skipped (no subpath emitted) and is NOT an
// error — deny-by-default is preserved.
func TestGenerate_NonAllowedDirMount_Skipped(t *testing.T) {
	sp := validSpec()
	sp.Mounts = []spec.Mount{{Source: "/outside", Target: "/mnt", ReadOnly: false}}

	out, err := sbpl.Generate(sp, []string{"/host"}, "")
	if err != nil {
		t.Fatalf("Generate(non-allowed mount) err = %v, want nil", err)
	}
	if strings.Contains(out, "/outside") {
		t.Fatalf("non-ALLOW_DIRS mount source leaked into SBPL:\n%s", out)
	}
	if strings.Contains(out, "(allow file-read*") {
		t.Fatalf("non-ALLOW_DIRS mount emitted a file-read rule:\n%s", out)
	}
}

// TestGenerate_Network pins the network projection: none → (deny network*);
// bridge/host → (allow network-outbound).
func TestGenerate_Network(t *testing.T) {
	tests := []struct {
		name string
		mode spec.NetworkMode
		want string
	}{
		{"none", spec.NetworkNone, "(deny network*)"},
		{"bridge", spec.NetworkBridge, "(allow network-outbound)"},
		{"host", spec.NetworkHost, "(allow network-outbound)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := validSpec()
			sp.Isolation.NetworkMode = tt.mode

			out, err := sbpl.Generate(sp, nil, "")
			if err != nil {
				t.Fatalf("Generate(network=%s) err = %v, want nil", tt.mode, err)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("network=%s: want %q in:\n%s", tt.mode, tt.want, out)
			}
			if tt.mode == spec.NetworkNone && strings.Contains(out, "network-outbound") {
				t.Errorf("network=none must not allow outbound:\n%s", out)
			}
			if tt.mode != spec.NetworkNone && strings.Contains(out, "(deny network*)") {
				t.Errorf("network=%s must not deny network:\n%s", tt.mode, out)
			}
		})
	}
}

// TestGenerate_Mach pins the mach projection: empty → only (deny mach-lookup);
// allow-list → (deny mach-lookup) plus one (allow mach-lookup (global-name ...))
// per name.
func TestGenerate_Mach(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out, err := sbpl.Generate(validSpec(), nil, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, "(deny mach-lookup)") {
			t.Errorf("want (deny mach-lookup) in:\n%s", out)
		}
		if strings.Contains(out, "(allow mach-lookup") {
			t.Errorf("empty mach list must not emit (allow mach-lookup):\n%s", out)
		}
	})

	t.Run("allow list", func(t *testing.T) {
		sp := validSpec()
		sp.Sandbox.MachLookups = []string{"com.apple.system.logger", "com.apple.audio"}

		out, err := sbpl.Generate(sp, nil, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, "(deny mach-lookup)") {
			t.Errorf("want (deny mach-lookup) in:\n%s", out)
		}
		for _, n := range sp.Sandbox.MachLookups {
			want := `(allow mach-lookup (global-name "` + n + `"))`
			if !strings.Contains(out, want) {
				t.Errorf("want %q in:\n%s", want, out)
			}
		}
	})
}

// TestGenerate_Sysctl pins the sysctl projection: empty → only (deny sysctl*);
// allow-list → (deny sysctl*) plus one (allow sysctl-read (sysctl-name ...))
// per key.
func TestGenerate_Sysctl(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out, err := sbpl.Generate(validSpec(), nil, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, "(deny sysctl*)") {
			t.Errorf("want (deny sysctl*) in:\n%s", out)
		}
		if strings.Contains(out, "(allow sysctl-read") {
			t.Errorf("empty sysctl list must not emit (allow sysctl-read):\n%s", out)
		}
	})

	t.Run("allow list", func(t *testing.T) {
		sp := validSpec()
		sp.Sandbox.Sysctls = []string{"kern.osversion", "net.inet.ip.ttl"}

		out, err := sbpl.Generate(sp, nil, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, "(deny sysctl*)") {
			t.Errorf("want (deny sysctl*) in:\n%s", out)
		}
		for _, s := range sp.Sandbox.Sysctls {
			want := `(allow sysctl-read (sysctl-name "` + s + `"))`
			if !strings.Contains(out, want) {
				t.Errorf("want %q in:\n%s", want, out)
			}
		}
	})
}

// TestGenerate_File pins the file projection: a read-only mount emits only
// file-read*; a read-write mount emits file-read* plus file-write*.
func TestGenerate_File(t *testing.T) {
	t.Run("read only", func(t *testing.T) {
		sp := validSpec()
		sp.Mounts = []spec.Mount{{Source: "/data", Target: "/mnt", ReadOnly: true}}

		out, err := sbpl.Generate(sp, []string{"/data"}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, `(allow file-read* (subpath "/data"))`) {
			t.Errorf("want file-read* subpath for /data in:\n%s", out)
		}
		if strings.Contains(out, "file-write*") {
			t.Errorf("read-only mount must not emit file-write*:\n%s", out)
		}
	})

	t.Run("read write", func(t *testing.T) {
		sp := validSpec()
		sp.Mounts = []spec.Mount{{Source: "/data", Target: "/mnt", ReadOnly: false}}

		out, err := sbpl.Generate(sp, []string{"/data"}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, `(allow file-read* (subpath "/data"))`) {
			t.Errorf("want file-read* subpath for /data in:\n%s", out)
		}
		if !strings.Contains(out, `(allow file-write* (subpath "/data"))`) {
			t.Errorf("want file-write* subpath for /data in:\n%s", out)
		}
	})
}

// TestGenerate_ScratchDir pins that the sanctioned per-session scratchDir always
// yields a file-write* rule (even alongside read-only mounts).
func TestGenerate_ScratchDir(t *testing.T) {
	sp := validSpec()
	sp.Mounts = []spec.Mount{{Source: "/data", Target: "/mnt", ReadOnly: true}}

	out, err := sbpl.Generate(sp, []string{"/data"}, "/scratch")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	if !strings.Contains(out, `(allow file-write* (subpath "/scratch"))`) {
		t.Errorf("want file-write* subpath for scratchDir /scratch in:\n%s", out)
	}
}

// TestGenerate_OperatorSBPL_Last pins that the operator seatbelt.sbpl is merged
// last (after the file section) and verbatim: it is the final section of the
// emitted profile.
func TestGenerate_OperatorSBPL_Last(t *testing.T) {
	sp := fullSpec()
	const operatorSBPL = "(allow process-fork)"
	sp.Sandbox.SBPL = operatorSBPL

	out, err := sbpl.Generate(sp, []string{"/data"}, "/scratch")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}

	if !strings.Contains(out, operatorSBPL) {
		t.Fatalf("operator SBPL %q not emitted verbatim in:\n%s", operatorSBPL, out)
	}
	idxOp := strings.Index(out, operatorSBPL)
	idxFile := strings.Index(out, "(allow file-read*")
	if idxOp == -1 || idxFile == -1 || idxOp <= idxFile {
		t.Fatalf("operator SBPL@%d must follow file section@%d in:\n%s", idxOp, idxFile, out)
	}
	// Operator SBPL is the final section: only trailing whitespace may follow it.
	if trimmed := strings.TrimRight(out[idxOp+len(operatorSBPL):], " \t\r\n"); trimmed != "" {
		t.Fatalf("operator SBPL must be the last section, trailing content: %q", trimmed)
	}
}

// TestGenerate_Deterministic pins byte-for-byte determinism: identical inputs —
// including shuffled order of Mounts / MachLookups / Sysctls — produce identical
// output across calls.
func TestGenerate_Deterministic(t *testing.T) {
	allowDirs := []string{"/data", "/logs"}
	scratch := "/scratch"

	base := validSpec()
	base.Isolation.NetworkMode = spec.NetworkBridge
	base.Sandbox = spec.Sandbox{
		MachLookups: []string{"com.apple.a", "com.apple.b", "com.apple.c"},
		Sysctls:     []string{"kern.os", "net.inet", "hw.memsize"},
	}
	base.Mounts = []spec.Mount{
		{Source: "/data", Target: "/m1", ReadOnly: false},
		{Source: "/logs", Target: "/m2", ReadOnly: true},
	}

	shuffled := base
	shuffled.Sandbox.MachLookups = []string{"com.apple.c", "com.apple.a", "com.apple.b"}
	shuffled.Sandbox.Sysctls = []string{"hw.memsize", "kern.os", "net.inet"}
	shuffled.Mounts = []spec.Mount{
		{Source: "/logs", Target: "/m2", ReadOnly: true},
		{Source: "/data", Target: "/m1", ReadOnly: false},
	}

	out1, err1 := sbpl.Generate(base, allowDirs, scratch)
	out2, err2 := sbpl.Generate(base, allowDirs, scratch)
	out3, err3 := sbpl.Generate(shuffled, allowDirs, scratch)
	out4, err4 := sbpl.Generate(base, []string{"/logs", "/data"}, scratch) // shuffled allowDirs
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		t.Fatalf("Generate errs = %v, %v, %v, %v; want all nil", err1, err2, err3, err4)
	}
	if out1 != out2 {
		t.Fatalf("same input produced different output across calls")
	}
	if out1 != out3 {
		t.Fatalf("shuffled element order changed the output:\nbase:\n%s\nshuffled:\n%s", out1, out3)
	}
	if out1 != out4 {
		t.Fatalf("shuffled allowDirs order changed the output:\nbase:\n%s\nshuffled:\n%s", out1, out4)
	}
}

// TestGenerate_AllowDirsBoundary pins the ALLOW_DIRS boundary semantics: a
// source equal to the root and a true subpath are emitted; a sibling that only
// shares a string prefix (and an unrelated path) are skipped.
func TestGenerate_AllowDirsBoundary(t *testing.T) {
	allowDirs := []string{"/data"}

	tests := []struct {
		name   string
		source string
		emit   bool
	}{
		{"root itself", "/data", true},
		{"subpath of root", "/data/sub/dir", true},
		{"prefix-sibling", "/data2", false}, // /data2 is NOT under /data
		{"unrelated", "/etc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := validSpec()
			sp.Mounts = []spec.Mount{{Source: tt.source, Target: "/mnt", ReadOnly: true}}

			out, err := sbpl.Generate(sp, allowDirs, "")
			if err != nil {
				t.Fatalf("Generate(%s) err = %v, want nil", tt.source, err)
			}
			subpath := `(subpath "` + tt.source + `")`
			has := strings.Contains(out, subpath)
			if has != tt.emit {
				t.Errorf("Generate(%s) subpath present = %v, want %v in:\n%s", tt.source, has, tt.emit, out)
			}
		})
	}
}
