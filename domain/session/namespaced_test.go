package session

import (
	"errors"
	"strings"
	"testing"
)

// TestParseNamespacedID_Valid pins the happy path: a `slug:internal` id parses
// into its two parts. docker:<64hex> (a real docker container id) and
// seatbelt:darwin-3 (the darwin backend's own id) are the canonical forms.
func TestParseNamespacedID_Valid(t *testing.T) {
	hex64 := strings.Repeat("a", 64)

	cases := []struct {
		in       string
		slug     string
		internal string
	}{
		{in: "docker:" + hex64, slug: "docker", internal: hex64},
		{in: "seatbelt:darwin-3", slug: "seatbelt", internal: "darwin-3"},
		// Slugs may contain internal separators.
		{in: "foo_bar:abc", slug: "foo_bar", internal: "abc"},
		{in: "foo.bar:abc", slug: "foo.bar", internal: "abc"},
	}
	for _, c := range cases {
		n, err := ParseNamespacedID(c.in)
		if err != nil {
			t.Fatalf("ParseNamespacedID(%q) err = %v, want nil", c.in, err)
		}
		if n.Slug != c.slug || n.Internal != c.internal {
			t.Fatalf("ParseNamespacedID(%q) = {%q,%q}, want {%q,%q}", c.in, n.Slug, n.Internal, c.slug, c.internal)
		}
		if got := n.String(); got != c.in {
			t.Fatalf("String() = %q, want round-trip %q", got, c.in)
		}
	}
}

// TestParseNamespacedID_EmptyParts pins that neither the slug nor the internal
// part may be empty.
func TestParseNamespacedID_EmptyParts(t *testing.T) {
	for _, in := range []string{":abc", "docker:", ":", "docker:\t"} {
		if _, err := ParseNamespacedID(in); !errors.Is(err, ErrInvalidNamespacedID) {
			t.Fatalf("ParseNamespacedID(%q) err = %v, want ErrInvalidNamespacedID", in, err)
		}
	}
}

// TestParseNamespacedID_NoColon pins the bare-id rejection: an id without a
// colon is not namespaced and is therefore invalid (fail-closed).
func TestParseNamespacedID_NoColon(t *testing.T) {
	for _, in := range []string{"", "abc", strings.Repeat("a", 64), "   "} {
		if _, err := ParseNamespacedID(in); !errors.Is(err, ErrInvalidNamespacedID) {
			t.Fatalf("ParseNamespacedID(%q) err = %v, want ErrInvalidNamespacedID", in, err)
		}
	}
}

// TestParseNamespacedID_MultipleColons pins the STRICT single-separator rule
// (user decision 4): an internal part containing ':' is rejected.
func TestParseNamespacedID_MultipleColons(t *testing.T) {
	for _, in := range []string{"docker:a:b", "a:b:c", "docker::", "::"} {
		if _, err := ParseNamespacedID(in); !errors.Is(err, ErrInvalidNamespacedID) {
			t.Fatalf("ParseNamespacedID(%q) err = %v, want ErrInvalidNamespacedID", in, err)
		}
	}
}

// TestParseNamespacedID_InvalidSlugChars pins the slug character set: uppercase,
// whitespace, slashes and a leading/trailing '-' or '.' are all rejected.
func TestParseNamespacedID_InvalidSlugChars(t *testing.T) {
	for _, in := range []string{
		"Docker:abc", // uppercase
		"docker x:y", // whitespace
		"docker/:y",  // slash
		"-docker:x",  // leading hyphen
		"docker-:x",  // trailing hyphen
		".docker:x",  // leading dot
		"docker.:x",  // trailing dot
		"докер:x",    // non-ASCII
	} {
		if _, err := ParseNamespacedID(in); !errors.Is(err, ErrInvalidNamespacedID) {
			t.Fatalf("ParseNamespacedID(%q) err = %v, want ErrInvalidNamespacedID", in, err)
		}
	}
}

// TestNamespacedID_Short pins the short display id (user decision 2): the slug
// is PRESERVED and only the internal id is shortened to 12 chars. An internal
// id already at or below 12 chars is unchanged.
func TestNamespacedID_Short(t *testing.T) {
	hex64 := strings.Repeat("a", 64)

	// docker:<64hex> -> docker:<12> (slug kept, internal shortened).
	n, err := ParseNamespacedID("docker:" + hex64)
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Short(); got != "docker:"+strings.Repeat("a", 12) {
		t.Fatalf("Short() = %q, want docker:<12>", got)
	}

	// seatbelt:darwin-3 has internal len 8 (<= 12) -> unchanged, slug kept.
	n2, err := ParseNamespacedID("seatbelt:darwin-3")
	if err != nil {
		t.Fatal(err)
	}
	if got := n2.Short(); got != "seatbelt:darwin-3" {
		t.Fatalf("Short() = %q, want seatbelt:darwin-3 unchanged", got)
	}

	// Boundary: internal exactly 12 chars -> unchanged.
	n3, err := ParseNamespacedID("docker:1234567890ab")
	if err != nil {
		t.Fatal(err)
	}
	if got := n3.Short(); got != "docker:1234567890ab" {
		t.Fatalf("Short() = %q, want docker:1234567890ab unchanged (len == 12)", got)
	}
}
