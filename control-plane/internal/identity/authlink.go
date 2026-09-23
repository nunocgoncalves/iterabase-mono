package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Auth-link purposes (architecture 7.4).
const (
	AuthLinkVerifyAccess  = "verify_access"
	AuthLinkSetupPassword = "setup_password"
	AuthLinkResetPassword = "reset_password"
)

// Approved one-time-link lifetimes (architecture 6).
const (
	VerifyAccessLinkTTL  = 24 * time.Hour
	SetupPasswordLinkTTL = 7 * 24 * time.Hour
	ResetPasswordLinkTTL = 30 * time.Minute
)

// Auth-link sentinel errors. The presented token is itself the secret, so the
// journey may disclose which bounded state applies to it.
var (
	// ErrAuthLinkInvalid is returned for an unknown or malformed token.
	ErrAuthLinkInvalid = errors.New("identity: auth link invalid")
	// ErrAuthLinkExpired is returned for a token past its lifetime.
	ErrAuthLinkExpired = errors.New("identity: auth link expired")
	// ErrAuthLinkSuperseded is returned for a token invalidated by a resend.
	ErrAuthLinkSuperseded = errors.New("identity: auth link superseded")
	// ErrAuthLinkConsumed is returned when a one-time link was already used.
	ErrAuthLinkConsumed = errors.New("identity: auth link already used")
)

// AuthLinkTTL returns the approved lifetime for a purpose.
func AuthLinkTTL(purpose string) time.Duration {
	switch purpose {
	case AuthLinkVerifyAccess:
		return VerifyAccessLinkTTL
	case AuthLinkSetupPassword:
		return SetupPasswordLinkTTL
	case AuthLinkResetPassword:
		return ResetPasswordLinkTTL
	default:
		return 0
	}
}

// AuthLinkToken is one persisted one-time link (hash only).
type AuthLinkToken struct {
	ID              string
	Purpose         string
	AccessRequestID string
	IdentityID      string
	OutboxID        string
	Generation      int
	ExpiresAt       time.Time
	ConsumedAt      *time.Time
	InvalidatedAt   *time.Time
}

// InvalidateAuthLinksTx invalidates every unused token of the purpose and
// subject. Resend always rotates the usable link.
func InvalidateAuthLinksTx(ctx context.Context, tx pgx.Tx, purpose, accessRequestID, identityID string, now time.Time, reason string) error {
	_, err := tx.Exec(ctx, `
		UPDATE identity.auth_link_tokens
		SET invalidated_at = $4, invalidated_reason = $5
		WHERE purpose = $1
		  AND consumed_at IS NULL AND invalidated_at IS NULL
		  AND (($2 <> '' AND access_request_id = NULLIF($2,'')::uuid) OR ($3 <> '' AND identity_id = NULLIF($3,'')::uuid))`,
		purpose, accessRequestID, identityID, now.UTC(), reason)
	if err != nil {
		return fmt.Errorf("invalidate auth links: %w", err)
	}
	return nil
}

// createAuthLinkTx issues one new one-time link for an outbox intent, rolling
// the previous unused link for the same purpose and subject. The raw token is
// returned exactly once; only its domain-separated hash is persisted.
func createAuthLinkTx(ctx context.Context, tx pgx.Tx, intent AuthEmailIntent, now time.Time) (string, time.Time, error) {
	ttl := AuthLinkTTL(intent.Purpose)
	if ttl <= 0 {
		return "", time.Time{}, fmt.Errorf("unknown auth link purpose %q", intent.Purpose)
	}
	if err := InvalidateAuthLinksTx(ctx, tx, intent.Purpose, intent.AccessRequestID, intent.RecipientIdentityID, now, "superseded"); err != nil {
		return "", time.Time{}, err
	}

	raw, hash, err := GenerateSecret(SecretDomainAuthLink)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := now.UTC().Add(ttl)

	var generation int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(generation), 0) + 1
		FROM identity.auth_link_tokens
		WHERE purpose = $1
		  AND (($2 <> '' AND access_request_id = NULLIF($2,'')::uuid) OR ($3 <> '' AND identity_id = NULLIF($3,'')::uuid))`,
		intent.Purpose, intent.AccessRequestID, intent.RecipientIdentityID).Scan(&generation); err != nil {
		return "", time.Time{}, fmt.Errorf("next auth link generation: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.auth_link_tokens (
			token_hash, purpose, access_request_id, identity_id, outbox_id, generation, issued_at, expires_at)
		VALUES ($1, $2, NULLIF($3,'')::uuid, NULLIF($4,'')::uuid, NULLIF($5,'')::uuid, $6, $7, $8)`,
		hash, intent.Purpose, intent.AccessRequestID, intent.RecipientIdentityID,
		intent.OutboxID, generation, now.UTC(), expiresAt); err != nil {
		return "", time.Time{}, fmt.Errorf("insert auth link: %w", err)
	}
	return raw, expiresAt, nil
}

func scanAuthLink(row pgx.Row) (AuthLinkToken, error) {
	var token AuthLinkToken
	var accessRequestID, identityID, outboxID *string
	err := row.Scan(&token.ID, &token.Purpose, &accessRequestID, &identityID, &outboxID,
		&token.Generation, &token.ExpiresAt, &token.ConsumedAt, &token.InvalidatedAt)
	if err != nil {
		return AuthLinkToken{}, err
	}
	if accessRequestID != nil {
		token.AccessRequestID = *accessRequestID
	}
	if identityID != nil {
		token.IdentityID = *identityID
	}
	if outboxID != nil {
		token.OutboxID = *outboxID
	}
	return token, nil
}

// lockAuthLinkTx loads a token by hash and locks the row for the consuming
// transaction.
func lockAuthLinkTx(ctx context.Context, tx pgx.Tx, raw string) (AuthLinkToken, error) {
	token, err := scanAuthLink(tx.QueryRow(ctx, `
		SELECT id, purpose, access_request_id, identity_id, outbox_id, generation, expires_at, consumed_at, invalidated_at
		FROM identity.auth_link_tokens
		WHERE token_hash = $1
		FOR UPDATE`, HashSecret(SecretDomainAuthLink, raw)))
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthLinkToken{}, ErrAuthLinkInvalid
	}
	if err != nil {
		return AuthLinkToken{}, fmt.Errorf("lock auth link: %w", err)
	}
	return token, nil
}

// ConsumeAuthLinkTx marks a locked token consumed. Exactly one consumer wins.
func ConsumeAuthLinkTx(ctx context.Context, tx pgx.Tx, tokenID string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE identity.auth_link_tokens SET consumed_at = $2
		WHERE id = $1 AND consumed_at IS NULL AND invalidated_at IS NULL`, tokenID, now.UTC())
	if err != nil {
		return fmt.Errorf("consume auth link: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrAuthLinkInvalid
	}
	return nil
}
