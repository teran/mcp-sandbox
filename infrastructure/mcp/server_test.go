package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/application/profile"
	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/guidance"
	"github.com/teran/mcp-sandbox/application/tools/handler"
	"github.com/teran/mcp-sandbox/infrastructure/backend"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

func testDef(name string) *def.ToolDef {
	return &def.ToolDef{
		Name:         name,
		Title:        "T",
		Description:  "D",
		Instructions: "I",
		Annotations:  def.Annotations{OpenWorldHint: false},
		InputSchema:  &jsonschema.Schema{Type: "object"},
		OutputSchema: &jsonschema.Schema{Type: "object"},
		Handler:      func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
}

func TestToSDKTool_AnnotationPinning(t *testing.T) {
	d := testDef("sandbox_x")
	d.Annotations = def.Annotations{ReadOnlyHint: true, OpenWorldHint: false}
	sdk := toSDKTool(d)
	if sdk.Name != "sandbox_x" || sdk.Title != "T" {
		t.Fatalf("tool = %+v", sdk)
	}
	a := sdk.Annotations
	if !a.ReadOnlyHint {
		t.Fatal("ReadOnlyHint not set")
	}
	if a.DestructiveHint == nil || *a.DestructiveHint {
		t.Fatalf("DestructiveHint must be explicitly false, got %v", a.DestructiveHint)
	}
	if a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Fatalf("OpenWorldHint must be explicitly false, got %v", a.OpenWorldHint)
	}
	if a.IdempotentHint {
		t.Fatal("IdempotentHint should be false")
	}
	if a.Title != "T" {
		t.Fatal("title annotation not set")
	}
}

func TestToSDKTool_DescriptionIncludesInstructions(t *testing.T) {
	d := testDef("sandbox_y")
	d.Instructions = "Use carefully."
	sdk := toSDKTool(d)
	if sdk.Description != "Use carefully.\n\nD" {
		t.Fatalf("description = %q", sdk.Description)
	}
}

// TestToSDKTool_DescriptionFoldsResourcePointer pins that when a tool's
// instructions carry a `sandbox://guidance/` resource pointer, the SDK tool
// description (which has no separate instructions field) must fold it in so the
// model-host still receives the pointer (M04).
func TestToSDKTool_DescriptionFoldsResourcePointer(t *testing.T) {
	d := testDef("sandbox_pointer")
	d.Instructions = "See sandbox://guidance/exec for details."
	sdk := toSDKTool(d)
	if !strings.Contains(sdk.Description, "sandbox://guidance/exec") {
		t.Fatalf("description %q must fold in the resource pointer", sdk.Description)
	}
}

func TestToSDKTool_NoInstructions(t *testing.T) {
	d := testDef("sandbox_z")
	d.Instructions = ""
	sdk := toSDKTool(d)
	if sdk.Description != "D" {
		t.Fatalf("description = %q", sdk.Description)
	}
}

func TestAccessLogger_EmitsLine(t *testing.T) {
	var buf bytes.Buffer
	base := logrus.New()
	base.SetOutput(&buf)
	al := NewAccessLogger(base.WithField("server", "test"))
	al.AccessLog(handler.AccessLogEntry{Tool: "sandbox_list", Args: map[string]any{}, Source: "STDIO", Outcome: "ok", RequestID: "r1"})
	if !bytes.Contains(buf.Bytes(), []byte("tools/call")) {
		t.Fatalf("log line missing: %s", buf.String())
	}
}

