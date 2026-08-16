-- HOR-489: codify the inference gateway's least-privilege workload reads.
--
-- The dedicated `gateway` role predates the supervisor workload-mTLS path. Its
-- original grants cover direct-caller identity, permissions, and catalog
-- snapshots, but workload authentication also resolves the verified worker's
-- AgentPool and active durable turn. OPO1 already carries these exact grants;
-- keep this migration idempotent so that migration-22 production state and a
-- fresh installation converge without a manual role update.
--
-- The role is created by the bundled Postgres init script before migrations.
-- BYO/test databases may omit it, so retain the established conditional-grant
-- behavior from migration 000008.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gateway') THEN
        GRANT USAGE ON SCHEMA toolgateway, runtime TO gateway;
        GRANT SELECT
            ON toolgateway.pools,
               runtime.turns,
               runtime.workflow_runs,
               runtime.run_pool_assignments,
               runtime.turn_assignments
            TO gateway;
    END IF;
END $$;
