package progress

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// recordingSink is a thread-safe Sink that appends every event it receives so
// tests can assert on the exact event stream.
type recordingSink struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingSink) sink(_ context.Context, e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingSink) got() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)

	return out
}

// collect returns a Sink that appends every event to the given slice under mu.
func collect(mu *sync.Mutex, dst *[]Event) Sink {
	return func(_ context.Context, e Event) {
		mu.Lock()
		defer mu.Unlock()
		*dst = append(*dst, e)
	}
}

// TestSinkFromContext_NoneIsNilSafe verifies that a context without a sink
// yields a nil-safe no-op sink: calling it never panics and emits nothing.
func TestSinkFromContext_NoneIsNilSafe(t *testing.T) {
	sink := SinkFromContext(context.Background())
	if sink == nil {
		t.Fatal("SinkFromContext on empty ctx must not return nil")
	}
	// Calling the no-op sink must not panic.
	sink(context.Background(), Event{Message: "x"})

	var got []Event
	var mu sync.Mutex
	outer := collect(&mu, &got)
	outer(context.Background(), Event{Message: "y"})
	if len(got) != 1 {
		t.Fatalf("sanity: collecting sink must record, got %v", got)
	}
}

// TestWithSink_SinkFromContext_RoundTrip verifies that a sink stored via
// WithSink is retrieved by SinkFromContext and receives emitted events.
func TestWithSink_SinkFromContext_RoundTrip(t *testing.T) {
	want := Event{Message: "hi", Progress: 3, Total: 10}
	rec := &recordingSink{}
	ctx := WithSink(context.Background(), rec.sink)

	sink := SinkFromContext(ctx)
	sink(context.Background(), want)

	ev := rec.got()
	if len(ev) != 1 || ev[0] != want {
		t.Fatalf("events = %v, want [%+v]", ev, want)
	}
}

// TestWithSink_NilLeavesContextUnchanged verifies that WithSink(ctx, nil)
// stores nothing, so SinkFromContext still returns a nil-safe no-op.
func TestWithSink_NilLeavesContextUnchanged(t *testing.T) {
	ctx := WithSink(context.Background(), nil)
	sink := SinkFromContext(ctx)
	if sink == nil {
		t.Fatal("must return a non-nil no-op sink")
	}
	// Emitting through the no-op sink must not panic and must not record.
	sink(context.Background(), Event{Message: "ignored"})
}

