// Package requestid provides a minimal domain-level request-id correlation
// mechanism (L09). An opaque request id is generated once per tools/call by the
// shared handler-wrapper and stored in the context, so every layer (application
// and infrastructure) can attach it to its log lines without depending on any
// infrastructure. It mirrors the session-id context helpers in domain/session
// and the progress sink mechanism (domain/progress): the key and accessors live
// in the domain so layer edges stay clean (A01) — the package imports only the
// standard library.
package requestid

import "context"

type key struct{}

// WithID returns a context carrying the given request id (L09).
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// IDFromContext returns the request id stored in the context, if any.
func IDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(key{}).(string)
	return id, ok
}
