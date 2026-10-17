package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/guidance"
	"github.com/teran/mcp-sandbox/application/tools/handler"
	"github.com/teran/mcp-sandbox/domain/progress"
	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// redactedMarker replaces secret values found in exec output (S02). It matches
// the marker used by the server's redactors so tool output and logs stay
// consistent.
const redactedMarker = "***REDACTED***"

// writeTool builds a non-read-only, non-destructive tool definition (S03).
func writeTool(
	b builder,
	name, title, desc, instructions string,
	in, out *jsonschema.Schema,
	redact []def.RedactionField,
	h def.ToolHandler,
) *def.ToolDef {
	return b.tool(
		name, title, desc, instructions,
		def.Annotations{ReadOnlyHint: false, DestructiveHint: false, OpenWorldHint: false},
		in, out, redact, h,
	)
}

// secretInput is the JSON shape of a secret in sandbox_create. The value is
// secret:true and redacted from output and logs (S02).
type secretInput struct {
	Name  string `json:"name"`
	Value string `json:"value" secret:"true"`
}

// createInput is the typed sandbox_create arguments (Phase 7). Only profile,
// cwd, session_id and secrets are accepted; image/env/mounts/resources/uid/gid/
// sudo are REMOVED and come from the selected profile. Isolation is never
// accepted: it is server-enforced and immutable (§7.1).
type createInput struct {
	Profile   string        `json:"profile"`
	Cwd       string        `json:"cwd"`
	SessionID string        `json:"session_id"`
	Secrets   []secretInput `json:"secrets"`
}

// sandboxCreateDef declares sandbox_create: create a sandbox from a profile.
func sandboxCreateDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_create", "Create a sandbox",
		"Create a sandbox from an operator-authored profile. The profile fully determines "+
			"image, env, mounts, network, resources, user and sudo; the session cwd is mounted 1:1 "+
			"at the same absolute path (read-only unless the profile sets cwd.mode=rw). Isolation "+
			"is server-enforced and cannot be weakened.",
		"Pass the profile name and session cwd. Secrets are delivered as tmpfs 0600 files, "+
			"never in argv or image. Creating a duplicate is surfaced as an error.\n\n"+
			guidance.RedirectNote+"\n\nSee "+guidance.URIRedirect+" and "+guidance.URICreate+" for details.",
		obj(map[string]*jsonschema.Schema{
			"profile": strP("Operator-authored profile name (see profile_list); determines image, " +
				"env, mounts, network, resources, user and sudo"),
			"cwd": strP("Session working directory, mounted 1:1 at the same absolute path"),
			"session_id": strP("OpenCode client session id that created this sandbox; recorded as a " +
				"container label for traceability. Distinct from the sandbox session id returned as `id`."),
			"secrets": arrP("Secrets delivered as tmpfs 0600 files", obj(map[string]*jsonschema.Schema{
				"name": strP("Secret name"), "value": strP("Secret value"),
			})),
		}, "profile", "cwd", "session_id"),
		objectOutput(),
		[]def.RedactionField{{Name: "secrets", Secret: true}},
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in createInput
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_create: %w", err)
			}
			if strings.TrimSpace(in.Profile) == "" {
				return nil, errors.New("sandbox_create: profile is required")
			}
			if strings.TrimSpace(in.Cwd) == "" {
				return nil, errors.New("sandbox_create: cwd is required")
			}
			if strings.TrimSpace(in.SessionID) == "" {
				return nil, errors.New("sandbox_create: session_id is required")
			}
			sp, err := buildSpec(b, in)
			if err != nil {
				return nil, err
			}
			s, err := b.svc.Create(ctx, sp)
			if err != nil {
				return nil, err
			}

			return CreateOutput{ID: domainsession.ShortID(s.ID())}, nil
		})
}

