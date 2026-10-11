package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestNew_DisabledWhenLevelEmpty(t *testing.T) {
	l, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	if l.Enabled() {
		t.Fatal("logger should be disabled")
	}
	if l.file != nil {
		t.Fatal("disabled logger should have no file")
	}
	// Banner must be a no-op.
	l.Banner("app", "v1", "abc", "now")
}

func TestNew_TextFormatWritesToFile(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "sandbox.log")
	l, err := New(Options{Level: "info", Filename: filename, Format: "text"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	if !l.Enabled() {
		t.Fatal("logger should be enabled")
	}
	l.Entry().Info("hello world")

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "hello world") {
		t.Fatalf("log file missing message: %q", data)
	}

	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestNew_JSONFormat(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "sandbox.json")
	l, err := New(Options{Level: "warn", Filename: filename, Format: "json"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	l.WithField("key", "value").Warn("warn message")

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), `"level":"warning"`) && !strings.Contains(string(data), `"level":"warn"`) {
		t.Fatalf("expected json level field, got: %q", data)
	}
	if !strings.Contains(string(data), `"key":"value"`) {
		t.Fatalf("expected key field in json, got: %q", data)
	}
}

func TestNew_DefaultFilename(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	l, err := New(Options{Level: "info"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()
	if _, err := os.Stat(DefaultLogFilename); err != nil {
		t.Fatalf("default log file not created: %v", err)
	}
}

func TestNew_OpenError(t *testing.T) {
	// Filename in a non-existent directory forces OpenFile to fail.
	if _, err := New(Options{Level: "info", Filename: filepath.Join(t.TempDir(), "no-dir", "x.log")}); err == nil {
		t.Fatal("expected open error")
	}
}

func TestNew_InvalidLevel(t *testing.T) {
	if _, err := New(Options{Level: "banana"}); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestNew_InvalidFormat(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(Options{Level: "info", Filename: filepath.Join(dir, "l.log"), Format: "yaml"}); err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestBanner_Format(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "banner.log")
	l, err := New(Options{Level: "info", Filename: filename})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Banner("mcp-sandbox", "1.0.0", "deadbeef", "2026-10-07")

	data, _ := os.ReadFile(filename)
	if !strings.Contains(string(data), "Starting mcp-sandbox/1.0.0 (commit: deadbeef; built at 2026-10-07)") {
		t.Fatalf("banner wrong: %q", data)
	}
}

func TestRequestID(t *testing.T) {
	ctx := WithRequestID(t.Context(), "req-123")
	id, ok := RequestIDFromContext(ctx)
	if !ok || id != "req-123" {
		t.Fatalf("RequestIDFromContext = %q, %v", id, ok)
	}

	if _, ok := RequestIDFromContext(t.Context()); ok {
		t.Fatal("expected no request id in plain context")
	}
}

func TestNewRequestID_Unique(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if a == "" || b == "" {
		t.Fatal("request ids must not be empty")
	}
	if a == b {
		t.Fatal("request ids must be unique")
	}
}

func TestRequestEntry_AttachesRequestID(t *testing.T) {
	var buf bytes.Buffer
	l := logrus.New()
	l.SetOutput(&buf)
	l.SetLevel(logrus.TraceLevel)
	l.SetFormatter(&logrus.TextFormatter{DisableTimestamp: true, DisableColors: true})
	base := logrus.NewEntry(l)

	// With a request id in the context, RequestEntry attaches it.
	ctx := WithRequestID(t.Context(), "req-456")
	e := RequestEntry(ctx, base)
	e.Trace("hello")
	if !strings.Contains(buf.String(), "req-456") {
		t.Fatalf("request_id not attached:\n%s", buf.String())
	}

	// Without a request id, RequestEntry returns the base entry unchanged.
	buf.Reset()
	RequestEntry(t.Context(), base).Trace("plain")
	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("unexpected request_id on plain context:\n%s", buf.String())
	}
}

func TestRedact(t *testing.T) {
	type inner struct {
		Token string `json:"token" secret:"true"`
		Name  string `json:"name"`
	}
	type outer struct {
		ID    string            `json:"id"`
		Inner inner             `json:"inner"`
		Map   map[string]string `json:"map"`
		List  []inner           `json:"list"`
		Ptr   *inner            `json:"ptr"`
	}

	ptr := &inner{Token: "s1", Name: "p"}
	o := outer{
		ID:    "id-1",
		Inner: inner{Token: "s2", Name: "n"},
		Map:   map[string]string{"k": "v"},
		List:  []inner{{Token: "s3", Name: "l"}},
		Ptr:   ptr,
	}

	r := mustMap(t, Redact(o))
	if r["id"] != "id-1" {
		t.Fatalf("id = %v", r["id"])
	}

	in := mustMap(t, r["inner"])
	if in["token"] != redactedMarker {
		t.Fatalf("inner.token = %v, want %q", in["token"], redactedMarker)
	}
	if in["name"] != "n" {
		t.Fatalf("inner.name = %v", in["name"])
	}

	m := mustMap(t, r["map"])
	if m["k"] != "v" {
		t.Fatalf("map = %v", m)
	}

	list, ok := r["list"].([]any)
	if !ok {
		t.Fatalf("expected list, got %T", r["list"])
	}
	first := mustMap(t, list[0])
	if first["token"] != redactedMarker {
		t.Fatalf("list[0].token not redacted: %v", list[0])
	}

	ptrOut := mustMap(t, r["ptr"])
	if ptrOut["token"] != redactedMarker || ptrOut["name"] != "p" {
		t.Fatalf("ptr redaction wrong: %v", ptrOut)
	}
}

func TestRedact_PrimitivesAndNil(t *testing.T) {
	if Redact("plain") != "plain" {
		t.Fatal("string passthrough failed")
	}
	if Redact(42) != 42 {
		t.Fatal("int passthrough failed")
	}
	if Redact(nil) != nil {
		t.Fatal("nil passthrough failed")
	}
	var p *struct{ X string }
	if Redact(p) != nil {
		t.Fatal("nil pointer should redact to nil")
	}
}

func TestRedact_SecretFieldNameFromJSON(t *testing.T) {
	type s struct {
		Value string `json:"value" secret:"true"`
	}
	r := mustMap(t, Redact(s{Value: "x"}))
	if r["value"] != redactedMarker {
		t.Fatalf("value = %v", r["value"])
	}
}

func TestRedact_NonSecretUnexported(t *testing.T) {
	type s struct {
		hidden string
	}
	r := mustMap(t, Redact(s{hidden: "x"}))
	// Unexported fields cannot be read via reflection and are omitted (nil).
	if r["hidden"] != nil {
		t.Fatalf("hidden = %v, want nil", r["hidden"])
	}
}

func mustMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", v)
	}

	return m
}
