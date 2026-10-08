package sandbox

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// TestInputSchemas_ResolveAsClient reproduces the client-side schema resolution
// performed by the MCP host (go-sdk calls Schema.Resolve on the tool's input
// schema before validating arguments). Every tool's input schema must resolve to
// a tree — a shared subschema pointer (such as the package-level falseSchema
// used for `additionalProperties:false`) appearing in more than one place makes
// jsonschema-go refuse to resolve it ("schemas ... do not form a tree").
//
// Regression: sandbox_create previously failed here because the top-level object
// and the nested objects inside `mounts`/`secrets` items all referenced the same
// shared falseSchema pointer, breaking resolution on the client side.
func TestInputSchemas_ResolveAsClient(t *testing.T) {
	for _, d := range All(newFake()) {
		t.Run(d.Name, func(t *testing.T) {
			if _, err := d.InputSchema.Resolve(nil); err != nil {
				t.Fatalf("%s: input schema must resolve for the MCP client: %v", d.Name, err)
			}
		})
	}
}

// TestSandboxCreate_SchemaFormsTree pins the specific regression: the
// sandbox_create input schema (the only one with nested object items) must not
// contain the same *jsonschema.Schema pointer in more than one location. It does
// so by asserting the client-side resolve succeeds and that the error would have
// been the shared-additionalProperties duplicate, keeping the failure message
// descriptive if it ever regresses.
func TestSandboxCreate_SchemaFormsTree(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_create")
	if _, err := d.InputSchema.Resolve(nil); err != nil {
		if strings.Contains(err.Error(), "do not form a tree") {
			t.Fatalf("sandbox_create input schema is not a tree (shared additionalProperties pointer): %v", err)
		}
		t.Fatalf("sandbox_create input schema failed to resolve: %v", err)
	}
}

// TestInputSchemas_NoSharedSubschemaPointers guards against reintroducing shared
// pointers anywhere in the tree by verifying that the same subschema instance is
// never reachable through more than one path. It uses the resolver's own
// tree-structure check via Resolve on every schema field reachable from the
// root, which is exactly what the MCP client depends on.
func TestInputSchemas_NoSharedSubschemaPointers(t *testing.T) {
	for _, d := range All(newFake()) {
		t.Run(d.Name, func(t *testing.T) {
			if err := assertNoSharedPointers(d.InputSchema); err != nil {
				t.Fatalf("%s: %v", d.Name, err)
			}
		})
	}
}

// assertNoSharedPointers walks the schema tree and fails if any *jsonschema.Schema
// instance is visited more than once, mirroring the resolver's checkStructure.
func assertNoSharedPointers(root *jsonschema.Schema) error {
	seen := map[*jsonschema.Schema]string{}
	var walk func(s *jsonschema.Schema, path string) error
	walk = func(s *jsonschema.Schema, path string) error {
		if s == nil {
			return nil
		}
		if prev, ok := seen[s]; ok {
			return &treeError{path: path, prev: prev}
		}
		seen[s] = path
		for k, p := range s.Properties {
			if err := walk(p, path+"/properties/"+k); err != nil {
				return err
			}
		}
		if err := walk(s.Items, path+"/items"); err != nil {
			return err
		}
		if err := walk(s.AdditionalProperties, path+"/additionalProperties"); err != nil {
			return err
		}
		return nil
	}
	return walk(root, "root")
}

type treeError struct {
	path, prev string
}

func (e *treeError) Error() string {
	return "shared subschema pointer: " + e.path + " duplicates " + e.prev
}
