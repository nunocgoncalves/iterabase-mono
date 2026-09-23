-- HOR-453 down: remove the V2 browser-authentication state. The irreversible
-- identity authority epoch is HOR-454 and is not part of this migration.

DROP TABLE IF EXISTS identity.bootstrap_marker;
DROP TABLE IF EXISTS identity.auth_throttle_counters;
DROP TABLE IF EXISTS identity.security_event_network;
DROP TABLE IF EXISTS identity.security_events;
DROP TABLE IF EXISTS identity.browser_sessions;
DROP TABLE IF EXISTS identity.auth_link_tokens;
DROP TABLE IF EXISTS identity.auth_email_outbox;

ALTER TABLE identity.local_users
    DROP CONSTRAINT IF EXISTS local_users_approved_request_fk;

DROP TABLE IF EXISTS identity.access_requests;

ALTER TABLE identity.local_users
    DROP COLUMN IF EXISTS email_normalized,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS locale,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS password_changed_at,
    DROP COLUMN IF EXISTS role_changed_at,
    DROP COLUMN IF EXISTS role_changed_by,
    DROP COLUMN IF EXISTS account_changed_at,
    DROP COLUMN IF EXISTS account_changed_by,
    DROP COLUMN IF EXISTS approved_access_request_id;

DROP INDEX IF EXISTS identity.local_users_email_normalized_key;

ALTER TABLE identity.local_users DROP CONSTRAINT IF EXISTS local_users_role_check;
UPDATE identity.local_users SET role = 'user' WHERE role = 'operator';
ALTER TABLE identity.local_users ADD CONSTRAINT local_users_role_check
    CHECK (role IN ('admin', 'user'));
ALTER TABLE identity.local_users ALTER COLUMN role SET DEFAULT 'user';
