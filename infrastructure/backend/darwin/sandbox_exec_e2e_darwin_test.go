//go:build e2e && darwin

package darwin

// Real smoke tests of /usr/bin/sandbox-exec (Seatbelt) on a live macOS host
// (SPEC §7.12). They are build-tagged `e2e && darwin`: they run only under
// `make e2e` (`go test -tags e2e ./...`) on a macOS runner and are excluded
// from the default unit pass and from Linux. They use the REAL realRunner and
// /usr/bin/sandbox-exec, exercising the genuine seatbelt path.
//
// NOTE: these run on a real macOS host (goldie.local) and use the REAL
// sandbox-exec. The exact SBPL strings and the deny marker in the child's
// stderr can vary across macOS versions, so the assertions are deliberately
// kept to the robust signals (exit code and IsPolicyDenial) and the SBPL may
// need live tuning on the host.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSandboxExecE2E_Allow spawns an allowed echo and asserts a clean exit with
// the expected stdout and no policy denial.
func TestSandboxExecE2E_Allow(t *testing.T) {
	if _, err := os.Stat(sandboxExecPath); err != nil {
		t.Skipf("sandbox-exec not present: %v", err)
	}

	sbpl := `(version 1)(allow default)`
	r := realRunner{}

	p, err := r.Start(context.Background(), ProcessSpec{
		Argv: []string{sandboxExecPath, "-p", sbpl, "/bin/echo", "hello"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0; stderr = %q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Fatalf("Stdout = %q, want it to contain %q", res.Stdout, "hello")
	}
	if IsPolicyDenial(res) {
		t.Fatalf("IsPolicyDenial = true for an allowed echo, want false")
	}
}

// TestSandboxExecE2E_Deny spawns a cat under a profile that denies file-read*
// under /Users and asserts the command fails non-zero AND IsPolicyDenial
// reports it as a policy denial (SPEC §7.12).
func TestSandboxExecE2E_Deny(t *testing.T) {
	if _, err := os.Stat(sandboxExecPath); err != nil {
		t.Skipf("sandbox-exec not present: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("cannot resolve home dir: %v", err)
	}

	// A real, existing path under /Users so `cat` reaches the policy check
	// rather than failing with "no such file". It lives in a dedicated temp
	// subdirectory of the home dir (removed wholesale via t.Cleanup, so a
	// hard-killed run leaves no stray file in $HOME), and stays under the
	// /Users deny scope.
	dir, err := os.MkdirTemp(home, "sbx-e2e-*")
	if err != nil {
		t.Skipf("cannot create temp dir under %s: %v", home, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	target := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	sbpl := `(version 1)(allow default)(deny file-read* (subpath "/Users"))`
	r := realRunner{}

	p, err := r.Start(context.Background(), ProcessSpec{
		Argv: []string{sandboxExecPath, "-p", sbpl, "/bin/cat", target},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	res, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode == 0 {
		t.Fatalf("ExitCode = 0 for a denied cat, want non-zero; stdout = %q", res.Stdout)
	}
	if !IsPolicyDenial(res) {
		t.Fatalf("IsPolicyDenial = false for a denied cat; stderr = %q", res.Stderr)
	}
}
