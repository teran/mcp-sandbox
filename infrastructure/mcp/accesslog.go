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

// AccessLog emits the per-call line (L08).
func (a *AccessLogger) AccessLog(e handler.AccessLogEntry) {
	a.entry.WithFields(logrus.Fields{
		"tool":       e.Tool,
		"args":       e.Args,
		"source":     e.Source,
		"duration":   e.Duration.Milliseconds(),
		"outcome":    e.Outcome,
		"request_id": e.RequestID,
	}).Info("tools/call")
}

// HandlerDependencies assembles the shared wrapper's dependencies from the
// server logger. Redaction, request-id generation and context wiring come from
// the infrastructure logging package (S02/L09); the access-log line goes
// through the logrus pipeline (L08).
func HandlerDependencies(logger *logging.Logger) handler.Dependencies {
	return handler.Dependencies{
		AccessLogger:  NewAccessLogger(logger.Entry()),
		Redact:        logging.Redact,
		NewRequestID:  logging.NewRequestID,
		WithRequestID: logging.WithRequestID,
	}
}
