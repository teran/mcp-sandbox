package handler

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/domain/progress"
)

// captureSink records every event pushed through it.
type captureSink struct {
	mu     sync.Mutex
	events []progress.Event
}

func (c *captureSink) sink(_ context.Context, e progress.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureSink) got() []progress.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]progress.Event, len(c.events))
	copy(out, c.events)

	return out
}

// TestCall_StashesProgressSinkInContext verifies that Call injects the sink
// returned by Dependencies.ProgressSink into the context seen by the dispatched
// tool handler, so every tool can emit progress via progress.SinkFromContext.
func TestCall_StashesProgressSinkInContext(t *testing.T) {
	al := &fakeAccessLogger{}
	cs := &captureSink{}
	want := progress.Event{Message: "running...", Progress: 1, Total: 5}

	// The handler reads the sink from its context and emits one event.
	emitted := false
	d := testTool(func(ctx context.Context, _ json.RawMessage) (any, error) {
		sink := progress.SinkFromContext(ctx)
		sink(ctx, want)
		emitted = true

		return map[string]any{"result": "ok"}, nil
	})

	deps := newDeps(al, nil)
	deps.ProgressSink = func(*mcp.CallToolRequest) progress.Sink { return cs.sink }

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	res, err := Call(context.Background(), deps, d, req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %v", res.GetError())
	}
	if !emitted {
		t.Fatal("tool handler did not run")
	}
	ev := cs.got()
	if len(ev) != 1 || ev[0] != want {
		t.Fatalf("events = %+v, want [%+v]", ev, want)
	}
}

// TestCall_NoProgressSink_HandlerGetsNilSafeSink verifies that when
// Dependencies.ProgressSink is nil, the tool handler still receives a nil-safe
// sink via SinkFromContext and emitting through it does not panic or error.
func TestCall_NoProgressSink_HandlerGetsNilSafeSink(t *testing.T) {
	al := &fakeAccessLogger{}
	called := false
	d := testTool(func(ctx context.Context, _ json.RawMessage) (any, error) {
		sink := progress.SinkFromContext(ctx)
		sink(ctx, progress.Event{Message: "probe"}) // must not panic
		called = true

		return map[string]any{"result": "ok"}, nil
	})

	// newDeps leaves ProgressSink nil.
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	res, err := Call(context.Background(), newDeps(al, nil), d, req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %v", res.GetError())
	}
	if !called {
		t.Fatal("tool handler did not run")
	}
}

// TestCall_ProgressSinkRetrievableByAllTools verifies the injection is generic:
// any tool (read-only here) inherits the sink stashed by the wrapper.
func TestCall_ProgressSinkRetrievableByAllTools(t *testing.T) {
	al := &fakeAccessLogger{}
	cs := &captureSink{}
	seen := false
	d := testTool(func(ctx context.Context, _ json.RawMessage) (any, error) {
		sink := progress.SinkFromContext(ctx)
		if sink == nil {
			t.Fatal("expected a non-nil sink in context")
		}
		seen = true

		return map[string]any{}, nil
	})

	deps := newDeps(al, nil)
	deps.ProgressSink = func(*mcp.CallToolRequest) progress.Sink { return cs.sink }

	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: marshalArgs(t, map[string]any{"path": "/x"})}}
	if _, err := Call(context.Background(), deps, d, req); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !seen {
		t.Fatal("handler did not observe the injected sink")
	}
}
