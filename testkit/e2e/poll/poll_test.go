package poll

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUntilObservesBoundedCondition(t *testing.T) {
	t.Parallel()
	observations := 0
	err := Until(context.Background(), time.Second, time.Millisecond, func(context.Context) (bool, string, error) {
		observations++
		return observations == 3, "waiting", nil
	})
	if err != nil || observations != 3 {
		t.Fatalf("observations = %d, error = %v", observations, err)
	}
}

func TestUntilDoesNotRetryObservationErrors(t *testing.T) {
	t.Parallel()
	observations := 0
	terminal := errors.New("terminal assertion")
	err := Until(context.Background(), time.Second, time.Millisecond, func(context.Context) (bool, string, error) {
		observations++
		return false, "failed", terminal
	})
	if !errors.Is(err, terminal) || observations != 1 {
		t.Fatalf("observations = %d, error = %v", observations, err)
	}
}
