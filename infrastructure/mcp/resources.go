package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/tools/guidance"
)

// registerResources registers every self-describing guidance resource
// (SPEC §5) on the server so the model-host can list and read them over the
// MCP resources protocol. Unknown URIs return a not-found error.
func registerResources(server *mcp.Server) {
	for _, g := range guidance.All() {
		server.AddResource(&mcp.Resource{
			URI:         g.URI,
			Name:        g.Name,
			Title:       g.Title,
			Description: g.Description,
			MIMEType:    g.MIMEType,
		}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			uri := req.Params.URI
			content, ok := guidance.Content(uri)
			if !ok {
				return nil, mcp.ResourceNotFoundError(uri)
			}

			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{
					URI:      uri,
					MIMEType: g.MIMEType,
					Text:     content,
				}},
			}, nil
		})
	}
}
