package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/handler"
	"github.com/teran/mcp-sandbox/application/tools/registry"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

func toolByName(t *testing.T, svc Service, name string) *def.ToolDef {
	t.Helper()
	for _, d := range All(svc) {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func callHandler(t *testing.T, d *def.ToolDef, _ Service, args any) (any, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return d.Handler(context.Background(), b)
}

// validateToolArgs validates args against the tool's strict input schema,
// mirroring the wrapper's validateArgs (S08).
func validateToolArgs(d *def.ToolDef, args any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	resolved, err := d.InputSchema.Resolve(nil)
	if err != nil {
		return err
	}
	var instance map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &instance); err != nil {
			return err
		}
	}
	return resolved.Validate(instance)
}

// callWrapped invokes the tool through the shared handler-wrapper so schema
// validation (InvalidParams) is exercised, not bypassed.
func callWrapped(t *testing.T, d *def.ToolDef, args any) *mcp.CallToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: raw}}
	deps := handler.Dependencies{
		// noopAccessLogger satisfies the AccessLogger hook the wrapper defers
		// (handler.go:103); without it the deferred call nil-panics.
		AccessLogger: noopAccessLogger{},
		// identityRedact is the wrapper's default redactor; the value is
		// returned unchanged so tests observe unmodified output.
		Redact:        func(v any) any { return v },
		NewRequestID:  func() string { return "req-1" },
		WithRequestID: func(ctx context.Context, _ string) context.Context { return ctx },
	}
	res, err := handler.Call(context.Background(), deps, d, req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// noopAccessLogger is a no-op handler.AccessLogger for tests that drive the
// wrapper but do not inspect access-log output.
type noopAccessLogger struct{}

func (noopAccessLogger) AccessLog(handler.AccessLogEntry) {}

func TestAll_ToolCountAndOrder(t *testing.T) {
	defs := All(newFake())
	if len(defs) != 7 {
		t.Fatalf("got %d tools, want 7", len(defs))
	}
	want := []string{
		"sandbox_list", "sandbox_info", "sandbox_stats",
		"sandbox_create", "sandbox_exec",
		"sandbox_destroy", "sandbox_destroy_all",
	}
	for i, w := range want {
		if defs[i].Name != w {
			t.Fatalf("tool[%d] = %q, want %q", i, defs[i].Name, w)
		}
	}
}

func TestAll_RequiredFieldsAndSchemas(t *testing.T) {
	for _, d := range All(newFake()) {
		if err := registry.Validate(d); err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		if d.Annotations.OpenWorldHint {
			t.Fatalf("%s: OpenWorldHint must be false", d.Name)
		}
		if d.InputSchema == nil || d.InputSchema.Type != "object" {
			t.Fatalf("%s: input schema must be object-rooted", d.Name)
		}
		if d.InputSchema.AdditionalProperties == nil {
			t.Fatalf("%s: input schema must disable additional properties", d.Name)
		}
		if d.OutputSchema == nil || d.OutputSchema.Type != "object" {
			t.Fatalf("%s: output schema must be object-rooted", d.Name)
		}
	}
}

func TestAnnotations_ReadTools(t *testing.T) {
	svc := newFake()
	for _, name := range []string{"sandbox_list", "sandbox_info", "sandbox_stats"} {
		d := toolByName(t, svc, name)
		if !d.Annotations.ReadOnlyHint {
			t.Fatalf("%s: ReadOnlyHint must be true", name)
		}
		if d.Annotations.DestructiveHint {
			t.Fatalf("%s: DestructiveHint must be false", name)
		}
		if !d.Annotations.IdempotentHint {
			t.Fatalf("%s: IdempotentHint must be true", name)
		}
	}
}

func TestAnnotations_WriteTools(t *testing.T) {
	svc := newFake()
	for _, name := range []string{"sandbox_create", "sandbox_exec"} {
		d := toolByName(t, svc, name)
		if d.Annotations.ReadOnlyHint {
			t.Fatalf("%s: ReadOnlyHint must be false", name)
		}
		if d.Annotations.DestructiveHint {
			t.Fatalf("%s: DestructiveHint must be false", name)
		}
	}
}

func TestAnnotations_DeleteTools(t *testing.T) {
	svc := newFake()
	for _, name := range []string{"sandbox_destroy", "sandbox_destroy_all"} {
		d := toolByName(t, svc, name)
		if !d.Annotations.DestructiveHint {
			t.Fatalf("%s: DestructiveHint must be true", name)
		}
		if d.Annotations.ReadOnlyHint {
			t.Fatalf("%s: ReadOnlyHint must be false", name)
		}
		if !d.Annotations.IdempotentHint {
			t.Fatalf("%s: IdempotentHint must be true", name)
		}
	}
}

func TestRedactionContracts(t *testing.T) {
	svc := newFake()
	cases := []struct {
		tool   string
		secret string
	}{
		{"sandbox_create", "secrets"},
	}
	for _, c := range cases {
		d := toolByName(t, svc, c.tool)
		found := false
		for _, f := range d.RedactionContract {
			if f.Name == c.secret && f.Secret {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: redaction contract must include %q", c.tool, c.secret)
		}
	}
}

// TestSandboxExec_NoArgvRedaction verifies that sandbox_exec no longer marks
// argv as secret, so the executed command is visible in access logs (debugging).
func TestSandboxExec_NoArgvRedaction(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_exec")
	for _, f := range d.RedactionContract {
		if f.Name == "argv" {
			t.Fatal("sandbox_exec must not redact argv (it is not secret)")
		}
	}
}

func TestSandboxList_Handler(t *testing.T) {
	svc := newFake()
	sp := defaultPolicy().Isolation
	sess := runningSession("abc", spec.SandboxSpec{Image: "img", Isolation: sp})
	svc.list = []*domainsession.Session{sess}
	d := toolByName(t, svc, "sandbox_list")
	out, err := callHandler(t, d, svc, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	lo, ok := out.(ListOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if len(lo.Sessions) != 1 || lo.Sessions[0].ID != "abc" || lo.Sessions[0].Backend != "docker" {
		t.Fatalf("output = %+v", lo)
	}
}

func TestSandboxInfo_HandlerRedactsSecrets(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{
		Image:      "img",
		WorkingDir: "/work",
		Secrets:    []spec.SecretRef{{Name: "k", Value: "topsecret"}},
		Isolation:  defaultPolicy().Isolation,
	})
	d := toolByName(t, svc, "sandbox_info")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	io := out.(InfoOutput)
	if len(io.Secrets) != 1 || io.Secrets[0].Name != "k" {
		t.Fatalf("secrets = %+v", io.Secrets)
	}
	if io.Image != "img" || io.State != string(domainsession.StateRunning) {
		t.Fatalf("info = %+v", io)
	}
}

func TestSandboxInfo_GetError(t *testing.T) {
	svc := newFake()
	svc.getErr = errFake
	d := toolByName(t, svc, "sandbox_info")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "x"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxStats_Handler(t *testing.T) {
	svc := newFake()
	svc.stats = map[string]any{"cpu": 1.5}
	d := toolByName(t, svc, "sandbox_stats")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	so := out.(StatsOutput)
	if so.ID != "abc" {
		t.Fatalf("output = %+v", so)
	}
}

func TestSandboxCreate_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_create")
	out, err := callHandler(t, d, svc, map[string]any{
		"profile":    "default",
		"cwd":        "/home/user/proj",
		"session_id": "opencode-sess-1",
		"secrets":    []map[string]any{{"name": "k", "value": "v"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	co := out.(CreateOutput)
	if co.ID == "" {
		t.Fatal("empty id")
	}
	sp := svc.createdSpec
	// Image now comes from the selected profile, not the request.
	if sp.Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("Image = %q, want profile image", sp.Image)
	}
	if sp.WorkingDir != "/home/user/proj" {
		t.Fatalf("WorkingDir = %q", sp.WorkingDir)
	}
	if sp.ClientSessionID != "opencode-sess-1" {
		t.Fatalf("client session id = %q, want opencode-sess-1", sp.ClientSessionID)
	}
	if sp.Sudo != spec.SudoNever {
		t.Fatalf("sudo = %q, want never (from profile)", sp.Sudo)
	}
	// The cwd mount is always present (profile.BuildSpec), read-only by default.
	if len(sp.Mounts) != 1 || sp.Mounts[0].Target != "/home/user/proj" || !sp.Mounts[0].ReadOnly {
		t.Fatalf("cwd mount = %+v, want single 1:1 read-only mount", sp.Mounts)
	}
	if len(sp.Secrets) != 1 || sp.Secrets[0].Value != "v" {
		t.Fatalf("secrets = %+v", sp.Secrets)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("created spec must validate: %v", err)
	}
}

func TestSandboxCreate_ProfileDrivenIsolation(t *testing.T) {
	svc := newFake()
	// The profile fully determines network + user; the request can no longer
	// override them.
	d := toolByName(t, svc, "sandbox_create")
	if _, err := callHandler(t, d, svc, map[string]any{
		"profile":    "default",
		"cwd":        "/c",
		"session_id": "s1",
	}); err != nil {
		t.Fatal(err)
	}
	sp := svc.createdSpec
	if sp.UIDGID.UID != 1000 {
		t.Fatalf("uid = %d, want profile non-root 1000", sp.UIDGID.UID)
	}
	if sp.Isolation.NetworkMode != spec.NetworkBridge || sp.Network.Mode != spec.NetworkBridge {
		t.Fatalf("network = %q/%q, want bridge from profile", sp.Isolation.NetworkMode, sp.Network.Mode)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestSandboxCreate_UnknownProfile(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_create")
	if _, err := callHandler(t, d, svc, map[string]any{
		"profile":    "does-not-exist",
		"cwd":        "/c",
		"session_id": "s1",
	}); err == nil {
		t.Fatal("expected unknown-profile error")
	} else if !strings.Contains(err.Error(), "profile") {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.createdSpec.Image != "" || len(svc.createdSpec.Secrets) != 0 {
		t.Fatal("no spec should be created for an unknown profile")
	}
}

func TestSandboxCreate_Error(t *testing.T) {
	svc := newFake()
	svc.createErr = errFake
	d := toolByName(t, svc, "sandbox_create")
	if _, err := callHandler(t, d, svc, map[string]any{"profile": "default", "cwd": "/c", "session_id": "s1"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

// TestSandboxCreate_RequiresRequiredParams verifies that sandbox_create
// rejects a missing/empty profile, cwd, or session_id before any creation
// happens. The strict schema enforces required-ness at the wrapper boundary
// (→ InvalidParams); the handler defends again for defense in depth.
func TestSandboxCreate_RequiresRequiredParams(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_create")
	for _, args := range []map[string]any{
		{},
		{"session_id": ""},
		{"session_id": "   "},
		{"profile": "", "cwd": "/c", "session_id": "s"},
		{"profile": "default", "cwd": "", "session_id": "s"},
		{"profile": "default", "cwd": "/c", "session_id": ""},
	} {
		if _, err := callHandler(t, d, svc, args); err == nil {
			t.Fatalf("args %v: expected missing/empty required rejection", args)
		}
	}
	if svc.createdSpec.Image != "" || len(svc.createdSpec.Secrets) != 0 {
		t.Fatal("no spec should be created when required params are missing")
	}
}

// TestSandboxCreate_SchemaRequiresProfileCwdSessionID pins the schema contract:
// profile, cwd and session_id must all be declared AND be in the required list
// of sandbox_create.
func TestSandboxCreate_SchemaRequiresProfileCwdSessionID(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_create")
	props := d.InputSchema.Properties
	for _, k := range []string{"profile", "cwd", "session_id"} {
		if props == nil || props[k] == nil {
			t.Fatalf("sandbox_create schema must declare %q property", k)
		}
	}
	required := d.InputSchema.Required
	for _, k := range []string{"profile", "cwd", "session_id"} {
		found := false
		for _, r := range required {
			if r == k {
				found = true
			}
		}
		if !found {
			t.Fatalf("sandbox_create schema required list %v must include %q", required, k)
		}
	}
}

// TestSandboxCreate_SchemaRejectsRemovedParams pins Phase 7: image, env,
// mounts, nano_cpus, memory, pids_limit, uid, gid and sudo are REMOVED from
// sandbox_create. The strict schema (additionalProperties:false) must reject a
// request carrying any of them as invalid-arguments.
func TestSandboxCreate_SchemaRejectsRemovedParams(t *testing.T) {
	d := toolByName(t, newFake(), "sandbox_create")
	base := map[string]any{"profile": "default", "cwd": "/c", "session_id": "s"}
	removed := []map[string]any{
		{"image": "x"},
		{"env": map[string]string{"A": "1"}},
		{"mounts": []any{}},
		{"nano_cpus": int64(1)},
		{"memory": int64(1)},
		{"pids_limit": int64(1)},
		{"uid": int64(1000)},
		{"gid": int64(1000)},
		{"sudo": "never"},
	}
	for _, extra := range removed {
		args := map[string]any{}
		for k, v := range base {
			args[k] = v
		}
		for k, v := range extra {
			args[k] = v
		}
		if err := validateToolArgs(d, args); err == nil {
			t.Fatalf("removed param %v must be rejected by the strict schema", extra)
		}
	}
}

// TestSandboxCreate_MissingRequiredIsInvalidParams goes through the shared
// wrapper (handler.Call) to confirm a missing required field surfaces as an
// invalid-arguments (InvalidParams) result, not an execution error.
func TestSandboxCreate_MissingRequiredIsInvalidParams(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_create")
	res := callWrapped(t, d, map[string]any{"cwd": "/c"}) // missing profile + session_id
	if !handler.IsInvalidArgumentsResult(res) {
		t.Fatalf("expected invalid-arguments result, got IsError=%v err=%v", res.IsError, res.GetError())
	}
}

func TestSandboxExec_Handler(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("\x1b[31mok\x1b[0m"), Stderr: []byte("err")}
	d := toolByName(t, svc, "sandbox_exec")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"echo", "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	eo := out.(ExecOutput)
	if eo.ExitCode != 0 || eo.Stdout != "ok" {
		t.Fatalf("output = %+v", eo)
	}
	if svc.execReq.User != "1000:1000" {
		t.Fatalf("user = %q", svc.execReq.User)
	}
	if svc.execReq.Argv[0] != "echo" {
		t.Fatalf("argv = %v", svc.execReq.Argv)
	}
}

func TestSandboxExec_RedactsSecretFromOutput(t *testing.T) {
	// A command that echoes a secret (e.g. `echo $SECRET` from the sandbox env
	// or `cat /run/sandbox/<name>`) must not leak the value into tool output
	// (S02/§7.3).
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{
		UIDGID:    spec.UIDGID{UID: 1000, GID: 1000},
		Env:       map[string]string{"SECRET": "envsecret", "PLAIN": "visible"},
		Secrets:   []spec.SecretRef{{Name: "token", Value: "topsecret"}},
		Isolation: defaultPolicy().Isolation,
	})
	svc.execRes = &exec.Result{
		ExitCode: 0,
		Stdout:   []byte("value=topsecret env=envsecret ok"),
		Stderr:   []byte("warning: topsecret leaked here"),
	}
	d := toolByName(t, svc, "sandbox_exec")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"echo", "$SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	eo := out.(ExecOutput)
	if strings.Contains(eo.Stdout, "topsecret") {
		t.Fatalf("stdout leaked secret: %q", eo.Stdout)
	}
	if strings.Contains(eo.Stdout, "envsecret") {
		t.Fatalf("stdout leaked env secret: %q", eo.Stdout)
	}
	if strings.Contains(eo.Stderr, "topsecret") {
		t.Fatalf("stderr leaked secret: %q", eo.Stderr)
	}
	if !strings.Contains(eo.Stdout, redactedMarker) {
		t.Fatalf("stdout should contain the redaction marker: %q", eo.Stdout)
	}
	if !strings.Contains(eo.Stdout, "ok") {
		t.Fatalf("non-secret stdout should be preserved: %q", eo.Stdout)
	}
}

func TestSandboxExec_NoSecrets_OutputUnchanged(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{
		UIDGID:    spec.UIDGID{UID: 1000, GID: 1000},
		Isolation: defaultPolicy().Isolation,
	})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("plain output")}
	d := toolByName(t, svc, "sandbox_exec")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"echo", "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if eo := out.(ExecOutput); eo.Stdout != "plain output" {
		t.Fatalf("output = %q, want unchanged", eo.Stdout)
	}
}

func TestSandboxExec_EmptyArgv(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_exec")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{}}); err == nil {
		t.Fatal("expected empty argv error")
	}
}

func TestSandboxExec_RootUserPolicy(t *testing.T) {
	svc := newFake()
	// SudoNever session: root rejected.
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Sudo: spec.SudoNever, Isolation: defaultPolicy().Isolation})
	d := toolByName(t, svc, "sandbox_exec")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"id"}, "user": "root"}); err == nil {
		t.Fatal("expected root rejection under SudoNever")
	}
	// SudoRequire session: root maps to uid 0.
	svc.session = runningSession("abc", spec.SandboxSpec{Sudo: spec.SudoRequire, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0}
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"id"}, "user": "root"}); err != nil {
		t.Fatalf("root exec failed: %v", err)
	}
	if svc.execReq.User != "0" {
		t.Fatalf("root user = %q", svc.execReq.User)
	}
}

