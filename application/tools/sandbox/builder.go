package sandbox

import (
	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/registry"
)

// builder wires a Service into the concrete tool handlers. A new builder is
// created per wiring so every tool closes over the same service.
type builder struct {
	svc Service
}

// tool assembles a ToolDef with the given annotations, schemas, redaction
// contract and handler.
func (b builder) tool(
	name, title, desc, instructions string,
	ann def.Annotations,
	in, out *jsonschema.Schema,
	redact []def.RedactionField,
	h def.ToolHandler,
) *def.ToolDef {
	return &def.ToolDef{
		Name:              name,
		Title:             title,
		Description:       desc,
		Instructions:      instructions,
		Annotations:       ann,
		InputSchema:       in,
		OutputSchema:      out,
		RedactionContract: redact,
		Handler:           h,
	}
}

// All returns every sandbox tool definition, ordered read → write → delete
// (S03).
func All(svc Service) []*def.ToolDef {
	b := builder{svc: svc}
	return []*def.ToolDef{
		sandboxListDef(b),
		sandboxInfoDef(b),
		sandboxStatsDef(b),
		sandboxCreateDef(b),
		sandboxExecDef(b),
		sandboxDestroyDef(b),
		sandboxDestroyAllDef(b),
	}
}

// Register registers every sandbox tool into the given registry.
func Register(reg *registry.Registry, svc Service) error {
	for _, t := range All(svc) {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}
