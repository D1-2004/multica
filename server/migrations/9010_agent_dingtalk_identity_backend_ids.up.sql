ALTER TABLE agent_dingtalk_identity_attempt
    ADD COLUMN completed_uid TEXT;

UPDATE agent_dingtalk_identity_attempt attempt
SET completed_uid = identity.dws_uid
FROM agent_dingtalk_identity identity
WHERE attempt.used_at IS NOT NULL
  AND attempt.agent_id = identity.agent_id
  AND attempt.workspace_id = identity.workspace_id;

DELETE FROM agent_dingtalk_identity_attempt
WHERE used_at IS NOT NULL
  AND completed_uid IS NULL;

ALTER TABLE agent_dingtalk_identity_attempt
    DROP COLUMN completed_open_id,
    DROP COLUMN completed_corp_id,
    ADD CONSTRAINT agent_dingtalk_identity_attempt_completed_uid_check
        CHECK ((used_at IS NULL) = (completed_uid IS NULL));

ALTER TABLE agent_dingtalk_identity
    DROP COLUMN account_open_id,
    DROP COLUMN account_corp_id;
