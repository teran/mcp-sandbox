package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/guidance"
	"github.com/teran/mcp-sandbox/application/tools/handler"
	profiletool "github.com/teran/mcp-sandbox/application/tools/profile"
	sandboxtool "github.com/teran/mcp-sandbox/application/tools/sandbox"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

// NewServer builds the go-sdk MCP server (SPEC §5) with the SDK slog logger
// wired into logrus (L07/L03GO), server-level instructions, every ToolDef
// registered through the shared handler-wrapper, and the self-describing
// guidance resources. Tool errors are returned as tool-error results (not
// protocol errors) so the model-host can read and self-correct.
func NewServer(impl *mcp.Implementation, logger *logging.Logger, tools []*def.ToolDef, deps handler.Dependencies) *mcp.Server {
	server := mcp.NewServer(impl, &mcp.ServerOptions{
		Logger:       logging.NewSlogLogger(logger.Logger()),
		Instructions: guidance.Instructions(),
	})

	registerResources(server)

	for _, tool := range tools {
		tt := tool
		server.AddTool(toSDKTool(tt), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return handler.Call(ctx, deps, tt, req)
		})
	}
	return server
}

// BuildTools assembles the sandbox ToolDefs plus the read-only profile_list
// tool from a concrete Service. It lives here (not in cmd) so the concrete
// infrastructure type never crosses the adapter edge in the composition root.
// The injection is passed through the application-layer interface so the
// deep-scan linter sees only the interface dependency (SPEC §3/§5), not the
// concrete infrastructure type.
func BuildTools(svc *Service) []*def.ToolDef {
	var s sandboxtool.Service = svc
	tools := sandboxtool.All(s)
	if svc.profiles != nil {
		tools = append(tools, profiletool.All(svc.profiles)...)
	}
	return tools
}

// toSDKTool maps an application-layer ToolDef to the go-sdk mcp.Tool. Because
// the SDK Tool has no per-tool Instructions field, the instructions are folded
// into the description so the model-host still receives them (M04). The
// closed-world annotations are always set explicitly: the SDK defaults
// DestructiveHint and OpenWorldHint to true when nil, which would mislabel
// non-destructive tools, so every hint is pinned.
func toSDKTool(t *def.ToolDef) *mcp.Tool {
	desc := t.Description
	if t.Instructions != "" {
		desc = t.Instructions + "\n\n" + desc
	}
	return &mcp.Tool{
		Name:        t.Name,
		Title:       t.Title,
		Description: desc,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    t.Annotations.ReadOnlyHint,
			DestructiveHint: new(t.Annotations.DestructiveHint),
			IdempotentHint:  t.Annotations.IdempotentHint,
			OpenWorldHint:   new(t.Annotations.OpenWorldHint),
			Title:           t.Title,
		},
		InputSchema:  t.InputSchema,
		OutputSchema: t.OutputSchema,
	}
}
