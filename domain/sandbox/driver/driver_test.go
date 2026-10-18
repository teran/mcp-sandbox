// Package driver_test pins the driver-registry + opaque options design
// (SPEC §6.11 / §7.11): an opaque `Options` wrapper around a yaml.Node, a
// `Builder` seam (with the `BuildFunc` adapter), and a `Registry` that maps a
// `spec.Driver` to its builder. The registry is fail-closed (unknown/duplicate
// drivers), and the reserved built-in drivers (`docker`, `seatbelt`) cannot be
// shadowed.
//
// These tests exercise only the public API so they hold regardless of how
// @developer seeds the package `Default` registry from the built-in builders.
package driver_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// buildOK is a BuildFunc that returns a fixed, valid docker spec. It is the
// concrete Builder used to exercise the Registry (the driver name is opaque to
// the registry — the builder does not need to match the driver it is filed
// under).
func buildOK(_ driver.Context) (spec.SandboxSpec, error) {
	return spec.SandboxSpec{
		Driver: spec.DriverDocker,
		Image:  "docker.io/library/alpine:3.20",
		Isolation: spec.Isolation{
			ReadOnlyRootfs:  true,
			CapDrop:         []string{"ALL"},
			NoNewPrivileges: true,
			User:            spec.UIDGID{UID: 1000, GID: 1000},
			PidsLimit:       256,
			Memory:          512 << 20,
			NanoCPUs:        1e9,
			NetworkMode:     spec.NetworkBridge,
			TmpfsWorkdir:    true,
			AutoRemove:      true,
		},
	}, nil
}

// buildErr returns the sentinel errForBuild to pin that a Builder's error is
// propagated verbatim through the registry (the registry never swallows it).
var errForBuild = errors.New("driver: build failed")

func buildErr(_ driver.Context) (spec.SandboxSpec, error) {
	return spec.SandboxSpec{}, errForBuild
}

func newRegistry() *driver.Registry {
	return driver.New()
}

// --- Registry: happy path ---

