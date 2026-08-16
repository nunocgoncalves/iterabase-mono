-- HOR-489: intentionally preserve the released workload authorization floor.
--
-- OPO1 already required and held these exact grants before migration 23 was
-- added to source authority. Revoking them during a manual schema downgrade
-- would break the already-released supervisor workload-mTLS path, while normal
-- Helm rollback leaves forward-compatible database migrations in place.
-- Reapplying migration 23 is idempotent.
SELECT 1;
