package handler

import (
	"testing"

	"github.com/teran/mcp-sandbox/application/tools/def"
)

func TestArgRedactor_RedactsSecretKeys(t *testing.T) {
	redact := argRedactor([]def.RedactionField{{Name: "argv", Secret: true}, {Name: "secrets", Secret: true}})
	in := map[string]any{
		"argv":    []any{"echo", "secret"},
		"secrets": []any{map[string]any{"name": "k", "value": "v"}},
		"path":    "/safe",
	}
	out := redact(in).(map[string]any)
	if out["argv"] != redactedMarker {
		t.Fatalf("argv = %v, want redacted", out["argv"])
	}
	if out["secrets"] != redactedMarker {
		t.Fatalf("secrets = %v, want redacted", out["secrets"])
	}
	if out["path"] != "/safe" {
		t.Fatalf("path = %v, want unchanged", out["path"])
	}
}

func TestArgRedactor_NoSecretKeys(t *testing.T) {
	redact := argRedactor(nil)
	out := redact(map[string]any{"a": 1, "b": []any{2, 3}})
	m := out.(map[string]any)
	if m["a"] != 1 {
		t.Fatalf("got %v", out)
	}
	if len(m["b"].([]any)) != 2 {
		t.Fatalf("got %v", out)
	}
}

func TestRedactKeys_NestedMap(t *testing.T) {
	secret := map[string]bool{"token": true}
	out := redactKeys(map[string]any{"nested": map[string]any{"token": "x", "keep": "y"}}, secret)
	nested := out.(map[string]any)["nested"].(map[string]any)
	if nested["token"] != redactedMarker {
		t.Fatalf("token = %v", nested["token"])
	}
	if nested["keep"] != "y" {
		t.Fatalf("keep = %v", nested["keep"])
	}
}

func TestRedactKeys_Scalar(t *testing.T) {
	if redactKeys("x", map[string]bool{}) != "x" {
		t.Fatal("scalar should pass through")
	}
}
