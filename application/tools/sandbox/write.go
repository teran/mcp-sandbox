package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// writeTool builds a non-read-only, non-destructive tool definition (S03).
func writeTool(b builder, name, title, desc, instructions string, in *jsonschema.Schema, out *jsonschema.Schema, redact []def.RedactionField, h def.ToolHandler) *def.ToolDef {
	return b.tool(name, title, desc, instructions, def.Annotations{ReadOnlyHint: false, DestructiveHint: false, OpenWorldHint: false}, in, out, redact, h)
}

// mountInput is the JSON shape of a mount in sandbox_create.
type mountInput struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

// secretInput is the JSON shape of a secret in sandbox_create. The value is
// secret:true and redacted from output and logs (S02).
type secretInput struct {
	Name  string `json:"name"`
	Value string `json:"value" secret:"true"`
}

// createInput is the typed sandbox_create arguments. Isolation is intentionally
// NOT accepted: it is server-enforced and immutable (§7.1).
type createInput struct {
	Image     string            `json:"image"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	Mounts    []mountInput      `json:"mounts"`
	NanoCPUs  int64             `json:"nano_cpus"`
	Memory    int64             `json:"memory"`
	PidsLimit int64             `json:"pids_limit"`
	UID       uint32            `json:"uid"`
	GID       uint32            `json:"gid"`
	Sudo      string            `json:"sudo"`
	Secrets   []secretInput     `json:"secrets"`
}

// sandboxCreateDef declares sandbox_create: create a sandbox from a typed spec.
func sandboxCreateDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_create", "Create a sandbox",
		"Create a sandbox from a typed spec. The session cwd is mounted 1:1 at the same absolute path (writable by default). Mounts are immutable. Isolation is server-enforced and cannot be weakened.",
		"Pass the image and session cwd. Secrets are delivered as tmpfs 0600 files, never in argv or image. Creating a duplicate is surfaced as an error.",
		obj(map[string]*jsonschema.Schema{
			"image":      strP("Container image (defaults to the server policy image)"),
			"cwd":        strP("Session working directory, mounted 1:1 at the same absolute path"),
			"env":        objP("Environment variables"),
			"mounts":     arrP("Additional immutable mounts", obj(map[string]*jsonschema.Schema{"source": strP("Host source within ALLOW_DIRS"), "target": strP("Container target"), "read_only": boolP("Mount read-only")})),
			"nano_cpus":  intP("CPU limit in nano-CPUs"),
			"memory":     intP("Memory limit in bytes"),
			"pids_limit": intP("PID limit"),
			"uid":        intP("Container uid (non-root enforced)"),
			"gid":        intP("Container gid"),
			"sudo":       strEnumP("Sudo policy", "never", "require"),
			"secrets":    arrP("Secrets delivered as tmpfs 0600 files", obj(map[string]*jsonschema.Schema{"name": strP("Secret name"), "value": strP("Secret value")})),
		}),
		objectOutput(),
		[]def.RedactionField{{Name: "secrets", Secret: true}},
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in createInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_create: %w", err)
			}
			s, err := b.svc.Create(ctx, buildSpec(b, in))
			if err != nil {
				return nil, err
			}
			return CreateOutput{ID: s.ID()}, nil
		})
}

// buildSpec assembles a typed SandboxSpec from the create input, applying the
// server-enforced immutable Isolation (§7.1). A caller cannot weaken any
// hardening value; only bounded resources are honoured.
func buildSpec(b builder, in createInput) spec.SandboxSpec {
	pol := b.svc.Policy()
	iso := pol.Isolation

	if in.NanoCPUs > 0 {
		iso.NanoCPUs = in.NanoCPUs
	}
	if in.Memory > 0 {
		iso.Memory = in.Memory
	}
	if in.PidsLimit > 0 {
		iso.PidsLimit = in.PidsLimit
	}

	uid, gid := in.UID, in.GID
	if uid == 0 {
		uid = iso.User.UID
	}
	if gid == 0 {
		gid = iso.User.GID
	}

	mounts := make([]spec.Mount, 0, len(in.Mounts))
	for _, m := range in.Mounts {
		mounts = append(mounts, spec.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}

	secrets := make([]spec.SecretRef, 0, len(in.Secrets))
	for _, sec := range in.Secrets {
		secrets = append(secrets, spec.SecretRef{Name: sec.Name, Value: sec.Value})
	}

	sudo := spec.SudoPolicy(in.Sudo)
	if sudo == "" {
		sudo = spec.SudoNever
	}

	image := in.Image
	if image == "" {
		image = pol.DefaultImage
	}

	return spec.SandboxSpec{
		Image:        image,
		WorkingDir:   in.Cwd,
		Env:          in.Env,
		Mounts:       mounts,
		Resources:    spec.Resources{NanoCPUs: iso.NanoCPUs, Memory: iso.Memory, MemorySwap: iso.MemorySwap, PidsLimit: iso.PidsLimit, Ulimits: iso.Ulimits},
		Network:      spec.Network{Mode: iso.NetworkMode},
		UIDGID:       spec.UIDGID{UID: uid, GID: gid},
		Sudo:         sudo,
		Secrets:      secrets,
		ReadOnlyRoot: true,
		Isolation:    iso,
	}
}

// execInput is the typed sandbox_exec arguments. argv is a strict array — never
// a shell string (S11).
type execInput struct {
	ID         string   `json:"id"`
	Argv       []string `json:"argv"`
	WorkingDir string   `json:"working_dir"`
	Timeout    uint64   `json:"timeout"`
	User       string   `json:"user"`
}

// sandboxExecDef declares sandbox_exec: run a command as an argument array.
func sandboxExecDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_exec", "Execute a command in a sandbox",
		"Execute a command inside the sandbox as an argument array (no shell). Runs under the sandbox user by default; user=root runs as scoped uid 0 per the sudo policy. Output is ANSI-sanitized.",
		"Pass the command and arguments as an array, never a shell string. Do not put secrets in argv. Output is ANSI-sanitized before returning.",
		obj(map[string]*jsonschema.Schema{
			"id":          strP("Sandbox session id"),
			"argv":        arrP("Command and arguments as an array (no shell)", strP("Argument")),
			"working_dir": strP("Optional working directory"),
			"timeout":     intP("Command timeout in seconds"),
			"user":        strEnumP("Run as user", "default", "root"),
		}, "id", "argv"),
		objectOutput(),
		[]def.RedactionField{{Name: "argv", Secret: true}},
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in execInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_exec: %w", err)
			}
			if len(in.Argv) == 0 {
				return nil, errors.New("sandbox_exec: argv must not be empty")
			}
			user, err := resolveUser(b, in.ID, in.User)
			if err != nil {
				return nil, err
			}
			res, err := b.svc.Exec(ctx, exec.Request{
				SandboxID:  in.ID,
				Argv:       in.Argv,
				WorkingDir: in.WorkingDir,
				Timeout:    in.Timeout,
				User:       user,
			})
			if err != nil {
				return nil, err
			}
			return ExecOutput{
				ID:       in.ID,
				ExitCode: res.ExitCode,
				Stdout:   sanitizeANSI(string(res.Stdout)),
				Stderr:   sanitizeANSI(string(res.Stderr)),
			}, nil
		})
}

// resolveUser maps a requested user to the docker exec user. root requires the
// sandbox's sudo policy (SudoRequire), otherwise the sandbox's non-root
// UIDGID is used (§7.10).
func resolveUser(b builder, id, requested string) (string, error) {
	if requested != "" && requested != "root" && requested != "default" {
		return "", fmt.Errorf("sandbox_exec: unsupported user %q", requested)
	}
	s, err := b.svc.Get(id)
	if err != nil {
		return "", err
	}
	if requested == "root" {
		if s.Spec().Sudo != spec.SudoRequire {
			return "", errors.New("sandbox_exec: root user requires a 'require' sudo policy")
		}
		return "0", nil
	}
	uid, gid := s.Spec().UIDGID.UID, s.Spec().UIDGID.GID
	if uid == 0 {
		return "65534:65534", nil // nobody
	}
	return fmt.Sprintf("%d:%d", uid, gid), nil
}

// writeFileInput is the typed sandbox_write_file arguments.
type writeFileInput struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// sandboxWriteFileDef declares sandbox_write_file: idempotent file write.
func sandboxWriteFileDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_write_file", "Write a file into a sandbox",
		"Write a file into a sandbox (tmpfs / allowed mount). Idempotent: repeated writes with identical content are a no-op (natural-key upsert, X02).",
		"Pass the session id, an absolute path and the content. Content is redacted from logs.",
		obj(map[string]*jsonschema.Schema{
			"id":      strP("Sandbox session id"),
			"path":    strP("Absolute destination path inside the sandbox"),
			"content": strP("File content"),
		}, "id", "path", "content"),
		objectOutput(),
		[]def.RedactionField{{Name: "content", Secret: true}},
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in writeFileInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_write_file: %w", err)
			}
			if err := b.svc.WriteFile(ctx, in.ID, in.Path, []byte(in.Content)); err != nil {
				return nil, err
			}
			return WriteFileOutput{Path: in.Path, Wrote: true, Bytes: len(in.Content)}, nil
		})
}

// copyInInput is the typed sandbox_copy_in arguments.
type copyInInput struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// sandboxCopyInDef declares sandbox_copy_in: host → sandbox.
func sandboxCopyInDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_copy_in", "Copy a host path into a sandbox",
		"Copy a file or directory from an allowed host path (ALLOW_DIRS) into the sandbox. Idempotent by destination path.",
		"Pass the session id, the host source path (must be within ALLOW_DIRS) and the sandbox destination path.",
		obj(map[string]*jsonschema.Schema{
			"id":          strP("Sandbox session id"),
			"source":      strP("Host source path within ALLOW_DIRS"),
			"destination": strP("Sandbox destination path"),
		}, "id", "source", "destination"),
		objectOutput(),
		nil,
		func(_ context.Context, args json.RawMessage) (any, error) {
			var in copyInInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_copy_in: %w", err)
			}
			if err := b.svc.CopyIn(context.Background(), in.ID, in.Source, in.Destination); err != nil {
				return nil, err
			}
			return CopyOutput{Source: in.Source, Destination: in.Destination, OK: true}, nil
		})
}

// copyOutInput is the typed sandbox_copy_out arguments.
type copyOutInput struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

// sandboxCopyOutDef declares sandbox_copy_out: sandbox → host.
func sandboxCopyOutDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_copy_out", "Copy a sandbox path to the host",
		"Copy a file or directory from the sandbox to an allowed host path (ALLOW_DIRS). Idempotent by destination path.",
		"Pass the session id, the sandbox source path and the host destination path (must be within ALLOW_DIRS).",
		obj(map[string]*jsonschema.Schema{
			"id":          strP("Sandbox session id"),
			"source":      strP("Sandbox source path"),
			"destination": strP("Host destination path within ALLOW_DIRS"),
		}, "id", "source", "destination"),
		objectOutput(),
		nil,
		func(_ context.Context, args json.RawMessage) (any, error) {
			var in copyOutInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_copy_out: %w", err)
			}
			if err := b.svc.CopyOut(context.Background(), in.ID, in.Source, in.Destination); err != nil {
				return nil, err
			}
			return CopyOutput{Source: in.Source, Destination: in.Destination, OK: true}, nil
		})
}

// installInput is the typed sandbox_install_package arguments.
type installInput struct {
	ID      string `json:"id"`
	Package string `json:"package"`
	Manager string `json:"manager"`
}

// packageNameRe restricts a package name to a safe character set so it is never
// interpolated into a shell string (S11/N21); only a fixed apt/dnf argv is
// built.
var packageNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]*$`)

