package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
		"Create a sandbox from a typed spec. The session cwd is mounted 1:1 at the same absolute path (read-only by default; opt-in rw via SANDBOX_CWD_MODE=rw). Mounts are immutable. Isolation is server-enforced and cannot be weakened.",
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
