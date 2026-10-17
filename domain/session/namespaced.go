package session

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidNamespacedID is returned by ParseNamespacedID for any id that is
// not a valid `slug:internal` pair. It is a sentinel so callers can classify a
// malformed session id without parsing the message.
var ErrInvalidNamespacedID = errors.New("session: invalid namespaced id")

// slugPattern matches a valid slug: a driver-style lowercase name that starts
// and ends with an alphanumeric and may contain internal '.', '_' or '-'.
// A leading/trailing '-' or '.' is rejected so slugs read like stable
// identifiers (e.g. "docker", "seatbelt", "foo_bar"). A slash, whitespace or
// uppercase is never accepted.
var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// NamespacedID is a session id split into its routing slug and its internal
// backend id (SPEC §7.8): "docker:6b4464b592f9" or "seatbelt:darwin-3". The
// slug selects the owning backend/driver; the internal id is the backend's own
// sandbox handle (the docker container id, or the darwin session id).
type NamespacedID struct {
	// Slug is the namespacing prefix identifying the owning backend.
	Slug string
	// Internal is the backend-owned sandbox id. It must NOT contain ':'.
	Internal string
}

// ParseNamespacedID parses a `slug:internal` session id. It is STRICT: it
// splits on the FIRST ':' and requires exactly one separator (internal must not
// contain ':'), both parts non-empty, and a slug matching slugPattern. It never
// panics — any malformed input returns ErrInvalidNamespacedID.
func ParseNamespacedID(id string) (NamespacedID, error) {
	slug, internal, ok := strings.Cut(id, ":")
	if !ok {
		return NamespacedID{}, ErrInvalidNamespacedID
	}
	if slug == "" || internal == "" {
		return NamespacedID{}, ErrInvalidNamespacedID
	}
	// A blank (whitespace-only) internal part is treated as empty — e.g.
	// "docker:\t" is rejected rather than accepted with an invisible id.
	if strings.TrimSpace(internal) == "" {
		return NamespacedID{}, ErrInvalidNamespacedID
	}
	if strings.ContainsRune(internal, ':') {
		return NamespacedID{}, ErrInvalidNamespacedID
	}
	if !slugPattern.MatchString(slug) {
		return NamespacedID{}, ErrInvalidNamespacedID
	}

	return NamespacedID{Slug: slug, Internal: internal}, nil
}

// String renders the id back as `slug:internal`.
func (n NamespacedID) String() string {
	return n.Slug + ":" + n.Internal
}

// Short renders the namespaced short display id: the slug is preserved and the
// internal id is shortened to ShortIDLength characters (docker-style, SPEC
// §6.6). An internal id already at or below ShortIDLength is left unchanged.
// It never panics.
func (n NamespacedID) Short() string {
	internal := n.Internal
	if len(internal) > ShortIDLength {
		internal = internal[:ShortIDLength]
	}

	return n.Slug + ":" + internal
}
