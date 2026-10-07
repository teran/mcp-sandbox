package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
)

func newFactory(name string, priority int, caps role.CapabilitySet, avail func() bool) Factory {
	return Factory{
		Name:         name,
		Priority:     priority,
		Capabilities: caps,
		Available:    avail,
		New:          func(Config) (role.Backend, error) { return nil, nil },
	}
}

func allCaps() role.CapabilitySet {
	return role.CapabilitySet{Lifecycle: true, Executable: true, Filesystem: true, Introspectable: true}
}

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	f := newFactory("docker", 0, allCaps(), func() bool { return true })
	if err := r.Register(f); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("docker")
	if !ok || got.Name != "docker" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get should report missing")
	}
}

func TestRegister_Duplicate(t *testing.T) {
	r := NewRegistry()
	f := newFactory("docker", 0, allCaps(), func() bool { return true })
	if err := r.Register(f); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(f); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegister_EmptyName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(newFactory("", 0, allCaps(), func() bool { return true })); err == nil {
		t.Fatal("expected empty-name error")
	}
}

func TestList(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactory("a", 0, allCaps(), func() bool { return true }))
	_ = r.Register(newFactory("b", 0, allCaps(), func() bool { return true }))
	if got := r.List(); len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
}

func TestSelect_NoAvailable(t *testing.T) {
	r := NewRegistry()
	// All available functions return false.
	_ = r.Register(newFactory("docker", 0, allCaps(), func() bool { return false }))
	_, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true})
	if !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("Select err = %v, want ErrNoAvailableBackend", err)
	}
}

func TestSelect_NoCapabilityCoverage(t *testing.T) {
	r := NewRegistry()
	// Backend available but lacks Introspectable.
	_ = r.Register(newFactory("docker", 0, role.CapabilitySet{Lifecycle: true, Executable: true}, func() bool { return true }))
	_, err := r.Select(context.Background(), role.CapabilitySet{Introspectable: true})
	if !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("Select err = %v, want ErrNoAvailableBackend", err)
	}
}

func TestSelect_PrefersLowestPriority(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactory("high", 10, allCaps(), func() bool { return true }))
	_ = r.Register(newFactory("low", 0, allCaps(), func() bool { return true }))
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != "low" {
		t.Fatalf("Select chose %q, want %q (lower priority preferred)", f.Name, "low")
	}
}

func TestSelect_PriorityTiebreakByName(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactory("b", 0, allCaps(), func() bool { return true }))
	_ = r.Register(newFactory("a", 0, allCaps(), func() bool { return true }))
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != "a" {
		t.Fatalf("Select chose %q, want %q (name tiebreak)", f.Name, "a")
	}
}

func TestSelect_FiltersUnavailable(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactory("down", 0, allCaps(), func() bool { return false }))
	_ = r.Register(newFactory("up", 1, allCaps(), func() bool { return true }))
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != "up" {
		t.Fatalf("Select chose %q, want %q", f.Name, "up")
	}
}

func TestPackageLevelRegisterAndSelect(t *testing.T) {
	name := "pkg-test-backend"
	if _, ok := Default.Get(name); ok {
		t.Fatalf("name %q already registered", name)
	}
	// Cleanup so repeated runs don't collide.
	t.Cleanup(func() {
		Default.mu.Lock()
		delete(Default.factories, name)
		Default.mu.Unlock()
	})

	if err := Register(newFactory(name, 0, allCaps(), func() bool { return true })); err != nil {
		t.Fatalf("Register: %v", err)
	}
	f, err := Select(context.Background(), role.CapabilitySet{Lifecycle: true})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != name {
		t.Fatalf("Select chose %q, want %q", f.Name, name)
	}
	// Duplicate registration errors.
	if err := Register(newFactory(name, 0, allCaps(), func() bool { return true })); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestSelect_EmptyRegistry(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}); !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("err = %v", err)
	}
}
