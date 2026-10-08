//go:build e2e

// Package contract provides a backend-independent e2e contract suite that
// verifies the sandbox role interfaces against any backend (SPEC §4/§7,
// T02/C09GO). The docker harness in e2e/docker runs it against the real Docker
// backend; the suite is shaped so a future backend (gVisor, Apple Container)
// can run it unchanged.
//
// The package is build-tagged `e2e` so the whole e2e tree is excluded from the
// default unit/coverage pass (make test, T02/N30) and runs only through
// `make e2e` / the e2e CI job.
//
// The suite is deliberately decoupled from any concrete backend: it drives only
// the role interfaces from domain/sandbox/role and the typed types from
// domain/sandbox. Scenarios that require a capability the target backend does
// not advertise are skipped (t.Skip), and copy scenarios are skipped when no
// allowed host directory is provided, so the same suite remains runnable across
// backends of differing abilities.
package contract

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// Contract bundles the role interfaces the suite drives and the backend's
// capability advertisement. The docker harness fills it with the real backend;
// scenarios skip when a required capability is absent.
type Contract struct {
	Lifecycle      role.Lifecycle
	Executable     role.Executable
	Introspectable role.Introspectable

	// AllowDir is a host directory that lies within the backend's ALLOW_DIRS
	// (SPEC §7.2). The mount scenario uses it; when empty that scenario is
	// skipped.
	AllowDir string
}

// Command paths the exec scenarios use. They must exist in the base image the
// harness provides (a busybox-based image such as alpine).
const (
	binSh        = "/bin/sh"
	binPwd       = "/bin/pwd"
	binPrintenv  = "/bin/printenv"
	binTrue      = "/bin/true"
	usrBinPrintf = "/usr/bin/printf"
	usrBinID     = "/usr/bin/id"
)

// RunContractSuite runs the full cross-backend contract suite. mkSpec returns a
// valid spec satisfying spec.Validate (the suite clones and extends it per
// scenario). The default writable workdir is assumed to be a tmpfs (SPEC §7.1);
// the server contract requires it.
func RunContractSuite(t *testing.T, c Contract, mkSpec func() spec.SandboxSpec) {
	t.Helper()

	t.Run("lifecycle", func(t *testing.T) {
		if c.Lifecycle == nil {
			t.Skip("backend has no Lifecycle capability")
		}
		base := mkSpec()
		runLifecycle(t, c, base)
	})

	t.Run("exec", func(t *testing.T) {
		if c.Lifecycle == nil || c.Executable == nil {
			t.Skip("backend lacks Lifecycle or Executable capability")
		}
		runExec(t, c, mkSpec())
	})

	t.Run("mount-immutability", func(t *testing.T) {
		if c.Lifecycle == nil {
			t.Skip("backend has no Lifecycle capability")
		}
		if c.AllowDir == "" {
			t.Skip("no allowed host directory provided (mount scenario needs ALLOW_DIRS)")
		}
		runMountImmutability(t, c, mkSpec())
	})

	t.Run("uid-gid", func(t *testing.T) {
		if c.Lifecycle == nil || c.Executable == nil {
			t.Skip("backend lacks Lifecycle or Executable capability")
		}
		runUIDGID(t, c, mkSpec())
	})

	t.Run("concurrency", func(t *testing.T) {
		if c.Lifecycle == nil || c.Executable == nil {
			t.Skip("backend lacks Lifecycle or Executable capability")
		}
		runConcurrency(t, c, mkSpec())
	})

	t.Run("introspect", func(t *testing.T) {
		if c.Lifecycle == nil || c.Introspectable == nil {
			t.Skip("backend lacks Lifecycle or Introspectable capability")
		}
		runIntrospect(t, c, mkSpec())
	})

	t.Run("cleanup", func(t *testing.T) {
		if c.Lifecycle == nil {
			t.Skip("backend has no Lifecycle capability")
		}
		runCleanup(t, c, mkSpec())
	})
}

// newID returns an opaque, unpredictable id for one sandbox instance. The id is
// threaded through the context so the backend can label the container by it
// (label-based ownership, SPEC §7.8).
func newID() string { return uuid.NewString() }

// create builds a sandbox for the given spec, returns its session id and
// registers a cleanup that destroys it (idempotent, so an explicit destroy in a
// scenario is safe).
func create(t *testing.T, c Contract, s spec.SandboxSpec) string {
	t.Helper()
	id := newID()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := c.Lifecycle.Create(domainsession.WithSessionID(ctx, id), s); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = c.Lifecycle.Destroy(cctx, id)
	})
	return id
}

