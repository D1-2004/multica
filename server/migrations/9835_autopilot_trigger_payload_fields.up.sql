-- Allowlist of the webhook payload a scene routine's Employee execution may
-- read: JSON pointers (RFC 6901) into the normalized envelope. NULL means the
-- default allowlist (/event and /eventPayload). Ordinary autopilots ignore it.
-- Nullable and additive: older binaries name their columns explicitly.
ALTER TABLE autopilot_trigger ADD COLUMN IF NOT EXISTS payload_fields JSONB;
