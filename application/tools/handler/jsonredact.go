package handler

import (
	"github.com/teran/mcp-sandbox/application/tools/def"
)

// redactedMarker replaces sensitive values in access-log arguments. It matches
// the marker used by the server's struct-tag redactor so logs are consistent
// (S02).
const redactedMarker = "***REDACTED***"

// argRedactor returns a function that walks an arbitrary JSON value (maps,
// slices, scalars) and replaces the value of every object key listed as secret
// in the tool's redaction contract. It is applied to the raw access-log
// arguments — raw JSON carries no struct tags, so the contract's field names
// drive redaction (e.g. `argv` for sandbox_exec, `secrets` for sandbox_create).
func argRedactor(contract []def.RedactionField) func(any) any {
	keys := make(map[string]bool)
	for _, f := range contract {
		if f.Secret {
			keys[f.Name] = true
		}
	}

	return func(v any) any { return redactKeys(v, keys) }
}

// redactKeys replaces the value of every map entry whose key is in the secret
// set, recursing into nested maps and slices.
func redactKeys(v any, secret map[string]bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if secret[k] {
				out[k] = redactedMarker

				continue
			}
			out[k] = redactKeys(val, secret)
		}

		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactKeys(val, secret)
		}

		return out
	default:
		return v
	}
}
