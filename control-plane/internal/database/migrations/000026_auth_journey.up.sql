-- HOR-453: durable V2 local access-request, verification, password, auth-email,
-- browser-session, recent-auth, throttling, and security-audit state.
--
-- Authority: docs/architecture/v2-authentication-authority.md (HOR-451,
-- DES-HOR-451-01..14). This migration adds the browser-authentication state
-- only. The irreversible `identity.authority_state` epoch, the API-key
-- owner/actor/action substrate, and the gateway credential projection remain
-- HOR-454; no competing customer authority is introduced here.

-- ---------------------------------------------------------------------------
-- Local users: V2 account state, locale, canonical email, lifecycle evidence.
-- ---------------------------------------------------------------------------

-- Legacy `user` is the pre-epoch spelling of `operator` (DES-HOR-451-12). The
-- data migration is intentionally idempotent so a fresh install and an upgrade
-- converge on the same rows. The old constraint must be dropped before the
-- value rewrite and re-added afterwards so both directions validate existing
-- rows.
ALTER TABLE identity.local_users DROP CONSTRAINT local_users_role_check;
UPDATE identity.local_users SET role = 'operator' WHERE role = 'user';
ALTER TABLE identity.local_users ADD CONSTRAINT local_users_role_check
    CHECK (role IN ('admin', 'operator'));
ALTER TABLE identity.local_users ALTER COLUMN role SET DEFAULT 'operator';

ALTER TABLE identity.local_users
    ADD COLUMN email_normalized text,
    ADD COLUMN display_name text NOT NULL DEFAULT '',
    ADD COLUMN locale text NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'pt')),
    ADD COLUMN status text NOT NULL DEFAULT 'setup_pending'
        CHECK (status IN ('setup_pending', 'active', 'disabled')),
    ADD COLUMN password_changed_at timestamptz,
    ADD COLUMN role_changed_at timestamptz,
    ADD COLUMN role_changed_by uuid REFERENCES identity.identities(id),
    ADD COLUMN account_changed_at timestamptz,
    ADD COLUMN account_changed_by uuid REFERENCES identity.identities(id),
    ADD COLUMN approved_access_request_id uuid;

UPDATE identity.local_users SET email_normalized = lower(btrim(email));
ALTER TABLE identity.local_users ALTER COLUMN email_normalized SET NOT NULL;
CREATE UNIQUE INDEX local_users_email_normalized_key
    ON identity.local_users (email_normalized);

-- Existing humans have no local password, so they complete email setup before
-- the first browser sign-in (architecture section 15.4). A pre-existing
-- password hash, if any, keeps the account active.
UPDATE identity.local_users
SET status = CASE WHEN password_hash IS NULL THEN 'setup_pending' ELSE 'active' END;