// execCmd runs a command and fails the test on error.
func execCmd(t *testing.T, c Contract, id string, req exec.Request) *exec.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := c.Executable.Exec(ctx, id, req)
	if err != nil {
		t.Fatalf("exec %v: %v", req.Argv, err)
	}
	return res
}

// ---------------------------------------------------------------------------
// lifecycle
// ---------------------------------------------------------------------------

func runLifecycle(t *testing.T, c Contract, base spec.SandboxSpec) {
	t.Run("create-and-destroy", func(t *testing.T) {
		id := create(t, c, base)
		if c.Introspectable != nil {
			if _, err := c.Introspectable.Info(context.Background(), id); err != nil {
				t.Fatalf("Info after create: %v", err)
			}
		}
		if err := destroy(t, c, id); err != nil {
			t.Fatalf("destroy: %v", err)
		}
		// After destroy the sandbox is gone: a further destroy is a no-op and
		// any role call against the id fails with not-found.
		if err := destroy(t, c, id); err != nil {
			t.Fatalf("idempotent destroy: %v", err)
		}
		if c.Introspectable != nil {
			if _, err := c.Introspectable.Info(context.Background(), id); err == nil {
				t.Fatal("Info after destroy should fail (not found)")
			}
		}
	})

	t.Run("destroy-not-found-is-noop", func(t *testing.T) {
		if err := destroy(t, c, newID()); err != nil {
			t.Fatalf("destroy of unknown id should be a no-op, got %v", err)
		}
	})

	t.Run("distinct-ids", func(t *testing.T) {
		id1 := create(t, c, base)
		id2 := create(t, c, base)
		if id1 == id2 {
			t.Fatal("two creates must return distinct ids")
		}
	})
}

func destroy(t *testing.T, c Contract, id string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Lifecycle.Destroy(ctx, id)
}

// ---------------------------------------------------------------------------
// exec
// ---------------------------------------------------------------------------

func runExec(t *testing.T, c Contract, base spec.SandboxSpec) {
	t.Run("exit-code", func(t *testing.T) {
		id := create(t, c, base)
		res := execCmd(t, c, id, exec.Request{Argv: []string{binSh, "-c", "exit 7"}})
		if res.ExitCode != 7 {
			t.Fatalf("exit code = %d, want 7", res.ExitCode)
		}
	})

	t.Run("stdout-stderr", func(t *testing.T) {
		id := create(t, c, base)
		res := execCmd(t, c, id, exec.Request{Argv: []string{binSh, "-c", "echo out; echo err >&2"}})
		if got := string(res.Stdout); got != "out\n" {
			t.Fatalf("stdout = %q, want %q", got, "out\n")
		}
		if got := string(res.Stderr); got != "err\n" {
			t.Fatalf("stderr = %q, want %q", got, "err\n")
		}
	})

	t.Run("args-array-no-shell", func(t *testing.T) {
		id := create(t, c, base)
		// printf is invoked with a single argv entry that contains a space; a
		// shell would split it, an argument array must not.
		res := execCmd(t, c, id, exec.Request{Argv: []string{usrBinPrintf, "%s\n", "one two"}})
		if got := string(res.Stdout); got != "one two\n" {
			t.Fatalf("stdout = %q, want %q", got, "one two\n")
		}
	})

	t.Run("cwd", func(t *testing.T) {
		id := create(t, c, base)
		res := execCmd(t, c, id, exec.Request{Argv: []string{binPwd}, WorkingDir: "/"})
		if got := string(res.Stdout); got != "/\n" {
			t.Fatalf("cwd = %q, want %q", got, "/\n")
		}
	})

	t.Run("env", func(t *testing.T) {
		s := base
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env["CONTRACT_ENV"] = "contract-value"
		id := create(t, c, s)
		res := execCmd(t, c, id, exec.Request{Argv: []string{binPrintenv, "CONTRACT_ENV"}})
		if got := string(res.Stdout); got != "contract-value\n" {
			t.Fatalf("env = %q, want %q", got, "contract-value\n")
		}
	})

	t.Run("not-found-session", func(t *testing.T) {
		if _, err := c.Executable.Exec(context.Background(), newID(), exec.Request{Argv: []string{binTrue}}); err == nil {
			t.Fatal("exec against unknown session should fail (not found)")
		}
	})
}

// ---------------------------------------------------------------------------
// mount-immutability
// ---------------------------------------------------------------------------

