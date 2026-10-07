package def

import (
	"testing"
)

func TestRedactionField(t *testing.T) {
	r := RedactionField{Name: "argv", Secret: true}
	if r.Name != "argv" || !r.Secret {
		t.Fatalf("RedactionField not populated: %+v", r)
	}
}

func TestAnnotations_DefaultsAreClosedWorld(t *testing.T) {
	// The zero value must not accidentally imply an open-world or destructive
	// tool; the sandbox domain is closed (S07/S10).
	var a Annotations
	if a.OpenWorldHint {
		t.Fatal("OpenWorldHint must default to false")
	}
	if a.DestructiveHint {
		t.Fatal("DestructiveHint must default to false")
	}
}
