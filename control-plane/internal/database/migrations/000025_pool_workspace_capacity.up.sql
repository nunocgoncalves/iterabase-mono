-- DES-HOR-545-01: every AgentPool has a distinct OpenEBS XFS PVC, so its
-- durable 20/25 hysteresis state is keyed by the materialized pool rather than
-- shared installation-wide. Existing singleton state is intentionally dropped:
-- a clean/replaced claim must earn reopening from a fresh >=25% observation.
DROP TABLE runtime.workspace_capacity_state;

CREATE TABLE runtime.workspace_capacity_state (
    pool_id         uuid PRIMARY KEY REFERENCES toolgateway.pools(id) ON DELETE CASCADE,
    observed        boolean NOT NULL DEFAULT false,
    free_bytes      bigint NOT NULL DEFAULT 0 CHECK (free_bytes >= 0),
    capacity_bytes  bigint NOT NULL DEFAULT 0 CHECK (capacity_bytes >= 0),
    free_ratio      double precision NOT NULL DEFAULT 0 CHECK (free_ratio >= 0 AND free_ratio <= 1),
    warning         boolean NOT NULL DEFAULT true,
    credit_gated    boolean NOT NULL DEFAULT true,
    observed_at     timestamptz
);
