// Package sbpl_test supplements the @qa contract tests with additional
// coverage for branches not exercised there (empty names, the unknown-network
// fail-closed branch, the working-dir projection and scratchDir deduplication),
// to keep package coverage above the 95% gate (C01). These tests pin the same
// public contract and are equally fail-closed.
package sbpl_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/sbpl"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// TestValidateMachName_Empty pins that an empty mach lookup name is rejected.
func TestValidateMachName_Empty(t *testing.T) {
	if err := sbpl.ValidateMachName(""); err == nil {
		t.Fatal("ValidateMachName(\"\") = nil, want error")
	}
}

// TestValidateSysctlName_Empty pins that an empty sysctl key is rejected.
func TestValidateSysctlName_Empty(t *testing.T) {
	if err := sbpl.ValidateSysctlName(""); err == nil {
		t.Fatal("ValidateSysctlName(\"\") = nil, want error")
	}
}

// TestValidatePath_EmptyError pins the empty-path branch yields a non-nil error.
func TestValidatePath_EmptyError(t *testing.T) {
	if err := sbpl.ValidatePath(""); err == nil {
		t.Fatal("ValidatePath(\"\") = nil, want error")
	}
}

// TestValidateMachName_NonMetacharCharset pins that characters outside the
// [A-Za-z0-9._-] charset are rejected (not only SBPL metacharacters).
func TestValidateMachName_NonMetacharCharset(t *testing.T) {
	for _, in := range []string{"com/apple", "com apple", "com@apple", "com+apple"} {
		if err := sbpl.ValidateMachName(in); err == nil {
			t.Errorf("ValidateMachName(%q) = nil, want error", in)
		}
	}
}

// TestValidateSysctlName_NonMetacharCharset pins that the dash and other
// out-of-charset characters are rejected for a sysctl key.
func TestValidateSysctlName_NonMetacharCharset(t *testing.T) {
	for _, in := range []string{"kern-osversion", "kern/osversion", "kern os"} {
		if err := sbpl.ValidateSysctlName(in); err == nil {
			t.Errorf("ValidateSysctlName(%q) = nil, want error", in)
		}
	}
}

// TestGenerate_UnknownNetwork_Error pins fail-closed on an unknown network mode:
// the generator returns an error and no partial SBPL.
func TestGenerate_UnknownNetwork_Error(t *testing.T) {
	sp := validSpec()
	sp.Isolation.NetworkMode = spec.NetworkMode("bogus")

	out, err := sbpl.Generate(sp, nil, "")
	if err == nil {
		t.Fatal("Generate(unknown network) = nil, want error")
	}
	if out != "" {
		t.Fatalf("Generate(unknown network) returned partial SBPL %q, want empty", out)
	}
}

// TestGenerate_WorkingDir_InsideAllowDirs pins that a WorkingDir inside
// ALLOW_DIRS with no matching cwd mount is projected read-only by default
// (least-privilege, SPEC §7.2/§7.12): file-read* without file-write*.
func TestGenerate_WorkingDir_InsideAllowDirs(t *testing.T) {
	sp := validSpec()
	sp.WorkingDir = "/work"

	out, err := sbpl.Generate(sp, []string{"/work"}, "")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	if !strings.Contains(out, `(allow file-read* (subpath "/work"))`) {
		t.Errorf("want file-read* for working dir /work in:\n%s", out)
	}
	if strings.Contains(out, `(allow file-write* (subpath "/work"))`) {
		t.Errorf("working dir without cwd.mode=rw must not emit file-write*:\n%s", out)
	}
}

