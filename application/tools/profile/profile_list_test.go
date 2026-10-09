// Package profile_test covers the read-only profile_list tool (Phase 7): its
// annotations, its redaction-safe output shape ({profiles:[{name,description}]})
// and that it is a valid ToolDef through the shared registry validation.
package profile

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/teran/mcp-sandbox/application/tools/def"
	"github.com/teran/mcp-sandbox/application/tools/registry"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// fakeLister returns a fixed set of profiles for the tool to enumerate.
type fakeLister struct {
	profiles []*domainprofile.Profile
}

func (f fakeLister) List() []*domainprofile.Profile { return f.profiles }

func testProfiles() []*domainprofile.Profile {
	return []*domainprofile.Profile{
		{
			Name:        "default",
			Description: "the default profile",
			Image:       "docker.io/library/alpine:3.20",
			Env:         map[string]string{"SECRET": "envvalue"},
			Mounts: []domainprofile.Mount{
				{Source: "/host/secret-dir", Target: "/mnt", ReadOnly: true},
			},
			User: &spec.UIDGID{UID: 1000, GID: 1000},
		},
		{
			Name:        "gpu",
			Description: "gpu profile",
			Image:       "ghcr.io/org/gpu:v1",
		},
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
