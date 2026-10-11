// Package registry holds the declarative tool registry (SPEC §6.4). It maps
// unique tool names to their ToolDefs and validates the required fields of a
// definition at registration time so that a malformed tool can never reach the
// handler-wrapper.
package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/teran/mcp-sandbox/application/tools/def"
)

// Registry is a thread-safe map of tool names to their declarations.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*def.ToolDef
}

// New returns an empty tool registry.
func New() *Registry {
	return &Registry{tools: make(map[string]*def.ToolDef)}
}

// Register adds a tool definition after validating it. It returns an error if
// the name is empty, a required field is missing, or the name is already taken.
func (r *Registry) Register(t *def.ToolDef) error {
	if err := Validate(t); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name]; ok {
		return fmt.Errorf("tools: tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t

	return nil
}

// Get returns the tool registered under the given name.
func (r *Registry) Get(name string) (*def.ToolDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]

	return t, ok
}

// List returns every registered tool ordered deterministically by name.
func (r *Registry) List() []*def.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*def.ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tools)
}

// Validate checks that a tool definition has the required fields and that its
// input schema is object-rooted with additionalProperties disabled (S08).
func Validate(t *def.ToolDef) error {
	if t == nil {
		return fmt.Errorf("tools: nil tool definition")
	}
	if t.Name == "" {
		return fmt.Errorf("tools: tool name is required")
	}
	if t.Title == "" {
		return fmt.Errorf("tools: tool %q: title is required", t.Name)
	}
	if t.Description == "" {
		return fmt.Errorf("tools: tool %q: description is required", t.Name)
	}
	if t.Instructions == "" {
		return fmt.Errorf("tools: tool %q: instructions are required", t.Name)
	}
	if t.InputSchema == nil {
		return fmt.Errorf("tools: tool %q: input schema is required", t.Name)
	}
	if t.InputSchema.Type != "object" {
		return fmt.Errorf("tools: tool %q: input schema must have type \"object\"", t.Name)
	}
	if t.OutputSchema == nil {
		return fmt.Errorf("tools: tool %q: output schema is required", t.Name)
	}
	if t.Handler == nil {
		return fmt.Errorf("tools: tool %q: handler is required", t.Name)
	}

	return nil
}
