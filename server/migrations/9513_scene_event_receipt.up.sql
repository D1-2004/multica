CREATE TABLE IF NOT EXISTS scene_event_receipt (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    tenant_org_id text NOT NULL,
    source text NOT NULL,
    source_event_id text NOT NULL,
    fingerprint text NOT NULL,
    envelope jsonb NOT NULL,
    scene_id uuid,
    route text NOT NULL,
    state text NOT NULL,
    reason text NOT NULL DEFAULT '',
    config_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scene_event_receipt_route_check CHECK (route IN ('legacy', 'unified')),
    CONSTRAINT scene_event_receipt_state_check CHECK (state IN ('legacy', 'ready', 'unmapped')),
    CONSTRAINT scene_event_receipt_scene_check CHECK (
        (route = 'legacy' AND state = 'legacy') OR
        (route = 'unified' AND state = 'ready' AND scene_id IS NOT NULL) OR
        (route = 'unified' AND state = 'unmapped' AND scene_id IS NULL))
);
