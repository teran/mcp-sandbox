package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
)

// readTool builds a read-only tool definition (S03).
func readTool(b builder, name, title, desc, instructions string, in *jsonschema.Schema, out *jsonschema.Schema, h def.ToolHandler) *def.ToolDef {
	return b.tool(name, title, desc, instructions, def.Annotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: false}, in, out, nil, h)
}

// sandboxListDef declares sandbox_list: list sandbox sessions.
func sandboxListDef(b builder) *def.ToolDef {
	return readTool(b, "sandbox_list", "List sandbox sessions",
		"List all sandbox sessions with their id, backend, image, creation time and state.",
		"Read-only. Never mutates any sandbox. Returns the current set of live sessions.",
		obj(nil),
		objectOutput(),
		func(_ context.Context, _ json.RawMessage) (any, error) {
			sessions := b.svc.List()
			out := ListOutput{Sessions: make([]SessionSummary, 0, len(sessions))}
			for _, s := range sessions {
				out.Sessions = append(out.Sessions, SessionSummary{
					ID:      s.ID(),
					Backend: b.svc.BackendName(),
					Image:   s.Spec().Image,
					Created: s.Created().Format(time.RFC3339),
					State:   string(s.State()),
				})
			}
			return out, nil
		})
}

// sandboxInfoDef declares sandbox_info: detailed spec + state of one sandbox.
func sandboxInfoDef(b builder) *def.ToolDef {
	return readTool(b, "sandbox_info", "Inspect a sandbox",
		"Return detailed spec and state of one sandbox: image, working directory, state, creation time and secret names (values redacted).",
		"Read-only. Pass the session id. Secret values are never returned; only their names are listed.",
		obj(map[string]*jsonschema.Schema{"id": strP("Sandbox session id")}, "id"),
		objectOutput(),
		func(_ context.Context, args json.RawMessage) (any, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_info: %w", err)
			}
			s, err := b.svc.Get(in.ID)
			if err != nil {
				return nil, err
			}
			sp := s.Spec()
			secrets := make([]SecretSummary, 0, len(sp.Secrets))
			for _, sec := range sp.Secrets {
				secrets = append(secrets, SecretSummary{Name: sec.Name})
			}
			return InfoOutput{
				ID:         s.ID(),
				Image:      sp.Image,
				WorkingDir: sp.WorkingDir,
				State:      string(s.State()),
				Created:    s.Created().Format(time.RFC3339),
				Secrets:    secrets,
			}, nil
		})
}

// sandboxStatsDef declares sandbox_stats: live resource usage of one sandbox.
func sandboxStatsDef(b builder) *def.ToolDef {
	return readTool(b, "sandbox_stats", "Sandbox resource usage",
		"Return live resource usage of one sandbox (CPU, memory, network, IO) from Docker stats.",
		"Read-only. Pass the session id. The payload is redacted before returning.",
		obj(map[string]*jsonschema.Schema{"id": strP("Sandbox session id")}, "id"),
		objectOutput(),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_stats: %w", err)
			}
			details, err := b.svc.Stats(ctx, in.ID)
			if err != nil {
				return nil, err
			}
			return StatsOutput{ID: in.ID, Details: details}, nil
		})
}

// sandboxReadFileDef declares sandbox_read_file: read a file's content.
func sandboxReadFileDef(b builder) *def.ToolDef {
	return readTool(b, "sandbox_read_file", "Read a file from a sandbox",
		"Read a file's content from inside a sandbox. The path must resolve inside the sandbox scope (mounted dirs / tmpfs).",
		"Read-only. Pass the session id and an absolute path inside the sandbox.",
		obj(map[string]*jsonschema.Schema{
			"id":   strP("Sandbox session id"),
			"path": strP("Absolute path inside the sandbox"),
		}, "id", "path"),
		objectOutput(),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_read_file: %w", err)
			}
			data, err := b.svc.ReadFile(ctx, in.ID, in.Path)
			if err != nil {
				return nil, err
			}
			return ReadFileOutput{Path: in.Path, Content: string(data)}, nil
		})
}
