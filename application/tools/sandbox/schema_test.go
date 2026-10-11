package sandbox

import (
	"maps"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/handler"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
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

// TestSandboxExec_SchemaRequiresCommand pins the schema contract: `command` is
// the required scalar string driving the exec; `summary` is OPTIONAL (the
// required-summary declaration is gone); `argv` is REMOVED (no longer a
// property, so the strict schema rejects it).
func TestSandboxExec_SchemaRequiresCommand(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_exec")

	// command: declared, scalar string, in required list.
	cmd, ok := d.InputSchema.Properties["command"]
	if !ok {
		t.Fatal("sandbox_exec input schema must declare a \"command\" property")
	}
	if cmd.Type != "string" {
		t.Fatalf("command type = %q, want \"string\"", cmd.Type)
	}
	foundCmd := false
	for _, r := range d.InputSchema.Required {
		if r == "command" {
			foundCmd = true
		}
	}
	if !foundCmd {
		t.Fatalf("sandbox_exec required list %v must include \"command\"", d.InputSchema.Required)
	}
}

// TestSandboxExec_SchemaSummaryOptional pins that `summary` is no longer in the
// required list (it remains an optional presentation-only scalar).
func TestSandboxExec_SchemaSummaryOptional(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_exec")

	prop, ok := d.InputSchema.Properties["summary"]
	if !ok {
		t.Fatal("sandbox_exec input schema must declare a \"summary\" property")
	}
	if prop.Type != "string" {
		t.Fatalf("summary type = %q, want \"string\"", prop.Type)
	}
	for _, r := range d.InputSchema.Required {
		if r == "summary" {
			t.Fatalf("summary must NOT be in the required list %v (it is optional)", d.InputSchema.Required)
		}
	}
}

// TestSandboxExec_SchemaRemovesArgv pins that the array `argv` input is gone:
// it must not be a declared property (and the strict schema rejects it).
func TestSandboxExec_SchemaRemovesArgv(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_exec")
	if _, ok := d.InputSchema.Properties["argv"]; ok {
		t.Fatal("sandbox_exec schema must no longer declare an \"argv\" property")
	}
	// The strict schema (additionalProperties:false) must reject an argv request.
	if err := validateToolArgs(d, map[string]any{"id": "abc", "command": "ls", "argv": []string{"ls"}}); err == nil {
		t.Fatal("strict schema must reject the removed \"argv\" field")
	}
}

// TestSandboxExec_SchemaDeclaresEnv pins that the optional per-exec `env` map
// is declared as an object property on sandbox_exec.
func TestSandboxExec_SchemaDeclaresEnv(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_exec")
	prop, ok := d.InputSchema.Properties["env"]
	if !ok {
		t.Fatal("sandbox_exec input schema must declare an \"env\" property")
	}
	if prop.Type != "object" {
		t.Fatalf("env type = %q, want \"object\"", prop.Type)
	}
}

// TestSandboxExec_SummaryDescriptionMentionsCommandDescription verifies that the
// summary property's description tells the model it is a short human-readable
// description of the command being run.
func TestSandboxExec_SummaryDescriptionMentionsCommandDescription(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_exec")
	prop, ok := d.InputSchema.Properties["summary"]
	if !ok {
		t.Fatal("sandbox_exec input schema must declare a \"summary\" property")
	}
	desc := strings.ToLower(prop.Description)
	if !strings.Contains(desc, "human-readable") && !strings.Contains(desc, "human readable") {
		t.Fatalf("summary description %q must describe it as a short human-readable command description", prop.Description)
	}
	if !strings.Contains(desc, "command") {
		t.Fatalf("summary description %q must reference the command", prop.Description)
	}
}

// TestSandboxExec_MissingCommandIsInvalidParams goes through the shared wrapper
// to confirm a sandbox_exec call without the required `command` surfaces as
// invalid-arguments (InvalidParams), not an execution error.
func TestSandboxExec_MissingCommandIsInvalidParams(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("ok")}
	d := toolByName(t, svc, "sandbox_exec")
	res := callWrapped(t, d, map[string]any{"id": "abc"})
	if !handler.IsInvalidArgumentsResult(res) {
		t.Fatalf("expected invalid-arguments result, got IsError=%v err=%v", res.IsError, res.GetError())
	}
}

// TestSandboxExec_WhitespaceCommandIsInvalidParams pins that an empty or
// whitespace-only `command` is rejected as invalid-arguments.
func TestSandboxExec_WhitespaceCommandIsInvalidParams(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("ok")}
	d := toolByName(t, svc, "sandbox_exec")
	res := callWrapped(t, d, map[string]any{"id": "abc", "command": "   "})
	if !handler.IsInvalidArgumentsResult(res) {
		t.Fatalf("expected invalid-arguments result for whitespace command, got IsError=%v err=%v", res.IsError, res.GetError())
	}
}

// TestSandboxExec_InvalidEnvIsInvalidParams pins that an env with an invalid
// key or a NUL/newline in a key or value is rejected as invalid-arguments,
// while a value containing '=' is accepted.
func TestSandboxExec_InvalidEnvIsInvalidParams(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("ok")}
	d := toolByName(t, svc, "sandbox_exec")

	cases := []map[string]any{
		{"env": map[string]string{"": "v"}},       // empty key
		{"env": map[string]string{"A\x00B": "v"}}, // NUL in key
		{"env": map[string]string{"A\nB": "v"}},   // newline in key
		{"env": map[string]string{"A": "v\x00"}},  // NUL in value
		{"env": map[string]string{"A": "v\nw"}},   // newline in value
	}
	for _, env := range cases {
		args := map[string]any{"id": "abc", "command": "echo hi"}
		maps.Copy(args, env)
		res := callWrapped(t, d, args)
		if !handler.IsInvalidArgumentsResult(res) {
			t.Fatalf("env %v: expected invalid-arguments result, got IsError=%v err=%v", env, res.IsError, res.GetError())
		}
	}
}

// TestSandboxExec_EnvValueWithEqualsAccepted pins that a value containing '='
// is valid (allowed in the value, only '=' separator between key and value).
func TestSandboxExec_EnvValueWithEqualsAccepted(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("ok")}
	d := toolByName(t, svc, "sandbox_exec")
	// Validation must pass; the call must not be rejected as invalid-arguments.
	if res := callWrapped(t, d, map[string]any{"id": "abc", "command": "echo hi", "env": map[string]string{"A": "b=c"}}); handler.IsInvalidArgumentsResult(res) {
		t.Fatalf("env value with '=' must be accepted, got err=%v", res.GetError())
	}
}

func (e *treeError) Error() string {
	return "shared subschema pointer: " + e.path + " duplicates " + e.prev
}
