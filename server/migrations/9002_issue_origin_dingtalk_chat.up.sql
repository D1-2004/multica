-- Fork-owned: re-assert dingtalk_chat on issue.origin_type.
--
-- The DingTalk inbound channel creates issues from the /issue chat command with
-- origin_type='dingtalk_chat' (originally fork migration 132). Upstream's 149
-- rebuilds issue_origin_type_check from its own label set, which does not know
-- about dingtalk_chat and would therefore drop it — and, with dingtalk_chat rows
-- already in the table, 149's ADD CONSTRAINT would fail outright (SQLSTATE
-- 23514) and abort the migration run at boot.
--
-- Running after 149 restores the fork label on top of upstream's list. Keeping
-- this in a fork-owned file (rather than editing 149) means a future upstream
-- sync cannot silently drop the requirement: upstream never touches this file,
-- and any new upstream migration that rebuilds the constraint will conflict
-- loudly here instead of regressing DingTalk issue creation.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat'));
