package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Auth-email outbox states (architecture 7.5).
const (
	AuthEmailPending        = "pending"
	AuthEmailLeased         = "leased"
	AuthEmailAccepted       = "accepted"
	AuthEmailOutcomeUnknown = "outcome_unknown"
	AuthEmailFailed         = "failed"
)

// Sender outcomes.
const (
	SendOutcomeAccepted  = "accepted"
	SendOutcomeRetryable = "retryable"
	SendOutcomeUnknown   = "unknown"
)

// AuthEmailMessage is one rendered transactional authentication email.
type AuthEmailMessage struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// AuthEmailSendResult reports the sender's best knowledge of SMTP acceptance.
type AuthEmailSendResult struct {
	Outcome    string
	ErrorClass string
}

// AuthEmailSender delivers one transactional authentication email. The
// implementation must report ambiguity instead of guessing acceptance.
type AuthEmailSender interface {
	Send(ctx context.Context, message AuthEmailMessage) AuthEmailSendResult
}

// AuthEmailIntent is one secret-free outbox intent.
type AuthEmailIntent struct {
	OutboxID            string
	Purpose             string
	RecipientEmail      string
	RecipientIdentityID string
	AccessRequestID     string
	Locale              string
	Attempts            int
}

// QueueAuthEmailTx stores a secret-free delivery intent in the caller's
// transaction. The raw token is created only when the sender leases the intent.
func QueueAuthEmailTx(ctx context.Context, tx pgx.Tx, intent AuthEmailIntent) (string, error) {
	if AuthLinkTTL(intent.Purpose) <= 0 {
		return "", fmt.Errorf("unknown auth email purpose %q", intent.Purpose)
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO identity.auth_email_outbox (
			purpose, recipient_email, recipient_identity_id, access_request_id, locale)
		VALUES ($1, $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, $5)
		RETURNING id`,
		intent.Purpose, intent.RecipientEmail, intent.RecipientIdentityID,
		intent.AccessRequestID, intent.Locale).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("queue auth email: %w", err)
	}
	return id, nil
}

// QueueAuthEmail stores one secret-free delivery intent in its own transaction.
// Flows that must commit state and intent atomically use QueueAuthEmailTx.
func (s *Store) QueueAuthEmail(ctx context.Context, intent AuthEmailIntent) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin auth email: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := QueueAuthEmailTx(ctx, tx, intent)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit auth email: %w", err)
	}
	return id, nil
}

// ClaimAuthEmails leases pending intents for one worker. Lease expiry marks the
// previous send outcome unknown rather than retrying an ambiguous delivery.
func (s *Store) ClaimAuthEmails(ctx context.Context, owner string, limit int, leaseTTL time.Duration, now time.Time) ([]AuthEmailIntent, error) {
	rows, err := s.pool.Query(ctx, `
		WITH claimed AS (
			SELECT id FROM identity.auth_email_outbox
			WHERE state = 'pending'
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE identity.auth_email_outbox o
		SET state = 'leased', lease_owner = $2, leased_until = $3, attempts = o.attempts + 1
		FROM claimed
		WHERE o.id = claimed.id
		RETURNING o.id, o.purpose, o.recipient_email,
		          COALESCE(o.recipient_identity_id::text, ''),
		          COALESCE(o.access_request_id::text, ''), o.locale, o.attempts`,
		limit, owner, now.UTC().Add(leaseTTL))
	if err != nil {
		return nil, fmt.Errorf("claim auth emails: %w", err)
	}
	defer rows.Close()

	out := []AuthEmailIntent{}
	for rows.Next() {
		var intent AuthEmailIntent
		if err := rows.Scan(&intent.OutboxID, &intent.Purpose, &intent.RecipientEmail,
			&intent.RecipientIdentityID, &intent.AccessRequestID, &intent.Locale, &intent.Attempts); err != nil {
			return nil, err
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

// ExpireAuthEmailLeases marks overdue leases outcome_unknown. A worker that
// died may already have reached SMTP, so the delivery is never silently
// retried.
func (s *Store) ExpireAuthEmailLeases(ctx context.Context, now time.Time) ([]AuthEmailIntent, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE identity.auth_email_outbox
		SET state = 'outcome_unknown', outcome_unknown_at = $1, terminal_at = $1,
		    last_error_class = 'lease_expired'
		WHERE state = 'leased' AND leased_until <= $1
		RETURNING id, purpose, recipient_email, COALESCE(recipient_identity_id::text, ''),
		          COALESCE(access_request_id::text, ''), locale, attempts`, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("expire auth email leases: %w", err)
	}
	defer rows.Close()

	out := []AuthEmailIntent{}
	for rows.Next() {
		var intent AuthEmailIntent
		if err := rows.Scan(&intent.OutboxID, &intent.Purpose, &intent.RecipientEmail,
			&intent.RecipientIdentityID, &intent.AccessRequestID, &intent.Locale, &intent.Attempts); err != nil {
			return nil, err
		}
		out = append(out, intent)
	}
	return out, rows.Err()
}

// CreateAuthLinkForIntent creates the raw one-time token for a leased intent in
// its own transaction and returns the raw value exactly once.
func (s *Store) CreateAuthLinkForIntent(ctx context.Context, outboxID string, now time.Time) (AuthEmailIntent, string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AuthEmailIntent{}, "", fmt.Errorf("begin auth link: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	intent, err := scanAuthEmailIntent(tx.QueryRow(ctx, `
		SELECT id, purpose, recipient_email, COALESCE(recipient_identity_id::text, ''),
		       COALESCE(access_request_id::text, ''), locale, attempts
		FROM identity.auth_email_outbox
		WHERE id = $1 AND state = 'leased'
		FOR UPDATE`, outboxID))
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthEmailIntent{}, "", fmt.Errorf("auth email intent %s is not leased", outboxID)
	}
	if err != nil {
		return AuthEmailIntent{}, "", err
	}

	raw, _, err := createAuthLinkTx(ctx, tx, intent, now)
	if err != nil {
		return AuthEmailIntent{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return AuthEmailIntent{}, "", fmt.Errorf("commit auth link: %w", err)
	}
	return intent, raw, nil
}

func scanAuthEmailIntent(row pgx.Row) (AuthEmailIntent, error) {
	var intent AuthEmailIntent
	err := row.Scan(&intent.OutboxID, &intent.Purpose, &intent.RecipientEmail,
		&intent.RecipientIdentityID, &intent.AccessRequestID, &intent.Locale, &intent.Attempts)
	return intent, err
}

// MarkAuthEmailAccepted records definite SMTP acceptance.
func (s *Store) MarkAuthEmailAccepted(ctx context.Context, outboxID string, now time.Time) error {
	return s.finishAuthEmail(ctx, outboxID, AuthEmailAccepted, "", now, EventAuthEmailSent)
}

// MarkAuthEmailOutcomeUnknown records ambiguous SMTP acceptance. The issued
// link may still be valid for the recipient.
func (s *Store) MarkAuthEmailOutcomeUnknown(ctx context.Context, outboxID, errorClass string, now time.Time) error {
	return s.finishAuthEmail(ctx, outboxID, AuthEmailOutcomeUnknown, errorClass, now, EventAuthEmailOutcomeUnknown)
}

// MarkAuthEmailFailed records a definite, terminal delivery failure and
// invalidates any link issued for the intent.
func (s *Store) MarkAuthEmailFailed(ctx context.Context, outboxID, errorClass string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin fail auth email: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE identity.auth_link_tokens SET invalidated_at = $2, invalidated_reason = 'send_failed'
		WHERE outbox_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL`, outboxID, now.UTC()); err != nil {
		return fmt.Errorf("invalidate failed link: %w", err)
	}
	if err := finishAuthEmailTx(ctx, tx, outboxID, AuthEmailFailed, errorClass, now, EventAuthEmailFailed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReleaseAuthEmailRetry returns an intent to the pending state after a definite
// pre-acceptance failure, bounded by maxAttempts. The terminal attempt is
// recorded as failed and invalidates the unusable link.
func (s *Store) ReleaseAuthEmailRetry(ctx context.Context, outboxID, errorClass string, maxAttempts int, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin email retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var attempts int
	var state string
	if err := tx.QueryRow(ctx, `
		SELECT attempts, state FROM identity.auth_email_outbox WHERE id = $1 FOR UPDATE`, outboxID).
		Scan(&attempts, &state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read auth email: %w", err)
	}
	if state == AuthEmailFailed || state == AuthEmailAccepted || state == AuthEmailOutcomeUnknown {
		return tx.Commit(ctx)
	}
	if attempts < maxAttempts {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.auth_email_outbox
			SET state = 'pending', lease_owner = NULL, leased_until = NULL, last_error_class = $2
			WHERE id = $1`, outboxID, errorClass); err != nil {
			return fmt.Errorf("release auth email retry: %w", err)
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE identity.auth_link_tokens SET invalidated_at = $2, invalidated_reason = 'send_failed'
		WHERE outbox_id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL`, outboxID, now.UTC()); err != nil {
		return fmt.Errorf("invalidate failed link: %w", err)
	}
	if err := finishAuthEmailTx(ctx, tx, outboxID, AuthEmailFailed, errorClass, now, EventAuthEmailFailed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) finishAuthEmail(ctx context.Context, outboxID, state, errorClass string, now time.Time, event string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin finish auth email: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := finishAuthEmailTx(ctx, tx, outboxID, state, errorClass, now, event); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func finishAuthEmailTx(ctx context.Context, tx pgx.Tx, outboxID, state, errorClass string, now time.Time, event string) error {
	var terminalAt any
	if state == AuthEmailAccepted || state == AuthEmailOutcomeUnknown || state == AuthEmailFailed {
		terminalAt = now.UTC()
	}
	var acceptedAt, unknownAt any
	if state == AuthEmailAccepted {
		acceptedAt = now.UTC()
	}
	if state == AuthEmailOutcomeUnknown {
		unknownAt = now.UTC()
	}
	tag, err := tx.Exec(ctx, `
		UPDATE identity.auth_email_outbox
		SET state = $2, accepted_at = $3, outcome_unknown_at = $4, terminal_at = $5,
		    lease_owner = NULL, leased_until = NULL, last_error_class = $6
		WHERE id = $1`,
		outboxID, state, acceptedAt, unknownAt, terminalAt, nullString(errorClass))
	if err != nil {
		return fmt.Errorf("finish auth email: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("auth email intent %s not found", outboxID)
	}
	return AppendSecurityEventTx(ctx, tx, SecurityEvent{
		Event:     event,
		Outcome:   eventOutcome(event),
		Detail:    map[string]any{"state": state},
		CreatedAt: now,
	})
}

func eventOutcome(event string) string {
	switch event {
	case EventAuthEmailFailed, EventAuthEmailOutcomeUnknown, EventAuthEmailLeaseExpired:
		return OutcomeUnknown
	default:
		return OutcomeSuccess
	}
}

// AuthEmailWorkerConfig bounds one worker's behaviour.
type AuthEmailWorkerConfig struct {
	Owner                string
	Interval             time.Duration
	BatchSize            int
	LeaseTTL             time.Duration
	MaxAttempts          int
	HousekeepingInterval time.Duration
}

// AuthEmailWorker leases outbox intents, issues one-time links, renders EN/PT
// mail in memory, and records definite/ambiguous delivery outcomes.
type AuthEmailWorker struct {
	Store  *Store
	Sender AuthEmailSender
	Origin string
	Config AuthEmailWorkerConfig
	Logger *slog.Logger
	Now    func() time.Time

	lastHousekeeping time.Time
}

// Run drives the worker until ctx is cancelled.
func (w *AuthEmailWorker) Run(ctx context.Context) {
	interval := w.Config.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil && w.Logger != nil {
			w.Logger.Warn("auth email worker pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce performs one lease/send pass plus due housekeeping.
func (w *AuthEmailWorker) RunOnce(ctx context.Context) error {
	now := w.now()

	expired, err := w.Store.ExpireAuthEmailLeases(ctx, now)
	if err != nil {
		return err
	}
	for _, intent := range expired {
		if err := w.Store.AppendSecurityEvent(ctx, SecurityEvent{
			Event:     EventAuthEmailLeaseExpired,
			Outcome:   OutcomeUnknown,
			Detail:    map[string]any{"purpose": intent.Purpose},
			CreatedAt: now,
		}); err != nil && w.Logger != nil {
			w.Logger.Warn("recording expired email lease failed", "error", err)
		}
	}

	claimSize := w.Config.BatchSize
	if claimSize <= 0 {
		claimSize = 20
	}
	leaseTTL := w.Config.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = 5 * time.Minute
	}
	intents, err := w.Store.ClaimAuthEmails(ctx, w.owner(), claimSize, leaseTTL, now)
	if err != nil {
		return err
	}
	for _, intent := range intents {
		w.deliver(ctx, intent)
	}

	if interval := w.Config.HousekeepingInterval; interval > 0 && w.now().Sub(w.lastHousekeeping) >= interval {
		w.lastHousekeeping = w.now()
		w.housekeeping(ctx)
	}
	return nil
}

func (w *AuthEmailWorker) deliver(ctx context.Context, intent AuthEmailIntent) {
	now := w.now()
	claimed, raw, err := w.Store.CreateAuthLinkForIntent(ctx, intent.OutboxID, now)
	if err != nil {
		w.fail(ctx, intent.OutboxID, "link_creation_failed", now)
		if w.Logger != nil {
			w.Logger.Warn("creating auth link failed", "error", err, "intent", intent.OutboxID)
		}
		return
	}
	intent = claimed

	link, err := AuthLinkURL(w.Origin, intent.Purpose, raw)
	if err != nil {
		w.fail(ctx, intent.OutboxID, "link_render_failed", now)
		return
	}
	subject, text, htmlBody, err := RenderAuthEmail(intent.Purpose, intent.Locale, link, intent.RecipientEmail)
	if err != nil {
		w.fail(ctx, intent.OutboxID, "template_failed", now)
		return
	}

	result := w.Sender.Send(ctx, AuthEmailMessage{
		To:      intent.RecipientEmail,
		Subject: subject,
		Text:    text,
		HTML:    htmlBody,
	})
	switch result.Outcome {
	case SendOutcomeAccepted:
		if err := w.Store.MarkAuthEmailAccepted(ctx, intent.OutboxID, now); err != nil && w.Logger != nil {
			w.Logger.Warn("marking email accepted failed", "error", err)
		}
	case SendOutcomeUnknown:
		if err := w.Store.MarkAuthEmailOutcomeUnknown(ctx, intent.OutboxID, result.ErrorClass, now); err != nil && w.Logger != nil {
			w.Logger.Warn("marking email outcome unknown failed", "error", err)
		}
	default:
		maxAttempts := w.Config.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 5
		}
		if err := w.Store.ReleaseAuthEmailRetry(ctx, intent.OutboxID, result.ErrorClass, maxAttempts, now); err != nil && w.Logger != nil {
			w.Logger.Warn("releasing email retry failed", "error", err)
		}
	}
}

// fail records a definite pre-acceptance failure for an intent.
func (w *AuthEmailWorker) fail(ctx context.Context, outboxID, errorClass string, now time.Time) {
	if err := w.Store.MarkAuthEmailFailed(ctx, outboxID, errorClass, now); err != nil && w.Logger != nil {
		w.Logger.Warn("marking email failed", "error", err, "intent", outboxID)
	}
}

func (w *AuthEmailWorker) housekeeping(ctx context.Context) {
	now := w.now()
	if _, err := w.Store.ExpireAccessRequests(ctx, now); err != nil && w.Logger != nil {
		w.Logger.Warn("expiring access requests failed", "error", err)
	}
	if _, err := w.Store.PurgeAccessRequests(ctx, now); err != nil && w.Logger != nil {
		w.Logger.Warn("purging access requests failed", "error", err)
	}
	if _, err := w.Store.PurgeSessionNetworkEvidence(ctx, now.Add(-30*24*time.Hour)); err != nil && w.Logger != nil {
		w.Logger.Warn("purging session network evidence failed", "error", err)
	}
	if _, err := w.Store.PruneAuthThrottle(ctx, now.Add(-24*time.Hour), now); err != nil && w.Logger != nil {
		w.Logger.Warn("pruning throttle counters failed", "error", err)
	}
	if _, err := w.Store.PurgeSecurityEventNetwork(ctx, now.Add(-30*24*time.Hour)); err != nil && w.Logger != nil {
		w.Logger.Warn("purging security network evidence failed", "error", err)
	}
	if _, err := w.Store.PurgeSecurityEvents(ctx, now.Add(-180*24*time.Hour)); err != nil && w.Logger != nil {
		w.Logger.Warn("purging security events failed", "error", err)
	}
	if _, err := w.Store.PurgeAuthEmailState(ctx, now.Add(-30*24*time.Hour)); err != nil && w.Logger != nil {
		w.Logger.Warn("purging auth email state failed", "error", err)
	}
}

func (w *AuthEmailWorker) owner() string {
	if w.Config.Owner != "" {
		return w.Config.Owner
	}
	return "auth-email-worker"
}

func (w *AuthEmailWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}
