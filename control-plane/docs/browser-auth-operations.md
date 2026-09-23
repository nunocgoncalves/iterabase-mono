# Browser authentication operations (HOR-453)

- **Status:** implemented; deployed enablement is opt-in per installation
- **Authority:** [`docs/architecture/v2-authentication-authority.md`](../../docs/architecture/v2-authentication-authority.md) (HOR-451, `DES-HOR-451-01`–`14`)
- **Component:** `control-plane` (`cmd/api` `serve`/`bootstrap`), `charts/charts/control-plane`
- **Ticket:** [HOR-453](https://linear.app/horizonshift/issue/HOR-453/v2-deliver-local-access-request-verification-password-and-browser)

This runbook covers the V2 local access-request, verification, password, and
browser-session journey. People administration beyond the approval handoff,
the irreversible authority epoch, API-key Settings, and gateway enforcement are
owned by HOR-454, HOR-514, and HOR-513.

## Enablement

Browser authentication is disabled by default. Pre-V2 installs keep the legacy
API-key bootstrap and every `/v1/auth/*` route returns the customer-safe
`auth_unavailable` state. To enable it, the deployment operator supplies the
following through repository chart values and Secrets:

| Input | Chart value | Notes |
| --- | --- | --- |
| Exact public browser origin | `auth.publicOrigin` | Absolute HTTPS origin, no path. Used for email links and exact-origin CSRF checks. |
| Transactional SMTP | `auth.email.*` + `auth.email.existingSecret` | Verified TLS only (`starttls` or `tls`); `mode: plain` fails chart render and startup validation. |
| First Admin | `auth.bootstrap.adminEmail`, `auth.bootstrap.adminLocale`, `auth.bootstrap.existingSecret` | Read from a Secret; never printed. |
| Trusted proxies | `auth.trustedProxies` | Exact CIDRs. The direct peer is trusted otherwise. |
| Advisory GeoIP | `auth.geoip.existingClaim` / `auth.geoip.databasePath` | Optional local database; failures only remove location labels. |
| Session bounds | `auth.session.idleTTL` / `absoluteTTL` / `recentAuthTTL` | May only tighten the approved 12 h / 30 d / 15 min maximums. |

Startup (`runServe` → `ValidateAuthServe`) and the bootstrap init container fail
closed when an enabled surface is missing a valid origin, SMTP transport,
trusted-proxy CIDR, or first-Admin address. A disabled surface continues to
serve health/readiness and the legacy API independently.

## First Admin bootstrap

`api bootstrap` runs as the RBAC-less init container with an advisory lock:

1. Reads the first-Admin email/locale from the mounted Secret.
2. Only on a completely empty install (`identity.local_users` and
   `identity.bootstrap_marker` empty) it atomically creates one `setup_pending`
   Admin, queues a normal setup email, records the marker, and appends audit.
3. It never creates or prints an API key, setup token, or password.
4. A consistent restart is a strict no-op. A marker with no local users fails
   closed (`ErrBootstrapInconsistent`).

The Admin completes first-time setup from the emailed link, which activates the
account without creating a browser session; sign-in is explicit.

## Recovery

`api bootstrap --recover-admin --admin-email <email>` is the cluster-operator
path and succeeds only while **no active Admin exists**:

- Creates or converts the named human to `setup_pending` Admin.
- Clears the old password, revokes every browser session and legacy API key
  owned by that identity, invalidates outstanding setup/reset links, queues a
  normal setup email, and audits (`recover_admin_completed`).
- Product mutations must never leave zero active Admins; this command is the
  only recovery path once no Admin can sign in.

## Revocation and retention

| Action | Effect |
| --- | --- |
| Sign out this device | Terminates the current session; the next authenticated request is denied. |
| Revoke one session | Own-session revocation, idempotent, no recent-password step. |
| Revoke all other sessions | Keeps the current session; API keys are unchanged. |
| Password reset completion | Replaces the password, consumes all reset links, revokes every browser session, keeps API keys. |
| Account disablement / role change (HOR-454) | Revokes sessions through the same state machine. |

Retention: raw session IP is server-only and purged no later than 30 days after
termination; access-request personal data is purged 180 days after a terminal
state; `identity.security_events` is append-only. Network evidence is stored
separately in `identity.security_event_network` so it can be purged
independently.

## Failure semantics

| Failure | Behavior |
| --- | --- |
| Database unavailable | Browser and session authorization denies; no stale authority fallback. |
| SMTP unavailable before acceptance | Durable outbox retry with bounded attempts; readiness stays honest. |
| SMTP acceptance ambiguous | Recorded as `outcome_unknown`; no automatic resend, explicit resend rotates the link. |
| Audit insert fails | The security mutation rolls back. |
| GeoIP/User-Agent parsing fails | Authentication succeeds without location/client labels. |
| Malformed trusted-proxy chain | Falls back to the direct peer. |
| Role/account changes race a request | The transactionally resolved current authority controls that request. |

## Observability

Security evidence lives in `identity.security_events` (core, 180 days) and
`identity.security_event_network` (bounded network evidence, ≤ 30 days).
Operational diagnosis should use those bounded rows; never query or log raw
passwords, one-time tokens, session tokens, or SMTP credentials.
