package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ThrottleRule is one persistent bounded throttle: at most Limit recorded
// failures inside Window, then a Block. It never permanently locks an account.
type ThrottleRule struct {
	Scope  string
	Limit  int
	Window time.Duration
	Block  time.Duration
}

// ThrottleState reports whether a subject is currently blocked.
type ThrottleState struct {
	Blocked    bool
	RetryAfter time.Duration
	Attempts   int
}

// ThrottleStatus reads the current counter without recording an attempt.
func (s *Store) ThrottleStatus(ctx context.Context, rule ThrottleRule, subjectHash string, now time.Time) (ThrottleState, error) {
	if rule.Limit <= 0 {
		return ThrottleState{}, nil
	}
	var blockedUntil *time.Time
	var attempts int
	err := s.pool.QueryRow(ctx, `
		SELECT attempts, blocked_until FROM identity.auth_throttle_counters
		WHERE scope = $1 AND subject_hash = $2`, rule.Scope, subjectHash).Scan(&attempts, &blockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return ThrottleState{}, nil
	}
	if err != nil {
		return ThrottleState{}, fmt.Errorf("read throttle: %w", err)
	}
	return throttleState(blockedUntil, attempts, now), nil
}

// RecordAuthFailure records one failed attempt and returns the resulting state.
// Crossing the limit blocks the subject for the rule's block duration.
func (s *Store) RecordAuthAttempt(ctx context.Context, rule ThrottleRule, subjectHash string, now time.Time) (ThrottleState, error) {
	if rule.Limit <= 0 {
		return ThrottleState{}, nil
	}
	var attempts int
	var blockedUntil *time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO identity.auth_throttle_counters (scope, subject_hash, window_started_at, attempts, blocked_until, updated_at)
		VALUES ($1, $2, $3, 1, NULL, $3)
		ON CONFLICT (scope, subject_hash) DO UPDATE SET
			attempts = CASE
				WHEN identity.auth_throttle_counters.blocked_until IS NOT NULL AND identity.auth_throttle_counters.blocked_until > $3 THEN identity.auth_throttle_counters.attempts
				WHEN identity.auth_throttle_counters.window_started_at <= $3 - make_interval(secs => $4) THEN 1
				ELSE identity.auth_throttle_counters.attempts + 1
			END,
			window_started_at = CASE
				WHEN identity.auth_throttle_counters.blocked_until IS NOT NULL AND identity.auth_throttle_counters.blocked_until > $3 THEN identity.auth_throttle_counters.window_started_at
				WHEN identity.auth_throttle_counters.window_started_at <= $3 - make_interval(secs => $4) THEN $3
				ELSE identity.auth_throttle_counters.window_started_at
			END,
			blocked_until = CASE
				WHEN identity.auth_throttle_counters.blocked_until IS NOT NULL AND identity.auth_throttle_counters.blocked_until > $3 THEN identity.auth_throttle_counters.blocked_until
				WHEN identity.auth_throttle_counters.window_started_at <= $3 - make_interval(secs => $4) THEN NULL
				WHEN identity.auth_throttle_counters.attempts + 1 >= $5 THEN $3 + make_interval(secs => $6)
				ELSE NULL
			END,
			updated_at = $3
		RETURNING attempts, blocked_until`,
		rule.Scope, subjectHash, now, rule.Window.Seconds(), rule.Limit, rule.Block.Seconds()).
		Scan(&attempts, &blockedUntil)
	if err != nil {
		return ThrottleState{}, fmt.Errorf("record throttle: %w", err)
	}
	return throttleState(blockedUntil, attempts, now), nil
}

// ClearAuthFailures forgets a subject's failures after a successful attempt.
func (s *Store) ClearAuthThrottle(ctx context.Context, scope, subjectHash string) error {
	if _, err := s.pool.Exec(ctx, `
		DELETE FROM identity.auth_throttle_counters WHERE scope = $1 AND subject_hash = $2`,
		scope, subjectHash); err != nil {
		return fmt.Errorf("clear throttle: %w", err)
	}
	return nil
}

// PruneAuthThrottle deletes counters that are outside their window and not
// currently blocked, so the table stays bounded.
func (s *Store) PruneAuthThrottle(ctx context.Context, before, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM identity.auth_throttle_counters
		WHERE updated_at < $1 AND (blocked_until IS NULL OR blocked_until <= $2)`,
		before, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune throttle: %w", err)
	}
	return tag.RowsAffected(), nil
}

func throttleState(blockedUntil *time.Time, attempts int, now time.Time) ThrottleState {
	if blockedUntil == nil || !blockedUntil.After(now) {
		return ThrottleState{Attempts: attempts}
	}
	return ThrottleState{Blocked: true, RetryAfter: blockedUntil.Sub(now), Attempts: attempts}
}
