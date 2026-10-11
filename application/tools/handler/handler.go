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

// TraceLogger emits trace-level diagnostics (gated behind LOG_LEVEL=trace). It
// is implemented by an infrastructure adapter over the server logger;
// application code never logs directly (L05). Fields are carried forward by
// WithFields so request_id and tool stay on every subsequent line (L09).
type TraceLogger interface {
	WithFields(fields map[string]any) TraceLogger
	Tracef(format string, args ...any)
	Debugf(format string, args ...any)
}

// noopTraceLogger discards every trace line. It is the nil-safe default so a
// wrapper without an injected logger still runs unchanged.
type noopTraceLogger struct{}

func (noopTraceLogger) WithFields(map[string]any) TraceLogger { return noopTraceLogger{} }
func (noopTraceLogger) Tracef(string, ...any)                 {}
func (noopTraceLogger) Debugf(string, ...any)                 {}

// Dependencies are the injected services the wrapper needs. They are provided
// by the composition root so the application layer stays free of
// infrastructure dependencies.
type Dependencies struct {
	// AccessLogger emits the access-log line (L08).
	AccessLogger AccessLogger
	// TraceLogger emits trace-level diagnostics (L09). Nil-safe: when unset a
	// no-op logger is used.
	TraceLogger TraceLogger
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

	// Trace diagnostics are gated at LOG_LEVEL=trace and carry request_id and
	// tool on every line (L09). Nil-safe: an unset TraceLogger is a no-op.
	trace := deps.TraceLogger
	if trace == nil {
		trace = noopTraceLogger{}
	}
	trace = trace.WithFields(map[string]any{"request_id": requestID, "tool": tool.Name})
	trace.Tracef("handler: call start")

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
		trace.Tracef("handler: call done")
	}()

	outcome = executeTool(ctx, deps, tool, req, result, argRedact, trace)
	return result, nil
}

// executeTool runs the four-step tool pipeline: validate arguments against the
// strict input schema (S08), dispatch to the application-layer handler, redact
// the structured output (S02/S09) and marshal it into the result. It returns
// the access-log outcome ("ok" or "error").
func executeTool(
	ctx context.Context,
	deps Dependencies,
	tool *def.ToolDef,
	req *mcp.CallToolRequest,
	result *mcp.CallToolResult,
	argRedact func(any) any,
	trace TraceLogger,
) string {
	// 1. Validate arguments against the strict input schema (S08). A failed
	// validation rejects the call as an invalid-params tool error before any
	// execution happens.
	trace.Tracef("handler: validating args")
	if err := validateArgs(tool.InputSchema, req.Params.Arguments); err != nil {
		verr := &invalidArgumentsError{err: fmt.Errorf("%s: invalid arguments: %w", tool.Name, err)}
		result.SetError(verr)
		return "error"
	}
	trace.Tracef("handler: args valid")

	// 2. Dispatch to the application-layer handler.
	trace.Tracef("handler: dispatch start")
	output, err := tool.Handler(ctx, req.Params.Arguments)
	if err != nil {
		result.SetError(err)
		return "error"
	}
	trace.Tracef("handler: dispatch done")

	// 3. Redact the structured output (S02/S09): first through the injected
	// struct-tag redactor (deps.Redact), then through the contract's secret
	// field names for defense-in-depth.
	trace.Tracef("handler: redacting output")
	output = deps.Redact(output)
	output = argRedact(output)

	// 4. Return structured output (S09).
	trace.Tracef("handler: marshaling output")
	raw, err := json.Marshal(output)
	if err != nil {
		result.SetError(fmt.Errorf("%s: marshal output: %w", tool.Name, err))
		return "error"
	}
	result.StructuredContent = json.RawMessage(raw)
	result.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
	return "ok"
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

// NewInvalidArgumentsError builds an invalid-arguments (S08) error — the same
// kind the wrapper's schema-validation failure produces — so a handler can
// reject semantically-invalid-but-schema-valid input (e.g. an empty command or
// a malformed env map) and have it surface to the model-host as InvalidParams
// rather than as an execution error.
func NewInvalidArgumentsError(format string, args ...any) error {
	return &invalidArgumentsError{err: fmt.Errorf(format, args...)}
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
