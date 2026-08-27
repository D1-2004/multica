ALTER TABLE chat_session
DROP COLUMN IF EXISTS resume_identity;

ALTER TABLE agent
DROP COLUMN IF EXISTS chat_session_resume;
