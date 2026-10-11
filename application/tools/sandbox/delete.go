package sandbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// deleteTool builds a destructive tool definition (S03/S12). Destructive tools
// require human confirmation client-side (HITL); the server does not cascade.
func deleteTool(
	b builder,
	name, title, desc, instructions string,
	in, out *jsonschema.Schema,
	h def.ToolHandler,
) *def.ToolDef {
	return b.tool(
		name, title, desc, instructions,
		def.Annotations{ReadOnlyHint: false, DestructiveHint: true, IdempotentHint: true, OpenWorldHint: false},
		in, out, nil, h,
	)
}

// sandboxDestroyDef declares sandbox_destroy: destroy one sandbox.
func sandboxDestroyDef(b builder) *def.ToolDef {
	return deleteTool(b, "sandbox_destroy", "Destroy a sandbox",
		"Destroy one sandbox session. Idempotent: destroying a non-existent session is a no-op (X02).",
		"Destructive and irreversible. Requires human confirmation (client-side HITL). Pass the session id.",
		obj(map[string]*jsonschema.Schema{"id": strP("Sandbox session id")}, "id"),
		objectOutput(),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, fmt.Errorf("sandbox_destroy: %w", err)
			}
			if err := b.svc.Destroy(ctx, in.ID); err != nil {
				return nil, err
			}

			return DestroyOutput{Destroyed: 1, IDs: []string{domainsession.ShortID(in.ID)}}, nil
		})
}

// sandboxDestroyAllDef declares sandbox_destroy_all: destroy every sandbox.
func sandboxDestroyAllDef(b builder) *def.ToolDef {
	return deleteTool(b, "sandbox_destroy_all", "Destroy all sandboxes",
		"Destroy every sandbox session. Idempotent (X02).",
		"Destructive and irreversible. Requires human confirmation (client-side HITL).",
		obj(nil),
		objectOutput(),
		func(ctx context.Context, _ json.RawMessage) (any, error) {
			before := b.svc.List(ctx)
			if err := b.svc.DestroyAll(ctx); err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(before))
			for _, s := range before {
				ids = append(ids, domainsession.ShortID(s.ID()))
			}

			return DestroyOutput{Destroyed: len(ids), IDs: ids}, nil
		})
}
