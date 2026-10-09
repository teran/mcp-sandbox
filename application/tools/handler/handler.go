// Package handler implements the shared handler-wrapper (SPEC §6.4) that every
// tool must pass through. It guarantees a uniform pipeline on each call:
//
//	validate args vs InputSchema (S08) → access-log at info (L08) → dispatch
//	→ redact output (S02/S09) → structured output (S09)
//
// No tool bypasses this wrapper. The wrapper is transport-agnostic and depends
// only on the application-layer tool model and on the go-sdk MCP result types;
// logging and redaction are injected as small interfaces so the application
// layer never touches logrus or the redaction helper directly.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/domain/progress"
)

// Source identifies the transport of a call. This Local stdio-only server
// always reports "STDIO" (L08).
const Source = "STDIO"

// AccessLogEntry carries the structured fields of a per-call access-log line
// (L08): tool name, already-redacted args, source, duration and outcome.
type AccessLogEntry struct {
	// Tool is the tool name.
	Tool string
	// Args are the redacted arguments (never secret-bearing).
	Args any
	// Source is the transport source ("STDIO").
	Source string
	// Duration is the wall-clock time of the call.
	Duration time.Duration
	// Outcome is "ok" or "error".
	Outcome string
	// RequestID correlates this call across logs (L09).
	RequestID string
}

// AccessLogger writes the per-call access-log line. It is implemented by an
// infrastructure adapter over the server logger; application code never logs
// directly.
type AccessLogger interface {
	AccessLog(entry AccessLogEntry)
}

// Dependencies are the injected services the wrapper needs. They are provided
// by the composition root so the application layer stays free of
// infrastructure dependencies.
type Dependencies struct {
	// AccessLogger emits the access-log line (L08).
	AccessLogger AccessLogger
	// Redact redacts sensitive values (S02) at the output boundary.
	Redact func(any) any
	// NewRequestID generates an opaque request id (L09).
	NewRequestID func() string
	// WithRequestID stores a request id in the context (L09).
	WithRequestID func(ctx context.Context, id string) context.Context
	// ProgressSink optionally returns a progress Sink for the given tool
	// request. When non-nil it is stashed into the request context before
	// dispatch, so any tool can emit progress notifications via
	// progress.SinkFromContext (Phase 6, SPEC §5.1). Nil-safe: when unset no
	// sink is stashed and SinkFromContext yields the no-op.
	ProgressSink func(*mcp.CallToolRequest) progress.Sink
}

// Call is the shared handler-wrapper. It validates the raw arguments against
// the tool's input schema (S08), emits the access-log line (L08), dispatches to
// the tool handler, redacts the structured output (S02/S09) and returns an MCP
// CallToolResult whose StructuredContent conforms to the tool's output schema
// (S09). Errors from the handler are turned into tool-error results, not
// protocol errors, so the model-host can read and self-correct.
func Call(ctx context.Context, deps Dependencies, tool *def.ToolDef, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	start := time.Now()
	requestID := deps.NewRequestID()
	ctx = deps.WithRequestID(ctx, requestID)

	// Stash the progress sink into the context before dispatch (Phase 6,
	// SPEC §5.1). This is generic: every tool inherits it and can emit live
	// progress via progress.SinkFromContext. Nil-safe.
	if deps.ProgressSink != nil {
		ctx = progress.WithSink(ctx, deps.ProgressSink(req))
	}

	// Redact the raw arguments for the access log (S08/S02); never log the
	// full argv or other sensitive values. Raw JSON carries no struct tags, so
	// the tool's redaction contract drives key-level redaction.
	argRedact := argRedactor(tool.RedactionContract)
	redactedArgs := argRedact(parseArgs(req.Params.Arguments))

	result := &mcp.CallToolResult{Content: []mcp.Content{}}
	outcome := "ok"

	defer func() {
		deps.AccessLogger.AccessLog(AccessLogEntry{
			Tool:      tool.Name,
			Args:      redactedArgs,
			Source:    Source,
			Duration:  time.Since(start),
			Outcome:   outcome,
			RequestID: requestID,
		})
	}()

	// 1. Validate arguments against the strict input schema (S08). A failed
	// validation rejects the call as an invalid-params tool error before any
	// execution happens.
	if err := validateArgs(tool.InputSchema, req.Params.Arguments); err != nil {
		outcome = "error"
		verr := &invalidArgumentsError{err: fmt.Errorf("%s: invalid arguments: %w", tool.Name, err)}
		result.SetError(verr)
		return result, nil
	}

	// 2. Dispatch to the application-layer handler.
	output, err := tool.Handler(ctx, req.Params.Arguments)
	if err != nil {
		outcome = "error"
		result.SetError(err)
		return result, nil
	}

	// 3. Redact the structured output (S02/S09): first through the injected
	// struct-tag redactor (deps.Redact), then through the contract's secret
	// field names for defense-in-depth.
	output = deps.Redact(output)
	output = argRedact(output)

	// 4. Return structured output (S09).
	raw, err := json.Marshal(output)
	if err != nil {
		outcome = "error"
		result.SetError(fmt.Errorf("%s: marshal output: %w", tool.Name, err))
		return result, nil
	}
	result.StructuredContent = json.RawMessage(raw)
	result.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
	return result, nil
}

// validateArgs validates the raw arguments against the tool's input schema.
// The schema is object-rooted with additionalProperties disabled (S08), so any
// unexpected field is rejected. An empty arguments document validates as an
// empty object.
func validateArgs(schema *jsonschema.Schema, args json.RawMessage) error {
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("resolve schema: %w", err)
	}

	var instance map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &instance); err != nil {
			return fmt.Errorf("unmarshal arguments: %w", err)
		}
	}
	return resolved.Validate(instance)
}

// parseArgs unmarshals raw arguments into a generic JSON value for redaction
// and access-logging. Invalid or empty input is reduced to an empty object so
// the log never fails.
func parseArgs(args json.RawMessage) any {
	var v any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &v); err != nil {
			return map[string]any{}
		}
	}
	return v
}

// IsInvalidArguments reports whether err is an invalid-arguments (S08) failure
// produced by the wrapper. It is used by callers/tests to distinguish input
// validation errors from execution errors.
func IsInvalidArguments(err error) bool {
	return err != nil && errors.Is(err, errInvalidArguments)
}

// IsInvalidArgumentsResult reports whether an MCP tool result carries an
// invalid-arguments (S08) failure. The wrapper returns tool errors inside the
// result (not as a Go error), so callers check the result's stored error.
func IsInvalidArgumentsResult(res *mcp.CallToolResult) bool {
	if res == nil {
		return false
	}
	return IsInvalidArguments(res.GetError())
}

// errInvalidArguments wraps an input-validation failure (S08).
var errInvalidArguments = errors.New("invalid arguments")

// invalidArguments marks a validation error so callers can identify it.
type invalidArgumentsError struct{ err error }

func (e *invalidArgumentsError) Error() string { return e.err.Error() }
func (e *invalidArgumentsError) Unwrap() error { return e.err }
func (e *invalidArgumentsError) Is(target error) bool {
	return target == errInvalidArguments
}
