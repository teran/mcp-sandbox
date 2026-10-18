package mcp

import (
	"github.com/sirupsen/logrus"

	"github.com/teran/mcp-sandbox/application/tools/handler"
	"github.com/teran/mcp-sandbox/infrastructure/logging"
)

// AccessLogger adapts the shared handler-wrapper's access-log hook to the
// server's logrus pipeline (L08). Each tools/call emits one line at info with
// the tool name, redacted args, source, duration, outcome and request_id.
// Secrets are never present because the wrapper redacts the args before calling
// AccessLog (S02).
type AccessLogger struct {
	entry *logrus.Entry
}

// NewAccessLogger returns an access-log adapter writing to the given logrus
// entry.
func NewAccessLogger(entry *logrus.Entry) *AccessLogger {
	return &AccessLogger{entry: entry}
}

// AccessLog emits the per-call line (L08). For a hoisted entry (sandbox_exec)
// it emits the top-level command/summary fields and omits args entirely; for a
// normal entry it emits the args map and no command/summary.
func (a *AccessLogger) AccessLog(e handler.AccessLogEntry) {
	fields := logrus.Fields{
		"tool":       e.Tool,
		"source":     e.Source,
		"duration":   e.Duration.Milliseconds(),
		"outcome":    e.Outcome,
		"request_id": e.RequestID,
	}
	if e.Command != "" {
		fields["command"] = e.Command
	}
	if e.Summary != "" {
		fields["summary"] = e.Summary
	}
	if e.Args != nil {
		fields["args"] = e.Args
	}
	a.entry.WithFields(fields).Info("tools/call")
}

// TraceLogger adapts the shared handler-wrapper's trace hook to the server's
// logrus pipeline (L09). WithFields carries fields forward so request_id and
// tool appear on every trace line. These lines are trace-level, so they only
// appear at LOG_LEVEL=trace.
type TraceLogger struct {
	entry *logrus.Entry
}

// NewTraceLogger returns a trace-log adapter writing to the given logrus entry.
func NewTraceLogger(entry *logrus.Entry) *TraceLogger {
	return &TraceLogger{entry: entry}
}

// WithFields returns a trace logger with the given fields merged in.
func (t *TraceLogger) WithFields(fields map[string]any) handler.TraceLogger {
	return &TraceLogger{entry: t.entry.WithFields(logrus.Fields(fields))}
}

// Tracef emits a trace-level line.
func (t *TraceLogger) Tracef(format string, args ...any) { t.entry.Tracef(format, args...) }

// Debugf emits a debug-level line.
func (t *TraceLogger) Debugf(format string, args ...any) { t.entry.Debugf(format, args...) }

// HandlerDependencies assembles the shared wrapper's dependencies from the
// server logger. Redaction, request-id generation and context wiring come from
// the infrastructure logging package (S02/L09); the access-log line and trace
// diagnostics go through the logrus pipeline (L08/L09).
func HandlerDependencies(logger *logging.Logger) handler.Dependencies {
	return handler.Dependencies{
		AccessLogger:  NewAccessLogger(logger.Entry()),
		TraceLogger:   NewTraceLogger(logger.Entry()),
		Redact:        logging.Redact,
		NewRequestID:  logging.NewRequestID,
		WithRequestID: logging.WithRequestID,
	}
}
