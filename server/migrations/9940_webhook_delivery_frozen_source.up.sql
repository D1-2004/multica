-- Frozen source of a webhook delivery, written by the ingress in the same
-- transaction as the delivery row:
--   source_digest            sha256 of the effective payload (event plus the
--                            canonical event payload). Decides duplicate vs
--                            conflict for a reused event id, and lets a
--                            worker retry prove it rebuilt the same input.
--   signing_secret_revision  fingerprint of the signing secret that verified
--                            the request (NULL when no signature is required);
--                            never the secret itself.
--   source_binding           the endpoint binding read from PostgreSQL at
--                            acceptance (workspace, autopilot, assignee,
--                            routine scene and tenant, creator, dispatch,
--                            signature and payload policy). Request fields
--                            never contribute to it.
-- Nullable and additive: rows written by older binaries have none of them and
-- older binaries name their columns explicitly, so they ignore these.
ALTER TABLE webhook_delivery
    ADD COLUMN IF NOT EXISTS source_digest TEXT,
    ADD COLUMN IF NOT EXISTS signing_secret_revision TEXT,
    ADD COLUMN IF NOT EXISTS source_binding JSONB;
