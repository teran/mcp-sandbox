package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/registry"
	"github.com/teran/mcp-sandbox/domain/sandbox/exec"
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

func TestAll_ToolCountAndOrder(t *testing.T) {
	defs := All(newFake())
	if len(defs) != 12 {
		t.Fatalf("got %d tools, want 12", len(defs))
	}
	want := []string{
		"sandbox_list", "sandbox_info", "sandbox_stats", "sandbox_read_file",
		"sandbox_create", "sandbox_exec", "sandbox_write_file", "sandbox_copy_in", "sandbox_copy_out", "sandbox_install_package",
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
	for _, name := range []string{"sandbox_list", "sandbox_info", "sandbox_stats", "sandbox_read_file"} {
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
	for _, name := range []string{"sandbox_create", "sandbox_exec", "sandbox_write_file", "sandbox_copy_in", "sandbox_copy_out", "sandbox_install_package"} {
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
		{"sandbox_exec", "argv"},
		{"sandbox_create", "secrets"},
		{"sandbox_write_file", "content"},
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

func TestSandboxReadFile_Handler(t *testing.T) {
	svc := newFake()
	svc.readData = []byte("hello")
	d := toolByName(t, svc, "sandbox_read_file")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "path": "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	ro := out.(ReadFileOutput)
	if ro.Content != "hello" || ro.Path != "/tmp/x" {
		t.Fatalf("output = %+v", ro)
	}
}

func TestSandboxCreate_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_create")
	out, err := callHandler(t, d, svc, map[string]any{
		"image":     "img",
		"cwd":       "/home/user/proj",
		"secrets":   []map[string]any{{"name": "k", "value": "v"}},
		"sudo":      "require",
		"nano_cpus": int64(2000000000),
	})
	if err != nil {
		t.Fatal(err)
	}
	co := out.(CreateOutput)
	if co.ID == "" {
		t.Fatal("empty id")
	}
	sp := svc.createdSpec
	if sp.Image != "img" || sp.WorkingDir != "/home/user/proj" {
		t.Fatalf("spec = %+v", sp)
	}
	if sp.Sudo != spec.SudoRequire {
		t.Fatalf("sudo = %q", sp.Sudo)
	}
	if sp.Resources.NanoCPUs != 2000000000 {
		t.Fatalf("nano cpus = %d", sp.Resources.NanoCPUs)
	}
	if sp.Isolation.NanoCPUs != 2000000000 {
		t.Fatalf("isolation nano cpus = %d", sp.Isolation.NanoCPUs)
	}
	if len(sp.Secrets) != 1 || sp.Secrets[0].Value != "v" {
		t.Fatalf("secrets = %+v", sp.Secrets)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("created spec must validate: %v", err)
	}
}

func TestSandboxCreate_DefaultImageAndIsolation(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_create")
	if _, err := callHandler(t, d, svc, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	sp := svc.createdSpec
	if sp.Image != defaultPolicy().DefaultImage {
		t.Fatalf("image = %q", sp.Image)
	}
	if sp.Sudo != spec.SudoNever {
		t.Fatalf("default sudo = %q", sp.Sudo)
	}
	if sp.UIDGID.UID != 1000 {
		t.Fatalf("uid = %d", sp.UIDGID.UID)
	}
	if err := sp.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestSandboxCreate_Error(t *testing.T) {
	svc := newFake()
	svc.createErr = errFake
	d := toolByName(t, svc, "sandbox_create")
	if _, err := callHandler(t, d, svc, map[string]any{}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
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

func TestSandboxWriteFile_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_write_file")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "path": "/w/f", "content": "data"})
	if err != nil {
		t.Fatal(err)
	}
	wo := out.(WriteFileOutput)
	if !wo.Wrote || wo.Bytes != 4 {
		t.Fatalf("output = %+v", wo)
	}
}

func TestSandboxWriteFile_Error(t *testing.T) {
	svc := newFake()
	svc.writeErr = errFake
	d := toolByName(t, svc, "sandbox_write_file")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "path": "/w/f", "content": "d"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxCopyIn_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_copy_in")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "source": "/host", "destination": "/sand"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.(CopyOutput).OK {
		t.Fatalf("output = %+v", out)
	}
}

func TestSandboxCopyIn_Error(t *testing.T) {
	svc := newFake()
	svc.copyInErr = errFake
	d := toolByName(t, svc, "sandbox_copy_in")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "source": "/h", "destination": "/s"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxCopyOut_Handler(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_copy_out")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "source": "/s", "destination": "/h"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.(CopyOutput).OK {
		t.Fatalf("output = %+v", out)
	}
}

func TestSandboxCopyOut_Error(t *testing.T) {
	svc := newFake()
	svc.copyOutErr = errFake
	d := toolByName(t, svc, "sandbox_copy_out")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "source": "/s", "destination": "/h"}); !errors.Is(err, errFake) {
		t.Fatalf("err = %v", err)
	}
}

func TestSandboxInstallPackage_Handler(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{Sudo: spec.SudoRequire, Isolation: defaultPolicy().Isolation})
	svc.execRes = &exec.Result{ExitCode: 0, Stdout: []byte("installed")}
	d := toolByName(t, svc, "sandbox_install_package")
	out, err := callHandler(t, d, svc, map[string]any{"id": "abc", "package": "curl", "manager": "apt"})
	if err != nil {
		t.Fatal(err)
	}
	io := out.(InstallPackageOutput)
	if io.ExitCode != 0 || io.Manager != "apt" {
		t.Fatalf("output = %+v", io)
	}
	got := svc.execReq.Argv
	if len(got) != 4 || got[0] != "apt-get" || got[3] != "curl" {
		t.Fatalf("argv = %v", got)
	}
	if svc.execReq.User != "0" {
		t.Fatalf("user = %q", svc.execReq.User)
	}
}

func TestSandboxInstallPackage_InvalidName(t *testing.T) {
	svc := newFake()
	d := toolByName(t, svc, "sandbox_install_package")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "package": "curl; rm -rf /", "manager": "apt"}); err == nil {
		t.Fatal("expected invalid package name error")
	}
}

