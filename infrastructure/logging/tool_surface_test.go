package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/application/tools/def"
)

// TestLogToolSurface_EnabledEmitsPerTool pins L10/L11 at the logging layer:
// when logging is enabled, LogToolSurface emits exactly one line per registered
// tool, each carrying tool=<name>, the tool title, and always_present=true
// (every tool in this server is static/always present).
func TestLogToolSurface_EnabledEmitsPerTool(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "surface.log")
	l, err := New(Options{Level: "info", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	tools := []*def.ToolDef{
		{Name: "sandbox_list", Title: "List sandbox sessions"},
		{Name: "sandbox_create", Title: "Create a sandbox"},
		{Name: "sandbox_destroy", Title: "Destroy a sandbox"},
	}

	l.LogToolSurface(tools)

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)

	// Exactly one line per tool: a double log would inflate the count, a
	// missing tool would under-count.
	if got := strings.Count(s, "tool="); got != len(tools) {
		t.Fatalf("got %d tool= lines, want %d:\n%s", got, len(tools), s)
	}
	for _, tool := range tools {
		if !strings.Contains(s, "tool="+tool.Name) {
			t.Fatalf("missing tool=%q line:\n%s", tool.Name, s)
		}
		if !strings.Contains(s, tool.Title) {
			t.Fatalf("missing title %q on tool=%q line:\n%s", tool.Title, tool.Name, s)
		}
	}
	if !strings.Contains(s, "always_present=true") {
		t.Fatalf("expected always_present=true marker:\n%s", s)
	}
}

// TestLogToolSurface_DisabledIsNoop pins L02: when logging is disabled
// (Level empty), LogToolSurface must be a no-op and must not create or write
// to the log file.
func TestLogToolSurface_DisabledIsNoop(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "surface.log")
	l, err := New(Options{Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.LogToolSurface([]*def.ToolDef{{Name: "sandbox_list", Title: "List sandbox sessions"}})

	if _, err := os.Stat(filename); err == nil {
		t.Fatal("log file must not be created when logging is disabled")
	}
}

// TestLogToolSurface_EmptySurfaceIsNoop pins the degenerate case: an empty
// (nil) tool surface emits no tool lines.
func TestLogToolSurface_EmptySurfaceIsNoop(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "surface.log")
	l, err := New(Options{Level: "info", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	l.LogToolSurface(nil)

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "tool=") {
		t.Fatalf("expected no tool lines for empty surface:\n%s", data)
	}
}
