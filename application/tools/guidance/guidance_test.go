package guidance

import (
	"strings"
	"testing"
)

// redactedMarker matches the server's redaction marker (S02). Guidance content
// must never carry a secret or a placeholder for one.
const redactedMarker = "***REDACTED***"

// TestInstructions_PresentAndIndexed asserts the server-level instructions are
// non-empty, state the redirect invariant, and index every resource URI — and
// reference no URI that is not in the registry. An instructions block that
// omits a resource, or that points at a ghost URI, breaks self-description.
func TestInstructions_PresentAndIndexed(t *testing.T) {
	ins := Instructions()
	if strings.TrimSpace(ins) == "" {
		t.Fatal("Instructions() must be non-empty")
	}
	if RedirectNote == "" {
		t.Fatal("RedirectNote must be non-empty (it is the redirect invariant)")
	}
	if !strings.Contains(ins, RedirectNote) {
		t.Fatal("Instructions() must contain the redirect note")
	}
	uris := URIs()
	for _, uri := range uris {
		if !strings.Contains(ins, uri) {
			t.Fatalf("Instructions() must reference resource URI %q", uri)
		}
	}
	// Every URI string mentioned in the instructions that looks like a
	// guidance resource must actually be in the registry (no dead pointers).
	known := map[string]bool{}
	for _, uri := range uris {
		known[uri] = true
	}
	for _, tok := range strings.Fields(ins) {
		if strings.HasPrefix(tok, "sandbox://guidance/") {
			if !known[tok] {
				t.Fatalf("Instructions() references unknown resource URI %q", tok)
			}
		}
	}
}

// TestResources_WellFormed asserts the registry is non-empty and every resource
// is fully specified with a unique, guidance-schemed URI. Order must be
// deterministic across calls.
func TestResources_WellFormed(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() must be non-empty")
	}
	seen := map[string]bool{}
	for _, r := range all {
		if r.URI == "" {
			t.Fatal("resource URI must be non-empty")
		}
		if !strings.HasPrefix(r.URI, "sandbox://guidance/") {
			t.Fatalf("resource URI %q must use the sandbox://guidance/ scheme", r.URI)
		}
		if seen[r.URI] {
			t.Fatalf("duplicate resource URI %q", r.URI)
		}
		seen[r.URI] = true
		if r.Name == "" || r.Title == "" || r.Description == "" || r.Content == "" {
			t.Fatalf("resource %q must have non-empty Name/Title/Description/Content", r.URI)
		}
		if r.MIMEType == "" {
			t.Fatalf("resource %q must declare a MIME type", r.URI)
		}
	}
	// Deterministic order: two calls must agree element-for-element.
	if a, b := All(), All(); len(a) != len(b) {
		t.Fatal("All() length unstable across calls")
	} else {
		for i := range a {
			if a[i].URI != b[i].URI {
				t.Fatalf("All() order unstable at %d: %q vs %q", i, a[i].URI, b[i].URI)
			}
		}
	}
}

// TestResources_SecretsSafe (S02) asserts no resource body carries the
// redaction marker or a secret placeholder — guidance is plain, safe markdown.
func TestResources_SecretsSafe(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() must be non-empty to check secret-safety")
	}
	for _, r := range all {
		if strings.Contains(r.Content, redactedMarker) {
			t.Fatalf("resource %q content must not contain the redaction marker", r.URI)
		}
		if strings.Contains(r.Content, "secret:") {
			t.Fatalf("resource %q content must not contain a `secret:` placeholder", r.URI)
		}
	}
}

// TestResources_ANSISafe (S09) asserts no resource body carries ESC or other
// C0 control characters (beyond newline/tab, which markdown uses).
func TestResources_ANSISafe(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() must be non-empty to check ANSI-safety")
	}
	for _, r := range all {
		for _, ch := range r.Content {
			if ch < 0x20 && ch != '\n' && ch != '\t' && ch != '\r' {
				t.Fatalf("resource %q content contains control char %q (ANSI-unsafe)", r.URI, ch)
			}
		}
		if strings.ContainsRune(r.Content, '\x1b') {
			t.Fatalf("resource %q content contains an ESC sequence", r.URI)
		}
	}
}

// TestContent_IsMarkdown asserts every resource body is non-empty and parses as
// markdown (carries a heading).
func TestContent_IsMarkdown(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() must be non-empty to check markdown")
	}
	for _, r := range all {
		if strings.TrimSpace(r.Content) == "" {
			t.Fatalf("resource %q content must be non-empty", r.URI)
		}
		if !strings.Contains(r.Content, "#") {
			t.Fatalf("resource %q content must be markdown with a heading", r.URI)
		}
	}
}

// TestContent_UnknownURI_NotFound asserts Content returns not-found for a URI
// that is not in the registry (the read handler maps this to ResourceNotFoundError).
func TestContent_UnknownURI_NotFound(t *testing.T) {
	if c, ok := Content("sandbox://guidance/nope"); ok {
		t.Fatalf("Content(unknown) = (%q, true), want not-found", c)
	}
}

// TestAll_HasSixResources asserts the registry holds exactly the six target
// resources and that every target URI constant resolves to a registered URI.
func TestAll_HasSixResources(t *testing.T) {
	all := All()
	if len(all) != 6 {
		t.Fatalf("All() has %d resources, want 6", len(all))
	}
	expected := []string{
		URIOverview, URIRedirect, URICreate, URIExec, URIFS, URISecurity,
	}
	registered := map[string]bool{}
	for _, r := range all {
		registered[r.URI] = true
	}
	for _, uri := range expected {
		if uri == "" {
			t.Fatal("a target URI constant is empty")
		}
		if !registered[uri] {
			t.Fatalf("target URI %q is not registered", uri)
		}
	}
	if uris := URIs(); len(uris) != len(all) {
		t.Fatalf("URIs() length %d != All() length %d", len(uris), len(all))
	}
}
