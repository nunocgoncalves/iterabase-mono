-- HOR-538: one durable installation-wide hysteresis state for the dedicated
-- AgentPool workspace filesystem. Every pool is backed by this one filesystem;
-- dispatch serializes observations here so worker/pool/process replacement
-- cannot reopen fresh credit inside the 20-25 percent band.
CREATE TABLE runtime.workspace_capacity_state (
    singleton       boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    observed        boolean NOT NULL DEFAULT false,
    free_bytes      bigint NOT NULL DEFAULT 0 CHECK (free_bytes >= 0),
    capacity_bytes  bigint NOT NULL DEFAULT 0 CHECK (capacity_bytes >= 0),
    free_ratio      double precision NOT NULL DEFAULT 0 CHECK (free_ratio >= 0 AND free_ratio <= 1),
    warning         boolean NOT NULL DEFAULT true,
    credit_gated    boolean NOT NULL DEFAULT true,
    observed_at     timestamptz
);

INSERT INTO runtime.workspace_capacity_state (singleton)
VALUES (true);
