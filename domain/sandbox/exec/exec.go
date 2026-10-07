// Package exec defines the typed request/result types for executing a command
// inside a sandbox. Commands are passed as argument arrays — never a shell
// string (S11).
package exec

// Request describes a command to run inside a sandbox.
type Request struct {
	// SandboxID is the target sandbox session.
	SandboxID string
	// Argv is the command and its arguments as an array. No shell is involved.
	Argv []string
	// WorkingDir overrides the working directory inside the sandbox (optional).
	WorkingDir string
	// Timeout is the command execution timeout.
	Timeout uint64
}

// Result describes the outcome of an executed command.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}