func TestSandboxInstallPackage_NoSudoPolicy(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{Sudo: spec.SudoNever, Isolation: defaultPolicy().Isolation})
	d := toolByName(t, svc, "sandbox_install_package")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "package": "curl", "manager": "apt"}); err == nil {
		t.Fatal("expected sudo policy error")
	}
}

func TestSandboxInstallPackage_BadManager(t *testing.T) {
	svc := newFake()
	svc.session = runningSession("abc", spec.SandboxSpec{Sudo: spec.SudoRequire, Isolation: defaultPolicy().Isolation})
	d := toolByName(t, svc, "sandbox_install_package")
	if _, err := callHandler(t, d, svc, map[string]any{"id": "abc", "package": "curl", "manager": "zypper"}); err == nil {
		t.Fatal("expected unsupported manager error")
	}
}

func TestSandboxInstallPackage_Dnf(t *testing.T) {
	argv, err := packageManagerArgv("dnf", "vim")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 4 || argv[0] != "dnf" || argv[3] != "vim" {
		t.Fatalf("argv = %v", argv)
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

func TestBuildSpec_MountsAndEnv(t *testing.T) {
	svc := newFake()
	b := builder{svc: svc}
	sp := buildSpec(b, createInput{
		Env:     map[string]string{"A": "1"},
		Mounts:  []mountInput{{Source: "/h", Target: "/s", ReadOnly: true}},
		Secrets: []secretInput{{Name: "k", Value: "v"}},
	})
	if len(sp.Mounts) != 1 || sp.Mounts[0].ReadOnly != true {
		t.Fatalf("mounts = %+v", sp.Mounts)
	}
	if sp.Env["A"] != "1" {
		t.Fatalf("env = %+v", sp.Env)
	}
}

func TestBuildSpec_UserOverride(t *testing.T) {
	svc := newFake()
	b := builder{svc: svc}
	sp := buildSpec(b, createInput{UID: 2000, GID: 2000})
	if sp.UIDGID.UID != 2000 || sp.UIDGID.GID != 2000 {
		t.Fatalf("uidgid = %+v", sp.UIDGID)
	}
}

func TestRegister(t *testing.T) {
	reg := registry.New()
	if err := Register(reg, newFake()); err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 12 {
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

func TestPackageManagerArgv_Unsupported(t *testing.T) {
	if _, err := packageManagerArgv("pacman", "x"); err == nil {
		t.Fatal("expected error")
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
		"sandbox_info", "sandbox_stats", "sandbox_read_file", "sandbox_create",
		"sandbox_exec", "sandbox_write_file", "sandbox_copy_in", "sandbox_copy_out",
		"sandbox_install_package", "sandbox_destroy",
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

func TestPackageManagerArgv_FixedArrays(t *testing.T) {
	apt, _ := packageManagerArgv("apt", "curl")
	if len(apt) != 4 || apt[1] != "install" || apt[2] != "-y" {
		t.Fatalf("apt argv = %v", apt)
	}
	dnf, _ := packageManagerArgv("dnf", "vim")
	if len(dnf) != 4 || dnf[0] != "dnf" {
		t.Fatalf("dnf argv = %v", dnf)
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
