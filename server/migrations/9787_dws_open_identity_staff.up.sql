-- The staffId behind an openDingTalkId. An openDingTalkId is relative to the
-- DingTalk account that sees it (viewer_uid in org_id); a DWS native
-- subscription event names its sender only so. Once the sender's address
-- book entry proves the staffId (exact openDingTalkId match), it is kept
-- here, so the person keeps one key (contextcap.TriggerPersonKey) across
-- deliveries, lookup failures and the Router. No foreign keys by design.
CREATE TABLE IF NOT EXISTS dws_open_identity_staff (
    org_id text NOT NULL,
    viewer_uid text NOT NULL,
    open_dingtalk_id text NOT NULL,
    staff_id text NOT NULL,
    resolved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, viewer_uid, open_dingtalk_id)
);
