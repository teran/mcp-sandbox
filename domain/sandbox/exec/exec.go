// Package exec defines the typed request/result types for executing a command
// inside a sandbox. Commands are passed as argument arrays — never a shell
// string (S11).
package exec

// Request describes a command to run inside a sandbox.
type Request struct {
	// SandboxID is the target sandbox session.
	SandboxID string
	// Command is the raw visible command string (presentation/streaming only,
	// never executed). The executed argv is always the fixed wrapper
	// [shellTokens..., "-c", command].
	Command string
	// Env is an optional per-exec environment override. Keys/VALUES are
	// validated at the tool boundary. An empty map means no override.
	Env map[string]string
	// Argv is the command wrapper [shellTokens..., "-c", command], built by the
	// tool layer from the profile shell. It is what the backend actually runs.
	Argv []string
	// WorkingDir overrides the working directory inside the sandbox (optional).
	WorkingDir string
	// Timeout is the command execution timeout.
	Timeout uint64
	// User overrides the user the command runs as (e.g. "0" for a
	// sudo-classed operation, or "1000:1000" for the default non-root user).
	// The session manager resolves the effective user from the sandbox's
	// UIDGID and SudoPolicy before calling the backend (SPEC §7.10).
	User string
}

// Result describes the outcome of an executed command.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	// PolicyDenied is true when the sandbox policy denied the operation
	// (darwin/Seatbelt in-band deny marker or EPERM; SPEC §7.12). Backends
	// without an in-band policy classifier (e.g. Docker) leave it zero, so a
	// plain non-zero exit remains distinguishable from a policy denial.
	PolicyDenied bool
}
