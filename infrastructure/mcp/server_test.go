package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/application/profile"
	"github.com/teran/mcp-sandbox/application/tools/def"
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
