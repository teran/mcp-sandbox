// Package darwin provides portable policy-denial detection for the
// /usr/bin/sandbox-exec (Seatbelt) backend (SPEC §7.12). macOS does not have
// the child log its own denials; they are recorded in the unified log under
// subsystem == "com.apple.sandbox". This file reads a log line, decides
// whether it is a sandbox policy deny, and maps it onto an L08-style
// `sandbox_deny {session_id, pid, ...}` record with **no secrets** (S02). It
// is pure Go and compiles on every GOOS; the macOS-specific unified-log tail
// lives behind the darwin build tag.
package darwin

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// sandboxSubsystem is the unified-log subsystem that records macOS sandbox
// policy denials (SPEC §7.12). The classified DenyEvent always carries it.
const sandboxSubsystem = "com.apple.sandbox"

// denyPidPattern matches a process id token of the form "pid <n>" (also the
// "for PID <n>" variant), capturing the numeric id. It is matched
// case-insensitively over the whole line.
var denyPidPattern = regexp.MustCompile(`(?i)\bpid\s+(\d+)`)

// DenyEvent is the parsed representation of a single policy-deny log line.
// Subsystem is always "com.apple.sandbox" for a classified deny; Message is a
// SHORT synthesized summary built from the parsed fields — never the raw line,
// so a secret embedded in the line's noise cannot leak into it (S02).
type DenyEvent struct {
	Subsystem string
	Operation string
	Target    string
	Pid       int
	Message   string
}

// SandboxDenyLog is the L08-style access/deny log record bound to a session
// (SPEC §7.12 / L08). It carries only structured fields — never the raw log
// line — so no secret can appear in any field (S02).
type SandboxDenyLog struct {
	SessionID string
	Subsystem string
	Operation string
	Target    string
	Message   string
	Pid       int
}

// logTailer is the boundary over the (macOS-specific) unified-log line source.
// Tests substitute an in-memory tailer; the real `log stream` wrapper lives
// behind the darwin build tag.
type logTailer interface {
	Lines(ctx context.Context) (<-chan string, error)
}

// isDenyText reports whether a line/block of text carries a sandbox deny
// marker: the word "deny" together with a sandbox subsystem marker (either the
// literal "com.apple.sandbox" or the "Sandbox:" prefix seatbelt emits). It is
// the shared predicate used by ClassifyDeny and the in-band IsPolicyDenial
// check.
func isDenyText(s string) bool {
	lower := strings.ToLower(s)

	return strings.Contains(lower, "deny") &&
		(strings.Contains(lower, sandboxSubsystem) || strings.Contains(lower, "sandbox:"))
}

// parseDenyClause extracts the operation and optional target that follow the
// "deny" keyword. The target is the token immediately after the operation
// unless it opens a parenthesized marker (e.g. "(com.apple.sandbox)"), in which
// case there is no target. ok is false when no deny keyword is present.
func parseDenyClause(line string) (operation, target string, ok bool) {
	idx := strings.Index(strings.ToLower(line), "deny")
	if idx < 0 {
		return "", "", false
	}

	fields := strings.Fields(line[idx+len("deny"):])
	if len(fields) == 0 {
		return "", "", false
	}

	operation = fields[0]
	if len(fields) > 1 && !strings.HasPrefix(fields[1], "(") {
		target = fields[1]
	}

	return operation, target, true
}

// synthesizeMessage builds a short, safe summary from the parsed fields. It
// never includes any part of the raw line, so secrets embedded in the noise can
// not leak into the message (S02).
func synthesizeMessage(operation, target string) string {
	if target == "" {
		return "deny " + operation
	}

	return "deny " + operation + " " + target
}

// parsePid extracts the numeric process id from a line, returning 0 when the
// line carries no pid. Extraction of this optional field is fail-open on the
// parse (a deny without a pid still classifies, Pid stays 0); whether the line
// is a deny at all is decided fail-closed elsewhere.
func parsePid(line string) int {
	m := denyPidPattern.FindStringSubmatch(line)
	if m == nil {
		return 0
	}
	// The pattern captures \d+ only, so the conversion cannot fail.
	pid, _ := strconv.Atoi(m[1])

	return pid
}

// ClassifyDeny parses a single unified-log line and reports whether it is a
// sandbox policy deny from subsystem "com.apple.sandbox". It is fail-closed: an
// empty/malformed/garbage line, a deny from any other subsystem, or a line
// without a deny marker is never classified as a deny and never panics (SPEC
// §7.12 / S02). The returned Message is a synthesized short summary built from
// the parsed fields — never the raw line.
func ClassifyDeny(line string) (DenyEvent, bool) {
	if !isDenyText(line) {
		return DenyEvent{}, false
	}

	operation, target, ok := parseDenyClause(line)
	if !ok {
		return DenyEvent{}, false
	}

	return DenyEvent{
		Subsystem: sandboxSubsystem,
		Operation: operation,
		Target:    target,
		Pid:       parsePid(line),
		Message:   synthesizeMessage(operation, target),
	}, true
}

// ToSandboxDeny maps a parsed DenyEvent and a session id onto an L08-style
// sandbox_deny log record. Only structured, parsed fields are copied — the raw
// line is never stored, so a secret cannot appear in any record field (S02).
func ToSandboxDeny(sessionID string, ev DenyEvent) SandboxDenyLog {
	return SandboxDenyLog{
		SessionID: sessionID,
		Subsystem: ev.Subsystem,
		Operation: ev.Operation,
		Target:    ev.Target,
		Message:   ev.Message,
		Pid:       ev.Pid,
	}
}

// IsPolicyDenial reports whether a backend Result is a sandbox policy failure
// rather than a normal exit code (SPEC §7.12): the child's stderr carries a
// deny marker or an in-band EPERM ("Operation not permitted"). A plain non-zero
// exit, a signal-terminated run without a marker, and a nil Result are all
// false — fail-closed, with no panic.
func IsPolicyDenial(res *Result) bool {
	if res == nil {
		return false
	}
	stderr := string(res.Stderr)

	return isDenyText(stderr) || strings.Contains(strings.ToLower(stderr), "operation not permitted")
}

// ConsumeDenials reads lines from the tailer, classifies each one and, for
// denials only, emits a sandbox_deny record bound to the session. A Lines error
// is propagated as-is. On context cancellation it returns ctx.Err()
// (context.Canceled / DeadlineExceeded) without leaking a goroutine.
func ConsumeDenials(ctx context.Context, t logTailer, sessionID string, emit func(SandboxDenyLog)) error {
	lines, err := t.Lines(ctx)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return nil
			}
			if ev, isDeny := ClassifyDeny(line); isDeny {
				emit(ToSandboxDeny(sessionID, ev))
			}
		}
	}
}
