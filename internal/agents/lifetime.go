package agents

import (
	"fmt"
	"time"
)

// RunWallclockLimit validates before duration conversion or TTL addition. The
// existing 24-hour authorization ceiling includes the settlement allowance.
func RunWallclockLimit(seconds int64) (time.Duration, error) {
	if seconds < 0 || seconds > int64((defaultGrantTTL-OrphanGrace)/time.Second) {
		return 0, fmt.Errorf("wallclock limit must fit within the 24-hour grant lifetime including 15-minute settlement allowance")
	}
	if seconds == 0 {
		return time.Hour, nil
	}
	return time.Duration(seconds) * time.Second, nil
}
