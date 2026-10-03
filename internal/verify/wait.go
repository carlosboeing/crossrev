package verify

import (
	"context"
	"time"
)

// WaitInterval is the required-check re-read cadence for both legs.
const WaitInterval = 30 * time.Second

// Wait re-reads pending or missing checks until terminal evidence, timeout,
// cancellation, a stop label or a changed head. Hooks preserve each leg's clock.
func Wait(ctx context.Context, minutes int, current Evidence, now func() time.Time, sleep func(time.Duration), stopped, headChanged func() bool, read func() Evidence) Evidence {
	if current.State != Pending && current.State != Missing {
		return current
	}
	if minutes <= 0 {
		return current
	}
	deadline := now().Add(time.Duration(minutes) * time.Minute)
	for now().Before(deadline) {
		if ctx.Err() != nil {
			return current
		}
		sleep(WaitInterval)
		if ctx.Err() != nil {
			return current
		}
		if stopped() {
			return current
		}
		if headChanged() {
			return current
		}
		current = read()
		if current.State != Pending && current.State != Missing {
			return current
		}
	}
	return current
}
