// Package progress provides a small pure-domain mechanism for streaming
// progress notifications from long-running tools (Phase 6, SPEC §7.x /
// roadmap item 6.x). It is transport-agnostic: a Sink is stashed into a context and
// retrieved by the layers that produce output, so the application and
// infrastructure layers can emit progress without depending on MCP or any
// infrastructure. The package imports only the standard library and never
// infrastructure (A01).
package progress

import (
	"context"
	"io"
)

// Event is a single progress notification. Message is the human-readable
// payload (a command output line, or a lifecycle marker such as "start",
// "done" or "cancelled"), Progress is the cumulative byte count / position of
// the stream (monotonic, never decreasing), and Total is the expected end
// position when it is known (0 when unknown).
type Event struct {
	Message  string
	Progress int64
	Total    int64
}

// Sink receives progress events. Implementations must be safe to call
// concurrently and must not panic; they never return a value.
type Sink func(ctx context.Context, e Event)

type sinkKey struct{}

// WithSink returns a context that carries the given sink. A nil sink leaves the
// context unchanged (nothing is stored), so SinkFromContext still returns the
// nil-safe no-op.
func WithSink(ctx context.Context, s Sink) context.Context {
	if s == nil {
		return ctx
	}

	return context.WithValue(ctx, sinkKey{}, s)
}

// SinkFromContext returns the sink stored in the context, or a nil-safe no-op
// sink when none is present. The returned value is never nil, so callers can
// always invoke it without checking.
func SinkFromContext(ctx context.Context) Sink {
	if s, ok := ctx.Value(sinkKey{}).(Sink); ok && s != nil {
		return s
	}

	return func(context.Context, Event) {}
}

// LineBatcherOption configures a LineBatcher.
type LineBatcherOption func(*lineBatcherOptions)

type lineBatcherOptions struct {
	flushThreshold int
	maxMessageLen  int
}

// WithFlushThreshold sets the partial-buffer size at which a batcher emits a
// chunk of buffered (newline-less) bytes, leaving the remainder buffered.
// Default 4096.
func WithFlushThreshold(n int) LineBatcherOption {
	return func(o *lineBatcherOptions) { o.flushThreshold = n }
}

// WithMaxMessageLen sets the maximum length of a single emitted Event.Message.
// Longer messages are split into capped segments that together preserve every
// byte. Default 4096.
func WithMaxMessageLen(n int) LineBatcherOption {
	return func(o *lineBatcherOptions) { o.maxMessageLen = n }
}

const (
	defaultFlushThreshold = 4096
	defaultMaxMessageLen  = 4096
)

// LineBatcher is an io.Writer that buffers command output and emits one Event
// per complete '\n'-terminated line (newline stripped), flushes buffered
// partial bytes as a chunk once they reach the flush threshold (leaving the
// remainder buffered), and splits any message longer than the per-message cap
// into capped segments preserving all bytes. Event.Progress is the cumulative
// byte count written so far, which is monotonic. It uses no goroutine or
// ticker: events are emitted only synchronously from Write and Flush, so it
// carries zero lifecycle risk.
type LineBatcher struct {
	sink           Sink
	buffer         []byte
	counter        int64
	flushThreshold int
	maxMessageLen  int
}

// compile-time check that LineBatcher satisfies io.Writer.
var _ io.Writer = (*LineBatcher)(nil)

// NewLineBatcher returns a LineBatcher that emits events to sink. A nil sink is
// accepted (events are discarded), so the batcher can be used purely as a byte
// counter.
func NewLineBatcher(sink Sink, opts ...LineBatcherOption) *LineBatcher {
	o := lineBatcherOptions{
		flushThreshold: defaultFlushThreshold,
		maxMessageLen:  defaultMaxMessageLen,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.flushThreshold <= 0 {
		o.flushThreshold = defaultFlushThreshold
	}
	if o.maxMessageLen <= 0 {
		o.maxMessageLen = defaultMaxMessageLen
	}

	return &LineBatcher{
		sink:           sink,
		flushThreshold: o.flushThreshold,
		maxMessageLen:  o.maxMessageLen,
	}
}

// Write implements io.Writer. It processes p byte-by-byte, accumulating a
// buffer and emitting an Event for each complete newline-terminated line
// (newline stripped) or a chunk once the buffered partial reaches the flush
// threshold. It returns len(p), nil.
func (b *LineBatcher) Write(p []byte) (int, error) {
	for _, c := range p {
		b.counter++
		if c == '\n' {
			b.emit(context.Background(), string(b.buffer))
			b.buffer = b.buffer[:0]

			continue
		}
		b.buffer = append(b.buffer, c)
		if len(b.buffer) >= b.flushThreshold {
			b.emit(context.Background(), string(b.buffer[:b.flushThreshold]))
			b.buffer = b.buffer[b.flushThreshold:]
		}
	}

	return len(p), nil
}

// Flush emits any buffered partial bytes as a single event and clears the
// buffer. It is idempotent: calling it when the buffer is empty is a no-op.
func (b *LineBatcher) Flush(ctx context.Context) {
	if len(b.buffer) == 0 {
		return
	}
	b.emit(ctx, string(b.buffer))
	b.buffer = b.buffer[:0]
}

// Progress returns the cumulative number of bytes written so far.
func (b *LineBatcher) Progress() int64 { return b.counter }

// emit splits msg into capped segments (each at most maxMessageLen bytes) and
// sends one Event per segment, carrying the current cumulative Progress.
// Empty messages are dropped so a bare newline produces no spurious event.
func (b *LineBatcher) emit(ctx context.Context, msg string) {
	if b.sink == nil {
		return
	}
	for len(msg) > b.maxMessageLen {
		b.sink(ctx, Event{Message: msg[:b.maxMessageLen], Progress: b.counter})
		msg = msg[b.maxMessageLen:]
	}
	if msg != "" {
		b.sink(ctx, Event{Message: msg, Progress: b.counter})
	}
}
