-- Opt-in cloud Chat provider-session resume. Off by default so existing
-- cloud Chat claims keep database history as the only continuity source.
-- When on, a warm 1:1 sandbox may --resume the prior Claude/Codex session
-- if the last completed answer is within 20 minutes and the agent's
-- instructions, skills, and runtime have not changed.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS chat_session_resume BOOLEAN NOT NULL DEFAULT false;

-- Snapshot of the agent identity the last claimed turn actually ran with.
-- The next claim compares the current identity to this value; a mismatch
-- (instructions, skills, or runtime) refuses --resume even on a warm sandbox.
ALTER TABLE chat_session
ADD COLUMN IF NOT EXISTS resume_identity TEXT NOT NULL DEFAULT '';
