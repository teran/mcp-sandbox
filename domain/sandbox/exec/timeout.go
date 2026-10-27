package exec

import (
	"math"
	"time"
)

// TimeoutDuration converts a seconds value (a uint64) into a time.Duration,
// saturating at the largest representable duration to avoid int64 overflow.
// A Request.Timeout is an unbounded uint64, so a naive
// time.Duration(seconds)*time.Second could wrap to a negative value (G115).
func TimeoutDuration(seconds uint64) time.Duration {
	const maxSeconds = uint64(math.MaxInt64 / int64(time.Second))
	if seconds > maxSeconds {
		seconds = maxSeconds
	}

	return time.Duration(seconds) * time.Second
}
