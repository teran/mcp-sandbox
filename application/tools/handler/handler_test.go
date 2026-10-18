package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/tools/def"
)

// fakeAccessLogger records every access-log entry emitted by the wrapper.
type fakeAccessLogger struct {
	mu  sync.Mutex
	got []AccessLogEntry
}

func (f *fakeAccessLogger) AccessLog(e AccessLogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, e)
}

func (f *fakeAccessLogger) entries() []AccessLogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AccessLogEntry, len(f.got))
	copy(out, f.got)

	return out
}

// identityRedact returns the value unchanged; the wrapper still calls it.
func identityRedact(v any) any { return v }

func newDeps(al AccessLogger, redact func(any) any) Dependencies {
	if redact == nil {
		redact = identityRedact
	}

	return Dependencies{
		AccessLogger:  al,
		Redact:        redact,
		NewRequestID:  func() string { return "req-123" },
		WithRequestID: func(ctx context.Context, id string) context.Context { return context.WithValue(ctx, k("rid"), id) },
	}
}

type k string

// traceRecorder captures every field set and trace line emitted by a fake
// TraceLogger.
type traceRecorder struct {
	mu        sync.Mutex
	fieldSets []map[string]any
	lines     []string
}

// fakeTraceLogger records WithFields/Tracef calls for assertions. WithFields
// merges into the current field set so the final fields on each line are the
// union across the chain.
type fakeTraceLogger struct {
	rec    *traceRecorder
	fields map[string]any
}

func (t *fakeTraceLogger) WithFields(fields map[string]any) TraceLogger {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	merged := make(map[string]any, len(t.fields)+len(fields))
	maps.Copy(merged, t.fields)
	maps.Copy(merged, fields)
	t.rec.fieldSets = append(t.rec.fieldSets, merged)

	return &fakeTraceLogger{rec: t.rec, fields: merged}
}

func (t *fakeTraceLogger) Tracef(format string, args ...any) {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	t.rec.lines = append(t.rec.lines, fmt.Sprintf(format, args...))
}

func (t *fakeTraceLogger) Debugf(format string, args ...any) {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	t.rec.lines = append(t.rec.lines, fmt.Sprintf(format, args...))
}

func (t *fakeTraceLogger) sawRequestID(id string) bool {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	for _, fs := range t.rec.fieldSets {
		if fs["request_id"] == id {
			return true
		}
	}

	return false
}

func (t *fakeTraceLogger) sawTool(tool string) bool {
	t.rec.mu.Lock()
	defer t.rec.mu.Unlock()
	for _, fs := range t.rec.fieldSets {
		if fs["tool"] == tool {
			return true
		}
	}

	return false
}

// TestCall_TraceLoggerCarriesRequestIDAndTool asserts the handler threads the
// generated request_id and the tool name into every trace line via WithFields
// (L09), and that trace diagnostics are actually emitted.
func TestCall_TraceLoggerCarriesRequestIDAndTool(t *testing.T) {
	rec := &traceRecorder{}
	tl := &fakeTraceLogger{rec: rec}
	deps := newDeps(&fakeAccessLogger{}, nil)
	deps.TraceLogger = tl

	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{"result": "ok"}, nil
	})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	if _, err := Call(context.Background(), deps, d, req); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if !tl.sawRequestID("req-123") {
		t.Fatalf("trace lines did not carry request_id: %+v", rec.fieldSets)
	}
	if !tl.sawTool("sandbox_test") {
		t.Fatalf("trace lines did not carry tool: %+v", rec.fieldSets)
	}
	rec.mu.Lock()
	lines := len(rec.lines)
	rec.mu.Unlock()
	if lines == 0 {
		t.Fatal("no trace lines emitted")
	}
}

// TestCall_NilTraceLoggerIsNoop asserts an unset TraceLogger does not panic.
func TestCall_NilTraceLoggerIsNoop(t *testing.T) {
	deps := newDeps(&fakeAccessLogger{}, nil) // TraceLogger left nil
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{"result": "ok"}, nil
	})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	if _, err := Call(context.Background(), deps, d, req); err != nil {
		t.Fatalf("Call: %v", err)
	}
}

func stringSchema(props map[string]*jsonschema.Schema, required []string) *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

func testTool(h def.ToolHandler) *def.ToolDef {
	return &def.ToolDef{
		Name:         "sandbox_test",
		Title:        "T",
		Description:  "D",
		Instructions: "I",
		InputSchema:  stringSchema(map[string]*jsonschema.Schema{"path": {Type: "string"}}, []string{"path"}),
		OutputSchema: &jsonschema.Schema{Type: "object"},
		Handler:      h,
	}
}

func marshalArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestCall_DispatchAndStructuredOutput(t *testing.T) {
	al := &fakeAccessLogger{}
	var gotArgv json.RawMessage
	d := testTool(func(_ context.Context, args json.RawMessage) (any, error) {
		gotArgv = args

		return map[string]any{"result": "ok"}, nil
	})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatal("unexpected IsError")
	}
	if string(gotArgv) != `{"path":"/x"}` {
		t.Fatalf("handler got args %s", gotArgv)
	}
	if res.StructuredContent == nil {
		t.Fatal("StructuredContent not set")
	}
	var out map[string]any
	raw, ok := res.StructuredContent.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage, got %T", res.StructuredContent)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["result"] != "ok" {
		t.Fatalf("output = %v", out)
	}

	entries := al.entries()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Tool != "sandbox_test" || e.Source != "STDIO" || e.Outcome != "ok" {
		t.Fatalf("entry = %+v", e)
	}
	if e.RequestID != "req-123" {
		t.Fatalf("request id = %q", e.RequestID)
	}
	if e.Duration < 0 {
		t.Fatal("duration negative")
	}
}

func TestCall_InvalidArguments(t *testing.T) {
	al := &fakeAccessLogger{}
	called := false
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) {
		called = true

		return map[string]any{}, nil
	})
	// Missing required "path".
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{})}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if called {
		t.Fatal("handler should not run on invalid args")
	}
	if !res.IsError {
		t.Fatal("expected IsError")
	}
	if !IsInvalidArgumentsResult(res) {
		t.Fatal("expected invalid-arguments error")
	}
	entries := al.entries()
	if len(entries) != 1 || entries[0].Outcome != "error" {
		t.Fatalf("access log = %+v", entries)
	}
}

func TestCall_RejectsAdditionalProperties(t *testing.T) {
	al := &fakeAccessLogger{}
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{}, nil })
	// "extra" is not in the schema.
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x", "extra": 1})}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !IsInvalidArgumentsResult(res) {
		t.Fatalf("expected rejection of additional property, got IsError=%v", res.IsError)
	}
}

func TestCall_HandlerError(t *testing.T) {
	al := &fakeAccessLogger{}
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected IsError on handler error")
	}
	if IsInvalidArgumentsResult(res) {
		t.Fatal("handler error should not be invalid-arguments")
	}
	if len(al.entries()) != 1 || al.entries()[0].Outcome != "error" {
		t.Fatalf("access log = %+v", al.entries())
	}
}

func TestCall_RedactsOutput(t *testing.T) {
	al := &fakeAccessLogger{}
	redactCalls := 0
	redact := func(_ any) any {
		redactCalls++

		return "REDACTED"
	}
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{"secret": "value"}, nil
	})
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	res, err := Call(context.Background(), newDeps(al, redact), d, req)
	if err != nil {
		t.Fatal(err)
	}
	if redactCalls < 1 {
		t.Fatal("redact not called on output")
	}
	raw, ok := res.StructuredContent.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage, got %T", res.StructuredContent)
	}
	if string(raw) != `"REDACTED"` {
		t.Fatalf("redacted output = %s", res.StructuredContent)
	}
}

func TestCall_EmptyArguments(t *testing.T) {
	al := &fakeAccessLogger{}
	// A tool with an empty (fully optional) object schema accepts empty args.
	d := &def.ToolDef{
		Name:         "sandbox_empty",
		Title:        "T",
		Description:  "D",
		Instructions: "I",
		InputSchema:  &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}},
		OutputSchema: &jsonschema.Schema{Type: "object"},
		Handler:      func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{}, nil },
	}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("empty args should validate: %v", res.GetError())
	}
}