// buildSpec assembles a typed SandboxSpec from the create input by resolving
// the selected operator profile and dispatching to the driver registry's
// builder for the profile's concrete driver (SPEC §6.11/§7.11). The image, env,
// mounts, network, resources, user and sudo all come from the profile's opaque
// options (decoded by the builder); a caller cannot weaken any hardening value
// because the builder takes the immutable Isolation policy from the Service.
func buildSpec(b builder, in createInput) (spec.SandboxSpec, error) {
	pol := b.svc.Policy()

	if pol.Profiles == nil {
		return spec.SandboxSpec{}, errors.New("sandbox_create: profile resolver is not configured")
	}
	p, err := pol.Profiles.Get(in.Profile)
	if err != nil {
		if errors.Is(err, domainprofile.ErrProfileNotFound) {
			return spec.SandboxSpec{}, fmt.Errorf("sandbox_create: unknown profile %q: %w", in.Profile, err)
		}

		return spec.SandboxSpec{}, err
	}

	secrets := make([]spec.SecretRef, 0, len(in.Secrets))
	for _, sec := range in.Secrets {
		secrets = append(secrets, spec.SecretRef{Name: sec.Name, Value: sec.Value})
	}

	// Dispatch to the profile's driver builder (fail-closed: an unknown driver
	// fails here with ErrUnknownDriver, at USE time, not at scan time).
	bld, err := driver.Default.Get(p.Driver)
	if err != nil {
		return spec.SandboxSpec{}, fmt.Errorf("sandbox_create: %w", err)
	}

	return bld.Build(driver.Context{
		Name:        p.Name,
		Description: p.Description,
		Cwd:         p.Cwd,
		Shell:       p.Shell,
		Options:     p.Options,
		Policy:      pol.Isolation,
		Params: spec.Params{
			Cwd:             in.Cwd,
			Secrets:         secrets,
			ClientSessionID: in.SessionID,
		},
	})
}

// execInput is the typed sandbox_exec arguments. The command is a single string
// executed via a fixed shell wrapper (never a shell string the caller
// controls — the wrapper is [shellTokens..., "-c", command], S11). Per-exec env
// overrides are optional.
type execInput struct {
	ID         string            `json:"id"`
	Command    string            `json:"command"`
	Env        map[string]string `json:"env"`
	Summary    string            `json:"summary"`
	WorkingDir string            `json:"working_dir"`
	Timeout    uint64            `json:"timeout"`
	User       string            `json:"user"`
}

// sandboxExecDef declares sandbox_exec: run a command via the sandbox's
// configured shell wrapper.
func sandboxExecDef(b builder) *def.ToolDef {
	return writeTool(b, "sandbox_exec", "Execute a command in a sandbox",
		"Execute a command inside the sandbox via its configured shell. Runs under the sandbox user "+
			"by default; user=root runs as scoped uid 0 per the sudo policy. Output is ANSI-sanitized.",
		"Pass the command as a single string, never a shell string (it is executed via the sandbox's "+
			"fixed shell wrapper). Fill `summary` with a short human-readable description of the "+
			"command (one or two words). Do not put secrets in the command or env. Output is "+
			"ANSI-sanitized before returning.\n\n"+
			guidance.RedirectNote+"\n\nSee "+guidance.URIExec+" for details.",
		obj(map[string]*jsonschema.Schema{
			"id":          strP("Sandbox session id"),
			"command":     strP("The command to run, executed via the sandbox's configured shell"),
			"env":         envP("Optional per-exec environment overrides (string key/value pairs)"),
			"summary":     strP("Short human-readable description of the command (e.g. \"run unit tests\")"),
			"working_dir": strP("Optional working directory"),
			"timeout":     intP("Command timeout in seconds"),
			"user":        strEnumP("Run as user", "default", "root"),
		}, "id", "command"),
		objectOutput(),
		nil, // no secret fields: command/env are not sensitive and are kept visible in access logs
		execHandler(b),
	)
}