// sandboxInstallPackageDef declares sandbox_install_package: fixed apt/dnf
// invocation under a sudo policy (§7.10).
func sandboxInstallPackageDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_install_package", "Install a package in a sandbox",
		"Install a package inside the sandbox via the targeted package manager (apt/dnf) using a fixed invocation, running as scoped uid 0 under the sandbox sudo policy. Targets pre-provisioned images.",
		"Use only for installing known packages. The underlying command is a fixed apt/dnf invocation; never use it to run arbitrary shell.",
		obj(map[string]*jsonschema.Schema{
			"id":      strP("Sandbox session id"),
			"package": strP("Package name"),
			"manager": strEnumP("Package manager", "apt", "dnf"),
		}, "id", "package", "manager"),
		objectOutput(),
		nil,
		func(_ context.Context, args json.RawMessage) (any, error) {
			var in installInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_install_package: %w", err)
			}
			if !packageNameRe.MatchString(in.Package) {
				return nil, fmt.Errorf("sandbox_install_package: invalid package name %q", in.Package)
			}
			s, err := b.svc.Get(in.ID)
			if err != nil {
				return nil, err
			}
			if s.Spec().Sudo != spec.SudoRequire {
				return nil, errors.New("sandbox_install_package: requires a 'require' sudo policy")
			}

			argv, err := packageManagerArgv(in.Manager, in.Package)
			if err != nil {
				return nil, err
			}
			res, err := b.svc.Exec(context.Background(), exec.Request{
				SandboxID: in.ID,
				Argv:      argv,
				User:      "0",
			})
			if err != nil {
				return nil, err
			}
			return InstallPackageOutput{
				ID:       in.ID,
				Package:  in.Package,
				Manager:  in.Manager,
				ExitCode: res.ExitCode,
				Output:   sanitizeANSI(string(res.Stdout) + string(res.Stderr)),
			}, nil
		})
}

// packageManagerArgv returns the fixed argument array for the given package
// manager and validated package name. The package name is appended to a fixed
// command — never interpolated into a shell string (S11/N21).
func packageManagerArgv(manager, pkg string) ([]string, error) {
	switch manager {
	case "apt":
		return []string{"apt-get", "install", "-y", pkg}, nil
	case "dnf":
		return []string{"dnf", "install", "-y", pkg}, nil
	default:
		return nil, fmt.Errorf("sandbox_install_package: unsupported package manager %q", manager)
	}
}