// TestGenerate_WorkingDir_RWDerivedFromCwdMount pins that the working-dir write
// permission is derived from the matching cwd mount entry (mirrors docker.go
// resolveMounts): a read-only cwd mount yields file-read* only, a read-write
// cwd mount (cwd.mode=rw) yields file-write* too — and never from an unrelated
// read-write mount.
func TestGenerate_WorkingDir_RWDerivedFromCwdMount(t *testing.T) {
	t.Run("cwd mount read only", func(t *testing.T) {
		sp := validSpec()
		sp.WorkingDir = "/work"
		sp.Mounts = []spec.Mount{
			{Source: "/work", Target: "/work", ReadOnly: true},
			{Source: "/data", Target: "/data", ReadOnly: false}, // unrelated rw mount
		}

		out, err := sbpl.Generate(sp, []string{"/work", "/data"}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, `(allow file-read* (subpath "/work"))`) {
			t.Errorf("want file-read* for working dir /work in:\n%s", out)
		}
		if strings.Contains(out, `(allow file-write* (subpath "/work"))`) {
			t.Errorf("read-only cwd mount must not emit file-write* for /work:\n%s", out)
		}
	})

	t.Run("cwd mount read write", func(t *testing.T) {
		sp := validSpec()
		sp.WorkingDir = "/work"
		sp.Mounts = []spec.Mount{{Source: "/work", Target: "/work", ReadOnly: false}}

		out, err := sbpl.Generate(sp, []string{"/work"}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if !strings.Contains(out, `(allow file-read* (subpath "/work"))`) {
			t.Errorf("want file-read* for working dir /work in:\n%s", out)
		}
		if !strings.Contains(out, `(allow file-write* (subpath "/work"))`) {
			t.Errorf("want file-write* for rw cwd mount /work in:\n%s", out)
		}
	})
}

// TestGenerate_WorkingDir_OutsideAllowDirs_Skipped pins that a WorkingDir not
// inside ALLOW_DIRS is skipped (deny-by-default), never an error.
func TestGenerate_WorkingDir_OutsideAllowDirs_Skipped(t *testing.T) {
	sp := validSpec()
	sp.WorkingDir = "/elsewhere"

	out, err := sbpl.Generate(sp, []string{"/work"}, "")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	if strings.Contains(out, "/elsewhere") {
		t.Fatalf("non-ALLOW_DIRS working dir leaked into SBPL:\n%s", out)
	}
}

// TestGenerate_MountEqualsScratchDir pins that a mount source equal to the
// scratch dir is projected read-write exactly once (no duplicate file-write*
// subpath), while remaining read-only for other mounts.
func TestGenerate_MountEqualsScratchDir(t *testing.T) {
	sp := validSpec()
	sp.Mounts = []spec.Mount{
		{Source: "/scratch", Target: "/mnt", ReadOnly: false},
		{Source: "/data", Target: "/d", ReadOnly: true},
	}

	out, err := sbpl.Generate(sp, []string{"/scratch", "/data"}, "/scratch")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	if n := strings.Count(out, `(allow file-write* (subpath "/scratch"))`); n != 1 {
		t.Fatalf("file-write* for /scratch count = %d, want exactly 1 (dedup) in:\n%s", n, out)
	}
	if strings.Contains(out, `(allow file-write* (subpath "/data"))`) {
		t.Fatalf("read-only /data mount must not emit file-write*:\n%s", out)
	}
}

// TestGenerate_ScratchDir_ReadOnlyMount pins that even a read-only mount whose
// source equals the scratch dir still yields a file-write* for the scratch dir
// (the sanctioned scratch is always writable).
func TestGenerate_ScratchDir_ReadOnlyMount(t *testing.T) {
	sp := validSpec()
	sp.Mounts = []spec.Mount{{Source: "/scratch", Target: "/mnt", ReadOnly: true}}

	out, err := sbpl.Generate(sp, nil, "/scratch")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	if !strings.Contains(out, `(allow file-write* (subpath "/scratch"))`) {
		t.Fatalf("want file-write* for scratch dir /scratch in:\n%s", out)
	}
}

// TestGenerate_HostileScratchDir_Error pins fail-closed on a hostile scratch
// dir (metacharacter).
func TestGenerate_HostileScratchDir_Error(t *testing.T) {
	sp := validSpec()

	_, err := sbpl.Generate(sp, nil, `/scratch") (allow file-write*) ;`)
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(hostile scratch) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_HostileWorkingDir_Error pins fail-closed on a hostile working
// dir (metacharacter) even though it lies outside ALLOW_DIRS.
func TestGenerate_HostileWorkingDir_Error(t *testing.T) {
	sp := validSpec()
	sp.WorkingDir = `/work") (allow file-write*) ;`

	_, err := sbpl.Generate(sp, []string{"/safe"}, "")
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(hostile working dir) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_MountHostileOutsideAllowDirs_Error pins that a hostile mount
// source is rejected by validation even when it lies outside ALLOW_DIRS
// (validation is primary, the filter is secondary).
func TestGenerate_MountHostileOutsideAllowDirs_Error(t *testing.T) {
	sp := validSpec()
	sp.Mounts = []spec.Mount{{Source: `/outside") ;`, Target: "/mnt", ReadOnly: false}}

	_, err := sbpl.Generate(sp, []string{"/host"}, "")
	if !errors.Is(err, sbpl.ErrSBPLMetachar) {
		t.Fatalf("Generate(hostile non-allowed mount) err = %v, want ErrSBPLMetachar", err)
	}
}

// TestGenerate_OperatorSBPL_Empty pins that an empty operator SBPL yields no
// trailing operator section (only the server-hardening sections remain).
func TestGenerate_OperatorSBPL_Empty(t *testing.T) {
	out, err := sbpl.Generate(validSpec(), nil, "")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	if strings.Contains(out, "(allow process-fork)") {
		t.Fatalf("empty operator SBPL must not be emitted:\n%s", out)
	}
}

// TestGenerate_AllowDirs_TraversalBypass pins the fail-closed contract (S04)
// that the generator does NOT trust non-canonicalized paths: a mount source
// carrying a "." or ".." component is never treated as inside ALLOW_DIRS, even
// when its string prefix lies within an allowed dir. The backend owns
// canonicalization (EvalSymlinks); the generator only ever projects unambiguous
// paths. An ambiguous path is silently skipped (deny-by-default), never an
// error.
func TestGenerate_AllowDirs_TraversalBypass(t *testing.T) {
	// /data/../etc string-prefixes into /data but canonicalizes to /etc — the
	// canonical path escapes ALLOW_DIRS, so it must not be projected.
	t.Run("parent traversal", func(t *testing.T) {
		sp := validSpec()
		sp.Mounts = []spec.Mount{{Source: "/data/../etc", Target: "/mnt", ReadOnly: false}}

		out, err := sbpl.Generate(sp, []string{"/data"}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if strings.Contains(out, "(allow file") {
			t.Fatalf("non-canonicalized traversal mount must be skipped (fail-closed):\n%s", out)
		}
		if strings.Contains(out, "/data/../etc") {
			t.Fatalf("traversal mount source leaked into SBPL:\n%s", out)
		}
	})

	t.Run("current dir component", func(t *testing.T) {
		sp := validSpec()
		sp.Mounts = []spec.Mount{{Source: "/data/./etc", Target: "/mnt", ReadOnly: false}}

		out, err := sbpl.Generate(sp, []string{"/data"}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if strings.Contains(out, "(allow file") {
			t.Fatalf("non-canonicalized . mount must be skipped (fail-closed):\n%s", out)
		}
	})

	t.Run("dot allow dir", func(t *testing.T) {
		sp := validSpec()
		sp.Mounts = []spec.Mount{{Source: "/data/foo", Target: "/mnt", ReadOnly: true}}

		out, err := sbpl.Generate(sp, []string{"/data/.."}, "")
		if err != nil {
			t.Fatalf("Generate err = %v, want nil", err)
		}
		if strings.Contains(out, "(allow file") {
			t.Fatalf("ambiguous allow dir must match nothing (fail-closed):\n%s", out)
		}
	})
}

// TestGenerate_AllowDirs_Normalization pins the ALLOW_DIRS root normalization
// and absolute-path contract: a single trailing slash on the allow dir (e.g.
// "/data/") is treated identically to "/data", the root "/" contains every
// absolute path, and relative or empty allow dirs match nothing (fail-closed).
func TestGenerate_AllowDirs_Normalization(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		source string
		emit   bool
	}{
		{"trailing slash allows subpath", "/data/", "/data/foo", true},
		{"trailing slash equals root", "/data/", "/data", true},
		{"root slash contains all", "/", "/anything/deep", true},
		{"root slash equals root", "/", "/", true},
		{"relative root matches nothing", "data", "/data", false},
		{"empty root matches nothing", "", "/data", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := validSpec()
			sp.Mounts = []spec.Mount{{Source: tt.source, Target: "/mnt", ReadOnly: true}}

			out, err := sbpl.Generate(sp, []string{tt.root}, "")
			if err != nil {
				t.Fatalf("Generate(root=%q) err = %v, want nil", tt.root, err)
			}
			subpath := `(subpath "` + tt.source + `")`
			has := strings.Contains(out, subpath)
			if has != tt.emit {
				t.Errorf("Generate(root=%q, source=%q) subpath present = %v, want %v in:\n%s", tt.root, tt.source, has, tt.emit, out)
			}
		})
	}
}

// TestGenerate_SecretsNotLeaked pins S02: sensitive spec fields — Env values,
// Secrets, Sudo and UIDGID — never appear in the emitted SBPL. The Seatbelt
// generator only projects isolation/network/mach/sysctl/file/operator-SBPL
// sections; secret-bearing fields are structurally excluded from the output.
func TestGenerate_SecretsNotLeaked(t *testing.T) {
	sp := validSpec()
	sp.Env = map[string]string{"TOKEN": "super-secret-env-value", "KEY": "another-secret-value"}
	sp.Secrets = []spec.SecretRef{
		{Name: "db_pw", Value: "super-secret-db-password"},
		{Name: "api_key", Value: "super-secret-api-token"},
	}
	sp.Sudo = spec.SudoRequire
	sp.UIDGID = spec.UIDGID{UID: 12345, GID: 54321}

	out, err := sbpl.Generate(sp, nil, "")
	if err != nil {
		t.Fatalf("Generate err = %v, want nil", err)
	}
	for _, sensitive := range []string{
		"super-secret-env-value", "another-secret-value",
		"super-secret-db-password", "super-secret-api-token",
		"db_pw", "api_key",
		"TOKEN", "KEY",
		"require",        // Sudo policy must not be projected
		"12345", "54321", // UIDGID must not be projected
	} {
		if strings.Contains(out, sensitive) {
			t.Errorf("sensitive value %q leaked into SBPL:\n%s", sensitive, out)
		}
	}
}
