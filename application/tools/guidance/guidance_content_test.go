package guidance

import (
	"strings"
	"testing"
)

// TestContent_KnownURI_HappyPath covers the successful branch of Content (the
// red tests only exercise the not-found branch), so mutation testing cannot
// silently flip the found/ok result or empty the embedded body without a test
// failing.
func TestContent_KnownURI_HappyPath(t *testing.T) {
	for _, uri := range URIs() {
		content, ok := Content(uri)
		if !ok {
			t.Fatalf("Content(%q) = _, false, want found", uri)
		}
		if strings.TrimSpace(content) == "" {
			t.Fatalf("Content(%q) returned empty body", uri)
		}
		if !strings.Contains(content, "#") {
			t.Fatalf("Content(%q) body has no markdown heading", uri)
		}
	}
}

// TestAll_ContentMatchesContent pins that the resource registry's Content field
// agrees with the Content() lookup for every URI, so the two lookup paths stay
// in lockstep.
func TestAll_ContentMatchesContent(t *testing.T) {
	for _, r := range All() {
		got, ok := Content(r.URI)
		if !ok {
			t.Fatalf("Content(%q) not found for registered resource", r.URI)
		}
		if got != r.Content {
			t.Fatalf("All().Content mismatch for %q", r.URI)
		}
	}
}
