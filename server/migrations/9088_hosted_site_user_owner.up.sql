ALTER TABLE hosted_site
    ADD COLUMN IF NOT EXISTS owner_user_id UUID;

UPDATE hosted_site site
SET owner_user_id = COALESCE(
    (SELECT agent.owner_id FROM agent WHERE agent.id = site.owner_agent_id),
    (
        SELECT member.user_id
        FROM member
        WHERE member.workspace_id = site.workspace_id AND member.role = 'owner'
        ORDER BY member.created_at
        LIMIT 1
    )
)
WHERE site.owner_user_id IS NULL;

ALTER TABLE hosted_site
    ALTER COLUMN workspace_id DROP NOT NULL,
    ALTER COLUMN owner_agent_id DROP NOT NULL;
