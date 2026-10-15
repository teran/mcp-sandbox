package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/role"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
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
	return role.CapabilitySet{Lifecycle: true, Executable: true, Introspectable: true}
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
	_, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverAuto)
	if !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("Select err = %v, want ErrNoAvailableBackend", err)
	}
}

func TestSelect_NoCapabilityCoverage(t *testing.T) {
	r := NewRegistry()
	// Backend available but lacks Introspectable.
	_ = r.Register(newFactory("docker", 0, role.CapabilitySet{Lifecycle: true, Executable: true}, func() bool { return true }))
	_, err := r.Select(context.Background(), role.CapabilitySet{Introspectable: true}, spec.DriverAuto)
	if !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("Select err = %v, want ErrNoAvailableBackend", err)
	}
}

func TestSelect_PrefersLowestPriority(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactory("high", 10, allCaps(), func() bool { return true }))
	_ = r.Register(newFactory("low", 0, allCaps(), func() bool { return true }))
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverAuto)
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
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverAuto)
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
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverAuto)
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
	f, err := Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverAuto)
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
	if _, err := r.Select(
		context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverAuto,
	); !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("err = %v", err)
	}
}

// newFactoryDriver is newFactory plus an explicit advertised driver.
func newFactoryDriver(name string, priority int, caps role.CapabilitySet, avail func() bool, driver spec.Driver) Factory {
	f := newFactory(name, priority, caps, avail)
	f.Driver = driver

	return f
}

// TestSelect_DriverDockerFiltersByDriver pins that a non-auto `want` narrows
// selection to factories advertising that driver, regardless of priority.
func TestSelect_DriverDockerFiltersByDriver(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactoryDriver("seatbelt", 0, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	_ = r.Register(newFactoryDriver("docker", 1, allCaps(), func() bool { return true }, spec.DriverDocker))
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverDocker)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != "docker" {
		t.Fatalf("Select chose %q, want docker (driver filter beats priority)", f.Name)
	}
}

// TestSelect_DriverSeatbeltFiltersByDriver pins the seatbelt side of the driver
// filter.
func TestSelect_DriverSeatbeltFiltersByDriver(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactoryDriver("docker", 0, allCaps(), func() bool { return true }, spec.DriverDocker))
	_ = r.Register(newFactoryDriver("seatbelt", 1, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverSeatbelt)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != "seatbelt" {
		t.Fatalf("Select chose %q, want seatbelt", f.Name)
	}
}

// TestSelect_DriverDoesNotOverrideAvailable pins that a matching driver never
// overrides availability: an unavailable seatbelt factory is skipped even when
// the caller asks for seatbelt.
func TestSelect_DriverDoesNotOverrideAvailable(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactoryDriver("seatbelt", 0, allCaps(), func() bool { return false }, spec.DriverSeatbelt))
	if _, err := r.Select(
		context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverSeatbelt,
	); !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("err = %v, want ErrNoAvailableBackend (driver must not cancel Available)", err)
	}
}

// TestSelect_DriverDoesNotOverrideCovers pins that a matching driver never
// overrides capability coverage.
func TestSelect_DriverDoesNotOverrideCovers(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactoryDriver(
		"seatbelt", 0, role.CapabilitySet{Lifecycle: true}, func() bool { return true }, spec.DriverSeatbelt,
	))
	if _, err := r.Select(
		context.Background(), role.CapabilitySet{Introspectable: true}, spec.DriverSeatbelt,
	); !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("err = %v, want ErrNoAvailableBackend (driver must not cancel covers)", err)
	}
}

// TestSelect_DriverNoMatch pins that requesting a driver with no matching,
// available, covering factory yields ErrNoAvailableBackend.
func TestSelect_DriverNoMatch(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactoryDriver("docker", 0, allCaps(), func() bool { return true }, spec.DriverDocker))
	if _, err := r.Select(
		context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverSeatbelt,
	); !errors.Is(err, ErrNoAvailableBackend) {
		t.Fatalf("err = %v, want ErrNoAvailableBackend", err)
	}
}

// TestSelect_DriverSortsByPriorityThenName pins that among the matching driver,
// selection still prefers lowest priority, then name as a deterministic
// tiebreaker.
func TestSelect_DriverSortsByPriorityThenName(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(newFactoryDriver("high", 10, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	_ = r.Register(newFactoryDriver("low", 0, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	_ = r.Register(newFactoryDriver("tie-b", 0, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	_ = r.Register(newFactoryDriver("tie-a", 0, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	// Exclude the non-matching docker factory.
	_ = r.Register(newFactoryDriver("docker", -1, allCaps(), func() bool { return true }, spec.DriverDocker))

	f, err := r.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverSeatbelt)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f.Name != "low" {
		t.Fatalf("Select chose %q, want low (lowest priority)", f.Name)
	}
	// Among the priority-0 seatbelt factories, name tiebreak should yield tie-a.
	// Re-register without the "low" (priority 0) and "high" factories is not
	// possible; assert that the priority-0 name ordering is deterministic by
	// selecting with only tie factories.
	r2 := NewRegistry()
	_ = r2.Register(newFactoryDriver("tie-b", 0, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	_ = r2.Register(newFactoryDriver("tie-a", 0, allCaps(), func() bool { return true }, spec.DriverSeatbelt))
	f2, err := r2.Select(context.Background(), role.CapabilitySet{Lifecycle: true}, spec.DriverSeatbelt)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if f2.Name != "tie-a" {
		t.Fatalf("Select chose %q, want tie-a (name tiebreak)", f2.Name)
	}
}
