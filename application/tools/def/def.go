// Package def defines the declarative tool model (SPEC §6.4): a ToolDef is a
// complete, strongly-typed declaration of an MCP tool — its identity,
// annotations, per-tool instructions, strict input/output schemas, redaction
// contract and the application-layer handler that implements it. Tools are
// grouped read → write → delete (S03) and are always invoked through the shared
// handler-wrapper (validate → access-log → dispatch → redact → structured
// output), never directly.
package def

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// RedactionField declares a single field of the tool's output (or input) that
// carries sensitive data and must be redacted from output and logs (S02). The
// shared wrapper honours these alongside `secret:"true"` struct tags.
type RedactionField struct {
	// Name is the JSON path of the field, e.g. "argv" or "spec.secrets".
	Name string
	// Secret marks the field as secret, so it is replaced with the redaction
	// marker everywhere it is emitted.
	Secret bool
}

// Annotations carries the MCP tool annotations (S03). All hints default to the
// safe closed-world values mandated by SPEC §4.
type Annotations struct {
	// ReadOnlyHint marks a read-only tool.
	ReadOnlyHint bool
	// DestructiveHint marks a destructive tool (HITL, S12).
	DestructiveHint bool
	// IdempotentHint marks a tool with a real data-level idempotency mechanism
	// (X02/N25).
	IdempotentHint bool
	// OpenWorldHint is always false for the sandbox domain (S07/S10).
	OpenWorldHint bool
}

// ToolHandler implements one tool. It receives the already-validated raw
// arguments (S08) and returns a structured output value (S09). It returns a
// regular error for a tool-level failure, which the wrapper turns into an
// error result.
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

// ToolDef is the complete declaration of a tool (SPEC §6.4). Every field is
// typed; there is no free-form blob. The shared handler-wrapper is the single
// place where validation, access-logging, redaction and structured output are
// guaranteed, so no tool can bypass it.
type ToolDef struct {
	// Name is the unique tool name (e.g. "sandbox_list").
	Name string
	// Title is a short human-readable title.
	Title string
	// Description explains the tool to the model-host (M07).
	Description string
	// Annotations carries the read/write/delete hints (S03).
	Annotations Annotations
	// Instructions is the per-tool guidance exposed to the model-host (M04).
	Instructions string
	// InputSchema is the strict argument schema. It is object-rooted with
	// additionalProperties:false (S08) and validated before execution.
	InputSchema *jsonschema.Schema
	// OutputSchema is the structured output schema (S09).
	OutputSchema *jsonschema.Schema
	// RedactionContract lists the sensitive fields of the output (S02). The
	// wrapper redacts these from every emitted value.
	RedactionContract []RedactionField
	// Handler implements the tool.
	Handler ToolHandler
}
