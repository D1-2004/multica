ALTER TABLE agent_dingtalk_identity
    ADD COLUMN account_open_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN account_corp_id TEXT NOT NULL DEFAULT '';

UPDATE agent_dingtalk_identity
SET account_open_id = dws_uid;

ALTER TABLE agent_dingtalk_identity
    ALTER COLUMN account_open_id DROP DEFAULT,
    ALTER COLUMN account_corp_id DROP DEFAULT;

ALTER TABLE agent_dingtalk_identity_attempt
    DROP CONSTRAINT agent_dingtalk_identity_attempt_completed_uid_check,
    ADD COLUMN completed_open_id TEXT,
    ADD COLUMN completed_corp_id TEXT;

UPDATE agent_dingtalk_identity_attempt
SET completed_open_id = completed_uid,
    completed_corp_id = ''
WHERE used_at IS NOT NULL;

ALTER TABLE agent_dingtalk_identity_attempt
    DROP COLUMN completed_uid,
    ADD CONSTRAINT agent_dingtalk_identity_attempt_completed_open_id_check
        CHECK ((used_at IS NULL) = (completed_open_id IS NULL)),
    ADD CONSTRAINT agent_dingtalk_identity_attempt_completed_corp_id_check
        CHECK ((used_at IS NULL) = (completed_corp_id IS NULL));
