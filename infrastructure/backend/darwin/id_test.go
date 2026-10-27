package darwin

// Tests for the deterministic, name-based seatbelt session-id derivation (§7.8).
// A seatbelt session's internal id is a UUIDv5 (SHA-1 name-based) of the
// sandbox's identity — [profile, session_id, cwd] joined with "/" — under a
// fixed namespace, encoded as base36 (0-9a-z, no special characters) →
// exactly 25 chars. Determinism is intentional: same identity → same id; a
// repeat sandbox_create with the same identity surfaces as a duplicate via
// search-before-create (X02); different profile/session/cwd → different,
// distinguishable ids.

import (
	"math/big"
	"strings"
	"testing"

	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
	domainsession "github.com/teran/mcp-sandbox/domain/session"
)

// seatbeltIdentitySpec returns a seatbelt spec with a full identity (profile,
// client session id, working dir) AND a valid Isolation block so it can be
// passed to sbpl.Generate / Backend.Create.
func seatbeltIdentitySpec() spec.SandboxSpec {
	s := baseSeatbeltSpec()
	s.Profile = "mac"
	s.ClientSessionID = "opencode-sess-9"
	s.WorkingDir = "/home/user/proj"

	return s
}

// TestSessionIDFromSpec_Deterministic pins that the same identity yields the
// same id (same profile/session/cwd), and that id is what Create returns.
func TestSessionIDFromSpec_Deterministic(t *testing.T) {
	s := seatbeltIdentitySpec()
	a := sessionIDFromSpec(s)
	b := sessionIDFromSpec(s)
	if a != b {
		t.Fatalf("sessionIDFromSpec(same identity) = %q vs %q, want identical", a, b)
	}
	if a == "" {
		t.Fatal("sessionIDFromSpec returned empty id")
	}

	// Create returns exactly this derived id.
	fake := newFakeRunner()
	be := newTestBackend(t, fake, []string{"/home/user/proj"})
	id, err := be.Create(t.Context(), s)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != a {
		t.Fatalf("Create id = %q, want derived id %q", id, a)
	}
}

// TestSessionIDFromSpec_Distinct pins that different identities yield different
// ids: changing the profile, the session id or the cwd each produce a distinct
// id (so sandboxes are distinguishable and cross-process collisions are avoided).
func TestSessionIDFromSpec_Distinct(t *testing.T) {
	base := seatbeltIdentitySpec()
	baseID := sessionIDFromSpec(base)

	variants := map[string]spec.SandboxSpec{
		"different profile":  {Profile: "other", ClientSessionID: base.ClientSessionID, WorkingDir: base.WorkingDir},
		"different session":  {Profile: base.Profile, ClientSessionID: "opencode-sess-OTHER", WorkingDir: base.WorkingDir},
		"different cwd":      {Profile: base.Profile, ClientSessionID: base.ClientSessionID, WorkingDir: "/other/dir"},
		"empty profile":      {Profile: "", ClientSessionID: base.ClientSessionID, WorkingDir: base.WorkingDir},
		"empty session":      {Profile: base.Profile, ClientSessionID: "", WorkingDir: base.WorkingDir},
		"empty cwd":          {Profile: base.Profile, ClientSessionID: base.ClientSessionID, WorkingDir: ""},
		"all empty identity": {Profile: "", ClientSessionID: "", WorkingDir: ""},
	}

	seen := map[string]string{baseID: "base"}
	for name, v := range variants {
		got := sessionIDFromSpec(v)
		if got == baseID {
			t.Fatalf("variant %q produced the same id %q as base", name, got)
		}
		if prev, dup := seen[got]; dup {
			t.Fatalf("variants %q and %q collide on id %q", prev, name, got)
		}
		seen[got] = name
	}
}

// TestSessionIDFromSpec_25Base36Chars pins the exact id shape: 25 chars of
// base36 (lowercase 0-9a-z) with NO special characters (no '-', '_', '+', '/',
// '='), and a lossless round-trip back to 16 bytes (the UUIDv5 output).
func TestSessionIDFromSpec_25Base36Chars(t *testing.T) {
	id := sessionIDFromSpec(seatbeltIdentitySpec())
	if len(id) != base36IDLength {
		t.Fatalf("id len = %d, want %d (%q)", len(id), base36IDLength, id)
	}
	if strings.ContainsAny(id, "-_+/= ") {
		t.Fatalf("id %q must be pure alphanumeric base36 with no special characters", id)
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
			t.Fatalf("id %q contains non-base36 char %q", id, r)
		}
	}

	// Round-trip: base36 → bytes → base36 reproduces the same id.
	n := new(big.Int)
	if _, ok := n.SetString(id, 36); !ok {
		t.Fatalf("id %q is not valid base36", id)
	}
	if len(n.Bytes()) > 16 {
		t.Fatalf("decoded id len = %d, want <= 16 (a UUIDv5)", len(n.Bytes()))
	}
}

// TestSessionIDFromSpec_ShortTruncates pins the display-id contract: the full
// namespaced id "seatbelt:<25>" truncates to "seatbelt:<12>" (the first 12
// chars of the 25-char internal id), docker-style (SPEC §6.6).
func TestSessionIDFromSpec_ShortTruncates(t *testing.T) {
	id := sessionIDFromSpec(seatbeltIdentitySpec())
	full := "seatbelt:" + id

	n, err := domainsession.ParseNamespacedID(full)
	if err != nil {
		t.Fatalf("ParseNamespacedID(%q): %v", full, err)
	}
	if n.Internal != id {
		t.Fatalf("internal = %q, want %q", n.Internal, id)
	}
	want := "seatbelt:" + id[:domainsession.ShortIDLength]
	if got := n.Short(); got != want {
		t.Fatalf("Short() = %q, want %q", got, want)
	}
	if got := domainsession.ShortID(full); got != want {
		t.Fatalf("ShortID(%q) = %q, want %q", full, got, want)
	}
}

// TestSessionID_OrderingOfEmptyPartsPinned pins that identity ignores the
// position of empty parts: leading/trailing empty fields collapse and the
// non-empty parts are joined in [profile, session, cwd] order.
func TestSessionID_OrderingOfEmptyPartsPinned(t *testing.T) {
	// A spec with only a profile + cwd must ignore the empty session part.
	sparse := spec.SandboxSpec{Profile: "mac", WorkingDir: "/work"}
	// A spec with a blank-whitespace session part is equivalent.
	ws := spec.SandboxSpec{Profile: "mac", ClientSessionID: "   ", WorkingDir: "/work"}
	if a, b := sessionIDFromSpec(sparse), sessionIDFromSpec(ws); a != b {
		t.Fatalf("whitespace/empty session parts must be equivalent: %q vs %q", a, b)
	}
	if sparseID := sessionIDFromSpec(sparse); sparseID == sessionIDFromSpec(seatbeltIdentitySpec()) {
		t.Fatal("sparse identity must differ from the full identity")
	}
}