-- ---------------------------------------------------------------------------
-- Access requests (architecture 6.1, 7.3)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.access_requests (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email                  text NOT NULL,               -- original validated delivery email
    email_normalized       text NOT NULL,
    locale                 text NOT NULL CHECK (locale IN ('en', 'pt')),
    state                  text NOT NULL CHECK (state IN (
                               'verification_pending', 'approval_pending',
                               'approved', 'declined', 'expired')),
    terminal_reason        text CHECK (terminal_reason IN (
                               'approved', 'declined', 'verification_expired', 'purged')),
    verified_at            timestamptz,
    reviewed_by_identity_id uuid REFERENCES identity.identities(id),
    reviewed_at            timestamptz,
    approved_identity_id   uuid REFERENCES identity.identities(id),
    approved_role          text CHECK (approved_role IN ('admin', 'operator')),
    purge_at               timestamptz NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

-- One non-terminal request per canonical normalized email.
CREATE UNIQUE INDEX access_requests_non_terminal_email
    ON identity.access_requests (email_normalized)
    WHERE state IN ('verification_pending', 'approval_pending');

CREATE INDEX access_requests_pending
    ON identity.access_requests (verified_at)
    WHERE state = 'approval_pending';

CREATE INDEX access_requests_purge
    ON identity.access_requests (purge_at);

CREATE TRIGGER access_requests_updated BEFORE UPDATE ON identity.access_requests
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

ALTER TABLE identity.local_users
    ADD CONSTRAINT local_users_approved_request_fk
    FOREIGN KEY (approved_access_request_id) REFERENCES identity.access_requests(id);

-- ---------------------------------------------------------------------------
-- Transactional authentication email outbox (architecture 7.5)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.auth_email_outbox (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purpose               text NOT NULL CHECK (purpose IN (
                              'verify_access', 'setup_password', 'reset_password')),
    recipient_email       text NOT NULL,
    recipient_identity_id uuid REFERENCES identity.identities(id),
    access_request_id     uuid REFERENCES identity.access_requests(id) ON DELETE CASCADE,
    locale                text NOT NULL CHECK (locale IN ('en', 'pt')),
    state                 text NOT NULL DEFAULT 'pending' CHECK (state IN (
                              'pending', 'leased', 'accepted', 'outcome_unknown', 'failed')),
    attempts              integer NOT NULL DEFAULT 0,
    lease_owner           text,
    leased_until          timestamptz,
    accepted_at           timestamptz,
    outcome_unknown_at    timestamptz,
    terminal_at           timestamptz,
    last_error_class      text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX auth_email_outbox_claim
    ON identity.auth_email_outbox (state, created_at);

CREATE TRIGGER auth_email_outbox_updated BEFORE UPDATE ON identity.auth_email_outbox
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

-- ---------------------------------------------------------------------------
-- One-time link tokens (architecture 7.4)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.auth_link_tokens (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash        text NOT NULL UNIQUE,             -- domain-separated SHA-256 hex
    purpose           text NOT NULL CHECK (purpose IN (
                          'verify_access', 'setup_password', 'reset_password')),
    access_request_id uuid REFERENCES identity.access_requests(id) ON DELETE CASCADE,
    identity_id       uuid REFERENCES identity.identities(id) ON DELETE CASCADE,
    outbox_id         uuid REFERENCES identity.auth_email_outbox(id) ON DELETE SET NULL,
    generation        integer NOT NULL DEFAULT 1,
    issued_at         timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    consumed_at       timestamptz,
    invalidated_at    timestamptz,
    invalidated_reason text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_link_tokens_subject CHECK (
        (purpose = 'verify_access' AND access_request_id IS NOT NULL AND identity_id IS NULL)
        OR (purpose <> 'verify_access' AND identity_id IS NOT NULL)
    )
);

CREATE INDEX auth_link_tokens_request
    ON identity.auth_link_tokens (access_request_id, purpose)
    WHERE consumed_at IS NULL AND invalidated_at IS NULL;

CREATE INDEX auth_link_tokens_identity
    ON identity.auth_link_tokens (identity_id, purpose)
    WHERE consumed_at IS NULL AND invalidated_at IS NULL;

-- ---------------------------------------------------------------------------
-- Opaque browser sessions (architecture 5.2-5.3, 6.4, 7.6)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.browser_sessions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash          text NOT NULL UNIQUE,           -- domain-separated SHA-256 hex
    identity_id         uuid NOT NULL REFERENCES identity.identities(id) ON DELETE CASCADE,
    csrf_hash           text NOT NULL,                  -- session-bound proof (hash only)
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_authorized_at  timestamptz NOT NULL DEFAULT now(),
    idle_expires_at     timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    recent_password_at  timestamptz,
    terminated_at       timestamptz,
    termination_reason  text CHECK (termination_reason IN (
                            'revoked', 'idle_expired', 'absolute_expired',
                            'account_revoked', 'password_revoked', 'role_revoked')),
    created_ip          inet,                           -- server-only; bounded retention
    last_ip             inet,                           -- server-only; bounded retention
    location_country    text,                           -- advisory coarse region only
    location_region     text,
    browser_label       text,                           -- normalized labels only, no raw UA
    os_label            text,
    device_label        text,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX browser_sessions_identity
    ON identity.browser_sessions (identity_id)
    WHERE terminated_at IS NULL;

CREATE INDEX browser_sessions_network_retention
    ON identity.browser_sessions (terminated_at)
    WHERE terminated_at IS NOT NULL AND (created_ip IS NOT NULL OR last_ip IS NOT NULL);

CREATE TRIGGER browser_sessions_updated BEFORE UPDATE ON identity.browser_sessions
    FOR EACH ROW EXECUTE FUNCTION identity.set_updated_at();

-- ---------------------------------------------------------------------------
-- Append-only identity security audit (architecture 7.9-7.10, 11)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.security_events (
    id                           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event                        text NOT NULL,
    outcome                      text NOT NULL CHECK (outcome IN ('success', 'failure', 'denied', 'unknown')),
    reason                       text,
    initiating_human_identity_id uuid,
    request_actor_identity_id    uuid,
    actor_role                   text,
    subject_identity_id          uuid,
    access_request_id            uuid,
    browser_session_id           uuid,
    api_key_id                   uuid,
    key_owner_identity_id        uuid,
    credential_kind              text,
    request_id                   text,
    correlation_id               text,
    detail                       jsonb NOT NULL DEFAULT '{}'::jsonb,  -- secret-free, bounded
    created_at                   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX security_events_created ON identity.security_events (created_at);
CREATE INDEX security_events_subject ON identity.security_events (subject_identity_id, created_at);
CREATE INDEX security_events_request ON identity.security_events (access_request_id, created_at);

-- Independently purgeable network evidence, one row per core event.
CREATE TABLE identity.security_event_network (
    security_event_id uuid PRIMARY KEY REFERENCES identity.security_events(id) ON DELETE CASCADE,
    source_ip         inet,
    location_country  text,
    location_region   text,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX security_event_network_created ON identity.security_event_network (created_at);

-- ---------------------------------------------------------------------------
-- Persistent bounded throttling (architecture 4.3, 5.1)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.auth_throttle_counters (
    scope             text NOT NULL,
    subject_hash      text NOT NULL,                    -- HMAC/SHA-256 of source or account
    window_started_at timestamptz NOT NULL DEFAULT now(),
    attempts          integer NOT NULL DEFAULT 0,
    blocked_until     timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, subject_hash)
);

CREATE INDEX auth_throttle_blocked
    ON identity.auth_throttle_counters (blocked_until)
    WHERE blocked_until IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Bootstrap/recovery marker (architecture 12, DES-HOR-451-11)
-- ---------------------------------------------------------------------------

CREATE TABLE identity.bootstrap_marker (
    id            boolean PRIMARY KEY DEFAULT true CHECK (id),
    admin_email   text NOT NULL,
    bootstrapped_at timestamptz NOT NULL DEFAULT now()
);