func TestLineBatcher_MultipleLinesRoundTrip(t *testing.T) {
	rec := &recordingSink{}
	b := NewLineBatcher(rec.sink)

	if _, err := b.Write([]byte("hello\nworld\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ev := rec.got()
	if len(ev) != 2 {
		t.Fatalf("events = %d, want 2 (one per complete line)", len(ev))
	}
	if ev[0].Message != "hello" {
		t.Errorf("event[0].Message = %q, want hello", ev[0].Message)
	}
	if ev[1].Message != "world" {
		t.Errorf("event[1].Message = %q, want world", ev[1].Message)
	}
	// Trailing newline must be stripped, no empty trailing message.
	if strings.Contains(ev[0].Message, "\n") || strings.Contains(ev[1].Message, "\n") {
		t.Fatalf("messages must not contain newlines: %+v", ev)
	}
}

func TestLineBatcher_PartialFlushAtThreshold(t *testing.T) {
	rec := &recordingSink{}
	b := NewLineBatcher(rec.sink, WithFlushThreshold(5))

	// "abcdef" has no newline; once the buffered partial reaches 5 bytes it
	// must flush "abcde", leaving "f" buffered until Flush().
	if _, err := b.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ev := rec.got()
	if len(ev) != 1 || ev[0].Message != "abcde" {
		t.Fatalf("after threshold flush events = %+v, want [abcde]", ev)
	}

	b.Flush(context.Background())
	ev = rec.got()
	if len(ev) != 2 || ev[1].Message != "f" {
		t.Fatalf("after Flush events = %+v, want trailing [f]", ev)
	}
}

func TestLineBatcher_FlushOnEOF(t *testing.T) {
	rec := &recordingSink{}
	b := NewLineBatcher(rec.sink)

	if _, err := b.Write([]byte("partial")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// No complete line and below threshold: nothing emitted yet.
	if got := rec.got(); len(got) != 0 {
		t.Fatalf("events before Flush = %+v, want none", got)
	}

	b.Flush(context.Background())
	ev := rec.got()
	if len(ev) != 1 || ev[0].Message != "partial" {
		t.Fatalf("after Flush events = %+v, want [partial]", ev)
	}
}

func TestLineBatcher_FlushIdempotent(t *testing.T) {
	rec := &recordingSink{}
	b := NewLineBatcher(rec.sink)
	if _, err := b.Write([]byte("x\ny")); err != nil {
		t.Fatal(err)
	}
	b.Flush(context.Background())
	b.Flush(context.Background()) // second flush must not re-emit the buffered partial
	if ev := rec.got(); len(ev) != 2 {
		t.Fatalf("events = %+v, want 2 (x then y once)", ev)
	}
}

func TestLineBatcher_MessageCap(t *testing.T) {
	rec := &recordingSink{}
	b := NewLineBatcher(rec.sink, WithMaxMessageLen(3))

	if _, err := b.Write([]byte("abcdef\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ev := rec.got()
	if len(ev) != 2 {
		t.Fatalf("events = %d, want 2 (line split across cap)", len(ev))
	}
	if ev[0].Message != "abc" || ev[1].Message != "def" {
		t.Fatalf("capped messages = %q, %q; want abc, def", ev[0].Message, ev[1].Message)
	}
	if concat := ev[0].Message + ev[1].Message; concat != "abcdef" {
		t.Fatalf("reassembled = %q, want abcdef", concat)
	}
}

func TestLineBatcher_MonotonicProgress(t *testing.T) {
	var got []Event
	var mu sync.Mutex
	b := NewLineBatcher(collect(&mu, &got))

	if _, err := b.Write([]byte("first\nsecond\nthird\n")); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("events = %d, want 3", len(got))
	}
	// Progress is the cumulative byte count at emission time and must be
	// non-decreasing across the stream.
	prev := int64(-1)
	for i, e := range got {
		if e.Progress < prev {
			t.Fatalf("progress not monotonic at event %d: %+v", i, got)
		}
		prev = e.Progress
	}
	if last := got[len(got)-1].Progress; last != int64(len("first\nsecond\nthird\n")) {
		t.Fatalf("final progress = %d, want %d", last, len("first\nsecond\nthird\n"))
	}
}

func TestLineBatcher_ProgressCounterAcrossWrites(t *testing.T) {
	b := NewLineBatcher(nil) // zero-sink, only checking the counter
	if _, err := b.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if p := b.Progress(); p != 2 {
		t.Fatalf("Progress = %d, want 2", p)
	}
	if _, err := b.Write([]byte("cde")); err != nil {
		t.Fatal(err)
	}
	if p := b.Progress(); p != 5 {
		t.Fatalf("Progress = %d, want 5", p)
	}
}

func TestLineBatcher_ZeroSinkNilSafe(t *testing.T) {
	b := NewLineBatcher(nil)
	if b == nil {
		t.Fatal("NewLineBatcher(nil) must return a batcher")
	}
	if _, err := b.Write([]byte("line\npartial")); err != nil {
		t.Fatalf("Write with nil sink: %v", err)
	}
	b.Flush(context.Background()) // must not panic
	if p := b.Progress(); p != int64(len("line\npartial")) {
		t.Fatalf("Progress = %d", p)
	}
}

func TestLineBatcher_MixedCompleteAndPartial(t *testing.T) {
	var got []Event
	var mu sync.Mutex
	b := NewLineBatcher(collect(&mu, &got), WithFlushThreshold(5))

	// "ab\ncd": the complete line "ab" emits immediately; the partial "cd" is
	// below the 5-byte threshold and is held until Flush().
	if _, err := b.Write([]byte("ab\ncd")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	n1 := len(got)
	mu.Unlock()
	if n1 != 1 || got[0].Message != "ab" {
		t.Fatalf("after Write events = %+v, want [ab]", got)
	}

	b.Flush(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[1].Message != "cd" {
		t.Fatalf("after Flush events = %+v, want [ab cd]", got)
	}
}
