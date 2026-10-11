// Package logging configures logrus for the server (L01–L09, L01GO–L04GO):
// stdio→file channel, enablement by LOG_LEVEL, banner, SDK slog→logrus wiring,
// access-log at info, request_id correlation and a single redaction helper
// driven by `secret:true` annotations (S02).
package logging

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/domain/requestid"
)

// DefaultLogFilename is used when stdio logging is enabled but no LOG_FILENAME
// is provided.
const DefaultLogFilename = "mcp-sandbox.log"

// redactedMarker replaces secret values in all output and logs.
const redactedMarker = "***REDACTED***"

// logFileMode is the strict 0600 permission applied to the stdio log file so
// log contents (which can carry request ids) stay readable only by the owner
// (L01).
const logFileMode = 0o600

// Options configures the logger.
type Options struct {
	Level    string // empty ⇒ logging disabled
	Filename string // stdio log file path (chmod 600)
	Format   string // text or json
}

// Logger wraps the configured logrus logger together with the underlying log
// file so callers can close it when done.
type Logger struct {
	base    *logrus.Logger
	entry   *logrus.Entry
	file    *os.File
	enabled bool
}

// New configures logrus according to opts. When Level is empty (unset), a
// disabled no-op logger is returned (L02). In stdio mode logs go to the file
// at Filename with mode 0600, never stdout (L01/N04GO).
func New(opts Options) (*Logger, error) {
	if opts.Level == "" {
		l := logrus.New()
		l.SetOutput(io.Discard)

		return &Logger{base: l, entry: logrus.NewEntry(l), enabled: false}, nil
	}

	lvl, err := logrus.ParseLevel(opts.Level)
	if err != nil {
		return nil, fmt.Errorf("logging: invalid LOG_LEVEL %q: %w", opts.Level, err)
	}

	filename := opts.Filename
	if filename == "" {
		filename = DefaultLogFilename
	}

	l := logrus.New()
	l.SetLevel(lvl)
	switch opts.Format {
	case "", "text":
		l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, DisableColors: true})
	case "json":
		l.SetFormatter(&logrus.JSONFormatter{})
	default:
		return nil, fmt.Errorf("logging: invalid LOG_FORMAT %q (want text or json)", opts.Format)
	}

	f, err := openLogFile(filename)
	if err != nil {
		return nil, err
	}
	l.SetOutput(f)

	return &Logger{base: l, entry: logrus.NewEntry(l), file: f, enabled: true}, nil
}

// openLogFile opens (creating if needed) the log file with mode 0600. It uses
// os.Root scoped to the file's parent directory so that file access is
// contained under a fixed root (G304: no directory traversal via a variable
// path).
func openLogFile(filename string) (*os.File, error) {
	dir := filepath.Dir(filename)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("logging: open root %q: %w", dir, err)
	}
	defer func() { _ = root.Close() }()

	f, err := root.OpenFile(filepath.Base(filename), os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFileMode)
	if err != nil {
		return nil, fmt.Errorf("logging: open %q: %w", filename, err)
	}
	if err := f.Chmod(logFileMode); err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("logging: chmod %q: %w", filename, err)
	}

	return f, nil
}

// Enabled reports whether logging is enabled (i.e. LOG_LEVEL was set).
func (l *Logger) Enabled() bool {
	return l.enabled
}

// Banner emits the startup banner as the very first log line (B05/L06). It is
// a no-op when logging is disabled.
func (l *Logger) Banner(appName, appVersion, commit, timestamp string) {
	if !l.enabled {
		return
	}
	l.entry.Infof("Starting %s/%s (commit: %s; built at %s) ...", appName, appVersion, commit, timestamp)
}

// Entry returns the base logrus entry for direct use (e.g. access logs).
func (l *Logger) Entry() *logrus.Entry {
	return l.entry
}

// WithField returns an entry with an additional structured field.
func (l *Logger) WithField(key string, value any) *logrus.Entry {
	return l.entry.WithField(key, value)
}

// Logger returns the underlying logrus.Logger (used for SDK slog wiring).
func (l *Logger) Logger() *logrus.Logger {
	return l.base
}

// Close closes the underlying log file, if any.
func (l *Logger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}

	return nil
}

// WithRequestID returns a context carrying the given request id (L09). The
// key and accessors live in the domain (domain/requestid) so both the
// application and infrastructure layers can read it without an infrastructure
// dependency.
func WithRequestID(ctx context.Context, id string) context.Context {
	return requestid.WithID(ctx, id)
}

// RequestIDFromContext returns the request id stored in the context, if any.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	return requestid.IDFromContext(ctx)
}

// NewRequestID generates a new opaque request id.
func NewRequestID() string {
	return uuid.NewString()
}

// RequestEntry returns a logrus entry derived from base with the request_id
// from ctx attached when present (L09). It lets application/infrastructure
// layers thread request_id onto every trace/debug line from the request
// context without duplicating the lookup.
func RequestEntry(ctx context.Context, base *logrus.Entry) *logrus.Entry {
	if id, ok := RequestIDFromContext(ctx); ok {
		return base.WithField("request_id", id)
	}

	return base
}

// Redact returns a redacted copy of v: every field annotated `secret:"true"`
// (S02) is replaced with the redaction marker. Maps, slices, pointers and
// nested structs are walked recursively. This is the single redaction helper
// applied at the boundary of all output and logs.
func Redact(v any) any {
	return redactValue(reflect.ValueOf(v))
}

func redactValue(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Invalid:
		return nil
	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		t := v.Type()
		for i := range v.NumField() {
			f := t.Field(i)
			if isSecret(f) {
				out[fieldName(f)] = redactedMarker

				continue
			}
			out[fieldName(f)] = redactValue(v.Field(i))
		}

		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[fmt.Sprint(iter.Key().Interface())] = redactValue(iter.Value())
		}

		return out
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}

		return redactValue(v.Elem())
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := range v.Len() {
			out[i] = redactValue(v.Index(i))
		}

		return out
	default:
		if v.CanInterface() {
			return v.Interface()
		}

		return nil
	}
}

func isSecret(f reflect.StructField) bool {
	return strings.EqualFold(f.Tag.Get("secret"), "true")
}

func fieldName(f reflect.StructField) string {
	if tag := f.Tag.Get("json"); tag != "" {
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			return name
		}
	}

	return f.Name
}