func TestRegistry_RegisterGetHappyPath(t *testing.T) {
	r := newRegistry()
	if err := r.Register(spec.Driver("foo"), driver.BuildFunc(buildOK)); err != nil {
		t.Fatalf("Register(foo): %v", err)
	}
	b, err := r.Get(spec.Driver("foo"))
	if err != nil {
		t.Fatalf("Get(foo): %v", err)
	}
	sp, err := b.Build(driver.Context{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if sp.Driver != spec.DriverDocker || sp.Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("spec = %+v, want the builder's output", sp)
	}
}

// TestRegistry_GetReturnsBuilderErrors pins that a Builder's error is propagated
// verbatim through Get().Build — the registry must not swallow it.
func TestRegistry_GetReturnsBuilderErrors(t *testing.T) {
	r := newRegistry()
	if err := r.Register(spec.Driver("foo"), driver.BuildFunc(buildErr)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	b, err := r.Get(spec.Driver("foo"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := b.Build(driver.Context{}); !errors.Is(err, errForBuild) {
		t.Fatalf("Build err = %v, want errForBuild propagated", err)
	}
}

// --- Registry: unknown driver (fail-closed, SPEC §6.3) ---

func TestRegistry_GetUnknown_ErrUnknownDriver(t *testing.T) {
	r := newRegistry()
	if _, err := r.Get(spec.Driver("nope")); !errors.Is(err, driver.ErrUnknownDriver) {
		t.Fatalf("Get(nope) err = %v, want ErrUnknownDriver", err)
	}
	if _, err := r.Get(spec.Driver("")); !errors.Is(err, driver.ErrUnknownDriver) {
		t.Fatalf("Get(\"\") err = %v, want ErrUnknownDriver", err)
	}
}

// --- Registry: duplicate / empty registration (fail-closed) ---

func TestRegistry_RegisterDuplicate_ErrDuplicateDriver(t *testing.T) {
	r := newRegistry()
	if err := r.Register(spec.Driver("foo"), driver.BuildFunc(buildOK)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(spec.Driver("foo"), driver.BuildFunc(buildOK)); !errors.Is(err, driver.ErrDuplicateDriver) {
		t.Fatalf("second Register(foo) err = %v, want ErrDuplicateDriver", err)
	}
}

func TestRegistry_RegisterEmpty_ErrDuplicateDriver(t *testing.T) {
	r := newRegistry()
	if err := r.Register(spec.Driver(""), driver.BuildFunc(buildOK)); !errors.Is(err, driver.ErrDuplicateDriver) {
		t.Fatalf("Register(\"\") err = %v, want ErrDuplicateDriver", err)
	}
}

// TestRegistry_ReservedCannotBeShadowed pins the invariant that the reserved
// built-in drivers (`docker`, `seatbelt`) can never be shadowed: a second
// Register under either name is rejected with ErrDuplicateDriver even after the
// first registration succeeded (so a hostile or accidental re-registration can
// never replace the built-in builder).
func TestRegistry_ReservedCannotBeShadowed(t *testing.T) {
	for _, d := range []spec.Driver{spec.DriverDocker, spec.DriverSeatbelt} {
		r := newRegistry()
		// First registration of the reserved name is allowed (a fresh registry
		// has no builder yet); the invariant is that it cannot be SHADOWED.
		if err := r.Register(d, driver.BuildFunc(buildOK)); err != nil {
			t.Fatalf("Register(%q) first time: %v", d, err)
		}
		if err := r.Register(d, driver.BuildFunc(buildErr)); !errors.Is(err, driver.ErrDuplicateDriver) {
			t.Fatalf("Register(%q) second time err = %v, want ErrDuplicateDriver (no shadowing)", d, err)
		}
		// The original builder is still the one returned.
		b, err := r.Get(d)
		if err != nil {
			t.Fatalf("Get(%q): %v", d, err)
		}
		if _, err := b.Build(driver.Context{}); err != nil {
			t.Fatalf("shadowed builder leaked in: %v", err)
		}
	}
}

// TestDefault_RegisterGet pins the package-level Default registry + Register
// package func: Register files a builder into Default and Get resolves it, and
// Get on an unregistered driver returns ErrUnknownDriver. This does not depend
// on whether the built-in docker/seatbelt builders have been seeded by their
// infra init() (which varies by test binary) — it exercises the public package
// API on a fresh driver name.
func TestDefault_RegisterGet(t *testing.T) {
	if err := driver.Register(spec.Driver("pkgdrv"), driver.BuildFunc(buildOK)); err != nil {
		t.Fatalf("driver.Register(pkgdrv): %v", err)
	}
	b, err := driver.Default.Get(spec.Driver("pkgdrv"))
	if err != nil {
		t.Fatalf("Default.Get(pkgdrv): %v", err)
	}
	if _, err := b.Build(driver.Context{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := driver.Default.Get(spec.Driver("unregistered")); !errors.Is(err, driver.ErrUnknownDriver) {
		t.Fatalf("Default.Get(unregistered) err = %v, want ErrUnknownDriver", err)
	}
}

// --- Registry: List ---

func TestRegistry_List_ReturnsRegisteredDrivers(t *testing.T) {
	r := newRegistry()
	for _, d := range []spec.Driver{"foo", "bar", "baz"} {
		if err := r.Register(d, driver.BuildFunc(buildOK)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List() = %v, want 3 drivers", got)
	}
	for _, d := range []spec.Driver{"foo", "bar", "baz"} {
		if !slices.Contains(got, d) {
			t.Errorf("List() = %v missing %q", got, d)
		}
	}
}

func TestRegistry_List_Empty(t *testing.T) {
	r := newRegistry()
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List() on empty registry = %v, want empty", got)
	}
}

// TestRegistry_List_Unordered_NoDuplicates pins that List never returns a
// driver more than once (each driver is a key in the registry map).
func TestRegistry_List_NoDuplicates(t *testing.T) {
	r := newRegistry()
	for _, d := range []spec.Driver{"a", "b"} {
		if err := r.Register(d, driver.BuildFunc(buildOK)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.List()
	seen := map[spec.Driver]int{}
	for _, d := range got {
		seen[d]++
	}
	for d, n := range seen {
		if n != 1 {
			t.Errorf("driver %q listed %d times, want once", d, n)
		}
	}
}

// --- BuildFunc adapter ---

func TestBuildFunc_Adapter(t *testing.T) {
	called := false
	var gotCtx driver.Context
	f := driver.BuildFunc(func(ctx driver.Context) (spec.SandboxSpec, error) {
		called = true
		gotCtx = ctx

		return spec.SandboxSpec{}, errForBuild
	})

	ctx := driver.Context{Name: "n"}
	_, err := f.Build(ctx)
	if !called {
		t.Fatal("BuildFunc did not invoke the wrapped function")
	}
	if !errors.Is(err, errForBuild) {
		t.Fatalf("err = %v, want errForBuild", err)
	}
	if gotCtx.Name != "n" {
		t.Fatalf("ctx passed = %+v, want the Build argument", gotCtx)
	}
}

// TestBuildFunc_IsABuilder pins the adapter satisfies the Builder interface.
func TestBuildFunc_IsABuilder(_ *testing.T) {
	var _ driver.Builder = driver.BuildFunc(buildOK)
}

// --- apiVersion envelope (ParseAPIVersion) ---

func TestParseAPIVersion_Valid(t *testing.T) {
	tests := []struct {
		in      string
		driver  spec.Driver
		version string
	}{
		{"docker.profiles.sandbox.teran.dev/v1", spec.DriverDocker, "v1"},
		{"seatbelt.profiles.sandbox.teran.dev/v1", spec.DriverSeatbelt, "v1"},
		{"my-driver.1.profiles.sandbox.teran.dev/v2", spec.Driver("my-driver.1"), "v2"},
		{"foo_bar.profiles.sandbox.teran.dev/v123", spec.Driver("foo_bar"), "v123"},
		// whitespace is tolerated
		{"  docker.profiles.sandbox.teran.dev/v1  ", spec.DriverDocker, "v1"},
	}
	for _, tt := range tests {
		d, v, err := driver.ParseAPIVersion(tt.in)
		if err != nil {
			t.Errorf("ParseAPIVersion(%q): %v", tt.in, err)

			continue
		}
		if d != tt.driver || v != tt.version {
			t.Errorf("ParseAPIVersion(%q) = (%q, %q), want (%q, %q)", tt.in, d, v, tt.driver, tt.version)
		}
	}
}

func TestParseAPIVersion_Invalid(t *testing.T) {
	for _, in := range []string{
		"", "   ", "docker", "v1", "docker/v1",
		"docker.example.com/v1",                  // wrong group
		"docker.profiles.sandbox.teran.dev",      // missing version
		"docker.profiles.sandbox.teran.dev/",     // empty version
		"docker.profiles.sandbox.teran.dev/v",    // version not numeric
		".profiles.sandbox.teran.dev/v1",         // "." driver label
		"..profiles.sandbox.teran.dev/v1",        // ".." driver label
		"UPPER.profiles.sandbox.teran.dev/v1",    // uppercase label
		"docker.profiles.sandbox.teran.dev/v01x", // trailing char
	} {
		if _, _, err := driver.ParseAPIVersion(in); err == nil {
			t.Errorf("ParseAPIVersion(%q): expected ErrInvalidAPIVersion", in)
		} else if !errors.Is(err, driver.ErrInvalidAPIVersion) {
			t.Errorf("ParseAPIVersion(%q) err = %v, want ErrInvalidAPIVersion", in, err)
		}
	}
}

// --- SplitCommon ---

func TestSplitCommon_RemovesCwdAndShell(t *testing.T) {
	doc := "image: golang:1.27.2\nenv:\n  A: '1'\ncwd:\n  mode: rw\nshell: bash\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &n); err != nil {
		t.Fatal(err)
	}
	root := rootNode(&n)
	// The input node is a mapping (after unwrapping the doc) with 4 keys.
	if root.Kind != yaml.MappingNode || len(root.Content) != 8 {
		t.Fatalf("fixture root = kind %d content %d, want mapping with 4 keys", root.Kind, len(root.Content))
	}

	out := driver.SplitCommon(root)
	if out == root {
		t.Fatal("SplitCommon must return a NEW node, not the input")
	}
	if out.Kind != yaml.MappingNode {
		t.Fatalf("out kind = %d, want mapping", out.Kind)
	}
	// The output has exactly image/env (cwd/shell stripped).
	if len(out.Content) != 4 {
		t.Fatalf("out content = %d, want 4 (image+env)", len(out.Content))
	}
	keys := map[string]bool{}
	for i := 0; i+1 < len(out.Content); i += 2 {
		keys[out.Content[i].Value] = true
	}
	if keys["image"] != true || keys["env"] != true {
		t.Fatalf("out keys = %v, want image+env present", keys)
	}
	if keys["cwd"] || keys["shell"] {
		t.Fatalf("out keys = %v, must NOT contain cwd/shell", keys)
	}
	// The input node is NOT mutated (still has all 4 keys).
	if len(root.Content) != 8 {
		t.Fatalf("input mutated: content = %d, want 8", len(root.Content))
	}
	// The remainder still decodes as driver options (image/env survive).
	var o struct {
		Image string            `yaml:"image"`
		Env   map[string]string `yaml:"env"`
	}
	if err := out.Decode(&o); err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if o.Image != "golang:1.27.2" || o.Env["A"] != "1" {
		t.Fatalf("decoded = %+v", o)
	}
}

func TestSplitCommon_NilAndScalarAsIs(t *testing.T) {
	if got := driver.SplitCommon(nil); got != nil {
		t.Fatalf("SplitCommon(nil) = %v, want nil", got)
	}
	var scalar yaml.Node
	if err := yaml.Unmarshal([]byte("just-a-scalar"), &scalar); err != nil {
		t.Fatal(err)
	}
	if got := driver.SplitCommon(&scalar); got != &scalar {
		t.Fatal("SplitCommon(scalar) must return the input unchanged")
	}
}

func TestSplitCommon_NoCommonKeysPreservesAll(t *testing.T) {
	doc := "image: golang:1.27.2\nnetwork:\n  mode: bridge\n"
	var n yaml.Node
	_ = yaml.Unmarshal([]byte(doc), &n)
	root := rootNode(&n)
	out := driver.SplitCommon(root)
	if out.Kind != yaml.MappingNode || len(out.Content) != 4 {
		t.Fatalf("out content = %d, want 4 (image+network preserved)", len(out.Content))
	}
}

// --- BuildFuncWithVersion / VersionedBuildFunc ---

// TestBuildFuncWithVersion_ImplementsAPIVersioner pins that
// BuildFuncWithVersion returns a Builder that satisfies both Builder and
// APIVersioner, delegating Build to the wrapped function.
func TestBuildFuncWithVersion_ImplementsAPIVersioner(t *testing.T) {
	const version = "docker.profiles.sandbox.teran.dev/v1"
	called := false
	b := driver.BuildFuncWithVersion(func(_ driver.Context) (spec.SandboxSpec, error) {
		called = true

		return spec.SandboxSpec{}, errForBuild
	}, version)

	_ = b
	av, ok := b.(driver.APIVersioner)
	if !ok {
		t.Fatal("BuildFuncWithVersion result must implement APIVersioner")
	}
	if av.APIVersion() != version {
		t.Fatalf("APIVersion() = %q, want %q", av.APIVersion(), version)
	}
	if _, err := b.Build(driver.Context{}); !errors.Is(err, errForBuild) {
		t.Fatalf("Build err = %v, want errForBuild", err)
	}
	if !called {
		t.Fatal("Build did not invoke the wrapped function")
	}
}

// --- Reconcile ---

func TestReconcile_Matched(t *testing.T) {
	const version = "docker.profiles.sandbox.teran.dev/v1"
	b := driver.BuildFuncWithVersion(buildOK, version)
	if err := driver.Reconcile(version, b); err != nil {
		t.Fatalf("Reconcile(matched) = %v, want nil", err)
	}
}

func TestReconcile_Mismatch(t *testing.T) {
	b := driver.BuildFuncWithVersion(buildOK, "docker.profiles.sandbox.teran.dev/v1")
	err := driver.Reconcile("docker.profiles.sandbox.teran.dev/v2", b)
	if !errors.Is(err, driver.ErrAPIVersionMismatch) {
		t.Fatalf("Reconcile(mismatch) err = %v, want ErrAPIVersionMismatch", err)
	}
}

func TestReconcile_LegacyEmptyDeclared(t *testing.T) {
	// A legacy profile declares no apiVersion; reconciliation is a no-op
	// regardless of whether the builder declares one.
	b := driver.BuildFuncWithVersion(buildOK, "docker.profiles.sandbox.teran.dev/v1")
	if err := driver.Reconcile("", b); err != nil {
		t.Fatalf("Reconcile(empty) = %v, want nil", err)
	}
	if err := driver.Reconcile("   ", driver.BuildFunc(buildOK)); err != nil {
		t.Fatalf("Reconcile(blank, non-declaring) = %v, want nil", err)
	}
}

func TestReconcile_NonDeclaringBuilder(t *testing.T) {
	// A builder that does NOT implement APIVersioner fails closed when the
	// profile declares an apiVersion.
	err := driver.Reconcile("docker.profiles.sandbox.teran.dev/v1", driver.BuildFunc(buildOK))
	if !errors.Is(err, driver.ErrAPIVersionMismatch) {
		t.Fatalf("Reconcile(non-declaring) err = %v, want ErrAPIVersionMismatch", err)
	}
}

// rootNode unwraps a DocumentNode (from yaml.Unmarshal of a whole doc) to its
// root mapping node, so tests can feed SplitCommon a bare mapping node.
func rootNode(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}

	return n
}

// --- Options (opaque yaml.Node) ---

// TestOptions_NewOptions_NodeAccess pins that NewOptions wraps the given node
// and exposes it via Node() so builders (and the store test) can decode it. A
// nil node is stored as nil (no panic).
func TestOptions_NewOptions_NodeAccess(t *testing.T) {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("image: alpine"), &n); err != nil {
		t.Fatal(err)
	}
	o := driver.NewOptions(&n)
	if o.Node() != &n {
		t.Fatalf("Node() = %p, want the wrapped node %p", o.Node(), &n)
	}
	if got := driver.NewOptions(nil); got.Node() != nil {
		t.Fatalf("NewOptions(nil).Node() = %p, want nil", got.Node())
	}
}

// TestOptions_NodeDecodes pins that the wrapped node decodes into a struct with
// yaml tags — the exact mechanism the built-in builders and the store test rely
// on to read the opaque options.
func TestOptions_NodeDecodes(t *testing.T) {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("image: alpine\nnetwork:\n  mode: bridge"), &n); err != nil {
		t.Fatal(err)
	}
	o := driver.NewOptions(&n)
	var decoded struct {
		Image   string `yaml:"image"`
		Network struct {
			Mode string `yaml:"mode"`
		} `yaml:"network"`
	}
	if err := o.Node().Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Image != "alpine" || decoded.Network.Mode != "bridge" {
		t.Fatalf("decoded = %+v, want image=alpine network.mode=bridge", decoded)
	}
}

// TestContext_CarriesProfileFields pins the Context shape the builders consume:
// it carries the driver-agnostic profile fields (Name, Description, Cwd, Shell)
// plus the immutable Policy, the opaque Options and the per-request Params.
func TestContext_CarriesProfileFields(t *testing.T) {
	ctx := driver.Context{
		Name:        "default",
		Description: "the default profile",
		Cwd:         domainprofile.CwdConfig{Target: "/work", Mode: "ro"},
		Shell:       "bash",
		Options:     driver.NewOptions(nil),
		Policy: spec.Isolation{
			User:    spec.UIDGID{UID: 1000, GID: 1000},
			CapDrop: []string{"ALL"},
		},
		Params: domainprofile.Params{Cwd: "/home/user/proj"},
	}
	// Assert the fields round-trip so a builder receiving this Context sees them.
	if ctx.Name != "default" || ctx.Description != "the default profile" {
		t.Fatalf("name/description = %q/%q", ctx.Name, ctx.Description)
	}
	if ctx.Cwd.Target != "/work" || ctx.Cwd.Mode != "ro" {
		t.Fatalf("cwd = %+v", ctx.Cwd)
	}
	if ctx.Shell != "bash" {
		t.Fatalf("shell = %q", ctx.Shell)
	}
	if ctx.Policy.User.UID != 1000 {
		t.Fatalf("policy = %+v", ctx.Policy)
	}
	if ctx.Params.Cwd != "/home/user/proj" {
		t.Fatalf("params = %+v", ctx.Params)
	}
	// reflect confirms the exported field set exactly matches the contract.
	wantFields := []string{"Name", "Description", "Cwd", "Shell", "Options", "Policy", "Params"}
	var have []string
	for f := range reflect.TypeFor[driver.Context]().Fields() {
		have = append(have, f.Name)
	}
	if !reflect.DeepEqual(have, wantFields) {
		t.Fatalf("Context fields = %v, want %v", have, wantFields)
	}
}
