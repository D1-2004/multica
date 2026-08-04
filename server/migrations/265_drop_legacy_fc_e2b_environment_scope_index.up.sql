-- The retired execution-environment rollout created this narrower unique
-- index. Current cloud sandbox sessions are keyed by Runtime, logical scope,
-- backend, and identity fingerprint. Keeping the legacy index rejects the
-- valid transition between an employee-bound ASB session and an unbound ASB
-- session for the same chat scope before the current upsert key can run.
DROP INDEX IF EXISTS uq_fc_e2b_sandbox_session_environment_scope;
