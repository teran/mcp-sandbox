package registry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
)

func validDef(name string) *def.ToolDef {
	return &def.ToolDef{
		Name:         name,
		Title:        "T",
		Description:  "D",
		Instructions: "I",
		InputSchema:  &jsonschema.Schema{Type: "object"},
		OutputSchema: &jsonschema.Schema{Type: "object"},
		Handler:      func(_ context.Context, _ json.RawMessage) (any, error) { return nil, nil },
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := New()
	d := validDef("sandbox_list")
	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("sandbox_list")
	if !ok {
		t.Fatal("Get: not found")
	}
	if got != d {
		t.Fatal("Get: wrong tool")
	}
}

func TestRegister_Duplicate(t *testing.T) {
	r := New()
	if err := r.Register(validDef("x")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(validDef("x")); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegister_Validation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*def.ToolDef)
		want string
	}{
		{"nil", nil, "nil tool"},
		{"empty name", func(d *def.ToolDef) { d.Name = "" }, "name is required"},
		{"empty title", func(d *def.ToolDef) { d.Title = "" }, "title is required"},
		{"empty description", func(d *def.ToolDef) { d.Description = "" }, "description is required"},
		{"empty instructions", func(d *def.ToolDef) { d.Instructions = "" }, "instructions are required"},
		{"nil input", func(d *def.ToolDef) { d.InputSchema = nil }, "input schema is required"},
		{"nil output", func(d *def.ToolDef) { d.OutputSchema = nil }, "output schema is required"},
		{"nil handler", func(d *def.ToolDef) { d.Handler = nil }, "handler is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := New()
			var d *def.ToolDef
			if c.mut != nil {
				d = validDef("y")
				c.mut(d)
			}
			err := r.Register(d)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestRegister_NonObjectInputSchema(t *testing.T) {
	r := New()
	d := validDef("z")
	d.InputSchema = &jsonschema.Schema{Type: "array"}
	if err := r.Register(d); err == nil {
		t.Fatal("expected non-object input schema error")
	}
}

func TestList_SortedAndLen(t *testing.T) {
	r := New()
	for _, n := range []string{"b", "a", "c"} {
		if err := r.Register(validDef(n)); err != nil {
			t.Fatal(err)
		}
	}
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
	names := make([]string, 0)
	for _, d := range r.List() {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("List order = %v, want sorted", names)
	}
}
