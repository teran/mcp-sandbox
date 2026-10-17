// Package profile_test covers the read-only profile_list tool (Phase 7): its
// annotations, its redaction-safe output shape ({profiles:[{name,description}]})
// and that it is a valid ToolDef through the shared registry validation.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/registry"
	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// fakeLister returns a fixed set of profiles (or an injected error) for the
// tool to enumerate. It matches the new Lister contract: List returns an error
// that the handler must propagate (fail-closed).
type fakeLister struct {
	profiles []*domainprofile.Profile
	err      error
}

func (f fakeLister) List() ([]*domainprofile.Profile, error) {
	if f.err != nil {
		return nil, f.err
	}

	return f.profiles, nil
}

// profileListDockerOpts mirrors a docker options blob using only locally-tagged
// structs (so yaml.Marshal's musttag check is satisfied — the domain spec.UIDGID
// has no yaml tags and cannot be marshaled directly).
type profileListDockerOpts struct {
	Image  string                   `yaml:"image"`
	Env    map[string]string        `yaml:"env"`
	Mounts []profileListDockerMount `yaml:"mounts"`
	User   *profileListDockerUser   `yaml:"user"`
}

type profileListDockerMount struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}

type profileListDockerUser struct {
	UID uint32 `yaml:"uid"`
	GID uint32 `yaml:"gid"`
}

// dockerProfile builds a driver+options profile whose opaque Options carry the
// same sensitive values the pre-migration Docker block did (image, a SECRET env
// value, a host mount source, a non-root user). The no-leakage test asserts the
// tool never emits any of them.
func dockerProfile(name, desc, image, secretVal, mountSrc string) *domainprofile.Profile {
	opts := profileListDockerOpts{
		Image:  image,
		Env:    map[string]string{"SECRET": secretVal},
		Mounts: []profileListDockerMount{{Source: mountSrc, Target: "/mnt", ReadOnly: true}},
		User:   &profileListDockerUser{UID: 1000, GID: 1000},
	}
	b, _ := yaml.Marshal(opts)
	var n yaml.Node
	_ = yaml.Unmarshal(b, &n)

	return &domainprofile.Profile{
		Name:        name,
		Description: desc,
		Driver:      spec.DriverDocker,
		Options:     driver.NewOptions(&n),
	}
}

func testProfiles() []*domainprofile.Profile {
	return []*domainprofile.Profile{
		dockerProfile("default", "the default profile",
			"docker.io/library/alpine:3.20", "envvalue", "/host/secret-dir"),
		dockerProfile("gpu", "gpu profile", "ghcr.io/org/gpu:v1", "envvalue2", "/host/gpu"),
	}
}

func toolDef(t *testing.T, l Lister) *def.ToolDef {
	t.Helper()
	for _, d := range All(l) {
		if d.Name == "profile_list" {
			return d
		}
	}
	t.Fatal("profile_list tool not found")

	return nil
}

func TestProfileList_Annotations(t *testing.T) {
	d := toolDef(t, fakeLister{})
	if !d.Annotations.ReadOnlyHint {
		t.Fatal("profile_list must be read-only")
	}
	if d.Annotations.DestructiveHint {
		t.Fatal("profile_list must not be destructive")
	}
	if !d.Annotations.IdempotentHint {
		t.Fatal("profile_list must be idempotent")
	}
	if d.Annotations.OpenWorldHint {
		t.Fatal("OpenWorldHint must be false")
	}
}

func TestProfileList_OutputShape(t *testing.T) {
	d := toolDef(t, fakeLister{profiles: testProfiles()})
	out, err := d.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	lo, ok := out.(ListOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if len(lo.Profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(lo.Profiles))
	}
	if lo.Profiles[0].Name != "default" || lo.Profiles[0].Description != "the default profile" {
		t.Fatalf("profiles[0] = %+v", lo.Profiles[0])
	}
	if lo.Profiles[1].Name != "gpu" {
		t.Fatalf("profiles[1] = %+v", lo.Profiles[1])
	}
}

// TestProfileList_NoLeakage asserts the output carries only name+description:
// no env values, no mount sources, no user identity — nothing an operator would
// consider sensitive — is emitted (S02).
func TestProfileList_NoLeakage(t *testing.T) {
	d := toolDef(t, fakeLister{profiles: testProfiles()})
	out, err := d.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"envvalue", "/host/secret-dir", "SECRET", "1000", "docker.io/library/alpine"} {
		if jsonHas(string(raw), forbidden) {
			t.Fatalf("output leaked forbidden value %q: %s", forbidden, raw)
		}
	}
	// The output shape is exactly profiles:[{name,description}].
	var parsed struct {
		Profiles []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Profiles) != 2 {
		t.Fatalf("profiles = %d", len(parsed.Profiles))
	}
}

func jsonHas(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}

	return false
}

// TestList_Error_Propagates pins fail-closed enumeration: a Lister error must
// be propagated out of the profile_list handler rather than swallowed (the tool
// must never emit a partial/empty list when the store is broken).
func TestList_Error_Propagates(t *testing.T) {
	errSentinel := errors.New("boom")
	d := toolDef(t, fakeLister{err: errSentinel})
	if _, err := d.Handler(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, errSentinel) {
		t.Fatalf("err = %v, want lister error to propagate", err)
	}
}

// TestProfileList_RegistryValid asserts profile_list is a valid ToolDef under
// the shared registry validation (object-rooted strict schema, required fields).
func TestProfileList_RegistryValid(t *testing.T) {
	d := toolDef(t, fakeLister{})
	if err := registry.Validate(d); err != nil {
		t.Fatalf("profile_list fails registry.Validate: %v", err)
	}
	if d.InputSchema == nil || d.InputSchema.Type != "object" {
		t.Fatal("input schema must be object-rooted")
	}
	if d.OutputSchema == nil || d.OutputSchema.Type != "object" {
		t.Fatal("output schema must be object-rooted")
	}
}

// TestProfileList_AllCount pins that the profile tool set is exactly one tool.
func TestProfileList_AllCount(t *testing.T) {
	if defs := All(fakeLister{}); len(defs) != 1 {
		t.Fatalf("All() = %d tools, want 1", len(defs))
	}
}
