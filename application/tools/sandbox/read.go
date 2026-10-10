package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
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
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			sessions := b.svc.List(ctx)
			out := ListOutput{Sessions: make([]SessionSummary, 0, len(sessions))}
			for _, s := range sessions {
				out.Sessions = append(out.Sessions, SessionSummary{
					ID:      domainsession.ShortID(s.ID()),
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
				ID:         domainsession.ShortID(s.ID()),
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
			sess, err := b.svc.Get(in.ID)
			if err != nil {
				return nil, err
			}
			details, err := b.svc.Stats(ctx, sess.ID())
			if err != nil {
				return nil, err
			}
			return StatsOutput{ID: domainsession.ShortID(sess.ID()), Details: details}, nil
		})
}