func TestCall_InvalidJSON(t *testing.T) {
	al := &fakeAccessLogger{}
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{}, nil })
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{invalid`)}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !IsInvalidArgumentsResult(res) {
		t.Fatalf("expected invalid-args for malformed JSON, got IsError=%v", res.IsError)
	}
}

func TestAccessLogEntry(t *testing.T) {
	e := AccessLogEntry{Tool: "x", Duration: time.Second, Outcome: "ok"}
	if e.Duration != time.Second {
		t.Fatal("duration not set")
	}
}

// hoistTool returns a ToolDef whose AccessLogHoist contract declares command
// and summary (the sandbox_exec contract), so the wrapper must hoist those
// redacted args to top-level access-log fields and drop the args map (L08).
func hoistTool() *def.ToolDef {
	return &def.ToolDef{
		Name:           "sandbox_exec",
		Title:          "T",
		Description:    "D",
		Instructions:   "I",
		AccessLogHoist: []string{"command", "summary"},
		InputSchema: stringSchema(map[string]*jsonschema.Schema{
			"id":      {Type: "string"},
			"command": {Type: "string"},
			"summary": {Type: "string"},
		}, []string{"id", "command"}),
		OutputSchema: &jsonschema.Schema{Type: "object"},
		Handler:      func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{}, nil },
	}
}

// TestCall_HoistsCommandAndSummaryAndDropsArgs pins the new sandbox_exec
// access-log behavior: for a tool that declares AccessLogHoist, the redacted
// command and summary become top-level fields on the AccessLogEntry and the
// args map is dropped entirely (no args= field) (L08).
func TestCall_HoistsCommandAndSummaryAndDropsArgs(t *testing.T) {
	al := &fakeAccessLogger{}
	d := hoistTool()
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{
		"id":      "seatbelt:darwin-2",
		"command": "git status ...",
		"summary": "verify status",
	})}}
	if _, err := Call(context.Background(), newDeps(al, nil), d, req); err != nil {
		t.Fatalf("Call: %v", err)
	}
	entries := al.entries()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Tool != "sandbox_exec" {
		t.Fatalf("Tool = %q, want sandbox_exec", e.Tool)
	}
	if e.Command != "git status ..." {
		t.Fatalf("Command = %q, want %q", e.Command, "git status ...")
	}
	if e.Summary != "verify status" {
		t.Fatalf("Summary = %q, want %q", e.Summary, "verify status")
	}
	if e.Args != nil {
		t.Fatalf("Args = %#v, want nil (args map dropped for hoisted tool)", e.Args)
	}
}

// TestCall_HoistedToolMissesArgsKeysStillDropsArgs pins that a hoisted tool
// whose redacted args lack one of the declared keys must not leave a partial
// args map and must not panic; the missing key's field stays empty.
func TestCall_HoistedToolMissesArgsKeysStillDropsArgs(t *testing.T) {
	al := &fakeAccessLogger{}
	d := hoistTool()
	// Omit "summary" from the arguments.
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{
		"id":      "seatbelt:darwin-2",
		"command": "git status ...",
	})}}
	if _, err := Call(context.Background(), newDeps(al, nil), d, req); err != nil {
		t.Fatalf("Call: %v", err)
	}
	e := al.entries()[0]
	if e.Command != "git status ..." {
		t.Fatalf("Command = %q", e.Command)
	}
	if e.Summary != "" {
		t.Fatalf("Summary = %q, want empty (key absent from args)", e.Summary)
	}
	if e.Args != nil {
		t.Fatalf("Args = %#v, want nil even with a missing hoisted key", e.Args)
	}
}

// TestCall_NonHoistedToolKeepsArgsMap pins that tools without an
// AccessLogHoist contract (i.e. every tool except sandbox_exec) keep the
// current behavior: Args stays populated with the redacted map and Command/
// Summary stay empty (L08).
func TestCall_NonHoistedToolKeepsArgsMap(t *testing.T) {
	al := &fakeAccessLogger{}
	d := testTool(func(_ context.Context, _ json.RawMessage) (any, error) { return map[string]any{}, nil })
	if len(d.AccessLogHoist) != 0 {
		t.Fatalf("testTool unexpectedly declares AccessLogHoist %v", d.AccessLogHoist)
	}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	if _, err := Call(context.Background(), newDeps(al, nil), d, req); err != nil {
		t.Fatalf("Call: %v", err)
	}
	entries := al.entries()
	if len(entries) != 1 {
		t.Fatalf("access log entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Command != "" {
		t.Fatalf("Command = %q, want empty for non-hoisted tool", e.Command)
	}
	if e.Summary != "" {
		t.Fatalf("Summary = %q, want empty for non-hoisted tool", e.Summary)
	}
	if e.Args == nil {
		t.Fatal("Args is nil, want redacted args map for non-hoisted tool")
	}
	m, ok := e.Args.(map[string]any)
	if !ok {
		t.Fatalf("Args = %T, want map[string]any", e.Args)
	}
	if m["path"] != "/x" {
		t.Fatalf("Args = %#v, want path=/x", m)
	}
}
