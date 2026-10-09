// Package profile declares the read-only profile_list tool (Phase 7): it
// enumerates the operator-authored profiles by name and description only, so no
// env value, mount source, user identity or image leaks into output or logs
// (S02). It is declared as a ToolDef and invoked through the shared
// handler-wrapper.
package profile

import (
	"context"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/teran/mcp-sandbox/application/tools/def"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
)

// Lister is the read-only profile enumeration the tool depends on.
type Lister interface {
	List() []*domainprofile.Profile
}

// Summary is one entry of profile_list output: name + description only
// (S02). No env/mount/image/user/sudo fields are ever emitted.
type Summary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ListOutput is the structured output of profile_list.
type ListOutput struct {
	Profiles []Summary `json:"profiles"`
}

// All returns every profile tool definition. The profile tool set is exactly
// one read-only, idempotent tool.
func All(l Lister) []*def.ToolDef {
	return []*def.ToolDef{profileListDef(l)}
}

// objectSchema builds an object-rooted schema with additionalProperties
// disabled (S08). It is used for the tool's INPUT (which takes no arguments).
func objectSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
	}
}

// outputSchema builds a permissive object-rooted OUTPUT schema (S09): the
// server generates the output, so additional properties are allowed (matches
// the sandbox package's objectOutput()).
func outputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object"}
}

// profileListDef declares profile_list: enumerate operator-authored profiles.
func profileListDef(l Lister) *def.ToolDef {
	return &def.ToolDef{
		Name:         "profile_list",
		Title:        "List sandbox profiles",
		Description:  "List the operator-authored sandbox profiles by name and description. Profiles are read-only and fully determine image, env, mounts, network, resources, user and sudo; sandbox_create references one by name.",
		Instructions: "Returns the available profiles and their descriptions. Pick a profile name to pass to sandbox_create. Only names and descriptions are returned; no sensitive profile content is exposed.",
		Annotations: def.Annotations{
			ReadOnlyHint:    true,
			DestructiveHint: false,
			IdempotentHint:  true,
			OpenWorldHint:   false,
		},
		InputSchema:  objectSchema(),
		OutputSchema: outputSchema(),
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			ps := l.List()
			out := ListOutput{Profiles: make([]Summary, 0, len(ps))}
			for _, p := range ps {
				out.Profiles = append(out.Profiles, Summary{Name: p.Name, Description: p.Description})
			}
			return out, nil
		},
	}
}
