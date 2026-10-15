//go:build !darwin

package darwin

import (
	"context"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
)

// unavailableRunner is the non-darwin stand-in for the real os/exec runner. The
// darwin backend is macOS-only; on any other GOOS the factory's Available()
// returns false, so this runner is never selected or used. It exists only so
// the package compiles (and its tests run) on non-darwin platforms.
type unavailableRunner struct{}

// Start never succeeds: there is no /usr/bin/sandbox-exec outside macOS. It
// returns ErrCapabilityNotSupported rather than a silent no-op.
func (unavailableRunner) Start(_ context.Context, _ ProcessSpec) (Process, error) {
	return nil, role.ErrCapabilityNotSupported
}

// newRunner returns the platform runner: on non-darwin hosts this is the
// unavailable stand-in (the backend is never selected there).
func newRunner() runner { return unavailableRunner{} }

// rlimitNPROC is not provided by the syscall package on Linux. The darwin
// backend never runs on Linux (Available() is always false there), so this
// value exists only to make the portable rlimit mapping compile and be
// testable on Linux; it is Linux's RLIMIT_NPROC (6). On darwin the constant is
// defined as 7 (macOS's RLIMIT_NPROC) in runner_darwin.go.
const rlimitNPROC = 0x6