func runMountImmutability(t *testing.T, c Contract, base spec.SandboxSpec) {
	// Different mounts must yield different, independent instances (SPEC §6.6
	// "Mounts are immutable"). Two sandboxes with distinct mount targets are
	// distinct containers.
	s1 := base
	s1.Mounts = []spec.Mount{{Source: c.AllowDir, Target: "/mnt-one", ReadOnly: true}}
	s2 := base
	s2.Mounts = []spec.Mount{{Source: c.AllowDir, Target: "/mnt-two", ReadOnly: true}}

	id1 := create(t, c, s1)
	id2 := create(t, c, s2)
	if id1 == id2 {
		t.Fatal("different mounts must produce distinct instances")
	}
	if c.Introspectable != nil {
		if _, err := c.Introspectable.Info(context.Background(), id1); err != nil {
			t.Fatalf("Info(id1): %v", err)
		}
		if _, err := c.Introspectable.Info(context.Background(), id2); err != nil {
			t.Fatalf("Info(id2): %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// uid/gid mapping
// ---------------------------------------------------------------------------

func runUIDGID(t *testing.T, c Contract, base spec.SandboxSpec) {
	if base.Isolation.User.UID == 0 || base.Isolation.User.GID == 0 {
		t.Skip("mkSpec does not declare a non-root user")
	}

	t.Run("default-non-root-user", func(t *testing.T) {
		id := create(t, c, base)
		res := execCmd(t, c, id, exec.Request{Argv: []string{usrBinID, "-u"}})
		if got := trimNl(res.Stdout); got != strconv.FormatUint(uint64(base.Isolation.User.UID), 10) {
			t.Fatalf("uid = %q, want %q", got, strconv.FormatUint(uint64(base.Isolation.User.UID), 10))
		}
		gid := execCmd(t, c, id, exec.Request{Argv: []string{usrBinID, "-g"}})
		if got := trimNl(gid.Stdout); got != strconv.FormatUint(uint64(base.Isolation.User.GID), 10) {
			t.Fatalf("gid = %q, want %q", got, strconv.FormatUint(uint64(base.Isolation.User.GID), 10))
		}
	})

	t.Run("scoped-root-user", func(t *testing.T) {
		id := create(t, c, base)
		res := execCmd(t, c, id, exec.Request{Argv: []string{usrBinID, "-u"}, User: "0"})
		if got := trimNl(res.Stdout); got != "0" {
			t.Fatalf("uid under scoped root = %q, want 0", got)
		}
	})
}

func trimNl(b []byte) string {
	s := string(b)
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}

// ---------------------------------------------------------------------------
// concurrency (X05)
// ---------------------------------------------------------------------------

func runConcurrency(t *testing.T, c Contract, base spec.SandboxSpec) {
	const workers = 8
	id := create(t, c, base)

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Each worker runs a distinct command concurrently against the same
			// sandbox; all must succeed with correct output (no partial/corrupt
			// state, X05).
			res, err := c.Executable.Exec(context.Background(), id, exec.Request{
				Argv: []string{usrBinPrintf, "%s\n", strconv.Itoa(n)},
			})
			if err != nil {
				errs <- err
				return
			}
			if got := trimNl(res.Stdout); got != strconv.Itoa(n) {
				errs <- &mismatchError{got: got, want: strconv.Itoa(n)}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

type mismatchError struct{ got, want string }

func (e *mismatchError) Error() string { return "concurrent exec: got " + e.got + ", want " + e.want }

// ---------------------------------------------------------------------------
// introspect
// ---------------------------------------------------------------------------

func runIntrospect(t *testing.T, c Contract, base spec.SandboxSpec) {
	id := create(t, c, base)

	t.Run("info", func(t *testing.T) {
		info, err := c.Introspectable.Info(context.Background(), id)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if info == nil {
			t.Fatal("Info returned nil")
		}
	})

	t.Run("stats", func(t *testing.T) {
		st, err := c.Introspectable.Stats(context.Background(), id)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if st == nil {
			t.Fatal("Stats returned nil")
		}
	})
}

// ---------------------------------------------------------------------------
// cleanup
// ---------------------------------------------------------------------------

func runCleanup(t *testing.T, c Contract, base spec.SandboxSpec) {
	t.Run("destroy-removes-sandbox", func(t *testing.T) {
		id := create(t, c, base)
		if err := destroy(t, c, id); err != nil {
			t.Fatalf("destroy: %v", err)
		}
		// After destroy every role call against the id must fail (not found),
		// proving the sandbox no longer exists.
		if c.Executable != nil {
			if _, err := c.Executable.Exec(context.Background(), id, exec.Request{Argv: []string{binTrue}}); err == nil {
				t.Fatal("exec after destroy should fail (not found)")
			}
		}
		if c.Introspectable != nil {
			if _, err := c.Introspectable.Info(context.Background(), id); err == nil {
				t.Fatal("info after destroy should fail (not found)")
			}
		}
	})
}
