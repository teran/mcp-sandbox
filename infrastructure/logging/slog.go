package logging

import (
	"context"
	"log/slog"

	"github.com/sirupsen/logrus"
)

// slogHandler adapts the Go stdlib slog.Handler interface to logrus (L07/
// L03GO), so the go-sdk server's slog logger can be forwarded into the server's
// logrus pipeline. request_id carried in the context is threaded onto the
// record (L09).
type slogHandler struct {
	logger *logrus.Logger
	attrs  []slog.Attr
	group  string
}

// NewSlogHandler returns a slog.Handler that forwards records to the given
// logrus logger.
func NewSlogHandler(l *logrus.Logger) slog.Handler {
	return &slogHandler{logger: l}
}

// NewSlogLogger returns a slog.Logger backed by the given logrus logger, for
// use as the go-sdk ServerOptions.Logger.
func NewSlogLogger(l *logrus.Logger) *slog.Logger {
	return slog.New(NewSlogHandler(l))
}

// Enabled reports whether the handler handles records at the given level. The
// logrus logger's own level gate applies at emit time.
func (h *slogHandler) Enabled(context.Context, slog.Level) bool { return true }

// Handle emits a log record to logrus.
func (h *slogHandler) Handle(ctx context.Context, rec slog.Record) error {
	fields := logrus.Fields{}
	for _, a := range h.attrs {
		fields[a.Key] = a.Value.Any()
	}
	rec.Attrs(func(a slog.Attr) bool {
		fields[a.Key] = a.Value.Any()
		return true
	})

	entry := h.logger.WithFields(fields)
	if id, ok := RequestIDFromContext(ctx); ok {
		entry = entry.WithField("request_id", id)
	}

	switch rec.Level {
	case slog.LevelDebug:
		entry.Debug(rec.Message)
	case slog.LevelInfo:
		entry.Info(rec.Message)
	case slog.LevelWarn:
		entry.Warn(rec.Message)
	case slog.LevelError:
		entry.Error(rec.Message)
	default:
		entry.Info(rec.Message)
	}
	return nil
}

// WithAttrs returns a handler with the given attributes attached.
func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	combined = append(combined, h.attrs...)
	combined = append(combined, attrs...)
	return &slogHandler{logger: h.logger, attrs: combined, group: h.group}
}

// WithGroup returns a handler that groups subsequent attributes.
func (h *slogHandler) WithGroup(name string) slog.Handler {
	if h.group != "" {
		name = h.group + "." + name
	}
	return &slogHandler{logger: h.logger, attrs: h.attrs, group: name}
}
