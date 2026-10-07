package sandbox

// Output types for the sandbox tools. All sensitive fields are annotated
// secret:"true" (S02) so the server's struct-tag redactor hides them from every
// emitted value and log line.

// SessionSummary is one entry of sandbox_list output.
type SessionSummary struct {
	ID      string `json:"id"`
	Backend string `json:"backend"`
	Image   string `json:"image"`
	Created string `json:"created"`
	State   string `json:"state"`
}

// ListOutput is the structured output of sandbox_list.
type ListOutput struct {
	Sessions []SessionSummary `json:"sessions"`
}

// InfoOutput is the structured output of sandbox_info. Secrets are redacted.
type InfoOutput struct {
	ID         string `json:"id"`
	Image      string `json:"image"`
	WorkingDir string `json:"working_dir"`
	State      string `json:"state"`
	Created    string `json:"created"`
	// Secrets are redacted from output and logs (S02).
	Secrets []SecretSummary `json:"secrets"`
	// Details carries the backend-specific inspect payload (already redacted
	// by the wrapper).
	Details any `json:"details,omitempty"`
}

// SecretSummary reports a secret's name only; the value is never emitted (S02).
type SecretSummary struct {
	Name string `json:"name"`
}

// StatsOutput is the structured output of sandbox_stats. The raw backend stats
// payload is carried in Details (already redacted by the wrapper).
type StatsOutput struct {
	ID      string `json:"id"`
	Details any    `json:"details"`
}

// ReadFileOutput is the structured output of sandbox_read_file.
type ReadFileOutput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// CreateOutput is the structured output of sandbox_create.
type CreateOutput struct {
	ID string `json:"id"`
}

// ExecOutput is the structured output of sandbox_exec. argv is never echoed;
// stdout/stderr are returned after ANSI sanitization (S09).
type ExecOutput struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// WriteFileOutput is the structured output of sandbox_write_file.
type WriteFileOutput struct {
	Path  string `json:"path"`
	Wrote bool   `json:"wrote"`
	Bytes int    `json:"bytes"`
}

// CopyOutput is the structured output of sandbox_copy_in / sandbox_copy_out.
type CopyOutput struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	OK          bool   `json:"ok"`
}

// InstallPackageOutput is the structured output of sandbox_install_package.
type InstallPackageOutput struct {
	ID       string `json:"id"`
	Package  string `json:"package"`
	Manager  string `json:"manager"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// DestroyOutput is the structured output of sandbox_destroy / destroy_all.
type DestroyOutput struct {
	Destroyed int      `json:"destroyed"`
	IDs       []string `json:"ids"`
}
