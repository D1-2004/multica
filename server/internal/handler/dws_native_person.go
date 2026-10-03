package handler

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	dwsevents "github.com/multica-ai/multica/server/pkg/dws/events"
)

// A native IM event names its sender only by the openDingTalkId the
// receiving account sees, while a Router delivery carries the sender's
// staffId. So that one person has one key (contextcap.TriggerPersonKey)
// whichever way their message arrives, the sender's staffId is looked up
// before dispatch: the staffId kept for this account and openDingTalkId
// (dws_open_identity_staff), else the account's own address book
// (DWSNativeStaffID: searched by name, decided only by the exact
// openDingTalkId), which is then kept. A kept staffId is proved again after
// nativeStaffRevalidateAfter: one the address book no longer proves (the
// person left the org, the staffId was reassigned) is dropped. A sender the
// address book cannot prove (another org's member, an unlisted account) has
// no staffId and is keyed by the openDingTalkId; the miss is retried after a
// while.

const (
	nativeStaffTimeout         = 3 * time.Second
	nativeStaffRevalidateAfter = 7 * 24 * time.Hour
	nativeStaffMissTTL         = 10 * time.Minute
	nativeStaffFailureTTL      = time.Minute
	nativeStaffSourceKept      = "kept"
	nativeStaffSourceLookup    = "lookup"
)

var nativeStaffMisses = &nativeTTLCache{entries: map[string]nativeTTLEntry{}}

// nativeSenderStaffID returns the staffId of a native message's sender as
// id's org knows them, "" when it cannot be proved. groupConversationID is
// the group of a group event ("" for a single chat), whose member nick also
// narrows the address book search. Failures are logged; the message is then
// dispatched with the sender's openDingTalkId only.
func (h *Handler) nativeSenderStaffID(ctx context.Context, id dwsclient.Identity, m *dwsevents.MessageEvent, groupConversationID string) string {
	openID := strings.TrimSpace(m.SenderOpenDingTalkID)
	if openID == "" || strings.EqualFold(openID, "null") {
		return ""
	}
	kept := ""
	if h.DB != nil {
		staff, resolvedAt, err := contextcap.LookupOpenIDStaff(ctx, h.DB, id.OrgID, id.UID, openID)
		switch {
		case err != nil:
			slog.Warn("DWS native sender staffId unavailable", "event", "dws_native_sender_staff_failed",
				"agent_id", id.AgentID, "stage", "kept", "error", err)
		case staff != "" && nativeClock().Sub(resolvedAt) < nativeStaffRevalidateAfter:
			logNativeSenderStaff(id, nativeStaffSourceKept, true)
			return staff
		default:
			// Missing, or due to be proved again.
			kept = staff
		}
	}
	lookup := h.DWSNativeStaffID
	if lookup == nil {
		return kept
	}
	key := strings.Join([]string{id.OrgID, id.UID, openID}, "\x00")
	if _, missed := nativeStaffMisses.get(key, nativeClock()); missed {
		return kept
	}
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nativeStaffTimeout)
	defer cancel()
	staff, err := lookup(lookupCtx, id, openID, []string{nativeDisplayName(m.Sender)}, strings.TrimSpace(groupConversationID))
	if err != nil {
		// A failed read proves nothing either way: a kept staffId stands.
		slog.Warn("DWS native sender staffId unavailable", "event", "dws_native_sender_staff_failed",
			"agent_id", id.AgentID, "stage", "lookup", "error", err)
		nativeStaffMisses.put(key, "", nativeStaffFailureTTL, nativeClock())
		return kept
	}
	staff = strings.TrimSpace(staff)
	if staff == "" || contextcap.TriggerPersonKey(staff, "") != staff {
		logNativeSenderStaff(id, nativeStaffSourceLookup, false)
		if kept != "" && h.DB != nil {
			if err := contextcap.ForgetOpenIDStaff(ctx, h.DB, id.OrgID, id.UID, openID); err != nil {
				slog.Warn("DWS native sender staffId not dropped", "event", "dws_native_sender_staff_failed",
					"agent_id", id.AgentID, "stage", "forget", "error", err)
			}
		}
		nativeStaffMisses.put(key, "", nativeStaffMissTTL, nativeClock())
		return ""
	}
	if h.DB != nil {
		if err := contextcap.RememberOpenIDStaff(ctx, h.DB, id.OrgID, id.UID, openID, staff); err != nil {
			slog.Warn("DWS native sender staffId not kept", "event", "dws_native_sender_staff_failed",
				"agent_id", id.AgentID, "stage", "keep", "error", err)
		}
	}
	logNativeSenderStaff(id, nativeStaffSourceLookup, true)
	return staff
}

func logNativeSenderStaff(id dwsclient.Identity, source string, found bool) {
	slog.Info("DWS native sender staffId", "event", "dws_native_sender_staff",
		"agent_id", id.AgentID, "source", source, "found", found)
}
