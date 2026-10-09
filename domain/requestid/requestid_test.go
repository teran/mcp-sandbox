package requestid

import (
	"context"
	"testing"
)

func TestWithIDAndIDFromContext(t *testing.T) {
	ctx := WithID(context.Background(), "req-1")
	id, ok := IDFromContext(ctx)
	if !ok || id != "req-1" {
		t.Fatalf("IDFromContext = %q, %v", id, ok)
	}
}

func TestIDFromContext_PlainContext(t *testing.T) {
	if _, ok := IDFromContext(context.Background()); ok {
		t.Fatal("plain context should have no request id")
	}
}
