// Package poll provides bounded condition observation, not scenario retries.
package poll

import (
	"context"
	"fmt"
	"time"
)

// Observe returns whether the condition is complete, a diagnostic description
// of the last observed state, or a terminal observation error.
type Observe func(context.Context) (done bool, detail string, err error)

// Until observes immediately and then at interval until success or timeout.
// Observation errors fail immediately; callers must not hide a failed assertion
// as transient retry behavior.
func Until(ctx context.Context, timeout, interval time.Duration, observe Observe) error {
	if timeout <= 0 || interval <= 0 {
		return fmt.Errorf("poll timeout and interval must be positive")
	}
	if observe == nil {
		return fmt.Errorf("poll observer is nil")
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastDetail string
	for {
		done, detail, err := observe(bounded)
		lastDetail = detail
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-bounded.Done():
			return fmt.Errorf("condition not met within %s (last observation: %s): %w", timeout, lastDetail, bounded.Err())
		case <-time.After(interval):
		}
	}
}
