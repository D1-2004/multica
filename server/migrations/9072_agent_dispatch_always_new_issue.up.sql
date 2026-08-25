-- Opt out of conversational Issue threading for Agent Dispatch V2 channel
-- messages. false (the default) keeps today's behavior: the Router replays an
-- issue continuation and Multica appends a follow-up comment to that Issue.
-- true makes every inbound channel message its own Issue; the Router still
-- persists and replays the continuation, Multica ignores it.
ALTER TABLE agent
ADD COLUMN IF NOT EXISTS dispatch_always_new_issue BOOLEAN NOT NULL DEFAULT false;