// execHandler builds the sandbox_exec handler: parse and validate the input,
// resolve the shell wrapper and user, wrap the progress sink for redaction and
// ANSI-sanitization, run the command and redact its output against the
// sandbox's known secret values (S02/§7.3).
func execHandler(b builder) def.ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		var in execInput
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("sandbox_exec: %w", err)
		}
		if strings.TrimSpace(in.Command) == "" {
			return nil, handler.NewInvalidArgumentsError("sandbox_exec: command must not be empty")
		}
		if err := validateExecEnv(in.Env); err != nil {
			return nil, err
		}
		user, err := resolveUser(b, in.ID, in.User)
		if err != nil {
			return nil, err
		}
		// Resolve the shell wrapper from the session spec, defaulting to
		// bash (SPEC §7.10). strings.Fields whitespace-splits a
		// multi-token shell like "bash -l" into ["bash","-l"].
		sess, err := b.svc.Get(in.ID)
		if err != nil {
			return nil, err
		}
		shell := sess.Spec().Shell
		if strings.TrimSpace(shell) == "" {
			shell = "bash"
		}
		wrapper := append(strings.Fields(shell), "-c", in.Command)
		// Fetch the sandbox's known secret values once so both the progress
		// stream and the final structured output are redacted against them
		// (S02/§7.3).
		secrets := sandboxSecretValues(b, in.ID)
		// Wrap the context sink so streamed progress events are secrets-
		// redacted (S02) and ANSI-sanitized (S09) before they reach the
		// caller — the same contract as any other output (Phase 6, §5.1).
		// Progress/Total are preserved. Nil-safe: SinkFromContext is never
		// nil, so a no-op sink is wrapped harmlessly.
		sink := progress.SinkFromContext(ctx)
		execCtx := ctx
		if sink != nil {
			wrapped := func(c context.Context, e progress.Event) {
				sink(c, progress.Event{
					Message:  redactSecrets(sanitizeANSI(e.Message), secrets),
					Progress: e.Progress,
					Total:    e.Total,
				})
			}
			execCtx = progress.WithSink(ctx, wrapped)
		}
		res, err := b.svc.Exec(execCtx, exec.Request{
			SandboxID:  sess.ID(),
			Command:    in.Command,
			Env:        in.Env,
			Argv:       wrapper,
			WorkingDir: in.WorkingDir,
			Timeout:    in.Timeout,
			User:       user,
		})
		if err != nil {
			return nil, err
		}
		// Redact stdout/stderr against the sandbox's known secret values
		// (S02/§7.3). A command may print a secret (e.g. `echo $SECRET`
		// from the env, or `cat /run/sandbox/<secret>`); the struct-tag
		// redactor cannot see inside free-form text, so we replace the
		// known secret values before returning the output.
		return ExecOutput{
			ID:       domainsession.ShortID(sess.ID()),
			ExitCode: res.ExitCode,
			Stdout:   redactSecrets(sanitizeANSI(string(res.Stdout)), secrets),
			Stderr:   redactSecrets(sanitizeANSI(string(res.Stderr)), secrets),
		}, nil
	}
}

// validateExecEnv rejects a per-exec env map with an empty key or a NUL/newline
// in any key or value, surfaced as invalid-arguments (S08/S11). '=' is allowed
// in values. An empty map is valid (no override).
func validateExecEnv(env map[string]string) error {
	for k, v := range env {
		if k == "" {
			return handler.NewInvalidArgumentsError("sandbox_exec: env key must not be empty")
		}
		if strings.ContainsAny(k, "\x00\n") || strings.ContainsAny(v, "\x00\n") {
			return handler.NewInvalidArgumentsError("sandbox_exec: env key/value must not contain NUL or newline")
		}
	}

	return nil
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

// sandboxSecretValues returns the known secret values for a sandbox session:
// every SecretRef value plus every env value (S02/§7.3). These are the values
// that must never appear in exec output. The fetch is best-effort — if the
// session is gone the returned list is empty and no redaction beyond the
// default is applied.
func sandboxSecretValues(b builder, id string) []string {
	sess, err := b.svc.Get(id)
	if err != nil {
		return nil
	}
	sp := sess.Spec()
	var vals []string
	for _, sec := range sp.Secrets {
		if sec.Value != "" {
			vals = append(vals, sec.Value)
		}
	}
	for _, v := range sp.Env {
		if v != "" {
			vals = append(vals, v)
		}
	}

	return vals
}

// redactSecrets replaces every occurrence of the given secret values in s with
// the redaction marker (S02). It is applied at the output boundary to exec
// stdout/stderr so free-form command output cannot leak a secret.
func redactSecrets(s string, secrets []string) string {
	if len(secrets) == 0 {
		return s
	}
	out := s
	for _, sec := range secrets {
		out = strings.ReplaceAll(out, sec, redactedMarker)
	}

	return out
}