func TestHandlerDependencies(t *testing.T) {
	l, err := logging.New(logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	deps := HandlerDependencies(l)
	if deps.AccessLogger == nil || deps.Redact == nil || deps.NewRequestID == nil || deps.WithRequestID == nil {
		t.Fatal("incomplete dependencies")
	}
	if id := deps.NewRequestID(); id == "" {
		t.Fatal("empty request id")
	}
}

func TestNewServer_RegistersTools(t *testing.T) {
	l, err := logging.New(logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools := []*def.ToolDef{testDef("sandbox_a"), testDef("sandbox_b")}
	s := NewServer(&mcp.Implementation{Name: "mcp-sandbox", Version: "dev"}, l, tools, HandlerDependencies(l))
	if s == nil {
		t.Fatal("server is nil")
	}
}

// connectClient wires an in-memory MCP client session to a built server so
// tests can observe the server's instructions and resources over the protocol
// (server.Connect must precede client.Connect).
func connectClient(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := s.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "dev"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func newTestServer(t *testing.T) *mcp.Server {
	t.Helper()
	l, err := logging.New(logging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(&mcp.Implementation{Name: "mcp-sandbox", Version: "dev"}, l,
		[]*def.ToolDef{testDef("sandbox_a")}, HandlerDependencies(l))
}

// TestServer_Instructions asserts the built server's server-level instructions
// are the guidance package's instructions (i.e. NewServer wired
// ServerOptions.Instructions = guidance.Instructions()).
func TestServer_Instructions(t *testing.T) {
	cs := connectClient(t, newTestServer(t))
	got := cs.InitializeResult()
	if got == nil {
		t.Fatal("client session has no initialize result")
	}
	if got.Instructions != guidance.Instructions() {
		t.Fatalf("server instructions = %q, want guidance.Instructions() = %q", got.Instructions, guidance.Instructions())
	}
}

// TestServer_RegistersResources asserts resources/list returns every guidance
// URI. The registry must be non-empty (not vacuous) and must contain all of
// guidance.URIs().
func TestServer_RegistersResources(t *testing.T) {
	cs := connectClient(t, newTestServer(t))
	res, err := cs.ListResources(context.Background(), &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("resources/list: %v", err)
	}
	registered := map[string]bool{}
	for _, r := range res.Resources {
		registered[r.URI] = true
	}
	uris := guidance.URIs()
	if len(uris) == 0 {
		t.Fatal("guidance.URIs() must be non-empty")
	}
	if len(res.Resources) != len(uris) {
		t.Fatalf("server registered %d resources, want %d", len(res.Resources), len(uris))
	}
	for _, uri := range uris {
		if !registered[uri] {
			t.Fatalf("server did not register guidance resource %q", uri)
		}
	}
}

// TestServer_ReadResource asserts resources/read of each guidance URI returns
// the matching markdown body with text/markdown MIME type.
func TestServer_ReadResource(t *testing.T) {
	cs := connectClient(t, newTestServer(t))
	ctx := context.Background()
	uris := guidance.URIs()
	if len(uris) == 0 {
		t.Fatal("guidance.URIs() must be non-empty")
	}
	for _, uri := range uris {
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("resources/read %q: %v", uri, err)
		}
		if len(res.Contents) == 0 {
			t.Fatalf("resources/read %q returned no contents", uri)
		}
		want, ok := guidance.Content(uri)
		if !ok {
			t.Fatalf("guidance.Content(%q) unexpectedly not found", uri)
		}
		if res.Contents[0].Text != want {
			t.Fatalf("read %q text mismatch: got %q, want %q", uri, res.Contents[0].Text, want)
		}
		if res.Contents[0].MIMEType != "text/markdown" {
			t.Fatalf("read %q MIME = %q, want text/markdown", uri, res.Contents[0].MIMEType)
		}
	}
}

// TestServer_ReadResource_UnknownURI asserts an unknown URI yields a not-found
// error (no panic).
func TestServer_ReadResource_UnknownURI(t *testing.T) {
	cs := connectClient(t, newTestServer(t))
	_, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "sandbox://guidance/does-not-exist"})
	if err == nil {
		t.Fatal("expected a not-found error for an unknown resource URI")
	}
}

// TestServer_ResourceRead_NoRedactionMarker (S02) asserts reading every
// resource yields no redaction marker in the returned body.
func TestServer_ResourceRead_NoRedactionMarker(t *testing.T) {
	cs := connectClient(t, newTestServer(t))
	ctx := context.Background()
	uris := guidance.URIs()
	if len(uris) == 0 {
		t.Fatal("guidance.URIs() must be non-empty")
	}
	for _, uri := range uris {
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("resources/read %q: %v", uri, err)
		}
		if len(res.Contents) == 0 {
			t.Fatalf("resources/read %q returned no contents", uri)
		}
		if strings.Contains(res.Contents[0].Text, "***REDACTED***") {
			t.Fatalf("read %q leaked a redaction marker into guidance content", uri)
		}
	}
}

// TestBuildTools_EmptyDirStoreKeepsProfileList pins that when the server wires
// a profile store rooted at an empty directory (the read-on-demand default),
// the profile_list tool must stay in the toolset. Regression: a nil store
// previously dropped profile_list entirely.
func TestBuildTools_EmptyDirStoreKeepsProfileList(t *testing.T) {
	svc := NewService(backend.NewRegistry(), backend.Config{})
	svc.SetProfileStore(profile.New(t.TempDir()))

	hasProfileList := false
	for _, d := range BuildTools(svc) {
		if d.Name == "profile_list" {
			hasProfileList = true
		}
	}
	if !hasProfileList {
		t.Fatal("profile_list missing from toolset with an empty (non-nil) profile store")
	}
}
