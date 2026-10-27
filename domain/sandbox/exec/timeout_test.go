package exec

import (
	"math"
	"testing"
	"time"
)

func TestTimeoutDuration(t *testing.T) {
	// Normal case: seconds map directly to a duration.
	if got := TimeoutDuration(5); got != 5*time.Second {
		t.Fatalf("TimeoutDuration(5) = %v, want %v", got, 5*time.Second)
	}

	// Boundary: the largest second count that still fits in an int64 duration.
	maxSeconds := uint64(math.MaxInt64 / int64(time.Second))
	if got := TimeoutDuration(maxSeconds); got != time.Duration(maxSeconds)*time.Second {
		t.Fatalf("TimeoutDuration(maxSeconds) = %v, want %v",
			got, time.Duration(maxSeconds)*time.Second)
	}

	// Overflow case: an unbounded value must saturate, not wrap negative.
	overflow := uint64(math.MaxUint64)
	if got := TimeoutDuration(overflow); got != time.Duration(maxSeconds)*time.Second {
		t.Fatalf("TimeoutDuration(MaxUint64) = %v, want saturated %v",
			got, time.Duration(maxSeconds)*time.Second)
	}
	if TimeoutDuration(overflow) < 0 {
		t.Fatalf("TimeoutDuration(MaxUint64) wrapped to a negative duration: %v",
			TimeoutDuration(overflow))
	}

	// Zero stays zero.
	if got := TimeoutDuration(0); got != 0 {
		t.Fatalf("TimeoutDuration(0) = %v, want 0", got)
	}
}