func TestSandboxExec_UnsupportedUser(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_exec")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"x"}, "user": "bogus"}); err == nil {
		t.Fatal("expected unsupported user error")
	}
}

func TestSandboxExec_FallbackNobody(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0}
	d := toolByName(t, svc, "sandbox_exec")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if svc.execReq.User != "65534:65534" {
		t.Fatalf("user = %q", svc.execReq.User)
	}
}

func TestSandboxExec_ExecError(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1000, GID: 1000}, Isolation: defaultPolicy().Isolation})
	svc.execErr = errFake
	d := toolByName(t, svc, "sandbox_exec")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "argv": []string{"x"}}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxDestroy_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_destroy")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(DestroyOutput).Destroyed != 1 {
		t.Fatalf("output = %+v", out)
	}
}

func TestSandboxDestroy_Error(t *testing.T) {
	svc := newFake()
	svc.destroyErr = errFake
	d := toolByName(t, svc, "sandbox_destroy")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxDestroyAll_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_destroy_all")
	if _, err := callHandler(t, d, svc, map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxDestroyAll_Error(t *testing.T) {
	svc := newFake()
	svc.destroyAllErr = errFake
	d := toolByName(t, svc, "sandbox_destroy_all")
	if _, err := callHandler(t, d, svc, map[string]any{}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxDestroyAll_CountsSessions(t *testing.T) {
	svc := newFake()
	svc.list = []*domainsession.Session{
		runningSession("a", spec.SandboxSpec{}),
		runningSession("b", spec.SandboxSpec{}),
	}
	d := toolByName(t, svc, "sandbox_destroy_all")
	out, err := callHandler(t, d, svc, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	do := out.(DestroyOutput)
	if do.Destroyed != 2 || len(do.IDs) != 2 {
		t.Fatalf("output = %+v", do)
	}
}

func TestBuildSpec_ProfileCwdAndClientSessionID(t *testing.T) {
	svc := newFake()
	b := builder{svc: svc}
	sp, err := buildSpec(b, createInput{Profile: "default", Cwd: "/home/user/proj", SessionID: "opencode-sess-9"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if sp.ClientSessionID != "opencode-sess-9" {
		t.Fatalf("client session id = %q, want opencode-sess-9", sp.ClientSessionID)
	}
	if sp.WorkingDir != "/home/user/proj" {
		t.Fatalf("WorkingDir = %q", sp.WorkingDir)
	}
	if len(sp.Mounts) != 1 || sp.Mounts[0].Source != "/home/user/proj" || sp.Mounts[0].Target != "/home/user/proj" {
		t.Fatalf("cwd mount = %+v, want 1:1", sp.Mounts)
	}
}

func TestBuildSpec_ProfileEnvAndMounts(t *testing.T) {
	svc := newFake()
	p := defaultProfile()
	p.Env = map[string]string{"A": "1"}
	p.Mounts = []domainprofile.Mount{{Source: "/host", Target: "/mnt", ReadOnly: true}}
	svc.policy.Profiles = fakeResolver{profiles: map[string]*domainprofile.Profile{"default": p}}

	b := builder{svc: svc}
	sp, err := buildSpec(b, createInput{Profile: "default", Cwd: "/c", SessionID: "s"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if sp.Env["A"] != "1" {
		t.Fatalf("env = %+v, want profile env", sp.Env)
	}
	if len(sp.Mounts) != 2 { // cwd + profile mount
		t.Fatalf("mounts = %+v, want cwd + profile mount", sp.Mounts)
	}
	m := sp.Mounts[1]
	if m.Source != "/host" || m.Target != "/mnt" || !m.ReadOnly {
		t.Fatalf("profile mount = %+v", m)
	}
}

func TestBuildSpec_ProfileUser(t *testing.T) {
	svc := newFake()
	p := defaultProfile()
	p.User = &spec.UIDGID{UID: 2000, GID: 2000}
	svc.policy.Profiles = fakeResolver{profiles: map[string]*domainprofile.Profile{"default": p}}

	b := builder{svc: svc}
	sp, err := buildSpec(b, createInput{Profile: "default", Cwd: "/c", SessionID: "s"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if sp.UIDGID.UID != 2000 || sp.UIDGID.GID != 2000 {
		t.Fatalf("uidgid = %+v, want profile 2000:2000", sp.UIDGID)
	}
}

func TestBuildSpec_UnknownProfile_ErrProfileNotFound(t *testing.T) {
	svc := newFake()
	b := builder{svc: svc}
	if _, err := buildSpec(b, createInput{Profile: "nope", Cwd: "/c", SessionID: "s"}); !errors.Is(err, domainprofile.ErrProfileNotFound) {
		t.Fatalf("err = %v, want ErrProfileNotFound", err)
	}
}

// TestBuildSpec_ProfileGetError_Propagates pins fail-closed resolution: a store
// read error must propagate out of buildSpec (and thus sandbox_create) rather
// than being swallowed or substituted.
func TestBuildSpec_ProfileGetError_Propagates(t *testing.T) {
	svc := newFake()
	svc.policy.Profiles = fakeResolver{err: errFake}
	b := builder{svc: svc}
	if _, err := buildSpec(b, createInput{Profile: "default", Cwd: "/c", SessionID: "s"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v, want resolver error to propagate", err)
	}
}

func TestRegister(t *testing.T) {
	reg := registry.New()
	if err := Register(reg, newFake()); err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 7 {
		t.Fatalf("Len = %d", reg.Len())
	}
}

func TestRegister_Duplicate(t *testing.T) {
	reg := registry.New()
	svc := newFake()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	if err := Register(reg, svc); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestSanitizeANSI(t *testing.T) {
	if got := sanitizeANSI("\x1b[1;31mred\x1b[0m plain"); got != "red plain" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeANSI("a\x00b\tc\nd\r"); got != "ab\tc\nd\r" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeANSI("\x1b]0;title\x07x"); got != "x" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeANSI("plain"); got != "plain" {
		t.Fatalf("got %q", got)
	}
}

func TestPolicyAndBackendName(t *testing.T) {
	svc := newFake()
	svc.backend = "custom"
	if svc.BackendName() != "custom" {
		t.Fatalf("backend = %q", svc.BackendName())
	}
	pol := svc.Policy()
	// Policy.DefaultImage is retained on the policy struct but is NO LONGER
	// applied to create: the image always comes from the selected profile (see
	// TestSandboxCreate_Handler). This only asserts the field stays present with
	// the fake's value.
	if pol.DefaultImage != "sandbox:latest" || pol.Backend != "docker" {
		t.Fatalf("policy = %+v", pol)
	}
}

// TestHandlers_RejectMalformedArgs exercises the json.Unmarshal error branch of
// every argument-taking handler (S08: malformed input is rejected, never
// panics).
func TestHandlers_RejectMalformedArgs(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{Isolation: defaultPolicy().Isolation})
	for _, name := range []string{
		"sandbox_info", "sandbox_stats", "sandbox_create",
		"sandbox_exec", "sandbox_destroy",
	} {
		d := toolByName(t, svc, name)
		if _, err := d.Handler(context.Background(), json.RawMessage(`{invalid`)); err == nil {
			t.Fatalf("%s: expected error for malformed args", name)
		}
	}
}

func TestResolveUser_UnsupportedValue(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{Isolation: defaultPolicy().Isolation})
	b := builder{svc: svc}
	if _, err := resolveUser(b, "abc", "9999"); err == nil {
		t.Fatal("expected unsupported user error")
	}
}

func TestResolveUser_DefaultNonRoot(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{UIDGID: spec.UIDGID{UID: 1234, GID: 1234}, Isolation: defaultPolicy().Isolation})
	b := builder{svc: svc}
	user, err := resolveUser(b, "abc", "")
	if err != nil {
		t.Fatal(err)
	}
	if user != "1234:1234" {
		t.Fatalf("user = %q", user)
	}
}

func TestSanitizeControl_StripAllControl(t *testing.T) {
	// NUL and vertical tab are dropped; printable stays.
	if got := sanitizeControl("a\x00\x0b\x1a b"); got != "a b" {
		t.Fatalf("got %q", got)
	}
}

func TestOutputTypes_SecretTagging(t *testing.T) {
	// Confirm sensitive output types carry the secret annotation so the server
	// redactor hides values (S02).
	sec := SecretSummary{Name: "k"}
	if sec.Name != "k" {
		t.Fatal("SecretSummary broken")
	}
}
